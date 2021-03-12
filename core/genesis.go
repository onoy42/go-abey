// Copyright 2014 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/abeychain/go-abey/core/vm"

	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/common/hexutil"
	"github.com/abeychain/go-abey/common/math"
	"github.com/abeychain/go-abey/consensus"
	"github.com/abeychain/go-abey/core/rawdb"
	snaildb "github.com/abeychain/go-abey/core/snailchain/rawdb"
	"github.com/abeychain/go-abey/core/state"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/crypto"
	"github.com/abeychain/go-abey/abeydb"
	"github.com/abeychain/go-abey/log"
	"github.com/abeychain/go-abey/params"
	"github.com/abeychain/go-abey/rlp"
)

//go:generate gencodec -type Genesis -field-override genesisSpecMarshaling -out gen_genesis.go
//go:generate gencodec -type GenesisAccount -field-override genesisAccountMarshaling -out gen_genesis_account.go

var errGenesisNoConfig = errors.New("genesis has no chain configuration")

// Genesis specifies the header fields, state of a genesis block. It also defines hard
// fork switch-over blocks through the chain configuration.
type Genesis struct {
	Config     *params.ChainConfig      `json:"config"`
	Nonce      uint64                   `json:"nonce"`
	Timestamp  uint64                   `json:"timestamp"`
	ExtraData  []byte                   `json:"extraData"`
	GasLimit   uint64                   `json:"gasLimit"   gencodec:"required"`
	Difficulty *big.Int                 `json:"difficulty" gencodec:"required"`
	Mixhash    common.Hash              `json:"mixHash"`
	Coinbase   common.Address           `json:"coinbase"`
	Alloc      types.GenesisAlloc       `json:"alloc"      gencodec:"required"`
	Committee  []*types.CommitteeMember `json:"committee"      gencodec:"required"`

	// These fields are used for consensus tests. Please don't use them
	// in actual genesis blocks.
	Number     uint64      `json:"number"`
	GasUsed    uint64      `json:"gasUsed"`
	ParentHash common.Hash `json:"parentHash"`
}

// GenesisAccount is an account in the state of the genesis block.
type GenesisAccount struct {
	Code       []byte                      `json:"code,omitempty"`
	Storage    map[common.Hash]common.Hash `json:"storage,omitempty"`
	Balance    *big.Int                    `json:"balance" gencodec:"required"`
	Nonce      uint64                      `json:"nonce,omitempty"`
	PrivateKey []byte                      `json:"secretKey,omitempty"` // for tests
}

// field type overrides for gencodec
type genesisSpecMarshaling struct {
	Nonce      math.HexOrDecimal64
	Timestamp  math.HexOrDecimal64
	ExtraData  hexutil.Bytes
	GasLimit   math.HexOrDecimal64
	GasUsed    math.HexOrDecimal64
	Number     math.HexOrDecimal64
	Difficulty *math.HexOrDecimal256
	Alloc      map[common.UnprefixedAddress]GenesisAccount
}

type genesisAccountMarshaling struct {
	Code       hexutil.Bytes
	Balance    *math.HexOrDecimal256
	Nonce      math.HexOrDecimal64
	Storage    map[storageJSON]storageJSON
	PrivateKey hexutil.Bytes
}

// storageJSON represents a 256 bit byte array, but allows less than 256 bits when
// unmarshaling from hex.
type storageJSON common.Hash

func (h *storageJSON) UnmarshalText(text []byte) error {
	text = bytes.TrimPrefix(text, []byte("0x"))
	if len(text) > 64 {
		return fmt.Errorf("too many hex characters in storage key/value %q", text)
	}
	offset := len(h) - len(text)/2 // pad on the left
	if _, err := hex.Decode(h[offset:], text); err != nil {
		fmt.Println(err)
		return fmt.Errorf("invalid hex storage key/value %q", text)
	}
	return nil
}

func (h storageJSON) MarshalText() ([]byte, error) {
	return hexutil.Bytes(h[:]).MarshalText()
}

// GenesisMismatchError is raised when trying to overwrite an existing
// genesis block with an incompatible one.
type GenesisMismatchError struct {
	Stored, New common.Hash
}

