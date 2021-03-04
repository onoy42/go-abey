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

package downloader

import (
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abeychain/go-abey/core/snailchain"
	"github.com/abeychain/go-abey/core/vm"
	"github.com/abeychain/go-abey/abey/fastdownloader"

	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/crypto"
	"github.com/abeychain/go-abey/consensus/minerva"
	"github.com/abeychain/go-abey/core"
	"github.com/abeychain/go-abey/core/types"
	dtypes "github.com/abeychain/go-abey/abey/types"
	"github.com/abeychain/go-abey/abeydb"
	"github.com/abeychain/go-abey/event"
	"github.com/abeychain/go-abey/params"
	"github.com/abeychain/go-abey/trie"
)

var (
	testKey, _      = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	testAddress     = crypto.PubkeyToAddress(testKey.PublicKey)
	fsMinFullBlocks int
)

// Reduce some of the parameters to make the tester faster.
func init() {
	MaxForkAncestry = uint64(10000)
	blockCacheItems = 25
	fsHeaderContCheck = 500 * time.Millisecond
}

// downloadTester is a test simulator for mocking out local block chain.
type downloadTester struct {
	downloader  *Downloader
	fdownloader *fastdownloader.Downloader
	ftester     *fastdownloader.DownloadTester
	genesis     *types.SnailBlock // Genesis blocks used by the tester and peers
	stateDb     abeydb.Database  // Database used by the tester for syncing from peers
	peerDb      abeydb.Database  // Database of the peers containing all data

	ownHashes   []common.Hash                      // Hash chain belonging to the tester
	ownHeaders  map[common.Hash]*types.SnailHeader // Headers belonging to the tester
	ownBlocks   map[common.Hash]*types.SnailBlock  // Blocks belonging to the tester
	ownReceipts map[common.Hash]types.Receipts     // Receipts belonging to the tester
	ownChainTd  map[common.Hash]*big.Int           // Total difficulties of the blocks in the local chain

	peerHashes   map[string][]common.Hash                      // Hash chain belonging to different test peers
	peerHeaders  map[string]map[common.Hash]*types.SnailHeader // Headers belonging to different test peers
	peerBlocks   map[string]map[common.Hash]*types.SnailBlock  // Blocks belonging to different test peers
	peerReceipts map[string]map[common.Hash]types.Receipts     // Receipts belonging to different test peers
	peerChainTds map[string]map[common.Hash]*big.Int           // Total difficulties of the blocks in the peer chains

	peerMissingStates map[string]map[common.Hash]bool // State entries that fast sync should not return

	lock sync.RWMutex
}

// newTester creates a new downloader test mocker.
func newTester() *downloadTester {
	testdb := abeydb.NewMemDatabase()
	genesis := core.GenesisSnailBlockForTesting(testdb, testAddress, big.NewInt(1000000000))

	tester := &downloadTester{
		genesis:           genesis,
		peerDb:            testdb,
		ownHashes:         []common.Hash{genesis.Hash()},
		ownHeaders:        map[common.Hash]*types.SnailHeader{genesis.Hash(): genesis.Header()},
		ownBlocks:         map[common.Hash]*types.SnailBlock{genesis.Hash(): genesis},
		ownChainTd:        map[common.Hash]*big.Int{genesis.Hash(): genesis.Difficulty()},
		peerHashes:        make(map[string][]common.Hash),
		peerHeaders:       make(map[string]map[common.Hash]*types.SnailHeader),
		peerBlocks:        make(map[string]map[common.Hash]*types.SnailBlock),
		peerChainTds:      make(map[string]map[common.Hash]*big.Int),
		peerMissingStates: make(map[string]map[common.Hash]bool),
	}

	tester.stateDb = abeydb.NewMemDatabase()
	tester.ftester = fastdownloader.NewTester(testdb, tester.stateDb)

	tester.downloader = New(FullSync, 0, tester.stateDb, new(event.TypeMux), tester, nil, tester.dropPeer, tester.ftester.GetDownloader())
	tester.fdownloader = tester.ftester.GetDownloader()

	return tester
}

func (dl *downloadTester) makeFastChain(n int) ([]common.Hash, map[common.Hash]*types.Header, map[common.Hash]*types.Block, map[common.Hash]types.Receipts, *core.BlockChain, *types.Header) {

	// Initialize a fresh chain with only a genesis block
	var (
		testdb   = dl.peerDb
		fgenesis = dl.ftester.GetGenesis()
		engine   = minerva.NewFaker()
	)

	cache := &core.CacheConfig{}
	fastChain, _ := core.NewBlockChain(testdb, cache, params.AllMinervaProtocolChanges, engine, vm.Config{})

	fastblocks, receipts := core.GenerateChain(params.TestChainConfig, fgenesis, engine, testdb, n*params.MinimumFruits, nil)
	fastChain.InsertChain(fastblocks)

	var remoteHeader *types.Header

	if len(fastblocks) > 16 && len(fastblocks) != 0 {
		remoteHeader = fastblocks[len(fastblocks)-16].Header()
	} else if len(fastblocks) != 0 {
		remoteHeader = fastblocks[len(fastblocks)-1].Header()
	} else {
		remoteHeader = fgenesis.Header()
	}

	fn := n * params.MinimumFruits
	// Convert the block-chain into a hash-chain and header/block maps
	fhashes := make([]common.Hash, fn+1)
	fhashes[len(fhashes)-1] = fgenesis.Hash()

	fheaderm := make(map[common.Hash]*types.Header, fn+1)
	fheaderm[fgenesis.Hash()] = fgenesis.Header()

	fblockm := make(map[common.Hash]*types.Block, fn+1)
	fblockm[fgenesis.Hash()] = fgenesis

	receiptm := make(map[common.Hash]types.Receipts, fn+1)
	receiptm[fgenesis.Hash()] = nil

	for i, b := range fastblocks {
		fhashes[len(fhashes)-i-2] = b.Hash()
		fheaderm[b.Hash()] = b.Header()
		fblockm[b.Hash()] = b
		receiptm[b.Hash()] = receipts[i]
	}

	return fhashes, fheaderm, fblockm, receiptm, fastChain, remoteHeader
}

// makeChain creates a chain of n blocks starting at and including parent.
// the returned hash chain is ordered 0.
// head->parent. In addition, every 3rd block
// contains a transaction and every 5th an uncle to allow testing correct block
// reassembly.
func (dl *downloadTester) makeChain(n int, seed byte, parents []*types.SnailBlock, heavy bool, fastChain *core.BlockChain, DifficultyLevel int) ([]common.Hash, map[common.Hash]*types.SnailHeader, map[common.Hash]*types.SnailBlock, []*types.SnailBlock) {

	// Initialize a new chain
	var (
		testdb = dl.peerDb
		engine = minerva.NewFaker()
	)

	snailChain, _ := snailchain.NewSnailBlockChain(testdb, params.TestChainConfig, engine, fastChain)

	var blocks1 []*types.SnailBlock
	blocks1 = append(blocks1, parents...)

	mconfig := snailchain.MakechianConfig{
		FruitNumber:     uint64(params.MinimumFruits),
		FruitFresh:      int64(7),
		DifficultyLevel: DifficultyLevel,
	}

	blocks, _ := snailchain.MakeSnailBlocks(fastChain, snailChain, parents, int64(n), mconfig)
	for _, block := range blocks {
		blocks1 = append(blocks1, block)
	}

	blocks1 = blocks1[1:]

	parent := parents[len(parents)-1]
	// Convert the block-chain into a hash-chain and header/block maps
	hashes := make([]common.Hash, n+1)
	hashes[len(hashes)-1] = parent.Hash()

	headerm := make(map[common.Hash]*types.SnailHeader, n+1)
	headerm[parent.Hash()] = parent.Header()

	blockm := make(map[common.Hash]*types.SnailBlock, n+1)
	blockm[parent.Hash()] = parent

	for i, b := range blocks {
		hashes[len(hashes)-i-2] = b.Hash()
		headerm[b.Hash()] = b.Header()
		blockm[b.Hash()] = b
	}

	return hashes, headerm, blockm, blocks
}

