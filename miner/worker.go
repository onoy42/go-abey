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

package miner

import (
	"fmt"
	"math/big"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/abeychain/go-abey/abeydb"
	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/consensus"
	"github.com/abeychain/go-abey/core"
	"github.com/abeychain/go-abey/core/state"
	"github.com/abeychain/go-abey/core/types"

	//"github.com/abeychain/go-abey/core/vm"
	//"crypto/rand"
	chain "github.com/abeychain/go-abey/core/snailchain"
	"github.com/abeychain/go-abey/event"
	"github.com/abeychain/go-abey/log"
	"github.com/abeychain/go-abey/params"
	"gopkg.in/fatih/set.v0"
)

const (
	resultQueueSize  = 10
	miningLogAtDepth = 5

	// fruitChanSize is the size of channel listening to NewFruitsEvent.
	// The number is referenced from the size of snail pool.
	fruitChanSize = 4096
	// chainHeadChanSize is the size of channel listening to ChainHeadEvent.
	chainHeadChanSize = 64
	// chainSideChanSize is the size of channel listening to ChainSideEvent.
	chainSideChanSize     = 64
	fastchainHeadChanSize = 1024
)

var (
	pointerHashFresh = big.NewInt(7)
)

// Agent can register themself with the worker
type Agent interface {
	Work() chan<- *Work
	SetReturnCh(chan<- *Result)
	Stop()
	Start()
	GetHashRate() int64
}

// Work is the workers current environment and holds
// all of the current state information
type Work struct {
	config *params.ChainConfig
	signer types.Signer

	state     *state.StateDB // apply state changes here
	ancestors *set.Set       // ancestor set (used for checking uncle parent validity)
	family    *set.Set       // family set (used for checking uncle invalidity)
	uncles    *set.Set       // uncle set
	tcount    int            // tx count in cycle

	Block *types.SnailBlock // the new block

	header *types.SnailHeader
	fruits []*types.SnailBlock // for the fresh
	signs  []*types.PbftSign

	createdAt time.Time
}

//Result is for miner and get mined result
type Result struct {
	Work  *Work
	Block *types.SnailBlock
}

// worker is the main object which takes care of applying messages to the new state
type worker struct {
	config *params.ChainConfig
	engine consensus.Engine

	mu sync.Mutex

	// update loop
	mux *event.TypeMux

	fruitCh  chan types.NewFruitsEvent
	fruitSub event.Subscription // for fruit pool

	minedfruitCh  chan types.NewMinedFruitEvent
	minedfruitSub event.Subscription // for fruit pool

	fastchainEventCh  chan types.FastChainEvent
	fastchainEventSub event.Subscription //for fast block pool

	chainHeadCh  chan types.SnailChainHeadEvent
	chainHeadSub event.Subscription
	chainSideCh  chan types.SnailChainSideEvent
	chainSideSub event.Subscription
	wg           sync.WaitGroup

	agents map[Agent]struct{}
	recv   chan *Result

	abey      Backend
	chain     *chain.SnailBlockChain
	fastchain *core.BlockChain
	proc      core.SnailValidator
	chainDb   abeydb.Database

	coinbase  common.Address
	extra     []byte
	fruitOnly bool   // only miner fruit
	publickey []byte // for publickey

	currentMu sync.Mutex
	current   *Work

	snapshotMu    sync.RWMutex
	snapshotBlock *types.SnailBlock
	minedFruit    *types.SnailBlock //for addFruits delay to create a new list
	snapshotState *state.StateDB

	possibleUncles map[common.Hash]*types.SnailBlock

	unconfirmed *unconfirmedBlocks // set of locally mined blocks pending canonicalness confirmations

	// atomic status counters
	mining            int32
	atWork            int32
	atCommintNewWoker bool
	fastBlockNumber   *big.Int

	// mine fruit random
	fastBlockPool []*big.Int

	fruitPoolMap map[uint64]*types.SnailBlock
}