func (e *GenesisMismatchError) Error() string {
	return fmt.Sprintf("database already contains an incompatible genesis block (have %x, new %x)", e.Stored[:8], e.New[:8])
}

// SetupGenesisBlock writes or updates the genesis block in db.
// The block that will be used is:
//
//                          genesis == nil       genesis != nil
//                       +------------------------------------------
//     db has no genesis |  main-net default  |  genesis
//     db has genesis    |  from DB           |  genesis (if compatible)
//
// The stored chain configuration will be updated if it is compatible (i.e. does not
// specify a fork block below the local head block). In case of a conflict, the
// error is a *params.ConfigCompatError and the new, unwritten config is returned.
//
// The returned chain configuration is never nil.
func SetupGenesisBlock(db abeydb.Database, genesis *Genesis) (*params.ChainConfig, common.Hash, common.Hash, error) {
	if genesis != nil && genesis.Config == nil {
		return params.AllMinervaProtocolChanges, common.Hash{}, common.Hash{}, errGenesisNoConfig
	}

	fastConfig, fastHash, fastErr := setupFastGenesisBlock(db, genesis)
	_, snailHash, _ := setupSnailGenesisBlock(db, genesis)

	return fastConfig, fastHash, snailHash, fastErr

}

// setupFastGenesisBlock writes or updates the fast genesis block in db.
// The block that will be used is:
//
//                          genesis == nil       genesis != nil
//                       +------------------------------------------
//     db has no genesis |  main-net default  |  genesis
//     db has genesis    |  from DB           |  genesis (if compatible)
//
// The stored chain configuration will be updated if it is compatible (i.e. does not
// specify a fork block below the local head block). In case of a conflict, the
// error is a *params.ConfigCompatError and the new, unwritten config is returned.
//
// The returned chain configuration is never nil.
func setupFastGenesisBlock(db abeydb.Database, genesis *Genesis) (*params.ChainConfig, common.Hash, error) {
	if genesis != nil && genesis.Config == nil {
		return params.AllMinervaProtocolChanges, common.Hash{}, errGenesisNoConfig
	}

	// Just commit the new block if there is no stored genesis block.
	stored := rawdb.ReadCanonicalHash(db, 0)
	if (stored == common.Hash{}) {
		if genesis == nil {
			log.Info("Writing default main-net genesis block")
			genesis = DefaultGenesisBlock()
		} else {
			log.Info("Writing custom genesis block")
		}
		block, err := genesis.CommitFast(db)
		return genesis.Config, block.Hash(), err
	}

	// Check whether the genesis block is already written.
	if genesis != nil {
		hash := genesis.ToFastBlock(nil).Hash()
		if hash != stored {
			return genesis.Config, hash, &GenesisMismatchError{stored, hash}
		}
	}

	// Get the existing chain configuration.
	newcfg := genesis.configOrDefault(stored)
	storedcfg := rawdb.ReadChainConfig(db, stored)
	if storedcfg == nil {
		log.Warn("Found genesis block without chain config")
		rawdb.WriteChainConfig(db, stored, newcfg)
		return newcfg, stored, nil
	}
	// Special case: don't change the existing config of a non-mainnet chain if no new
	// config is supplied. These chains would get AllProtocolChanges (and a compat error)
	// if we just continued here.
	if genesis == nil && stored != params.MainnetGenesisHash {
		return storedcfg, stored, nil
	}

	// Check config compatibility and write the config. Compatibility errors
	// are returned to the caller unless we're already at block zero.
	height := rawdb.ReadHeaderNumber(db, rawdb.ReadHeadHeaderHash(db))
	if height == nil {
		return newcfg, stored, fmt.Errorf("missing block number for head header hash")
	}
	compatErr := storedcfg.CheckCompatible(newcfg, *height)
	if compatErr != nil && *height != 0 && compatErr.RewindTo != 0 {
		return newcfg, stored, compatErr
	}
	rawdb.WriteChainConfig(db, stored, newcfg)
	return newcfg, stored, nil
}

