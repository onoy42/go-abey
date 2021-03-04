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

package snailchain

import (
	"fmt"
	"io/ioutil"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/consensus"
	"github.com/abeychain/go-abey/consensus/minerva"
	"github.com/abeychain/go-abey/core"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/core/vm"
	"github.com/abeychain/go-abey/abeydb"
	"github.com/abeychain/go-abey/params"
)

// testSnailPoolConfig is a fruit pool configuration without stateful disk
// sideeffects used during testing.
var testSnailPoolConfig SnailPoolConfig
var fastchainpool *core.BlockChain
var snailblockchain *SnailBlockChain
var enginepool consensus.Engine
var chainConfigpool *params.ChainConfig
var peerDbpool abeydb.Database // Database of the peers containing all data
var genesispool *core.Genesis
var snailGenesis *types.SnailBlock

func poolinit() {
	peerDbpool = abeydb.NewMemDatabase()
	testSnailPoolConfig = DefaultSnailPoolConfig
	chainConfigpool = params.TestChainConfig
	testSnailPoolConfig.Journal = ""
	enginepool = minerva.NewFaker()
	genesispool = core.DefaultGenesisBlock()

	cache := &core.CacheConfig{}

	fastGenesis := genesispool.MustFastCommit(peerDbpool)
	fastchainpool, _ = core.NewBlockChain(peerDbpool, cache, params.AllMinervaProtocolChanges, enginepool, vm.Config{})

	fastblocks, _ := core.GenerateChain(params.TestChainConfig, fastGenesis, enginepool, peerDbpool, 300, func(i int, b *core.BlockGen) {
		b.SetCoinbase(common.Address{0: byte(1), 19: byte(i)})
	})
	fastchainpool.InsertChain(fastblocks)

	snailGenesis = genesispool.MustSnailCommit(peerDbpool)
	snailblockchain, _ = NewSnailBlockChain(peerDbpool, params.TestChainConfig, enginepool, fastchainpool)
	/*if err != nil{
		fmt.Print(err)
	}*/
	blocks1, _ := MakeSnailBlockFruits(snailblockchain, fastchainpool, 1, 3, 1, 180, snailGenesis.PublicKey(), snailGenesis.Coinbase(), true, nil)
	snailblockchain.InsertChain(blocks1)

}

func fruit(fastNumber int, fruitDifficulty *big.Int) *types.SnailBlock {
	var fruit *types.SnailBlock

	fastblocks, _ := core.GenerateChain(params.TestChainConfig, fastchainpool.CurrentBlock(), enginepool, peerDbpool, 1, func(i int, b *core.BlockGen) {
		b.SetCoinbase(common.Address{0: byte(1), 19: byte(i)})
	})

	fastchainpool.InsertChain(fastblocks)

	fruit, err := makeSnailFruit(snailblockchain, fastchainpool, 1, fastNumber, 1, snailGenesis.PublicKey(), snailGenesis.Coinbase(), false, fruitDifficulty)
	if err != nil {
		fmt.Print(err)
	}

	return fruit
}