// makeChainFork creates two chains of length n, such that h1[:f] and
// h2[:f] are different but have a common suffix of length n-f.
func (dl *downloadTester) makeChainFork(n, f int, parent *types.SnailBlock, balanced bool) ([]common.Hash, []common.Hash, map[common.Hash]*types.SnailHeader, map[common.Hash]*types.SnailHeader, map[common.Hash]*types.SnailBlock, map[common.Hash]*types.SnailBlock, []common.Hash, map[common.Hash]*types.Header, map[common.Hash]*types.Block, map[common.Hash]types.Receipts, *types.Header) {
	// Create the common suffix
	//parents := make([]*types.SnailBlock,1)
	var parents []*types.SnailBlock

	parents = append(parents, parent)
	fhashes, fheaderm, fblockm, receiptm, fastChain, remoteHeader := dl.makeFastChain(n + f)

	hashes, headers, blocks, blocks_ := dl.makeChain(n-f, 0, parents, false, fastChain, 1)
	for _, block := range blocks_ {
		parents = append(parents, block)
	}

	// Create the forks, making the second heavier if non balanced forks were requested
	hashes1, headers1, blocks1, _ := dl.makeChain(f, 1, parents, false, fastChain, 1)
	hashes1 = append(hashes1, hashes[1:]...)

	heavy := false
	if !balanced {
		heavy = true
	}
	hashes2, headers2, blocks2, _ := dl.makeChain(f, 1, parents, heavy, fastChain, 2)
	hashes2 = append(hashes2, hashes[1:]...)

	for hash, header := range headers {
		headers1[hash] = header
		headers2[hash] = header
	}
	for hash, block := range blocks {
		blocks1[hash] = block
		blocks2[hash] = block
	}

	return hashes1, hashes2, headers1, headers2, blocks1, blocks2, fhashes, fheaderm, fblockm, receiptm, remoteHeader
}

// terminate aborts any operations on the embedded downloader and releases all
// held resources.
func (dl *downloadTester) terminate() {
	dl.downloader.Terminate()
}

// sync starts synchronizing with a remote peer, blocking until it completes.
func (dl *downloadTester) sync(id string, td *big.Int, mode SyncMode) error {
	dl.lock.RLock()
	hash := dl.peerHashes[id][0]
	// If no particular TD was requested, load from the peer's blockchain
	if td == nil {
		td = big.NewInt(1)
		if diff, ok := dl.peerChainTds[id][hash]; ok {
			td = diff
		}
	}
	dl.lock.RUnlock()

	// Synchronise with the chosen peer and ensure proper cleanup afterwards
	err := dl.downloader.synchronise(id, hash, td, mode)
	select {
	case <-dl.downloader.cancelCh:
		// Ok, downloader fully cancelled after sync cycle
	default:
		// Downloader is still accepting packets, can block a peer up
		panic("downloader active post sync cycle") // panic will be caught by tester
	}
	return err
}

// HasHeader checks if a header is present in the testers canonical chain.
func (dl *downloadTester) HasHeader(hash common.Hash, number uint64) bool {
	return dl.GetHeaderByHash(hash) != nil
}

// HasBlock checks if a block is present in the testers canonical chain.
func (dl *downloadTester) HasBlock(hash common.Hash, number uint64) bool {
	return dl.GetBlockByHash(hash) != nil
}

// GetHeader retrieves a header from the testers canonical chain.
func (dl *downloadTester) GetHeaderByHash(hash common.Hash) *types.SnailHeader {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	return dl.ownHeaders[hash]
}

// GetBlock retrieves a block from the testers canonical chain.
func (dl *downloadTester) GetBlockByHash(hash common.Hash) *types.SnailBlock {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	return dl.ownBlocks[hash]
}

// CurrentHeader retrieves the current head header from the canonical chain.
func (dl *downloadTester) CurrentHeader() *types.SnailHeader {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	for i := len(dl.ownHashes) - 1; i >= 0; i-- {
		if header := dl.ownHeaders[dl.ownHashes[i]]; header != nil {
			return header
		}
	}
	return dl.genesis.Header()
}

// CurrentBlock retrieves the current head block from the canonical chain.
func (dl *downloadTester) CurrentBlock() *types.SnailBlock {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	for i := len(dl.ownHashes) - 1; i >= 0; i-- {
		if block := dl.ownBlocks[dl.ownHashes[i]]; block != nil {
			//if _, err := dl.stateDb.Get(block.Hash().Bytes()); err == nil {
			return block
			//}
		}
	}
	return dl.genesis
}

// CurrentFastBlock retrieves the current head fast-sync block from the canonical chain.
func (dl *downloadTester) CurrentFastBlock() *types.SnailBlock {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	for i := len(dl.ownHashes) - 1; i >= 0; i-- {
		if block := dl.ownBlocks[dl.ownHashes[i]]; block != nil {
			return block
		}
	}
	return dl.genesis
}

// FastSyncCommitHead manually sets the head block to a given hash.
func (dl *downloadTester) FastSyncCommitHead(hash common.Hash) error {
	// For now only check that the state trie is correct
	if block := dl.GetBlockByHash(hash); block != nil {
		_, err := trie.NewSecure(block.Hash(), trie.NewDatabase(dl.stateDb), 0)
		return err
	}
	return fmt.Errorf("non existent block: %x", hash[:4])
}

// GetTd retrieves the block's total difficulty from the canonical chain.
func (dl *downloadTester) GetTd(hash common.Hash, number uint64) *big.Int {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	return dl.ownChainTd[hash]
}

// InsertHeaderChain injects a new batch of headers into the simulated chain.
func (dl *downloadTester) InsertHeaderChain(headers []*types.SnailHeader, fruits [][]*types.SnailHeader, checkFreq int) (int, error) {
	dl.lock.Lock()
	defer dl.lock.Unlock()

	// Do a quick check, as the blockchain.InsertHeaderChain doesn't insert anything in case of errors
	if _, ok := dl.ownHeaders[headers[0].ParentHash]; !ok {
		return 0, errors.New("unknown parent")
	}
	for i := 1; i < len(headers); i++ {
		if headers[i].ParentHash != headers[i-1].Hash() {
			return i, errors.New("unknown parent")
		}
	}
	// Do a full insert if pre-checks passed
	for i, header := range headers {
		if _, ok := dl.ownHeaders[header.Hash()]; ok {
			continue
		}
		if _, ok := dl.ownHeaders[header.ParentHash]; !ok {
			return i, errors.New("unknown parent")
		}
		dl.ownHashes = append(dl.ownHashes, header.Hash())
		dl.ownHeaders[header.Hash()] = header
		dl.ownChainTd[header.Hash()] = new(big.Int).Add(dl.ownChainTd[header.ParentHash], header.Difficulty)
	}
	return len(headers), nil
}

// InsertChain injects a new batch of blocks into the simulated chain.
func (dl *downloadTester) FastInsertChain(blocks types.SnailBlocks) (int, error) {
	dl.lock.Lock()
	defer dl.lock.Unlock()
	for _, block := range blocks {

		if _, ok := dl.ownHeaders[block.Hash()]; !ok {
			dl.ownHashes = append(dl.ownHashes, block.Hash())
			dl.ownHeaders[block.Hash()] = block.Header()
		}
		dl.ownBlocks[block.Hash()] = block
		dl.ownChainTd[block.Hash()] = new(big.Int).Add(dl.ownChainTd[block.ParentHash()], block.Difficulty())
	}
	return len(blocks), nil
}

// InsertChain injects a new batch of blocks into the simulated chain.
func (dl *downloadTester) InsertChain(blocks types.SnailBlocks) (int, error) {
	dl.lock.Lock()
	defer dl.lock.Unlock()
	for _, block := range blocks {

		if _, ok := dl.ownHeaders[block.Hash()]; !ok {
			dl.ownHashes = append(dl.ownHashes, block.Hash())
			dl.ownHeaders[block.Hash()] = block.Header()
		}
		dl.ownBlocks[block.Hash()] = block
		dl.ownChainTd[block.Hash()] = new(big.Int).Add(dl.ownChainTd[block.ParentHash()], block.Difficulty())
	}
	return len(blocks), nil
}

// HasConfirmedBlock checks if a block is fully present in the database or not.and number must bigger than currentBlockNumber
func (bc *downloadTester) HasConfirmedBlock(hash common.Hash, number uint64) bool {
	return bc.HasBlock(hash, number)
}

// InsertReceiptChain injects a new batch of receipts into the simulated chain.
func (dl *downloadTester) InsertReceiptChain(blocks types.SnailBlocks, receipts []types.Receipts) (int, error) {
	dl.lock.Lock()
	return 0, nil
}

// Rollback removes some recently added elements from the chain.
func (dl *downloadTester) Rollback(hashes []common.Hash) {
	dl.lock.Lock()
	defer dl.lock.Unlock()

	for i := len(hashes) - 1; i >= 0; i-- {
		if dl.ownHashes[len(dl.ownHashes)-1] == hashes[i] {
			dl.ownHashes = dl.ownHashes[:len(dl.ownHashes)-1]
		}
		delete(dl.ownChainTd, hashes[i])
		delete(dl.ownHeaders, hashes[i])
		delete(dl.ownBlocks, hashes[i])
	}
}

// newPeer registers a new block download source into the downloader.
func (dl *downloadTester) newPeer(id string, version int, hashes []common.Hash, headers map[common.Hash]*types.SnailHeader, blocks map[common.Hash]*types.SnailBlock) error {
	return dl.newSlowPeer(id, version, hashes, headers, blocks, 0)
}

