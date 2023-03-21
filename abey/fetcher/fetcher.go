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

// Package fetcher contains the block announcement based synchronisation.
package fetcher

import (
	"errors"
	"fmt"
	"github.com/abeychain/go-abey/consensus/tbft/help"
	"math/rand"
	"time"

	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/common/prque"
	"github.com/abeychain/go-abey/consensus"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/log"
	"math/big"
	"sync"
)

const (
	arriveTimeout    = 500 * time.Millisecond // Time allowance before an announced block is explicitly requested
	gatherSlack      = 100 * time.Millisecond // Interval used to collate almost-expired announces with fetches
	fetchTimeout     = 5 * time.Second        // Maximum allotted time to return an explicitly requested block
	lowCommitteeDist = 1                      // Maximum allowed backward distance from the chain head
	maxQueueDist     = 512                    // Maximum allowed distance from the chain head to queue
	hashLimit        = 512                    // Maximum number of unique blocks a peer may have announced
	blockLimit       = 512                    // Maximum number of unique blocks a peer may have delivered
)

var (
	errTerminated = errors.New("terminated")
)

// blockRetrievalFn is a callback type for retrieving a block from the local chain.
type blockRetrievalFn func(common.Hash) *types.Block

// headerRequesterFn is a callback type for sending a header retrieval request.
type headerRequesterFn func(common.Hash) error

// bodyRequesterFn is a callback type for sending a body retrieval request.
type bodyRequesterFn func([]common.Hash, bool, uint32) error

// headerVerifierFn is a callback type to verify a block's header for fast propagation.
type headerVerifierFn func(header *types.Header) error

// blockBroadcasterFn is a callback type for broadcasting a block to connected peers.
type blockBroadcasterFn func(block *types.Block, propagate bool)

// signBroadcasterFn is a callback type for broadcasting a sign to connected peers.
type signBroadcasterFn func(pbSign []*types.PbftSign)

// chainHeightFn is a callback type to retrieve the current chain height.
type chainHeightFn func() uint64

// chainInsertFn is a callback type to insert a batch of blocks into the local chain.
type chainInsertFn func(types.Blocks) (int, error)

// peerDropFn is a callback type for dropping a peer detected as malicious.
type peerDropFn func(id string, call uint32)

// announce is the hash notification of the availability of a new block in the
// network.
type announce struct {
	hash   common.Hash   // Hash of the block being announced
	number uint64        // Number of the block being announced (0 = unknown | old protocol)
	header *types.Header // Header of the block partially reassembled (new protocol)
	time   time.Time     // Timestamp of the announcement

	origin string // Identifier of the peer originating the notification

	fetchHeader headerRequesterFn // Fetcher function to retrieve the header of an announced block
	fetchBodies bodyRequesterFn   // Fetcher function to retrieve the body of an announced block
}

// headerFilterTask represents a batch of headers needing fetcher filtering.
type headerFilterTask struct {
	peer    string          // The source peer of block headers
	headers []*types.Header // Collection of headers to filter
	time    time.Time       // Arrival time of the headers
}

// PbftAgentFetcher encapsulates functions required to interact with PbftAgent.
type PbftAgentFetcher interface {
	// a  type to verify a leader's fast block for fast propagation.
	VerifyCommitteeSign(signs *types.PbftSign) bool
	// when check evil Leader, changeLeader
	ChangeCommitteeLeader(height *big.Int) bool
	//  according height require committee number
	GetCommitteeNumber(height *big.Int) int32
	// AcquireCommitteeAuth check current node whether committee.
	AcquireCommitteeAuth(*big.Int) bool
}

// bodyFilterTask represents a batch of block bodies (transactions and uncles)
// needing fetcher filtering.
type bodyFilterTask struct {
	peer         string                     // The source peer of block bodies
	transactions [][]*types.Transaction     // Collection of transactions per block bodies
	signs        [][]*types.PbftSign        // Collection of sign per block bodies
	infos        [][]*types.CommitteeMember // committee change
	time         time.Time                  // Arrival time of the blocks' contents
}

// inject represents a schedules import operation.
type inject struct {
	origin string
	block  *types.Block
}

// injectMulti represents more schedules import operation.
type injectMulti struct {
	origins []string
	blocks  []*types.Block
}

// injectDone represents a schedules block sign remove operation.
type injectDone struct {
	block *types.Block
}

