//// Copyright 2017 The go-ethereum Authors
//// This file is part of the go-ethereum library.
////
//// The go-ethereum library is free software: you can redistribute it and/or modify
//// it under the terms of the GNU Lesser General Public License as published by
//// the Free Software Foundation, either version 3 of the License, or
//// (at your option) any later version.
////
//// The go-ethereum library is distributed in the hope that it will be useful,
//// but WITHOUT ANY WARRANTY; without even the implied warranty of
//// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
//// GNU Lesser General Public License for more details.
////
//// You should have received a copy of the GNU Lesser General Public License
//// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.
//
package core

//
import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"github.com/abeychain/go-abey/abeyclient"
	"github.com/abeychain/go-abey/abeydb"
	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/consensus"
	"github.com/abeychain/go-abey/consensus/minerva"
	"github.com/abeychain/go-abey/core/rawdb"
	snaildb "github.com/abeychain/go-abey/core/snailchain/rawdb"
	"github.com/abeychain/go-abey/core/state"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/core/vm"
	"github.com/abeychain/go-abey/crypto"
	"github.com/abeychain/go-abey/params"
	"github.com/davecgh/go-spew/spew"
	"io"
	"math/big"
	"os"
	"reflect"
	"sort"
	"testing"
)

func TestDefaultGenesisBlock(t *testing.T) {
	//block1 := DefaultDevGenesisBlock().ToFastBlock(nil)
	//if block1.Hash() != params.MainnetGenesisHash {
	//	fmt.Println(block1.Hash().Hex())
	//	t.Errorf("wrong mainnet genesis hash, got %v, want %v", common.ToHex(block1.Hash().Bytes()), params.MainnetGenesisHash)
	//}
	block := DefaultGenesisBlock().ToFastBlock(nil)
	if block.Hash() != params.MainnetGenesisHash {
		fmt.Println(block.Hash().Hex())
		t.Errorf("wrong mainnet genesis hash, got %v, want %v", common.ToHex(block.Hash().Bytes()), params.MainnetGenesisHash)
	}
	block = DefaultTestnetGenesisBlock().ToFastBlock(nil)
	if block.Hash() != params.TestnetGenesisHash {
		fmt.Println(block.Hash().Hex())
		t.Errorf("wrong testnet genesis hash, got %v, want %v", common.ToHex(block.Hash().Bytes()), params.TestnetGenesisHash)
	}
}
func TestDefaultLesGenesisBlock(t *testing.T) {
	client, err := abeyclient.Dial("https://rpc.abeychain.com")
	if err != nil {
		t.Errorf("dail failed,%v", err)
	}
	block0, err := client.BlockByNumber(context.Background(), big.NewInt(int64(params.LesProtocolGenesisBlock)))
	if err != nil {
		t.Errorf("dail failed,%v", err)
	}
	if block0.Hash() != params.MainnetGenesisHashForLes {
		fmt.Println(block0.Hash().Hex())
		t.Errorf("wrong mainnet genesis hash, got %v, want %v", common.ToHex(block0.Hash().Bytes()), params.MainnetGenesisHashForLes.Hex())
	}
	block := DefaultGenesisBlockForLes().ToLesFastBlock()
	chash := types.RlpHash(block0.SwitchInfos())
	chash2 := types.RlpHash(block.SwitchInfos())
	fmt.Println("chash", chash.Hex())
	fmt.Println("chash2", chash2.Hex())
	fmt.Println("txhash", block0.Header().TxHash.Hex(), block.Header().TxHash.Hex())
	fmt.Println("CommitteeHash", block0.Header().CommitteeHash.Hex(), block.Header().CommitteeHash.Hex())
	fmt.Println("ReceiptHash", block0.Header().ReceiptHash.Hex(), block.Header().ReceiptHash.Hex())

	if block.Hash() != params.MainnetGenesisHashForLes {
		fmt.Println(block.Hash().Hex())
		t.Errorf("wrong mainnet genesis hash, got %v, want %v", common.ToHex(block.Hash().Bytes()), params.MainnetGenesisHashForLes.Hex())
	}
}

