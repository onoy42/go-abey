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
	crand "crypto/rand"
	"errors"
	"fmt"
	"math"
	"math/big"
	mrand "math/rand"
	"sync/atomic"
	"time"

	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/log"
	"github.com/hashicorp/golang-lru"
	"github.com/abeychain/go-abey/consensus"
	"github.com/abeychain/go-abey/core"
	"github.com/abeychain/go-abey/core/snailchain/rawdb"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/abeydb"
	"github.com/abeychain/go-abey/params"
)

const (
	headerCacheLimit = 512
	tdCacheLimit     = 1024
	numberCacheLimit = 2048
)

// HeaderChain implements the basic block header chain logic that is shared by
// core.BlockChain and light.LightChain. It is not usable in itself, only as
// a part of either structure.
// It is not thread safe either, the encapsulating chain structures should do
// the necessary mutex locking/unlocking.
type HeaderChain struct {
	config *params.ChainConfig

	chainDb       abeydb.Database
	genesisHeader *types.SnailHeader

	currentHeader     atomic.Value // Current head of the header chain (may be above the block chain!)
	currentHeaderHash common.Hash  // Hash of the current head of the header chain (prevent recomputing all the time)

	headerCache *lru.Cache // Cache for the most recent block headers
	tdCache     *lru.Cache // Cache for the most recent block total difficulties
	numberCache *lru.Cache // Cache for the most recent block numbers

	procInterrupt func() bool

	rand   *mrand.Rand
	engine consensus.Engine
}

// NewHeaderChain creates a new HeaderChain structure.
//  getValidator should return the parent's validator
//  procInterrupt points to the parent's interrupt semaphore
//  wg points to the parent's shutdown wait group
func NewHeaderChain(chainDb abeydb.Database, config *params.ChainConfig, engine consensus.Engine, procInterrupt func() bool) (*HeaderChain, error) {
	headerCache, _ := lru.New(headerCacheLimit)
	tdCache, _ := lru.New(tdCacheLimit)
	numberCache, _ := lru.New(numberCacheLimit)

	// Seed a fast but crypto originating random generator
	seed, err := crand.Int(crand.Reader, big.NewInt(math.MaxInt64))
	if err != nil {
		return nil, err
	}

	hc := &HeaderChain{
		config:        config,
		chainDb:       chainDb,
		headerCache:   headerCache,
		tdCache:       tdCache,
		numberCache:   numberCache,
		procInterrupt: procInterrupt,
		rand:          mrand.New(mrand.NewSource(seed.Int64())),
		engine:        engine,
	}

	//TODO have get hc.hash 20180805

	hc.genesisHeader = hc.GetHeaderByNumber(0)
	if hc.genesisHeader == nil {
		return nil, ErrNoGenesis
	}

	hc.currentHeader.Store(hc.genesisHeader)
	if head := rawdb.ReadHeadBlockHash(chainDb); head != (common.Hash{}) {
		if chead := hc.GetHeaderByHash(head); chead != nil {
			hc.currentHeader.Store(chead)
		}
	}

	hc.currentHeaderHash = hc.CurrentHeader().Hash()

	return hc, nil
}

// GetBlockNumber retrieves the block number belonging to the given hash
// from the cache or database
func (hc *HeaderChain) GetBlockNumber(hash common.Hash) *uint64 {
	if cached, ok := hc.numberCache.Get(hash); ok {
		number := cached.(uint64)
		return &number
	}
	number := rawdb.ReadHeaderNumber(hc.chainDb, hash)
	if number != nil {
		hc.numberCache.Add(hash, *number)
	}
	return number
}