// Fetcher is responsible for accumulating block announcements from various peers
// and scheduling them for retrieval.
type Fetcher struct {
	// Various event channels
	notify chan *announce
	inject chan *inject

	headerFilter chan chan *headerFilterTask
	bodyFilter   chan chan *bodyFilterTask

	doneBlockSign chan *injectDone
	doneConsensus chan *big.Int
	quit          chan struct{}
	blockMutex    *sync.Mutex //block mutex

	// Announce states
	announces  map[string]int              // Per peer announce counts to prevent memory exhaustion
	announced  map[common.Hash][]*announce // Announced blocks, scheduled for fetching
	fetching   map[common.Hash]*announce   // Announced blocks, currently fetching
	fetched    map[common.Hash][]*announce // Blocks with headers fetched, scheduled for body retrieval
	completing map[common.Hash]*announce   // Blocks with headers, currently body-completing

	// Block cache
	queue          *prque.Prque             // Queue containing the import operations (block number sorted)
	queues         map[string]int           // Per peer block counts to prevent memory exhaustion
	queued         map[common.Hash]*inject  // Set of already queued blocks (to dedupe imports)
	blockMultiHash map[uint64][]common.Hash //solve same height more block question
	sendBlockHash  map[uint64][]common.Hash //mark already send block in same height

	// Callbacks
	getBlock           blockRetrievalFn   // Retrieves a block from the local chain
	verifyHeader       headerVerifierFn   // Checks if a block's headers have a valid proof of work
	broadcastFastBlock blockBroadcasterFn // Broadcasts a block to connected peers
	broadcastSigns     signBroadcasterFn  // Broadcasts a signs to connected peers

	chainHeight  chainHeightFn // Retrieves the current chain's height
	insertChain  chainInsertFn // Injects a batch of blocks into the chain
	dropPeer     peerDropFn    // Drops a peer for misbehaving
	agentFetcher PbftAgentFetcher

	// Testing hooks
	announceChangeHook func(common.Hash, bool) // Method to call upon adding or deleting a hash from the announce list
	queueChangeHook    func(common.Hash, bool) // Method to call upon adding or deleting a block from the import queue
	fetchingHook       func([]common.Hash)     // Method to call upon starting a block (eth/61) or header (eth/62) fetch
	completingHook     func([]common.Hash)     // Method to call upon starting a block body fetch (eth/62)
	importedHook       func(*types.Block)      // Method to call upon successful block import (both eth/61 and eth/62)
}

// New creates a block fetcher to retrieve blocks based on hash announcements.
func New(getBlock blockRetrievalFn, verifyHeader headerVerifierFn, broadcastFastBlock blockBroadcasterFn, chainHeight chainHeightFn, insertChain chainInsertFn, dropPeer peerDropFn, agentFetcher PbftAgentFetcher, broadcastSigns signBroadcasterFn) *Fetcher {
	return &Fetcher{
		notify:        make(chan *announce),
		inject:        make(chan *inject),
		headerFilter:  make(chan chan *headerFilterTask),
		bodyFilter:    make(chan chan *bodyFilterTask),
		doneBlockSign: make(chan *injectDone),
		doneConsensus: make(chan *big.Int),
		quit:          make(chan struct{}),
		announces:     make(map[string]int),
		announced:     make(map[common.Hash][]*announce),
		fetching:      make(map[common.Hash]*announce),
		fetched:       make(map[common.Hash][]*announce),
		completing:    make(map[common.Hash]*announce),
		queue:         prque.New(nil),
		queues:        make(map[string]int),
		queued:        make(map[common.Hash]*inject),

		getBlock:           getBlock,
		verifyHeader:       verifyHeader,
		broadcastFastBlock: broadcastFastBlock,
		chainHeight:        chainHeight,
		insertChain:        insertChain,
		dropPeer:           dropPeer,
		blockMultiHash:     make(map[uint64][]common.Hash),
		sendBlockHash:      make(map[uint64][]common.Hash),
		agentFetcher:       agentFetcher,
		broadcastSigns:     broadcastSigns,
		blockMutex:         new(sync.Mutex),
	}
}

// Start boots up the announcement based synchroniser, accepting and processing
// hash notifications and block fetches until termination requested.
func (f *Fetcher) Start() {
	go f.loop()
}

// Stop terminates the announcement based synchroniser, canceling all pending
// operations.
func (f *Fetcher) Stop() {
	close(f.quit)
}