// newSlowPeer registers a new block download source into the downloader, with a
// specific delay time on processing the network packets sent to it, simulating
// potentially slow network IO.
func (dl *downloadTester) newSlowPeer(id string, version int, hashes []common.Hash, headers map[common.Hash]*types.SnailHeader, blocks map[common.Hash]*types.SnailBlock, delay time.Duration) error {
	dl.lock.Lock()
	defer dl.lock.Unlock()

	var err = dl.downloader.RegisterPeer(id, version, "ip", &downloadTesterPeer{dl: dl, id: id, delay: delay})
	if err == nil {
		// Assign the owned hashes, headers and blocks to the peer (deep copy)
		dl.peerHashes[id] = make([]common.Hash, len(hashes))
		copy(dl.peerHashes[id], hashes)

		dl.peerHeaders[id] = make(map[common.Hash]*types.SnailHeader)
		dl.peerBlocks[id] = make(map[common.Hash]*types.SnailBlock)
		dl.peerChainTds[id] = make(map[common.Hash]*big.Int)
		dl.peerMissingStates[id] = make(map[common.Hash]bool)

		genesis := hashes[len(hashes)-1]
		if header := headers[genesis]; header != nil {
			dl.peerHeaders[id][genesis] = header
			dl.peerChainTds[id][genesis] = header.Difficulty
		}
		if block := blocks[genesis]; block != nil {
			dl.peerBlocks[id][genesis] = block
			dl.peerChainTds[id][genesis] = block.Difficulty()
		}

		for i := len(hashes) - 2; i >= 0; i-- {
			hash := hashes[i]

			if header, ok := headers[hash]; ok {
				dl.peerHeaders[id][hash] = header
				if _, ok := dl.peerHeaders[id][header.ParentHash]; ok {
					dl.peerChainTds[id][hash] = new(big.Int).Add(header.Difficulty, dl.peerChainTds[id][header.ParentHash])
				}
			}
			if block, ok := blocks[hash]; ok {
				dl.peerBlocks[id][hash] = block
				if _, ok := dl.peerBlocks[id][block.ParentHash()]; ok {
					dl.peerChainTds[id][hash] = new(big.Int).Add(block.Difficulty(), dl.peerChainTds[id][block.ParentHash()])
				}
			}
		}
	}
	return err
}

// dropPeer simulates a hard peer removal from the connection pool.
func (dl *downloadTester) dropPeer(id string, call uint32) {
	dl.lock.Lock()
	defer dl.lock.Unlock()

	delete(dl.peerHashes, id)
	delete(dl.peerHeaders, id)
	delete(dl.peerBlocks, id)
	delete(dl.peerChainTds, id)

	dl.downloader.UnregisterPeer(id)
}

func (dl *downloadTester) GetFruitsHash(header *types.SnailHeader, fruits []*types.SnailBlock) common.Hash {

	if params.AllMinervaProtocolChanges.IsTIP5(header.Number) {
		var headers []*types.SnailHeader
		for i := 0; i < len(fruits); i++ {
			headers = append(headers, fruits[i].Header())
		}
		return types.DeriveSha(types.FruitsHeaders(headers))
	}
	return types.DeriveSha(types.Fruits(fruits))
}

type downloadTesterPeer struct {
	dl    *downloadTester
	id    string
	delay time.Duration
	lock  sync.RWMutex
}

// setDelay is a thread safe setter for the network delay value.
func (dlp *downloadTesterPeer) setDelay(delay time.Duration) {
	dlp.lock.Lock()
	defer dlp.lock.Unlock()

	dlp.delay = delay
}

// waitDelay is a thread safe way to sleep for the configured time.
func (dlp *downloadTesterPeer) waitDelay() {
	dlp.lock.RLock()
	delay := dlp.delay
	dlp.lock.RUnlock()

	time.Sleep(delay)
}

// Head constructs a function to retrieve a peer's current head hash
// and total difficulty.
func (dlp *downloadTesterPeer) Head() (common.Hash, *big.Int) {
	dlp.dl.lock.RLock()
	defer dlp.dl.lock.RUnlock()

	return dlp.dl.peerHashes[dlp.id][0], nil
}

// RequestHeadersByHash constructs a GetBlockHeaders function based on a hashed
// origin; associated with a particular peer in the download tester. The returned
// function can be used to retrieve batches of headers from the particular peer.
func (dlp *downloadTesterPeer) RequestHeadersByHash(origin common.Hash, amount int, skip int, reverse bool, isFastchain bool) error {
	// Find the canonical number of the hash
	dlp.dl.lock.RLock()
	number := uint64(0)
	for num, hash := range dlp.dl.peerHashes[dlp.id] {
		if hash == origin {
			number = uint64(len(dlp.dl.peerHashes[dlp.id]) - num - 1)
			break
		}
	}
	dlp.dl.lock.RUnlock()

	// Use the absolute header fetcher to satisfy the query
	return dlp.RequestHeadersByNumber(number, amount, skip, reverse, isFastchain)
}

// RequestHeadersByNumber constructs a GetBlockHeaders function based on a numbered
// origin; associated with a particular peer in the download tester. The returned
// function can be used to retrieve batches of headers from the particular peer.
func (dlp *downloadTesterPeer) RequestHeadersByNumber(origin uint64, amount int, skip int, reverse bool, isFastchain bool) error {
	dlp.waitDelay()

	dlp.dl.lock.RLock()
	defer dlp.dl.lock.RUnlock()

	// Gather the next batch of headers
	hashes := dlp.dl.peerHashes[dlp.id]
	headers := dlp.dl.peerHeaders[dlp.id]
	result := make([]*types.SnailHeader, 0, amount)
	for i := 0; i < amount && len(hashes)-int(origin)-1-i*(skip+1) >= 0; i++ {
		if header, ok := headers[hashes[len(hashes)-int(origin)-1-i*(skip+1)]]; ok {
			result = append(result, header)
		}
	}
	// Delay delivery a bit to allow attacks to unfold
	go func() {
		time.Sleep(time.Millisecond)
		dlp.dl.downloader.DeliverHeaders(dlp.id, result)
	}()
	return nil
}

// RequestBodies constructs a getBlockBodies method associated with a particular
// peer in the download tester. The returned function can be used to retrieve
// batches of block bodies from the particularly requested peer.
func (dlp *downloadTesterPeer) RequestBodies(hashes []common.Hash, isFastchain bool, call uint32) error {
	dlp.waitDelay()

	dlp.dl.lock.RLock()
	defer dlp.dl.lock.RUnlock()

	blocks := dlp.dl.peerBlocks[dlp.id]

	fruits := make([][]*types.SnailBlock, 0, len(hashes))

	for _, hash := range hashes {
		if block, ok := blocks[hash]; ok {
			fruits = append(fruits, block.Fruits())
		}
	}
	go dlp.dl.downloader.DeliverBodies(dlp.id, fruits)

	return nil
}

// RequestReceipts constructs a getReceipts method associated with a particular
// peer in the download tester. The returned function can be used to retrieve
// batches of block receipts from the particularly requested peer.
func (dlp *downloadTesterPeer) RequestReceipts(hashes []common.Hash, isFastchain bool) error {
	return nil
}

// RequestNodeData constructs a getNodeData method associated with a particular
// peer in the download tester. The returned function can be used to retrieve
// batches of node state data from the particularly requested peer.
func (dlp *downloadTesterPeer) RequestNodeData(hashes []common.Hash, isFastchain bool) error {
	dlp.waitDelay()

	dlp.dl.lock.RLock()
	defer dlp.dl.lock.RUnlock()

	results := make([][]byte, 0, len(hashes))
	for _, hash := range hashes {
		if data, err := dlp.dl.peerDb.Get(hash.Bytes()); err == nil {
			if !dlp.dl.peerMissingStates[dlp.id][hash] {
				results = append(results, data)
			}
		}
	}
	go dlp.dl.downloader.DeliverNodeData(dlp.id, results)
	return nil
}

// assertOwnChain checks if the local chain contains the correct number of items
// of the various chain components.
func assertOwnChain(t *testing.T, tester *downloadTester, length int) {
	assertOwnForkedChain(t, tester, 1, []int{length})
}

