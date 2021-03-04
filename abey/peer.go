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
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/deckarep/golang-set"
	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/log"
	"github.com/abeychain/go-abey/rlp"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/p2p"
)

var (
	errClosed            = errors.New("peer set is closed")
	errAlreadyRegistered = errors.New("peer is already registered")
	errNotRegistered     = errors.New("peer is not registered")
	notHandle            = "not handled"
)

const (
	maxKnownTxs         = 163840 // Maximum transactions hashes to keep in the known list (prevent DOS) 32768 * 5
	maxKnownSigns       = 8192   // Maximum signs to keep in the known list
	maxKnownNodeInfo    = 2048   // Maximum node info to keep in the known list
	maxKnownFruits      = 16384  // Maximum fruits hashes to keep in the known list (prevent DOS)
	maxKnownSnailBlocks = 1024   // Maximum snailBlocks hashes to keep in the known list (prevent DOS)
	maxKnownFastBlocks  = 1024   // Maximum block hashes to keep in the known list (prevent DOS)

	// maxQueuedTxs is the maximum number of transaction lists to queue up before
	// dropping broadcasts. This is a sensitive number as a transaction list might
	// contain a single transaction, or thousands.
	maxQueuedTxs = 256
	// maxQueuedSigns is the maximum number of sign lists to queue up before
	// dropping broadcasts. This is a sensitive number as a transaction list might
	// contain a single transaction, or thousands.
	maxQueuedSigns = 128
	// contain a single transaction, or thousands.
	maxQueuedFruits     = 128
	maxQueuedSnailBlock = 4
	// maxQueuedProps is the maximum number of block propagations to queue up before
	// dropping broadcasts. There's not much point in queueing stale blocks, so a few
	// that might cover uncles should be enough.
	maxQueuedFastProps = 4

	// maxQueuedNodeInfo is the maximum number of node info propagations to queue up before
	// dropping broadcasts. There's not much point in queueing stale blocks, so a few
	// that might cover uncles should be enough.
	maxQueuedNodeInfo = 128

	maxQueuedNodeInfoHash = 256

	// maxQueuedAnns is the maximum number of block announcements to queue up before
	// dropping broadcasts. Similarly to block propagations, there's no point to queue
	// above some healthy uncle limit, so use that.
	maxQueuedFastAnns = 4

	// maxQueuedAnns is the maximum number of snail block announcements to queue up before
	// dropping broadcasts. Similarly to block propagations, there's no point to queue
	// above some healthy uncle limit, so use that.
	maxQueuedSnailAnns = 4

	maxQueuedDrop = 1

	handshakeTimeout = 5 * time.Second
)

// peerDropFn is a callback type for dropping a peer detected as malicious.
type peerDropFn func(id string, call uint32)

// PeerInfo represents a short summary of the Abeychain sub-protocol metadata known
// about a connected peer.
type PeerInfo struct {
	Version    int      `json:"version"`    // Abeychain protocol version negotiated
	Difficulty *big.Int `json:"difficulty"` // Total difficulty of the peer's blockchain
	Head       string   `json:"head"`       // SHA3 hash of the peer's best owned block
}

// propEvent is a fast block propagation, waiting for its turn in the broadcast queue.
type propEvent struct {
	block  *types.Block
	sblock *types.SnailBlock
	td     *big.Int
	fast   bool
}

// propEvent is a fast block propagation, waiting for its turn in the broadcast queue.
type propHashEvent struct {
	hash   common.Hash // Hash of one particular block being announced
	number uint64      // Number of one particular block being announced
	fast   bool
}

// dropPeerEvent is a snailBlock propagation, waiting for its turn in the broadcast queue.
type dropPeerEvent struct {
	id     string
	reason string
}

type peer struct {
	id string

	*p2p.Peer
	rw p2p.MsgReadWriter

	version int // Protocol version negotiated

	head         common.Hash
	fastHead     common.Hash
	td           *big.Int
	fastHeight   *big.Int
	gcHeight     *big.Int
	commitHeight *big.Int

	lock sync.RWMutex

	knownTxs           mapset.Set                     // Set of transaction hashes known to be known by this peer
	knownSign          mapset.Set                     // Set of sign  known to be known by this peer
	knownNodeInfos     mapset.Set                     // Set of node info  known to be known by this peer
	knownFruits        mapset.Set                     // Set of fruits hashes known to be known by this peer
	knownSnailBlocks   mapset.Set                     // Set of snailBlocks hashes known to be known by this peer
	knownFastBlocks    mapset.Set                     // Set of fast block hashes known to be known by this peer
	queuedTxs          chan []*types.Transaction      // Queue of transactions to broadcast to the peer
	queuedSign         chan []*types.PbftSign         // Queue of sign to broadcast to the peer
	queuedNodeInfo     chan *types.EncryptNodeMessage // a node info to broadcast to the peer
	queuedNodeInfoHash chan *types.EncryptNodeMessage // a node info to broadcast to the peer
	queuedFruits       chan []*types.SnailBlock       // Queue of fruits to broadcast to the peer
	queuedFastProps    chan *propEvent                // Queue of fast blocks to broadcast to the peer
	queuedSnailProps   chan *propEvent                // Queue of newSnailBlock to broadcast to the peer
	queuedFastAnns     chan *propHashEvent            // Queue of fastBlocks to announce to the peer
	queuedSnailAnns    chan *propHashEvent            // Queue of snailBlocks to announce to the peer

	term      chan struct{} // Termination channel to stop the broadcaster
	dropTx    uint64
	dropEvent chan *dropPeerEvent // Queue of drop error peer
	dropPeer  peerDropFn          // Drops a peer for misbehaving
}