func newWorker(config *params.ChainConfig, engine consensus.Engine, coinbase common.Address, abey Backend, mux *event.TypeMux) *worker {
	worker := &worker{
		config:            config,
		engine:            engine,
		abey:              abey,
		mux:               mux,
		fruitCh:           make(chan types.NewFruitsEvent, fruitChanSize),
		fastchainEventCh:  make(chan types.FastChainEvent, fastchainHeadChanSize),
		chainHeadCh:       make(chan types.SnailChainHeadEvent, chainHeadChanSize),
		chainSideCh:       make(chan types.SnailChainSideEvent, chainSideChanSize),
		minedfruitCh:      make(chan types.NewMinedFruitEvent, fruitChanSize),
		chainDb:           abey.ChainDb(),
		recv:              make(chan *Result, resultQueueSize),
		chain:             abey.SnailBlockChain(),
		fastchain:         abey.BlockChain(),
		proc:              abey.SnailBlockChain().Validator(),
		possibleUncles:    make(map[common.Hash]*types.SnailBlock),
		coinbase:          coinbase,
		agents:            make(map[Agent]struct{}),
		unconfirmed:       newUnconfirmedBlocks(abey.SnailBlockChain(), miningLogAtDepth),
		fastBlockNumber:   big.NewInt(0),
		atCommintNewWoker: false,
		fruitPoolMap:      make(map[uint64]*types.SnailBlock),
	}
	// Subscribe events for blockchain
	worker.chainHeadSub = abey.SnailBlockChain().SubscribeChainHeadEvent(worker.chainHeadCh)
	worker.chainSideSub = abey.SnailBlockChain().SubscribeChainSideEvent(worker.chainSideCh)
	worker.minedfruitSub = abey.SnailBlockChain().SubscribeNewFruitEvent(worker.minedfruitCh)

	worker.fruitSub = abey.SnailPool().SubscribeNewFruitEvent(worker.fruitCh)
	worker.fastchainEventSub = worker.fastchain.SubscribeChainEvent(worker.fastchainEventCh)

	go worker.update()
	go worker.wait()

	if !worker.freezeMiner() {
		worker.commitNewWork()
	}

	return worker
}

func (w *worker) freezeMiner() bool {
	cur := w.chain.CurrentBlock().Number()
	if cur.Cmp(w.config.TIP9.SnailNumber) >= 0 {
		return true
	}
	return false
}

func (w *worker) setEtherbase(addr common.Address) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.coinbase = addr
}

func (w *worker) setElection(toElect bool, pubkey []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.publickey = make([]byte, len(pubkey))
	copy(w.publickey, pubkey)
}

func (w *worker) SetFruitOnly(FruitOnly bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.fruitOnly = FruitOnly
}

func (w *worker) setExtra(extra []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.extra = extra
}

func (w *worker) pending() (*types.Block, *state.StateDB) {
	if atomic.LoadInt32(&w.mining) == 0 {
		// return a snapshot to avoid contention on currentMu mutex
		w.snapshotMu.RLock()
		defer w.snapshotMu.RUnlock()
		//return self.snapshotBlock, self.snapshotState.Copy()
		return nil, nil
	}

	w.currentMu.Lock()
	defer w.currentMu.Unlock()
	return nil, nil
	//return self.current.Block, self.current.state.Copy()
}

func (w *worker) pendingSnail() (*types.SnailBlock, *state.StateDB) {
	if atomic.LoadInt32(&w.mining) == 0 {
		// return a snapshot to avoid contention on currentMu mutex
		w.snapshotMu.RLock()
		defer w.snapshotMu.RUnlock()
		return w.snapshotBlock, w.snapshotState.Copy()
	}

	w.currentMu.Lock()
	defer w.currentMu.Unlock()
	return w.current.Block, w.current.state.Copy()
}

func (w *worker) pendingBlock() *types.Block {
	if atomic.LoadInt32(&w.mining) == 0 {
		// return a snapshot to avoid contention on currentMu mutex
		w.snapshotMu.RLock()
		defer w.snapshotMu.RUnlock()
		return nil
	}

	w.currentMu.Lock()
	defer w.currentMu.Unlock()
	//return self.current.Block
	return nil
}

func (w *worker) pendingSnailBlock() *types.SnailBlock {
	if atomic.LoadInt32(&w.mining) == 0 {
		// return a snapshot to avoid contention on currentMu mutex
		w.snapshotMu.RLock()
		defer w.snapshotMu.RUnlock()
		return w.snapshotBlock
	}

	w.currentMu.Lock()
	defer w.currentMu.Unlock()

	return w.current.Block
}

func (self *worker) start() {
	self.mu.Lock()
	defer self.mu.Unlock()

	if !self.freezeMiner() {
		atomic.StoreInt32(&self.mining, 1)
		// spin up agents
		for agent := range self.agents {
			agent.Start()
		}
	}
}

func (w *worker) stop() {
	w.wg.Wait()

	w.mu.Lock()
	defer w.mu.Unlock()
	if atomic.LoadInt32(&w.mining) == 1 {
		for agent := range w.agents {
			agent.Stop()
		}
	}
	w.atCommintNewWoker = false
	atomic.StoreInt32(&w.mining, 0)
	atomic.StoreInt32(&w.atWork, 0)

}

func (w *worker) register(agent Agent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.agents[agent] = struct{}{}
	agent.SetReturnCh(w.recv)
}