// WriteHeader writes a header into the local chain, given that its parent is
// already known. If the total difficulty of the newly inserted header becomes
// greater than the current known TD, the canonical chain is re-routed.
//
// Note: This method is not concurrent-safe with inserting blocks simultaneously
// into the chain, as side effects caused by reorganisations cannot be emulated
// without the real blocks. Hence, writing headers directly should only be done
// in two scenarios: pure-header mode of operation (light clients), or properly
// separated header/block phases (non-archive clients).
func (hc *HeaderChain) WriteHeader(header *types.SnailHeader, fruitHeads []*types.SnailHeader) (status WriteStatus, err error) {
	// Cache some values to prevent constant recalculation
	var (
		hash   = header.Hash()
		number = header.Number.Uint64()
	)
	// Calculate the total difficulty of the header
	ptd := hc.GetTd(header.ParentHash, number-1)
	if ptd == nil {
		log.Info("WriteHeader", "ptd", ptd, "parentHash", header.ParentHash, "number", number-1)
		return NonStatTy, consensus.ErrUnknownAncestor
	}
	localTd := hc.GetTd(hc.currentHeaderHash, hc.CurrentHeader().Number.Uint64())
	blockTd := big.NewInt(0)
	for _, f := range fruitHeads {
		blockTd.Add(blockTd, f.FruitDifficulty)
	}
	blockTd.Add(blockTd, header.Difficulty)
	externTd := new(big.Int).Add(blockTd, ptd)

	// Irrelevant of the canonical status, write the td and header to the database
	if err := hc.WriteTd(hash, number, externTd); err != nil {
		log.Crit("Failed to write header total difficulty", "err", err)
	}
	rawdb.WriteHeader(hc.chainDb, header)
	if len(fruitHeads) > 0 {
		rawdb.WriteFruitsHead(hc.chainDb, header.Hash(), header.Number.Uint64(), fruitHeads)
		rawdb.WriteFtHeadLookupEntries(hc.chainDb, header, fruitHeads)
	}

	// If the total difficulty is higher than our known, add it to the canonical chain
	// Second clause in the if statement reduces the vulnerability to selfish mining.
	// Please refer to http://www.cs.cornell.edu/~ie53/publications/btcProcFC.pdf
	if externTd.Cmp(localTd) > 0 || (externTd.Cmp(localTd) == 0 && mrand.Float64() < 0.5) {
		// Delete any canonical number assignments above the new head
		batch := hc.chainDb.NewBatch()
		for i := number + 1; ; i++ {
			hash := rawdb.ReadCanonicalHash(hc.chainDb, i)
			if hash == (common.Hash{}) {
				break
			}
			rawdb.DeleteCanonicalHash(batch, i)
		}
		batch.Write()

		// Overwrite any stale canonical number assignments
		var (
			headHash   = header.ParentHash
			headNumber = header.Number.Uint64() - 1
			headHeader = hc.GetHeader(headHash, headNumber)
		)
		for rawdb.ReadCanonicalHash(hc.chainDb, headNumber) != headHash {
			rawdb.WriteCanonicalHash(hc.chainDb, headHash, headNumber)

			headHash = headHeader.ParentHash
			headNumber = headHeader.Number.Uint64() - 1
			headHeader = hc.GetHeader(headHash, headNumber)
		}
		// Extend the canonical chain with the new header
		rawdb.WriteCanonicalHash(hc.chainDb, hash, number)
		rawdb.WriteHeadHeaderHash(hc.chainDb, hash)

		hc.currentHeaderHash = hash
		hc.currentHeader.Store(types.CopySnailHeader(header))

		status = CanonStatTy
	} else {
		status = SideStatTy
	}

	hc.headerCache.Add(hash, header)
	hc.numberCache.Add(hash, number)

	return
}

// WhCallback is a callback function for inserting individual headers.
// A callback is used for two reasons: first, in a LightChain, status should be
// processed and light chain events sent, while in a BlockChain this is not
// necessary since chain events are sent after inserting blocks. Second, the
// header writes should be protected by the parent chain mutex individually.
type WhCallback func(*types.SnailHeader, []*types.SnailHeader) error