// CommitFast writes the block and state of a genesis specification to the database.
// The block is committed as the canonical head block.
func (g *Genesis) CommitFast(db abeydb.Database) (*types.Block, error) {
	block := g.ToFastBlock(db)
	if block.Number().Sign() != 0 {
		return nil, fmt.Errorf("can't commit genesis block with number > 0")
	}
	rawdb.WriteBlock(db, block)
	rawdb.WriteReceipts(db, block.Hash(), block.NumberU64(), nil)
	rawdb.WriteCanonicalHash(db, block.Hash(), block.NumberU64())
	rawdb.WriteHeadBlockHash(db, block.Hash())
	rawdb.WriteHeadHeaderHash(db, block.Hash())
	rawdb.WriteStateGcBR(db, block.NumberU64())

	config := g.Config
	if config == nil {
		config = params.AllMinervaProtocolChanges
	}
	rawdb.WriteChainConfig(db, block.Hash(), config)
	return block, nil
}

// ToFastBlock creates the genesis block and writes state of a genesis specification
// to the given database (or discards it if nil).
func (g *Genesis) ToFastBlock(db abeydb.Database) *types.Block {
	if db == nil {
		db = abeydb.NewMemDatabase()
	}
	statedb, _ := state.New(common.Hash{}, state.NewDatabase(db))
	for addr, account := range g.Alloc {
		statedb.AddBalance(addr, account.Balance)
		statedb.SetCode(addr, account.Code)
		statedb.SetNonce(addr, account.Nonce)
		for key, value := range account.Storage {
			statedb.SetState(addr, key, value)
		}
	}
	consensus.OnceInitImpawnState(g.Config, statedb, new(big.Int).SetUint64(g.Number))
	if consensus.IsTIP8(new(big.Int).SetUint64(g.Number), g.Config, nil) {
		impl := vm.NewImpawnImpl()
		hh := g.Number
		if hh != 0 {
			hh = hh - 1
		}
		for _, member := range g.Committee {
			err := impl.InsertSAccount2(hh, 0, member.Coinbase, member.Publickey, params.ElectionMinLimitForStaking, big.NewInt(100), true)
			if err != nil {
				log.Error("ToFastBlock InsertSAccount", "error", err)
			}
			statedb.AddBalance(types.StakingAddress, big.NewInt(1000000000000000000))
		}
		_, err := impl.DoElections(1, 0)
		if err != nil {
			log.Error("ToFastBlock DoElections", "error", err)
		}
		err = impl.Shift(1, 0)
		if err != nil {
			log.Error("ToFastBlock Shift", "error", err)
		}
		err = impl.Save(statedb, types.StakingAddress)
		if err != nil {
			log.Error("ToFastBlock IMPL Save", "error", err)
		}
	}

	root := statedb.IntermediateRoot(false)

	head := &types.Header{
		Number:     new(big.Int).SetUint64(g.Number),
		Time:       new(big.Int).SetUint64(g.Timestamp),
		ParentHash: g.ParentHash,
		Extra:      g.ExtraData,
		GasLimit:   g.GasLimit,
		GasUsed:    g.GasUsed,
		Root:       root,
	}
	if g.GasLimit == 0 {
		head.GasLimit = params.GenesisGasLimit
	}
	statedb.Commit(false)
	statedb.Database().TrieDB().Commit(root, true)

	// All genesis committee members are included in switchinfo of block #0
	committee := &types.SwitchInfos{CID: common.Big0, Members: g.Committee, BackMembers: make([]*types.CommitteeMember, 0), Vals: make([]*types.SwitchEnter, 0)}
	for _, member := range committee.Members {
		pubkey, _ := crypto.UnmarshalPubkey(member.Publickey)
		member.Flag = types.StateUsedFlag
		member.MType = types.TypeFixed
		member.CommitteeBase = crypto.PubkeyToAddress(*pubkey)
	}
	return types.NewBlock(head, nil, nil, nil, committee.Members)
}

// MustFastCommit writes the genesis block and state to db, panicking on error.
// The block is committed as the canonical head block.
func (g *Genesis) MustFastCommit(db abeydb.Database) *types.Block {
	block, err := g.CommitFast(db)
	if err != nil {
		panic(err)
	}
	return block
}