func (w *worker) unregister(agent Agent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.agents, agent)
	agent.Stop()
}

func (w *worker) update() {
	//defer self.txsSub.Unsubscribe()
	defer w.chainHeadSub.Unsubscribe()
	defer w.chainSideSub.Unsubscribe()
	defer w.fastchainEventSub.Unsubscribe()
	defer w.fruitSub.Unsubscribe()
	defer w.minedfruitSub.Unsubscribe()

	for {
		if w.freezeMiner() {
			log.Info("freeze miner in update.....")
			return
		}
		// A real event arrived, process interesting content
		select {
		// Handle ChainHeadEvent
		case ev := <-w.chainHeadCh:
			if !w.atCommintNewWoker {
				log.Debug("star commit new work  chainHeadCh", "chain block number", ev.Block.Number())
				if atomic.LoadInt32(&w.mining) == 1 {
					w.commitNewWork()
				}
			} else {
				if atomic.LoadInt32(&w.mining) == 1 && !w.fruitOnly && len(w.current.Block.Fruits()) >= 60 {
					log.Info("stop the mining and start a new mine", "need stop mining block number ", w.current.Block.Number(), "get block ev number", ev.Block.Number())
					w.commitNewWork()
				}
			}

			// Handle ChainSideEvent
		case ev := <-w.chainSideCh:
			log.Debug("chain side", "number", ev.Block.Number(), "hash", ev.Block.Hash())
			if !w.atCommintNewWoker {
				log.Debug("star commit new work  chainHeadCh", "chain block number", ev.Block.Number())
				if atomic.LoadInt32(&w.mining) == 1 {
					w.commitNewWork()
				}
			}

		case ev := <-w.fruitCh:
			// if only fruit only not need care about fruit event
			if w.current.Block == nil || ev.Fruits[0].FastNumber() == nil {
				return
			}
			if (w.fruitOnly || len(w.current.Block.Fruits()) == 0) && (w.current.Block.FastNumber().Cmp(ev.Fruits[0].FastNumber()) == 0) {
				// after get the fruit event should star mining if have not mining
				log.Debug("star commit new work  fruitCh")

				if atomic.LoadInt32(&w.mining) == 1 {
					w.commitNewWork()
				}
			}
		case <-w.fastchainEventCh:
			if !w.atCommintNewWoker {
				log.Debug("star commit new work  fastchainEventCh")
				if atomic.LoadInt32(&w.mining) == 1 {
					w.commitNewWork()
				}
			}
		case <-w.minedfruitCh:
			if !w.atCommintNewWoker {
				log.Debug("star commit new work  minedfruitCh")
				if atomic.LoadInt32(&w.mining) == 1 {
					w.commitNewWork()
				}
			}
		case <-w.minedfruitSub.Err():
			return

		case <-w.fastchainEventSub.Err():

			return
		case <-w.fruitSub.Err():
			return
		case <-w.chainHeadSub.Err():
			return
		case <-w.chainSideSub.Err():
			return
		}
	}
}

func (w *worker) wait() {
	for {
		if w.freezeMiner() {
			log.Info("freeze miner in wait1.....")
			return
		}
		for result := range w.recv {
			atomic.AddInt32(&w.atWork, -1)

			if result == nil {
				continue
			}
			if w.freezeMiner() {
				log.Info("freeze miner in wait2.....")
				return
			}
			block := result.Block
			log.Debug("Worker get wait fond block or fruit")
			if block.IsFruit() {
				if block.FastNumber() == nil {
					// if it does't include a fast block signs, it's not a fruit
					continue
				}
				if block.FastNumber().Cmp(common.Big0) == 0 {
					continue
				}

				if w.minedFruit == nil {
					log.Info("🍒  mined fruit", "number", block.FastNumber(), "diff", block.FruitDifficulty(), "hash", block.Hash(), "signs", len(block.Signs()))
					var newFruits []*types.SnailBlock
					newFruits = append(newFruits, block)
					w.abey.SnailPool().AddRemoteFruits(newFruits, true)
					// store the mined fruit to woker.minedfruit
					w.minedFruit = types.CopyFruit(block)
				} else {
					if w.minedFruit.FastNumber().Cmp(block.FastNumber()) != 0 {

						log.Info("🍒  mined fruit", "number", block.FastNumber(), "diff", block.FruitDifficulty(), "hash", block.Hash(), "signs", len(block.Signs()))
						var newFruits []*types.SnailBlock
						newFruits = append(newFruits, block)
						w.abey.SnailPool().AddRemoteFruits(newFruits, true)
						// store the mined fruit to woker.minedfruit
						w.minedFruit = types.CopyFruit(block)
					}
				}

				// only have fast block not fruits we need commit new work
				if w.current.fruits == nil {
					w.atCommintNewWoker = false
					// post msg for commitnew work
					var (
						events []interface{}
					)
					events = append(events, types.NewMinedFruitEvent{Block: block})
					w.chain.PostChainEvents(events)
				}
			} else {
				if block.Fruits() == nil {
					w.atCommintNewWoker = false
					continue
				}

				fruits := block.Fruits()
				log.Info("+++++ mined block  ---  ", "block number", block.Number(), "fruits", len(fruits), "first", fruits[0].FastNumber(), "end", fruits[len(fruits)-1].FastNumber())

				stat, err := w.chain.WriteMinedCanonicalBlock(block)
				if err != nil {
					log.Error("Failed writing block to chain", "err", err)
					continue
				}

				// set flag
				w.atCommintNewWoker = false

				// Broadcast the block and announce chain insertion event
				w.mux.Post(types.NewMinedBlockEvent{Block: block})
				var (
					events []interface{}
				)
				events = append(events, types.SnailChainEvent{Block: block, Hash: block.Hash()})
				if stat == chain.CanonStatTy {
					events = append(events, types.SnailChainHeadEvent{Block: block})
				}
				events = append(events, types.NewMinedFruitEvent{Block: block})
				w.chain.PostChainEvents(events)

				// Insert the block into the set of pending ones to wait for confirmations
				w.unconfirmed.Insert(block.NumberU64(), block.Hash())

			}
		}
	}
}

