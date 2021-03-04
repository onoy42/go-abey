// Copyright 2018 The AbeyChain Authors
// This file is part of the abey library.
//
// The abey library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The abey library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the abey library. If not, see <http://www.gnu.org/licenses/>.

package election

import (
	"math/big"
	"bytes"
	"testing"

	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/consensus"
	"github.com/abeychain/go-abey/consensus/minerva"
	"github.com/abeychain/go-abey/core"
	"github.com/abeychain/go-abey/core/snailchain"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/abeydb"
	"github.com/abeychain/go-abey/params"
)

var (
	canonicalSeed = 1
)

func makeTestBlock() *types.Block {
	db := abeydb.NewMemDatabase()
	BaseGenesis := new(core.Genesis)
	genesis := BaseGenesis.MustFastCommit(db)
	header := &types.Header{
		ParentHash: genesis.Hash(),
		Number:     common.Big1,
		GasLimit:   0, //core.FastCalcGasLimit(genesis),
	}
	fb := types.NewBlock(header, nil, nil, nil, nil)
	return fb
}

type nodeType struct{}

func (nodeType) GetNodeType() bool { return false }

func TestElectionTestMode(t *testing.T) {
	// TestMode election return a local static committee, whose members are generated barely
	// by local node
	election := NewFakeElection()
	members := election.GetCommittee(common.Big1)
	if len(members) != params.MinimumCommitteeNumber {
		t.Errorf("Commit members count error %d", len(members))
	}
}

func TestVerifySigns(t *testing.T) {
	// TestMode election return a local static committee, whose members are generated barely
	// by local node
	election := NewFakeElection()
	pbftSigns, err := election.GenerateFakeSigns(makeTestBlock())
	if err != nil {
		t.Errorf("Generate fake sign failed")
	}
	members, errs := election.VerifySigns(pbftSigns)

	for _, m := range members {
		if m == nil {
			t.Errorf("Pbft fake signs get invalid member")
		}
	}
	for _, err := range errs {
		if err != nil {
			t.Errorf("Pbft fake signs failed, error=%v", err)
		}
	}
}

func committeeEqual(left, right []*types.CommitteeMember) bool {
	members := make(map[common.Address]*types.CommitteeMember)
	for _, l := range left {
		members[l.Coinbase] = l
	}
	for _, r := range right {
		if m, ok := members[r.Coinbase]; ok {
			if !bytes.Equal(m.Publickey, r.Publickey) {
				return false
			}
		} else {
			return false
		}
	}
	return true
}

func makeChain(n int) (*snailchain.SnailBlockChain, *core.BlockChain) {
	var (
	// 	testdb  = abeydb.NewMemDatabase()
		genesis = core.DefaultGenesisBlock()
	// 	engine  = minerva.NewFaker()
	)
	// fastGenesis := genesis.MustFastCommit(testdb)
	// fastchain, _ := core.NewBlockChain(testdb, nil, params.AllMinervaProtocolChanges, engine, vm.Config{})
	// fastblocks := makeFast(fastGenesis, n * params.MinimumFruits, engine, testdb, canonicalSeed)
	// fastchain.InsertChain(fastblocks)

	// snailGenesis := genesis.MustSnailCommit(testdb)
	// snail, _ := snailchain.NewSnailBlockChain(testdb, nil, params.TestChainConfig, engine, vm.Config{})
	// blocks := makeSnail(snail, fastchain, snailGenesis, n, engine, testdb, canonicalSeed)
	// snail.InsertChain(blocks)
	snail, fastchain := snailchain.MakeChain(n*params.MinimumFruits, n, genesis, minerva.NewFaker())

	return snail, fastchain
}

func makeSnail(snail *snailchain.SnailBlockChain, fastchain *core.BlockChain, parent *types.SnailBlock, n int, engine consensus.Engine, db abeydb.Database, seed int) []*types.SnailBlock {
	blocks, _ := snailchain.MakeSnailBlockFruits(snail, fastchain, 1, n, 1, n*params.MinimumFruits,
		parent.PublicKey(), parent.Coinbase(), true, big.NewInt(20000))
	return blocks
}

// makeBlockChain creates a deterministic chain of blocks rooted at parent.
func makeFast(parent *types.Block, n int, engine consensus.Engine, db abeydb.Database, seed int) []*types.Block {
	blocks, _ := core.GenerateChain(params.TestChainConfig, parent, engine, db, n, func(i int, b *core.BlockGen) {
		b.SetCoinbase(common.Address{0: byte(seed), 19: byte(i)})
	})

	return blocks
}

// func TestCommitteeMembers(t *testing.T) {
// 	snail, fast := makeChain(180)
// 	election := NewElection(fast, snail, nodeType{})
// 	members := election.electCommittee(big.NewInt(1), big.NewInt(144)).Members
// 	if len(members) == 0 {
// 		t.Errorf("Committee election get none member")
// 	}
// 	if int64(len(members)) > params.MaximumCommitteeNumber.Int64() {
// 		t.Errorf("Elected members exceed MAX member num")
// 	}
// }