// Copyright 2022-2023, EthStorage.
// For license information, see https://github.com/ethstorage/es-node/blob/main/LICENSE

package protocol

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/detailyang/go-fallocate"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethstorage/go-ethstorage/ethstorage"
	prv "github.com/ethstorage/go-ethstorage/ethstorage/prover"
	"github.com/ethstorage/go-ethstorage/ethstorage/rollup"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	bhost "github.com/libp2p/go-libp2p/p2p/host/blank"
	swarmt "github.com/libp2p/go-libp2p/p2p/net/swarm/testing"
)

const (
	defaultChunkSize     = uint64(1) << 17
	defaultEncodeType    = ethstorage.ENCODE_BLOB_POSEIDON
	blobEmptyFillingMask = byte(0b10000000)
	metafileName         = "metafile.dat.meta"
)

var (
	contract       = common.HexToAddress("0x0000000000000000000000000000000003330001")
	empty          = make([]byte, 0)
	maxRequestSize = uint64(4 * 1024 * 1024)
	testLog        = log.New("TestSync")
	prover         = prv.NewKZGProver(testLog)
)

type remotePeer struct {
	shards       []uint64            // shards the remote peer support
	excludedList map[uint64]struct{} // excludedList a list of blob indexes whose data is not exist in the remote peer
}

func CreateMetaFile(filename string, len int64) (*os.File, error) {
	file, err := os.Create(filename)
	if err != nil {
		return nil, err
	}
	err = fallocate.Fallocate(file, int64((32)*len), int64(32))
	if err != nil {
		return nil, err
	}
	return file, nil
}

func GenerateMetadata(idx, size uint64, hash []byte) common.Hash {
	meta := make([]byte, 0)
	idx_bs := make([]byte, 8)
	binary.BigEndian.PutUint64(idx_bs, idx)
	meta = append(meta, idx_bs[3:]...)
	size_bs := make([]byte, 8)
	binary.BigEndian.PutUint64(size_bs, size)
	meta = append(meta, size_bs[5:]...)
	meta = append(meta, hash[:24]...)
	return common.BytesToHash(meta)
}

type mockL1Source struct {
	lastBlobIndex uint64
	metaFile      *os.File
}

func NewMockL1Source(lastBlobIndex uint64, metafile string) ethstorage.Il1Source {
	if len(metafile) == 0 {
		panic("metafile param is needed when using mock l1")
	}

	file, err := os.OpenFile(metafile, os.O_RDONLY, 0600)
	if err != nil {
		panic(fmt.Sprintf("open metafile faiil with err %s", err.Error()))
	}
	return &mockL1Source{lastBlobIndex: lastBlobIndex, metaFile: file}
}

func (l1 *mockL1Source) getMetadata(idx uint64) ([32]byte, error) {
	bs := make([]byte, 32)
	l, err := l1.metaFile.ReadAt(bs, int64(idx*32))
	if err != nil {
		return common.Hash{}, fmt.Errorf("get metadata fail, err %s", err.Error())
	}
	if l != 32 {
		return common.Hash{}, errors.New("get metadata fail, err read less than 32 bytes")
	}
	return common.BytesToHash(bs), nil
}

func (l1 *mockL1Source) GetKvMetas(kvIndices []uint64, blockNumber int64) ([][32]byte, error) {
	metas := make([][32]byte, 0)
	for _, idx := range kvIndices {
		meta, err := l1.getMetadata(idx)
		if err != nil {
			log.Debug("read meta fail", "err", err.Error())
			continue
		}
		metas = append(metas, meta)
	}
	return metas, nil
}

func (l1 *mockL1Source) GetStorageLastBlobIdx(blockNumber int64) (uint64, error) {
	return l1.lastBlobIndex, nil
}

type mockStorageManagerReader struct {
	kvEntries       uint64
	maxKvSize       uint64
	encodeType      uint64
	shards          []uint64
	contractAddress common.Address
	shardMiner      common.Address
	blobPayloads    map[uint64]*BlobPayloadWithRowData
}

func (s *mockStorageManagerReader) TryReadEncoded(kvIdx uint64, readLen int) ([]byte, bool, error) {
	if blobPayload, ok := s.blobPayloads[kvIdx]; ok {
		data := blobPayload.EncodedBlob
		if len(data) > readLen {
			data = data[:readLen]
		}
		return data, true, nil
	} else {
		return nil, false, ethereum.NotFound
	}
}

func (s *mockStorageManagerReader) TryReadMeta(kvIdx uint64) ([]byte, bool, error) {
	if blobPayload, ok := s.blobPayloads[kvIdx]; ok {
		return blobPayload.BlobCommit[:], true, nil
	} else {
		return nil, false, ethereum.NotFound
	}
}

func (s *mockStorageManagerReader) KvEntries() uint64 {
	return s.kvEntries
}