// push sends a new work task to currently live miner agents.
func (w *worker) push(work *Work) {
	if w.freezeMiner() {
		log.Info("freeze miner in push.....")
		return
	}

	if atomic.LoadInt32(&w.mining) != 1 {
		w.atCommintNewWoker = false
		log.Info("miner was stop")
		return
	}

	for agent := range w.agents {
		atomic.AddInt32(&w.atWork, 1)
		if ch := agent.Work(); ch != nil {
			ch <- work
		}
	}
}

// makeCurrent creates a new environment for the current cycle.
func (w *worker) makeCurrent(parent *types.SnailBlock, header *types.SnailHeader) error {
	work := &Work{
		config:    w.config,
		signer:    types.NewTIP1Signer(w.config.ChainID),
		ancestors: set.New(),
		family:    set.New(),
		uncles:    set.New(),
		header:    header,
		createdAt: time.Now(),
	}

	// when 08 is processed ancestors contain 07 (quick block)
	for _, ancestor := range w.chain.GetBlocksFromHash(parent.Hash(), 7) {
		//TODO need add snail uncles 20180804
		work.family.Add(ancestor.Hash())
		work.ancestors.Add(ancestor.Hash())
	}

	// Keep track of transactions which return errors so they can be removed
	work.tcount = 0
	w.current = work

	return nil
}