// setupSnailGenesisBlock writes or updates the genesis snail block in db.
// The block that will be used is:
//
//                          genesis == nil       genesis != nil
//                       +------------------------------------------
//     db has no genesis |  main-net default  |  genesis
//     db has genesis    |  from DB           |  genesis (if compatible)
//
// The stored chain configuration will be updated if it is compatible (i.e. does not
// specify a fork block below the local head block). In case of a conflict, the
// error is a *params.ConfigCompatError and the new, unwritten config is returned.
//
// The returned chain configuration is never nil.
func setupSnailGenesisBlock(db abeydb.Database, genesis *Genesis) (*params.ChainConfig, common.Hash, error) {
	if genesis != nil && genesis.Config == nil {
		return params.AllMinervaProtocolChanges, common.Hash{}, errGenesisNoConfig
	}
	// Just commit the new block if there is no stored genesis block.
	stored := snaildb.ReadCanonicalHash(db, 0)
	if (stored == common.Hash{}) {
		if genesis == nil {
			log.Info("Writing default main-net genesis block")
			genesis = DefaultGenesisBlock()
		} else {
			log.Info("Writing custom genesis block")
		}
		block, err := genesis.CommitSnail(db)
		return genesis.Config, block.Hash(), err
	}

	// Check whether the genesis block is already written.
	if genesis != nil {
		hash := genesis.ToSnailBlock(nil).Hash()
		if hash != stored {
			return genesis.Config, hash, &GenesisMismatchError{stored, hash}
		}
	}

	// Get the existing chain configuration.
	newcfg := genesis.configOrDefault(stored)
	return newcfg, stored, nil
}

// ToSnailBlock creates the genesis block and writes state of a genesis specification
// to the given database (or discards it if nil).
func (g *Genesis) ToSnailBlock(db abeydb.Database) *types.SnailBlock {
	if db == nil {
		db = abeydb.NewMemDatabase()
	}

	head := &types.SnailHeader{
		Number:     new(big.Int).SetUint64(g.Number),
		Nonce:      types.EncodeNonce(g.Nonce),
		Time:       new(big.Int).SetUint64(g.Timestamp),
		ParentHash: g.ParentHash,
		Extra:      g.ExtraData,
		Difficulty: g.Difficulty,
		MixDigest:  g.Mixhash,
		Coinbase:   g.Coinbase,
	}

	if g.Difficulty == nil {
		head.Difficulty = params.GenesisDifficulty
		g.Difficulty = params.GenesisDifficulty
	}

	fastBlock := g.ToFastBlock(db)
	fruitHead := &types.SnailHeader{
		Number:          new(big.Int).SetUint64(g.Number),
		Nonce:           types.EncodeNonce(g.Nonce),
		Time:            new(big.Int).SetUint64(g.Timestamp),
		ParentHash:      g.ParentHash,
		FastNumber:      fastBlock.Number(),
		FastHash:        fastBlock.Hash(),
		FruitDifficulty: new(big.Int).Div(g.Difficulty, params.FruitBlockRatio),
		Coinbase:        g.Coinbase,
	}
	fruit := types.NewSnailBlock(fruitHead, nil, nil, nil, g.Config)

	return types.NewSnailBlock(head, []*types.SnailBlock{fruit}, nil, nil, g.Config)
}

// CommitSnail writes the block and state of a genesis specification to the database.
// The block is committed as the canonical head block.
func (g *Genesis) CommitSnail(db abeydb.Database) (*types.SnailBlock, error) {
	block := g.ToSnailBlock(db)
	if block.Number().Sign() != 0 {
		return nil, fmt.Errorf("can't commit genesis block with number > 0")
	}
	snaildb.WriteTd(db, block.Hash(), block.NumberU64(), g.Difficulty)
	snaildb.WriteBlock(db, block)
	snaildb.WriteFtLookupEntries(db, block)
	snaildb.WriteCanonicalHash(db, block.Hash(), block.NumberU64())
	snaildb.WriteHeadBlockHash(db, block.Hash())
	snaildb.WriteHeadHeaderHash(db, block.Hash())

	// config := g.Config
	// if config == nil {
	// 	config = params.AllMinervaProtocolChanges
	// }
	// snaildb.WriteChainConfig(db, block.Hash(), config)
	return block, nil
}

// MustSnailCommit writes the genesis block and state to db, panicking on error.
// The block is committed as the canonical head block.
func (g *Genesis) MustSnailCommit(db abeydb.Database) *types.SnailBlock {
	block, err := g.CommitSnail(db)
	if err != nil {
		panic(err)
	}
	return block
}