//ValidateHeaderChain validate the header of the snailchain
func (hc *HeaderChain) ValidateHeaderChain(chain []*types.SnailHeader, fruits [][]*types.SnailHeader, checkFreq int, fastchain *core.HeaderChain, checkpoint uint64) (int, error) {
	// Do a sanity check that the provided chain is actually ordered and linked
	for i := 1; i < len(chain); i++ {
		if chain[i].Number.Uint64() != chain[i-1].Number.Uint64()+1 || chain[i].ParentHash != chain[i-1].Hash() {
			// Chain broke ancestry, log a messge (programming error) and skip insertion
			log.Error("Non contiguous header insert", "number", chain[i].Number, "hash", chain[i].Hash(),
				"parent", chain[i].ParentHash, "prevnumber", chain[i-1].Number, "prevhash", chain[i-1].Hash())

			return 0, fmt.Errorf("non contiguous insert: item %d is #%d [%x…], item %d is #%d [%x…] (parent [%x…])", i-1, chain[i-1].Number,
				chain[i-1].Hash().Bytes()[:4], i, chain[i].Number, chain[i].Hash().Bytes()[:4], chain[i].ParentHash[:4])
		}
	}

	// Generate the list of seal verification requests, and start the parallel verifier
	seals := make([]bool, len(chain))
	for i := 0; i < len(seals)/checkFreq; i++ {
		index := i*checkFreq + hc.rand.Intn(checkFreq)
		if index >= len(seals) {
			index = len(seals) - 1
		}
		seals[index] = true
	}
	seals[len(seals)-1] = true // Last should always be verified to avoid junk

	abort, results := hc.engine.VerifySnailHeaders(hc, chain, seals)
	defer close(abort)

	// Iterate over the headers and ensure they all check out
	for i, header := range chain {
		// If the chain is terminating, stop processing blocks
		if hc.procInterrupt() {
			log.Debug("Premature abort during headers verification")
			return 0, errors.New("aborted")
		}
		// If the header is a banned one, straight out abort
		if BadHashes[header.Hash()] {
			return i, ErrBlacklistedHash
		}
		// Otherwise wait for headers checks and ensure they pass
		if err := <-results; err != nil {
			return i, err
		}
		if err := hc.engine.ValidateRewarded(header.Number.Uint64(), header.Hash(), fastchain); err != nil {
			return i, err
		}
		fruitHeaders := fruits[i]
		maxfb := fastchain.GetHeader(fruitHeaders[len(fruitHeaders)-1].FastHash, fruitHeaders[len(fruitHeaders)-1].FastNumber.Uint64())
		minfb := fastchain.GetHeader(fruitHeaders[0].FastHash, fruitHeaders[0].FastNumber.Uint64())
		if minfb == nil || maxfb == nil {
			return i, consensus.ErrFutureBlock
		}
		if fruitHeaders[len(fruitHeaders)-1].Time == nil || header.Time == nil || header.Time.Cmp(fruitHeaders[len(fruitHeaders)-1].Time) < 0 {
			log.Info("ValidateHeaderChain validate header time", "block.Time()", header.Time, "fruitHeaders[len(fruitHeaders)-1].Time", fruitHeaders[len(fruitHeaders)-1].Time)
			return i, ErrBlockTime
		}
		gap := new(big.Int).Sub(maxfb.Time, minfb.Time)
		if gap.Cmp(params.MinTimeGap) < 0 {
			log.Info("ValidateHeaderChain snail validate time gap error", "block", header.Number, "first fb number", minfb.Number, "first fb time", minfb.Time, "last fb number", maxfb.Number, "last fb time", maxfb.Time, "tim gap", gap)
			return i, ErrGapFruits
		}
		if hash := hc.GetFruitsHash(header, fruitHeaders); hash != header.FruitsHash {
			return i, fmt.Errorf("ValidateHeaderChain fruits hash mismatch: have %x, want %x", types.DeriveSha(types.FruitsHeaders(fruitHeaders)), header.FruitsHash)
		}
		/*if !hc.IsCanonicalBlock(header.ParentHash, header.Number.Uint64()-1) {
			if !hc.HasHeader(header.ParentHash, header.Number.Uint64()-1) {
				log.Info("ValidateHeaderChain HasHeader", "header.ParentHash", header.ParentHash, "parentNumber", header.Number.Uint64()-1)
				return i, consensus.ErrUnknownAncestor
			}
			return i, consensus.ErrPrunedAncestor
		}*/
		for _, fruit := range fruitHeaders {
			if err := hc.engine.ValidateFruitHeader(header, fruit, hc, fastchain, checkpoint); err != nil {
				log.Error("ValidateHeaderChain", "snailBlock number", header.Number, "fruit number", fruit.FastNumber, "err", err)
				return i, err
			}
		}
	}
	return 0, nil
}

// InsertHeaderChain attempts to insert the given header chain in to the local
// chain, possibly creating a reorg. If an error is returned, it will return the
// index number of the failing header as well an error describing what went wrong.
//
// The verify parameter can be used to fine tune whether nonce verification
// should be done or not. The reason behind the optional check is because some
// of the header retrieval mechanisms already need to verfy nonces, as well as
// because nonces can be verified sparsely, not needing to check each.
func (hc *HeaderChain) InsertHeaderChain(chain []*types.SnailHeader, fruitHeads [][]*types.SnailHeader, writeHeader WhCallback, start time.Time) (int, error) {
	// Collect some import statistics to report on
	stats := struct{ processed, ignored int }{}
	// All headers passed verification, import them into the database
	for i, header := range chain {
		// Short circuit insertion if shutting down
		if hc.procInterrupt() {
			log.Debug("Premature abort during headers import")
			return i, errors.New("aborted")
		}
		// If the header's already known, skip it, otherwise store
		hash := header.Hash()
		if hc.HasHeader(hash, header.Number.Uint64()) {
			externTd := hc.GetTd(hash, header.Number.Uint64())
			localTd := hc.GetTd(hc.currentHeaderHash, hc.CurrentHeader().Number.Uint64())
			if externTd == nil || externTd.Cmp(localTd) <= 0 {
				stats.ignored++
				continue
			}
		}
		if len(fruitHeads) > 0 {
			if err := writeHeader(header, fruitHeads[i]); err != nil {
				return i, err
			}
		} else {
			if err := writeHeader(header, nil); err != nil {
				return i, err
			}
		}

		stats.processed++
	}
	// Report some public statistics so the user has a clue what's going on
	last := chain[len(chain)-1]
	log.Info("Imported new snail block headers", "count", stats.processed, "elapsed", common.PrettyDuration(time.Since(start)),
		"number", last.Number, "hash", last.Hash(), "ignored", stats.ignored)

	return 0, nil
}