func (w *worker) commitNewWork() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.currentMu.Lock()
	defer w.currentMu.Unlock()

	tstart := time.Now()
	parent := w.chain.CurrentBlock()
	w.atCommintNewWoker = true

	//can not start miner when  fruits and fast block
	tstamp := tstart.Unix()
	if parent.Time().Cmp(new(big.Int).SetInt64(tstamp)) >= 0 {
		tstamp = parent.Time().Int64() + 1
	}
	// this will ensure we're not going off too far in the future
	if now := time.Now().Unix(); tstamp > now+1 {
		wait := time.Duration(tstamp-now) * time.Second
		log.Info("Mining too far in the future", "wait", common.PrettyDuration(wait))
		time.Sleep(wait)
	}

	num := parent.Number()
	//TODO need add more struct member
	header := &types.SnailHeader{
		ParentHash: parent.Hash(),
		Publickey:  w.publickey,
		Number:     num.Add(num, common.Big1),
		Extra:      w.extra,
		Time:       big.NewInt(tstamp),
	}

	// Only set the coinbase if we are mining (avoid spurious block rewards)
	if atomic.LoadInt32(&w.mining) == 1 {
		header.Coinbase = w.coinbase
	}

	// Could potentially happen if starting to mine in an odd state.
	err := w.makeCurrent(parent, header)
	if err != nil {
		log.Error("Failed to create mining context", "err", err)
		w.atCommintNewWoker = false
		return
	}
	// Create the current work task and check any fork transitions needed
	work := w.current
	fruits := w.abey.SnailPool().PendingFruits()

	pendingFruits := w.CopyPendingFruit(fruits, w.chain)
	//for create a new fruits for worker
	//self.copyPendingFruit(fruits)
	w.CommitFastBlocksByWoker(pendingFruits, w.chain, w.fastchain, w.engine)

	// only miner fruit if not fruit set only miner the fruit
	if !w.fruitOnly {
		err := w.CommitFruits(pendingFruits, w.chain, w.fastchain, w.engine)
		if err != nil {
			log.Error("Failed to commit fruits", "err", err)
			return
		}
	}
	// check the fruits when it to be mining
	if work.fruits != nil {
		log.Debug("commitNewWork fruits", "first", work.fruits[0].FastNumber(), "last", work.fruits[len(work.fruits)-1].FastNumber())
		if count := len(work.fruits); count < params.MinimumFruits {
			work.fruits = nil
		} else if count > params.MaximumFruits {
			log.Info("commitNewWork fruits", "first", work.fruits[0].FastNumber(), "last", work.fruits[len(work.fruits)-1].FastNumber())
			work.fruits = work.fruits[:params.MaximumFruits]
		}

		// make sure the time
		if work.fruits != nil {
			if work.fruits[len(work.fruits)-1].Time() == nil || work.header.Time == nil || work.header.Time.Cmp(work.fruits[len(work.fruits)-1].Time()) < 0 {
				log.Error("validate time", "block.Time()", work.header.Time, "fruits[len(fruits)-1].Time()", work.fruits[len(work.fruits)-1].Time(), "block number", work.header.Number, "fruit fast number", work.fruits[len(work.fruits)-1].FastNumber())
				work.fruits = nil
			}
		}
	}

	// Set the pointerHash
	pointerNum := new(big.Int).Sub(parent.Number(), pointerHashFresh)
	if pointerNum.Cmp(common.Big0) < 0 {
		pointerNum = new(big.Int).Set(common.Big0)
	}
	pointer := w.chain.GetBlockByNumber(pointerNum.Uint64())
	header.PointerHash = pointer.Hash()
	header.PointerNumber = pointer.Number()

	if err := w.engine.PrepareSnail(w.fastchain, w.chain, header); err != nil {
		log.Error("Failed to prepare header for mining", "err", err)
		w.atCommintNewWoker = false
		return
	}

	// set work block
	work.Block = types.NewSnailBlock(
		w.current.header,
		w.current.fruits,
		w.current.signs,
		nil,
		w.current.config,
	)

	if w.current.Block.FastNumber().Cmp(big.NewInt(0)) == 0 && w.current.Block.Fruits() == nil {
		log.Debug("__commit new work have not fruits and fast block do not start miner  again")
		w.atCommintNewWoker = false
		return
	}

	// compute uncles for the new block.
	var (
		uncles    []*types.SnailHeader
		badUncles []common.Hash
	)
	for hash, uncle := range w.possibleUncles {
		if len(uncles) == 2 {
			break
		}
		if err := w.commitUncle(work, uncle.Header()); err != nil {
			log.Trace("Bad uncle found and will be removed", "hash", hash)
			log.Trace(fmt.Sprint(uncle))

			badUncles = append(badUncles, hash)
		} else {
			log.Debug("Committing new uncle to block", "hash", hash)
			uncles = append(uncles, uncle.Header())
		}
	}
	for _, hash := range badUncles {
		delete(w.possibleUncles, hash)
	}

	// Create the new block to seal with the consensus engine
	if work.Block, err = w.engine.FinalizeSnail(w.chain, header, uncles, work.fruits, work.signs); err != nil {
		log.Error("Failed to finalize block for sealing", "err", err)
		w.atCommintNewWoker = false
		return
	}

	// We only care about logging if we're actually mining.
	if atomic.LoadInt32(&w.mining) == 1 {
		log.Info("____Commit new mining work", "number", work.Block.Number(), "uncles", len(uncles), "fruits", len(work.Block.Fruits()), " fastblock", work.Block.FastNumber(), "diff", work.Block.BlockDifficulty(), "fdiff", work.Block.FruitDifficulty(), "elapsed", common.PrettyDuration(time.Since(tstart)))
		w.unconfirmed.Shift(work.Block.NumberU64() - 1)
	}
	work.Block.Time()
	w.push(work)
	w.updateSnapshot()
}

func (w *worker) commitUncle(work *Work, uncle *types.SnailHeader) error {
	hash := uncle.Hash()
	if work.uncles.Has(hash) {
		return fmt.Errorf("uncle not unique")
	}
	if !work.ancestors.Has(uncle.ParentHash) {
		return fmt.Errorf("uncle's parent unknown (%x)", uncle.ParentHash[0:4])
	}
	if work.family.Has(hash) {
		return fmt.Errorf("uncle already in family (%x)", hash)
	}
	work.uncles.Add(uncle.Hash())
	return nil
}