// DefaultGenesisBlock returns the Abeychain main net snail block.
func DefaultGenesisBlock() *Genesis {
	i, _ := new(big.Int).SetString("65750000000000000000000000", 10)
	j, _ := new(big.Int).SetString("8250000000000000000000000", 10)
	key1 := hexutil.MustDecode("0x0406e9c1f797fe21229f8146f5ecf837a545e4d7e96dc88903286ce3036f425f307c88418f902c9b08fd07e0aee0f249994cf19819235fd607acd38ce77f777d1a")
	key2 := hexutil.MustDecode("0x04ce1b2f41acdb293408da34a84162bc313be4b8682c183c7bdd4891ac87c514549a14ac564a9de615e7e8eae75441b1332042a2d00160079b2714b4bed1665f29")
	key3 := hexutil.MustDecode("0x045e020f6f27adf1bdc8e682c6fb7a7623d8a5899fa88702d436b18e245e35dd4b573c28565b4105abb48a14d5aa442326c56a9eb2fa3f8509aea3f5625cfd621b")
	key4 := hexutil.MustDecode("0x04028b42cb580bd78579441c96fb25b54b284c3f3258bb7ec9e37828f716cfe2ca4fb3dabc51b3d4215f14715a0999c86ae9ce4bc4e4bccfbcf7b64b6969746fb8")
	key5 := hexutil.MustDecode("0x04a36c5cc785b10b8d5c7f18f6387f511c060ca0760c06b816db5cdb087723d185f034988ce1a117cd81138f7c971d272a6b6e8affa49ccf82df0d0388c644a6c1")
	key6 := hexutil.MustDecode("0x049b240252750233ffb2e2fb0872e9dc8029a0af2a0bf8cb494181eae1f2673d662bbbb56215a42cb509ec42e80b089e2d6be581d084f54efe094a3fba3e990717")
	key7 := hexutil.MustDecode("0x048a0560f53440a84bad6286eb65a756a1a1880492e19f7500e4f3ef760b939b9fafb9e14660ce5fc7a90d45136117690beac13121d22952d054d4727b39764468")
	key8 := hexutil.MustDecode("0x0409e96160f03587376c3dff6a3b2f8d6028afc25668aa653d7f1bfe9eff8fb1165474cb93d8b2f292a6e4364d5447ca28002ae211b1624e33447864511e1d4d5d")
	key9 := hexutil.MustDecode("0x040d721ac02c250be372156b4bf2620e6bec1799c70105705fc8aee7f11a11b2a6697086ca2161176b85b663f2d9b98275644fd3971af5d08fd7f6070f45314f55")
	key10 := hexutil.MustDecode("0x04487dc07260059573abe6e7bf3209c975985c49400092ec246d28cc4d1eb54a6f7f5eb8375a2f7d398f1e0bb75406e5d38451935cc16376ddbac5d057c66a231c")
	key11 := hexutil.MustDecode("0x04f3611f44cd7913fbd2452040716e13c8759743dd44a566e94df1f81078234a45d36259ede0186cbbec3e2e7bd638d7fca1586ddf47d596bb41c668e39021556a")
	key12 := hexutil.MustDecode("0x0465c75fa5e80eabd141b08a4345573d43d582b42aed524025e7dcac4919bc16eee14705f49da7f381747a5196de792ba4a11ede9079a9025e11bcb0930760ef2e")
	key13 := hexutil.MustDecode("0x049a943801bd862f0287eacb8221f13bff351c63b7ab78c9a1f71472ae8a8c28779f32c4dcfd904f36d9edb54d8a0c57462654026cde4f5022fe2d99b63174b9ae")
	key14 := hexutil.MustDecode("0x04c4e01103818ca955c9219000c297c928b02b89d0eb3043886f524d645e20251343bf117d5a1b553708638c7dca8d1a12fb6379ae2d20756b57fdc5052a0dd787")

	return &Genesis{
		Config:     params.MainnetChainConfig,
		Nonce:      330,
		ExtraData:  hexutil.MustDecode("0x54727565436861696E204D61696E4E6574"),
		GasLimit:   16777216,
		Difficulty: big.NewInt(2147483648),
		//Timestamp:  1553918400,
		Coinbase:   common.HexToAddress("0x0000000000000000000000000000000000000000"),
		Mixhash:    common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
		ParentHash: common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
		//Alloc:      decodePrealloc(mainnetAllocData),
		Alloc: map[common.Address]types.GenesisAccount{
			common.HexToAddress("0xa5F41eaf51d24c8eDcDF254F200f8a6D818a6836"): {Balance: i},
			common.HexToAddress("0xbD1edee3bdD812BB5058Df1F1392dDdd99dE58cc"): {Balance: j},
		},
		Committee: []*types.CommitteeMember{
			&types.CommitteeMember{Coinbase: common.HexToAddress("0xfC5659050350eB76F9Ebcc6c2b1598C3a2fFc625"), Publickey: key1},
			&types.CommitteeMember{Coinbase: common.HexToAddress("0xfC5659050350eB76F9Ebcc6c2b1598C3a2fFc625"), Publickey: key2},
			&types.CommitteeMember{Coinbase: common.HexToAddress("0xfC5659050350eB76F9Ebcc6c2b1598C3a2fFc625"), Publickey: key3},
			&types.CommitteeMember{Coinbase: common.HexToAddress("0xfC5659050350eB76F9Ebcc6c2b1598C3a2fFc625"), Publickey: key4},
			&types.CommitteeMember{Coinbase: common.HexToAddress("0xfC5659050350eB76F9Ebcc6c2b1598C3a2fFc625"), Publickey: key5},
			&types.CommitteeMember{Coinbase: common.HexToAddress("0xfC5659050350eB76F9Ebcc6c2b1598C3a2fFc625"), Publickey: key6},
			&types.CommitteeMember{Coinbase: common.HexToAddress("0xfC5659050350eB76F9Ebcc6c2b1598C3a2fFc625"), Publickey: key7},
			&types.CommitteeMember{Coinbase: common.HexToAddress("0xfC5659050350eB76F9Ebcc6c2b1598C3a2fFc625"), Publickey: key8},
			&types.CommitteeMember{Coinbase: common.HexToAddress("0xfC5659050350eB76F9Ebcc6c2b1598C3a2fFc625"), Publickey: key9},
			&types.CommitteeMember{Coinbase: common.HexToAddress("0xfC5659050350eB76F9Ebcc6c2b1598C3a2fFc625"), Publickey: key10},
			&types.CommitteeMember{Coinbase: common.HexToAddress("0xfC5659050350eB76F9Ebcc6c2b1598C3a2fFc625"), Publickey: key11},
			&types.CommitteeMember{Coinbase: common.HexToAddress("0xfC5659050350eB76F9Ebcc6c2b1598C3a2fFc625"), Publickey: key12},
			&types.CommitteeMember{Coinbase: common.HexToAddress("0xfC5659050350eB76F9Ebcc6c2b1598C3a2fFc625"), Publickey: key13},
			&types.CommitteeMember{Coinbase: common.HexToAddress("0xfC5659050350eB76F9Ebcc6c2b1598C3a2fFc625"), Publickey: key14},
		},
	}
}