// Notify announces the fetcher of the potential availability of a new block in
// the network.
func (f *Fetcher) Notify(peer string, hash common.Hash, number uint64, time time.Time,
	headerFetcher headerRequesterFn, bodyFetcher bodyRequesterFn) error {
	block := &announce{
		hash:        hash,
		number:      number,
		time:        time,
		origin:      peer,
		fetchHeader: headerFetcher,
		fetchBodies: bodyFetcher,
	}

	select {
	case f.notify <- block:
		return nil
	case <-f.quit:
		return errTerminated
	}
}

// Enqueue tries to fill gaps the the fetcher's future import queue.
func (f *Fetcher) Enqueue(peer string, block *types.Block) error {
	op := &inject{
		origin: peer,
		block:  block,
	}
	select {
	case f.inject <- op:
		return nil
	case <-f.quit:
		return errTerminated
	}
}

// FilterHeaders extracts all the headers that were explicitly requested by the fetcher,
// returning those that should be handled differently.
func (f *Fetcher) FilterHeaders(peer string, headers []*types.Header, time time.Time) []*types.Header {
	// Send the filter channel to the fetcher
	filter := make(chan *headerFilterTask)

	select {
	case f.headerFilter <- filter:
	case <-f.quit:
		return nil
	}
	// Request the filtering of the header list
	select {
	case filter <- &headerFilterTask{peer: peer, headers: headers, time: time}:
	case <-f.quit:
		return nil
	}
	// Retrieve the headers remaining after filtering
	select {
	case task := <-filter:
		return task.headers
	case <-f.quit:
		return nil
	}
}

// FilterBodies extracts all the block bodies that were explicitly requested by
// the fetcher, returning those that should be handled differently.
func (f *Fetcher) FilterBodies(peer string, transactions [][]*types.Transaction, signs [][]*types.PbftSign, infos [][]*types.CommitteeMember, time time.Time) ([][]*types.Transaction, [][]*types.PbftSign, [][]*types.CommitteeMember) {
	log.Debug("Filtering fast bodies", "peer", peer, "txs", len(transactions), "signs", len(signs))

	// Send the filter channel to the fetcher
	filter := make(chan *bodyFilterTask)

	select {
	case f.bodyFilter <- filter:
	case <-f.quit:
		return nil, nil, nil
	}
	// Request the filtering of the body list
	select {
	case filter <- &bodyFilterTask{peer: peer, transactions: transactions, signs: signs, infos: infos, time: time}:
	case <-f.quit:
		return nil, nil, nil
	}
	// Retrieve the bodies remaining after filtering
	select {
	case task := <-filter:
		return task.transactions, task.signs, task.infos
	case <-f.quit:
		return nil, nil, nil
	}
}