func TestSetupGenesis(t *testing.T) {
	var (
		customghash = common.HexToHash("0x3fec74f04bae7d8a8c71d250a6edfb330ecc18d2a4bbb44c85ca1cbec21bee29")
		customg     = Genesis{
			Config: params.TestChainConfig,
			Alloc: types.GenesisAlloc{
				{1}: {Balance: big.NewInt(1), Storage: map[common.Hash]common.Hash{{1}: {1}}},
			},
		}
		oldcustomg = customg
	)
	oldcustomg.Config = &params.ChainConfig{}
	tests := []struct {
		name       string
		fn         func(abeydb.Database) (*params.ChainConfig, common.Hash, common.Hash, error)
		wantConfig *params.ChainConfig
		wantHash   common.Hash
		wantErr    error
	}{
		{
			name: "genesis without ChainConfig",
			fn: func(db abeydb.Database) (*params.ChainConfig, common.Hash, common.Hash, error) {
				return SetupGenesisBlock(db, new(Genesis))
			},
			wantErr:    errGenesisNoConfig,
			wantConfig: params.AllMinervaProtocolChanges,
		},
		{
			name: "no block in DB, genesis == nil",
			fn: func(db abeydb.Database) (*params.ChainConfig, common.Hash, common.Hash, error) {
				return SetupGenesisBlock(db, nil)
			},
			wantHash:   params.MainnetGenesisHash,
			wantConfig: params.MainnetChainConfig,
		},
		{
			name: "mainnet block in DB, genesis == nil",
			fn: func(db abeydb.Database) (*params.ChainConfig, common.Hash, common.Hash, error) {
				DefaultGenesisBlock().MustFastCommit(db)
				DefaultGenesisBlock().MustSnailCommit(db)
				return SetupGenesisBlock(db, nil)
			},
			wantHash:   params.MainnetGenesisHash,
			wantConfig: params.MainnetChainConfig,
		},
		{
			name: "custom block in DB, genesis == testnet",
			fn: func(db abeydb.Database) (*params.ChainConfig, common.Hash, common.Hash, error) {
				customg.MustFastCommit(db)
				customg.MustSnailCommit(db)
				return SetupGenesisBlock(db, DefaultTestnetGenesisBlock())
			},
			wantErr:    &GenesisMismatchError{Stored: customghash, New: params.TestnetGenesisHash},
			wantHash:   params.TestnetGenesisHash,
			wantConfig: params.TestnetChainConfig,
		},
		// {
		// 	name: "compatible config in DB",
		// 	fn: func(db abeydb.Database) (*params.ChainConfig, common.Hash, error, *params.ChainConfig, common.Hash, error) {
		// 		oldcustomg.MustFastCommit(db)
		// 		oldcustomg.MustSnailCommit(db)
		// 		return SetupGenesisBlock(db, &customg)
		// 	},
		// 	wantHash:   customghash,
		// 	wantConfig: customg.Config,
		// },
		// {
		// 	name: "incompatible config in DB",
		// 	fn: func(db abeydb.Database) (*params.ChainConfig, common.Hash, error, *params.ChainConfig, common.Hash, error) {
		// 		// Commit the 'old' genesis block with Homestead transition at #2.
		// 		// Advance to block #4, past the homestead transition block of customg.
		// 		genesis := oldcustomg.MustFastCommit(db)

		// 		// bc, _ := NewFastBlockChain(db, nil, oldcustomg.Config, ethash.NewFullFaker(), vm.Config{})
		// 		// defer bc.Stop()

		// 		blocks, _ := GenerateChain(oldcustomg.Config, genesis, ethash.NewFaker(), db, 4, nil)
		// 		// bc.InsertChain(blocks)
		// 		// bc.CurrentBlock()
		// 		// This should return a compatibility error.
		// 		return SetupGenesisBlock(db, &customg)
		// 	},
		// 	wantHash:   customghash,
		// 	wantConfig: customg.Config,
		// 	wantErr: &params.ConfigCompatError{
		// 		What:         "Homestead fork block",
		// 		StoredConfig: big.NewInt(2),
		// 		NewConfig:    big.NewInt(3),
		// 		RewindTo:     1,
		// 	},
		// },
	}

	for _, test := range tests {
		db := abeydb.NewMemDatabase()
		config, hash, _, err := test.fn(db)
		config.TIP5 = nil
		// Check the return values.
		if !reflect.DeepEqual(err, test.wantErr) {
			spew := spew.ConfigState{DisablePointerAddresses: true, DisableCapacities: true}
			t.Errorf("%s: returned error %#v, want %#v", test.name, spew.NewFormatter(err), spew.NewFormatter(test.wantErr))
		}
		if !reflect.DeepEqual(config, test.wantConfig) {
			t.Errorf("%s:\nreturned %v\nwant     %v", test.name, config, test.wantConfig)
		}
		if hash != test.wantHash {
			t.Errorf("%s: returned hash %s, want %s", test.name, hash.Hex(), test.wantHash.Hex())
		} else if err == nil {
			// Check database content.
			stored := rawdb.ReadBlock(db, test.wantHash, 0)
			if stored.Hash() != test.wantHash {
				t.Errorf("%s: block in DB has hash %s, want %s", test.name, stored.Hash(), test.wantHash)
			}
		}
	}
}

