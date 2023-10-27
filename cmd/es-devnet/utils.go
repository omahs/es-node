// Copyright 2022-2023, EthStorage.
// For license information, see https://github.com/ethstorage/es-node/blob/main/LICENSE

package main

import (
	"bufio"
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/ethclient"
	es "github.com/ethstorage/go-ethstorage/ethstorage"
	"github.com/ethstorage/go-ethstorage/ethstorage/storage"
)

const fileName = "shard-%d.dat"
const fileHashName = "blob-hash.txt"
const blobCommitmentVersionKZG uint8 = 0x01

func readSlotFromContract(ctx context.Context, client *ethclient.Client, l1Contract common.Address, fieldName string) ([]byte, error) {
	h := crypto.Keccak256Hash([]byte(fieldName + "()"))
	msg := ethereum.CallMsg{
		To:   &l1Contract,
		Data: h[0:4],
	}
	bs, err := client.CallContract(ctx, msg, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get %s from contract: %v", fieldName, err)
	}
	return bs, nil
}

func readUintFromContract(ctx context.Context, client *ethclient.Client, l1Contract common.Address, fieldName string) (uint64, error) {
	bs, err := readSlotFromContract(ctx, client, l1Contract, fieldName)
	if err != nil {
		return 0, err
	}
	value := new(big.Int).SetBytes(bs).Uint64()
	log.Info("Read uint from contract", "field", fieldName, "value", value)
	return value, nil
}

func initStorageConfig(ctx context.Context, client *ethclient.Client, l1Contract, miner common.Address) (*storage.StorageConfig, error) {
	maxKvSizeBits, err := readUintFromContract(ctx, client, l1Contract, "maxKvSizeBits")
	if err != nil {
		return nil, err
	}
	chunkSizeBits := maxKvSizeBits
	shardEntryBits, err := readUintFromContract(ctx, client, l1Contract, "shardEntryBits")
	if err != nil {
		return nil, err
	}
	return &storage.StorageConfig{
		L1Contract:        l1Contract,
		Miner:             miner,
		KvSize:            1 << maxKvSizeBits,
		ChunkSize:         1 << chunkSizeBits,
		KvEntriesPerShard: 1 << shardEntryBits,
	}, nil
}

func createDataFile(cfg *storage.StorageConfig, shardIdxList []uint64, datadir string) ([]string, error) {
	log.Info("Creating data files", "shardIdxList", shardIdxList, "dataDir", datadir)
	if _, err := os.Stat(datadir); os.IsNotExist(err) {
		if err := os.Mkdir(datadir, 0755); err != nil {
			log.Error("Creating data directory", "error", err)
			return nil, err
		}
	}
	var files []string
	for index := range shardIdxList {
		shardIdx := uint64(index)
		dataFile := filepath.Join(datadir, fmt.Sprintf(fileName, shardIdx))
		if _, err := os.Stat(dataFile); err == nil {
			log.Error("Creating data file", "error", "file already exists, will not overwrite", "file", dataFile)
			return nil, err
		}
		if cfg.ChunkSize == 0 {
			return nil, fmt.Errorf("chunk size should not be 0")
		}
		if cfg.KvSize%cfg.ChunkSize != 0 {
			return nil, fmt.Errorf("max kv size %% chunk size should be 0")
		}
		chunkPerKv := cfg.KvSize / cfg.ChunkSize
		startChunkId := shardIdx * cfg.KvEntriesPerShard * chunkPerKv
		chunkIdxLen := chunkPerKv * cfg.KvEntriesPerShard
		log.Info("Creating data file", "chunkIdxStart", startChunkId, "chunkIdxLen", chunkIdxLen, "chunkSize", cfg.ChunkSize, "miner", cfg.Miner, "encodeType", es.ENCODE_BLOB_POSEIDON)

		df, err := es.Create(dataFile, startChunkId, chunkPerKv*cfg.KvEntriesPerShard, 0, cfg.KvSize, es.ENCODE_BLOB_POSEIDON, cfg.Miner, cfg.ChunkSize)
		if err != nil {
			log.Error("Creating data file", "error", err)
			return nil, err
		}
		log.Info("Data file created", "shard", shardIdx, "file", dataFile, "chunkIdxStart", df.KvIdxStart(), "ChunkIdxEnd", df.ChunkIdxEnd(), "miner", df.Miner())
		files = append(files, dataFile)
	}
	return files, nil
}

