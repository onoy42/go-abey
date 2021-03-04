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

package snailchain

import (
	"fmt"
	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/log"
	"github.com/abeychain/go-abey/consensus"
	"github.com/abeychain/go-abey/consensus/minerva"
	"github.com/abeychain/go-abey/core"
	"github.com/abeychain/go-abey/core/snailchain/rawdb"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/core/vm"
	"github.com/abeychain/go-abey/abeydb"
	"github.com/abeychain/go-abey/params"
	"math/big"
	"math/rand"
	"os"
	"sync"
	"testing"
)

func init() {
	log.Root().SetHandler(log.LvlFilterHandler(log.LvlTrace, log.StreamHandler(os.Stderr, log.TerminalFormat(false))))
}

// So we can deterministically seed different blockchains
var (
	canonicalSeed = 1
	forkSeed      = 2
)

// newCanonical creates a chain database, and injects a deterministic canonical
// chain. Depending on the full flag, if creates either a full block chain or a
// header only chain.
func newCanonical(engine consensus.Engine, n int, full bool) (abeydb.Database, *SnailBlockChain, *core.BlockChain, error) {
	var (
		db            = abeydb.NewMemDatabase()
		commonGenesis = core.DefaultGenesisBlock()
		snailGenesis  = commonGenesis.MustSnailCommit(db)
		fastGenesis   = commonGenesis.MustFastCommit(db)
	)

	fastChain, _ := core.NewBlockChain(db, nil, params.AllMinervaProtocolChanges, engine, vm.Config{})

	// Initialize a fresh chain with only a genesis block
	blockchain, _ := NewSnailBlockChain(db, params.TestChainConfig, engine, fastChain)
	//blockchain.SetValidator(NewBlockValidator(chainConfig, fastChain, blockchain, engine))
	// Create and inject the requested chain
	if n == 0 {
		return db, blockchain, fastChain, nil
	}
	fastBlocks, _ := core.GenerateChain(params.TestChainConfig, fastGenesis, engine, db, (n+20)*params.MinimumFruits, func(i int, b *core.BlockGen) {
		b.SetCoinbase(common.Address{0: byte(1), 19: byte(i)})
	})
	fastChain.InsertChain(fastBlocks)
	if full {
		// Full block-chain requested
		blocks := makeBlockChain(fastChain, []*types.SnailBlock{snailGenesis}, n, engine, db, canonicalSeed)
		_, err := blockchain.InsertChain(blocks)
		return db, blockchain, fastChain, err
	}
	// Header-only chain requested
	headers := makeHeaderChain(fastChain, []*types.SnailHeader{snailGenesis.Header()}, n, engine, db, canonicalSeed)
	_, err := blockchain.InsertHeaderChain(headers, nil, 1)
	return db, blockchain, fastChain, err
}

func TestMakeChain(t *testing.T) {
	genesis := core.DefaultGenesisBlock()
	chain, _ := MakeChain(180, 3, genesis, minerva.NewFaker())
	log.Info("TestMakeChain", "number", chain.CurrentBlock().Number(), "fast number", chain.CurrentFastBlock().Number())
	blocks := chain.GetBlocksFromNumber(1)

	for _, block := range blocks {
		fmt.Printf("%d => %x\n", block.Number(), block.Hash())
	}

	header := chain.GetHeaderByNumber(1)

	if header == nil {
		fmt.Printf("1111111111111111\n")
	} else {
		fmt.Printf("%x\n", header.Hash())
	}
}

// Test fork of length N starting from block i
func testFork(t *testing.T, blockchain *SnailBlockChain, i, n int, full bool, comparator func(td1, td2 *big.Int), engine consensus.Engine) {
	// Copy old chain up to #i into a new db
	db, blockchain2, fastChain, err := newCanonical(engine, i, full)
	if err != nil {
		t.Fatal("could not make new canonical in testFork", err)
	}
	defer blockchain2.Stop()

	// Assert the chains have the same header/block at #i
	var hash1, hash2 common.Hash
	if full {
		hash1 = blockchain.GetBlockByNumber(uint64(i)).Hash()
		hash2 = blockchain2.GetBlockByNumber(uint64(i)).Hash()
	} else {
		hash1 = blockchain.GetHeaderByNumber(uint64(i)).Hash()
		hash2 = blockchain2.GetHeaderByNumber(uint64(i)).Hash()
	}
	if hash1 != hash2 {
		t.Errorf("chain content mismatch at %d: have hash %v, want hash %v", i, hash2, hash1)
	}
	// Extend the newly created chain
	var (
		blockChainB  []*types.SnailBlock
		headerChainB []*types.SnailHeader
	)
	commonGenesis := core.DefaultGenesisBlock()
	fastBlocks, _ := core.GenerateChain(params.TestChainConfig, commonGenesis.MustFastCommit(db), engine, db, (n+i)*params.MinimumFruits, func(i int, b *core.BlockGen) {
		b.SetCoinbase(common.Address{0: byte(1), 19: byte(i)})
	})
	fastChain.InsertChain(fastBlocks)
	if full {
		blockChainB = makeBlockChain(fastChain, blockchain2.GetBlocksFromNumber(0), n, engine, db, forkSeed)
		if _, err := blockchain2.InsertChain(blockChainB); err != nil {
			t.Fatalf("failed to insert forking chain: %v", err)
		}
	} else {

		headerChainB = makeHeaderChain(fastChain, blockchain2.GetHeadsFromNumber(0), n, engine, db, forkSeed)
		if _, err := blockchain2.InsertHeaderChain(headerChainB, nil, 1); err != nil {
			t.Fatalf("failed to insert forking chain: %v", err)
		}
	}
	// Sanity check that the forked chain can be imported into the original
	var tdPre, tdPost *big.Int

	if full {
		tdPre = blockchain.GetTdByHash(blockchain.CurrentBlock().Hash())
		if err := testBlockChainImport(blockChainB, blockchain); err != nil {
			t.Fatalf("failed to import forked block chain: %v", err)
		}
		tdPost = blockchain.GetTdByHash(blockChainB[len(blockChainB)-1].Hash())
	} else {
		tdPre = blockchain.GetTdByHash(blockchain.CurrentHeader().Hash())
		if err := testHeaderChainImport(headerChainB, blockchain); err != nil {
			t.Fatalf("failed to import forked header chain: %v", err)
		}
		tdPost = blockchain.GetTdByHash(headerChainB[len(headerChainB)-1].Hash())
	}
	// Compare the total difficulties of the chains
	comparator(tdPre, tdPost)
}

// testBlockChainImport tries to process a chain of blocks, writing them into
// the database if successful.
func testBlockChainImport(chain types.SnailBlocks, blockchain *SnailBlockChain) error {
	for _, block := range chain {
		// Try and process the block
		err := blockchain.engine.VerifySnailHeader(blockchain, nil, block.Header(), true, false)
		/*if err == nil {
			err = blockchain.validator.ValidateBody(block)
		}*/
		if err != nil {
			if err == ErrKnownBlock {
				continue
			}
			return err
		}
		blockchain.chainmu.Lock()
		rawdb.WriteTd(blockchain.db, block.Hash(), block.NumberU64(), new(big.Int).Add(block.Difficulty(), blockchain.GetTdByHash(block.ParentHash())))
		rawdb.WriteBlock(blockchain.db, block)
		blockchain.chainmu.Unlock()
	}
	return nil
}