// Loop is the main fetcher loop, checking and processing various notification
// events.
func (f *Fetcher) loop() {
	// Iterate the block fetching until a quit is requested
	fetchTimer := time.NewTimer(0)
	completeTimer := time.NewTimer(0)

	for {
		log.Debug("Loop start", "announced", len(f.announced), "fetching", len(f.fetching), "fetched", len(f.fetched), "completing", len(f.completing))
		// Clean up any expired block fetches
		for hash, announce := range f.fetching {
			if time.Since(announce.time) > fetchTimeout {
				f.forgetHash(hash)
			}
		}

		finished := false
		// Import any queued blocks that could potentially fit
		height := f.chainHeight()
		for !f.queue.Empty() {
			opMulti := f.queue.PopItem().(*injectMulti)
			blocks := opMulti.blocks
			peers := opMulti.origins

			if len(blocks) > 0 {
				for i, block := range blocks {
					hash := block.Hash()
					peer := peers[i]

					if f.queueChangeHook != nil {
						f.queueChangeHook(block.Hash(), false)
					}

					// If too high up the chain or phase, continue later
					number := block.NumberU64()
					log.Trace("Loop check", "number", number, "height", height, "same block", len(blocks), "peer", peer, "hash", hash)
					if number > height+1 {
						f.queue.Push(opMulti, -int64(number))
						if f.queueChangeHook != nil {
							f.queueChangeHook(hash, true)
						}
						finished = true
						break
					}
					// Otherwise if fresh and still unknown, try and import
					if number <= height {
						log.Info("Block has already exist", "num", number, "height", height, "peer", peer, "hash", hash)
						f.forgetBlockHeight(big.NewInt(int64(number)))
						break
					}

					if f.getBlock(hash) != nil {
						if f.agentFetcher.AcquireCommitteeAuth(block.Number()) {
							f.markBroadcastBlock(number, peer, block)
						}
						f.forgetBlockHeight(big.NewInt(int64(number)))
						break
					} else {

						if !f.markBroadcastBlock(number, peer, block) {
							f.forgetInvaildBlock(block)
							continue
						}

						committeeNumber := f.agentFetcher.GetCommitteeNumber(block.Number())
						if f.agreeAtSameHeight(block.Signs(), hash, committeeNumber) {
							log.Debug("Block come agreement", "num", number, "height", height, "same block", len(blocks), "sign", len(block.Signs()), "hash", hash)
							f.verifyComeAgreement(peer, block, block.AllSigns())
							finished = true
						}
					}
				}
			}
			if finished {
				break
			}
		}
		// Wait for an outside event to occur
		select {
		case <-f.quit:
			// Fetcher terminating, abort all operations
			return

		case notification := <-f.notify:
			// A block was announced, make sure the peer isn't DOSing us
			propAnnounceInMeter.Mark(1)

			count := f.announces[notification.origin] + 1
			if count > hashLimit {
				log.Debug("Peer exceeded outstanding announces", "peer", notification.origin, "limit", hashLimit)
				propAnnounceDOSMeter.Mark(1)
				break
			}
			// If we have a valid block number, check that it's potentially useful
			if notification.number > 0 {
				if dist := int64(notification.number) - int64(f.chainHeight()); dist < lowCommitteeDist || dist > maxQueueDist {
					log.Debug("Peer discarded announcement", "peer", notification.origin, "number", notification.number, "hash", notification.hash, "distance", dist)
					propAnnounceDropMeter.Mark(1)
					break
				}
			}
			// All is well, schedule the announce if block's not yet downloading
			if _, ok := f.fetching[notification.hash]; ok {
				break
			}
			if _, ok := f.completing[notification.hash]; ok {
				break
			}
			f.announces[notification.origin] = count
			f.announced[notification.hash] = append(f.announced[notification.hash], notification)
			if f.announceChangeHook != nil && len(f.announced[notification.hash]) == 1 {
				f.announceChangeHook(notification.hash, true)
			}
			log.Debug("Announce hash", "num", notification.number, "annouce len", len(f.announced), "peer", notification.origin)
			if len(f.announced) == 1 {
				f.rescheduleFetch(fetchTimer)
			}

		case op := <-f.inject:
			// A direct block insertion was requested, try and fill any pending gaps
			propBroadcastInMeter.Mark(1)
			f.enqueue(op.origin, op.block)

		case block := <-f.doneBlockSign:
			// A pending import finished, remove all traces of the notification
			f.forgetInvaildBlock(block.block)

		case number := <-f.doneConsensus:
			f.forgetBlockHeight(number)

		case <-fetchTimer.C:
			// At least one block's timer ran out, check for needing retrieval
			request := make(map[string][]common.Hash)

			for hash, announces := range f.announced {
				if time.Since(announces[0].time) > arriveTimeout-gatherSlack {
					// Pick a random peer to retrieve from, reset all others
					announce := announces[rand.Intn(len(announces))]
					f.forgetHash(hash)

					// If the block still didn't arrive, queue for fetching
					if f.getBlock(hash) == nil {
						request[announce.origin] = append(request[announce.origin], hash)
						f.fetching[hash] = announce
					}
					log.Debug("Fetching headers fetchTimer", "count", len(request), "number", announce.number, "hash", hash, "peer", announce.origin)
				}
			}
			// Send out all block header requests
			for peer, hashes := range request {
				log.Trace("Fetching scheduled fast headers", "number", f.fetching[hashes[0]].number, "peer", peer, "list", hashes)

				// Create a closure of the fetch and schedule in on a new thread
				fetchHeader, hashes := f.fetching[hashes[0]].fetchHeader, hashes
				go func() {
					if f.fetchingHook != nil {
						f.fetchingHook(hashes)
					}
					for _, hash := range hashes {
						headerFetchMeter.Mark(1)
						fetchHeader(hash) // Suboptimal, but protocol doesn't allow batch header retrievals
					}
				}()
			}
			// Schedule the next fetch if blocks are still pending
			f.rescheduleFetch(fetchTimer)

		case <-completeTimer.C:
			// At least one header's timer ran out, retrieve everything
			request := make(map[string][]common.Hash)

			for hash, announces := range f.fetched {
				// Pick a random peer to retrieve from, reset all others
				announce := announces[rand.Intn(len(announces))]
				f.forgetHash(hash)

				// If the block still didn't arrive, queue for completion
				if f.getBlock(hash) == nil {
					request[announce.origin] = append(request[announce.origin], hash)
					f.completing[hash] = announce
				}
				log.Debug("Fetching body completeTimer", "count", len(request), "number", announce.number, "hash", hash, "peer", announce.origin)
			}
			// Send out all block body requests
			for peer, hashes := range request {
				log.Trace("Fetching scheduled fast bodies", "number", f.completing[hashes[0]].number, "peer", peer, "list", hashes)

				// Create a closure of the fetch and schedule in on a new thread
				if f.completingHook != nil {
					f.completingHook(hashes)
				}
				bodyFetchMeter.Mark(int64(len(hashes)))
				go f.completing[hashes[0]].fetchBodies(hashes, true, types.FetcherCall)
			}
			// Schedule the next fetch if blocks are still pending
			f.rescheduleComplete(completeTimer)

		case filter := <-f.headerFilter:
			// Headers arrived from a remote peer. Extract those that were explicitly
			// requested by the fetcher, and return everything else so it's delivered
			// to other parts of the system.
			var task *headerFilterTask
			select {
			case task = <-filter:
			case <-f.quit:
				return
			}
			headerFilterInMeter.Mark(int64(len(task.headers)))
			if len(task.headers) != 0 {
				log.Debug("Loop headerFilter", "headers", len(task.headers), "number", task.headers[0].Number, "peer", task.peer)
			}
			// Split the batch of headers into unknown ones (to return to the caller),
			// known incomplete ones (requiring body retrievals) and completed blocks.
			unknown, incomplete := []*types.Header{}, []*announce{}
			for _, header := range task.headers {
				hash := header.Hash()

				// Filter fetcher-requested headers from other synchronisation algorithms
				if announce := f.fetching[hash]; announce != nil && announce.origin == task.peer && f.fetched[hash] == nil && f.completing[hash] == nil && f.getPendingBlock(hash) == nil {
					// If the delivered header does not match the promised number, drop the announcer
					if header.Number.Uint64() != announce.number {
						log.Info("Invalid fast block number fetched", "peer", announce.origin, "hash", header.Hash(), "announced", announce.number, "provided", header.Number)
						f.dropPeer(announce.origin, types.FetcherHeadCall)
						f.forgetHash(hash)
						continue
					}
					// Only keep if not imported by other means
					if f.getBlock(hash) == nil {
						announce.header = header
						announce.time = task.time

						// Otherwise add to the list of blocks needing completion
						incomplete = append(incomplete, announce)
					} else {
						log.Trace("Block already imported, discarding fast header", "peer", announce.origin, "number", header.Number, "hash", header.Hash())
						f.forgetHash(hash)
					}
				} else {
					// Fetcher doesn't know about it, add to the return list
					unknown = append(unknown, header)
				}
			}
			headerFilterOutMeter.Mark(int64(len(unknown)))
			select {
			case filter <- &headerFilterTask{headers: unknown, time: task.time}:
			case <-f.quit:
				return
			}
			// Schedule the retrieved headers for body completion
			for _, announce := range incomplete {
				hash := announce.header.Hash()
				if _, ok := f.completing[hash]; ok {
					continue
				}
				log.Debug("Loop header incomplete", "unknown", len(unknown), "fetched[hash]", len(f.fetched[hash]), "number", announce.number, "peer", task.peer)
				f.fetched[hash] = append(f.fetched[hash], announce)
				if len(f.fetched) == 1 {
					f.rescheduleComplete(completeTimer)
				}
			}

		case filter := <-f.bodyFilter:
			// Block bodies arrived, extract any explicitly requested blocks, return the rest
			var task *bodyFilterTask
			select {
			case task = <-filter:
			case <-f.quit:
				return
			}
			bodyFilterInMeter.Mark(int64(len(task.transactions)))
			watch := help.NewTWatch(3, fmt.Sprintf("peer: %s, handleMsg filtering fast bodies: %d", task.peer, len(task.transactions[0])))

			blocks := []*types.Block{}
			for i := 0; i < len(task.transactions) && i < len(task.signs); i++ {
				// Match up a body to any possible completion request
				matched := false

				for hash, announce := range f.completing {
					if f.getPendingBlock(hash) == nil {
						txnHash := types.DeriveSha(types.Transactions(task.transactions[i]))

						if txnHash == announce.header.TxHash && announce.origin == task.peer && len(task.signs[i]) > 0 && task.signs[i][0].FastHash == hash {
							// Mark the body matched, reassemble if still unknown
							matched = true

							if f.getBlock(hash) == nil {
								// mecMark
								block := types.NewBlockWithHeader(announce.header).WithBody(task.transactions[i], task.signs[i], task.infos[i])
								block.ReceivedAt = task.time

								blocks = append(blocks, block)
							} else {
								f.forgetHash(hash)
							}
						}
					}
				}
				if matched {
					task.transactions = append(task.transactions[:i], task.transactions[i+1:]...)
					task.signs = append(task.signs[:i], task.signs[i+1:]...)
					task.infos = append(task.infos[:i], task.infos[i+1:]...)
					i--
					continue
				}
			}

			bodyFilterOutMeter.Mark(int64(len(task.transactions)))
			select {
			case filter <- task:
			case <-f.quit:
				return
			}
			// Schedule the retrieved blocks for ordered import
			for _, block := range blocks {
				log.Debug("Loop filter end", "transactions", len(task.transactions), "blocks", len(blocks), "number", block.Number(), "sign", len(block.Signs()))
				if announce := f.completing[block.Hash()]; announce != nil {
					f.enqueue(announce.origin, block)
				}
			}
			watch.EndWatch()
			watch.Finish(fmt.Sprintf("end  blocks: %d completing: %d ", len(blocks), len(f.completing)))
		}
	}
}