func makeSnailFruit(chain *SnailBlockChain, fastchainpool *core.BlockChain, makeBlockNum int, makeStartFastNum int, makeFruitSize int,
	pubkey []byte, coinbaseAddr common.Address, isBlock bool, diff *big.Int) (*types.SnailBlock, error) {

	var fruitsetCopy []*types.SnailBlock
	var pointerHashFresh = big.NewInt(7)
	var snailFruitsLastFastNumber *big.Int

	if chain == nil {
		return nil, fmt.Errorf("chain is nil")
	}

	// create head
	parent := chain.CurrentBlock()
	if parent.Fruits() != nil && len(parent.Fruits()) != 0 {
		snailFruitsLastFastNumber = parent.Fruits()[len(parent.Fruits())-1].FastNumber()
	} else {
		snailFruitsLastFastNumber = new(big.Int).SetUint64(0)
	}

	//parentNum := parent.Number()

	if isBlock {
		if makeFruitSize < params.MinimumFruits || snailFruitsLastFastNumber.Int64() >= int64(makeStartFastNum) {
			return nil, fmt.Errorf("fruitSet is nill or size less then 60")
		}
	}

	makeHead := func(chain *SnailBlockChain, pubkey []byte, coinbaseAddr common.Address, fastNumber *big.Int, isFruit bool) *types.SnailHeader {
		parent := chain.CurrentBlock()
		//num := parent.Number()
		var fruitDiff *big.Int
		if isFruit {
			fruitDiff = diff
		}
		tstamp := time.Now().Unix()
		header := &types.SnailHeader{
			ParentHash:      parent.Hash(),
			Publickey:       pubkey,
			Number:          new(big.Int).SetUint64(uint64(makeBlockNum)),
			Time:            big.NewInt(tstamp),
			Coinbase:        coinbaseAddr,
			FastNumber:      fastNumber,
			Difficulty:      diff,
			FruitDifficulty: fruitDiff,
			FastHash:        fastchainpool.GetBlockByNumber(fastNumber.Uint64()).Hash(),
		}

		pointerNum := new(big.Int).Sub(parent.Number(), pointerHashFresh)
		if pointerNum.Cmp(common.Big0) < 0 {
			pointerNum = new(big.Int).Set(common.Big0)
		}
		pointer := chain.GetBlockByNumber(pointerNum.Uint64())
		header.PointerHash = pointer.Hash()
		header.PointerNumber = pointer.Number()

		return header
	}

	copySignsByFastNum := func(fc *core.BlockChain, fNumber *big.Int) ([]*types.PbftSign, error) {

		if fc.CurrentBlock().Number().Cmp(fNumber) < 0 {
			return nil, fmt.Errorf("fastblocknumber highter then fast chain hight")
		}

		fastSigns := fc.GetBlockByNumber(fNumber.Uint64()).Signs()
		return fastSigns, nil

	}

	makeFruit := func(chain *SnailBlockChain, fChain *core.BlockChain, fastNumber *big.Int, pubkey []byte, coinbaseAddr common.Address) (*types.SnailBlock, error) {

		head := makeHead(chain, pubkey, coinbaseAddr, fastNumber, true)
		//pointer := chain.GetHeader(head.PointerHash, head.PointerNumber.Uint64())
		fastBlock := fChain.GetBlockByNumber(fastNumber.Uint64())
		head.FastHash = fastBlock.Hash()
		//head.FruitDifficulty = minerva.CalcFruitDifficulty(chain.chainConfig, head.Time.Uint64(), fastBlock.Header().Time.Uint64(), pointer)

		fSign, err := copySignsByFastNum(fChain, fastNumber)
		if err != nil {
			return nil, err
		}

		fruit := types.NewSnailBlock(
			head,
			nil,
			fSign,
			nil,
			nil,
		)
		return fruit, nil
	}

	// creat fruits
	if isBlock {
		for i := makeStartFastNum; i < makeStartFastNum+makeFruitSize; i++ {
			fruit, err := makeFruit(chain, fastchainpool, new(big.Int).SetInt64(int64(i)), pubkey, coinbaseAddr)
			if err != nil {
				return nil, err
			}
			fruitsetCopy = append(fruitsetCopy, fruit)
		}
		if len(fruitsetCopy) != makeFruitSize {
			return nil, fmt.Errorf("fruits make fail the length less then makeFruitSize")
		}

		fSign, err := copySignsByFastNum(fastchainpool, new(big.Int).SetUint64(uint64(makeStartFastNum)))
		if err != nil {
			return nil, err
		}

		block := types.NewSnailBlock(
			makeHead(chain, pubkey, coinbaseAddr, new(big.Int).SetInt64(int64(makeStartFastNum)), false),
			fruitsetCopy,
			fSign,
			nil,
			chain.Config(),
		)
		return block, nil

	}
	fruit, err := makeFruit(chain, fastchainpool, new(big.Int).SetInt64(int64(makeStartFastNum)), pubkey, coinbaseAddr)
	if err != nil {
		return nil, err
	}
	return fruit, nil

}

func setupSnailPool() *SnailPool {

	pool := NewSnailPool(testSnailPoolConfig, fastchainpool, snailblockchain, enginepool)
	return pool
}

// validateSnailPoolInternals checks various consistency invariants within the pool.
func validateSnailPoolInternals(pool *SnailPool) error {
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	// Ensure the total fruits set is consistent with pending + unVerified
	pending, unVerified := pool.Stats()
	if total := len(pool.allFruits); total != pending+unVerified {
		return fmt.Errorf("total fruits count %d != %d pending + %d unVerified", total, pending, unVerified)
	}
	return nil
}

// validateEvents checks that the correct number of fruit addition events
// were fired on the pool's event feed.
func validateFruitEvents(events chan types.NewFruitsEvent, count int) error {
	var received []*types.SnailBlock

	for len(received) < count {
		select {
		case ev := <-events:
			received = append(received, ev.Fruits...)
		case <-time.After(time.Second):
			return fmt.Errorf("event #%v not fired", received)
		}
	}
	if len(received) > count {
		return fmt.Errorf("more than %d events fired: %v", count, received[count:])
	}
	select {
	case ev := <-events:
		return fmt.Errorf("more than %d events fired: %v", count, ev.Fruits)

	case <-time.After(50 * time.Millisecond):
		// This branch should be "default", but it's a data race between goroutines,
		// reading the event channel and pushing into it, so better wait a bit ensuring
		// really nothing gets injected.
	}
	return nil
}