// testHeaderChainImport tries to process a chain of header, writing them into
// the database if successful.
func testHeaderChainImport(chain []*types.SnailHeader, blockchain *SnailBlockChain) error {
	for _, header := range chain {
		// Try and validate the header
		if err := blockchain.engine.VerifySnailHeader(blockchain, nil, header, false, false); err != nil {
			return err
		}
		// Manually insert the header into the database, but don't reorganise (allows subsequent testing)
		blockchain.chainmu.Lock()
		rawdb.WriteTd(blockchain.db, header.Hash(), header.Number.Uint64(), new(big.Int).Add(header.Difficulty, blockchain.GetTdByHash(header.ParentHash)))
		rawdb.WriteHeader(blockchain.db, header)
		blockchain.chainmu.Unlock()
	}
	return nil
}

func insertChain(done chan bool, blockchain *SnailBlockChain, chain types.SnailBlocks, t *testing.T) {
	_, err := blockchain.InsertChain(chain)
	if err != nil {
		fmt.Println(err)
		t.FailNow()
	}
	done <- true
}

func TestLastBlock(t *testing.T) {
	engine := minerva.NewFaker()
	_, blockchain, _, err := newCanonical(engine, 3, false)
	if err != nil {
		t.Fatalf("failed to create pristine chain: %v", err)
	}
	defer blockchain.Stop()

	chain, _ := MakeSnailChain(5, core.DefaultGenesisBlock(), engine)
	blocks := chain.GetBlocksFromNumber(1)
	defer chain.Stop()
	for _, block := range blocks {
		fmt.Printf("%d => %x\n", block.Number(), block.Hash())
	}

	if _, err := blockchain.InsertChain(blocks); err != nil {
		t.Fatalf("Failed to insert block: %v", err)
	}

	fmt.Printf("%d ==> %x\n", blockchain.CurrentBlock().Number(), rawdb.ReadHeadBlockHash(blockchain.db))

	if blocks[len(blocks)-1].Hash() != rawdb.ReadHeadBlockHash(blockchain.db) {
		t.Fatalf("Write/Get HeadBlockHash failed")
	}
}

// Tests that given a starting canonical chain of a given size, it can be extended
// with various length chains.
func TestExtendCanonicalHeaders(t *testing.T) { testExtendCanonical(t, false) }
func TestExtendCanonicalBlocks(t *testing.T)  { testExtendCanonical(t, true) }

func testExtendCanonical(t *testing.T, full bool) {
	length := 5
	engine := minerva.NewFaker()
	// Make first chain starting from genesis
	_, processor, _, err := newCanonical(engine, length, full)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	defer processor.Stop()
	//hash1 := processor.GetBlockByNumber(uint64(1)).Hash()
	//log.Info("hash", "hash1", hash1)
	// Define the difficulty comparator
	better := func(td1, td2 *big.Int) {
		if td2.Cmp(td1) <= 0 {
			t.Errorf("total difficulty mismatch: have %v, expected more than %v", td2, td1)
		}
	}
	// Start fork from current height
	testFork(t, processor, length, 1, full, better, engine)
	testFork(t, processor, length, 2, full, better, engine)
	testFork(t, processor, length, 5, full, better, engine)
	testFork(t, processor, length, 10, full, better, engine)
}

// Tests that given a starting canonical chain of a given size, creating shorter
// forks do not take canonical ownership.
func TestShorterForkHeaders(t *testing.T) { testShorterFork(t, false) }
func TestShorterForkBlocks(t *testing.T)  { testShorterFork(t, true) }

func testShorterFork(t *testing.T, full bool) {
	length := 10
	engine := minerva.NewFaker()
	// Make first chain starting from genesis
	_, processor, _, err := newCanonical(engine, length, full)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	defer processor.Stop()

	// Define the difficulty comparator
	worse := func(td1, td2 *big.Int) {
		if td2.Cmp(td1) >= 0 {
			t.Errorf("total difficulty mismatch: have %v, expected less than %v", td2, td1)
		}
	}
	// Sum of numbers must be less than `length` for this to be a shorter fork
	testFork(t, processor, 0, 3, full, worse, engine)
	testFork(t, processor, 0, 7, full, worse, engine)
	testFork(t, processor, 1, 1, full, worse, engine)
	testFork(t, processor, 1, 7, full, worse, engine)
	testFork(t, processor, 5, 3, full, worse, engine)
	testFork(t, processor, 5, 4, full, worse, engine)
}

// Tests that given a starting canonical chain of a given size, creating longer
// forks do take canonical ownership.
func TestLongerForkHeaders(t *testing.T) { testLongerFork(t, false) }
func TestLongerForkBlocks(t *testing.T)  { testLongerFork(t, true) }

func testLongerFork(t *testing.T, full bool) {
	length := 10
	engine := minerva.NewFaker()
	// Make first chain starting from genesis
	_, processor, _, err := newCanonical(engine, length, full)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	defer processor.Stop()

	// Define the difficulty comparator
	better := func(td1, td2 *big.Int) {
		if td2.Cmp(td1) <= 0 {
			t.Errorf("total difficulty mismatch: have %v, expected more than %v", td2, td1)
		}
	}
	// Sum of numbers must be greater than `length` for this to be a longer fork
	testFork(t, processor, 0, 11, full, better, engine)
	testFork(t, processor, 0, 15, full, better, engine)
	testFork(t, processor, 1, 10, full, better, engine)
	testFork(t, processor, 1, 12, full, better, engine)
	testFork(t, processor, 5, 6, full, better, engine)
	testFork(t, processor, 5, 8, full, better, engine)
}

// Tests that given a starting canonical chain of a given size, creating equal
// forks do take canonical ownership.
func TestEqualForkHeaders(t *testing.T) { testEqualFork(t, false) }
func TestEqualForkBlocks(t *testing.T)  { testEqualFork(t, true) }

func testEqualFork(t *testing.T, full bool) {
	length := 10
	engine := minerva.NewFaker()
	// Make first chain starting from genesis
	_, processor, _, err := newCanonical(engine, length, full)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	defer processor.Stop()

	// Define the difficulty comparator
	equal := func(td1, td2 *big.Int) {
		if td2.Cmp(td1) != 0 {
			t.Errorf("total difficulty mismatch: have %v, want %v", td2, td1)
		}
	}
	// Sum of numbers must be equal to `length` for this to be an equal fork
	testFork(t, processor, 0, 10, full, equal, engine)
	testFork(t, processor, 1, 9, full, equal, engine)
	testFork(t, processor, 2, 8, full, equal, engine)
	testFork(t, processor, 5, 5, full, equal, engine)
	testFork(t, processor, 6, 4, full, equal, engine)
	testFork(t, processor, 9, 1, full, equal, engine)
}

// Tests that chains missing links do not get accepted by the processor.
func TestBrokenHeaderChain(t *testing.T) { testBrokenChain(t, false) }
func TestBrokenBlockChain(t *testing.T)  { testBrokenChain(t, true) }

func testBrokenChain(t *testing.T, full bool) {
	engine := minerva.NewFaker()
	// Make chain starting from genesis
	db, blockchain, fastChain, err := newCanonical(engine, 10, full)
	if err != nil {
		t.Fatalf("failed to make new canonical chain: %v", err)
	}
	defer blockchain.Stop()

	// Create a forked chain, and try to insert with a missing link
	if full {
		chain := makeBlockChain(fastChain, blockchain.GetBlocksFromNumber(0), 5, engine, db, forkSeed)[1:]
		if err := testBlockChainImport(chain, blockchain); err == nil {
			t.Errorf("broken block chain not reported")
		}
	} else {
		chain := makeHeaderChain(fastChain, blockchain.GetHeadsFromNumber(0), 5, engine, db, forkSeed)[1:]
		if err := testHeaderChainImport(chain, blockchain); err == nil {
			t.Errorf("broken header chain not reported")
		}
	}
}

// Tests that reorganising a long difficult chain after a short easy one
// overwrites the canonical numbers and links in the database.
//func TestReorgLongHeaders(t *testing.T) { testReorgLong(t, false) }
func TestReorgLongBlocks(t *testing.T) { testReorgLong(t, true) }