// GetBlockHashesFromHash retrieves a number of block hashes starting at a given
// hash, fetching towards the genesis block.
func (hc *HeaderChain) GetBlockHashesFromHash(hash common.Hash, max uint64) []common.Hash {
	// Get the origin header from which to fetch
	header := hc.GetHeaderByHash(hash)
	if header == nil {
		return nil
	}
	// Iterate the headers until enough is collected or the genesis reached
	chain := make([]common.Hash, 0, max)
	for i := uint64(0); i < max; i++ {
		next := header.ParentHash
		if header = hc.GetHeader(next, header.Number.Uint64()-1); header == nil {
			break
		}
		chain = append(chain, next)
		if header.Number.Sign() == 0 {
			break
		}
	}
	return chain
}

// GetAncestor retrieves the Nth ancestor of a given block. It assumes that either the given block or
// a close ancestor of it is canonical. maxNonCanonical points to a downwards counter limiting the
// number of blocks to be individually checked before we reach the canonical chain.
//
// Note: ancestor == 0 returns the same block, 1 returns its parent and so on.
func (hc *HeaderChain) GetAncestor(hash common.Hash, number, ancestor uint64, maxNonCanonical *uint64) (common.Hash, uint64) {
	if ancestor > number {
		return common.Hash{}, 0
	}
	if ancestor == 1 {
		// in this case it is cheaper to just read the header
		if header := hc.GetHeader(hash, number); header != nil {
			return header.ParentHash, number - 1
		}
		return common.Hash{}, 0
	}
	for ancestor != 0 {
		if rawdb.ReadCanonicalHash(hc.chainDb, number) == hash {
			number -= ancestor
			return rawdb.ReadCanonicalHash(hc.chainDb, number), number
		}
		if *maxNonCanonical == 0 {
			return common.Hash{}, 0
		}
		*maxNonCanonical--
		ancestor--
		header := hc.GetHeader(hash, number)
		if header == nil {
			return common.Hash{}, 0
		}
		hash = header.ParentHash
		number--
	}
	return hash, number
}

// GetTd retrieves a block's total difficulty in the canonical chain from the
// database by hash and number, caching it if found.
func (hc *HeaderChain) GetTd(hash common.Hash, number uint64) *big.Int {
	// Short circuit if the td's already in the cache, retrieve otherwise
	if cached, ok := hc.tdCache.Get(hash); ok {
		return cached.(*big.Int)
	}
	td := rawdb.ReadTd(hc.chainDb, hash, number)
	if td == nil {
		return nil
	}
	// Cache the found body for next time and return
	hc.tdCache.Add(hash, td)
	return td
}

// GetTdByHash retrieves a block's total difficulty in the canonical chain from the
// database by hash, caching it if found.
func (hc *HeaderChain) GetTdByHash(hash common.Hash) *big.Int {
	number := hc.GetBlockNumber(hash)
	if number == nil {
		return nil
	}
	return hc.GetTd(hash, *number)
}

// WriteTd stores a block's total difficulty into the database, also caching it
// along the way.
func (hc *HeaderChain) WriteTd(hash common.Hash, number uint64, td *big.Int) error {
	rawdb.WriteTd(hc.chainDb, hash, number, td)
	hc.tdCache.Add(hash, new(big.Int).Set(td))
	return nil
}

// GetHeader retrieves a block header from the database by hash and number,
// caching it if found.
func (hc *HeaderChain) GetHeader(hash common.Hash, number uint64) *types.SnailHeader {
	// Short circuit if the header's already in the cache, retrieve otherwise
	if header, ok := hc.headerCache.Get(hash); ok {
		return header.(*types.SnailHeader)
	}
	header := rawdb.ReadHeader(hc.chainDb, hash, number)
	if header == nil {
		return nil
	}
	// Cache the found header for next time and return
	hc.headerCache.Add(hash, header)
	return header
}

