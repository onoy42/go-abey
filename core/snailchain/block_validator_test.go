// Copyright 2015 The go-ethereum Authors
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

package snailchain

import (
	"reflect"
	"testing"

	"fmt"
	"github.com/davecgh/go-spew/spew"
	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/consensus"
	"github.com/abeychain/go-abey/consensus/minerva"
	"github.com/abeychain/go-abey/core"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/core/vm"
	"github.com/abeychain/go-abey/abeydb"
	"github.com/abeychain/go-abey/params"
)

func TestValidateBody(t *testing.T) {

	tests := []struct {
		name    string
		fn      func() error
		wantErr error
	}{
		{
			name: "valid",
			fn: func() error {
				snail, fast, block := makeChain(1, 0)
				t.Log("---the block info", "number", block.Number(), "hash", block.Hash())
				validator := NewBlockValidator(snail.chainConfig, fast, snail, snail.Engine())
				return validator.ValidateBody(block, true)
			},
			wantErr: nil,
		},
		{
			name: "HasBlockAndState",
			fn: func() error {
				snail, fast, _ := makeChain(1, 0)
				validator := NewBlockValidator(snail.chainConfig, fast, snail, snail.Engine())
				return validator.ValidateBody(validator.bc.CurrentBlock(), true)
			},
			wantErr: ErrKnownBlock,
		},
		// {
		// 	name: "ErrInvalidFruits",
		// 	fn: func() error {
		// 		snail, fast, block := makeChain(2, 1)
		// 		validator := NewBlockValidator(snail.chainConfig, fast, snail, snail.Engine())
		// 		return validator.ValidateBody(block)
		// 	},
		// 	wantErr: ErrInvalidFruits,
		// },
	}

	for _, test := range tests {

		err := test.fn()
		// Check the return values.
		if !reflect.DeepEqual(err, test.wantErr) {
			spew := spew.ConfigState{DisablePointerAddresses: true, DisableCapacities: true}
			t.Errorf("%s: returned error %#v, want %#v", test.name, spew.NewFormatter(err), spew.NewFormatter(test.wantErr))
		}

	}
}

func makeChain(n int, i int) (*SnailBlockChain, *core.BlockChain, *types.SnailBlock) {
	var (
		testdb = abeydb.NewMemDatabase()
		// genesis = new(core.Genesis).MustSnailCommit(testdb)
		genesis = core.DefaultGenesisBlock()
		engine  = minerva.NewFaker()
	)

	//blocks := make(types.SnailBlocks, 2)
	cache := &core.CacheConfig{}
	fastGenesis := genesis.MustFastCommit(testdb)
	fastchain, _ := core.NewBlockChain(testdb, cache, params.AllMinervaProtocolChanges, engine, vm.Config{})
	fastblocks, _ := core.GenerateChain(params.TestChainConfig, fastGenesis, engine, testdb, n*params.MinimumFruits, func(i int, b *core.BlockGen) {
		b.SetCoinbase(common.Address{0: byte(1), 19: byte(i)})
	})
	fastchain.InsertChain(fastblocks)
	/*fastblocks := makeFast(fastGenesis, n*params.MinimumFruits, engine, testdb, canonicalSeed)
	fastchain.InsertChain(fastblocks)*/

	snailGenesis := genesis.MustSnailCommit(testdb)
	snailChain, _ := NewSnailBlockChain(testdb, params.TestChainConfig, engine, fastchain)

	if _, error := snailChain.InsertChain(types.SnailBlocks{snailGenesis}); error != nil {
		panic(error)
	}

	mconfig := MakechianConfig{
		FruitNumber:     uint64(params.MinimumFruits),
		FruitFresh:      int64(7),
		DifficultyLevel: 1,
	}
	var pperents []*types.SnailBlock

	fmt.Println("the log is", "snailChain.CurrentBlock().Number()", snailChain.CurrentBlock().Number())

	for i := 0; i <= int(snailChain.CurrentBlock().Number().Uint64()); i++ {
		pperents = append(pperents, snailChain.GetBlockByNumber(uint64(i)))
	}
	//blocks1, err := MakeSnailBloocks(fastchain, snailChain, pperents, int64(n), mconfig)
	start := uint64(n)
	if snailChain.CurrentBlock().Number().Uint64() > uint64(n) {
		start = snailChain.CurrentBlock().Number().Uint64() + uint64(1)
	}
	block, err := makeSnailBlock(fastchain, snailChain, start, snailChain.CurrentBlock(), pperents, mconfig)

	//blocks1, err := MakeSnailBlockFruitsWithoutInsert(snailChain, fastchain, n, n*params.MinimumFruits, snailGenesis.PublicKey(), snailGenesis.Coinbase(), true, nil)

	if err != nil {
		return nil, nil, nil
	}
	//snailChain.InsertChain(blocks1)

	//InsertChain(blocks)

	return snailChain, fastchain, block
}

func makeSnail(fastChain *core.BlockChain, parent *types.SnailBlock, n int, engine consensus.Engine, db abeydb.Database, seed int) []*types.SnailBlock {
	blocks := GenerateChain(params.TestChainConfig, fastChain, []*types.SnailBlock{parent}, n, 7, func(i int, b *BlockGen) {
		b.SetCoinbase(common.Address{0: byte(seed), 19: byte(i)})
	})
	return blocks
}