func newPeer(version int, p *p2p.Peer, rw p2p.MsgReadWriter, dropPeer peerDropFn) *peer {
	return &peer{
		Peer:               p,
		rw:                 rw,
		version:            version,
		id:                 fmt.Sprintf("%x", p.ID().Bytes()[:8]),
		knownTxs:           mapset.NewSet(),
		knownSign:          mapset.NewSet(),
		knownNodeInfos:     mapset.NewSet(),
		knownFruits:        mapset.NewSet(),
		knownSnailBlocks:   mapset.NewSet(),
		knownFastBlocks:    mapset.NewSet(),
		queuedTxs:          make(chan []*types.Transaction, maxQueuedTxs),
		queuedSign:         make(chan []*types.PbftSign, maxQueuedSigns),
		queuedNodeInfo:     make(chan *types.EncryptNodeMessage, maxQueuedNodeInfo),
		queuedNodeInfoHash: make(chan *types.EncryptNodeMessage, maxQueuedNodeInfoHash),
		queuedFruits:       make(chan []*types.SnailBlock, maxQueuedFruits),
		queuedFastProps:    make(chan *propEvent, maxQueuedFastProps),
		queuedSnailProps:   make(chan *propEvent, maxQueuedSnailBlock),
		queuedFastAnns:     make(chan *propHashEvent, maxQueuedFastAnns),
		queuedSnailAnns:    make(chan *propHashEvent, maxQueuedSnailAnns),

		term:      make(chan struct{}),
		dropTx:    0,
		dropEvent: make(chan *dropPeerEvent, maxQueuedDrop),
		dropPeer:  dropPeer,
	}
}

// broadcast is a write loop that multiplexes block propagations, announcements
// and transaction broadcasts into the remote peer. The goal is to have an async
// writer that does not lock up node internals.
func (p *peer) broadcast() {
	for {
		select {
		case ctxs := <-p.queuedTxs:

			txs := []*types.Transaction{}
			for _, tx := range ctxs {
				txs = append(txs, tx)
			}

			for len(p.queuedTxs) > 1 && len(txs) < txPackSize {
				select {
				case event := <-p.queuedTxs:
					for _, tx := range event {
						txs = append(txs, tx)
					}
					log.Debug("broadcast", "queuedTxs", len(p.queuedTxs), "Txs", len(ctxs), "txs", len(txs))
				}
			}

			if len(txs) > txPackSize*3 {
				log.Debug("broadcast", "queuedTxs", len(p.queuedTxs), "Txs", len(ctxs), "txs", len(txs))
			}

			if err := p.SendTransactions(txs); err != nil {
				return
			}
			p.Log().Trace("Broadcast transactions", "count", len(txs))

			//add for sign
		case signs := <-p.queuedSign:
			p.Log().Trace("Broadcast sign", "signs", signs)

			//add for node info
		case nodeInfo := <-p.queuedNodeInfo:
			if err := p.SendNodeInfo(nodeInfo); err != nil {
				return
			}
			p.Log().Trace("Broadcast node info ")
		case nodeInfo := <-p.queuedNodeInfoHash:
			if err := p.SendNodeInfoHash(nodeInfo); err != nil {
				log.Info("SendNodeInfoHash error", "err", err)
			}
			p.Log().Trace("Broadcast node info hash")
		//add for fruit
		case fruits := <-p.queuedFruits:
			if len(fruits) > fruitPackSize*2 {
				log.Debug("broadcast", "queuedFruits", len(p.queuedFruits), "fxs", len(fruits))
			}
			if err := p.SendFruits(fruits); err != nil {
				return
			}
			p.Log().Trace("Broadcast fruits", "count", len(fruits))

		case snailBlock := <-p.queuedSnailProps:
			if err := p.SendNewBlock(nil, snailBlock.sblock, snailBlock.td); err != nil {
				p.Log().Debug("Propagated snailBlock success", "peer", p.RemoteAddr(), "number", snailBlock.sblock.Number(), "hash", snailBlock.sblock.Hash(), "td", snailBlock.td)
				return
			}
			p.Log().Trace("Propagated snailBlock", "number", snailBlock.sblock.Number(), "hash", snailBlock.sblock.Hash(), "td", snailBlock.td)

		case prop := <-p.queuedFastProps:
			if err := p.SendNewBlock(prop.block, nil, nil); err != nil {
				return
			}
			p.Log().Trace("Propagated fast block", "number", prop.block.Number(), "hash", prop.block.Hash())

		case block := <-p.queuedFastAnns:
			if err := p.SendNewFastBlockHashes([]common.Hash{block.hash}, []uint64{block.number}, block.fast); err != nil {
				return
			}
			p.Log().Trace("Announced fast block", "number", block.number, "hash", block.hash)
		case block := <-p.queuedSnailAnns:
			if err := p.SendNewFastBlockHashes([]common.Hash{block.hash}, []uint64{block.number}, block.fast); err != nil {
				p.Log().Info("Announced snail block", "number", block.number, "hash", block.hash, "err", err)
			}
			p.Log().Trace("Announced snail block", "number", block.number, "hash", block.hash)
		case event := <-p.dropEvent:
			log.Info("Drop peer", "id", event.id, "err", event.reason)
			p.dropPeer(event.id, types.PeerSendCall)
			return

		case <-p.term:
			return
		}
	}
}