// rescheduleFetch resets the specified fetch timer to the next announce timeout.
func (f *Fetcher) rescheduleFetch(fetch *time.Timer) {
	// Short circuit if no blocks are announced
	if len(f.announced) == 0 {
		return
	}
	// Otherwise find the earliest expiring announcement
	earliest := time.Now()
	for _, announces := range f.announced {
		if earliest.After(announces[0].time) {
			earliest = announces[0].time
		}
	}
	fetch.Reset(arriveTimeout - time.Since(earliest))
}

// rescheduleComplete resets the specified completion timer to the next fetch timeout.
func (f *Fetcher) rescheduleComplete(complete *time.Timer) {
	// Short circuit if no headers are fetched
	if len(f.fetched) == 0 {
		return
	}
	// Otherwise find the earliest expiring announcement
	earliest := time.Now()
	for _, announces := range f.fetched {
		if earliest.After(announces[0].time) {
			earliest = announces[0].time
		}
	}
	complete.Reset(gatherSlack - time.Since(earliest))
}

// enqueue schedules a new future import operation, if the block to be imported
// has not yet been seen.
func (f *Fetcher) enqueue(peer string, block *types.Block) {
	hash := block.Hash()
	number := block.NumberU64()

	// Ensure the peer isn't DOSing us
	count := f.queues[peer] + 1
	if count > blockLimit {
		log.Info("Discarded propagated fast block, exceeded allowance", "peer", peer, "number", block.Number(), "hash", hash, "limit", blockLimit)
		propBroadcastDOSMeter.Mark(1)
		f.forgetHash(hash)
		return
	}
	// Discard any past or too distant blocks
	if dist := int64(block.NumberU64()) - int64(f.chainHeight()); dist < lowCommitteeDist || dist > maxQueueDist {
		log.Debug("Discarded propagated fast block, too far away", "peer", peer, "number", block.Number(), "hash", hash, "distance", dist)
		propBroadcastDropMeter.Mark(1)
		f.forgetHash(hash)
		return
	}

	for _, sign := range block.Signs() {
		if ok := f.agentFetcher.VerifyCommitteeSign(sign); !ok {
			log.Info("Discarded propagated sign failed", "peer", peer, "number", number, "hash", hash)
			propSignInvaildMeter.Mark(1)
			f.forgetHash(hash)
			return
		}
	}

	// Schedule the block for future importing
	if f.getPendingBlock(hash) == nil {

		op := &inject{
			origin: peer,
			block:  block,
		}
		f.queues[peer] = count
		f.setPendingBlock(hash, op)

		opMulti := &injectMulti{}
		f.blockMultiHash[number] = append(f.blockMultiHash[number], hash)
		// update queue cache far more block in same height
		for _, hash := range f.blockMultiHash[number] {
			opOld := f.getPendingBlock(hash)
			opMulti.origins = append(opMulti.origins, opOld.origin)
			opMulti.blocks = append(opMulti.blocks, opOld.block)
		}

		f.queue.Push(opMulti, -int64(block.NumberU64()))
		if f.queueChangeHook != nil {
			f.queueChangeHook(op.block.Hash(), true)
		}
		log.Debug("Queued propagated fast block", "peer", peer, "number", block.Number(), "hash", hash, "queue", f.queue.Size())
	}
}