// assertOwnForkedChain checks if the local forked chain contains the correct
// number of items of the various chain components.
func assertOwnForkedChain(t *testing.T, tester *downloadTester, common int, lengths []int) {
	// Initialize the counters for the first fork
	headers, blocks, receipts := lengths[0], lengths[0], lengths[0]

	if receipts < 0 {
		receipts = 1
	}
	// Update the counters for each subsequent fork
	for _, length := range lengths[1:] {
		headers += length - common
		blocks += length - common
		receipts += length - common
	}
	switch tester.downloader.mode {
	case FullSync:
		receipts = 1
	case LightSync:
		blocks, receipts = 1, 1
	}
	if hs := len(tester.ownHeaders); hs != headers {
		t.Fatalf("synchronised headers mismatch: have %v, want %v", hs, headers)
	}
	if bs := len(tester.ownBlocks); bs != blocks {
		t.Fatalf("synchronised blocks mismatch: have %v, want %v", bs, blocks)
	}

}

// Tests that simple synchronization against a canonical chain works correctly.
// In this test common ancestor lookup should be short circuited and not require
// binary searching.
func TestCanonicalSynchronisation62(t *testing.T)     { testCanonicalSynchronisation(t, 62, FullSync) }
func TestCanonicalSynchronisation63Full(t *testing.T) { testCanonicalSynchronisation(t, 63, FullSync) }
func TestCanonicalSynchronisation63Fast(t *testing.T) { testCanonicalSynchronisation(t, 63, FastSync) }
func TestCanonicalSynchronisation64Full(t *testing.T) { testCanonicalSynchronisation(t, 64, FullSync) }
func TestCanonicalSynchronisation64Fast(t *testing.T) { testCanonicalSynchronisation(t, 64, FastSync) }