func TestDefaultSnailGenesisBlock(t *testing.T) {
	block := DefaultGenesisBlock().ToSnailBlock(nil)
	if block.Hash() != params.MainnetSnailGenesisHash {
		fmt.Println(block.Hash().Hex())
		t.Errorf("wrong mainnet genesis hash, got %v, want %v", common.ToHex(block.Hash().Bytes()), params.MainnetSnailGenesisHash)
	}
	block = DefaultTestnetGenesisBlock().ToSnailBlock(nil)
	if block.Hash() != params.TestnetSnailGenesisHash {
		fmt.Println(block.Hash().Hex())
		t.Errorf("wrong testnet genesis hash, got %v, want %v", common.ToHex(block.Hash().Bytes()), params.TestnetSnailGenesisHash)
	}
	block = DefaultDevGenesisBlock().ToSnailBlock(nil)
	if block.Hash() != params.DevnetSnailGenesisHash {
		fmt.Println(block.Hash().Hex())
		t.Errorf("wrong testnet genesis hash, got %v, want %v", common.ToHex(block.Hash().Bytes()), params.TestnetSnailGenesisHash)
	}
}

func TestSetupSnailGenesis(t *testing.T) {
	var (
		//customghash = common.HexToHash("0x62e8674fcc8df82c74aad443e97c4cfdb748652ea117c8afe86cd4a04e5f44f8")
		customg = Genesis{
			Alloc: types.GenesisAlloc{
				{1}: {Balance: big.NewInt(1), Storage: map[common.Hash]common.Hash{{1}: {1}}},
			},
		}
		oldcustomg = customg
	)
	oldcustomg.Config = &params.ChainConfig{}
	tests := []struct {
		name       string
		fn         func(abeydb.Database) (*params.ChainConfig, common.Hash, common.Hash, error)
		wantConfig *params.ChainConfig
		wantHash   common.Hash
		wantErr    error
	}{
		{
			name: "genesis without ChainConfig",
			fn: func(db abeydb.Database) (*params.ChainConfig, common.Hash, common.Hash, error) {
				return SetupGenesisBlock(db, new(Genesis))
			},
			wantErr:    errGenesisNoConfig,
			wantConfig: params.AllMinervaProtocolChanges,
		},
		{
			name: "no block in DB, genesis == nil",
			fn: func(db abeydb.Database) (*params.ChainConfig, common.Hash, common.Hash, error) {
				return SetupGenesisBlock(db, nil)
			},
			wantHash:   params.MainnetSnailGenesisHash,
			wantConfig: params.MainnetChainConfig,
		},
		{
			name: "mainnet block in DB, genesis == nil",
			fn: func(db abeydb.Database) (*params.ChainConfig, common.Hash, common.Hash, error) {
				DefaultGenesisBlock().MustFastCommit(db)
				DefaultGenesisBlock().MustSnailCommit(db)
				return SetupGenesisBlock(db, nil)
			},
			wantHash:   params.MainnetSnailGenesisHash,
			wantConfig: params.MainnetChainConfig,
		},
		// {
		// 	name: "custom block in DB, genesis == testnet",
		// 	fn: func(db abeydb.Database) (*params.ChainConfig, common.Hash, common.Hash, error) {
		// 		//customg.MustFastCommit(db)
		// 		customg.MustSnailCommit(db)
		// 		return SetupGenesisBlock(db, DefaultTestnetGenesisBlock())
		// 	},
		// 	wantErr:    &GenesisMismatchError{Stored: customghash, New: params.TestnetSnailGenesisHash},
		// 	wantHash:   params.TestnetSnailGenesisHash,
		// 	wantConfig: params.TestnetChainConfig,
		// },
		// {
		// 	name: "compatible config in DB",
		// 	fn: func(db abeydb.Database) (*params.ChainConfig, common.Hash, error, *params.ChainConfig, common.Hash, error) {
		// 		oldcustomg.MustFastCommit(db)
		// 		oldcustomg.MustSnailCommit(db)
		// 		return SetupGenesisBlock(db, &customg)
		// 	},
		// 	wantHash:   customghash,
		// 	wantConfig: customg.Config,
		// },
		// {
		// 	name: "incompatible config in DB",
		// 	fn: func(db abeydb.Database) (*params.ChainConfig, common.Hash, error, *params.ChainConfig, common.Hash, error) {
		// 		// Commit the 'old' genesis block with Homestead transition at #2.
		// 		// Advance to block #4, past the homestead transition block of customg.
		// 		genesis := oldcustomg.MustFastCommit(db)

		// 		// bc, _ := NewFastBlockChain(db, nil, oldcustomg.Config, ethash.NewFullFaker(), vm.Config{})
		// 		// defer bc.Stop()

		// 		blocks, _ := GenerateChain(oldcustomg.Config, genesis, ethash.NewFaker(), db, 4, nil)
		// 		// bc.InsertChain(blocks)
		// 		// bc.CurrentBlock()
		// 		// This should return a compatibility error.
		// 		return SetupGenesisBlock(db, &customg)
		// 	},
		// 	wantHash:   customghash,
		// 	wantConfig: customg.Config,
		// 	wantErr: &params.ConfigCompatError{
		// 		What:         "Homestead fork block",
		// 		StoredConfig: big.NewInt(2),
		// 		NewConfig:    big.NewInt(3),
		// 		RewindTo:     1,
		// 	},
		// },
	}

	for _, test := range tests {
		db := abeydb.NewMemDatabase()
		config, _, hash, err := test.fn(db)
		// Check the return values.
		if !reflect.DeepEqual(err, test.wantErr) {
			spew := spew.ConfigState{DisablePointerAddresses: true, DisableCapacities: true}
			t.Errorf("%s: returned error %#v, want %#v", test.name, spew.NewFormatter(err), spew.NewFormatter(test.wantErr))
		}
		if !reflect.DeepEqual(config, test.wantConfig) {
			t.Errorf("%s:\nreturned %v\nwant     %v", test.name, config, test.wantConfig)
		}
		if hash != test.wantHash {
			t.Errorf("%s: returned hash %s, want %s", test.name, hash.Hex(), test.wantHash.Hex())
		} else if err == nil {
			// Check database content.
			stored := snaildb.ReadBlock(db, test.wantHash, 0)
			if stored.Hash() != test.wantHash {
				t.Errorf("%s: block in DB has hash %s, want %s", test.name, stored.Hash(), test.wantHash)
			}
		}
	}
}