func testReorgLong(t *testing.T, full bool) {
	testReorg(t, []int64{0, 0, -9}, []int64{0, 0, 0, -9}, 5131292940, full)
}

// Tests that reorganising a short difficult chain after a long easy one
// overwrites the canonical numbers and links in the database.
//func TestReorgShortHeaders(t *testing.T) { testReorgShort(t, false) }
func TestReorgShortBlocks(t *testing.T) { testReorgShort(t, true) }

func testReorgShort(t *testing.T, full bool) {
	// Create a long easy chain vs. a short heavy one. Due to difficulty adjustment
	// we need a fairly long chain of blocks with different difficulties for a short
	// one to become heavyer than a long one. The 96 is an empirical value.
	easy := make([]int64, 96)
	for i := 0; i < len(easy); i++ {
		easy[i] = 60
	}
	diff := make([]int64, len(easy)-1)
	for i := 0; i < len(diff); i++ {
		diff[i] = -9
	}
	testReorg(t, easy, diff, 25578783544, full)
}

func testReorg(t *testing.T, first, second []int64, td int64, full bool) {
	// Create a pristine chain and database
	engine := minerva.NewFaker()
	db, blockchain, fastChain, err := newCanonical(engine, 0, full)
	if err != nil {
		t.Fatalf("failed to create pristine chain: %v", err)
	}
	defer blockchain.Stop()

	commonGenesis := core.DefaultGenesisBlock()
	fastBlocks, _ := core.GenerateChain(params.TestChainConfig, commonGenesis.MustFastCommit(db), engine, db, len(second)*params.MinimumFruits, func(i int, b *core.BlockGen) {
		b.SetCoinbase(common.Address{0: byte(1), 19: byte(i)})
	})
	fastChain.InsertChain(fastBlocks)
	// Insert an easy and a difficult chain afterwards
	easyBlocks := GenerateChain(params.TestChainConfig, fastChain, blockchain.GetBlocksFromNumber(0), len(first), 7, func(i int, b *BlockGen) {
		b.OffsetTime(first[i])
	})
	diffBlocks := GenerateChain(params.TestChainConfig, fastChain, blockchain.GetBlocksFromNumber(0), len(second), 7, func(i int, b *BlockGen) {
		b.OffsetTime(second[i])
	})
	if full {
		if _, err := blockchain.InsertChain(easyBlocks); err != nil {
			t.Fatalf("failed to insert easy chain: %v", err)
		}
		if _, err := blockchain.InsertChain(diffBlocks); err != nil {
			t.Fatalf("failed to insert difficult chain: %v", err)
		}
	} else {
		easyHeaders := make([]*types.SnailHeader, len(easyBlocks))
		for i, block := range easyBlocks {
			easyHeaders[i] = block.Header()
		}
		diffHeaders := make([]*types.SnailHeader, len(diffBlocks))
		for i, block := range diffBlocks {
			diffHeaders[i] = block.Header()
		}
		if _, err := blockchain.InsertHeaderChain(easyHeaders, nil, 1); err != nil {
			t.Fatalf("failed to insert easy chain: %v", err)
		}
		if _, err := blockchain.InsertHeaderChain(diffHeaders, nil, 1); err != nil {
			t.Fatalf("failed to insert difficult chain: %v", err)
		}
	}
	// Check that the chain is valid number and link wise
	if full {
		prev := blockchain.CurrentBlock()
		for block := blockchain.GetBlockByNumber(blockchain.CurrentBlock().NumberU64() - 1); block.NumberU64() != 0; prev, block = block, blockchain.GetBlockByNumber(block.NumberU64()-1) {
			if prev.ParentHash() != block.Hash() {
				t.Errorf("parent block hash mismatch: have %x, want %x", prev.ParentHash(), block.Hash())
			}
		}
	} else {
		prev := blockchain.CurrentHeader()
		for header := blockchain.GetHeaderByNumber(blockchain.CurrentHeader().Number.Uint64() - 1); header.Number.Uint64() != 0; prev, header = header, blockchain.GetHeaderByNumber(header.Number.Uint64()-1) {
			if prev.ParentHash != header.Hash() {
				t.Errorf("parent header hash mismatch: have %x, want %x", prev.ParentHash, header.Hash())
			}
		}
	}
	// Make sure the chain total difficulty is the correct one
	want := new(big.Int).Add(blockchain.genesisBlock.Difficulty(), big.NewInt(td))
	if full {
		if have := blockchain.GetTdByHash(blockchain.CurrentBlock().Hash()); have.Cmp(want) != 0 {
			log.Info("CurrentBlock", "blockchain.genesisBlock.Difficulty()", blockchain.genesisBlock.Difficulty(), "td", td, "want", want, "have", have)
			t.Errorf("total difficulty mismatch: have %v, want %v", have, want)
		}
	} else {
		if have := blockchain.GetTdByHash(blockchain.CurrentHeader().Hash()); have.Cmp(want) != 0 {
			log.Info("CurrentHeader", "blockchain.genesisBlock.Difficulty()", blockchain.genesisBlock.Difficulty(), "td", td, "want", want, "have", have)
			t.Errorf("total difficulty mismatch: have %v, want %v", have, want)
		}
	}
}

// Tests that the insertion functions detect banned hashes.
func TestBadHeaderHashes(t *testing.T) { testBadHashes(t, false) }
func TestBadBlockHashes(t *testing.T)  { testBadHashes(t, true) }

func testBadHashes(t *testing.T, full bool) {
	// Create a pristine chain and database
	db, blockchain, fastChain, err := newCanonical(minerva.NewFaker(), 0, full)
	if err != nil {
		t.Fatalf("failed to create pristine chain: %v", err)
	}
	defer blockchain.Stop()

	// Create a chain, ban a hash and try to import
	if full {
		blocks := makeBlockChain(fastChain, blockchain.GetBlocksFromNumber(blockchain.CurrentBlock().NumberU64()), 3, minerva.NewFaker(), db, 10)

		BadHashes[blocks[2].Header().Hash()] = true
		defer func() { delete(BadHashes, blocks[2].Header().Hash()) }()

		_, err = blockchain.InsertChain(blocks)
	} else {
		headers := makeHeaderChain(fastChain, blockchain.GetHeadsFromNumber(0), 3, minerva.NewFaker(), db, 10)

		BadHashes[headers[2].Hash()] = true
		defer func() { delete(BadHashes, headers[2].Hash()) }()

		_, err = blockchain.InsertHeaderChain(headers, nil, 1)
	}
	if err != ErrBlacklistedHash {
		t.Errorf("error mismatch: have: %v, want: %v", err, ErrBlacklistedHash)
	}
}

// Tests that bad hashes are detected on boot, and the chain rolled back to a
// good state prior to the bad hash.
//func TestReorgBadHeaderHashes(t *testing.T) { testReorgBadHashes(t, false) }
func TestReorgBadBlockHashes(t *testing.T) { testReorgBadHashes(t, true) }