func (w *worker) updateSnapshot() {
	w.snapshotMu.Lock()
	defer w.snapshotMu.Unlock()

	w.snapshotBlock = types.NewSnailBlock(
		w.current.header,
		w.current.fruits,
		w.current.signs,
		nil,
		w.current.config,
	)

}

func (env *Work) commitFruit(fruit *types.SnailBlock, bc *chain.SnailBlockChain, engine consensus.Engine) error {

	err := engine.VerifyFreshness(bc, fruit.Header(), env.header.Number, true)
	if err != nil {
		log.Debug("commitFruit verify freshness error", "err", err, "fruit", fruit.FastNumber(), "pointer", fruit.PointNumber(), "block", env.header.Number)
		return err
	}

	return nil
}

// CommitFruits find all fruits and start to the last parent fruits number and end continue fruit list
func (w *worker) CommitFruits(fruits []*types.SnailBlock, bc *chain.SnailBlockChain, fc *core.BlockChain, engine consensus.Engine) error {
	var currentFastNumber *big.Int
	var fruitset []*types.SnailBlock

	rand.Seed(time.Now().UnixNano())

	parent := bc.CurrentBlock()
	fs := parent.Fruits()
	fastHight := fc.CurrentHeader().Number

	if len(fs) > 0 {
		currentFastNumber = fs[len(fs)-1].FastNumber()
	} else {
		// genesis block
		currentFastNumber = new(big.Int).Set(common.Big0)
	}

	if fruits == nil {
		return nil
	}

	log.Debug("commitFruits fruit pool list", "f min fb", fruits[0].FastNumber(), "f max fb", fruits[len(fruits)-1].FastNumber())

	// one commit the fruits len bigger then 50
	if len(fruits) >= params.MinimumFruits {

		currentFastNumber.Add(currentFastNumber, common.Big1)
		// find the continue fruits
		for _, fruit := range fruits {
			//find one equel currentFastNumber+1
			if rst := currentFastNumber.Cmp(fruit.FastNumber()); rst > 0 {
				// the fruit less then current fruit fb number so move to next
				continue
			} else if rst == 0 {
				err := w.current.commitFruit(fruit, bc, engine)
				if err == nil {
					if fruitset != nil {
						if fruitset[len(fruitset)-1].FastNumber().Uint64()+1 == fruit.FastNumber().Uint64() {
							fruitset = append(fruitset, fruit)
						} else {
							log.Info("there is not continue fruits", "fruitset[len(fruitset)-1].FastNumber()", fruitset[len(fruitset)-1].FastNumber(), "fruit.FastNumber()", fruit.FastNumber())
							break
						}
					} else {
						fruitset = append(fruitset, fruit)
					}
				} else {
					//need del the fruit
					log.Debug("commitFruits  remove unVerifyFreshness fruit", "fb num", fruit.FastNumber())
					w.abey.SnailPool().RemovePendingFruitByFastHash(fruit.FastHash())

					//post a event to start a new commitwork
					var (
						events []interface{}
					)
					events = append(events, types.NewMinedFruitEvent{Block: nil})
					w.chain.PostChainEvents(events)
					break
				}
			} else {
				break
			}
			currentFastNumber.Add(currentFastNumber, common.Big1)
		}
		if len(fruitset) >= params.MaximumFruits {
			w.current.fruits = fruitset
			return nil
		}
		if len(fruitset) >= 111 {
			// need add the time interval
			startFb := fc.GetHeaderByNumber(fruitset[0].FastNumber().Uint64())
			endFb := fc.GetHeaderByNumber(fruitset[len(fruitset)-1].FastNumber().Uint64())
			if startFb == nil || endFb == nil {
				return fmt.Errorf("the fast chain have not exist")
			}
			startTime := startFb.Time
			endTime := endFb.Time
			timeinterval := new(big.Int).Sub(endTime, startTime)

			unmineFruitLen := new(big.Int).Sub(fastHight, fruits[len(fruits)-1].FastNumber())
			waitmine := rand.Intn(900)
			tmp := big.NewInt(360) // upgrade for temporary
			if timeinterval.Cmp(tmp) >= 0 && (waitmine > int(unmineFruitLen.Int64())) {
				// must big then 5min
				w.current.fruits = fruitset
			} else {
				//mine fruit
				w.current.fruits = nil
			}

		}

	} else {
		// make the fruits to nil if not find the fruitset
		w.current.fruits = nil
	}
	return nil
}