// close signals the broadcast goroutine to terminate.
func (p *peer) close() {
	close(p.term)
}

// Info gathers and returns a collection of metadata known about a peer.
func (p *peer) Info() *PeerInfo {
	hash, td := p.Head()

	return &PeerInfo{
		Version:    p.version,
		Difficulty: td,
		Head:       hash.Hex(),
	}
}

// Head retrieves a copy of the current head hash and total difficulty of the
// peer.
func (p *peer) Head() (hash common.Hash, td *big.Int) {
	p.lock.RLock()
	defer p.lock.RUnlock()

	copy(hash[:], p.head[:])
	return hash, new(big.Int).Set(p.td)
}

// SetHead updates the head hash and total difficulty of the peer.
func (p *peer) SetHead(hash common.Hash, td *big.Int) {
	p.lock.Lock()
	defer p.lock.Unlock()

	copy(p.head[:], hash[:])
	p.td.Set(td)
}

// FastHeight retrieves a copy of the current fast height of the peer.
func (p *peer) FastHeight() (fastHeight *big.Int) {
	p.lock.RLock()
	defer p.lock.RUnlock()

	return new(big.Int).Set(p.fastHeight)
}

// SetFastHeight updates the fast height of the peer.
func (p *peer) SetFastHeight(fastHeight *big.Int) {
	p.lock.Lock()
	defer p.lock.Unlock()

	p.fastHeight.Set(fastHeight)
}

// MarkFastBlock marks a block as known for the peer, ensuring that the block will
// never be propagated to this particular peer.
func (p *peer) MarkFastBlock(hash common.Hash) {
	// If we reached the memory allowance, drop a previously known block hash
	for p.knownFastBlocks.Cardinality() >= maxKnownFastBlocks {
		p.knownFastBlocks.Pop()
	}
	p.knownFastBlocks.Add(hash)
}

// MarkTransaction marks a transaction as known for the peer, ensuring that it
// will never be propagated to this particular peer.
func (p *peer) MarkTransaction(hash common.Hash) {
	// If we reached the memory allowance, drop a previously known transaction hash
	for p.knownTxs.Cardinality() >= maxKnownTxs {
		p.knownTxs.Pop()
	}
	p.knownTxs.Add(hash)
}

// MarkSign marks a sign as known for the peer, ensuring that it
// will never be propagated to this particular peer.
func (p *peer) MarkSign(hash common.Hash) {
	// If we reached the memory allowance, drop a previously known sign hash
	for p.knownSign.Cardinality() >= maxKnownSigns {
		p.knownSign.Pop()
	}
	p.knownSign.Add(hash)
}

// MarkNodeInfo marks a node info as known for the peer, ensuring that it
// will never be propagated to this particular peer.
func (p *peer) MarkNodeInfo(hash common.Hash) {
	// If we reached the memory allowance, drop a previously known node info hash
	for p.knownNodeInfos.Cardinality() >= maxKnownNodeInfo {
		p.knownNodeInfos.Pop()
	}
	p.knownNodeInfos.Add(hash)
}

// MarkFruit marks a fruit as known for the peer, ensuring that it
// will never be propagated to this particular peer.
func (p *peer) MarkFruit(hash common.Hash) {
	// If we reached the memory allowance, drop a previously known transaction hash
	for p.knownFruits.Cardinality() >= maxKnownFruits {
		p.knownFruits.Pop()
	}
	p.knownFruits.Add(hash)
}

// MarkSnailBlock marks a snailBlock as known for the peer, ensuring that it
// will never be propagated to this particular peer.
func (p *peer) MarkSnailBlock(hash common.Hash) {
	// If we reached the memory allowance, drop a previously known transaction hash
	for p.knownSnailBlocks.Cardinality() >= maxKnownSnailBlocks {
		p.knownSnailBlocks.Pop()
	}
	p.knownSnailBlocks.Add(hash)
}