var (
	root      = common.Hash{}
	key1      = "da5756ffa265ed55dcb741c97e8d3d2f36269df8afcae4b59b0b1f1f8eb58977"
	addr1     = "0x573baF2a36BFd683F1301db1EeBa1D55fd14De0A"
	balance1  = new(big.Int).Mul(big.NewInt(1000), big.NewInt(1e18))
	balance2  = new(big.Int).Mul(big.NewInt(100000), big.NewInt(1e18))
	balance3  = new(big.Int).Mul(big.NewInt(10), big.NewInt(1e18))
	gp        = new(GasPool).AddGas(new(big.Int).Mul(big.NewInt(1), big.NewInt(1e18)).Uint64())
	code      = `0x608060405234801561001057600080fd5b506040516020806101758339810180604052810190808051906020019092919050505060006a747275657374616b696e6790508073ffffffffffffffffffffffffffffffffffffffff1663e1254fba836040518263ffffffff167c0100000000000000000000000000000000000000000000000000000000028152600401808273ffffffffffffffffffffffffffffffffffffffff1673ffffffffffffffffffffffffffffffffffffffff168152602001915050606060405180830381600087803b1580156100de57600080fd5b505af11580156100f2573d6000803e3d6000fd5b505050506040513d606081101561010857600080fd5b8101908080519060200190929190805190602001909291908051906020019092919050505050505050506035806101406000396000f3006080604052600080fd00a165627a7a72305820a76679c2a9c73eeafffe41cfccde51b6b5150b920f6d90f25792987d9ab855c400290000000000000000000000006d348e0188cc2596aaa4046a1d50bb3ba50e8524`
	gasLimit  = uint64(3000000)
	allReward = new(big.Int).Mul(big.NewInt(100), big.NewInt(1e18))
)