// GetHeaderByHash retrieves a block header from the database by hash, caching it if
// found.
func (hc *HeaderChain) GetHeaderByHash(hash common.Hash) *types.SnailHeader {
	number := hc.GetBlockNumber(hash)
	if number == nil {
		return nil
	}
	return hc.GetHeader(hash, *number)
}

// HasHeader checks if a block header is present in the database or not.
func (hc *HeaderChain) HasHeader(hash common.Hash, number uint64) bool {
	if hc.numberCache.Contains(hash) || hc.headerCache.Contains(hash) {
		return true
	}
	return rawdb.HasHeader(hc.chainDb, hash, number)
}

// GetHeaderByNumber retrieves a block header from the database by number,
// caching it (associated with its hash) if found.
func (hc *HeaderChain) GetHeaderByNumber(number uint64) *types.SnailHeader {
	hash := rawdb.ReadCanonicalHash(hc.chainDb, number)
	if hash == (common.Hash{}) {
		return nil
	}
	return hc.GetHeader(hash, number)
}

// CurrentHeader retrieves the current head header of the canonical chain. The
// header is retrieved from the HeaderChain's internal cache.
func (hc *HeaderChain) CurrentHeader() *types.SnailHeader {
	//return nil
	//TODO shoud add
	return hc.currentHeader.Load().(*types.SnailHeader)
}

// SetCurrentHeader sets the current head header of the canonical chain.
func (hc *HeaderChain) SetCurrentHeader(head *types.SnailHeader) {
	rawdb.WriteHeadHeaderHash(hc.chainDb, head.Hash())

	hc.currentHeader.Store(head)
	hc.currentHeaderHash = head.Hash()
}

// DeleteCallback is a callback function that is called by SetHead before
// each header is deleted.
type DeleteCallback func(rawdb.DatabaseDeleter, common.Hash, uint64)

// SetHead rewinds the local chain to a new head. Everything above the new head
// will be deleted and the new one set.
func (hc *HeaderChain) SetHead(head uint64, delFn DeleteCallback) {
	height := uint64(0)

	if hdr := hc.CurrentHeader(); hdr != nil {
		height = hdr.Number.Uint64()
	}
	batch := hc.chainDb.NewBatch()
	for hdr := hc.CurrentHeader(); hdr != nil && hdr.Number.Uint64() > head; hdr = hc.CurrentHeader() {
		hash := hdr.Hash()
		num := hdr.Number.Uint64()
		if delFn != nil {
			delFn(batch, hash, num)
		}
		rawdb.DeleteHeader(batch, hash, num)
		rawdb.DeleteTd(batch, hash, num)

		hc.currentHeader.Store(hc.GetHeader(hdr.ParentHash, hdr.Number.Uint64()-1))
	}
	// Roll back the canonical chain numbering
	for i := height; i > head; i-- {
		rawdb.DeleteCanonicalHash(batch, i)
	}
	batch.Write()

	// Clear out any stale content from the caches
	hc.headerCache.Purge()
	hc.tdCache.Purge()
	hc.numberCache.Purge()

	if hc.CurrentHeader() == nil {
		hc.currentHeader.Store(hc.genesisHeader)
	}
	hc.currentHeaderHash = hc.CurrentHeader().Hash()

	rawdb.WriteHeadHeaderHash(hc.chainDb, hc.currentHeaderHash)
}

// SetGenesis sets a new genesis block header for the chain
func (hc *HeaderChain) SetGenesis(head *types.SnailHeader) {
	hc.genesisHeader = head
}

// Config retrieves the header chain's chain configuration.
func (hc *HeaderChain) Config() *params.ChainConfig { return hc.config }

// Engine retrieves the header chain's consensus engine.
func (hc *HeaderChain) Engine() consensus.Engine { return hc.engine }

// GetBlock implements consensus.ChainReader, and returns nil for every input as
// a header chain does not have blocks available for retrieval.
func (hc *HeaderChain) GetBlock(hash common.Hash, number uint64) *types.SnailBlock {
	return nil
}

func (hc *HeaderChain) GetFruitsHash(header *types.SnailHeader, headers []*types.SnailHeader) common.Hash {
	if hc.config.IsTIP5(header.Number) {
		return types.DeriveSha(types.FruitsHeaders(headers))
	}
	return header.FruitsHash
}

// IsCanonicalBlock checks if a block on the Canonical block chain
// or not, caching it if present.
func (hc *HeaderChain) IsCanonicalBlock(hash common.Hash, number uint64) bool {
	//if get number bigger than currentNumber return nil
	if number > hc.currentHeader.Load().(*types.SnailHeader).Number.Uint64() {
		return false
	}
	return rawdb.ReadCanonicalHash(hc.chainDb, number) == hash
}