func testReorgBadHashes(t *testing.T, full bool) {
	// Create a pristine chain and database
	db, blockchain, fastChain, err := newCanonical(minerva.NewFaker(), 0, full)
	if err != nil {
		t.Fatalf("failed to create pristine chain: %v", err)
	}
	// Create a chain, import and ban afterwards
	headers := makeHeaderChain(fastChain, blockchain.GetHeadsFromNumber(0), 4, minerva.NewFaker(), db, 10)
	blocks := makeBlockChain(fastChain, blockchain.GetBlocksFromNumber(blockchain.CurrentBlock().NumberU64()), 4, minerva.NewFaker(), db, 10)

	if full {
		if _, err = blockchain.InsertChain(blocks); err != nil {
			t.Errorf("failed to import blocks: %v", err)
		}
		if blockchain.CurrentBlock().Hash() != blocks[3].Hash() {
			t.Errorf("last block hash mismatch: have: %x, want %x", blockchain.CurrentBlock().Hash(), blocks[3].Header().Hash())
		}
		BadHashes[blocks[3].Header().Hash()] = true
		defer func() { delete(BadHashes, blocks[3].Header().Hash()) }()
	} else {
		if _, err = blockchain.InsertHeaderChain(headers, nil, 1); err != nil {
			t.Errorf("failed to import headers: %v", err)
		}
		if blockchain.CurrentHeader().Hash() != headers[3].Hash() {
			t.Errorf("last header hash mismatch: have: %x, want %x", blockchain.CurrentHeader().Hash(), headers[3].Hash())
		}
		BadHashes[headers[3].Hash()] = true
		defer func() { delete(BadHashes, headers[3].Hash()) }()
	}
	blockchain.Stop()

	// Create a new BlockChain and check that it rolled back the state.
	ncm, err := NewSnailBlockChain(blockchain.db, blockchain.chainConfig, minerva.NewFaker(), fastChain)
	if err != nil {
		t.Fatalf("failed to create new chain manager: %v", err)
	}
	if full {
		if ncm.CurrentBlock().Hash() != blocks[2].Header().Hash() {
			t.Errorf("last block hash mismatch: have: %x, want %x", ncm.CurrentBlock().Hash(), blocks[2].Header().Hash())
		}
	} else {
		if ncm.CurrentHeader().Hash() != headers[2].Hash() {
			t.Errorf("last header hash mismatch: have: %x, want %x", ncm.CurrentHeader().Hash(), headers[2].Hash())
		}
	}
	ncm.Stop()
}

// Tests chain insertions in the face of one entity containing an invalid nonce.
func TestHeadersInsertNonceError(t *testing.T) { testInsertNonceError(t, false) }
func TestBlocksInsertNonceError(t *testing.T)  { testInsertNonceError(t, true) }

func testInsertNonceError(t *testing.T, full bool) {
	for i := 1; i < 25 && !t.Failed(); i++ {
		// Create a pristine chain and database
		db, blockchain, fastChain, err := newCanonical(minerva.NewFaker(), 0, full)
		if err != nil {
			t.Fatalf("failed to create pristine chain: %v", err)
		}
		defer blockchain.Stop()

		// Create and insert a chain with a failing nonce
		var (
			failAt  int
			failRes int
			failNum uint64
		)
		if full {
			blocks := makeBlockChain(fastChain, blockchain.GetBlocksFromNumber(blockchain.CurrentBlock().NumberU64()), i, minerva.NewFaker(), db, 0)

			failAt = rand.Int() % len(blocks)
			failNum = blocks[failAt].NumberU64()

			blockchain.engine = minerva.NewFakeFailer(failNum)
			failRes, err = blockchain.InsertChain(blocks)
		} else {
			headers := makeHeaderChain(fastChain, blockchain.GetHeadsFromNumber(0), i, minerva.NewFaker(), db, 0)

			failAt = rand.Int() % len(headers)
			failNum = headers[failAt].Number.Uint64()

			blockchain.engine = minerva.NewFakeFailer(failNum)
			blockchain.hc.engine = blockchain.engine
			failRes, err = blockchain.InsertHeaderChain(headers, nil, 1)
		}
		// Check that the returned error indicates the failure.
		if failRes != failAt {
			t.Errorf("test %d: failure index mismatch: have %d, want %d", i, failRes, failAt)
		}
		// Check that all no blocks after the failing block have been inserted.
		for j := 0; j < i-failAt; j++ {
			if full {
				if block := blockchain.GetBlockByNumber(failNum + uint64(j)); block != nil {
					t.Errorf("test %d: invalid block in chain: %v", i, block)
				}
			} else {
				if header := blockchain.GetHeaderByNumber(failNum + uint64(j)); header != nil {
					t.Errorf("test %d: invalid header in chain: %v", i, header)
				}
			}
		}
	}
}

