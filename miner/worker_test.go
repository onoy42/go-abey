// Copyright 2018 The go-ethereum Authors
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

package miner

import (
	"fmt"
	"testing"

	"github.com/abeychain/go-abey/abeydb"
	"github.com/abeychain/go-abey/accounts"
	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/consensus"
	"github.com/abeychain/go-abey/consensus/minerva"
	"github.com/abeychain/go-abey/core"
	"github.com/abeychain/go-abey/core/snailchain"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/params"
)

var (
	testTxPoolConfig  core.TxPoolConfig
	ethashChainConfig *params.ChainConfig
	snailChainLocal   *snailchain.SnailBlockChain
	fastChainLocal    *core.BlockChain

	pendingTxs     []*types.Transaction
	newTxs         []*types.Transaction
	blockNum       int
	fastChainHight int
	coinbase       common.Address
)

func init() {
	blockNum = 10
	fastChainHight = 700
}

// testWorkerBackend implements worker.Backend interfaces and wraps all information needed during the testing.
type testWorkerBackend struct {
	db             abeydb.Database
	txPool         *core.TxPool
	chain          *snailchain.SnailBlockChain
	fastchain      *core.BlockChain
	uncleBlock     *types.Block
	snailPool      *snailchain.SnailPool
	accountManager *accounts.Manager
}

func newTestWorkerBackend(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine, n int) *testWorkerBackend {
	var (
		db      = abeydb.NewMemDatabase()
		genesis = core.DefaultGenesisBlock()
	)
	snailChainLocal, fastChainLocal = snailchain.MakeChain(fastChainHight, blockNum, genesis, minerva.NewFaker())
	//sv := snailchain.NewBlockValidator(chainConfig, fastChainLocal, snailChainLocal, engine)

	return &testWorkerBackend{
		db:        db,
		chain:     snailChainLocal,
		fastchain: fastChainLocal,
		snailPool: snailchain.NewSnailPool(snailchain.DefaultSnailPoolConfig, fastChainLocal, snailChainLocal, engine),
	}
}

func (b *testWorkerBackend) SnailBlockChain() *snailchain.SnailBlockChain { return b.chain }
func (b *testWorkerBackend) AccountManager() *accounts.Manager            { return b.accountManager }
func (b *testWorkerBackend) SnailGenesis() *types.SnailBlock              { return b.chain.GetBlockByNumber(0) }
func (b *testWorkerBackend) TxPool() *core.TxPool                         { return b.txPool }
func (b *testWorkerBackend) BlockChain() *core.BlockChain                 { return b.fastchain }
func (b *testWorkerBackend) ChainDb() abeydb.Database                     { return b.db }
func (b *testWorkerBackend) SnailPool() *snailchain.SnailPool             { return b.snailPool }

func newTestWorker(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine, blocks int) (*worker, *testWorkerBackend) {
	backend := newTestWorkerBackend(t, chainConfig, engine, blocks)

	w := newWorker(chainConfig, engine, coinbase, backend, nil)

	return w, backend
}