func (s *mockStorageManagerReader) ContractAddress() common.Address {
	return s.contractAddress
}

func (s *mockStorageManagerReader) Shards() []uint64 {
	return s.shards
}

func (s *mockStorageManagerReader) MaxKvSize() uint64 {
	return s.maxKvSize
}

func (s *mockStorageManagerReader) GetShardMiner(shardIdx uint64) (common.Address, bool) {
	return s.shardMiner, true
}

func (s *mockStorageManagerReader) GetShardEncodeType(shardIdx uint64) (uint64, bool) {
	return s.encodeType, true
}

type BlobPayloadWithRowData struct {
	MinerAddress common.Address `json:"minerAddress"`
	BlobIndex    uint64         `json:"blobIndex"`
	BlobCommit   common.Hash    `json:"blobCommit"`
	EncodeType   uint64         `json:"encodeType"`
	EncodedBlob  []byte         `json:"blob"`
	RowData      []byte
}

func createEthStorage(contract common.Address, shardIdxList []uint64, chunkSize, kvSize, kvEntries uint64,
	miner common.Address, encodeType uint64) (*ethstorage.ShardManager, []string) {
	sm := ethstorage.NewShardManager(contract, kvSize, kvEntries, chunkSize)
	ethstorage.ContractToShardManager[contract] = sm
	chunkPerKv := kvSize / chunkSize

	files := make([]string, 0)
	for _, shardIdx := range shardIdxList {
		sm.AddDataShard(shardIdx)
		fileName := fmt.Sprintf(".\\ss%d.dat", shardIdx)
		files = append(files, fileName)
		startChunkId := shardIdx * chunkPerKv * kvEntries
		_, err := ethstorage.Create(fileName, startChunkId, kvEntries*chunkPerKv, 0, kvSize, encodeType, miner, sm.ChunkSize())
		if err != nil {
			log.Crit("open failed", "error", err)
		}

		var df *ethstorage.DataFile
		df, err = ethstorage.OpenDataFile(fileName)
		if err != nil {
			log.Crit("open failed", "error", err)
		}
		sm.AddDataFile(df)
	}

	return sm, files
}

// makeKVStorage generate a range of storage Data and its metadata
func makeKVStorage(contract common.Address, shards []uint64, chunkSize, kvSize, kvCount, lastKvIndex uint64,
	miner common.Address, encodeType uint64, metafile *os.File) map[common.Address]map[uint64]*BlobPayloadWithRowData {
	shardData := make(map[common.Address]map[uint64]*BlobPayloadWithRowData)
	smData := make(map[uint64]*BlobPayloadWithRowData)
	shardData[contract] = smData
	sm := ethstorage.ContractToShardManager[contract]

	for _, sidx := range shards {
		for i := sidx * kvCount; i < (sidx+1)*kvCount; i++ {
			val := make([]byte, kvSize)
			root := common.Hash{}
			if i < lastKvIndex {
				copy(val[:20], contract.Bytes())
				binary.BigEndian.PutUint64(val[20:28], i)
				root, _ = prover.GetRoot(val, kvSize/chunkSize, chunkSize)
			}

			commit := generateMetadata(root)
			encodeData, _, _ := sm.EncodeKV(i, val, commit, miner, encodeType)
			smData[i] = &BlobPayloadWithRowData{
				MinerAddress: miner,
				BlobIndex:    i,
				BlobCommit:   commit,
				EncodeType:   encodeType,
				EncodedBlob:  encodeData,
				RowData:      val,
			}
			meta := GenerateMetadata(i, kvSize, root[:])
			metafile.WriteAt(meta.Bytes(), int64(i*32))
		}
	}

	return shardData
}

func fillEmpty(sm *ethstorage.ShardManager, list map[uint64]struct{}) {
	commit := common.Hash{}
	commit[ethstorage.HashSizeInContract] = commit[ethstorage.HashSizeInContract] | blobEmptyFillingMask

	for i := range list {
		sm.TryWrite(i, empty, commit)
	}
}