func createHashFile() (*os.File, error) {
	dataFile := filepath.Join(datadir, fileHashName)
	if _, err := os.Stat(dataFile); err == nil {
		log.Error("Creating hash file", "error", "file already exists, will not overwrite", "file", dataFile)
		return nil, err
	}
	return os.Create(dataFile)
}

func readHashFile() []common.Hash {
	dataFile := filepath.Join(datadir, fileHashName)
	file, err := os.Open(dataFile)
	if err != nil {
		return nil
	}
	defer file.Close()

	var hash = common.Hash{}
	var count int64
	reader := bufio.NewReader(file)
	for {
		line, _ := reader.ReadString('\n')
		line = strings.Replace(line, "\n", "", -1)
		val := strings.Split(line, ":")

		count, _ = strconv.ParseInt(val[0], 10, 0)
		hashData, _ := hex.DecodeString(val[1])
		copy(hash[:], hashData[:])
		break
	}

	var hashes []common.Hash
	for i := int64(0); i < count; i++ {
		hashes = append(hashes, hash)
	}
	return hashes
}

func sortHashInfos(hashInfos []HashInfo) {
	sort.Slice(hashInfos, func(i, j int) bool {
		return hashInfos[i].index < hashInfos[j].index
	})
}

func prepareCommit(commit common.Hash) common.Hash {
	c := common.Hash{}
	copy(c[0:HashSizeInContract], commit[0:HashSizeInContract])

	// The first bit after data hash in the meta indicate whether this blob has been filled. 0 stands for NOT filled yet.
	// We want to make sure this bit to be 1 when filling data
	c[HashSizeInContract] = c[HashSizeInContract] | blobFillingMask

	return c
}

func initDataShard(shardIdx uint64, filename string, storageCfg *storage.StorageConfig) *es.DataShard {
	ds := es.NewDataShard(shardIdx, storageCfg.KvSize, storageCfg.KvEntriesPerShard, storageCfg.ChunkSize)
	var err error
	var df *es.DataFile
	df, err = es.OpenDataFile(filename)
	if err != nil {
		log.Crit("Open failed", "error", err)
	}
	err = ds.AddDataFile(df)
	if err != nil {
		log.Crit("Open failed", "error", err)
	}
	if !ds.IsComplete() {
		log.Warn("Shard is not completed")
	}
	return ds
}

// random data
func genRandomCanonicalScalar() [32]byte {
	maxCanonical := new(big.Int)
	_, success := maxCanonical.SetString("73eda753299d7d483339d80809a1d80553bda402fffe5bfeffffffff00000000", 16)
	if !success {
		log.Crit("Error creating modulus")
	}
	randomNum, err := crand.Int(crand.Reader, maxCanonical)
	if err != nil {
		log.Crit("Error generate random number")
	}
	var res [32]byte
	randomNum.FillBytes(res[:])
	return res
}

func writeBlob(kvIdx uint64, blob kzg4844.Blob, ds *es.DataShard) common.Hash {
	commit, err := kzg4844.BlobToCommitment(blob)
	if err != nil {
		log.Crit("Compute commit failed", "error", err)
	}

	versionHash := sha256.Sum256(commit[:])
	versionHash[0] = blobCommitmentVersionKZG

	err = ds.Write(kvIdx, blob[:], versionHash)
	if err != nil {
		log.Crit("Write failed", "error", err)
	}
	log.Info("Write value", "kvIdx", kvIdx)

	hash := common.Hash{}
	copy(hash[0:], versionHash[0:HashSizeInContract])
	return hash
}