// markBroadcastBlock only validation block header and propagated block.
func (f *Fetcher) markBroadcastBlock(number uint64, peer string, block *types.Block) bool {
	find := false
	for _, hashOld := range f.sendBlockHash[number] {
		if hashOld == block.Hash() {
			find = true
			break
		}
	}
	if !find {
		return f.verifyBlockBroadcast(peer, block)
	}
	return false
}

// verifyBlockBroadcast only validation block header and propagated block.
func (f *Fetcher) verifyBlockBroadcast(peer string, block *types.Block) bool {
	hash := block.Hash()
	f.sendBlockHash[block.NumberU64()] = append(f.sendBlockHash[block.NumberU64()], hash)
	// Run the import on a new thread
	log.Debug("Broadcast propagated fast block", "peer", peer, "number", block.Number(), "hash", hash)
	// If the parent's unknown, abort insertion
	parent := f.getBlock(block.ParentHash())
	if parent == nil {
		log.Debug("Unknown parent of propagated fast block", "peer", peer, "number", block.Number(), "hash", hash, "parent", block.ParentHash())
		return false
	}

	// Quickly validate the header and propagate the block if it passes
	switch err := f.verifyHeader(block.Header()); err {
	case nil:
		// All ok, quickly propagate to our peers
		propBroadcastOutTimer.UpdateSince(block.ReceivedAt)
		go f.broadcastFastBlock(block, true)

	case consensus.ErrFutureBlock:
		// Weird future block, don't fail, but neither propagate

	default:
		// Something went very wrong, drop the peer
		log.Debug("Propagated fast block verification failed", "peer", peer, "number", block.Number(), "hash", hash, "err", err)
		f.agentFetcher.ChangeCommitteeLeader(block.Number())
		f.dropPeer(peer, types.FetcherCall)
		return false
	}
	return true
}

