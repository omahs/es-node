// Copyright 2022-2023, EthStorage.
// For license information, see https://github.com/ethstorage/es-node/blob/main/LICENSE

package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethstorage/go-ethstorage/cmd/es-utils/utils"
	esLog "github.com/ethstorage/go-ethstorage/ethstorage/log"
	"github.com/ethstorage/go-ethstorage/ethstorage/storage"
	"github.com/urfave/cli"
)

const (
	HashSizeInContract = 24
)

var (
	log = esLog.NewLogger(esLog.DefaultCLIConfig())
)

var (
	l1Rpc        string
	contract     string
	privateKey   string
	miner        string
	datadir      string
	generateData string
	shardLength  int
	chainId      int

	fromAddress common.Address
	firstBlob   = true
	kvIdx       uint64
)

var flags = []cli.Flag{
	cli.StringFlag{
		Name:        "l1.rpc",
		Usage:       "Address of L1 User JSON-RPC endpoint to use (eth namespace required)",
		Destination: &l1Rpc,
	},
	cli.StringFlag{
		Name:        "storage.l1contract",
		Usage:       "Storage contract address on l1",
		Destination: &contract,
	},
	cli.IntFlag{
		Name:        "l1.chainId",
		Usage:       "L1 network chain id",
		Destination: &chainId,
	},
	cli.StringFlag{
		Name:        "storage.privateKey",
		Usage:       "Storage private key",
		Destination: &privateKey,
	},
	cli.StringFlag{
		Name:        "storage.miner",
		Usage:       "Miner's address to encode data and receive mining rewards",
		Destination: &miner,
	},
	cli.StringFlag{
		Name:        "datadir",
		Value:       "./es-data",
		Usage:       "Data directory for the storage files, databases and keystore",
		Destination: &datadir,
	},
	cli.IntFlag{
		Name:        "shardLength",
		Value:       1,
		Usage:       "File counts",
		Destination: &shardLength,
	},
	cli.StringFlag{
		Name:        "generateData",
		Usage:       "need to Generate Data",
		Destination: &generateData,
	},
}

func main() {
	app := cli.NewApp()
	app.Version = "1.0.0"
	app.Name = "es-devnet"
	app.Usage = "Create EthStorage Test Data"
	app.Flags = flags
	app.Action = GenerateTestData

	// start
	err := app.Run(os.Args)
	if err != nil {
		log.Crit("Application failed", "message", err)
		return
	}
}

func initFiles(storageCfg *storage.StorageConfig) ([]string, error) {
	shardIdxList := make([]uint64, shardLength)
	return createDataFile(storageCfg, shardIdxList, datadir)
}

func randomData(dataSize uint64) []byte {
	//fileSize := uint64(5 * 4096 * 31)
	data := make([]byte, dataSize)
	for j := uint64(0); j < dataSize; j += 32 {
		scalar := genRandomCanonicalScalar()
		max := j + 32
		if max > dataSize {
			max = dataSize
		}
		copy(data[j:max], scalar[:max-j])
	}
	return data
}

func generateDataAndWrite(files []string, storageCfg *storage.StorageConfig) []common.Hash {
	log.Info("Start write files...")

	hashFile, err := createHashFile()
	if err != nil {
		log.Crit("Create hash file failed", "error", err)
	}
	defer hashFile.Close()

	writer := bufio.NewWriter(hashFile)

	var hashes []common.Hash
	for shardIdx, file := range files {
		ds := initDataShard(uint64(shardIdx), file, storageCfg)

		// set blob size
		maxBlobSize := 8192
		if shardIdx == len(files)-1 {
			// last file, set 192 empty blob
			maxBlobSize = 8000
		}

		// write
		for i := 0; i < maxBlobSize; i++ {
			// generate data
			data := randomData(4096 * 31)
			// generate blob
			blobs := utils.EncodeBlobs(data)
			// write blob
			versionedHash := writeBlob(kvIdx, blobs[0], ds)
			hash := common.Hash{}
			copy(hash[0:], versionedHash[0:HashSizeInContract])
			hashes = append(hashes, hash)
			kvIdx += 1

			// write to file
			content := hex.EncodeToString(hash[:])
			_, err = writer.WriteString(content + "\n")
			if err != nil {
				log.Crit("Write file failed", "error", err)
			}
		}

		// last file, write 192 empty blob
		if shardIdx == len(files)-1 {
			blob := kzg4844.Blob{}
			for j := 0; j < 192; j++ {
				writeBlob(kvIdx, blob, ds)
				kvIdx += 1
			}
		}
		log.Info("Write File Success \n")
	}

	err = writer.Flush()
	if err != nil {
		log.Crit("Save file failed", "error", err)
	}
	return hashes
}

func uploadBlobHashes(cli *ethclient.Client, hashes []common.Hash) error {
	// Submitting 580 blob hashes costs 30 million gas
	submitCount := 580
	for i, length := 0, len(hashes); i < length; i += submitCount {
		max := i + submitCount
		if max > length {
			max = length
		}
		submitHashes := hashes[i:max]
		log.Info("Transaction submitted start", "from", i, "to", max)
		// update to contract
		err := UploadHashes(cli, submitHashes)
		if err != nil {
			return err
		}
		log.Info("Upload Success \n")
	}
	return nil
}

func GenerateTestData(ctx *cli.Context) error {
	// init
	cctx := context.Background()
	client, err := ethclient.DialContext(cctx, l1Rpc)
	if err != nil {
		log.Error("Failed to connect to the Ethereum client", "error", err, "l1Rpc", l1Rpc)
		return err
	}
	defer client.Close()
	// init config
	l1Contract := common.HexToAddress(contract)
	storageCfg, err := initStorageConfig(cctx, client, l1Contract, common.HexToAddress(miner))
	if err != nil {
		log.Error("Failed to load storage config", "error", err)
		return err
	}
	log.Info("Storage config loaded", "storageCfg", storageCfg)
	// generate from address
	key, err := crypto.HexToECDSA(privateKey)
	if err != nil {
		log.Error("Invalid private key", "err", err)
		return err
	}
	fromAddress = crypto.PubkeyToAddress(key.PublicKey)

	// create files
	var hashes []common.Hash
	if generateData == "true" {
		files, err := initFiles(storageCfg)
		if err != nil {
			log.Error("Failed to create data file", "error", err)
			return err
		} else {
			log.Info("File Create Success \n")
		}

		// generate data
		hashes = generateDataAndWrite(files, storageCfg)
	} else {
		hashes, err = readHashFile()
		if err != nil {
			log.Error("Failed to load hash", "error", err)
			return err
		} else {
			log.Info("Load Hash Success \n")
		}
	}

	// upload
	return uploadBlobHashes(client, hashes)
}
