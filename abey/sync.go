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

package abey

import (
	"github.com/abeychain/go-abey/params"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/log"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/abey/downloader"
	dtype "github.com/abeychain/go-abey/abey/types"
	"github.com/abeychain/go-abey/p2p/enode"
)

const (
	forceSyncCycle      = 10 * time.Second // Time interval to force syncs, even if few peers are available
	minDesiredPeerCount = 5                // Amount of peers desired to start syncing

	// This is the target size for the packs of transactions sent by txsyncLoop.
	// A pack can get larger than this if a single transactions exceeds this size.
	txsyncPackSize    = 64 * 1024
	fruitsyncPackSize = 50 * 1024
)

type txsync struct {
	p   *peer
	txs []*types.Transaction
}

type fruitsync struct {
	p      *peer
	fruits []*types.SnailBlock
}

// syncTransactions starts sending all currently pending transactions to the given peer.
func (pm *ProtocolManager) syncTransactions(p *peer) {
	var txs types.Transactions
	pending, _ := pm.txpool.Pending()
	for _, batch := range pending {
		txs = append(txs, batch...)
	}
	log.Debug("syncTransactions", "pending", len(pending), "txs", len(txs))
	if len(txs) == 0 {
		return
	}
	select {
	case pm.txsyncCh <- &txsync{p, txs}:
	case <-pm.quitSync:
	}
}

// syncFruits starts sending all currently pending fruits to the given peer.
func (pm *ProtocolManager) syncFruits(p *peer) {
	var fruits types.SnailBlocks
	pending := pm.SnailPool.PendingFruits()
	for _, batch := range pending {
		fruits = append(fruits, batch)
	}
	log.Debug("syncFruits", "pending", len(pending), "fts", len(fruits))
	if len(fruits) == 0 {
		return
	}
	select {
	case pm.fruitsyncCh <- &fruitsync{p, fruits}:
	case <-pm.quitSync:
	}
}

// txsyncLoop takes care of the initial transaction sync for each new
// connection. When a new peer appears, we relay all currently pending
// transactions. In order to minimise egress bandwidth usage, we send
// the transactions in small packs to one peer at a time.
func (pm *ProtocolManager) txsyncLoop() {
	var (
		pending = make(map[enode.ID]*txsync)
		sending = false               // whether a send is active
		pack    = new(txsync)         // the pack that is being sent
		done    = make(chan error, 1) // result of the send
	)

	// send starts a sending a pack of transactions from the sync.
	send := func(s *txsync) {
		// Fill pack with transactions up to the target size.
		size := common.StorageSize(0)
		pack.p = s.p
		pack.txs = pack.txs[:0]
		for i := 0; i < len(s.txs) && size < txsyncPackSize; i++ {
			pack.txs = append(pack.txs, s.txs[i])
			size += s.txs[i].Size()
		}
		// Remove the transactions that will be sent.
		s.txs = s.txs[:copy(s.txs, s.txs[len(pack.txs):])]
		if len(s.txs) == 0 {
			delete(pending, s.p.ID())
		}
		// Send the pack in the background.
		s.p.Log().Debug("Sending batch of transactions", "count", len(pack.txs), "bytes", size)
		sending = true
		go func() { done <- pack.p.SendTransactions(pack.txs) }()
	}

	// pick chooses the next pending sync.
	pick := func() *txsync {
		if len(pending) == 0 {
			return nil
		}
		n := rand.Intn(len(pending)) + 1
		for _, s := range pending {
			if n--; n == 0 {
				return s
			}
		}
		return nil
	}

	for {
		select {
		case s := <-pm.txsyncCh:
			pending[s.p.ID()] = s
			if !sending {
				send(s)
			}
		case err := <-done:
			sending = false
			// Stop tracking peers that cause send failures.
			if err != nil {
				pack.p.Log().Debug("Transaction send failed", "err", err)
				delete(pending, pack.p.ID())
			}
			// Schedule the next send.
			if s := pick(); s != nil {
				send(s)
			}
		case <-pm.quitSync:
			return
		}
	}
}