func UploadHashes(client *ethclient.Client, hashes []common.Hash) error {

	to := common.HexToAddress(contract)

	// query exits
	//hash := hashes[0]
	//bytes32, _ := abi.NewType("bytes32", "", nil)
	//dataField, _ := abi.Arguments{{Type: bytes32}}.Pack(hash)
	//h := crypto.Keccak256Hash([]byte(`exist(bytes32)`))
	//data := append(h[0:4], dataField...)
	//callMsg := ethereum.CallMsg{
	//	From: fromAddress,
	//	To:   &to,
	//	Data: data,
	//}
	//bs, err := client.CallContract(context.Background(), callMsg, nil)
	//if err != nil {
	//	log.Crit("Failed to get exist", "error", err)
	//}
	//boolType, _ := abi.NewType("bool", "", nil)
	//res, err := abi.Arguments{{Type: boolType}}.UnpackValues(bs)
	//if err != nil {
	//	log.Crit("Failed to unpack values", "error", err)
	//}
	//exist := res[0].(bool)
	//if exist {
	//	log.Info("This hash is exist", "hash", hash)
	//	return nil
	//}

	// query price
	h := crypto.Keccak256Hash([]byte(`upfrontPayment()`))
	callMsg := ethereum.CallMsg{
		To:   &to,
		Data: h[:],
	}
	bs, err := client.CallContract(context.Background(), callMsg, new(big.Int).SetInt64(-2))
	if err != nil {
		log.Crit("Failed to get upfront fee", "error", err)
	}
	uint256Type, _ := abi.NewType("uint256", "", nil)
	res, err := abi.Arguments{{Type: uint256Type}}.UnpackValues(bs)
	if err != nil {
		log.Crit("Failed to unpack values", "error", err)
	}
	value256 := res[0].(*big.Int)
	if firstBlob {
		value256 = new(big.Int).Add(value256, big.NewInt(100000000))
		firstBlob = false
	}

	// create calldata
	bytes32Array, _ := abi.NewType("bytes32[]", "", nil)
	dataField, _ := abi.Arguments{{Type: bytes32Array}}.Pack(hashes)
	h = crypto.Keccak256Hash([]byte("putHashes(bytes32[])"))
	calldata := "0x" + common.Bytes2Hex(append(h[0:4], dataField...))

	tx := SendTx(
		client,
		value256,
		30000000,
		calldata,
	)

	resultCh := make(chan *types.Receipt, 1)
	errorCh := make(chan error, 1)
	revert := fmt.Errorf("revert")
	go func() {
		receipt, err := bind.WaitMined(context.Background(), client, tx)
		if err != nil {
			log.Error("Get transaction receipt err", "error", err)
			errorCh <- err
		}
		if receipt.Status == 0 {
			log.Error("Transaction reverted")
			errorCh <- revert
			return
		}
		log.Info("Transaction confirmed successfully", "txHash", tx.Hash())
		resultCh <- receipt
	}()
	select {
	// try to get data hash from events first
	case receipt := <-resultCh:
		log.Info("Receipt returned", "gasUsed", receipt.GasUsed)
		var dataHashs []common.Hash
		var kvIndexes []uint64
		for i := range receipt.Logs {
			eventTopics := receipt.Logs[i].Topics
			kvIndex := new(big.Int).SetBytes(eventTopics[1][:]).Uint64()
			dataHash := eventTopics[3]
			dataHashs = append(dataHashs, dataHash)
			kvIndexes = append(kvIndexes, kvIndex)
		}
		return nil
	case err := <-errorCh:
		log.Error("Get transaction receipt err", "error", err)
		if err == revert {
			return err
		}
	case <-time.After(5 * time.Second):
		log.Info("Timed out for receipt, query contract for data hash...")
	}
	return nil
}

func SendTx(
	client *ethclient.Client,
	value *big.Int,
	gasLimit uint64,
	calldata string,
) *types.Transaction {
	ctx := context.Background()

	to := common.HexToAddress(contract)

	key, err := crypto.HexToECDSA(privateKey)
	if err != nil {
		log.Crit("Invalid private key", "err", err)
	}

	pendingNonce, err := client.PendingNonceAt(ctx, fromAddress)
	if err != nil {
		log.Crit("Error getting nonce", "error", err)
	}

	gasPrice256, err := client.SuggestGasPrice(ctx)
	if err != nil {
		log.Crit("Error getting suggested gas price", "error", err)
	}
	priorityGasPrice256 := gasPrice256

	calldataBytes, err := common.ParseHexOrString(calldata)
	if err != nil {
		log.Crit("Failed to parse calldata", "error", err)
	}
	unSignTx := &types.DynamicFeeTx{
		ChainID:   big.NewInt(int64(chainId)),
		Nonce:     pendingNonce,
		GasTipCap: priorityGasPrice256,
		GasFeeCap: gasPrice256,
		Gas:       gasLimit,
		To:        &to,
		Value:     value,
		Data:      calldataBytes,
	}
	tx := types.MustSignNewTx(key, types.NewLondonSigner(big.NewInt(int64(chainId))), unSignTx)

	log.Info("Start Send Transaction")
	err = client.SendTransaction(context.Background(), tx)
	if err != nil {
		log.Crit("Unable to send transaction", "error", err)
	}

	for {
		txn, isPending, err := client.TransactionByHash(context.Background(), tx.Hash())
		if err != nil || isPending {
			time.Sleep(1 * time.Second)
		} else {
			tx = txn
			break
		}
	}
	log.Info("Transaction submitted", "nonce", pendingNonce, "hash", tx.Hash())
	return tx
}