func (g *Genesis) configOrDefault(ghash common.Hash) *params.ChainConfig {
	switch {
	case g != nil:
		return g.Config
	case ghash == params.MainnetGenesisHash:
		return params.MainnetChainConfig
	case ghash == params.MainnetSnailGenesisHash:
		return params.MainnetChainConfig
	case ghash == params.TestnetGenesisHash:
		return params.TestnetChainConfig
	case ghash == params.TestnetSnailGenesisHash:
		return params.TestnetChainConfig
	default:
		return params.AllMinervaProtocolChanges
	}
}

func decodePrealloc(data string) types.GenesisAlloc {
	var p []struct{ Addr, Balance *big.Int }
	if err := rlp.NewStream(strings.NewReader(data), 0).Decode(&p); err != nil {
		panic(err)
	}
	ga := make(types.GenesisAlloc, len(p))
	for _, account := range p {
		ga[common.BigToAddress(account.Addr)] = types.GenesisAccount{Balance: account.Balance}
	}
	return ga
}

// GenesisFastBlockForTesting creates and writes a block in which addr has the given wei balance.
func GenesisFastBlockForTesting(db abeydb.Database, addr common.Address, balance *big.Int) *types.Block {
	g := Genesis{Alloc: types.GenesisAlloc{addr: {Balance: balance}}, Config: params.AllMinervaProtocolChanges}
	return g.MustFastCommit(db)
}