// verifyComeAgreement verify consensus and insert block.
func (f *Fetcher) verifyComeAgreement(peer string, block *types.Block, signs []*types.PbftSign) {
	go func() {
		height := block.Number()
		inBlock := types.NewBlockWithHeader(block.Header()).WithBody(block.Transactions(), signs, block.SwitchInfos())
		find := f.insert(peer, inBlock)
		if !find {
			log.Debug("Failed insert fast block", "number", height, "sign number", len(signs), "result", find, "hash", block.Hash(), "peer", peer)
			return
		}
		log.Debug("Agreement insert fast block", "number", height, "sign number", len(signs), "result", find, "hash", block.Hash(), "peer", peer)

		// Invoke the testing hook if needed
		if f.importedHook != nil {
			f.importedHook(block)
		}

		f.doneConsensus <- height
	}()
}

// insert spawns a new goroutine to run a block insertion into the chain. If the
// block's number is at the same height as the current import phase, it updates
// the phase states accordingly.
func (f *Fetcher) insert(peer string, block *types.Block) bool {
	// Run the actual import and log any issues
	if _, err := f.insertChain(types.Blocks{block}); err != nil {
		log.Debug("Propagated fast block import failed", "peer", peer, "number", block.Number(), "hash", block.Hash(), "err", err)
		f.doneBlockSign <- &injectDone{block}
		return false
	}

	// If import succeeded, broadcast the block
	propAnnounceOutTimer.UpdateSince(block.ReceivedAt)
	go f.broadcastFastBlock(block, false)

	return true
}

// GetPendingBlock gets a block that is not inserted locally
func (f *Fetcher) GetPendingBlock(hash common.Hash) *types.Block {
	inject := f.getPendingBlock(hash)
	if inject != nil {
		return inject.block
	}
	return nil
}

func (f *Fetcher) getPendingBlock(hash common.Hash) *inject {
	f.blockMutex.Lock()
	defer f.blockMutex.Unlock()
	if _, ok := f.queued[hash]; !ok {
		return nil
	}

	return f.queued[hash]
}