func getFisrtState() *state.StateDB {
	db := abeydb.NewMemDatabase()
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(db))
	return statedb
}
func getBytes() []byte {
	return nil
}
func TestTip7(t *testing.T) {
	statedb := getFisrtState()
	toFirstBlock(statedb)

	fmt.Println("finish")
}
func Test08(t *testing.T) {
	fmt.Println("len(nil):", len(getBytes()))
	addr := common.HexToAddress("0x46498c274686be5e3c01b9268ea4604da5142265")
	fmt.Println("addr:", addr.Hex())
	fmt.Println("abey-addr:", addr.StringToAbey())
	fmt.Println("finish")
}
func TestOnceUpdateWhitelist(t *testing.T) {
	statedb := getFisrtState()
	addr0 := common.HexToAddress("0x751A86Bd48CAD1fa554928996aD2d404486C8B8D")
	whitelist := []common.Address{
		common.HexToAddress("0x8818d143773426071068C514Db25106338009363"),
		common.HexToAddress("0x4eD71f64C4Dbd037B02BC4E1bD6Fd6900fcFd396"),
	}
	b1 := big.NewInt(10000000)
	for _, addr := range whitelist {
		statedb.SetBalance(addr, b1)
	}

	consensus.OnceUpdateWhitelist(statedb, big.NewInt(1))
	for _, addr := range whitelist {
		b2 := statedb.GetBalance(addr)
		fmt.Println(addr.String(), b2.String())
	}
	consensus.OnceUpdateWhitelist(statedb, big.NewInt(5000000))
	for _, addr := range whitelist {
		b2 := statedb.GetBalance(addr)
		fmt.Println(addr.String(), b2.String())
	}
	consensus.OnceUpdateWhitelist(statedb, big.NewInt(5000001))
	for _, addr := range whitelist {
		b2 := statedb.GetBalance(addr)
		fmt.Println(addr.String(), b2.String())
	}

	b0 := statedb.GetBalance(addr0)
	if b0.Cmp(new(big.Int).Mul(b1, big.NewInt(2))) != 0 {
		panic("error .....")
	}
	for _, addr := range whitelist {
		b2 := statedb.GetBalance(addr)
		if b2.Sign() != 0 {
			panic("error2 .....")
		}
	}
	fmt.Println("finish")
}
func generateAddr() common.Address {
	priv, _ := crypto.GenerateKey()
	privHex := hex.EncodeToString(crypto.FromECDSA(priv))
	fmt.Println(privHex)
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	fmt.Println(addr.String())
	fmt.Println("finish")
	return addr
}

func toFirstBlock(statedb *state.StateDB) {
	statedb.AddBalance(common.HexToAddress(addr1), balance1)
	config := params.DevnetChainConfig
	consensus.OnceInitImpawnState(config, statedb, new(big.Int).SetUint64(0))
	root = statedb.IntermediateRoot(false)
	statedb.Commit(false)
	statedb.Database().TrieDB().Commit(root, true)

	addr := types.StakingAddress
	nonce := statedb.GetNonce(addr)
	codeHash := statedb.GetCodeHash(addr)
	codeSize := statedb.GetCodeSize(addr)
	fmt.Println("nonce:", nonce, "codehash:", codeHash, "codesize:", codeSize)
}
func makeDeployedTx() *types.Transaction {
	priv1, _ := crypto.HexToECDSA(key1)
	tx := types.NewContractCreation(0, big.NewInt(0), gasLimit,
		new(big.Int).Mul(big.NewInt(10), big.NewInt(1e10)), common.FromHex(code))
	tx, _ = types.SignTx(tx, types.NewTIP1Signer(big.NewInt(100)), priv1)
	return tx
}
func TestDeployedTx(t *testing.T) {

	var (
		db    = abeydb.NewMemDatabase()
		addr1 = common.HexToAddress(addr1)
		gspec = &Genesis{
			Config: params.DevnetChainConfig,
			Alloc:  types.GenesisAlloc{addr1: {Balance: balance1}},
		}
		genesis = gspec.MustFastCommit(db)
		pow     = minerva.NewFaker()
	)

	// This call generates a chain of 5 blocks. The function runs for
	// each block and adds different features to gen based on the
	// block index.
	chain, _ := GenerateChain(gspec.Config, genesis, pow, db, 1, func(i int, gen *BlockGen) {
		switch i {
		case 0:
			tx := makeDeployedTx()
			gen.AddTx(tx)
		}
	})

	// Import the chain. This runs all block validation rules.
	blockchain, _ := NewBlockChain(db, nil, gspec.Config, pow, vm.Config{})
	defer blockchain.Stop()

	if i, err := blockchain.InsertChain(chain); err != nil {
		fmt.Printf("insert error (block %d): %v\n", chain[i].NumberU64(), err)
		return
	}

	state, _ := blockchain.State()
	fmt.Printf("last block: #%d\n", blockchain.CurrentBlock().Number())
	fmt.Println("balance of addr1:", state.GetBalance(addr1))
	fmt.Println("finish")
}