// SendTransactions sends transactions to the peer and includes the hashes
// in its transaction hash set for future reference.
func (p *peer) SendTransactions(txs types.Transactions) error {
	for _, tx := range txs {
		p.knownTxs.Add(tx.Hash())
	}
	return p.Send(TxMsg, txs)
}

// AsyncSendTransactions queues list of transactions propagation to a remote
// peer. If the peer's broadcast queue is full, the event is silently dropped.
func (p *peer) AsyncSendTransactions(txs []*types.Transaction) {
	select {
	case p.queuedTxs <- txs:
		log.Debug("AsyncSendTransactions", "queuedTxs", len(p.queuedTxs), "Txs", len(txs))
		for _, tx := range txs {
			p.knownTxs.Add(tx.Hash())
		}
	default:
		p.dropTx += uint64(len(txs))
		p.Log().Debug("Dropping transaction propagation", "count", len(txs), "size", txs[0].Size(), "dropTx", p.dropTx, "queuedTxs", len(p.queuedTxs), "peer", p.RemoteAddr())
	}
}

func (p *peer) AsyncSendSign(signs []*types.PbftSign) {
	select {
	case p.queuedSign <- signs:
		for _, sign := range signs {
			p.knownSign.Add(sign.Hash())
		}
	default:
		p.Log().Info("Dropping sign propagation")
	}
}

//SendNodeInfo sends node info to the peer and includes the hashes
// in its signs hash set for future reference.
func (p *peer) SendNodeInfo(nodeInfo *types.EncryptNodeMessage) error {
	p.knownNodeInfos.Add(nodeInfo.Hash())
	log.Trace("SendNodeInfo", "size", nodeInfo.Size(), "peer", p.id)
	return p.Send(TbftNodeInfoMsg, nodeInfo)
}

func (p *peer) SendNodeInfoHash(nodeInfo *types.EncryptNodeMessage) error {
	p.knownNodeInfos.Add(nodeInfo.Hash())
	log.Trace("SendNodeInfoHash", "peer", p.id)
	return p.Send(TbftNodeInfoHashMsg, &nodeInfoHashData{nodeInfo.Hash()})
}

func (p *peer) AsyncSendNodeInfo(nodeInfo *types.EncryptNodeMessage) {
	select {
	case p.queuedNodeInfo <- nodeInfo:
		p.knownNodeInfos.Add(nodeInfo.Hash())
	default:
		p.Log().Debug("Dropping nodeInfo propagation", "size", nodeInfo.Size(), "queuedNodeInfo", len(p.queuedNodeInfo), "peer", p.RemoteAddr())
	}
}

func (p *peer) AsyncSendNodeInfoHash(nodeInfo *types.EncryptNodeMessage) {
	select {
	case p.queuedNodeInfoHash <- nodeInfo:
		p.knownNodeInfos.Add(nodeInfo.Hash())
	default:
		p.Log().Debug("Dropping nodeInfoHash propagation", "queuedNodeInfoHash", len(p.queuedNodeInfoHash), "peer", p.RemoteAddr())
	}
}

//Sendfruits sends fruits to the peer and includes the hashes
// in its fruit hash set for future reference.
func (p *peer) SendFruits(fruits types.Fruits) error {
	for _, fruit := range fruits {
		p.knownFruits.Add(fruit.Hash())
	}
	log.Debug("SendFruits", "fts", len(fruits), "size", fruits[0].Size(), "peer", p.id)
	return p.Send(NewFruitMsg, fruits)
}

// AsyncSendFruits queues list of fruits propagation to a remote
// peer. If the peer's broadcast queue is full, the event is silently dropped.
func (p *peer) AsyncSendFruits(fruits []*types.SnailBlock) {
	select {
	case p.queuedFruits <- fruits:
		for _, fruit := range fruits {
			p.knownFruits.Add(fruit.Hash())
		}
	default:
		p.Log().Debug("Dropping fruits propagation", "size", fruits[0].Size(), "count", len(fruits), "queuedFruits", len(p.queuedFruits), "peer", p.RemoteAddr())
	}
}

// SendNewBlockHashes announces the availability of a number of blocks through
// a hash notification.
func (p *peer) SendNewFastBlockHashes(hashes []common.Hash, numbers []uint64, fast bool) error {
	if fast {
		for _, hash := range hashes {
			p.knownFastBlocks.Add(hash)
		}
		request := make(newBlockHashesData, len(hashes))
		for i := 0; i < len(hashes); i++ {
			request[i].Hash = hashes[i]
			request[i].Number = numbers[i]
		}
		return p.Send(NewFastBlockHashesMsg, request)
	} else {
		for _, hash := range hashes {
			p.knownSnailBlocks.Add(hash)
		}
		request := make(newBlockHashesData, len(hashes))
		for i := 0; i < len(hashes); i++ {
			request[i].Hash = hashes[i]
			request[i].Number = numbers[i]
		}
		return p.Send(NewSnailBlockHashesMsg, request)
	}
}