func verifyKVs(data map[common.Address]map[uint64]*BlobPayloadWithRowData,
	excludedList map[uint64]struct{}, t *testing.T) {
	emptyCommit := common.Hash{}
	emptyCommit[ethstorage.HashSizeInContract] = emptyCommit[ethstorage.HashSizeInContract] | blobEmptyFillingMask
	for contract, shardData := range data {
		shardManager := ethstorage.ContractToShardManager[contract]
		if shardManager == nil {
			t.Fatalf("sstorage manager for contract %s do not exist.", contract.Hex())
		}
		for idx, blobPayload := range shardData {
			rowData := blobPayload.RowData
			encodedBlob := blobPayload.EncodedBlob
			commit := blobPayload.BlobCommit
			// for data in the excluded list, that mean it should not sync to the local node, but written by empty blob,
			// so the expected data is make([]byte, kvSize)
			if _, ok := excludedList[idx]; ok {
				rowData = make([]byte, len(blobPayload.RowData))
				commit = emptyCommit
				encodedBlob, _, _ = shardManager.EncodeKV(idx, rowData, commit, blobPayload.MinerAddress, blobPayload.EncodeType)
			}
			decodedData, ok, err := shardManager.TryRead(idx, len(blobPayload.RowData), commit)
			if err != nil {
				t.Fatalf("TryRead sstorage Data fail. err: %s", err.Error())
			}
			if !ok {
				t.Fatalf("TryRead sstroage Data fail. err: %s, index %d", "shard Idx not support", idx)
			}

			encodedData, _, err := shardManager.TryReadEncoded(idx, len(blobPayload.EncodedBlob))
			if err != nil {
				t.Fatalf("TryRead encoded Data fail. err: %s", err.Error())
			}

			if !bytes.Equal(rowData, decodedData) {
				t.Fatalf("verify KV failed; index: %d; rowData: %s; decodedData: %s", idx,
					common.Bytes2Hex(rowData), common.Bytes2Hex(decodedData))
			}
			if !bytes.Equal(encodedBlob, encodedData) {
				t.Fatalf("verify KV failed; index: %d; blobPayload: %s; encodedData: %s", idx,
					common.Bytes2Hex(rowData), common.Bytes2Hex(encodedData))
			}
		}
	}
}

func generateMetadata(hash common.Hash) common.Hash {
	meta := make([]byte, 32)

	copy(meta[0:ethstorage.HashSizeInContract], hash[0:ethstorage.HashSizeInContract])
	meta[ethstorage.HashSizeInContract] = meta[ethstorage.HashSizeInContract] | blobEmptyFillingMask

	return common.BytesToHash(meta)
}

func getNetHost(t *testing.T) host.Host {
	netw := swarmt.GenSwarm(t)
	h := bhost.NewBlankHost(netw)
	t.Cleanup(func() { h.Close() })
	return h
}

func connect(t *testing.T, a, b host.Host, as, bs map[common.Address][]uint64) {
	pinfo := a.Peerstore().PeerInfo(a.ID())
	a.Peerstore().Put(b.ID(), EthStorageENRKey, ConvertToContractShards(bs))
	b.Peerstore().Put(a.ID(), EthStorageENRKey, ConvertToContractShards(as))
	err := b.Connect(context.Background(), pinfo)
	if err != nil {
		t.Fatal(err)
	}
}

func createLocalHostAndSyncClient(t *testing.T, testLog log.Logger, rollupCfg *rollup.EsConfig, db ethdb.Database,
	storageManager StorageManager, metrics SyncClientMetrics, mux *event.Feed) (host.Host, *SyncClient) {
	localHost := getNetHost(t)

	syncCl := NewSyncClient(testLog, rollupCfg, localHost.NewStream, storageManager, maxRequestSize, db, metrics, mux)
	localHost.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(nw network.Network, conn network.Conn) {
			shards := make(map[common.Address][]uint64)
			css, err := localHost.Peerstore().Get(conn.RemotePeer(), EthStorageENRKey)
			if err != nil {
				log.Warn("Get shards from peer failed", "error", err.Error())
			} else {
				shards = ConvertToShardList(css.([]*ContractShards))
			}

			added := syncCl.AddPeer(conn.RemotePeer(), shards)
			if !added {
				conn.Close()
			}
		},
		DisconnectedF: func(nw network.Network, conn network.Conn) {
			syncCl.RemovePeer(conn.RemotePeer())
		},
	})
	// the host may already be connected to peers, add them all to the sync client
	for _, conn := range localHost.Network().Conns() {
		shards := make(map[common.Address][]uint64)
		css, err := localHost.Peerstore().Get(conn.RemotePeer(), EthStorageENRKey)
		if err != nil {
			log.Warn("Get shards from peer failed", "error", err.Error())
		} else {
			shards = ConvertToShardList(css.([]*ContractShards))
		}
		added := syncCl.AddPeer(conn.RemotePeer(), shards)
		if !added {
			conn.Close()
		}
	}
	return localHost, syncCl
}

func createRemoteHost(t *testing.T, ctx context.Context, rollupCfg *rollup.EsConfig,
	storageManager *mockStorageManagerReader, metrics SyncServerMetrics, testLog log.Logger) host.Host {

	remoteHost := getNetHost(t)
	syncSrv := NewSyncServer(rollupCfg, storageManager, metrics)
	blobByRangeHandler := MakeStreamHandler(ctx, testLog, syncSrv.HandleGetBlobsByRangeRequest)
	remoteHost.SetStreamHandler(GetProtocolID(RequestBlobsByRangeProtocolID, rollupCfg.L2ChainID), blobByRangeHandler)
	blobByListHandler := MakeStreamHandler(ctx, testLog, syncSrv.HandleGetBlobsByListRequest)
	remoteHost.SetStreamHandler(GetProtocolID(RequestBlobsByListProtocolID, rollupCfg.L2ChainID), blobByListHandler)

	return remoteHost
}