// Tests that fast importing a block chain produces the same chain data as the
// classical full block processing.
/*func TestFastVsFullChains(t *testing.T) {
	// Configure and generate a sample block chain
	var (
		gendb   = abeydb.NewMemDatabase()
		key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = crypto.PubkeyToAddress(key.PublicKey)
		//funds   = big.NewInt(1000000000)
		gspec   = &Genesis{
			Config: params.TestChainConfig,
			//Alloc:  GenesisAlloc{address: {Balance: funds}},
		}
		genesis = gspec.MustCommit(gendb)
		signer  = types.NewTIP1Signer(gspec.Config.ChainID)
	)
	blocks := GenerateChain(gspec.Config, genesis, minerva.NewFaker(), gendb, 1024, func(i int, block *BlockGen) {
		block.SetCoinbase(common.Address{0x00})

		// If the block number is multiple of 3, send a few bonus transactions to the miner
		if i%3 == 2 {
			for j := 0; j < i%4+1; j++ {
				tx, err := types.SignTx(types.NewTransaction(block.TxNonce(address), common.Address{0x00}, big.NewInt(1000), params.TxGas, nil, nil), signer, key)
				if err != nil {
					panic(err)
				}
				block.AddTx(tx)
			}
		}
		// If the block number is a multiple of 5, add a few bonus uncles to the block
		if i%5 == 5 {
			block.AddUncle(&types.SnailHeader{ParentHash: block.PrevBlock(i - 1).Hash(), Number: big.NewInt(int64(i - 1))})
		}
	})
	// Import the chain as an archive node for the comparison baseline
	archiveDb := abeydb.NewMemDatabase()
	gspec.MustCommit(archiveDb)
	archive, _ := NewSnailBlockChain(archiveDb, gspec.Config, minerva.NewFaker(), vm.Config{})
	defer archive.Stop()

	if n, err := archive.InsertChain(blocks); err != nil {
		t.Fatalf("failed to process block %d: %v", n, err)
	}
	// Fast import the chain as a non-archive node to test
	fastDb := abeydb.NewMemDatabase()
	gspec.MustCommit(fastDb)
	fast, _ := NewSnailBlockChain(fastDb, gspec.Config, minerva.NewFaker(), vm.Config{})
	defer fast.Stop()

	headers := make([]*types.SnailHeader, len(blocks))
	for i, block := range blocks {
		headers[i] = block.Header()
	}
	if n, err := fast.InsertHeaderChain(headers, 1); err != nil {
		t.Fatalf("failed to insert header %d: %v", n, err)
	}
	if n, err := fast.InsertReceiptChain(blocks, receipts); err != nil {
		t.Fatalf("failed to insert receipt %d: %v", n, err)
	}
	// Iterate over all chain data components, and cross reference
	for i := 0; i < len(blocks); i++ {
		num, hash := blocks[i].NumberU64(), blocks[i].Hash()

		if ftd, atd := fast.GetTdByHash(hash), archive.GetTdByHash(hash); ftd.Cmp(atd) != 0 {
			t.Errorf("block #%d [%x]: td mismatch: have %v, want %v", num, hash, ftd, atd)
		}
		if fheader, aheader := fast.GetHeaderByHash(hash), archive.GetHeaderByHash(hash); fheader.Hash() != aheader.Hash() {
			t.Errorf("block #%d [%x]: header mismatch: have %v, want %v", num, hash, fheader, aheader)
		}
		if fblock, ablock := fast.GetBlockByHash(hash), archive.GetBlockByHash(hash); fblock.Hash() != ablock.Hash() {
			t.Errorf("block #%d [%x]: block mismatch: have %v, want %v", num, hash, fblock, ablock)
		}
		//else if types.DeriveSha(fblock.Fruits()) != types.DeriveSha(ablock.Fruits()) {
		//	t.Errorf("block #%d [%x]: transactions mismatch: have %v, want %v", num, hash, fblock.Transactions(), ablock.Transactions())
		//} else if types.CalcUncleHash(fblock.Uncles()) != types.CalcUncleHash(ablock.Uncles()) {
		//	t.Errorf("block #%d [%x]: uncles mismatch: have %v, want %v", num, hash, fblock.Uncles(), ablock.Uncles())
		//}
		if freceipts, areceipts := rawdb.ReadReceipts(fastDb, hash, *rawdb.ReadHeaderNumber(fastDb, hash)), rawdb.ReadReceipts(archiveDb, hash, *rawdb.ReadHeaderNumber(archiveDb, hash)); types.DeriveSha(freceipts) != types.DeriveSha(areceipts) {
			t.Errorf("block #%d [%x]: receipts mismatch: have %v, want %v", num, hash, freceipts, areceipts)
		}
	}
	// Check that the canonical chains are the same between the databases
	for i := 0; i < len(blocks)+1; i++ {
		if fhash, ahash := rawdb.ReadCanonicalHash(fastDb, uint64(i)), rawdb.ReadCanonicalHash(archiveDb, uint64(i)); fhash != ahash {
			t.Errorf("block #%d: canonical hash mismatch: have %v, want %v", i, fhash, ahash)
		}
	}
}
*/
// Tests that various import methods move the chain head pointers to the correct
// positions.
/*func TestLightVsFastVsFullChainHeads(t *testing.T) {
	// Configure and generate a sample block chain
	var (
		gendb   = abeydb.NewMemDatabase()
		//key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		//address = crypto.PubkeyToAddress(key.PublicKey)
		//funds   = big.NewInt(1000000000)
		gspec   = &Genesis{Config: params.TestChainConfig,
		//Alloc: GenesisAlloc{address: {Balance: funds}},
		}
		genesis = gspec.MustCommit(gendb)
	)
	height := uint64(1024)
	blocks := GenerateChain(gspec.Config, genesis, minerva.NewFaker(), gendb, int(height), nil)

	// Configure a subchain to roll back
	remove := []common.Hash{}
	for _, block := range blocks[height/2:] {
		remove = append(remove, block.Hash())
	}
	// Create a small assertion method to check the three heads
	assert := func(t *testing.T, kind string, chain *SnailBlockChain, header uint64, fast uint64, block uint64) {
		if num := chain.CurrentBlock().NumberU64(); num != block {
			t.Errorf("%s head block mismatch: have #%v, want #%v", kind, num, block)
		}
		if num := chain.CurrentFastBlock().NumberU64(); num != fast {
			t.Errorf("%s head fast-block mismatch: have #%v, want #%v", kind, num, fast)
		}
		if num := chain.CurrentHeader().Number.Uint64(); num != header {
			t.Errorf("%s head header mismatch: have #%v, want #%v", kind, num, header)
		}
	}
	// Import the chain as an archive node and ensure all pointers are updated
	archiveDb := abeydb.NewMemDatabase()
	gspec.MustCommit(archiveDb)

	archive, _ := NewSnailBlockChain(archiveDb, gspec.Config, minerva.NewFaker(), vm.Config{})
	if n, err := archive.InsertChain(blocks); err != nil {
		t.Fatalf("failed to process block %d: %v", n, err)
	}
	defer archive.Stop()

	assert(t, "archive", archive, height, height, height)
	archive.Rollback(remove)
	assert(t, "archive", archive, height/2, height/2, height/2)

	// Import the chain as a non-archive node and ensure all pointers are updated
	fastDb := abeydb.NewMemDatabase()
	gspec.MustCommit(fastDb)
	fast, _ := NewSnailBlockChain(fastDb, gspec.Config, minerva.NewFaker(), vm.Config{})
	defer fast.Stop()

	headers := make([]*types.SnailHeader, len(blocks))
	for i, block := range blocks {
		headers[i] = block.Header()
	}
	if n, err := fast.InsertHeaderChain(headers, 1); err != nil {
		t.Fatalf("failed to insert header %d: %v", n, err)
	}

	assert(t, "fast", fast, height, height, 0)
	fast.Rollback(remove)
	assert(t, "fast", fast, height/2, height/2, 0)

	// Import the chain as a light node and ensure all pointers are updated
	lightDb := abeydb.NewMemDatabase()
	gspec.MustCommit(lightDb)

	light, _ := NewSnailBlockChain(lightDb, gspec.Config, minerva.NewFaker(), vm.Config{})
	if n, err := light.InsertHeaderChain(headers, 1); err != nil {
		t.Fatalf("failed to insert header %d: %v", n, err)
	}
	defer light.Stop()

	assert(t, "light", light, height, 0, 0)
	light.Rollback(remove)
	assert(t, "light", light, height/2, 0, 0)
}
*/
// Tests that chain reorganisations handle transaction removals and reinsertions.
/*func TestChainTxReorgs(t *testing.T) {
	var (
		key1, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		key2, _ = crypto.HexToECDSA("8a1f9a8f95be41cd7ccb6168179afb4504aefe388d1e14474d32c45c72ce7b7a")
		key3, _ = crypto.HexToECDSA("49a7b37aa6f6645917e7b807e9d1c00d4fa71f18343b0d4122a4d2df64dd6fee")
		addr1   = crypto.PubkeyToAddress(key1.PublicKey)
		addr2   = crypto.PubkeyToAddress(key2.PublicKey)
		addr3   = crypto.PubkeyToAddress(key3.PublicKey)
		db      = abeydb.NewMemDatabase()
		gspec   = &Genesis{
			Config:   params.TestChainConfig,
			GasLimit: 3141592,
			Alloc: types.GenesisAlloc{
				addr1: {Balance: big.NewInt(1000000)},
				addr2: {Balance: big.NewInt(1000000)},
				addr3: {Balance: big.NewInt(1000000)},
			},
		}
		genesis = gspec.MustCommit(db)
	)

	// Create two transactions shared between the chains:
	//  - postponed: transaction included at a later block in the forked chain
	//  - swapped: transaction included at the same block number in the forked chain
	postponed := types.NewFruitWithHeader(&types.SnailHeader{Number: big.NewInt(42), Extra: []byte("test header")})
	swapped := types.NewFruitWithHeader(&types.SnailHeader{Number: big.NewInt(42), Extra: []byte("test header")})

	// Create two transactions that will be dropped by the forked chain:
	//  - pastDrop: transaction dropped retroactively from a past block
	//  - freshDrop: transaction dropped exactly at the block where the reorg is detected
	var pastDrop, freshDrop *types.SnailBlock

	// Create three transactions that will be added in the forked chain:
	//  - pastAdd:   transaction added before the reorganization is detected
	//  - freshAdd:  transaction added at the exact block the reorg is detected
	//  - futureAdd: transaction added after the reorg has already finished
	var pastAdd, freshAdd, futureAdd *types.SnailBlock

	chain := GenerateChain(gspec.Config, genesis, minerva.NewFaker(), db, 3, func(i int, gen *BlockGen) {
		switch i {
		case 0:
			pastDrop = types.NewFruitWithHeader(&types.SnailHeader{Number: big.NewInt(42), Extra: []byte("test header")})

			gen.AddFruit(pastDrop)  // This transaction will be dropped in the fork from below the split point
			gen.AddFruit(postponed) // This transaction will be postponed till block #3 in the fork

		case 2:
			freshDrop = types.NewFruitWithHeader(&types.SnailHeader{Number: big.NewInt(42), Extra: []byte("test header")})

			gen.AddFruit(freshDrop) // This transaction will be dropped in the fork from exactly at the split point
			gen.AddFruit(swapped)   // This transaction will be swapped out at the exact height

			gen.OffsetTime(9) // Lower the block difficulty to simulate a weaker chain
		}
	})
	// Import the chain. This runs all block validation rules.
	blockchain, _ := NewSnailBlockChain(db, gspec.Config, minerva.NewFaker(), vm.Config{})
	if i, err := blockchain.InsertChain(chain); err != nil {
		t.Fatalf("failed to insert original chain[%d]: %v", i, err)
	}
	defer blockchain.Stop()

	// overwrite the old chain
	chain = GenerateChain(gspec.Config, genesis, minerva.NewFaker(), db, 5, func(i int, gen *BlockGen) {
		switch i {
		case 0:
			pastAdd = types.NewFruitWithHeader(&types.SnailHeader{Number: big.NewInt(42), Extra: []byte("test header")})
			gen.AddFruit(pastAdd) // This transaction needs to be injected during reorg

		case 2:
			gen.AddFruit(postponed) // This transaction was postponed from block #1 in the original chain
			gen.AddFruit(swapped)   // This transaction was swapped from the exact current spot in the original chain

			freshAdd = types.NewFruitWithHeader(&types.SnailHeader{Number: big.NewInt(42), Extra: []byte("test header")})
			gen.AddFruit(freshAdd) // This transaction will be added exactly at reorg time

		case 3:
			futureAdd = types.NewFruitWithHeader(&types.SnailHeader{Number: big.NewInt(42), Extra: []byte("test header")})
			gen.AddFruit(futureAdd) // This transaction will be added after a full reorg
		}
	})
	if _, err := blockchain.InsertChain(chain); err != nil {
		t.Fatalf("failed to insert forked chain: %v", err)
	}

	// removed tx
	for i, tx := range (types.Fruits{pastDrop, freshDrop}) {
		if txn, _, _, _ := rawdb.ReadFruit(db, tx.Hash()); txn != nil {
			t.Errorf("drop %d: tx %v found while shouldn't have been", i, txn)
		}
		if rcpt, _, _, _ := rawdb.ReadReceipt(db, tx.Hash()); rcpt != nil {
			t.Errorf("drop %d: receipt %v found while shouldn't have been", i, rcpt)
		}
	}
	// added tx
	for i, tx := range (types.Fruits{pastAdd, freshAdd, futureAdd}) {
		if txn, _, _, _ := rawdb.ReadFruit(db, tx.Hash()); txn == nil {
			t.Errorf("add %d: expected tx to be found", i)
		}
		if rcpt, _, _, _ := rawdb.ReadReceipt(db, tx.Hash()); rcpt == nil {
			t.Errorf("add %d: expected receipt to be found", i)
		}
	}
	// shared tx
	for i, tx := range (types.Fruits{postponed, swapped}) {
		if txn, _, _, _ := rawdb.ReadFruit(db, tx.Hash()); txn == nil {
			t.Errorf("share %d: expected tx to be found", i)
		}
		if rcpt, _, _, _ := rawdb.ReadReceipt(db, tx.Hash()); rcpt == nil {
			t.Errorf("share %d: expected receipt to be found", i)
		}
	}
}
*/