// AsyncSendNewBlockHash queues the availability of a fast block for propagation to a
// remote peer. If the peer's broadcast queue is full, the event is silently
// dropped.
func (p *peer) AsyncSendNewBlockHash(block *types.Block, snailBlock *types.SnailBlock, fast bool) {

	if fast {
		select {
		case p.queuedFastAnns <- &propHashEvent{hash: block.Hash(), number: block.NumberU64(), fast: fast}:
			p.knownFastBlocks.Add(block.Hash())
		default:
			p.Log().Debug("Dropping fast block announcement", "number", block.NumberU64(), "hash", block.Hash(), "queuedFastAnns", len(p.queuedFastAnns), "peer", p.RemoteAddr())
		}
	} else {
		select {
		case p.queuedSnailAnns <- &propHashEvent{hash: snailBlock.Hash(), number: snailBlock.NumberU64(), fast: fast}:
			p.knownSnailBlocks.Add(snailBlock.Hash())
		default:
			p.Log().Debug("Dropping snail block announcement", "number", snailBlock.NumberU64(), "hash", snailBlock.Hash(), "queuedSnailAnns", len(p.queuedSnailAnns), "peer", p.RemoteAddr())
		}
	}
}

// AsyncSendNewBlock queues an entire block for propagation to a remote peer. If
// the peer's broadcast queue is full, the event is silently dropped.
func (p *peer) AsyncSendNewBlock(block *types.Block, snailBlock *types.SnailBlock, td *big.Int, fast bool) {
	if fast {
		select {
		case p.queuedFastProps <- &propEvent{block: block, fast: fast}:
			p.knownFastBlocks.Add(block.Hash())
		default:
			p.Log().Debug("Dropping block propagation", "number", block.NumberU64(), "hash", block.Hash(), "queuedFastProps", len(p.queuedFastProps), "peer", p.RemoteAddr())
		}
	} else {
		select {
		case p.queuedSnailProps <- &propEvent{sblock: snailBlock, td: td, fast: fast}:
			p.knownSnailBlocks.Add(snailBlock.Hash())
		default:
			p.Log().Debug("Dropping snailBlock propagation", "number", snailBlock.NumberU64(), "hash", snailBlock.Hash(), "queuedSnailProps", len(p.queuedSnailProps), "peer", p.RemoteAddr())
		}
	}
}

func (p *peer) SendNewBlock(block *types.Block, snailBlock *types.SnailBlock, td *big.Int) error {
	if td != nil {
		p.knownSnailBlocks.Add(snailBlock.Hash())
		log.Debug("SendNewSnailBlock", "number", snailBlock.Number(), "td", td, "hash", snailBlock.Hash(), "size", snailBlock.Size(), "peer", p.id)
		return p.Send(NewSnailBlockMsg, &newBlockData{SnailBlock: []*types.SnailBlock{snailBlock}, TD: td})
	} else {
		p.knownFastBlocks.Add(block.Hash())
		log.Debug("SendNewFastBlock", "size", block.Size(), "peer", p.id)
		return p.Send(NewFastBlockMsg, &newBlockData{Block: []*types.Block{block}})
	}
}

// SendBlockHeaders sends a batch of block headers to the remote peer.
func (p *peer) SendBlockHeaders(headerData *BlockHeadersData, fast bool) error {
	if fast {
		return p.Send(FastBlockHeadersMsg, headerData)
	} else {
		return p.Send(SnailBlockHeadersMsg, headerData)
	}
}

// RequestOneFastHeader is a wrapper around the header query functions to fetch a
// single fast header. It is used solely by the fetcher fast.
func (p *peer) RequestOneSnailHeader(hash common.Hash) error {
	p.Log().Debug("Fetching single header", "hash", hash)
	return p.Send(GetSnailBlockHeadersMsg, &getBlockHeadersData{Origin: hashOrNumber{Hash: hash}, Amount: uint64(1), Skip: uint64(0), Reverse: false})
}

// SendBlockBodiesRLP sends a batch of block contents to the remote peer from
// an already RLP encoded format.
func (p *peer) SendBlockBodiesRLP(bodiesData *BlockBodiesRawData, fast bool) error {
	if fast {
		return p.Send(FastBlockBodiesMsg, bodiesData)
	} else {
		return p.Send(SnailBlockBodiesMsg, bodiesData)
	}
}

// SendNodeDataRLP sends a batch of arbitrary internal data, corresponding to the
// hashes requested.
func (p *peer) SendNodeData(data [][]byte) error {
	return p.Send(NodeDataMsg, data)
}

// SendReceiptsRLP sends a batch of transaction receipts, corresponding to the
// ones requested from an already RLP encoded format.
func (p *peer) SendReceiptsRLP(receipts []rlp.RawValue) error {
	return p.Send(ReceiptsMsg, receipts)
}