type da struct {
	address common.Address
	amount  *big.Int
	reward  *big.Int
}
type daByAmount []*da

func (vs daByAmount) Len() int {
	return len(vs)
}
func (vs daByAmount) Less(i, j int) bool {
	return vs[i].amount.Cmp(vs[j].amount) < 0
}
func (vs daByAmount) Swap(i, j int) {
	it := vs[i]
	vs[i] = vs[j]
	vs[j] = it
}

type sa struct {
	address common.Address
	fee     *big.Int
	amount  *big.Int
	reward  *big.Int
	das     []*da
	pk      []byte
}

func (s *sa) getAllAmount() *big.Int {
	amount := new(big.Int).Set(s.amount)
	for _, v := range s.das {
		amount = amount.Add(amount, v.amount)
	}
	return amount
}
func (s *sa) getAmount() *big.Int {
	return s.amount
}

type saByAmount []*sa

func (vs saByAmount) Len() int {
	return len(vs)
}
func (vs saByAmount) Less(i, j int) bool {
	return vs[i].getAllAmount().Cmp(vs[j].getAllAmount()) < 0
}
func (vs saByAmount) Swap(i, j int) {
	it := vs[i]
	vs[i] = vs[j]
	vs[j] = it
}
func (vs saByAmount) getAllAmount() *big.Int {
	amount := big.NewInt(0)
	for _, v := range vs {
		amount = amount.Add(amount, v.getAllAmount())
	}
	return amount
}
func generatePK() []byte {
	key0, _ := crypto.GenerateKey()
	pk := crypto.FromECDSAPub(&key0.PublicKey)
	return pk
}