func TestInvalidFruits(t *testing.T) {
	poolinit()
	t.Parallel()
	var header *types.SnailHeader
	header = &types.SnailHeader{
		ParentHash:      header.Hash(),
		Coinbase:        common.BytesToAddress([]byte{0}),
		PointerHash:     header.Hash(),
		PointerNumber:   big.NewInt(0),
		FruitsHash:      header.Hash(),
		FastHash:        header.Hash(),
		FastNumber:      big.NewInt(182),
		SignHash:        header.Hash(),
		Difficulty:      big.NewInt(0),
		FruitDifficulty: big.NewInt(1789570),
		Number:          big.NewInt(0),
		Publickey:       []byte{0},
		Time:            nil,
		Extra:           []byte{0},
		MixDigest:       header.Hash(),
		Nonce:           [8]byte{},
	}
	var ft *types.SnailBlock
	ft = types.NewSnailBlock(header, nil, nil, nil, nil)
	pool := setupSnailPool()
	defer pool.Stop()
	if err, _ := pool.addFruit(ft); err != ErrNotExist {
		t.Error("expected", ErrNotExist)
	}
}

func TestFruitQueue(t *testing.T) {
	poolinit()
	t.Parallel()

	pool := setupSnailPool()
	defer pool.Stop()

	ft := fruit(181, big.NewInt(1789570))
	pool.addFruit(ft)
	//if fastNumber is bigger than pool.fastchainpool.CurrentBlock().Number() will only add to allFruits
	if len(pool.allFruits) != 1 {
		t.Error("expected allFruits to be 1 is", len(pool.allFruits))
	}
	if len(pool.fruitPending) != 1 {
		t.Error("expected fruit pending to be 1. is", len(pool.fruitPending))
	}

	pool = setupSnailPool()
	defer pool.Stop()

	ft1 := fruit(181, big.NewInt(1789570))
	ft2 := fruit(182, big.NewInt(1789570))
	ft3 := fruit(183, big.NewInt(1789570))

	pool.addFruit(ft1)
	pool.addFruit(ft2)
	pool.addFruit(ft3)

	if len(pool.fruitPending) != 3 {
		t.Error("expected fruitPending to be 3, got", len(pool.fruitPending))
	}
	if len(pool.allFruits) != 3 {
		t.Error("expected len(allFruits) == 3, got", len(pool.allFruits))
	}
}

func TestFruitDropping(t *testing.T) {
	poolinit()
	t.Parallel()

	pool := setupSnailPool()
	defer pool.Stop()
	events := make(chan types.NewFruitsEvent, 3)
	sub := pool.fruitFeed.Subscribe(events)
	defer sub.Unsubscribe()
	// Add some pending fruits
	var (
		ft10 = fruit(181, big.NewInt(1789570))
		ft11 = fruit(182, big.NewInt(1789570))
		ft12 = fruit(183, big.NewInt(1789570))
	)
	pool.addFruits([]*types.SnailBlock{ft10})
	pool.addFruits([]*types.SnailBlock{ft11})
	pool.addFruits([]*types.SnailBlock{ft12})

	pool.RemovePendingFruitByFastHash(ft10.FastHash())
	// Check that pre and post validations leave the pool as is
	if len(pool.fruitPending) != 2 {
		t.Errorf("pending fruit mismatch: have %d, want %d", len(pool.fruitPending), 2)
	}
	if len(pool.allFruits) != 2 {
		t.Errorf(
			"queued fruit mismatch: have %d, want %d", len(pool.allFruits), 2)
	}
	if err := validateFruitEvents(events, 3); err != nil {
		t.Fatalf(" replacement event firing failed: %v", err)
	}
}

// Tests that the pool rejects replacement fruits that a new is difficulty
// than old one.
func TestFruitReplacement(t *testing.T) {
	poolinit()
	t.Parallel()

	// Create a test account and fund it
	pool := setupSnailPool()
	defer pool.Stop()
	events := make(chan types.NewFruitsEvent, 1)
	sub := pool.fruitFeed.Subscribe(events)
	defer sub.Unsubscribe()
	// Add some pending fruits
	var (
		ft0 = fruit(181, big.NewInt(1000))
		ft2 = fruit(181, big.NewInt(1789570))
	)

	pool.addFruits([]*types.SnailBlock{ft0})
	pool.addFruits([]*types.SnailBlock{ft2})

	if pool.fruitPending[ft0.FastHash()].FruitDifficulty().Cmp(big.NewInt(1789570)) != 0 {
		t.Errorf("pending fruit's difficulty mismatch: is %d, want %d", pool.fruitPending[ft0.FastHash()].FruitDifficulty(), big.NewInt(1789570))
	}
	if pool.allFruits[ft0.FastHash()].FruitDifficulty().Cmp(big.NewInt(1789570)) != 0 {
		t.Errorf("allFruits's difficulty mismatch: is %d, want %d", pool.allFruits[ft0.FastHash()].FruitDifficulty(), big.NewInt(1789570))
	}
	if err := validateFruitEvents(events, 1); err != nil {
		t.Fatalf("replacement event firing failed: %v", err)
	}

}