// GenesisSnailBlockForTesting creates and writes a block in which addr has the given wei balance.
func GenesisSnailBlockForTesting(db abeydb.Database, addr common.Address, balance *big.Int) *types.SnailBlock {
	g := Genesis{Alloc: types.GenesisAlloc{addr: {Balance: balance}}, Config: params.AllMinervaProtocolChanges}
	return g.MustSnailCommit(db)
}

// DefaultDevGenesisBlock returns the Rinkeby network genesis block.
func DefaultDevGenesisBlock() *Genesis {
	i, _ := new(big.Int).SetString("90000000000000000000000", 10)
	// priv1: 55dcdfd62f565a66e1886959e82a365e4987ed0b405adc43614a42c3481edd1a
	// addr1: 0x3e3429F72450A39CE227026E8DdeF331E9973E4d
	key1 := hexutil.MustDecode("0x04600254af4ce74276f54b4f9df193f2cb72ed76b7341cb144f4d6f1408402dc10719eebdcb947ced9ac6fe9a690e004692db6222de7867cbab712246eb23a50b7")
	// priv2: a0eb966cae593e0d85c7eda4ad4815d0c857bee9a7085a8b19e52e3227138ae4
	// addr2: 0xf353ab1417177F766497bF716D7aAd4ECd5f36C8
	key2 := hexutil.MustDecode("0x043ae657860b05d119351eac9d2f4531811ade3895ee2df00661368ca528ee36ceb850315f7bb566c6bbebf765e2c15f6af16b253a4d3d930cca7a191ae14af80d")
	// priv3: 5b743d4234c54710a644ff93a6f5284af065d2a42fff5b51de73a7c13d427b1c
	// addr3: 0x8fF345746C3d3435a105538E4c024Af5FE700598
	key3 := hexutil.MustDecode("0x049e0a67955d69e28faabe654b4a8f85e7d32b32fd2687a080e6357b53ec9413ad4f472d979bdccfe21cb135c7e144ca90f2beeb728b06e59f80918c7e52fbc6ff")
	// priv4: 229ca04fb83ec698296037c7d2b04a731905df53b96c260555cbeed9e4c64036
	// addr4: 0xf0C8898B2016Afa0Ec5912413ebe403930446779
	key4 := hexutil.MustDecode("0x04718502f879a949ca5fa29f78f1d3cef362ecdc36ee42a3023cca80371c2e1936d1f632a0ec5bf5edb2af228a5ba1669d31ea55df87548de172e5767b9201097d")

	return &Genesis{
		Config:     params.DevnetChainConfig,
		Nonce:      928,
		ExtraData:  nil,
		GasLimit:   88080384,
		Difficulty: big.NewInt(20000),
		//Alloc:      decodePrealloc(mainnetAllocData),
		Alloc: map[common.Address]types.GenesisAccount{
			common.HexToAddress("0x3e3429F72450A39CE227026E8DdeF331E9973E4d"): {Balance: i},
			common.HexToAddress("0xf353ab1417177F766497bF716D7aAd4ECd5f36C8"): {Balance: i},
			common.HexToAddress("0x8fF345746C3d3435a105538E4c024Af5FE700598"): {Balance: i},
			common.HexToAddress("0xf0C8898B2016Afa0Ec5912413ebe403930446779"): {Balance: i},
		},
		Committee: []*types.CommitteeMember{
			{Coinbase: common.HexToAddress("0x3e3429F72450A39CE227026E8DdeF331E9973E4d"), Publickey: key1},
			{Coinbase: common.HexToAddress("0x3e3429F72450A39CE227026E8DdeF331E9973E4d"), Publickey: key2},
			{Coinbase: common.HexToAddress("0x3e3429F72450A39CE227026E8DdeF331E9973E4d"), Publickey: key3},
			{Coinbase: common.HexToAddress("0x3e3429F72450A39CE227026E8DdeF331E9973E4d"), Publickey: key4},
		},
	}
}