// fruitsyncLoop takes care of the initial fruit sync for each new
// connection. When a new peer appears, we relay all currently pending
// fruits. In order to minimise egress bandwidth usage, we send
// the fruits in small packs to one peer at a time.
func (pm *ProtocolManager) fruitsyncLoop() {
	var (
		pending = make(map[enode.ID]*fruitsync)
		sending = false               // whether a send is active
		pack    = new(fruitsync)      // the pack that is being sent
		done    = make(chan error, 1) // result of the send
	)

	// send starts a sending a pack of fruits from the sync.
	send := func(f *fruitsync) {
		// Fill pack with fruits up to the target size.
		size := common.StorageSize(0)
		pack.p = f.p
		pack.fruits = pack.fruits[:0]
		for i := 0; i < len(f.fruits) && size < fruitsyncPackSize; i++ {
			pack.fruits = append(pack.fruits, f.fruits[i])
			size += f.fruits[i].Size()
		}
		// Remove the fruits that will be sent.
		f.fruits = f.fruits[:copy(f.fruits, f.fruits[len(pack.fruits):])]
		if len(f.fruits) == 0 {
			delete(pending, f.p.ID())
		}
		// Send the pack in the background.
		f.p.Log().Trace("Sending batch of fruits", "count", len(pack.fruits), "bytes", size)
		sending = true
		go func() { done <- pack.p.SendFruits(pack.fruits) }()
	}

	// pick chooses the next pending sync.
	pick := func() *fruitsync {
		if len(pending) == 0 {
			return nil
		}
		n := rand.Intn(len(pending)) + 1
		for _, f := range pending {
			if n--; n == 0 {
				return f
			}
		}
		return nil
	}

	for {
		select {
		case f := <-pm.fruitsyncCh:
			pending[f.p.ID()] = f
			if !sending {
				send(f)
			}
		case err := <-done:
			sending = false
			// Stop tracking peers that cause send failures.
			if err != nil {
				pack.p.Log().Debug("Fruits send failed", "err", err)
				delete(pending, pack.p.ID())
			}
			// Schedule the next send.
			if f := pick(); f != nil {
				send(f)
			}
		case <-pm.quitSync:
			return
		}
	}
}

// syncer is responsible for periodically synchronising with the network, both
// downloading hashes and blocks as well as handling the announcement handler.
func (pm *ProtocolManager) syncer() {
	// Start and ensure cleanup of sync mechanisms
	pm.fetcherFast.Start()
	pm.fetcherSnail.Start()
	defer pm.fetcherFast.Stop()
	defer pm.fetcherSnail.Stop()
	defer pm.downloader.Terminate()
	defer pm.fdownloader.Terminate()

	// Wait for different events to fire synchronisation operations
	forceSync := time.NewTicker(forceSyncCycle)
	defer forceSync.Stop()

	for {
		select {
		case <-pm.newPeerCh:
			// Make sure we have peers to select from, then sync
			if pm.peers.Len() < minDesiredPeerCount {
				break
			}
			go pm.synchronise(pm.peers.BestPeer())

		case <-forceSync.C:
			// Force a sync even if not enough peers are present
			go pm.synchronise(pm.peers.BestPeer())

		case <-pm.noMorePeers:
			return
		}

	}
}