func TestReward(t *testing.T) {
	want := uint64(100)
	params.DposForkPoint = want

	var (
		accounts = []*sa{
			&sa{
				address: generateAddr(),
				fee:     big.NewInt(50),
				amount:  balance2,
				reward:  big.NewInt(0),
				pk:      generatePK(),
				das: []*da{
					&da{
						address: generateAddr(),
						amount:  balance1,
						reward:  big.NewInt(0),
					},
					&da{
						address: generateAddr(),
						amount:  balance1,
						reward:  big.NewInt(0),
					},
				},
			},
			&sa{
				address: generateAddr(),
				fee:     big.NewInt(20),
				amount:  balance2,
				reward:  big.NewInt(0),
				pk:      generatePK(),
				das: []*da{
					&da{
						address: generateAddr(),
						amount:  balance1,
						reward:  big.NewInt(0),
					},
					&da{
						address: generateAddr(),
						amount:  balance1,
						reward:  big.NewInt(0),
					},
				},
			},
		}
	)
	calcReward(accounts)

	impl := vm.NewImpawnImpl()
	for _, val := range accounts {
		impl.InsertSAccount2(want, 0, val.address, val.pk, val.amount, val.fee, true)
		for _, val2 := range val.das {
			impl.InsertDAccount2(want, val.address, val2.address, val2.amount)
		}
	}

	_, err := impl.DoElections(1, want)
	if err != nil {
		fmt.Println(err)
	}

	rinfo, _ := impl.Reward2(0, want, 1, allReward)

	for i, v := range accounts {
		wReward := getReward(v.address, rinfo)
		if wReward.Sign() > 0 && wReward.Cmp(v.reward) != 0 {
			fmt.Println("i:", i, "sa reward not match", "req:", v.reward, "res:", wReward)
		}
		for ii, vv := range v.das {
			wReward2 := getReward(vv.address, rinfo)
			if wReward2.Sign() > 0 && wReward2.Cmp(vv.reward) != 0 {
				fmt.Println("i:", i, "j", ii, "da reward not match", "req:", vv.reward, "res:", wReward2)
			}
		}
	}
	fmt.Println("reward equal")
	fmt.Println("finish")
}
func calcReward(accounts []*sa) {
	sas := make([]*sa, 0, 0)
	for _, v := range accounts {
		if params.ElectionMinLimitForStaking.Cmp(v.getAmount()) <= 0 {
			sas = append(sas, v)
		}
	}
	calcReward2(sas, allReward)
}
func calcReward2(accounts []*sa, allReward *big.Int) {
	sort.Sort(saByAmount(accounts))
	allStaking := saByAmount(accounts).getAllAmount()
	sum := len(accounts)
	left := big.NewInt(0)

	for i, v := range accounts {
		saAllStaking := v.getAllAmount()
		if saAllStaking.Sign() <= 0 {
			continue
		}

		v2 := new(big.Int).Quo(new(big.Int).Mul(saAllStaking, allReward), allStaking)
		if i == sum-1 {
			v2 = new(big.Int).Sub(allReward, left)
		}
		left = left.Add(left, v2)

		calcRewardInSa(v, v2)
	}
}
func calcRewardInSa(aa *sa, allReward *big.Int) {
	allStaking := aa.getAllAmount()
	fee := new(big.Int).Quo(new(big.Int).Mul(allReward, aa.fee), types.Base)
	all, left := new(big.Int).Sub(allReward, fee), big.NewInt(0)
	sort.Sort(daByAmount(aa.das))

	for _, v := range aa.das {
		daAll := v.amount
		if daAll.Sign() <= 0 {
			continue
		}
		v1 := new(big.Int).Quo(new(big.Int).Mul(all, daAll), allStaking)
		left = left.Add(left, v1)
		v.reward = v1
	}
	aa.reward = new(big.Int).Add(new(big.Int).Sub(all, left), fee)
}
func getReward(addr common.Address, infos []*types.SARewardInfos) *big.Int {
	reward := big.NewInt(0)
	for _, v := range infos {
		for _, vv := range v.Items {
			if bytes.Equal(vv.Address.Bytes(), addr.Bytes()) {
				return vv.Amount
			}
		}
	}
	return reward
}
func Test1(t *testing.T) {
	fmt.Println(generateAddr())
	fmt.Println("finish")
}
func Test03(t *testing.T) {
	a2 := common.HexToAddress("0x46498c274686be5e3c01b9268ea4604da5142265")
	fmt.Println("addr:", a2.Hex())
	fmt.Println("abey-addr:", a2.StringToAbey())
	b1 := a2.Bytes()
	b := make([]byte, 0, 3+len(b1)+4)
	b = append(b, 0x43, 0xe5, 0x52)
	b = append(b, b1[:]...)
	b2 := crypto.Keccak256(b)
	fmt.Println("b2", hex.EncodeToString(b2[:]))
	for i := 0; i < 10; i++ {
		addr := generateAddr()
		fmt.Println("addr:", addr.Hex())
		fmt.Println("abey-addr:", addr.StringToAbey())
		a := common.Address{}
		a.FromAbeyString(addr.StringToAbey())
		fmt.Println("addr2:", a.Hex())
	}
	fmt.Println("finish")
}

func Test04(t *testing.T) {
	var marshalErrorTests = []struct {
		Value interface{}
		Err   string
		Kind  reflect.Kind
	}{
		//{
		//	Value: make(chan bool),
		//	Err:   "xml: unsupported type: chan bool",
		//	Kind:  reflect.Chan,
		//},
		//{
		//	Value: map[string]string{
		//		"question": "What do you get when you multiply six by nine?",
		//		"answer":   "42",
		//	},
		//	Err:  "xml: unsupported type: map[string]string",
		//	Kind: reflect.Map,
		//},
		{
			Value: map[common.Address]*types.BalanceInfo{
				generateAddr(): &types.BalanceInfo{
					Address: generateAddr(),
					Valid:   big.NewInt(1),
					Lock:    big.NewInt(0),
				},
			},
			Err:  "xml: unsupported type: map[*xml.Ship]bool",
			Kind: reflect.Map,
		},
	}

	for idx, test := range marshalErrorTests {
		data, err := xml.Marshal(test.Value)
		if err == nil {
			t.Errorf("#%d: marshal(%#v) = [success] %q, want error %v", idx, test.Value, data, test.Err)
			continue
		}
		if err.Error() != test.Err {
			t.Errorf("#%d: marshal(%#v) = [error] %v, want %v", idx, test.Value, err, test.Err)
		}
		if test.Kind != reflect.Invalid {
			if kind := err.(*xml.UnsupportedTypeError).Type.Kind(); kind != test.Kind {
				t.Errorf("#%d: marshal(%#v) = [error kind] %s, want %s", idx, test.Value, kind, test.Kind)
			}
		}
	}
}