func TestCommitFastBlock(t *testing.T) {

	var (
		//fruitset1 []*types.SnailBlock  // nil situation
		fruitset2 []*types.SnailBlock // contine but not have 60
		fruitset3 []*types.SnailBlock // not contine   1 2 3  5 7 8
		fruitset4 []*types.SnailBlock // contine and langer then 60
		fruitset5 []*types.SnailBlock // frist one big then snailfruitslast fast numbe 10000 10001...
	)
	engine := minerva.NewFaker()

	chainDb := abeydb.NewMemDatabase()
	chainConfig, _, _, _ := core.SetupGenesisBlock(chainDb, core.DefaultGenesisBlock())
	//Miner := New(snailChainLocal, nil, nil, snailChainLocal.Engine(), nil, false, nil)
	worker, _ := newTestWorker(t, chainConfig, engine, 1)

	startFastNum := blockNum*params.MinimumFruits + 1
	gensisSnail := snailChainLocal.GetBlockByNumber(0)

	// situation 1   nil
	//fruitset1 = nil
	err0 := worker.CommitFastBlocksByWoker(nil, snailChainLocal, fastChainLocal, nil)
	if err0 != nil {
		fmt.Println("1 is err", err0)
	}

	// situation 2   1 2 3 4
	for i := startFastNum; i < (10 + startFastNum); i++ {

		fruit, _ := snailchain.MakeSnailBlockFruit(snailChainLocal, fastChainLocal, blockNum, i, gensisSnail.PublicKey(), gensisSnail.Coinbase(), false, nil)
		if fruit == nil {
			fmt.Println("fruit is nil  2")
		}
		fruitset2 = append(fruitset2, fruit)
	}

	err := worker.CommitFastBlocksByWoker(fruitset2, snailChainLocal, fastChainLocal, nil)
	if err != nil {
		fmt.Println("1 is err", err)
	}

	// situation 3   1 2 3 5 7
	j := 0
	for i := startFastNum; i < startFastNum+20; i++ {
		j++
		if j == 10 {
			continue
		}
		fruit, _ := snailchain.MakeSnailBlockFruit(snailChainLocal, fastChainLocal, blockNum, i, gensisSnail.PublicKey(), gensisSnail.Coinbase(), false, nil)
		if fruit == nil {
			fmt.Println("fruit is nil  3")
		}
		fruitset3 = append(fruitset3, fruit)
	}

	err2 := worker.CommitFastBlocksByWoker(fruitset2, snailChainLocal, fastChainLocal, nil)
	if err != nil {
		fmt.Println("2 is err", err2)
	}
	// situation 4   1 2 3...60
	for i := startFastNum; i < startFastNum+60; i++ {

		fruit, _ := snailchain.MakeSnailBlockFruit(snailChainLocal, fastChainLocal, blockNum, i, gensisSnail.PublicKey(), gensisSnail.Coinbase(), false, nil)
		if fruit == nil {
			fmt.Println("fruit is nil 4 ")
		}
		fruitset4 = append(fruitset4, fruit)
	}
	err3 := worker.CommitFastBlocksByWoker(fruitset2, snailChainLocal, fastChainLocal, nil)
	if err != nil {
		fmt.Println("2 is err", err3)
	}

	// situation 5   10000 10001...
	for i := fastChainHight; i < startFastNum+60; i++ {

		fruit, _ := snailchain.MakeSnailBlockFruit(snailChainLocal, fastChainLocal, blockNum, i, gensisSnail.PublicKey(), gensisSnail.Coinbase(), false, nil)
		if fruit == nil {
			fmt.Println("fruit is nil  5")
		}
		fruitset5 = append(fruitset5, fruit)
	}
	err5 := worker.CommitFastBlocksByWoker(fruitset2, snailChainLocal, fastChainLocal, nil)
	if err != nil {
		fmt.Println("2 is err", err5)
	}

}

func TestCommitFruits(t *testing.T) {

	var (
		//fruitset1 []*types.SnailBlock  // nil situation
		fruitset []*types.SnailBlock // contine but not have 60

	)
	engine := minerva.NewFaker()

	chainDb := abeydb.NewMemDatabase()
	chainConfig, _, _, _ := core.SetupGenesisBlock(chainDb, core.DefaultDevGenesisBlock())
	//Miner := New(snailChainLocal, nil, nil, snailChainLocal.Engine(), nil, false, nil)
	worker, _ := newTestWorker(t, chainConfig, engine, 1)

	startFastNum := blockNum*params.MinimumFruits + 1
	gensisSnail := snailChainLocal.GetBlockByNumber(0)

	//create some fruits but less then cureent block

	fruitNofresh, _ := snailchain.MakeSnailBlockFruit(snailChainLocal, fastChainLocal, 1, startFastNum+params.MinimumFruits+1, gensisSnail.PublicKey(), gensisSnail.Coinbase(), false, nil)

	for i := startFastNum; i < startFastNum+params.MinimumFruits; i++ {
		fruit, _ := snailchain.MakeSnailBlockFruit(snailChainLocal, fastChainLocal, startFastNum, i, gensisSnail.PublicKey(), gensisSnail.Coinbase(), false, nil)
		fruitset = append(fruitset, fruit)
	}
	fruitset = append(fruitset, fruitNofresh)

	worker.CommitFruits(fruitset, snailChainLocal, fastChainLocal, engine)
}

func TestMiner01(t *testing.T) {

}