// Tests that local fruits are journaled to disk, but remote fruits
// get discarded between restarts.
func TestFruitJournaling(t *testing.T) { poolinit(); testFruitJournaling(t) }

func testFruitJournaling(t *testing.T) {
	t.Parallel()

	// Create a temporary file for the journal
	file, err := ioutil.TempFile("", "")
	if err != nil {
		t.Fatalf("failed to create temporary journal: %v", err)
	}
	journal := file.Name()
	defer os.Remove(journal)

	// Clean up the temporary file, we only need the path for now
	file.Close()
	os.Remove(journal)

	// Create the original pool to inject fruit into the journal
	config := testSnailPoolConfig
	config.Journal = journal
	config.Rejournal = time.Second

	pool := setupSnailPool()
	defer pool.Stop()

	// Add three fruits and ensure they are queued up
	if err, _ := pool.addFruit(fruit(181, big.NewInt(1789570))); err != nil {
		t.Fatalf("failed to add local fruit: %v", err)
	}
	if err, _ := pool.addFruit(fruit(182, big.NewInt(1789570))); err != nil {
		t.Fatalf("failed to add local fruit: %v", err)
	}
	if err, _ := pool.addFruit(fruit(183, big.NewInt(1789570))); err != nil {
		t.Fatalf("failed to add local fruit: %v", err)
	}
	if err, _ := pool.addFruit(fruit(184, big.NewInt(1789570))); err != nil {
		t.Fatalf("failed to add remote fruit: %v", err)
	}
	pending, unverified := pool.Stats()
	if pending != 4 {
		t.Fatalf("pending fruits mismatched: have %d, want %d", pending, 4)
	}
	if unverified != 0 {
		t.Fatalf("unverified fruits mismatched: have %d, want %d", unverified, 0)
	}
	if err := validateSnailPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
	// Terminate the old pool,create a new pool and ensure relevant fruit survive
	pool.Stop()

	pool = NewSnailPool(testSnailPoolConfig, fastchainpool, snailblockchain, enginepool)

	pending, unverified = pool.Stats()
	if unverified != 0 {
		t.Fatalf("unverified fruits mismatched: have %d, want %d", unverified, 0)
	}

	if err := validateSnailPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
	time.Sleep(2 * config.Rejournal)
	pool.Stop()

	pool = NewSnailPool(testSnailPoolConfig, fastchainpool, snailblockchain, enginepool)
	pending, unverified = pool.Stats()
	if pending != 0 {
		t.Fatalf("pending fruits mismatched: have %d, want %d", pending, 0)
	}
	if err := validateSnailPoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
	pool.Stop()
}

// Benchmarks the speed of iterative fruit insertion.
func BenchmarkSnailPoolInsert(b *testing.B) {
	poolinit()
	// Generate a batch of fruits to enqueue into the pool
	pool := setupSnailPool()
	defer pool.Stop()

	fruits := make(types.Fruits, b.N)
	for i := 0; i < b.N; i++ {
		fruits[i] = fruit(180+i, big.NewInt(0))
	}
	// Benchmark importing the fruits into the pending and allFruits
	b.ResetTimer()
	for _, tx := range fruits {
		pool.addFruit(tx)
	}
}

// Benchmarks the speed of batched fruit insertion.
func BenchmarkSnailPoolBatchInsert100(b *testing.B) {
	poolinit()
	benchmarkSnailPoolBatchInsert(b, 100)
}
func BenchmarkSnailPoolBatchInsert1000(b *testing.B) {
	poolinit()
	benchmarkSnailPoolBatchInsert(b, 1000)
}
func BenchmarkSnailPoolBatchInsert10000(b *testing.B) {
	poolinit()
	benchmarkSnailPoolBatchInsert(b, 10000)
}

func benchmarkSnailPoolBatchInsert(b *testing.B, size int) {
	// Generate a batch of fruits to enqueue into the pool
	pool := setupSnailPool()
	defer pool.Stop()

	batches := make([]types.Fruits, b.N)
	for i := 0; i < b.N; i++ {
		batches[i] = make(types.Fruits, size)
		for j := 0; j < size; j++ {
			batches[i][j] = fruit(size*i+j, big.NewInt(0))
		}
	}
	// Benchmark importing the fruits into the queue
	b.ResetTimer()
	for _, batch := range batches {
		pool.AddRemoteFruits(batch, false)
	}
}