func checkStall(t *testing.T, waitTime time.Duration, mux *event.Feed, cancel func()) {
	dlEventCh := make(chan EthStorageSyncDone, 16)
	events := mux.Subscribe(dlEventCh)
	defer events.Unsubscribe()
	for {
		select {
		case <-time.After(waitTime * time.Second):
			t.Log("Sync stalled")
			cancel()
			return
		case ev := <-dlEventCh:
			if ev.DoneType == AllShardDone {
				return
			}
		}
	}
}

func compareTasks(tasks1, tasks2 []*task) error {
	if err := checkTasksWithBaskTasks(tasks1, tasks2); err != nil {
		return err
	}
	if err := checkTasksWithBaskTasks(tasks2, tasks1); err != nil {
		return err
	}
	return nil
}

func checkTasksWithBaskTasks(baseTasks, tasks []*task) error {
	for _, task1 := range baseTasks {
		var task2 *task = nil
		for _, stask := range tasks {
			if task1.Contract == stask.Contract && task1.ShardId == stask.ShardId {
				task2 = stask
				break
			}
		}
		if task2 == nil {
			return fmt.Errorf("compare tasks failed. error: missing task; contract %s & shardId %d",
				task1.Contract.Hex(), task1.ShardId)
		}
		if len(task1.SubTasks) != len(task2.SubTasks) {
			return fmt.Errorf("compare tasks failed: error: subtask len mismatch; contract %s & shardId %d, len 1 %d, len 2 %d",
				task1.Contract.Hex(), task1.ShardId, len(task1.SubTasks), len(task2.SubTasks))
		}
		if len(task1.healTask.Indexes) != len(task2.healTask.Indexes) {
			return fmt.Errorf("compare tasks failed: error: index len in heal task mismatch; contract %s & shardId %d, len 1 %d, len 2 %d",
				task1.Contract.Hex(), task1.ShardId, len(task1.healTask.Indexes), len(task2.healTask.Indexes))
		}
		if task1.done != task2.done {
			return fmt.Errorf("compare tasks failed: error: task done mismatch, ontract %s & shardId %d, task 1 %v, task 2 %v",
				task1.Contract.Hex(), task1.ShardId, task1.done, task2.done)
		}

		for _, subTask1 := range task1.SubTasks {
			exist := false
			for _, subTask2 := range task2.SubTasks {
				if subTask1.First == subTask2.First && subTask1.Last == subTask2.Last && subTask1.next == subTask2.next {
					exist = true
					break
				}
			}
			if !exist {
				return fmt.Errorf("compare tasks failed: error: missing subtask; contract %s & shardId %d, Next %d, Last %d",
					task1.Contract.Hex(), task1.ShardId, subTask1.next, subTask1.Last)
			}
		}

		for idx := range task1.healTask.Indexes {
			if _, ok := task2.healTask.Indexes[idx]; !ok {
				return fmt.Errorf("compare tasks failed: error: index missing; contract %s & shardId %d, index %d",
					task1.Contract.Hex(), task1.ShardId, idx)
			}
		}
	}
	return nil
}

func copyShardData(data map[uint64]*BlobPayloadWithRowData, shards []uint64, entries uint64,
	excludedList map[uint64]struct{}) map[uint64]*BlobPayloadWithRowData {
	pData := make(map[uint64]*BlobPayloadWithRowData)
	for _, id := range shards {
		for idx := id * entries; idx < (id+1)*entries; idx++ {
			val, exist := data[idx]
			_, destroyed := excludedList[idx]
			if exist && !destroyed {
				pData[idx] = val
			}
		}
	}
	return pData
}

func mergeExcludedList(aList, bList map[uint64]struct{}) map[uint64]struct{} {
	newDestroyedList := make(map[uint64]struct{})
	for idx := range aList {
		if _, ok := bList[idx]; ok {
			newDestroyedList[idx] = struct{}{}
		}
	}
	return newDestroyedList
}

func getRandomU64InRange(excludedList map[uint64]struct{}, start, end, count uint64) map[uint64]struct{} {
	i := uint64(0)
	m := make(map[uint64]struct{})
	for i < count {
		idx := rand.Uint64()%(end-start) + start
		if _, ok := excludedList[idx]; ok {
			continue
		}
		if _, ok := m[idx]; ok {
			continue
		}
		m[idx] = struct{}{}
		i++
	}
	return m
}