//create a new list that maye add one fruit who just mined but not add in to pending list
// make sure not need mined the same fruit
func (w *worker) CopyPendingFruit(fruits map[common.Hash]*types.SnailBlock, bc *chain.SnailBlockChain) []*types.SnailBlock {

	// get current block the biggest fruit fastnumber
	snailblockFruits := bc.CurrentBlock().Fruits()
	snailFruitsLastFastNumber := new(big.Int).Set(common.Big0)
	if len(snailblockFruits) > 0 {
		snailFruitsLastFastNumber = snailblockFruits[len(snailblockFruits)-1].FastNumber()
	}

	// clean the map fisrt
	for k, _ := range w.fruitPoolMap {
		delete(w.fruitPoolMap, k)
	}

	var copyPendingFruits []*types.SnailBlock

	// del less then block fruits fast number fruit
	for _, v := range fruits {
		if v.FastNumber().Cmp(snailFruitsLastFastNumber) > 0 {
			copyPendingFruits = append(copyPendingFruits, v)
			w.fruitPoolMap[v.FastNumber().Uint64()] = v
		}
	}

	if w.minedFruit != nil {
		if w.minedFruit.FastNumber().Cmp(snailFruitsLastFastNumber) > 0 {
			if _, ok := fruits[w.minedFruit.FastHash()]; !ok {
				copyPendingFruits = append(copyPendingFruits, w.minedFruit)
				w.fruitPoolMap[w.minedFruit.FastNumber().Uint64()] = w.minedFruit
			}
		}
	}

	var blockby types.SnailBlockBy = types.FruitNumber
	blockby.Sort(copyPendingFruits)
	if len(fruits) > 0 && len(copyPendingFruits) > 0 {
		log.Debug("CopyPendingFruit pengding fruit info", "len of pengding", len(fruits), "sort copy fruits len", len(copyPendingFruits))
	}

	return copyPendingFruits

}

// find a corect fast block to miner
func (w *worker) commitFastNumber(fastBlockHight, snailFruitsLastFastNumber *big.Int, copyPendingFruits []*types.SnailBlock) *big.Int {

	if fastBlockHight.Cmp(snailFruitsLastFastNumber) <= 0 {
		return nil
	}

	log.Debug("--------commitFastBlocksByWoker Info", "snailFruitsLastFastNumber", snailFruitsLastFastNumber, "fastBlockHight", fastBlockHight)

	if copyPendingFruits == nil {
		return new(big.Int).Add(snailFruitsLastFastNumber, common.Big1)
	}

	log.Debug("--------commitFastBlocksByWoker Info2 ", "pendind fruit min fb", copyPendingFruits[0].FastNumber(), "max fb", copyPendingFruits[len(copyPendingFruits)-1].FastNumber())

	nextFruit := new(big.Int).Add(snailFruitsLastFastNumber, common.Big1)
	if copyPendingFruits[0].FastNumber().Cmp(nextFruit) > 0 {
		return nextFruit
	}
	// find the realy need miner fastblock
	for i, fb := range copyPendingFruits {
		//log.Info(" pending fruit fb num", fb.FastNumber())
		if i == len(copyPendingFruits)-1 {
			if fb.FastNumber().Cmp(fastBlockHight) < 0 {
				return new(big.Int).Add(fb.FastNumber(), common.Big1)
			}
			return nil
		} else if i == 0 {
			continue
		}

		//cmp
		if fb.FastNumber().Uint64()-1 > copyPendingFruits[i-1].FastNumber().Uint64() {
			//there have fruit need to miner 1 3 4 5,so need mine 2，or 1 5 6 7 need mine 2，3，4，5
			log.Debug("fruit fb number ", "fruits[i-1].FastNumber().Uint64()", copyPendingFruits[i-1].FastNumber(), "fb.FastNumber().Uint64()", fb.FastNumber())
			tempfruits := copyPendingFruits[i-1]
			if tempfruits.FastNumber().Cmp(fastBlockHight) < 0 {
				return new(big.Int).Add(tempfruits.FastNumber(), common.Big1)
			}

			return nil
		}
	}

	return nil
}