// RequestOneFastHeader is a wrapper around the header query functions to fetch a
// single fast header. It is used solely by the fetcher fast.
func (p *peer) RequestOneFastHeader(hash common.Hash) error {
	if strings.HasPrefix(hash.String(), "00") {
		p.Log().Info("Fetching single header  GetFastBlockHeadersMsg", "hash", hash)
	} else {
		p.Log().Debug("Fetching single header  GetFastBlockHeadersMsg", "hash", hash)
	}
	return p.Send(GetFastBlockHeadersMsg, &getBlockHeadersData{Origin: hashOrNumber{Hash: hash}, Amount: uint64(1), Skip: uint64(0), Reverse: false, Call: types.FetcherCall})
}

// RequestHeadersByHash fetches a batch of blocks' headers corresponding to the
// specified header query, based on the hash of an origin block.
func (p *peer) RequestHeadersByHash(origin common.Hash, amount int, skip int, reverse bool, isFastchain bool) error {
	if isFastchain {
		if strings.HasPrefix(origin.String(), "00") {
			p.Log().Info("Fetching batch of headers  GetFastOneBlockHeadersMsg", "count", amount, "fromhash", origin, "skip", skip, "reverse", reverse)
		} else {
			p.Log().Debug("Fetching batch of headers  GetFastOneBlockHeadersMsg", "count", amount, "fromhash", origin, "skip", skip, "reverse", reverse)
		}
		return p.Send(GetFastBlockHeadersMsg, &getBlockHeadersData{Origin: hashOrNumber{Hash: origin}, Amount: uint64(amount), Skip: uint64(skip), Reverse: reverse, Call: types.DownloaderCall})
	}
	p.Log().Debug("Fetching batch of headers  GetSnailBlockHeadersMsg", "count", amount, "fromhash", origin, "skip", skip, "reverse", reverse)
	return p.Send(GetSnailBlockHeadersMsg, &getBlockHeadersData{Origin: hashOrNumber{Hash: origin}, Amount: uint64(amount), Skip: uint64(skip), Reverse: reverse, Call: types.DownloaderCall})
}

// RequestHeadersByNumber fetches a batch of blocks' headers corresponding to the
// specified header query, based on the number of an origin block.
func (p *peer) RequestHeadersByNumber(origin uint64, amount int, skip int, reverse bool, isFastchain bool) error {

	if isFastchain {
		return p.Send(GetFastBlockHeadersMsg, &getBlockHeadersData{Origin: hashOrNumber{Number: origin}, Amount: uint64(amount), Skip: uint64(skip), Reverse: reverse, Call: types.DownloaderCall})
	}
	p.Log().Debug("Fetching batch of headers  GetSnailBlockHeadersMsg number", "count", amount, "fromhash", origin, "skip", skip, "reverse", reverse)
	return p.Send(GetSnailBlockHeadersMsg, &getBlockHeadersData{Origin: hashOrNumber{Number: origin}, Amount: uint64(amount), Skip: uint64(skip), Reverse: reverse, Call: types.DownloaderCall})

}

// RequestBodies fetches a batch of blocks' bodies corresponding to the hashes
// specified.
func (p *peer) RequestBodies(hashes []common.Hash, isFastchain bool, call uint32) error {
	datas := make([]getBlockBodiesData, len(hashes))
	for _, hash := range hashes {
		datas = append(datas, getBlockBodiesData{hash, call})
	}

	if isFastchain {
		p.Log().Debug("Fetching batch of block bodies  GetFastBlockBodiesMsg", "count", len(hashes))
		return p.Send(GetFastBlockBodiesMsg, datas)
	}
	p.Log().Debug("Fetching batch of block bodies  GetSnailBlockBodiesMsg", "count", len(hashes))
	return p.Send(GetSnailBlockBodiesMsg, datas)
}

// RequestNodeData fetches a batch of arbitrary data from a node's known state
// data, corresponding to the specified hashes.
func (p *peer) RequestNodeData(hashes []common.Hash, isFastchain bool) error {

	p.Log().Debug("Fetching batch of state data  GetNodeDataMsg", "count", len(hashes))
	return p.Send(GetNodeDataMsg, hashes)
}

// RequestReceipts fetches a batch of transaction receipts from a remote node.
func (p *peer) RequestReceipts(hashes []common.Hash, isFastchain bool) error {
	p.Log().Debug("Fetching batch of receipts  GetReceiptsMsg", "count", len(hashes))
	return p.Send(GetReceiptsMsg, hashes)
}

func (p *peer) Send(msgcode uint64, data interface{}) error {
	err := p2p.Send(p.rw, msgcode, data)

	if err != nil && !strings.Contains(err.Error(), notHandle) {
		select {
		case p.dropEvent <- &dropPeerEvent{p.id, err.Error()}:
		default:
			p.Log().Info("Dropping Send propagation", "peer", p.id, "err", err)
		}
	}
	return err
}