// synchronise tries to sync up our local block chain with a remote peer.
func (pm *ProtocolManager) synchronise(peer *peer) {
	// Short circuit if no peers are available
	if !atomic.CompareAndSwapInt32(&pm.synchronising, 0, 1) {
		log.Debug("synchronise snail busy")
		return
	}
	defer atomic.StoreInt32(&pm.synchronising, 0)
	pm.lock.Lock()
	defer pm.lock.Unlock()
	defer log.Debug("synchronise exit")

	if peer == nil {
		log.Debug("synchronise peer nil")
		return
	}
	var err error
	sendEvent := func() {
		// reset on error
		if err != nil {
			pm.eventMux.Post(downloader.FailedEvent{Err: err})
		} else {
			pm.eventMux.Post(downloader.DoneEvent{})
		}
	}

	// Make sure the peer's TD is higher than our own
	currentBlock := pm.snailchain.CurrentBlock()
	td := pm.snailchain.GetTd(currentBlock.Hash(), currentBlock.NumberU64())
	pHeadHash, pTd := peer.Head()
	_, fastHeight := peer.fastHead, peer.fastHeight.Uint64()

	pm.fdownloader.SetSyncStatsChainHeightLast(fastHeight)
	currentNumber := pm.blockchain.CurrentBlock().NumberU64()
	log.Debug("synchronise  ", "remoteHeadHash", pHeadHash, "pTd", pTd, "td", td, "fastHeight",
		fastHeight, "currentNumber", currentNumber, "snailHeight", currentBlock.Number())

	// sync the fast blocks
	if pTd.Cmp(td) <= 0 || currentBlock.Number().Cmp(params.StopSnailMiner) >= 0 {
		if fastHeight > currentNumber {
			pm.eventMux.Post(downloader.StartEvent{})
			defer sendEvent()
			if err := pm.downloader.SyncFast(peer.id, pHeadHash, fastHeight, downloader.FullSync); err != nil {
				log.Error("ProtocolManager fast sync: ", "err", err)
				return
			}
			atomic.StoreUint32(&pm.fastSync, 0)
			atomic.StoreUint32(&pm.snapSync, 0)
			atomic.StoreUint32(&pm.acceptTxs, 1)    // Mark initial sync done
			atomic.StoreUint32(&pm.acceptFruits, 1) // Mark initial sync done on any fetcher import
		}
		return
	}

	// Otherwise try to sync with the downloader
	mode := downloader.FullSync
	if atomic.LoadUint32(&pm.fastSync) == 1 {
		// Fast sync was explicitly requested, and explicitly granted
		mode = downloader.FastSync

		//else if atomic.LoadUint32(&pm.snapSync) == 1 {
		//	mode = downloader.SnapShotSync
		//}
	}else if pm.blockchain.CurrentBlock().NumberU64() == 0 && pm.blockchain.CurrentFastBlock().NumberU64() > 0 {
		// The database  seems empty as the current block is the genesis. Yet the fast
		// block is ahead, so fast sync was enabled for this node at a certain point.
		// The only scenario where this can happen is if the user manually (or via a
		// bad block) rolled back a fast sync node below the sync point. In this case
		// however it's safe to reenable fast sync.
		atomic.StoreUint32(&pm.fastSync, 1)
		mode = downloader.FastSync

	}

	if mode == downloader.FastSync || mode == downloader.SnapShotSync {
		var pivotHeader *types.Header

		// Make sure the peer's total difficulty we are synchronizing is higher.
		if pm.snailchain.GetTdByHash(pm.snailchain.CurrentFastBlock().Hash()).Cmp(pTd) >= 0 {
			return
		}

		var err error
		pivotNumber := fastHeight - dtype.FsMinFullBlocks
		if pivotHeader, err = pm.fdownloader.FetchHeight(peer.id, pivotNumber); err != nil {
			log.Error("FetchHeight pivotHeader", "peer", peer.id, "pivotNumber", pivotNumber, "err", err)
			return
		}
		pm.downloader.SetHeader(pivotHeader)
		pm.fdownloader.SetHeader(pivotHeader)
	}

	pm.eventMux.Post(downloader.StartEvent{})
	defer sendEvent()
	log.Debug("ProtocolManager1","mode",mode)
	if params.StopSnailMiner.Cmp(currentBlock.Number()) > 0 {
		// Run the sync cycle, and disable fast sync if we've went past the pivot block
		if err = pm.downloader.Synchronise(peer.id, pHeadHash, pTd, mode); err != nil {
			log.Error("ProtocolManager end", "err", err)
			return
		}
		log.Debug("ProtocolManager2","mode",mode)
		if atomic.LoadUint32(&pm.fastSync) == 1 ||  atomic.LoadUint32(&pm.snapSync) == 1 {
			if pm.blockchain.CurrentBlock().NumberU64() == 0 && pm.blockchain.CurrentFastBlock().NumberU64() > 0 {
				if err := pm.downloader.SyncFast(peer.id, pHeadHash, fastHeight, downloader.FastSync); err != nil {
					log.Error("ProtocolManager fast sync: ", "err", err)
					return
				}
			}
		}

		atomic.StoreUint32(&pm.fastSync, 0)
		atomic.StoreUint32(&pm.snapSync, 0)
		atomic.StoreUint32(&pm.acceptTxs, 1)    // Mark initial sync done
		atomic.StoreUint32(&pm.acceptFruits, 1) // Mark initial sync done on any fetcher import
		//atomic.StoreUint32(&pm.acceptSnailBlocks, 1) // Mark initial sync done on any fetcher import
		if head := pm.snailchain.CurrentBlock(); head.NumberU64() > 0 {
			// We've completed a sync cycle, notify all peers of new state. This path is
			// essential in star-topology networks where a gateway node needs to notify
			// all its out-of-date peers of the availability of a new block. This failure
			// scenario will most often crop up in private and hackathon networks with
			// degenerate connectivity, but it should be healthy for the mainnet too to
			// more reliably update peers or the local TD state.
			go pm.BroadcastSnailBlock(head, false)
		}
		if head := pm.blockchain.CurrentBlock(); head.NumberU64() > 0 {
			// We've completed a sync cycle, notify all peers of new state. This path is
			// essential in star-topology networks where a gateway node needs to notify
			// all its out-of-date peers of the availability of a new block. This failure
			// scenario will most often crop up in private and hackathon networks with
			// degenerate connectivity, but it should be healthy for the mainnet too to
			// more reliably update peers or the local TD state.
			log.Debug("synchronise", "number", head.Number(), "sign", head.GetLeaderSign() != nil)
			go pm.BroadcastFastBlock(head, false)
		}
	}
}