/*func TestReorgSideEvent(t *testing.T) {
	var (
		db      = abeydb.NewMemDatabase()
		key1, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		addr1   = crypto.PubkeyToAddress(key1.PublicKey)
		gspec   = &Genesis{
			Config: params.TestChainConfig,
			//Alloc:  GenesisAlloc{addr1: {Balance: big.NewInt(10000000000000)}},
		}
		genesis = gspec.MustCommit(db)
		signer  = types.NewTIP1Signer(gspec.Config.ChainID)
	)

	blockchain, _ := NewSnailBlockChain(db, gspec.Config, minerva.NewFaker(), vm.Config{})
	defer blockchain.Stop()

	chain := GenerateChain(gspec.Config, genesis, minerva.NewFaker(), db, 3, func(i int, gen *BlockGen) {})
	if _, err := blockchain.InsertChain(chain); err != nil {
		t.Fatalf("failed to insert chain: %v", err)
	}

	replacementBlocks := GenerateChain(gspec.Config, genesis, minerva.NewFaker(), db, 4, func(i int, gen *BlockGen) {
		tx, err := types.SignTx(types.NewContractCreation(gen.TxNonce(addr1), new(big.Int), 1000000, new(big.Int), nil), signer, key1)
		if i == 2 {
			gen.OffsetTime(-9)
		}
		if err != nil {
			t.Fatalf("failed to create tx: %v", err)
		}
		gen.AddTx(tx)
	})
	chainSideCh := make(chan ChainSideEvent, 64)
	blockchain.SubscribeChainSideEvent(chainSideCh)
	if _, err := blockchain.InsertChain(replacementBlocks); err != nil {
		t.Fatalf("failed to insert chain: %v", err)
	}

	// first two block of the secondary chain are for a brief moment considered
	// side chains because up to that point the first one is considered the
	// heavier chain.
	expectedSideHashes := map[common.Hash]bool{
		replacementBlocks[0].Hash(): true,
		replacementBlocks[1].Hash(): true,
		chain[0].Hash():             true,
		chain[1].Hash():             true,
		chain[2].Hash():             true,
	}

	i := 0

	const timeoutDura = 10 * time.Second
	timeout := time.NewTimer(timeoutDura)
done:
	for {
		select {
		case ev := <-chainSideCh:
			block := ev.Block
			if _, ok := expectedSideHashes[block.Hash()]; !ok {
				t.Errorf("%d: didn't expect %x to be in side chain", i, block.Hash())
			}
			i++

			if i == len(expectedSideHashes) {
				timeout.Stop()

				break done
			}
			timeout.Reset(timeoutDura)

		case <-timeout.C:
			t.Fatal("Timeout. Possibly not all blocks were triggered for sideevent")
		}
	}

	// make sure no more events are fired
	select {
	case e := <-chainSideCh:
		t.Errorf("unexpected event fired: %v", e)
	case <-time.After(250 * time.Millisecond):
	}

}
*/
// Tests if the canonical block can be fetched from the database during chain insertion.
func TestCanonicalBlockRetrieval(t *testing.T) {
	_, blockchain, fastChain, err := newCanonical(minerva.NewFaker(), 0, true)
	if err != nil {
		t.Fatalf("failed to create pristine chain: %v", err)
	}
	defer blockchain.Stop()

	chain := GenerateChain(blockchain.chainConfig, fastChain, blockchain.GetBlocksFromNumber(blockchain.genesisBlock.NumberU64()), 10, 7, func(i int, gen *BlockGen) {})

	var pend sync.WaitGroup
	pend.Add(len(chain))

	for i := range chain {
		go func(block *types.SnailBlock) {
			defer pend.Done()

			// try to retrieve a block by its canonical hash and see if the block data can be retrieved.
			for {
				ch := rawdb.ReadCanonicalHash(blockchain.db, block.NumberU64())
				if ch == (common.Hash{}) {
					continue // busy wait for canonical hash to be written
				}
				if ch != block.Hash() {
					t.Fatalf("unknown canonical hash, want %s, got %s", block.Hash().Hex(), ch.Hex())
				}
				fb := rawdb.ReadBlock(blockchain.db, ch, block.NumberU64())
				if fb == nil {
					t.Fatalf("unable to retrieve block %d for canonical hash: %s", block.NumberU64(), ch.Hex())
				}
				if fb.Hash() != block.Hash() {
					t.Fatalf("invalid block hash for block %d, want %s, got %s", block.NumberU64(), block.Hash().Hex(), fb.Hash().Hex())
				}
				return
			}
		}(chain[i])

		if _, err := blockchain.InsertChain(types.SnailBlocks{chain[i]}); err != nil {
			t.Fatalf("failed to insert block %d: %v", i, err)
		}
	}
	pend.Wait()
}