// Handshake executes the abey protocol handshake, negotiating version number,
// network IDs, difficulties, head and genesis blocks.
func (p *peer) Handshake(network uint64, td *big.Int, head common.Hash, genesis common.Hash, fastHead common.Hash, fastHeight *big.Int) error {
	// Send out own handshake in a new thread
	errc := make(chan error, 2)
	var status statusData // safe to read after two values have been received from errc

	go func() {
		errc <- p.Send(StatusMsg, &statusData{
			ProtocolVersion:  uint32(p.version),
			NetworkId:        network,
			TD:               td,
			FastHeight:       fastHeight,
			CurrentBlock:     head,
			GenesisBlock:     genesis,
			CurrentFastBlock: fastHead,
		})
	}()
	go func() {
		errc <- p.readStatus(network, &status, genesis)
	}()
	timeout := time.NewTimer(handshakeTimeout)
	defer timeout.Stop()
	for i := 0; i < 2; i++ {
		select {
		case err := <-errc:
			if err != nil {
				return err
			}
		case <-timeout.C:
			return p2p.DiscReadTimeout
		}
	}
	p.td, p.head, p.fastHeight = status.TD, status.CurrentBlock, status.FastHeight
	return nil
}

func (p *peer) readStatus(network uint64, status *statusData, genesis common.Hash) (err error) {
	msg, err := p.rw.ReadMsg()
	if err != nil {
		return err
	}
	if msg.Code != StatusMsg {
		return errResp(ErrNoStatusMsg, "first msg has code %x (!= %x)", msg.Code, StatusMsg)
	}
	if msg.Size > ProtocolMaxMsgSize {
		return errResp(ErrMsgTooLarge, "%v > %v", msg.Size, ProtocolMaxMsgSize)
	}
	// Decode the handshake and make sure everything matches
	if err := msg.Decode(&status); err != nil {
		return errResp(ErrDecode, "msg %v: %v", msg, err)
	}
	if status.GenesisBlock != genesis {
		return errResp(ErrGenesisBlockMismatch, "%x (!= %x)", status.GenesisBlock[:8], genesis[:8])
	}
	if status.NetworkId != network {
		return errResp(ErrNetworkIdMismatch, "%d (!= %d)", status.NetworkId, network)
	}
	if int(status.ProtocolVersion) != p.version {
		return errResp(ErrProtocolVersionMismatch, "%d (!= %d)", status.ProtocolVersion, p.version)
	}
	return nil
}

// Handshake executes the abey protocol handshake, negotiating version number,
// network IDs, difficulties, head and genesis blocks.
func (p *peer) SnapHandshake(network uint64, td *big.Int, head common.Hash, genesis common.Hash, fastHead common.Hash, fastHeight *big.Int, gcHeight *big.Int, commitHeight *big.Int) error {
	// Send out own handshake in a new thread
	errc := make(chan error, 2)
	var status statusSnapData // safe to read after two values have been received from errc

	go func() {
		errc <- p.Send(StatusMsg, &statusSnapData{
			ProtocolVersion:  uint32(p.version),
			NetworkId:        network,
			TD:               td,
			FastHeight:       fastHeight,
			CurrentBlock:     head,
			GenesisBlock:     genesis,
			CurrentFastBlock: fastHead,
			GcHeight:         gcHeight,
			CommitHeight:     commitHeight,
		})
	}()
	go func() {
		errc <- p.readSnapStatus(network, &status, genesis)
	}()
	timeout := time.NewTimer(handshakeTimeout)
	defer timeout.Stop()
	for i := 0; i < 2; i++ {
		select {
		case err := <-errc:
			if err != nil {
				return err
			}
		case <-timeout.C:
			return p2p.DiscReadTimeout
		}
	}
	p.td, p.head, p.fastHeight, p.gcHeight, p.commitHeight = status.TD, status.CurrentBlock, status.FastHeight, status.GcHeight, status.CommitHeight
	return nil
}

func (p *peer) readSnapStatus(network uint64, status *statusSnapData, genesis common.Hash) (err error) {
	msg, err := p.rw.ReadMsg()
	if err != nil {
		return err
	}
	if msg.Code != StatusMsg {
		return errResp(ErrNoStatusMsg, "first msg has code %x (!= %x)", msg.Code, StatusMsg)
	}
	if msg.Size > ProtocolMaxMsgSize {
		return errResp(ErrMsgTooLarge, "%v > %v", msg.Size, ProtocolMaxMsgSize)
	}
	// Decode the handshake and make sure everything matches
	if err := msg.Decode(&status); err != nil {
		return errResp(ErrDecode, "msg %v: %v", msg, err)
	}
	if status.GenesisBlock != genesis {
		return errResp(ErrGenesisBlockMismatch, "%x (!= %x)", status.GenesisBlock[:8], genesis[:8])
	}
	if status.NetworkId != network {
		return errResp(ErrNetworkIdMismatch, "%d (!= %d)", status.NetworkId, network)
	}
	if int(status.ProtocolVersion) != p.version {
		return errResp(ErrProtocolVersionMismatch, "%d (!= %d)", status.ProtocolVersion, p.version)
	}
	return nil
}