func dump(w io.Writer, val interface{}) error {
	je := json.NewEncoder(w)
	return je.Encode(val)
}

func Test05(t *testing.T) {
	Value := map[common.Address]*types.BalanceInfo{
		generateAddr(): &types.BalanceInfo{
			Address: generateAddr(),
			Valid:   big.NewInt(1),
			Lock:    big.NewInt(0),
		},
	}
	err := dump(os.Stdout, Value)
	if err != nil {
		fmt.Println(err)
	}
}
func TestRedeem(t *testing.T) {
	want := uint64(100)
	params.DposForkPoint = want

	var (
		accounts = []*sa{
			&sa{
				address: generateAddr(),
				fee:     big.NewInt(50),
				amount:  balance2,
				reward:  big.NewInt(0),
				pk:      generatePK(),
				das: []*da{
					&da{
						address: generateAddr(),
						amount:  balance1,
						reward:  big.NewInt(0),
					},
					&da{
						address: generateAddr(),
						amount:  balance1,
						reward:  big.NewInt(0),
					},
				},
			},
			&sa{
				address: generateAddr(),
				fee:     big.NewInt(20),
				amount:  balance2,
				reward:  big.NewInt(0),
				pk:      generatePK(),
				das: []*da{
					&da{
						address: generateAddr(),
						amount:  balance1,
						reward:  big.NewInt(0),
					},
					&da{
						address: generateAddr(),
						amount:  balance1,
						reward:  big.NewInt(0),
					},
				},
			},
		}
	)

	impl := vm.NewImpawnImpl()
	for _, val := range accounts {
		impl.InsertSAccount2(want-5, 0, val.address, val.pk, val.amount, val.fee, true)
		for _, val2 := range val.das {
			impl.InsertDAccount2(want-5, val.address, val2.address, val2.amount)
		}
	}

	for i, val := range accounts {
		if i%2 == 0 {
			impl.AppendSAAmount(want-2, val.address, balance2)
		}
	}

	_, err := impl.DoElections(1, want)
	if err != nil {
		fmt.Println(err)
	}

	if err := impl.Shift(1, 0); err != nil {
		fmt.Println("shift error:", err)
	}

	for i, val := range accounts {
		if i%2 == 0 {
			impl.CancelSAccount(want+2, val.address, balance3)
			for j, val2 := range val.das {
				if j%2 == 0 {
					impl.CancelDAccount(want+2, val.address, val2.address, balance3)
				}
			}
		}
	}

	for i, aa := range accounts {
		fmt.Println("i", i, "display staking..........")
		res1 := impl.GetStakingAsset(aa.address)
		displayStakingAsset(res1, false)
		res2 := impl.GetLockedAsset2(aa.address, uint64(1000))
		displayLockedAsset(res2, uint64(1000))
		for j, vv := range aa.das {
			fmt.Println("i", i, "j", j, "display delegation..........")
			res3 := impl.GetStakingAsset(vv.address)
			displayStakingAsset(res3, false)
			res4 := impl.GetLockedAsset2(aa.address, uint64(1000))
			displayLockedAsset(res4, uint64(1000))
		}
	}

	fmt.Println("finish")
}

func displayStakingAsset(infos map[common.Address]*types.StakingValue, lock bool) {
	for k, v := range infos {
		fmt.Println("address:", k.String(), "staking amount info.................")
		for kk, vv := range v.Value {
			if !lock {
				fmt.Println("staking value:", "height:", kk, "value:", vv)
			} else {
				fmt.Println("locked value:", "epochid:", kk, "value:", vv)
			}
		}
		fmt.Println("address:", k.String(), "staking amount info.................")
	}
}
func displayLockedAsset(infos map[common.Address]*types.LockedValue, height uint64) {
	for k, v := range infos {
		fmt.Println("address:", k.String(), "staking amount in locked info.................")
		for kk, vv := range v.Value {
			if vv.Locked {
				e := types.GetEpochFromID(kk + 1)
				last := e.BeginHeight + params.MaxRedeemHeight - height
				last = last * 5
				tt := new(big.Float).Quo(big.NewFloat(float64(last)), big.NewFloat(float64(86400)))
				fmt.Println("locked value:", "epochid:", kk, "value:", vv.Amount, "locked time:",
					tt.Text('f', 6), "days")
				continue
			}
			fmt.Println("locked value:", "epochid:", kk, "value:", vv.Amount, "locked:", vv.Locked)
		}
		fmt.Println("address:", k.String(), "staking amount in locked info.................")
	}
}