func testCanonicalSynchronisation(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()
	// Create a small enough block chain to download
	targetBlocks := blockCacheItems - 15

	parents1 := make([]*types.SnailBlock, 1)
	parents1[0] = tester.genesis

	fhashes, fheaders, fblocks, freceipt, fastChain, remoteHeader := tester.makeFastChain(targetBlocks)
	hashes, headers, blocks, _ := tester.makeChain(targetBlocks, 0, parents1, false, fastChain, 1)

	tester.fdownloader.SetHeader(remoteHeader)
	tester.downloader.SetHeader(remoteHeader)
	tester.fdownloader.SetSD(tester.downloader)

	tester.newPeer("peer", protocol, hashes, headers, blocks)
	tester.ftester.NewPeer("peer", protocol, fhashes, fheaders, fblocks, freceipt)

	// Synchronise with the peer and make sure all relevant data was retrieved
	if err := tester.sync("peer", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnChain(t, tester, targetBlocks+1)
}

// Tests that simple synchronization against a forked chain works correctly. In
// this test common ancestor lookup should *not* be short circuited, and a full
// binary search should be executed.

func TestForkedSync62(t *testing.T)     { testForkedSync(t, 62, FullSync) }
func TestForkedSync63Full(t *testing.T) { testForkedSync(t, 63, FullSync) }
func TestForkedSync63Fast(t *testing.T) { testForkedSync(t, 63, FastSync) }
func TestForkedSync64Full(t *testing.T) { testForkedSync(t, 64, FullSync) }
func TestForkedSync64Fast(t *testing.T) { testForkedSync(t, 64, FastSync) }

func testForkedSync(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()
	MaxHashFetch = 2

	// Create a long enough forked chain
	common, fork := MaxHashFetch, 2*MaxHashFetch
	hashesA, hashesB, headersA, headersB, blocksA, blocksB, fhashes, fheaders, fblocks, freceipt, remoteHeader := tester.makeChainFork(common+fork, fork, tester.genesis, true)

	tester.fdownloader.SetHeader(remoteHeader)
	tester.downloader.SetHeader(remoteHeader)
	tester.fdownloader.SetSD(tester.downloader)

	err := tester.newPeer("fork A", protocol, hashesA, headersA, blocksA)
	err = tester.ftester.NewPeer("fork A", protocol, fhashes, fheaders, fblocks, freceipt)

	err = tester.newPeer("fork B", protocol, hashesB, headersB, blocksB)
	err = tester.ftester.NewPeer("fork B", protocol, fhashes, fheaders, fblocks, freceipt)

	// Synchronise with the peer and make sure all blocks were retrieved
	if err = tester.sync("fork A", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}

	// Synchronise with the second peer and make sure that fork is pulled too
	if err = tester.sync("fork B", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
}

// Tests that synchronising against a much shorter but much heavyer fork works
// corrently and is not dropped.
func TestHeavyForkedSync62(t *testing.T)     { testHeavyForkedSync(t, 62, FullSync) }
func TestHeavyForkedSync63Full(t *testing.T) { testHeavyForkedSync(t, 63, FullSync) }
func TestHeavyForkedSync63Fast(t *testing.T) { testHeavyForkedSync(t, 63, FastSync) }
func TestHeavyForkedSync64Full(t *testing.T) { testHeavyForkedSync(t, 64, FullSync) }
func TestHeavyForkedSync64Fast(t *testing.T) { testHeavyForkedSync(t, 64, FastSync) }

func testHeavyForkedSync(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	// Create a long enough forked chain
	MaxHashFetch = 8

	// Create a long enough forked chain
	common, fork := MaxHashFetch, 4*MaxHashFetch
	hashesA, hashesB, headersA, headersB, blocksA, blocksB, fhashes, fheaders, fblocks, freceipt, remoteHeader := tester.makeChainFork(common+fork, fork, tester.genesis, true)

	tester.fdownloader.SetHeader(remoteHeader)
	tester.downloader.SetHeader(remoteHeader)
	tester.fdownloader.SetSD(tester.downloader)

	err := tester.newPeer("light", protocol, hashesA, headersA, blocksA)
	err = tester.ftester.NewPeer("light", protocol, fhashes, fheaders, fblocks, freceipt)

	err = tester.newPeer("heavy", protocol, hashesB[fork/2:], headersB, blocksB)
	err = tester.ftester.NewPeer("heavy", protocol, fhashes, fheaders, fblocks, freceipt)

	// Synchronise with the peer and make sure all blocks were retrieved
	if err = tester.sync("light", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnChain(t, tester, common+fork+1)

	// Synchronise with the second peer and make sure that fork is pulled too
	if err = tester.sync("heavy", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnForkedChain(t, tester, common+1, []int{common + fork + 1, common + fork/2 + 1})
}

// Tests that chain forks are contained within a certain interval of the current
// chain head, ensuring that malicious peers cannot waste resources by feeding
// long dead chains.
//func TestBoundedForkedSync62(t *testing.T)      { testBoundedForkedSync(t, 62, FullSync) }
//func TestBoundedForkedSync63Full(t *testing.T)  { testBoundedForkedSync(t, 63, FullSync) }
//func TestBoundedForkedSync63Fast(t *testing.T)  { testBoundedForkedSync(t, 63, FastSync) }
//func TestBoundedForkedSync64Full(t *testing.T)  { testBoundedForkedSync(t, 64, FullSync) }
//func TestBoundedForkedSync64Fast(t *testing.T)  { testBoundedForkedSync(t, 64, FastSync) }
//func TestBoundedForkedSync64Light(t *testing.T) { testBoundedForkedSync(t, 64, LightSync) }
//
//func testBoundedForkedSync(t *testing.T, protocol int, mode SyncMode) {
//	t.Parallel()
//
//	tester := newTester()
//	defer tester.terminate()
//
//	// Create a long enough forked chain
//	common, fork := 13, int(MaxForkAncestry+17)
//	hashesA, hashesB, headersA, headersB, blocksA, blocksB, fhashes, fheaders, fblocks, freceipt,remoteHeader := tester.makeChainFork(common+fork, fork, tester.genesis, true)
//
//	tester.fdownloader.SetHeader(remoteHeader)
//	tester.downloader.SetHeader(remoteHeader)
//	tester.fdownloader.SetSD(tester.downloader)
//
//
//	err := tester.newPeer("original", protocol, hashesA, headersA, blocksA)
//	err = tester.ftester.NewPeer("original", protocol, fhashes, fheaders, fblocks, freceipt)
//
//	err = tester.newPeer("rewriter", protocol, hashesB, headersB, blocksB)
//	err = tester.ftester.NewPeer("rewriter", protocol, fhashes, fheaders, fblocks, freceipt)
//
//	// Synchronise with the peer and make sure all blocks were retrieved
//	if err = tester.sync("original", nil, mode); err != nil {
//		t.Fatalf("failed to synchronise blocks: %v", err)
//	}
//	assertOwnChain(t, tester, common+fork+1)
//
//	// Synchronise with the second peer and ensure that the fork is rejected to being too old
//	if err := tester.sync("rewriter", nil, mode); err != errInvalidAncestor {
//		t.Fatalf("sync failure mismatch: have %v, want %v", err, errInvalidAncestor)
//	}
//}
//
//// Tests that chain forks are contained within a certain interval of the current
//// chain head for short but heavy forks too. These are a bit special because they
//// take different ancestor lookup paths.
//func TestBoundedHeavyForkedSync62(t *testing.T)      { testBoundedHeavyForkedSync(t, 62, FullSync) }
//func TestBoundedHeavyForkedSync63Full(t *testing.T)  { testBoundedHeavyForkedSync(t, 63, FullSync) }
//func TestBoundedHeavyForkedSync63Fast(t *testing.T)  { testBoundedHeavyForkedSync(t, 63, FastSync) }
//func TestBoundedHeavyForkedSync64Full(t *testing.T)  { testBoundedHeavyForkedSync(t, 64, FullSync) }
//func TestBoundedHeavyForkedSync64Fast(t *testing.T)  { testBoundedHeavyForkedSync(t, 64, FastSync) }
//func TestBoundedHeavyForkedSync64Light(t *testing.T) { testBoundedHeavyForkedSync(t, 64, LightSync) }
//
//func testBoundedHeavyForkedSync(t *testing.T, protocol int, mode SyncMode) {
//	t.Parallel()
//
//	tester := newTester()
//	defer tester.terminate()
//	// Create a long enough forked chain
//	common, fork := 13, int(MaxForkAncestry+17)
//	hashesA, hashesB, headersA, headersB, blocksA, blocksB, fhashes, fheaders, fblocks, freceipt,remoteHeader := tester.makeChainFork(common+fork, fork, tester.genesis, true)
//
//	tester.fdownloader.SetHeader(remoteHeader)
//	tester.downloader.SetHeader(remoteHeader)
//	tester.fdownloader.SetSD(tester.downloader)
//
//
//	err := tester.newPeer("original", protocol, hashesA, headersA, blocksA)
//	err = tester.ftester.NewPeer("original", protocol, fhashes, fheaders, fblocks, freceipt)
//
//	err = tester.newPeer("heavy-rewriter", protocol, hashesB, headersB, blocksB)
//	err = tester.ftester.NewPeer("heavy-rewriter", protocol, fhashes, fheaders, fblocks, freceipt)
//
//	// Synchronise with the peer and make sure all blocks were retrieved
//	if err = tester.sync("original", nil, mode); err != nil {
//		t.Fatalf("failed to synchronise blocks: %v", err)
//	}
//	assertOwnChain(t, tester, common+fork+1)
//
//	// Synchronise with the second peer and ensure that the fork is rejected to being too old
//	if err := tester.sync("heavy-rewriter", nil, mode); err != errInvalidAncestor {
//		t.Fatalf("sync failure mismatch: have %v, want %v", err, errInvalidAncestor)
//	}
//}

// Tests that an inactive downloader will not accept incoming block headers and
// bodies.
func TestInactiveDownloader62(t *testing.T) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	// Check that neither block headers nor bodies are accepted
	if err := tester.downloader.DeliverHeaders("bad peer", []*types.SnailHeader{}); err != errNoSyncActive {
		t.Errorf("error mismatch: have %v, want %v", err, errNoSyncActive)
	}
	if err := tester.downloader.DeliverBodies("bad peer", [][]*types.SnailBlock{}); err != errNoSyncActive {
		t.Errorf("error mismatch: have %v, want %v", err, errNoSyncActive)
	}
}

// Tests that an inactive downloader will not accept incoming block headers,
// bodies and receipts.
func TestInactiveDownloader63(t *testing.T) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	// Check that neither block headers nor bodies are accepted
	if err := tester.downloader.DeliverHeaders("bad peer", []*types.SnailHeader{}); err != errNoSyncActive {
		t.Errorf("error mismatch: have %v, want %v", err, errNoSyncActive)
	}
	if err := tester.downloader.DeliverBodies("bad peer", [][]*types.SnailBlock{}); err != errNoSyncActive {
		t.Errorf("error mismatch: have %v, want %v", err, errNoSyncActive)
	}
}

// Tests that a canceled download wipes all previously accumulated state.
func TestCancel62(t *testing.T)     { testCancel(t, 62, FullSync) }
func TestCancel63Full(t *testing.T) { testCancel(t, 63, FullSync) }
func TestCancel63Fast(t *testing.T) { testCancel(t, 63, FastSync) }
func TestCancel64Full(t *testing.T) { testCancel(t, 64, FullSync) }
func TestCancel64Fast(t *testing.T) { testCancel(t, 64, FastSync) }

func testCancel(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	// Create a small enough block chain to download and the tester
	targetBlocks := blockCacheItems - 15

	parents1 := make([]*types.SnailBlock, 1)
	parents1[0] = tester.genesis

	fhashes, fheaders, fblocks, freceipt, fastChain, remoteHeader := tester.makeFastChain(targetBlocks)
	hashes, headers, blocks, _ := tester.makeChain(targetBlocks, 0, parents1, false, fastChain, 1)

	tester.fdownloader.SetHeader(remoteHeader)
	tester.downloader.SetHeader(remoteHeader)
	tester.fdownloader.SetSD(tester.downloader)

	tester.newPeer("peer", protocol, hashes, headers, blocks)
	tester.ftester.NewPeer("peer", protocol, fhashes, fheaders, fblocks, freceipt)
	// Make sure canceling works with a pristine downloader
	tester.downloader.Cancel()
	if !tester.downloader.queue.Idle() {
		t.Errorf("download queue not idle")
	}
	// Synchronise with the peer, but cancel afterwards
	if err := tester.sync("peer", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	tester.downloader.Cancel()
	if !tester.downloader.queue.Idle() {
		t.Errorf("download queue not idle")
	}
}

// Tests that synchronisation from multiple peers works as intended (multi thread sanity test).
func TestMultiSynchronisation62(t *testing.T)     { testMultiSynchronisation(t, 62, FullSync) }
func TestMultiSynchronisation63Full(t *testing.T) { testMultiSynchronisation(t, 63, FullSync) }
func TestMultiSynchronisation63Fast(t *testing.T) { testMultiSynchronisation(t, 63, FastSync) }
func TestMultiSynchronisation64Full(t *testing.T) { testMultiSynchronisation(t, 64, FullSync) }
func TestMultiSynchronisation64Fast(t *testing.T) { testMultiSynchronisation(t, 64, FastSync) }

func testMultiSynchronisation(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	// Create various peers with various parts of the chain
	targetPeers := 8
	targetBlocks := targetPeers*blockCacheItems - 15

	parents1 := make([]*types.SnailBlock, 1)
	parents1[0] = tester.genesis

	fhashes, fheaders, fblocks, freceipt, fastChain, remoteHeader := tester.makeFastChain(targetBlocks)
	hashes, headers, blocks, _ := tester.makeChain(targetBlocks, 0, parents1, false, fastChain, 1)
	tester.fdownloader.SetHeader(remoteHeader)
	tester.downloader.SetHeader(remoteHeader)
	tester.fdownloader.SetSD(tester.downloader)

	for i := 0; i < targetPeers; i++ {
		id := fmt.Sprintf("peer #%d", i)
		tester.newPeer(id, protocol, hashes[i*blockCacheItems:], headers, blocks)
		tester.ftester.NewPeer(id, protocol, fhashes, fheaders, fblocks, freceipt)
	}
	if err := tester.sync("peer #0", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnChain(t, tester, targetBlocks+1)
}

// Tests that synchronisations behave well in multi-version protocol environments
// and not wreak havoc on other nodes in the network.
func TestMultiProtoSynchronisation62(t *testing.T)     { testMultiProtoSync(t, 62, FullSync) }
func TestMultiProtoSynchronisation63Full(t *testing.T) { testMultiProtoSync(t, 63, FullSync) }
func TestMultiProtoSynchronisation63Fast(t *testing.T) { testMultiProtoSync(t, 63, FastSync) }
func TestMultiProtoSynchronisation64Full(t *testing.T) { testMultiProtoSync(t, 64, FullSync) }
func TestMultiProtoSynchronisation64Fast(t *testing.T) { testMultiProtoSync(t, 64, FastSync) }

func testMultiProtoSync(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	// Create a small enough block chain to download
	targetBlocks := blockCacheItems - 15
	parents1 := make([]*types.SnailBlock, 1)
	parents1[0] = tester.genesis

	fhashes, fheaders, fblocks, freceipt, fastChain, remoteHeader := tester.makeFastChain(targetBlocks)
	hashes, headers, blocks, _ := tester.makeChain(targetBlocks, 0, parents1, false, fastChain, 1)
	tester.fdownloader.SetHeader(remoteHeader)
	tester.downloader.SetHeader(remoteHeader)
	tester.fdownloader.SetSD(tester.downloader)

	// Create peers of every type
	tester.newPeer("peer 62", 62, hashes, headers, blocks)
	tester.ftester.NewPeer("peer 62", 62, fhashes, fheaders, fblocks, freceipt)

	tester.newPeer("peer 63", 63, hashes, headers, blocks)
	tester.ftester.NewPeer("peer 63", 63, fhashes, fheaders, fblocks, freceipt)

	tester.newPeer("peer 64", 64, hashes, headers, blocks)
	tester.ftester.NewPeer("peer 64", 64, fhashes, fheaders, fblocks, freceipt)

	// Synchronise with the requested peer and make sure all blocks were retrieved
	if err := tester.sync(fmt.Sprintf("peer %d", protocol), nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnChain(t, tester, targetBlocks+1)

	// Check that no peers have been dropped off
	for _, version := range []int{62, 63, 64} {
		peer := fmt.Sprintf("peer %d", version)
		if _, ok := tester.peerHashes[peer]; !ok {
			t.Errorf("%s dropped", peer)
		}
	}
}

// Tests that if a block is empty (e.g. header only), no body request should be
// made, and instead the header should be assembled into a whole block in itself.
func TestEmptyShortCircuit62(t *testing.T)     { testEmptyShortCircuit(t, 62, FullSync) }
func TestEmptyShortCircuit63Full(t *testing.T) { testEmptyShortCircuit(t, 63, FullSync) }
func TestEmptyShortCircuit63Fast(t *testing.T) { testEmptyShortCircuit(t, 63, FastSync) }
func TestEmptyShortCircuit64Full(t *testing.T) { testEmptyShortCircuit(t, 64, FullSync) }
func TestEmptyShortCircuit64Fast(t *testing.T) { testEmptyShortCircuit(t, 64, FastSync) }

func testEmptyShortCircuit(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	// Create a block chain to download
	targetBlocks := 2*blockCacheItems - 15

	parents1 := make([]*types.SnailBlock, 1)
	parents1[0] = tester.genesis

	fhashes, fheaders, fblocks, freceipt, fastChain, remoteHeader := tester.makeFastChain(targetBlocks)
	hashes, headers, blocks, _ := tester.makeChain(targetBlocks, 0, parents1, false, fastChain, 1)
	tester.fdownloader.SetHeader(remoteHeader)
	tester.downloader.SetHeader(remoteHeader)
	tester.fdownloader.SetSD(tester.downloader)

	tester.newPeer("peer", protocol, hashes, headers, blocks)
	tester.ftester.NewPeer("peer", protocol, fhashes, fheaders, fblocks, freceipt)

	// Instrument the downloader to signal body requests
	bodiesHave := int32(0)
	tester.downloader.bodyFetchHook = func(headers []*types.SnailHeader) {
		atomic.AddInt32(&bodiesHave, int32(len(headers)))
	}
	// Synchronise with the peer and make sure all blocks were retrieved
	if err := tester.sync("peer", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnChain(t, tester, targetBlocks+1)

	// Validate the number of block bodies that should have been requested
	bodiesNeeded := 0
	for _, block := range blocks {
		if mode != LightSync && block != tester.genesis && (len(block.Fruits()) > 0) {
			bodiesNeeded++
		}
	}

	if int(bodiesHave) != bodiesNeeded {
		t.Errorf("body retrieval count mismatch: have %v, want %v", bodiesHave, bodiesNeeded)
	}
}

// Tests that headers are enqueued continuously, preventing malicious nodes from
// stalling the downloader by feeding gapped header chains.
func TestMissingHeaderAttack62(t *testing.T)     { testMissingHeaderAttack(t, 62, FullSync) }
func TestMissingHeaderAttack63Full(t *testing.T) { testMissingHeaderAttack(t, 63, FullSync) }
func TestMissingHeaderAttack63Fast(t *testing.T) { testMissingHeaderAttack(t, 63, FastSync) }
func TestMissingHeaderAttack64Full(t *testing.T) { testMissingHeaderAttack(t, 64, FullSync) }
func TestMissingHeaderAttack64Fast(t *testing.T) { testMissingHeaderAttack(t, 64, FastSync) }

func testMissingHeaderAttack(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	// Create a small enough block chain to download
	targetBlocks := blockCacheItems - 15
	parents1 := make([]*types.SnailBlock, 1)
	parents1[0] = tester.genesis

	fhashes, fheaders, fblocks, freceipt, fastChain, remoteHeader := tester.makeFastChain(targetBlocks)
	hashes, headers, blocks, _ := tester.makeChain(targetBlocks, 0, parents1, false, fastChain, 1)
	tester.fdownloader.SetHeader(remoteHeader)
	tester.downloader.SetHeader(remoteHeader)
	tester.fdownloader.SetSD(tester.downloader)

	// Attempt a full sync with an attacker feeding gapped headers
	tester.newPeer("attack", protocol, hashes, headers, blocks)
	tester.ftester.NewPeer("attack", protocol, fhashes, fheaders, fblocks, freceipt)

	missing := targetBlocks / 2
	delete(tester.peerHeaders["attack"], hashes[missing])

	if err := tester.sync("attack", nil, mode); err == nil {
		t.Fatalf("succeeded attacker synchronisation")
	}
	// Synchronise with the valid peer and make sure sync succeeds
	tester.newPeer("valid", protocol, hashes, headers, blocks)
	tester.ftester.NewPeer("valid", protocol, fhashes, fheaders, fblocks, freceipt)
	if err := tester.sync("valid", nil, mode); err != nil {
		t.Fatalf("failed to synchronise blocks: %v", err)
	}
	assertOwnChain(t, tester, targetBlocks+1)
}

// Tests that if requested headers are shifted (i.e. first is missing), the queue
// detects the invalid numbering.
func TestShiftedHeaderAttack62(t *testing.T)     { testShiftedHeaderAttack(t, 62, FullSync) }
func TestShiftedHeaderAttack63Full(t *testing.T) { testShiftedHeaderAttack(t, 63, FullSync) }
func TestShiftedHeaderAttack63Fast(t *testing.T) { testShiftedHeaderAttack(t, 63, FastSync) }
func TestShiftedHeaderAttack64Full(t *testing.T) { testShiftedHeaderAttack(t, 64, FullSync) }
func TestShiftedHeaderAttack64Fast(t *testing.T) { testShiftedHeaderAttack(t, 64, FastSync) }

func testShiftedHeaderAttack(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	// Create a small enough block chain to download
	targetBlocks := blockCacheItems - 15
	parents1 := make([]*types.SnailBlock, 1)
	parents1[0] = tester.genesis

	fhashes, fheaders, fblocks, freceipt, fastChain, remoteHeader := tester.makeFastChain(targetBlocks)
	hashes, headers, blocks, _ := tester.makeChain(targetBlocks, 0, parents1, false, fastChain, 1)
	tester.fdownloader.SetHeader(remoteHeader)
	tester.downloader.SetHeader(remoteHeader)
	tester.fdownloader.SetSD(tester.downloader)

	// Attempt a full sync with an attacker feeding shifted headers
	tester.newPeer("attack", protocol, hashes, headers, blocks)
	tester.ftester.NewPeer("attack", protocol, fhashes, fheaders, fblocks, freceipt)

	delete(tester.peerHeaders["attack"], hashes[len(hashes)-2])
	delete(tester.peerBlocks["attack"], hashes[len(hashes)-2])

	if err := tester.sync("attack", nil, mode); err == nil {
		t.Fatalf("succeeded attacker synchronisation")
	}
}

// Tests that a peer advertising an high TD doesn't get to stall the downloader
// afterwards by not sending any useful hashes.
func TestHighTDStarvationAttack62(t *testing.T)     { testHighTDStarvationAttack(t, 62, FullSync) }
func TestHighTDStarvationAttack63Full(t *testing.T) { testHighTDStarvationAttack(t, 63, FullSync) }
func TestHighTDStarvationAttack63Fast(t *testing.T) { testHighTDStarvationAttack(t, 63, FastSync) }
func TestHighTDStarvationAttack64Full(t *testing.T) { testHighTDStarvationAttack(t, 64, FullSync) }
func TestHighTDStarvationAttack64Fast(t *testing.T) { testHighTDStarvationAttack(t, 64, FastSync) }

func testHighTDStarvationAttack(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	parents1 := make([]*types.SnailBlock, 1)
	parents1[0] = tester.genesis

	fhashes, fheaders, fblocks, freceipt, fastChain, remoteHeader := tester.makeFastChain(0)
	hashes, headers, blocks, _ := tester.makeChain(0, 0, parents1, false, fastChain, 1)
	tester.fdownloader.SetHeader(remoteHeader)
	tester.downloader.SetHeader(remoteHeader)
	tester.fdownloader.SetSD(tester.downloader)

	tester.newPeer("attack", protocol, hashes, headers, blocks)
	tester.ftester.NewPeer("attack", protocol, fhashes, fheaders, fblocks, freceipt)

	if err := tester.sync("attack", big.NewInt(10000000000000), mode); err != errStallingPeer {
		t.Fatalf("synchronisation error mismatch: have %v, want %v", err, errStallingPeer)
	}
}

// Tests that misbehaving peers are disconnected, whilst behaving ones are not.
func TestBlockHeaderAttackerDropping62(t *testing.T) { testBlockHeaderAttackerDropping(t, 62) }
func TestBlockHeaderAttackerDropping63(t *testing.T) { testBlockHeaderAttackerDropping(t, 63) }
func TestBlockHeaderAttackerDropping64(t *testing.T) { testBlockHeaderAttackerDropping(t, 64) }

func testBlockHeaderAttackerDropping(t *testing.T, protocol int) {
	t.Parallel()

	// Define the disconnection requirement for individual hash fetch errors
	tests := []struct {
		result error
		drop   bool
	}{
		{nil, false},                        // Sync succeeded, all is well
		{errBusy, false},                    // Sync is already in progress, no problem
		{errUnknownPeer, false},             // Peer is unknown, was already dropped, don't double drop
		{errBadPeer, true},                  // Peer was deemed bad for some reason, drop it
		{errStallingPeer, true},             // Peer was detected to be stalling, drop it
		{errNoPeers, false},                 // No peers to download from, soft race, no issue
		{errTimeout, true},                  // No hashes received in due time, drop the peer
		{errEmptyHeaderSet, true},           // No headers were returned as a response, drop as it's a dead end
		{errPeersUnavailable, true},         // Nobody had the advertised blocks, drop the advertiser
		{errInvalidAncestor, true},          // Agreed upon ancestor is not acceptable, drop the chain rewriter
		{errInvalidChain, true},             // Hash chain was detected as invalid, definitely drop
		{errInvalidBlock, false},            // A bad peer was detected, but not the sync origin
		{errInvalidBody, false},             // A bad peer was detected, but not the sync origin
		{errInvalidReceipt, false},          // A bad peer was detected, but not the sync origin
		{errCancelBlockFetch, false},        // Synchronisation was canceled, origin may be innocent, don't drop
		{errCancelHeaderFetch, false},       // Synchronisation was canceled, origin may be innocent, don't drop
		{errCancelBodyFetch, false},         // Synchronisation was canceled, origin may be innocent, don't drop
		{errCancelReceiptFetch, false},      // Synchronisation was canceled, origin may be innocent, don't drop
		{errCancelHeaderProcessing, false},  // Synchronisation was canceled, origin may be innocent, don't drop
		{errCancelContentProcessing, false}, // Synchronisation was canceled, origin may be innocent, don't drop
	}
	// Run the tests and check disconnection status
	tester := newTester()
	defer tester.terminate()

	for i, tt := range tests {
		// Register a new peer and ensure it's presence
		id := fmt.Sprintf("test %d", i)
		if err := tester.newPeer(id, protocol, []common.Hash{tester.genesis.Hash()}, nil, nil); err != nil {
			t.Fatalf("test %d: failed to register new peer: %v", i, err)
		}
		if _, ok := tester.peerHashes[id]; !ok {
			t.Fatalf("test %d: registered peer not found", i)
		}
		// Simulate a synchronisation and check the required result
		tester.downloader.synchroniseMock = func(string, common.Hash) error { return tt.result }

		tester.downloader.Synchronise(id, tester.genesis.Hash(), big.NewInt(1000), FullSync)
		if _, ok := tester.peerHashes[id]; !ok != tt.drop {
			t.Errorf("test %d: peer drop mismatch for %v: have %v, want %v", i, tt.result, !ok, tt.drop)
		}
	}
}

// Tests that synchronisation progress (origin block number, current block number
// and highest block number) is tracked and updated correctly.
func TestSyncProgress62(t *testing.T)     { testSyncProgress(t, 62, FullSync) }
func TestSyncProgress63Full(t *testing.T) { testSyncProgress(t, 63, FullSync) }
func TestSyncProgress63Fast(t *testing.T) { testSyncProgress(t, 63, FastSync) }
func TestSyncProgress64Full(t *testing.T) { testSyncProgress(t, 64, FullSync) }
func TestSyncProgress64Fast(t *testing.T) { testSyncProgress(t, 64, FastSync) }

func testSyncProgress(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	tester := newTester()
	defer tester.terminate()

	// Create a small enough block chain to download
	targetBlocks := blockCacheItems - 15
	parents1 := make([]*types.SnailBlock, 1)
	parents1[0] = tester.genesis

	fhashes, fheaders, fblocks, freceipt, fastChain, remoteHeader := tester.makeFastChain(targetBlocks)
	hashes, headers, blocks, _ := tester.makeChain(targetBlocks, 0, parents1, false, fastChain, 1)
	tester.fdownloader.SetHeader(remoteHeader)
	tester.downloader.SetHeader(remoteHeader)
	tester.fdownloader.SetSD(tester.downloader)

	// Set a sync init hook to catch progress changes
	starting := make(chan struct{})
	progress := make(chan struct{})

	tester.downloader.syncInitHook = func(origin, latest uint64) {
		starting <- struct{}{}
		<-progress
	}
	// Retrieve the sync progress and ensure they are zero (pristine sync)
	if progress := tester.downloader.Progress(); progress.StartingSnailBlock != 0 || progress.CurrentSnailBlock != 0 || progress.HighestSnailBlock != 0 {
		t.Fatalf("Pristine progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingSnailBlock, progress.CurrentSnailBlock, progress.HighestSnailBlock, 0, 0, 0)
	}

	// Retrieve the sync progress and ensure they are zero (pristine sync)
	if progress := tester.fdownloader.Progress(); progress.StartingFastBlock != 0 || progress.CurrentFastBlock != 0 || progress.HighestFastBlock != 0 {
		t.Fatalf("Pristine progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingFastBlock, progress.CurrentFastBlock, progress.HighestFastBlock, 0, 0, 0)
	}

	// Synchronise half the blocks and check initial progress
	tester.newPeer("peer-half", protocol, hashes[targetBlocks/2:], headers, blocks)
	tester.ftester.NewPeer("peer-half", protocol, fhashes, fheaders, fblocks, freceipt)
	pending := new(sync.WaitGroup)
	pending.Add(1)

	go func() {
		defer pending.Done()
		if err := tester.sync("peer-half", nil, mode); err != nil {
			panic(fmt.Sprintf("failed to synchronise blocks: %v", err))
		}
	}()
	<-starting
	if progress := tester.downloader.Progress(); progress.StartingSnailBlock != 0 || progress.CurrentSnailBlock != 0 || progress.HighestSnailBlock != uint64(targetBlocks/2) {
		t.Fatalf("Initial progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingSnailBlock, progress.CurrentSnailBlock, progress.HighestSnailBlock, 0, 0, targetBlocks/2)
	}
	// Retrieve the sync progress and ensure they are zero (pristine sync)
	if progress := tester.fdownloader.Progress(); progress.StartingFastBlock != 0 || progress.CurrentFastBlock != 0 || progress.HighestFastBlock != 0 {
		t.Fatalf("Pristine progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingFastBlock, progress.CurrentFastBlock, progress.HighestFastBlock, 0, 0, len(fhashes))
	}
	progress <- struct{}{}
	pending.Wait()

	// Synchronise all the blocks and check continuation progress
	tester.newPeer("peer-full", protocol, hashes, headers, blocks)
	tester.ftester.NewPeer("peer-full", protocol, fhashes, fheaders, fblocks, freceipt)
	pending.Add(1)

	go func() {
		defer pending.Done()
		if err := tester.sync("peer-full", nil, mode); err != nil {
			panic(fmt.Sprintf("failed to synchronise blocks: %v", err))
		}
	}()
	<-starting
	if progress := tester.downloader.Progress(); progress.StartingSnailBlock != uint64(targetBlocks/2) || progress.CurrentSnailBlock != uint64(targetBlocks/2) || progress.HighestSnailBlock != uint64(targetBlocks) {
		t.Fatalf("Completing progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingSnailBlock, progress.CurrentSnailBlock, progress.HighestSnailBlock, targetBlocks/2, targetBlocks/2+1, targetBlocks)
	}

	progress <- struct{}{}
	pending.Wait()

	// Check final progress after successful sync
	if progress := tester.downloader.Progress(); progress.StartingSnailBlock != uint64(targetBlocks/2) || progress.CurrentSnailBlock != uint64(targetBlocks) || progress.HighestSnailBlock != uint64(targetBlocks) {
		t.Fatalf("Final progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingSnailBlock, progress.CurrentSnailBlock, progress.HighestSnailBlock, targetBlocks/2, targetBlocks, targetBlocks)
	}

}

// Tests that synchronisation progress (origin block number and highest block
// number) is tracked and updated correctly in case of a fork (or manual head
// revertal).
//func TestForkedSyncProgress62(t *testing.T)      { testForkedSyncProgress(t, 62, FullSync) }
//func TestForkedSyncProgress63Full(t *testing.T)  { testForkedSyncProgress(t, 63, FullSync) }
//func TestForkedSyncProgress63Fast(t *testing.T)  { testForkedSyncProgress(t, 63, FastSync) }
//func TestForkedSyncProgress64Full(t *testing.T)  { testForkedSyncProgress(t, 64, FullSync) }
//func TestForkedSyncProgress64Fast(t *testing.T)  { testForkedSyncProgress(t, 64, FastSync) }
//
//func testForkedSyncProgress(t *testing.T, protocol int, mode SyncMode) {
//	t.Parallel()
//
//	tester := newTester()
//	defer tester.terminate()
//	defer tester.ftester.Terminate()
//
//	// Create a forked chain to simulate origin revertal
//	common, fork := MaxHashFetch, 2*MaxHashFetch
//	hashesA, hashesB, headersA, headersB, blocksA, blocksB, fhashes, fheaders, fblocks, freceipt,remoteHeader := tester.makeChainFork(common+fork, fork, tester.genesis, true)
//
//	tester.fdownloader.SetHeader(remoteHeader)
//	tester.downloader.SetHeader(remoteHeader)
//	tester.fdownloader.SetSD(tester.downloader)
//
//	// Set a sync init hook to catch progress changes
//	starting := make(chan struct{})
//	progress := make(chan struct{})
//
//	tester.downloader.syncInitHook = func(origin, latest uint64) {
//		starting <- struct{}{}
//		<-progress
//	}
//	// Retrieve the sync progress and ensure they are zero (pristine sync)
//	if progress := tester.downloader.Progress(); progress.StartingSnailBlock != 0 || progress.CurrentSnailBlock != 0 || progress.HighestSnailBlock != 0 {
//		t.Fatalf("Pristine progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingSnailBlock, progress.CurrentSnailBlock, progress.HighestSnailBlock, 0, 0, 0)
//	}
//	// Synchronise with one of the forks and check progress
//	tester.newPeer("fork A", protocol, hashesA, headersA, blocksA)
//	tester.ftester.NewPeer("fork A", protocol, fhashes, fheaders, fblocks, freceipt)
//	pending := new(sync.WaitGroup)
//	pending.Add(1)
//
//	go func() {
//		defer pending.Done()
//		if err := tester.sync("fork A", nil, mode); err != nil {
//			panic(fmt.Sprintf("failed to synchronise blocks: %v", err))
//		}
//	}()
//	<-starting
//	if progress := tester.downloader.Progress(); progress.StartingSnailBlock != 0 || progress.CurrentSnailBlock != 0 || progress.HighestSnailBlock != uint64(len(hashesA)-1) {
//		t.Fatalf("Initial progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingSnailBlock, progress.CurrentSnailBlock, progress.HighestSnailBlock, 0, 0, len(hashesA)-1)
//	}
//	progress <- struct{}{}
//	pending.Wait()
//
//	// Simulate a successful sync above the fork
//	tester.downloader.syncStatsChainOrigin = uint64(common)
//
//	// Synchronise with the second fork and check progress resets
//	tester.newPeer("fork B", protocol, hashesB, headersB, blocksB)
//	tester.ftester.NewPeer("fork B", protocol, fhashes, fheaders, fblocks, freceipt)
//	pending.Add(1)
//
//	go func() {
//		defer pending.Done()
//		if err := tester.sync("fork B", nil, mode); err != nil {
//			panic(fmt.Sprintf("failed to synchronise blocks: %v", err))
//		}
//	}()
//	<-starting
//	if progress := tester.downloader.Progress(); progress.StartingSnailBlock != uint64(common) || progress.CurrentSnailBlock != uint64(len(hashesA)-1) || progress.HighestSnailBlock != uint64(len(hashesB)-1) {
//		t.Fatalf("Forking progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingSnailBlock, progress.CurrentSnailBlock, progress.HighestSnailBlock, common, len(hashesA)-1, len(hashesB)-1)
//	}
//	progress <- struct{}{}
//	pending.Wait()
//
//	// Check final progress after successful sync
//	if progress := tester.downloader.Progress(); progress.StartingSnailBlock != uint64(common) || progress.CurrentSnailBlock != uint64(len(hashesB)-1) || progress.HighestSnailBlock != uint64(len(hashesB)-1) {
//		t.Fatalf("Final progress mismatch: have %v/%v/%v, want %v/%v/%v", progress.StartingSnailBlock, progress.CurrentSnailBlock, progress.HighestSnailBlock, common, len(hashesB)-1, len(hashesB)-1)
//	}
//}

func TestDeliverHeadersHang62(t *testing.T)     { testDeliverHeadersHang(t, 62, FullSync) }
func TestDeliverHeadersHang63Full(t *testing.T) { testDeliverHeadersHang(t, 63, FullSync) }
func TestDeliverHeadersHang63Fast(t *testing.T) { testDeliverHeadersHang(t, 63, FastSync) }
func TestDeliverHeadersHang64Full(t *testing.T) { testDeliverHeadersHang(t, 64, FullSync) }
func TestDeliverHeadersHang64Fast(t *testing.T) { testDeliverHeadersHang(t, 64, FastSync) }

type floodingTestPeer struct {
	peer   dtypes.Peer
	tester *downloadTester
	pend   sync.WaitGroup
}

func (ftp *floodingTestPeer) Head() (common.Hash, *big.Int) { return ftp.peer.Head() }
func (ftp *floodingTestPeer) RequestHeadersByHash(hash common.Hash, count int, skip int, reverse bool, isFastchain bool) error {
	return ftp.peer.RequestHeadersByHash(hash, count, skip, reverse, isFastchain)
}
func (ftp *floodingTestPeer) RequestBodies(hashes []common.Hash, isFastchain bool, call uint32) error {
	return ftp.peer.RequestBodies(hashes, isFastchain, types.DownloaderCall)
}
func (ftp *floodingTestPeer) RequestReceipts(hashes []common.Hash, isFastchain bool) error {
	return ftp.peer.RequestReceipts(hashes, isFastchain)
}
func (ftp *floodingTestPeer) RequestNodeData(hashes []common.Hash, isFastchain bool) error {
	return ftp.peer.RequestNodeData(hashes, isFastchain)
}

func (ftp *floodingTestPeer) RequestHeadersByNumber(from uint64, count, skip int, reverse bool, isFastchain bool) error {
	deliveriesDone := make(chan struct{}, 500)
	for i := 0; i < cap(deliveriesDone); i++ {
		peer := fmt.Sprintf("fake-peer%d", i)
		ftp.pend.Add(1)

		go func() {
			ftp.tester.downloader.DeliverHeaders(peer, []*types.SnailHeader{{}, {}, {}, {}})
			deliveriesDone <- struct{}{}
			ftp.pend.Done()
		}()
	}
	// Deliver the actual requested headers.
	go ftp.peer.RequestHeadersByNumber(from, count, skip, reverse, isFastchain)
	// None of the extra deliveries should block.
	timeout := time.After(60 * time.Second)
	for i := 0; i < cap(deliveriesDone); i++ {
		select {
		case <-deliveriesDone:
		case <-timeout:
			panic("blocked")
		}
	}
	return nil
}

func testDeliverHeadersHang(t *testing.T, protocol int, mode SyncMode) {
	t.Parallel()

	master := newTester()
	defer master.terminate()
	parents1 := make([]*types.SnailBlock, 1)
	parents1[0] = master.genesis

	targetBlocks := 5

	fhashes, fheaders, fblocks, freceipt, fastChain, remoteHeader := master.makeFastChain(targetBlocks)
	hashes, headers, blocks, _ := master.makeChain(targetBlocks, 0, parents1, false, fastChain, 1)
	master.fdownloader.SetHeader(remoteHeader)
	master.downloader.SetHeader(remoteHeader)
	master.fdownloader.SetSD(master.downloader)

	for i := 0; i < 200; i++ {
		tester := newTester()
		tester.peerDb = master.peerDb

		tester.newPeer("peer", protocol, hashes, headers, blocks)
		tester.ftester.NewPeer("peer", protocol, fhashes, fheaders, fblocks, freceipt)

		// Whenever the downloader requests headers, flood it with
		// a lot of unrequested header deliveries.
		tester.downloader.peers.Peer("peer").SetPeer(&floodingTestPeer{
			peer:   tester.downloader.peers.Peer("peer").GetPeer(),
			tester: tester,
		})
		if err := tester.sync("peer", nil, mode); err != nil {
			t.Errorf("test %d: sync failed: %v", i, err)
		}
		tester.terminate()

		// Flush all goroutines to prevent messing with subsequent tests
		tester.downloader.peers.Peer("peer").GetPeer().(*floodingTestPeer).pend.Wait()
	}
}