/*func TestEIP155Transition(t *testing.T) {
	// Configure and generate a sample block chain
	var (
		db         = abeydb.NewMemDatabase()
		key, _     = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address    = crypto.PubkeyToAddress(key.PublicKey)
		//funds      = big.NewInt(1000000000)
		//deleteAddr = common.Address{1}
		gspec      = &Genesis{
			Config: &params.ChainConfig{ChainID: big.NewInt(1), EIP155Block: big.NewInt(2), HomesteadBlock: new(big.Int)},
			//Alloc:  GenesisAlloc{address: {Balance: funds}, deleteAddr: {Balance: new(big.Int)}},
		}
		genesis = gspec.MustCommit(db)
	)

	blockchain, _ := NewSnailBlockChain(db, gspec.Config, minerva.NewFaker(), vm.Config{})
	defer blockchain.Stop()

	blocks := GenerateChain(gspec.Config, genesis, minerva.NewFaker(), db, 4, func(i int, block *BlockGen) {
		var (
			tx      *types.Transaction
			err     error
			basicTx = func(signer types.Signer) (*types.Transaction, error) {
				return types.SignTx(types.NewTransaction(block.TxNonce(address), common.Address{}, new(big.Int), 21000, new(big.Int), nil), signer, key)
			}
		)
		switch i {
		case 0:
			tx, err = basicTx(types.HomesteadSigner{})
			if err != nil {
				t.Fatal(err)
			}
			block.AddTx(tx)
		case 2:
			tx, err = basicTx(types.HomesteadSigner{})
			if err != nil {
				t.Fatal(err)
			}
			block.AddTx(tx)

			tx, err = basicTx(types.NewTIP1Signer(gspec.Config.ChainID))
			if err != nil {
				t.Fatal(err)
			}
			block.AddTx(tx)
		case 3:
			tx, err = basicTx(types.HomesteadSigner{})
			if err != nil {
				t.Fatal(err)
			}
			block.AddTx(tx)

			tx, err = basicTx(types.NewTIP1Signer(gspec.Config.ChainID))
			if err != nil {
				t.Fatal(err)
			}
			block.AddTx(tx)
		}
	})

	if _, err := blockchain.InsertChain(blocks); err != nil {
		t.Fatal(err)
	}
	//block := blockchain.GetBlockByNumber(1)
	//if block.Transactions()[0].Protected() {
	//	t.Error("Expected block[0].txs[0] to not be replay protected")
	//}
	//
	//block = blockchain.GetBlockByNumber(3)
	//if block.Transactions()[0].Protected() {
	//	t.Error("Expected block[3].txs[0] to not be replay protected")
	//}
	//if !block.Transactions()[1].Protected() {
	//	t.Error("Expected block[3].txs[1] to be replay protected")
	//}
	if _, err := blockchain.InsertChain(blocks[4:]); err != nil {
		t.Fatal(err)
	}

	// generate an invalid chain id transaction
	config := &params.ChainConfig{ChainID: big.NewInt(2), EIP155Block: big.NewInt(2), HomesteadBlock: new(big.Int)}
	blocks = GenerateChain(config, blocks[len(blocks)-1], minerva.NewFaker(), db, 4, func(i int, block *BlockGen) {
		var (
			tx      *types.Transaction
			err     error
			basicTx = func(signer types.Signer) (*types.Transaction, error) {
				return types.SignTx(types.NewTransaction(block.TxNonce(address), common.Address{}, new(big.Int), 21000, new(big.Int), nil), signer, key)
			}
		)
		if i == 0 {
			tx, err = basicTx(types.NewTIP1Signer(big.NewInt(2)))
			if err != nil {
				t.Fatal(err)
			}
			block.AddTx(tx)
		}
	})
	_, err := blockchain.InsertChain(blocks)
	if err != types.ErrInvalidChainId {
		t.Error("expected error:", types.ErrInvalidChainId)
	}
}
*/
/*func TestEIP161AccountRemoval(t *testing.T) {
	// Configure and generate a sample block chain
	var (
		db      = abeydb.NewMemDatabase()
		key, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
		address = crypto.PubkeyToAddress(key.PublicKey)
		//funds   = big.NewInt(1000000000)
		theAddr = common.Address{1}
		gspec   = &Genesis{
			Config: &params.ChainConfig{
				ChainID:        big.NewInt(1),
			},
			//Alloc: GenesisAlloc{address: {Balance: funds}},
		}
		genesis = gspec.MustCommit(db)
	)
	blockchain, _ := NewSnailBlockChain(db, gspec.Config, minerva.NewFaker(), vm.Config{})
	defer blockchain.Stop()

	blocks := GenerateChain(gspec.Config, genesis, minerva.NewFaker(), db, 3, func(i int, block *BlockGen) {
		var (
			tx     *types.Transaction
			err    error
			signer = types.NewTIP1Signer(gspec.Config.ChainID)
		)
		switch i {
		case 0:
			tx, err = types.SignTx(types.NewTransaction(block.TxNonce(address), theAddr, new(big.Int), 21000, new(big.Int), nil), signer, key)
		case 1:
			tx, err = types.SignTx(types.NewTransaction(block.TxNonce(address), theAddr, new(big.Int), 21000, new(big.Int), nil), signer, key)
		case 2:
			tx, err = types.SignTx(types.NewTransaction(block.TxNonce(address), theAddr, new(big.Int), 21000, new(big.Int), nil), signer, key)
		}
		if err != nil {
			t.Fatal(err)
		}
		block.AddTx(tx)
	})
	// account must exist pre eip 161
	if _, err := blockchain.InsertChain(types.SnailBlocks{blocks[0]}); err != nil {
		t.Fatal(err)
	}
	if st, _ := blockchain.State(); !st.Exist(theAddr) {
		t.Error("expected account to exist")
	}

	// account needs to be deleted post eip 161
	if _, err := blockchain.InsertChain(types.SnailBlocks{blocks[1]}); err != nil {
		t.Fatal(err)
	}
	if st, _ := blockchain.State(); st.Exist(theAddr) {
		t.Error("account should not exist")
	}

	// account musn't be created post eip 161
	if _, err := blockchain.InsertChain(types.SnailBlocks{blocks[2]}); err != nil {
		t.Fatal(err)
	}
	if st, _ := blockchain.State(); st.Exist(theAddr) {
		t.Error("account should not exist")
	}
}
*/
// This is a regression test (i.e. as weird as it is, don't delete it ever), which
// tests that under weird reorg conditions the blockchain and its internal header-
// chain return the same latest block/header.
//
// https://github.com/abeychain/go-abey/pull/15941
func TestBlockchainHeaderchainReorgConsistency(t *testing.T) {
	// Generate a canonical chain to act as the main dataset
	engine := minerva.NewFaker()

	db := abeydb.NewMemDatabase()
	commonGenesis := core.DefaultGenesisBlock()
	genesis := commonGenesis.MustSnailCommit(db)
	_, fastChain, _ := core.NewCanonical(engine, 0, true)
	blocks := GenerateChain(params.TestChainConfig, fastChain, []*types.SnailBlock{genesis}, 64, 7, func(i int, b *BlockGen) { b.SetCoinbase(common.Address{1}) })

	// Generate a bunch of fork blocks, each side forking from the canonical chain
	forks := make([]*types.SnailBlock, len(blocks))
	for i := 0; i < len(forks); i++ {
		//parent := genesis
		parents := []*types.SnailBlock{genesis}
		if i > 0 {
			//parent = blocks[i-1]
			parents = blocks[0:i]
		}
		fork := GenerateChain(params.TestChainConfig, fastChain, parents, 1, 7, func(i int, b *BlockGen) { b.SetCoinbase(common.Address{2}) })
		forks[i] = fork[0]
	}
	// Import the canonical and fork chain side by side, verifying the current block
	// and current header consistency
	diskdb := abeydb.NewMemDatabase()
	commonGenesis.MustSnailCommit(diskdb)

	chain, err := NewSnailBlockChain(diskdb, params.TestChainConfig, engine, fastChain)
	if err != nil {
		t.Fatalf("failed to create tester chain: %v", err)
	}
	for i := 0; i < len(blocks); i++ {
		if _, err := chain.InsertChain(blocks[i : i+1]); err != nil {
			t.Fatalf("block %d: failed to insert into chain: %v", i, err)
		}
		if chain.CurrentBlock().Hash() != chain.CurrentHeader().Hash() {
			t.Errorf("block %d: current block/header mismatch: block #%d [%x…], header #%d [%x…]", i, chain.CurrentBlock().Number(), chain.CurrentBlock().Hash().Bytes()[:4], chain.CurrentHeader().Number, chain.CurrentHeader().Hash().Bytes()[:4])
		}
		if _, err := chain.InsertChain(forks[i : i+1]); err != nil {
			t.Fatalf(" fork %d: failed to insert into chain: %v", i, err)
		}
		if chain.CurrentBlock().Hash() != chain.CurrentHeader().Hash() {
			t.Errorf(" fork %d: current block/header mismatch: block #%d [%x…], header #%d [%x…]", i, chain.CurrentBlock().Number(), chain.CurrentBlock().Hash().Bytes()[:4], chain.CurrentHeader().Number, chain.CurrentHeader().Hash().Bytes()[:4])
		}
	}
}