func DefaultSingleNodeGenesisBlock() *Genesis {
	i, _ := new(big.Int).SetString("90000000000000000000000", 10)
	// priv: 229ca04fb83ec698296037c7d2b04a731905df53b96c260555cbeed9e4c64036
	key1 := hexutil.MustDecode("0x04718502f879a949ca5fa29f78f1d3cef362ecdc36ee42a3023cca80371c2e1936d1f632a0ec5bf5edb2af228a5ba1669d31ea55df87548de172e5767b9201097d")

	return &Genesis{
		Config:     params.SingleNodeChainConfig,
		Nonce:      66,
		ExtraData:  nil,
		GasLimit:   22020096,
		Difficulty: big.NewInt(256),
		//Alloc:      decodePrealloc(mainnetAllocData),
		Alloc: map[common.Address]types.GenesisAccount{
			common.HexToAddress("0xbd54a6c8298a70e9636d0555a77ffa412abdd71a"): {Balance: i},
			common.HexToAddress("0x3c2e0a65a023465090aaedaa6ed2975aec9ef7f9"): {Balance: i},
			common.HexToAddress("0x7c357530174275dd30e46319b89f71186256e4f7"): {Balance: i},
			common.HexToAddress("0xeeb69c67751e9f4917b605840fa9a28be4517871"): {Balance: i},
			common.HexToAddress("0x76ea2f3a002431fede1141b660dbb75c26ba6d97"): {Balance: i},
		},
		Committee: []*types.CommitteeMember{
			{Coinbase: common.HexToAddress("0xf0C8898B2016Afa0Ec5912413ebe403930446779"), Publickey: key1},
		},
	}
}

// DefaultTestnetGenesisBlock returns the Ropsten network genesis block.
func DefaultTestnetGenesisBlock() *Genesis {
	// priv1: 55dcdfd62f565a66e1886959e82a365e4987ed0b405adc43614a42c3481edd1a
	seedkey1 := hexutil.MustDecode("0x04600254af4ce74276f54b4f9df193f2cb72ed76b7341cb144f4d6f1408402dc10719eebdcb947ced9ac6fe9a690e004692db6222de7867cbab712246eb23a50b7")
	// priv2: a0eb966cae593e0d85c7eda4ad4815d0c857bee9a7085a8b19e52e3227138ae4
	seedkey2 := hexutil.MustDecode("0x043ae657860b05d119351eac9d2f4531811ade3895ee2df00661368ca528ee36ceb850315f7bb566c6bbebf765e2c15f6af16b253a4d3d930cca7a191ae14af80d")
	// priv3: 5b743d4234c54710a644ff93a6f5284af065d2a42fff5b51de73a7c13d427b1c
	seedkey3 := hexutil.MustDecode("0x049e0a67955d69e28faabe654b4a8f85e7d32b32fd2687a080e6357b53ec9413ad4f472d979bdccfe21cb135c7e144ca90f2beeb728b06e59f80918c7e52fbc6ff")
	// priv4: 229ca04fb83ec698296037c7d2b04a731905df53b96c260555cbeed9e4c64036
	seedkey4 := hexutil.MustDecode("0x04718502f879a949ca5fa29f78f1d3cef362ecdc36ee42a3023cca80371c2e1936d1f632a0ec5bf5edb2af228a5ba1669d31ea55df87548de172e5767b9201097d")

	// seed4
	coinbase := common.HexToAddress("0xf0C8898B2016Afa0Ec5912413ebe403930446779")
	amount1, _ := new(big.Int).SetString("24000000000000000000000000", 10)
	return &Genesis{
		Config:     params.TestnetChainConfig,
		Nonce:      0,
		ExtraData:  hexutil.MustDecode("0x54727565436861696E20546573744E6574203035"),
		GasLimit:   20971520,
		Difficulty: big.NewInt(100000),
		Timestamp:  1537891200,
		Coinbase:   common.HexToAddress("0x0000000000000000000000000000000000000000"),
		Mixhash:    common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
		ParentHash: common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
		Alloc: map[common.Address]types.GenesisAccount{
			common.HexToAddress("0xf0C8898B2016Afa0Ec5912413ebe403930446779"): {Balance: amount1},
		},
		Committee: []*types.CommitteeMember{
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey1},
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey2},
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey3},
			&types.CommitteeMember{Coinbase: coinbase, Publickey: seedkey4},
		},
	}
}