// find a corect fast block to miner
func (w *worker) commitFastNumberRandom(fastBlockHight, snailFruitsLastFastNumber *big.Int, copyPendingFruits []*types.SnailBlock) *big.Int {

	if fastBlockHight.Cmp(snailFruitsLastFastNumber) <= 0 {
		return nil
	}

	log.Debug("commitFastBlocksByWoker Info", "snailFruitsLastFastNumber", snailFruitsLastFastNumber, "fastBlockHight", fastBlockHight)

	log.Debug("the copyPendingFruits info", "len copyPendingFruits", len(copyPendingFruits), "the pool len", len(w.fastBlockPool))
	if len(copyPendingFruits) > 0 {
		log.Debug("the copyPendingFruits info", "len copyPendingFruits 1", copyPendingFruits[0].FastNumber(), "copyPendingFruits 2", copyPendingFruits[len(copyPendingFruits)-1].FastNumber())
	}
	//log.Info("---the info","len copyPendingFruits",len(copyPendingFruits),"the pool len",len(w.fastBlockPool))

	rand.Seed(time.Now().UnixNano())

	if len(w.fastBlockPool) > 0 {
		// del alread mined fastblock
		var pool []*big.Int
		for _, fb := range w.fastBlockPool {
			if _, ok := w.fruitPoolMap[fb.Uint64()]; !ok {
				if fb.Cmp(snailFruitsLastFastNumber) > 0 {
					pool = append(pool, fb)
				}
			}
		}
		w.fastBlockPool = pool
	}

	if len(w.fastBlockPool) == 0 {
		// find ten need mine fastblock
		if len(copyPendingFruits) > 0 {
			for i, fruit := range copyPendingFruits {
				// not care the frist
				if i == 0 {
					continue
				}
				n := int(new(big.Int).Sub(fruit.FastNumber(), copyPendingFruits[i-1].FastNumber()).Int64())
				if n == 1 {
					continue
				} else {
					for j := 1; j < n; j++ {
						temp := new(big.Int).Add(copyPendingFruits[i-1].FastNumber(), new(big.Int).SetInt64(int64(j)))
						w.fastBlockPool = append(w.fastBlockPool, temp)

						if len(w.fastBlockPool) >= 10 {
							break
						}
					}
					if len(w.fastBlockPool) >= 10 {
						break
					}
				}

			}
		}

		lenfbPool := len(w.fastBlockPool)
		if lenfbPool == 0 || (lenfbPool > 0 && lenfbPool < 10) {
			// need find from the
			var number int
			if len(copyPendingFruits) > 0 {
				number = int(new(big.Int).Sub(fastBlockHight, copyPendingFruits[len(copyPendingFruits)-1].FastNumber()).Int64())
				for i := 1; i <= 10-lenfbPool; i++ {
					if i > number {
						break
					}
					temp := new(big.Int).Add(copyPendingFruits[len(copyPendingFruits)-1].FastNumber(), new(big.Int).SetInt64(int64(i)))
					w.fastBlockPool = append(w.fastBlockPool, temp)
				}
			} else {
				number = int(new(big.Int).Sub(fastBlockHight, snailFruitsLastFastNumber).Int64())
				for i := 1; i <= 10-lenfbPool; i++ {
					if i > number {
						break
					}
					temp := new(big.Int).Add(snailFruitsLastFastNumber, new(big.Int).SetInt64(int64(i)))
					w.fastBlockPool = append(w.fastBlockPool, temp)
				}
			}

		}

	}

	if len(w.fastBlockPool) == 0 {
		return nil
	}

	//rand find one
	mineFastBlock := rand.Intn(len(w.fastBlockPool))

	log.Debug("need mine fruit info", "random one", mineFastBlock, "len pool", len(w.fastBlockPool), "begin", w.fastBlockPool[0], "end", w.fastBlockPool[len(w.fastBlockPool)-1])

	return w.fastBlockPool[mineFastBlock]
}

// find a corect fast block to miner
func (w *worker) CommitFastBlocksByWoker(fruits []*types.SnailBlock, bc *chain.SnailBlockChain, fc *core.BlockChain, engine consensus.Engine) error {
	//get current snailblock block and fruits
	// get the last fast number from the parent snail block
	snailblockFruits := bc.CurrentBlock().Fruits()
	snailFruitsLastFastNumber := new(big.Int).Set(common.Big0)
	if len(snailblockFruits) > 0 {
		snailFruitsLastFastNumber = snailblockFruits[len(snailblockFruits)-1].FastNumber()
	}

	//get current fast block hight
	fastBlockHight := fc.CurrentBlock().Number()

	fastNumber := w.commitFastNumberRandom(fastBlockHight, snailFruitsLastFastNumber, fruits)
	if fastNumber != nil {
		w.fastBlockNumber = fastNumber
		log.Debug("-------find the one", "fb number", w.fastBlockNumber)
		fbMined := fc.GetBlockByNumber(w.fastBlockNumber.Uint64())
		w.current.header.FastNumber = fbMined.Number()
		w.current.header.FastHash = fbMined.Hash()
		signs := fbMined.Signs()
		w.current.signs = make([]*types.PbftSign, len(signs))
		for i := range signs {
			w.current.signs[i] = types.CopyPbftSign(signs[i])
		}
	}
	return nil
}