// String implements fmt.Stringer.
func (p *peer) String() string {
	return fmt.Sprintf("Peer %s [%s]", p.id,
		fmt.Sprintf("abey/%2d", p.version),
	)
}

// peerSet represents the collection of active peers currently participating in
// the Abeychain sub-protocol.
type peerSet struct {
	peers  map[string]*peer
	lock   sync.RWMutex
	closed bool
}

// newPeerSet creates a new peer set to track the active participants.
func newPeerSet() *peerSet {
	return &peerSet{
		peers: make(map[string]*peer),
	}
}

// Register injects a new peer into the working set, or returns an error if the
// peer is already known. If a new peer it registered, its broadcast loop is also
// started.
func (ps *peerSet) Register(p *peer) error {
	ps.lock.Lock()
	defer ps.lock.Unlock()

	if ps.closed {
		return errClosed
	}
	if _, ok := ps.peers[p.id]; ok {
		return errAlreadyRegistered
	}
	ps.peers[p.id] = p
	go p.broadcast()

	return nil
}

// Unregister removes a remote peer from the active set, disabling any further
// actions to/from that particular entity.
func (ps *peerSet) Unregister(id string) error {
	ps.lock.Lock()
	defer ps.lock.Unlock()

	p, ok := ps.peers[id]
	if !ok {
		return errNotRegistered
	}
	delete(ps.peers, id)
	p.close()

	return nil
}

// Peer retrieves the registered peer with the given id.
func (ps *peerSet) Peer(id string) *peer {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	return ps.peers[id]
}

// Len returns if the current number of peers in the set.
func (ps *peerSet) Len() int {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	return len(ps.peers)
}

// PeersWithoutBlock retrieves a list of peers that do not have a given block in
// their set of known hashes.
func (ps *peerSet) PeersWithoutFastBlock(hash common.Hash) []*peer {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	list := make([]*peer, 0, len(ps.peers))
	for _, p := range ps.peers {
		if !p.knownFastBlocks.Contains(hash) {
			list = append(list, p)
		}
	}
	return list
}

// PeersWithoutSign retrieves a list of peers that do not have a given sign
// in their set of known hashes.
func (ps *peerSet) PeersWithoutSign(hash common.Hash) []*peer {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	list := make([]*peer, 0, len(ps.peers))
	for _, p := range ps.peers {
		if !p.knownSign.Contains(hash) {
			list = append(list, p)
		}
	}
	return list
}

// PeersWithoutNodeInfo retrieves a list of peers that do not have a given node info
// in their set of known hashes.
func (ps *peerSet) PeersWithoutNodeInfo(hash common.Hash) []*peer {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	list := make([]*peer, 0, len(ps.peers))
	for _, p := range ps.peers {
		if !p.knownNodeInfos.Contains(hash) {
			list = append(list, p)
		}
	}
	return list
}

// PeersWithoutTx retrieves a list of peers that do not have a given transaction
// in their set of known hashes.
func (ps *peerSet) PeersWithoutTx(hash common.Hash) []*peer {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	list := make([]*peer, 0, len(ps.peers))
	for _, p := range ps.peers {
		if !p.knownTxs.Contains(hash) {
			list = append(list, p)
		}
	}
	return list
}

// PeersWithoutFruit retrieves a list of peers that do not have a given fruits
// in their set of known hashes.
func (ps *peerSet) PeersWithoutFruit(hash common.Hash) []*peer {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	list := make([]*peer, 0, len(ps.peers))
	for _, p := range ps.peers {
		if !p.knownFruits.Contains(hash) {
			list = append(list, p)
		}
	}
	return list
}

func (ps *peerSet) PeersWithoutSnailBlock(hash common.Hash) []*peer {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	list := make([]*peer, 0, len(ps.peers))
	for _, p := range ps.peers {
		if !p.knownSnailBlocks.Contains(hash) {
			list = append(list, p)
		}
	}
	return list
}

// BestPeer retrieves the known peer with the currently highest total difficulty.
func (ps *peerSet) BestPeer() *peer {
	ps.lock.RLock()
	defer ps.lock.RUnlock()

	var (
		bestPeer *peer
		bestTd   *big.Int
	)
	for _, p := range ps.peers {
		if _, td := p.Head(); bestPeer == nil || td.Cmp(bestTd) > 0 {
			bestPeer, bestTd = p, td
		}
	}
	return bestPeer
}

// Close disconnects all peers.
// No new peers can be registered after Close has returned.
func (ps *peerSet) Close() {
	ps.lock.Lock()
	defer ps.lock.Unlock()

	for _, p := range ps.peers {
		p.Disconnect(p2p.DiscQuitting)
	}
	ps.closed = true
}