func testRewardOrg(t *testing.T, n int) {
	//params.MinimumFruits = 1
	params.MinTimeGap = big.NewInt(0)
	var (
		db  = abeydb.NewMemDatabase()
		pow = minerva.NewFaker()

		gspec = &core.Genesis{
			Config: params.TestChainConfig,
			//Alloc:      types.GenesisAlloc{addr1: {Balance: big.NewInt(3000000)}},
			Difficulty: big.NewInt(20000),
		}
		genesis      = gspec.MustFastCommit(db)
		snailGenesis = gspec.MustSnailCommit(db)
	)

	var (
		snailRewardBlock *types.SnailBlock
		snailBlocks      []*types.SnailBlock
		allSnailBlocks   []*types.SnailBlock
		fastParent       = genesis
		fastBlocks       []*types.Block
	)
	//generate blockchain
	blockchain, _ := core.NewBlockChain(db, nil, gspec.Config, pow, vm.Config{})
	defer blockchain.Stop()
	snailChain, _ := NewSnailBlockChain(db, gspec.Config, pow, blockchain)
	defer snailChain.Stop()
	pow.SetSnailChainReader(snailChain)
	allSnailBlocks = append(allSnailBlocks, []*types.SnailBlock{snailGenesis}...)
	for i := 1; i < geneSnailBlockNumber; i++ {
		log.Info("getInfo", "i", i, "blockchain", fastParent.NumberU64(), "snailNumber", snailChain.CurrentBlock().NumberU64())

		fastBlocks, _ = core.GenerateChainWithReward(gspec.Config, fastParent, snailRewardBlock, pow, db, n*params.MinimumFruits, nil)
		if i, err := blockchain.InsertChain(fastBlocks); err != nil {
			fmt.Printf("insert error (block %d): %v\n", fastBlocks[i].NumberU64(), err)
			return
		}
		fastParent = blockchain.CurrentBlock()

		if i == 1 {
			snailBlocks = GenerateChain(gspec.Config, blockchain, []*types.SnailBlock{snailGenesis}, n, 7, nil)
		} else {
			snailBlocks = GenerateChain(gspec.Config, blockchain, snailChain.GetBlocksFromNumber(0), n, 7, nil)
		}
		if _, err := snailChain.InsertChain(snailBlocks); err != nil {
			panic(err)
		}
		snailRewardBlock = snailChain.CurrentBlock()
		allSnailBlocks = append(allSnailBlocks, snailBlocks...)
	}
	fastBlocks, _ = core.GenerateChain(gspec.Config, fastBlocks[len(fastBlocks)-1], pow, db, 3*params.MinimumFruits, nil)
	if i, err := blockchain.InsertChain(fastBlocks); err != nil {
		fmt.Printf("insert error (block %d): %v\n", fastBlocks[i].NumberU64(), err)
		return
	}
	snailRewardBlock = snailChain.CurrentBlock()
	log.Info("testRewardOrg1", "hash", snailChain.CurrentBlock().Hash(), "number", snailChain.CurrentBlock().Number())
	diffparents := allSnailBlocks[:]
	log.Info("len", "diffparents", len(diffparents), "allSnailBlocks", len(allSnailBlocks))
	//genesis := commonGenesis.MustSnailCommit(db)
	diffBlocks := GenerateChain(gspec.Config, blockchain, diffparents, 2, 7, func(i int, b *BlockGen) {
		b.OffsetTime(51)
	})
	if _, err := snailChain.InsertChain(diffBlocks); err != nil {
		t.Fatalf("err is mismatch, err is: %v", err)
	}
	diffparents = append(diffparents, diffBlocks...)
	log.Info("len", "diffparents", len(diffparents), "allSnailBlocks", len(allSnailBlocks))
	log.Info("testRewardOrg2", "hash", snailChain.CurrentBlock().Hash(), "number", snailChain.CurrentBlock().Number())
	easyBlocks := GenerateChain(gspec.Config, blockchain, allSnailBlocks[:], 3, 7, func(i int, b *BlockGen) {
		b.OffsetTime(50)
	})
	if _, err := snailChain.InsertChain(easyBlocks); err != nil {
		t.Fatalf("err is mismatch, err is: %v", err)
	}
	log.Info("testRewardOrg3", "hash", snailChain.CurrentBlock().Hash(), "number", snailChain.CurrentBlock().Number())

	pow.SetSnailChainReader(snailChain)
	fastBlocks, _ = core.GenerateChainWithReward(gspec.Config, fastBlocks[len(fastBlocks)-1], snailRewardBlock, pow, db, n*params.MinimumFruits, nil)
	if i, err := blockchain.InsertChain(fastBlocks); err != nil {
		fmt.Printf("insert error (block %d): %v\n", fastBlocks[i].NumberU64(), err)
		return
	}
	fastBlocks, _ = core.GenerateChainWithReward(gspec.Config, fastBlocks[len(fastBlocks)-1], easyBlocks[0], pow, db, n*params.MinimumFruits, nil)
	if i, err := blockchain.InsertChain(fastBlocks); err != nil {
		fmt.Printf("insert error (block %d): %v\n", fastBlocks[i].NumberU64(), err)
		return
	}

	diffBlocks = GenerateChain(gspec.Config, blockchain, diffparents, 2, 7, func(i int, b *BlockGen) {
		b.OffsetTime(51)
	})
	if _, err := snailChain.InsertChain(diffBlocks); err != ErrRewardedBlock {
		t.Fatalf("err is wrong want:%v; is: %v", ErrRewardedBlock, err)
	}
	log.Info("TestReorgRward end", "current hash", snailChain.CurrentBlock().Hash(), "current number", snailChain.CurrentBlock().Number(), "number 11 is equall", diffparents[len(diffparents)-2].Hash() == snailChain.GetBlockByNumber(diffparents[len(diffparents)-2].NumberU64()).Hash())
}

func TestReorgRward(t *testing.T) { testRewardOrg(t, 1) }