func (f *Fetcher) setPendingBlock(hash common.Hash, op *inject) {
	f.blockMutex.Lock()
	defer f.blockMutex.Unlock()
	if _, ok := f.queued[hash]; !ok {
		f.queued[hash] = op
	}
}

// forgetHash removes all traces of a block announcement from the fetcher's
// internal state.
func (f *Fetcher) forgetHash(hash common.Hash) {
	// Remove all pending announces and decrement DOS counters
	for _, announce := range f.announced[hash] {
		f.announces[announce.origin]--
		if f.announces[announce.origin] == 0 {
			delete(f.announces, announce.origin)
		}
	}
	delete(f.announced, hash)
	if f.announceChangeHook != nil {
		f.announceChangeHook(hash, false)
	}
	// Remove any pending fetches and decrement the DOS counters
	if announce := f.fetching[hash]; announce != nil {
		f.announces[announce.origin]--
		if f.announces[announce.origin] == 0 {
			delete(f.announces, announce.origin)
		}
		delete(f.fetching, hash)
	}

	// Remove any pending completion requests and decrement the DOS counters
	for _, announce := range f.fetched[hash] {
		f.announces[announce.origin]--
		if f.announces[announce.origin] == 0 {
			delete(f.announces, announce.origin)
		}
	}
	delete(f.fetched, hash)

	// Remove any pending completions and decrement the DOS counters
	if announce := f.completing[hash]; announce != nil {
		f.announces[announce.origin]--
		if f.announces[announce.origin] == 0 {
			delete(f.announces, announce.origin)
		}
		delete(f.completing, hash)
	}
}

// forgetBlockHeight removes all traces of a queued block from the fetcher's internal
// state.
func (f *Fetcher) forgetBlockHeight(height *big.Int) {
	number := height.Uint64()
	log.Trace("Forget block height", "height", height, "block len", len(f.blockMultiHash[number]), "send", len(f.sendBlockHash[number]))
	if blockHashs, ok := f.blockMultiHash[number]; ok {
		for _, hash := range blockHashs {
			f.forgetHash(hash)
			f.forgetBlock(hash)
		}
	}
	delete(f.blockMultiHash, number)
	delete(f.sendBlockHash, number)
}

// forgetBlock removes all traces of a queued block from the fetcher's internal
// state.
func (f *Fetcher) forgetBlock(hash common.Hash) {
	f.blockMutex.Lock()
	defer f.blockMutex.Unlock()
	if insert := f.queued[hash]; insert != nil {
		f.queues[insert.origin]--
		if f.queues[insert.origin] == 0 {
			delete(f.queues, insert.origin)
		}
		delete(f.queued, hash)
	}
}

// agreeAtSameHeight judge whether consensus is reached at a certain height.
func (f *Fetcher) agreeAtSameHeight(signs []*types.PbftSign, blockHash common.Hash, committeeNumber int32) bool {
	voteCount := 0
	for _, sign := range signs {
		if sign.Result == types.VoteAgree && blockHash == sign.FastHash {
			voteCount++
		}

		if verifyCommitteesReachedTwoThirds(committeeNumber, int32(voteCount)) {
			return true
		}
	}
	return false
}

// verifyCommitteesReachedTwoThirds decide whether number reaches two-thirds of the committeeNumber.
func verifyCommitteesReachedTwoThirds(committeeNumber int32, number int32) bool {
	value := int32(committeeNumber*2/3) + 1
	if number >= value {
		return true
	}

	return false
}

// forgetBlockHeight removes all traces of a queued block from the fetcher's internal
// state.
func (f *Fetcher) forgetInvaildBlock(block *types.Block) {
	hash := block.Hash()
	number := block.NumberU64()
	f.forgetHash(hash)
	f.forgetBlock(hash)

	if hashs, ok := f.blockMultiHash[number]; ok {
		delete(f.blockMultiHash, number)
		for _, blockhash := range hashs {
			if blockhash != hash {
				f.blockMultiHash[number] = append(f.blockMultiHash[number], blockhash)
			}
		}
	}
	if hashs, ok := f.sendBlockHash[number]; ok {
		delete(f.sendBlockHash, number)
		for _, blockhash := range hashs {
			if blockhash != hash {
				f.sendBlockHash[number] = append(f.sendBlockHash[number], blockhash)
			}
		}
	}
}
