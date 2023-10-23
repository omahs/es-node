// Copyright 2022-2023, EthStorage.
// For license information, see https://github.com/ethstorage/es-node/blob/main/LICENSE

package main

import (
	"context"
	"math"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethstorage/go-ethstorage/cmd/es-utils/utils"
	esLog "github.com/ethstorage/go-ethstorage/ethstorage/log"
	"github.com/ethstorage/go-ethstorage/ethstorage/storage"
	"github.com/urfave/cli"
)

var (
	log = esLog.NewLogger(esLog.DefaultCLIConfig())
)

type Config struct {
	MaxKvSizeBits  *big.Int `json:"maxKvSizeBits"`
	ShardSizeBits  *big.Int `json:"shardSizeBits"`
	RandomChecks   *big.Int `json:"randomChecks"`
	MinimumDiff    *big.Int `json:"minimumDiff"`
	Cutoff         *big.Int `json:"cutoff"`
	DiffAdjDivisor *big.Int `json:"diffAdjDivisor"`
	TreasuryShare  *big.Int `json:"treasuryShare"`
}

var (
	l1Rpc       string
	chainId     int
	privateKey  string
	miner       string
	datadir     string
	shardLength int
)

var kvIdx uint64

var flags = []cli.Flag{
	cli.StringFlag{
		Name:        "l1.rpc",
		Usage:       "Address of L1 User JSON-RPC endpoint to use (eth namespace required)",
		Destination: &l1Rpc,
	},
	cli.IntFlag{
		Name:        "l1.chainId",
		Usage:       "L1 network ID",
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
}

var abiJson = `
[{"inputs":[{"components":[{"internalType":"uint256","name":"maxKvSizeBits","type":"uint256"},{"internalType":"uint256","name":"shardSizeBits","type":"uint256"},{"internalType":"uint256","name":"randomChecks","type":"uint256"},{"internalType":"uint256","name":"minimumDiff","type":"uint256"},{"internalType":"uint256","name":"cutoff","type":"uint256"},{"internalType":"uint256","name":"diffAdjDivisor","type":"uint256"},{"internalType":"uint256","name":"treasuryShare","type":"uint256"}],"internalType":"struct StorageContract.Config","name":"_config","type":"tuple"},{"internalType":"uint256","name":"_startTime","type":"uint256"},{"internalType":"uint256","name":"_storageCost","type":"uint256"},{"internalType":"uint256","name":"_dcfFactor","type":"uint256"},{"internalType":"uint256","name":"_nonceLimit","type":"uint256"},{"internalType":"address","name":"_treasury","type":"address"},{"internalType":"uint256","name":"_prepaidAmount","type":"uint256"}],"stateMutability":"nonpayable","type":"constructor"}]
`
var contractBytecode = "0x6102406040526000805464ffffffffff191690553480156200002057600080fd5b5060405162003e4c38038062003e4c8339810160408190526200004391620002c8565b865160c08790526001901b60e052608085905260a084905286516020880151889188918891889188918891889188918891889188918891889188911015620000d25760405162461bcd60e51b815260206004820152601360248201527f736861726453697a6520746f6f20736d616c6c0000000000000000000000000060448201526064015b60405180910390fd5b865160051115620001265760405162461bcd60e51b815260206004820152601360248201527f6d61784b7653697a6520746f6f20736d616c6c000000000000000000000000006044820152606401620000c9565b60008760400151116200017c5760405162461bcd60e51b815260206004820152601e60248201527f4174206c65617374206f6e6520636865636b706f696e74206e656564656400006044820152606401620000c9565b60208701805161012052875161010052875190516200019c9190620003b4565b610140528651620001b090600590620003b4565b610160526040878101516101805260608801516101a05260808801516101c05260a08801516101e05260c09097015161020052600492909255600580546001600160a01b0319166001600160a01b039290921691909117905561022052505060068190556000805260036020527f3617319a054d772f909f7c479a2cebe5066e836a939412e32403c99029b92eff55516200024b906200029e565b604051809103906000f08015801562000268573d6000803e3d6000fd5b50600780546001600160a01b0319166001600160a01b039290921691909117905550620003dc9c50505050505050505050505050565b60398062003e1383390190565b80516001600160a01b0381168114620002c357600080fd5b919050565b60008060008060008060008789036101a0811215620002e657600080fd5b60e0811215620002f557600080fd5b5060405160e081016001600160401b03811182821017156200032757634e487b7160e01b600052604160045260246000fd5b8060405250885181526020890151602082015260408901516040820152606089015160608201526080890151608082015260a089015160a082015260c089015160c08201528097505060e088015195506101008801519450610120880151935061014088015192506200039e6101608901620002ab565b9150610180880151905092959891949750929550565b81810381811115620003d657634e487b7160e01b600052601160045260246000fd5b92915050565b60805160a05160c05160e05161010051610120516101405161016051610180516101a0516101c0516101e05161020051610220516138b66200055d600039600081816107c6015261272d01526000818161055901526127ca01526000818161049701526125910152600081816103c4015261257001526000818161044301526125b201526000818161075e01528181612239015281816122a0015261235c0152600081816105f70152818161230c0152818161239701526123ed01526000818161072a01528181610a330152818161232d015281816123b80152818161260001528181612679015281816126bd0152612c17015260006107920152600061039001526000818161062b01528181610ac401528181610bf201528181610c3401526115c501526000818161058d0152818161152f01528181612d9c0152612dc601526000818161065f015281816121f401528181612e630152612e8d0152600081816102f50152818161150b0152818161265801526126f001526138b66000f3fe60806040526004361061021a5760003560e01c80637796ff3711610123578063b9e4cb90116100ab578063d8389dc51161006f578063d8389dc5146107e8578063e447467b14610863578063e5ec2a6514610879578063ed2dabc614610899578063fedee7ea146108c657600080fd5b8063b9e4cb90146106f8578063c5d3490c14610718578063d32897131461074c578063d4044b3314610780578063d6e71a3b146107b457600080fd5b80639a576a02116100f25780639a576a02146105e5578063a097365f14610619578063a4a8435e1461064d578063acfeeb5414610681578063afd5644d1461069657600080fd5b80637796ff371461054757806378e979251461057b578063904fa21b146105af57806395bc2673146105c557600080fd5b80634e86235e116101a65780636d951bc5116101755780636d951bc514610431578063720d80d314610465578063739b482f1461048557806373e8b3d4146104b957806375a390bb1461052757600080fd5b80634e86235e1461037e57806354b02ba4146103b257806354e902b7146103e657806361d027b3146103f957600080fd5b80633aa2651c116101ed5780633aa2651c146102d05780633cb2fecc146102e3578063429dd7ad146103175780634581a9201461034b57806349bdd6f51461035e57600080fd5b8063122f30f71461021f5780631ccbc6da1461025457806327c845dc1461027757806328de3c9b14610279575b600080fd5b34801561022b57600080fd5b5061023f61023a366004613151565b6108f3565b60405190151581526020015b60405180910390f35b34801561026057600080fd5b50610269610a09565b60405190815260200161024b565b005b34801561028557600080fd5b506102b561029436600461318f565b60036020526000908152604090208054600182015460029092015490919083565b6040805193845260208401929092529082015260600161024b565b6102776102de366004613236565b610a98565b3480156102ef57600080fd5b506102697f000000000000000000000000000000000000000000000000000000000000000081565b34801561032357600080fd5b506000546103359064ffffffffff1681565b60405164ffffffffff909116815260200161024b565b61027761035936600461326a565b610afe565b34801561036a57600080fd5b506102776103793660046132b2565b610b5e565b34801561038a57600080fd5b506102697f000000000000000000000000000000000000000000000000000000000000000081565b3480156103be57600080fd5b506102697f000000000000000000000000000000000000000000000000000000000000000081565b6102776103f43660046132de565b610bab565b34801561040557600080fd5b50600554610419906001600160a01b031681565b6040516001600160a01b03909116815260200161024b565b34801561043d57600080fd5b506102697f000000000000000000000000000000000000000000000000000000000000000081565b34801561047157600080fd5b5061023f610480366004613341565b610c98565b34801561049157600080fd5b506102697f000000000000000000000000000000000000000000000000000000000000000081565b3480156104c557600080fd5b5061023f6104d436600461318f565b60408051336020808301919091528183019390935281518082038301815260609091018252805190830120600090815260019092529081902054600160401b9004901b67ffffffffffffffff1916151590565b34801561053357600080fd5b506102776105423660046133df565b610dac565b34801561055357600080fd5b506102697f000000000000000000000000000000000000000000000000000000000000000081565b34801561058757600080fd5b506102697f000000000000000000000000000000000000000000000000000000000000000081565b3480156105bb57600080fd5b5061026960045481565b3480156105d157600080fd5b506102776105e036600461318f565b610dc4565b3480156105f157600080fd5b506102697f000000000000000000000000000000000000000000000000000000000000000081565b34801561062557600080fd5b506102697f000000000000000000000000000000000000000000000000000000000000000081565b34801561065957600080fd5b506102697f000000000000000000000000000000000000000000000000000000000000000081565b34801561068d57600080fd5b50610269600581565b3480156106a257600080fd5b506102696106b136600461318f565b6040805133602080830191909152818301939093528151808203830181526060909101825280519083012060009081526001909252902054600160281b900462ffffff1690565b34801561070457600080fd5b5061023f610713366004613515565b610dd1565b34801561072457600080fd5b506102697f000000000000000000000000000000000000000000000000000000000000000081565b34801561075857600080fd5b506102697f000000000000000000000000000000000000000000000000000000000000000081565b34801561078c57600080fd5b506102697f000000000000000000000000000000000000000000000000000000000000000081565b3480156107c057600080fd5b506102697f000000000000000000000000000000000000000000000000000000000000000081565b3480156107f457600080fd5b5061084961080336600461318f565b60408051336020808301919091528183019390935281518082038301815260609091018252805190830120600090815260019092529081902054600160401b9004901b90565b60405167ffffffffffffffff19909116815260200161024b565b34801561086f57600080fd5b5061026960065481565b34801561088557600080fd5b50600754610419906001600160a01b031681565b3480156108a557600080fd5b506108b96108b436600461356e565b610e90565b60405161024b91906135b0565b3480156108d257600080fd5b506108e66108e13660046135fe565b611110565b60405161024b9190613683565b6000806109417f0931d596de2fd10f01ddd073fd5a90a976f169c76f039bb91c4775720042d43a857f30644e72e131a029b85045b68181585d2833e84879b9709143e1f593f00000016112c0565b6040805160038082526080820190925291925060009190602082016060803683370190505090506109927f30644e72e131a029b85045b68181585d2833e84879b9709143e1f593f0000001876136dd565b816000815181106109a5576109a56136f1565b60200260200101818152505081816001815181106109c5576109c56136f1565b60200260200101818152505083816002815181106109e5576109e56136f1565b6020026020010181815250506109fb8188611312565b15925050505b949350505050565b600080548190610a219064ffffffffff16600161371d565b60005464ffffffffff918216925081167f000000000000000000000000000000000000000000000000000000000000000090811c90911690610a67906001901b836136dd565b600103610a7e57610a7742611504565b9250505090565b600081815260036020526040902054610a7790611504565b565b60005b8151811015610afa57610ae8828281518110610ab957610ab96136f1565b6020026020010151827f0000000000000000000000000000000000000000000000000000000000000000610afe565b80610af28161373b565b915050610a9b565b5050565b600754600090610b17906001600160a01b031684611559565b90506000610b268583856115c1565b90508183827f8b7a21215282409938287ae262331bfe6411d35d3d46aa7e505ef02000524ac260405160405180910390a45050505050565b60405162461bcd60e51b815260206004820152601860248201527f72656d6f7665546f282920756e696d706c656d656e746564000000000000000060448201526064015b60405180910390fd5b60005b8251811015610c93576000610c16848381518110610bce57610bce6136f1565b6020026020010151848481518110610be857610be86136f1565b60200260200101517f00000000000000000000000000000000000000000000000000000000000000006115c1565b9050828281518110610c2a57610c2a6136f1565b60200260200101517f0000000000000000000000000000000000000000000000000000000000000000827f8b7a21215282409938287ae262331bfe6411d35d3d46aa7e505ef02000524ac260405160405180910390a45080610c8b8161373b565b915050610bae565b505050565b6000868152600260209081526040808320548352600182528083208151606081018352905464ffffffffff81168252600160281b810462ffffff1693820193909352600160401b909204811b67ffffffffffffffff191690820152818080610d0287870188613754565b925092509250610d6c8385604001518b8e604051602001610d4c9392919067ffffffffffffffff199390931683526001600160a01b03919091166020830152604082015260600190565b6040516020818303038152906040528051906020012060001c8c856108f3565b610d7d576000945050505050610da2565b6040840151610d9b9067ffffffffffffffff19168b848b1884610dd1565b9450505050505b9695505050505050565b610dbb8787878787878761176b565b50505050505050565b610dce8133610b5e565b50565b6000848103610de257508115610a01565b6000610def600c86611936565b90506000610e3e7f564c0a11a0f704f4fc3e8acfe0f8245f0ad1347b378fbf96e206da11a5d36306837f73eda753299d7d483339d80809a1d80553bda402fffe5bfeffffffff000000016112c0565b90506000806000610e4e87611998565b9250925092508382141580610e6e575067ffffffffffffffff1983168a14155b15610e8157600095505050505050610a01565b90961498975050505050505050565b606060008211610ee25760405162461bcd60e51b815260206004820152601b60248201527f64617461206c656e2073686f756c64206265206e6f6e207a65726f00000000006044820152606401610ba2565b6040805133602082015290810186905260009060600160408051808303601f1901815282825280516020918201206000818152600183528381206060860185525464ffffffffff81168652600160281b810462ffffff1693860193909352600160401b909204831b67ffffffffffffffff1916928401839052935003610f9b5760405162461bcd60e51b815260206004820152600e60248201526d19185d18481b9bdd08195e1a5cdd60921b6044820152606401610ba2565b610fa584866137ae565b816020015162ffffff161015610ffd5760405162461bcd60e51b815260206004820152601a60248201527f6265796f6e64207468652072616e6765206f66206b7653697a650000000000006044820152606401610ba2565b8051604080830151815164ffffffffff909316602084015288151583830152606083018890526080830187905267ffffffffffffffff191660a0808401919091528151808403909101815260c090920190526000856001600160401b0381111561106957611069612fbe565b6040519080825280601f01601f191660200182016040528015611093576020820181803683370190505b5090506000866020830160a06020860162033301600019fa6110b457600080fd5b503d806111035760405162461bcd60e51b815260206004820152601f60248201527f6765742829206d7573742062652063616c6c6564206f6e204553206e6f6465006044820152606401610ba2565b5098975050505050505050565b6060600082516001600160401b0381111561112d5761112d612fbe565b604051908082528060200260200182016040528015611156578160200160208202803683370190505b50905060005b83518110156112b95760006001600060026000888681518110611181576111816136f1565b60209081029190910181015182528181019290925260409081016000908120548452838301949094529182019092208151606081018352905464ffffffffff81168252600160281b810462ffffff1693820193909352600160401b909204811b67ffffffffffffffff191690820152855190915060d89086908490811061120a5761120a6136f1565b602002602001015160001b901b838381518110611229576112296136f1565b60200260200101818151179150818152505060c0816020015162ffffff1660001b901b83838151811061125e5761125e6136f1565b602002602001018181511791508181525050604081604001516001600160401b031916901c838381518110611295576112956136f1565b602090810291909101018051909117905250806112b18161373b565b91505061115c565b5092915050565b600060405160208152602080820152602060408201528460608201528360808201528260a08201526020600060c0836005600019fa6112fe57600080fd5b600051915060c08101604052509392505050565b60007f30644e72e131a029b85045b68181585d2833e84879b9709143e1f593f00000018161133e611a09565b90508060800151518551600161135491906137ae565b146113965760405162461bcd60e51b81526020600482015260126024820152711d995c9a599a595c8b5898590b5a5b9c1d5d60721b6044820152606401610ba2565b604080518082019091526000808252602082018190525b865181101561148757838782815181106113c9576113c96136f1565b60200260200101511061141e5760405162461bcd60e51b815260206004820152601f60248201527f76657269666965722d6774652d736e61726b2d7363616c61722d6669656c64006044820152606401610ba2565b6114738261146e856080015184600161143791906137ae565b81518110611447576114476136f1565b60200260200101518a8581518110611461576114616136f1565b6020026020010151611e92565b611f28565b91508061147f8161373b565b9150506113ad565b506114b08183608001516000815181106114a3576114a36136f1565b6020026020010151611f28565b90506114e66114c28660000151611fc1565b8660200151846000015185602001518587604001518b604001518960600151612060565b6114f657600193505050506114fe565b600093505050505b92915050565b60006114fe7f00000000000000000000000000000000000000000000000000000000000000006115547f0000000000000000000000000000000000000000000000000000000000000000856137c1565b6121eb565b6000806000836000526020600060206000885afa9150600051905081610a015760405162461bcd60e51b815260206004820152601760248201527f6661696c656420746f2067657420626c6f6220686173680000000000000000006044820152606401610ba2565b60007f00000000000000000000000000000000000000000000000000000000000000008211156116245760405162461bcd60e51b815260206004820152600e60248201526d6461746120746f6f206c6172676560901b6044820152606401610ba2565b6040805133602082015290810185905260009060600160408051808303601f1901815282825280516020918201206000818152600183528381206060860185525464ffffffffff81168652600160281b810462ffffff1693860193909352600160401b909204831b67ffffffffffffffff19169284018390529350036116f5576116ac61222c565b6000805464ffffffffff908116808452825260026020526040822084905590546116d89116600161371d565b6000805464ffffffffff191664ffffffffff929092169190911790555b62ffffff808516602080840191825267ffffffffffffffff1980891660408087019182526000978852600190935295829020945185549351965190921c600160401b0295909316600160281b029190921664ffffffffff909216918217176001600160401b031692909217905590509392505050565b604061177788436137c1565b11156117bc5760405162461bcd60e51b8152602060048201526014602482015273189b1bd8dac81b9d5b58995c881d1bdbc81bdb1960621b6044820152606401610ba2565b86408061180b5760405162461bcd60e51b815260206004820152601a60248201527f6661696c656420746f206f627461696e20626c6f636b686173680000000000006044820152606401610ba2565b600061181789436137c1565b61182290600c6137d4565b61182c90426137c1565b9050600454861061186f5760405162461bcd60e51b815260206004820152600d60248201526c6e6f6e636520746f6f2062696760981b6044820152606401610ba2565b604080516001600160a01b0389166020820152908101839052606081018790526000906080016040516020818303038152906040528051906020012090506118bb89828a898989612235565b905060006118c98a84612516565b905060006118d9826000196137eb565b90508083111561191c5760405162461bcd60e51b815260206004820152600e60248201526d0c8d2cccc40dcdee840dac2e8c6d60931b6044820152606401610ba2565b6119288b8b86856125d6565b505050505050505050505050565b60006001831b821061194a5761194a6137ff565b816000805b8581101561198f576119626002846136dd565b61196d8360026137d4565b17915061197b6002846137eb565b9250806119878161373b565b91505061194f565b50949350505050565b602081018051604080840151606085015192939092919060009060c090600a600019fa6119c457600080fd5b611000600051146119d457600080fd5b7f73eda753299d7d483339d80809a1d80553bda402fffe5bfeffffffff0000000160205114611a0257600080fd5b9193909250565b611a11612ed0565b6040805180820182527f10988d6c2d54f28c25a0718015e512c36da5ca9e0b5643d7af02c20f2293646381527f23dfa0a56f85c5491fdced399eb5e13382204b2e6cfef68b4c13769bde43a98a6020808301919091529083528151608080820184527f27fc2d45e854d09eac69a6c11777153eb2699536b2580073f8e26d23502652788285019081527f205ace91468165575227d56963b92cd031e404b6ff8cdff60e0acf15a0fab267606080850191909152908352845180860186527f0be5bac130e37ea0e1e6c55abd0817a17e7f9027d11a09f4ce4985a7f9bd714e81527f231dd62f292b6811fcd5cdfd9a4c645ab425576bb20fe0ed18f60814d0011bfc818601528385015285840192909252835180820185527f198e9393920d483a7260bfb731fb5d25f1aa493335a9e71297e485b7aef312c28186019081527f1800deef121f1e76426a00665e5c4479674322d4f75edadd46debd5cd992f6ed828501528152845180860186527f090689d0585ff075ec9e99ad690c3395bc4b313370b38ef355acdadcd122975b81527f12c85ea5db8c6deb4aab71808dcb408fe3d1e7690c43d37b4ce6cc0166fa7daa818601528185015285850152835190810184527f1c60bc71f85d3120e336087daf45a8386ef48dbedd97fc87f7978b0b35d7e4768185019081527f1813d77586ff1f677d79fdcdb7fe9b4cea4c466e2c908d08d0f636406ae79bc1828401528152835180850185527f0aa84398ad8517473c42113de0ad583f67b79e07372f761f32938f94e565179781527f2c6c441389cb49117c12d11a33d276a6e7002278ac1a18994b5d8a19cbf99ae68185015281840152908401528151600480825260a08201909352919082015b6040805180820190915260008082526020820152815260200190600190039081611c8c57505060808201908152604080518082019091527f2330aa3cf9bcbba304d0bc79708b033b4d7109d34206705b925cab83220c56bf81527f04dfd04b465947ce6dab76dc905320a5159a8ce380984e7b567d78c7f35b8a90602082015290518051600090611d1f57611d1f6136f1565b602002602001018190525060405180604001604052807f0a3871b19c45de85cb11e37aa019db38022d5413b4f3c7074dc83c85a347c2b481526020017f12632c86c0b82e22de78e81e9d2d3725f3e045677c1cd06734f9613adfcf76498152508160800151600181518110611d9657611d966136f1565b602002602001018190525060405180604001604052807f0d1411430a4abc17cfaeeef4d107bc9577a2042b7635b41bd1f7ecdd8c387a4f81526020017f2b91efb16254fd96d6fab43c55fb0a6e44806408972fe16b38eea771150c87bc8152508160800151600281518110611e0d57611e0d6136f1565b602002602001018190525060405180604001604052807f1a338de8362b7166e135cf1a1456ce0b2874a5531b83986645b383ffcb35170481526020017f2eb42f106a1301ce45031e056e0a16e88e44146f63b2df7a40344c3d026c6c1b8152508160800151600381518110611e8457611e846136f1565b602002602001018190525090565b6040805180820190915260008082526020820152611eae612f21565b835181526020808501519082015260408101839052600060608360808460076107d05a03fa90508080611edd57fe5b5080611f205760405162461bcd60e51b81526020600482015260126024820152711c185a5c9a5b99cb5b5d5b0b59985a5b195960721b6044820152606401610ba2565b505092915050565b6040805180820190915260008082526020820152611f44612f3f565b8351815260208085015181830152835160408301528301516060808301919091526000908360c08460066107d05a03fa90508080611f7e57fe5b5080611f205760405162461bcd60e51b81526020600482015260126024820152711c185a5c9a5b99cb5859190b59985a5b195960721b6044820152606401610ba2565b604080518082019091526000808252602082015281517f30644e72e131a029b85045b68181585d97816a916871ca8d3c208c16d87cfd479015801561200857506020830151155b156120285750506040805180820190915260008082526020820152919050565b60405180604001604052808460000151815260200182856020015161204d91906136dd565b61205790846137c1565b90529392505050565b60408051600480825260a08201909252600091829190816020015b604080518082019091526000808252602082015281526020019060019003908161207b57505060408051600480825260a0820190925291925060009190602082015b6120c5612f5d565b8152602001906001900390816120bd5790505090508a826000815181106120ee576120ee6136f1565b6020026020010181905250888260018151811061210d5761210d6136f1565b6020026020010181905250868260028151811061212c5761212c6136f1565b6020026020010181905250848260038151811061214b5761214b6136f1565b6020026020010181905250898160008151811061216a5761216a6136f1565b60200260200101819052508781600181518110612189576121896136f1565b602002602001018190525085816002815181106121a8576121a86136f1565b602002602001018190525083816003815181106121c7576121c76136f1565b60200260200101819052506121dc8282612885565b9b9a5050505050505050505050565b600060806122197f000000000000000000000000000000000000000000000000000000000000000084612bdc565b61222390856137d4565b901c9392505050565b610a9642612bef565b60007f000000000000000000000000000000000000000000000000000000000000000084511461229e5760405162461bcd60e51b81526020600482015260146024820152730c8c2e8c240d8cadccee8d040dad2e6dac2e8c6d60631b6044820152606401610ba2565b7f000000000000000000000000000000000000000000000000000000000000000082146123055760405162461bcd60e51b81526020600482015260156024820152740e0e4dedecc40d8cadccee8d040dad2e6dac2e8c6d605b1b6044820152606401610ba2565b60006123517f00000000000000000000000000000000000000000000000000000000000000007f00000000000000000000000000000000000000000000000000000000000000006137ae565b6001901b905060005b7f000000000000000000000000000000000000000000000000000000000000000081101561250957600061238e838a6136dd565b905060006123dc7f00000000000000000000000000000000000000000000000000000000000000007f00000000000000000000000000000000000000000000000000000000000000006137ae565b6123e9908c901b836137ae565b90507f000000000000000000000000000000000000000000000000000000000000000081811c90600090612421906001901b846136dd565b905061246782828d8d898151811061243b5761243b6136f1565b60200260200101518d8d8b818110612455576124556136f1565b90506020028101906104809190613815565b6124a55760405162461bcd60e51b815260206004820152600f60248201526e696e76616c69642073616d706c657360881b6044820152606401610ba2565b8b8a86815181106124b8576124b86136f1565b60200260200101516040516020016124da929190918252602082015260400190565b604051602081830303815290604052805190602001209b505050505080806125019061373b565b91505061235a565b5095979650505050505050565b600082815260036020526040812080548310156125695760405162461bcd60e51b81526020600482015260116024820152701b5a5b9959151cc81d1bdbc81cdb585b1b607a1b6044820152606401610ba2565b610a0181847f00000000000000000000000000000000000000000000000000000000000000007f00000000000000000000000000000000000000000000000000000000000000007f0000000000000000000000000000000000000000000000000000000000000000612cc6565b6000848152600360205260408120815490919064ffffffffff166125fb57600061263d565b6000547f0000000000000000000000000000000000000000000000000000000000000000906126339060019064ffffffffff16613862565b64ffffffffff16901c5b64ffffffffff1690506000818710156126a75782546126a0907f00000000000000000000000000000000000000000000000000000000000000007f00000000000000000000000000000000000000000000000000000000000000001b9087612d91565b9050612767565b8187036127675760005461271c906126ea9060017f00000000000000000000000000000000000000000000000000000000000000001b9064ffffffffff166136dd565b612714907f00000000000000000000000000000000000000000000000000000000000000006137d4565b845487612d91565b9050846006541015612767576127557f000000000000000000000000000000000000000000000000000000000000000060065487612d91565b61275f90826137ae565b600686905590505b6000878152600360205260409020612780908686612df0565b826002015484887f21169a803c4f80bc17b58fec31621acb14e52e5afefb2f661d8a3e1ed42b7e0a886040516127b891815260200190565b60405180910390a460006127106127ef7f0000000000000000000000000000000000000000000000000000000000000000846137d4565b6127f991906137eb565b9050600061280782846137c1565b6005546040519192506001600160a01b03169083156108fc029084906000818181858888f19350505050158015612842573d6000803e3d6000fd5b506040516001600160a01b0389169082156108fc029083906000818181858888f19350505050158015612879573d6000803e3d6000fd5b50505050505050505050565b600081518351146128d15760405162461bcd60e51b81526020600482015260166024820152751c185a5c9a5b99cb5b195b99dd1a1ccb59985a5b195960521b6044820152606401610ba2565b825160006128e08260066137d4565b90506000816001600160401b038111156128fc576128fc612fbe565b604051908082528060200260200182016040528015612925578160200160208202803683370190505b50905060005b83811015612b6057868181518110612945576129456136f1565b6020026020010151600001518282600661295f91906137d4565b61296a9060006137ae565b8151811061297a5761297a6136f1565b602002602001018181525050868181518110612998576129986136f1565b602002602001015160200151828260066129b291906137d4565b6129bd9060016137ae565b815181106129cd576129cd6136f1565b6020026020010181815250508581815181106129eb576129eb6136f1565b6020908102919091010151515182612a048360066137d4565b612a0f9060026137ae565b81518110612a1f57612a1f6136f1565b602002602001018181525050858181518110612a3d57612a3d6136f1565b60209081029190910181015151015182612a588360066137d4565b612a639060036137ae565b81518110612a7357612a736136f1565b602002602001018181525050858181518110612a9157612a916136f1565b602002602001015160200151600060028110612aaf57612aaf6136f1565b602002015182612ac08360066137d4565b612acb9060046137ae565b81518110612adb57612adb6136f1565b602002602001018181525050858181518110612af957612af96136f1565b602002602001015160200151600160028110612b1757612b176136f1565b602002015182612b288360066137d4565b612b339060056137ae565b81518110612b4357612b436136f1565b602090810291909101015280612b588161373b565b91505061292b565b50612b69612f82565b6000602082602086026020860160086107d05a03fa90508080612b8857fe5b5080612bce5760405162461bcd60e51b81526020600482015260156024820152741c185a5c9a5b99cb5bdc18dbd9194b59985a5b1959605a1b6044820152606401610ba2565b505115159695505050505050565b6000612be88383612e0e565b9392505050565b60008054612c059064ffffffffff16600161371d565b60005464ffffffffff918216925081167f000000000000000000000000000000000000000000000000000000000000000090811c90911690612c4b906001901b836136dd565b600103612c6a578015612c6a5760008181526003602052604090208390555b600081815260036020526040902054612c8290611504565b341015610c935760405162461bcd60e51b81526020600482015260126024820152711b9bdd08195b9bdd59da081c185e5b595b9d60721b6044820152606401610ba2565b84546000908190612cd790876137c1565b600188015490915085821015612d2e578481612cf388856137eb565b612cfe9060016137c1565b612d0891906137d4565b612d1291906137eb565b612d1c90826137ae565b905083811015612d295750825b612d86565b600085826001612d3e8a876137eb565b612d4891906137c1565b612d5291906137d4565b612d5c91906137eb565b905081612d6986836137ae565b1115612d7757849150612d84565b612d8181836137c1565b91505b505b979650505050505050565b6000610a0184612dc17f0000000000000000000000000000000000000000000000000000000000000000866137c1565b612deb7f0000000000000000000000000000000000000000000000000000000000000000866137c1565b612e5a565b6002830154612e009060016137ae565b600284015560018301559055565b6000600160801b5b8215612be85782600116600103612e38576080612e3385836137d4565b901c90505b6080612e4485806137d4565b901c9350612e536002846137eb565b9250612e16565b60006080612e887f000000000000000000000000000000000000000000000000000000000000000084612bdc565b612eb27f000000000000000000000000000000000000000000000000000000000000000086612bdc565b612ebc91906137c1565b612ec690866137d4565b901c949350505050565b6040805160e08101909152600060a0820181815260c0830191909152815260208101612efa612f5d565b8152602001612f07612f5d565b8152602001612f14612f5d565b8152602001606081525090565b60405180606001604052806003906020820280368337509192915050565b60405180608001604052806004906020820280368337509192915050565b6040518060400160405280612f70612fa0565b8152602001612f7d612fa0565b905290565b60405180602001604052806001906020820280368337509192915050565b60405180604001604052806002906020820280368337509192915050565b634e487b7160e01b600052604160045260246000fd5b604080519081016001600160401b0381118282101715612ff657612ff6612fbe565b60405290565b604051601f8201601f191681016001600160401b038111828210171561302457613024612fbe565b604052919050565b60006040828403121561303e57600080fd5b613046612fd4565b9050813581526020820135602082015292915050565b600082601f83011261306d57600080fd5b613075612fd4565b80604084018581111561308757600080fd5b845b818110156130a1578035845260209384019301613089565b509095945050505050565b60008183036101008112156130c057600080fd5b604051606081018181106001600160401b03821117156130e2576130e2612fbe565b6040529150816130f2858561302c565b81526080603f198301121561310657600080fd5b61310e612fd4565b915061311d856040860161305c565b825261312c856080860161305c565b60208301528160208201526131448560c0860161302c565b6040820152505092915050565b600080600080610160858703121561316857600080fd5b61317286866130ac565b966101008601359650610120860135956101400135945092505050565b6000602082840312156131a157600080fd5b5035919050565b60006001600160401b038211156131c1576131c1612fbe565b5060051b60200190565b600082601f8301126131dc57600080fd5b813560206131f16131ec836131a8565b612ffc565b82815260059290921b8401810191818101908684111561321057600080fd5b8286015b8481101561322b5780358352918301918301613214565b509695505050505050565b60006020828403121561324857600080fd5b81356001600160401b0381111561325e57600080fd5b610a01848285016131cb565b60008060006060848603121561327f57600080fd5b505081359360208301359350604090920135919050565b80356001600160a01b03811681146132ad57600080fd5b919050565b600080604083850312156132c557600080fd5b823591506132d560208401613296565b90509250929050565b600080604083850312156132f157600080fd5b82356001600160401b038082111561330857600080fd5b613314868387016131cb565b9350602085013591508082111561332a57600080fd5b50613337858286016131cb565b9150509250929050565b60008060008060008060a0878903121561335a57600080fd5b863595506020870135945061337160408801613296565b93506060870135925060808701356001600160401b038082111561339457600080fd5b818901915089601f8301126133a857600080fd5b8135818111156133b757600080fd5b8a60208285010111156133c957600080fd5b6020830194508093505050509295509295509295565b600080600080600080600060c0888a0312156133fa57600080fd5b873596506020880135955061341160408901613296565b94506060880135935060808801356001600160401b038082111561343457600080fd5b6134408b838c016131cb565b945060a08a013591508082111561345657600080fd5b818a0191508a601f83011261346a57600080fd5b81358181111561347957600080fd5b8b60208260051b850101111561348e57600080fd5b60208301945080935050505092959891949750929550565b600082601f8301126134b757600080fd5b81356001600160401b038111156134d0576134d0612fbe565b6134e3601f8201601f1916602001612ffc565b8181528460208386010111156134f857600080fd5b816020850160208301376000918101602001919091529392505050565b6000806000806080858703121561352b57600080fd5b84359350602085013592506040850135915060608501356001600160401b0381111561355657600080fd5b613562878288016134a6565b91505092959194509250565b6000806000806080858703121561358457600080fd5b843593506020850135801515811461359b57600080fd5b93969395505050506040820135916060013590565b600060208083528351808285015260005b818110156135dd578581018301518582016040015282016135c1565b506000604082860101526040601f19601f8301168501019250505092915050565b6000602080838503121561361157600080fd5b82356001600160401b0381111561362757600080fd5b8301601f8101851361363857600080fd5b80356136466131ec826131a8565b81815260059190911b8201830190838101908783111561366557600080fd5b928401925b82841015612d865783358252928401929084019061366a565b6020808252825182820181905260009190848201906040850190845b818110156136bb5783518352928401929184019160010161369f565b50909695505050505050565b634e487b7160e01b600052601260045260246000fd5b6000826136ec576136ec6136c7565b500690565b634e487b7160e01b600052603260045260246000fd5b634e487b7160e01b600052601160045260246000fd5b64ffffffffff8181168382160190808211156112b9576112b9613707565b60006001820161374d5761374d613707565b5060010190565b6000806000610140848603121561376a57600080fd5b61377485856130ac565b925061010084013591506101208401356001600160401b0381111561379857600080fd5b6137a4868287016134a6565b9150509250925092565b808201808211156114fe576114fe613707565b818103818111156114fe576114fe613707565b80820281158282048414176114fe576114fe613707565b6000826137fa576137fa6136c7565b500490565b634e487b7160e01b600052600160045260246000fd5b6000808335601e1984360301811261382c57600080fd5b8301803591506001600160401b0382111561384657600080fd5b60200191503681900382131561385b57600080fd5b9250929050565b64ffffffffff8281168282160390808211156112b9576112b961370756fea2646970667358221220633a5539f1a99a9c1f2dbbe3aa05ea1592c6b2d29ce3fd5aca4e55b62a43021764736f6c6343000812003360c0604052600c60808181527f6000354960005260206000f3000000000000000000000000000000000000000060a09081529091908190f3fe"

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

func deployContract(client *ethclient.Client) (common.Address, error) {
	bytecode := common.Hex2Bytes(contractBytecode)
	contractAbi, err := abi.JSON(strings.NewReader(abiJson))
	chainID := big.NewInt(int64(chainId))

	pk, err := crypto.HexToECDSA(privateKey)
	if err != nil {
		log.Crit("Deploy Contract", "private key", err)
	}
	auth, err := bind.NewKeyedTransactorWithChainID(pk, chainID)
	if err != nil {
		log.Crit("Deploy Contract", "auth", err)
	}

	nonce, err := client.PendingNonceAt(context.Background(), auth.From)
	if err != nil {
		log.Crit("Deploy Contract", "nonce", err)
	}
	auth.Nonce = big.NewInt(int64(nonce))
	auth.Value = big.NewInt(0)
	auth.GasLimit = uint64(3000000)
	auth.GasPrice = big.NewInt(2000000000)

	config := Config{big.NewInt(17), big.NewInt(30),
		big.NewInt(2), big.NewInt(10000000),
		big.NewInt(60), big.NewInt(1024), big.NewInt(100)}
	_startTime := big.NewInt(int64(math.Floor(float64(time.Now().Unix() / 1000))))
	_storageCost := big.NewInt(2000000000000)
	_dcfFactor := new(big.Int)
	_dcfFactor, _ = _dcfFactor.SetString("340282365167313208607671216367074279424", 10)
	_nonceLimit := big.NewInt(1048576)
	_treasury := common.HexToAddress("0x0000000000000000000000000000000000000000")
	_prepaidAmount := big.NewInt(8192000000000000)

	log.Info("Start Deploy Contract")
	address, tx, _, err := bind.DeployContract(auth, contractAbi, bytecode, client, config, _startTime,
		_storageCost, _dcfFactor, _nonceLimit, _treasury, _prepaidAmount)
	if err != nil {
		return common.Address{}, err
	}

	_, err = bind.WaitMined(context.TODO(), client, tx)
	return address, err
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

func generateDataAndWrite(files []string, storageCfg *storage.StorageConfig) error {
	for shardIdx, file := range files {
		ds := initDataShard(uint64(shardIdx), file, storageCfg)

		// generate data
		data := randomData(5 * 4096 * 31)

		// generate blob and write
		var hashes []common.Hash
		blobs := utils.EncodeBlobs(data)
		for _, blob := range blobs {
			hash := writeBlob(kvIdx, blob, ds)
			hashes = append(hashes, hash)
			kvIdx += 1
		}
		log.Info("", "hash", hashes)
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

	// create contract
	l1Contract, err := deployContract(client)
	if err != nil {
		log.Error("Failed to deploy contract", "error", err)
		return err
	} else {
		log.Info("Deploy Contract", "address", l1Contract, "", "\n")
	}

	//common.HexToAddress(contract)
	storageCfg, err := initStorageConfig(cctx, client, l1Contract, common.HexToAddress(miner))
	if err != nil {
		log.Error("Failed to load storage config", "error", err)
		return err
	}
	log.Info("Storage config loaded", "storageCfg", storageCfg)

	// create files
	files, err := initFiles(storageCfg)
	if err != nil {
		log.Error("Failed to create data file", "error", err)
		return err
	} else {
		log.Info("File create success \n")
	}

	// generate data
	return generateDataAndWrite(files, storageCfg)
}
