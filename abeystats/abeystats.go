// Copyright 2016 The go-ethereum Authors
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

// Package abeystats implements the network stats reporting service.
package abeystats

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"encoding/json"
	"github.com/abeychain/go-abey/abey"
	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/common/mclock"
	"github.com/abeychain/go-abey/consensus"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/event"
	"github.com/abeychain/go-abey/les"
	"github.com/abeychain/go-abey/log"
	"github.com/abeychain/go-abey/p2p"
	"github.com/abeychain/go-abey/rpc"
	"golang.org/x/net/websocket"
)

const (
	// historyUpdateRange is the number of blocks a node should report upon login or
	// history request.
	historyUpdateRange = 50

	snailHistoryUpdateRange = 50

	// txChanSize is the size of channel listening to NewTxsEvent.
	// The number is referenced from the size of tx pool.
	txChanSize = 4096

	// chainFastHeadChanSize is the size of channel listening to ChainHeadEvent.
	chainHeadChanSize = 4096

	// chainSnailHeadChanSize is the size of channel listening to SnailChainHeadEvent.
	chainSnailHeadChanSize = 128
)

type txPool interface {
	// SubscribeNewTxsEvent should return an event subscription of
	// NewTxsEvent and send events to the given channel.
	SubscribeNewTxsEvent(chan<- types.NewTxsEvent) event.Subscription
}

type blockChain interface {
	SubscribeChainHeadEvent(ch chan<- types.FastChainHeadEvent) event.Subscription
}

type snailBlockChain interface {
	SubscribeChainHeadEvent(ch chan<- types.SnailChainHeadEvent) event.Subscription
}

// Service implements an Abeychain netstats reporting daemon that pushes local
// chain statistics up to a monitoring server.
type Service struct {
	server *p2p.Server      // Peer-to-peer server to retrieve networking infos
	abey   *abey.Abeychain  // Full Abeychain service if monitoring a full node
	les    *les.LightAbey   // Light Abeychain service if monitoring a light node
	engine consensus.Engine // Consensus engine to retrieve variadic block fields

	node string // Name of the node to display on the monitoring page
	pass string // Password to authorize access to the monitoring page
	host string // Remote address of the monitoring service

	pongCh      chan struct{} // Pong notifications are fed into this channel
	histCh      chan []uint64 // History request block numbers are fed into this channel
	snailHistCh chan []uint64 // History request snailBlock numbers are fed into this channel
}

// New returns a monitoring service ready for stats reporting.
func New(url string, ethServ *abey.Abeychain, lesServ *les.LightAbey) (*Service, error) {
	// Parse the netstats connection url
	re := regexp.MustCompile("([^:@]*)(:([^@]*))?@(.+)")
	parts := re.FindStringSubmatch(url)
	if len(parts) != 5 {
		return nil, fmt.Errorf("invalid netstats url: \"%s\", should be nodename:secret@host:port", url)
	}
	// Assemble and return the stats service
	var engine consensus.Engine
	if ethServ != nil {
		engine = ethServ.Engine()
	} else {
		engine = lesServ.Engine()
	}
	return &Service{
		abey:        ethServ,
		les:         lesServ,
		engine:      engine,
		node:        parts[1],
		pass:        parts[3],
		host:        parts[4],
		pongCh:      make(chan struct{}),
		histCh:      make(chan []uint64, 1),
		snailHistCh: make(chan []uint64, 1),
	}, nil
}

// Protocols implements node.Service, returning the P2P network protocols used
// by the stats service (nil as it doesn't use the devp2p overlay network).
func (s *Service) Protocols() []p2p.Protocol { return nil }

// APIs implements node.Service, returning the RPC API endpoints provided by the
// stats service (nil as it doesn't provide any user callable APIs).
func (s *Service) APIs() []rpc.API { return nil }

// Start implements node.Service, starting up the monitoring and reporting daemon.
func (s *Service) Start(server *p2p.Server) error {
	s.server = server
	go s.loop()

	log.Info("Stats daemon started")
	return nil
}

// Stop implements node.Service, terminating the monitoring and reporting daemon.
func (s *Service) Stop() error {
	log.Info("Stats daemon stopped")
	return nil
}

// loop keeps trying to connect to the netstats server, reporting chain events
// until termination.
func (s *Service) loop() {
	// Subscribe to chain events to execute updates on
	var blockchain blockChain
	var txpool txPool
	var snailBlockChain snailBlockChain
	var snailheadSub event.Subscription

	if s.abey != nil {
		blockchain = s.abey.BlockChain()
		txpool = s.abey.TxPool()
		snailBlockChain = s.abey.SnailBlockChain()
	} else {
		blockchain = s.les.BlockChain()
		txpool = s.les.TxPool()
	}
	//fastBlock
	chainHeadCh := make(chan types.FastChainHeadEvent, chainHeadChanSize)
	headSub := blockchain.SubscribeChainHeadEvent(chainHeadCh)
	defer headSub.Unsubscribe()

	//tx
	txEventCh := make(chan types.NewTxsEvent, txChanSize)
	txSub := txpool.SubscribeNewTxsEvent(txEventCh)
	defer txSub.Unsubscribe()

	chainsnailHeadCh := make(chan types.SnailChainHeadEvent, chainSnailHeadChanSize)
	//snailBlock
	if s.abey != nil {
		snailheadSub = snailBlockChain.SubscribeChainHeadEvent(chainsnailHeadCh)
		defer snailheadSub.Unsubscribe()
	}

	//fruit
	/*chainFruitCh := make(chan types.NewMinedFruitEvent, chainSnailHeadChanSize)
	fruitSub := snailBlockChain.SubscribeNewFruitEvent(chainFruitCh)
	defer fruitSub.Unsubscribe()*/

	// Start a goroutine that exhausts the subsciptions to avoid events piling up
	var (
		quitCh      = make(chan struct{})
		headCh      = make(chan *types.Block, 1)
		snailHeadCh = make(chan *types.SnailBlock, 1)
		txCh        = make(chan struct{}, 1)
	)
	go func() {
		var lastTx mclock.AbsTime

	HandleLoop:
		for {
			select {
			// Notify of chain head events, but drop if too frequent
			case head := <-chainHeadCh:
				select {
				case headCh <- head.Block:
				default:
				}

				// Notify of chain snailHead events, but drop if too frequent
			case snailHead := <-chainsnailHeadCh:
				select {
				case snailHeadCh <- snailHead.Block:
				default:
				}
				// Notify of new transaction events, but drop if too frequent
			case <-txEventCh:
				if time.Duration(mclock.Now()-lastTx) < time.Second {
					continue
				}
				lastTx = mclock.Now()

				select {
				case txCh <- struct{}{}:
				default:
				}

				// node stopped
			case <-txSub.Err():
				break HandleLoop
			case <-headSub.Err():
				break HandleLoop
			}
		}
		close(quitCh)
	}()
	// Loop reporting until termination
	for {
		// Resolve the URL, defaulting to TLS, but falling back to none too
		path := fmt.Sprintf("%s/api", s.host)
		urls := []string{path}

		if !strings.Contains(path, "://") { // url.Parse and url.IsAbs is unsuitable (https://github.com/golang/go/issues/19779)
			urls = []string{"wss://" + path, "ws://" + path}
		}
		// Establish a websocket connection to the server on any supported URL
		var (
			conf *websocket.Config
			conn *websocket.Conn
			err  error
		)
		for _, url := range urls {
			if conf, err = websocket.NewConfig(url, "http://localhost/"); err != nil {
				continue
			}
			conf.Dialer = &net.Dialer{Timeout: 5 * time.Second}
			if conn, err = websocket.DialConfig(conf); err == nil {
				break
			}
		}
		if err != nil {
			log.Warn("Stats server unreachable", "err", err)
			time.Sleep(10 * time.Second)
			continue
		}
		// Authenticate the client with the server
		if err = s.login(conn); err != nil {
			log.Warn("Stats login failed", "err", err)
			conn.Close()
			time.Sleep(10 * time.Second)
			continue
		}
		go s.readLoop(conn)

		// Send the initial stats so our node looks decent from the get go
		if err = s.report(conn); err != nil {
			log.Warn("Initial stats report failed", "err", err)
			conn.Close()
			continue
		}
		if err = s.reportSnailBlock(conn, nil); err != nil {
			log.Warn("Initial snailBlock stats report failed", "err", err)
			conn.Close()
			continue
		}
		// Keep sending status updates until the connection breaks
		fullReport := time.NewTicker(15 * time.Second)
		snailBlockReport := time.NewTicker(10 * time.Minute)
		for err == nil {
			select {
			case <-quitCh:
				conn.Close()
				return
			case <-fullReport.C:
				if err = s.report(conn); err != nil {
					log.Warn("Full stats report failed", "err", err)
				}
			case <-snailBlockReport.C:
				if err = s.reportSnailBlock(conn, nil); err != nil {
					log.Warn("snailBlockReport stats report failed", "err", err)
				}
			case list := <-s.histCh:
				if err = s.reportHistory(conn, list); err != nil {
					log.Warn("Requested history report failed", "err", err)
				}
			case list := <-s.snailHistCh:
				if err = s.reportSnailHistory(conn, list); err != nil {
					log.Warn("Requested snailHistory report failed", "err", err)
				}
			case head := <-headCh:
				if err = s.reportBlock(conn, head); err != nil {
					log.Warn("Block stats report failed", "err", err)
				}
				if err = s.reportPending(conn); err != nil {
					log.Warn("Post-block transaction stats report failed", "err", err)
				}
			case snailBlock := <-snailHeadCh:
				if err = s.reportSnailBlock(conn, snailBlock); err != nil {
					log.Warn("Block stats report failed", "err", err)
				}
			case <-txCh:
				if err = s.reportPending(conn); err != nil {
					log.Warn("Transaction stats report failed", "err", err)
				}
			}
		}
		// Make sure the connection is closed
		conn.Close()
	}
}

// readLoop loops as long as the connection is alive and retrieves data packets
// from the network socket. If any of them match an active request, it forwards
// it, if they themselves are requests it initiates a reply, and lastly it drops
// unknown packets.
func (s *Service) readLoop(conn *websocket.Conn) {
	// If the read loop exists, close the connection
	defer conn.Close()

	for {
		// Retrieve the next generic network packet and bail out on error
		var msg map[string][]interface{}
		if err := websocket.JSON.Receive(conn, &msg); err != nil {
			log.Warn("Failed to decode stats server message", "err", err)
			return
		}
		log.Trace("Received message from stats server", "msg", msg)
		if len(msg["emit"]) == 0 {
			log.Warn("Stats server sent non-broadcast", "msg", msg)
			return
		}
		command, ok := msg["emit"][0].(string)
		if !ok {
			log.Warn("Invalid stats server message type", "type", msg["emit"][0])
			return
		}
		// If the message is a ping reply, deliver (someone must be listening!)
		if len(msg["emit"]) == 2 && command == "node-pong" {
			select {
			case s.pongCh <- struct{}{}:
				// Pong delivered, continue listening
				continue
			default:
				// Ping routine dead, abort
				log.Warn("Stats server pinger seems to have died")
				return
			}
		}
		// If the message is a history request, forward to the event processor
		if len(msg["emit"]) == 2 && (command == "history" || command == "snailHistory") {
			result := handleHistCh(msg, s, command)
			if result == "continue" {
				continue
			} else if result == "error" {
				return
			}
		}
		// Report anything else and continue
		log.Info("Unknown stats message", "msg", msg)
	}
}

func handleHistCh(msg map[string][]interface{}, s *Service, command string) string {
	// Make sure the request is valid and doesn't crash us
	request, ok := msg["emit"][1].(map[string]interface{})
	if !ok {
		if command == "history" {
			s.histCh <- nil
		} else {
			s.snailHistCh <- nil
		}
		return "continue" // Abeystats sometime sends invalid history requests, ignore those
	}
	list, ok := request["list"].([]interface{})
	if !ok {
		log.Warn("Invalid stats history block list", "list", request["list"])
		return "error"
	}
	// Convert the block number list to an integer list
	numbers := make([]uint64, len(list))
	for i, num := range list {
		n, ok := num.(float64)
		if !ok {
			log.Warn("Invalid stats history block number", "number", num)
			return "error"
		}
		numbers[i] = uint64(n)
	}
	if command == "history" {
		select {
		case s.histCh <- numbers:
			return "continue"
		default:
		}
	} else {
		select {
		case s.snailHistCh <- numbers:
			return "continue"
		default:
		}
	}
	return ""
}

// nodeInfo is the collection of metainformation about a node that is displayed
// on the monitoring page.
type nodeInfo struct {
	Name     string `json:"name"`
	Node     string `json:"node"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Network  string `json:"net"`
	Protocol string `json:"protocol"`
	API      string `json:"api"`
	Os       string `json:"os"`
	OsVer    string `json:"os_v"`
	Client   string `json:"client"`
	History  bool   `json:"canUpdateHistory"`
}

// authMsg is the authentication infos needed to login to a monitoring server.
type authMsg struct {
	ID     string   `json:"id"`
	Info   nodeInfo `json:"info"`
	Secret string   `json:"secret"`
}

// login tries to authorize the client at the remote server.
func (s *Service) login(conn *websocket.Conn) error {
	// Construct and send the login authentication
	infos := s.server.NodeInfo()

	var network, protocol string
	if info := infos.Protocols["abey"]; info != nil {
		network = fmt.Sprintf("%d", info.(*abey.NodeInfo).Network)
		protocol = fmt.Sprintf("abey/%d", abey.ProtocolVersions[0])
	} else {
		network = fmt.Sprintf("%d", infos.Protocols["les"].(*les.NodeInfo).Network)
		protocol = fmt.Sprintf("les/%d", les.ClientProtocolVersions[0])
	}
	//fmt.Println("infos.IP= ",infos.IP)
	auth := &authMsg{
		ID: s.node,
		Info: nodeInfo{
			Name:     s.node,
			Node:     infos.Name,
			IP:       infos.IP,
			Port:     infos.Ports.Listener,
			Network:  network,
			Protocol: protocol,
			API:      "No",
			Os:       runtime.GOOS,
			OsVer:    runtime.GOARCH,
			Client:   "0.1.1",
			History:  true,
		},
		Secret: s.pass,
	}
	login := map[string][]interface{}{
		"emit": {"hello", auth},
	}
	if err := websocket.JSON.Send(conn, login); err != nil {
		return err
	}
	// Retrieve the remote ack or connection termination
	var ack map[string][]string
	if err := websocket.JSON.Receive(conn, &ack); err != nil || len(ack["emit"]) != 1 || ack["emit"][0] != "ready" {
		return errors.New("unauthorized")
	}
	return nil
}

// report collects all possible data to report and send it to the stats server.
// This should only be used on reconnects or rarely to avoid overloading the
// server. Use the individual methods for reporting subscribed events.
func (s *Service) report(conn *websocket.Conn) error {
	if err := s.reportLatency(conn); err != nil {
		return err
	}
	if err := s.reportBlock(conn, nil); err != nil {
		return err
	}
	if err := s.reportPending(conn); err != nil {
		return err
	}
	if err := s.reportStats(conn); err != nil {
		return err
	}
	return nil
}

// reportLatency sends a ping request to the server, measures the RTT time and
// finally sends a latency update.
func (s *Service) reportLatency(conn *websocket.Conn) error {
	// Send the current time to the abeystats server
	start := time.Now()

	ping := map[string][]interface{}{
		"emit": {"node-ping", map[string]string{
			"id":         s.node,
			"clientTime": start.String(),
		}},
	}
	if err := websocket.JSON.Send(conn, ping); err != nil {
		return err
	}
	// Wait for the pong request to arrive back
	select {
	case <-s.pongCh:
		// Pong delivered, report the latency
	case <-time.After(5 * time.Second):
		// Ping timeout, abort
		return errors.New("ping timed out")
	}
	latency := strconv.Itoa(int((time.Since(start) / time.Duration(2)).Nanoseconds() / 1000000))

	// Send back the measured latency
	log.Trace("Sending measured latency to abeystats", "latency", latency)

	stats := map[string][]interface{}{
		"emit": {"latency", map[string]string{
			"id":      s.node,
			"latency": latency,
		}},
	}
	return websocket.JSON.Send(conn, stats)
}

// blockStats is the information to report about individual blocks.
type blockStats struct {
	Number     *big.Int    `json:"number"`
	Hash       common.Hash `json:"hash"`
	ParentHash common.Hash `json:"parentHash"`
	Timestamp  *big.Int    `json:"timestamp"`
	GasUsed    uint64      `json:"gasUsed"`
	GasLimit   uint64      `json:"gasLimit"`
	Txs        []txStats   `json:"transactions"`
	TxHash     common.Hash `json:"transactionsRoot"`
	Root       common.Hash `json:"stateRoot"`
}

// blockStats is the information to report about individual blocks.
type snailBlockStats struct {
	Number      *big.Int        `json:"number"`
	Hash        common.Hash     `json:"hash"`
	ParentHash  common.Hash     `json:"parentHash"`
	Timestamp   *big.Int        `json:"timestamp"`
	Miner       common.Address  `json:"miner"`
	Diff        string          `json:"difficulty"`
	TotalDiff   string          `json:"totalDifficulty"`
	Uncles      snailUncleStats `json:"uncles"`
	FruitNumber *big.Int        `json:"fruits"`
	LastFruit   *big.Int        `json:"lastFruit"`
	//Specific properties of fruit
	//signs types.PbftSigns
}

// txStats is the information to report about individual transactions.
type txStats struct {
	Hash common.Hash `json:"hash"`
}

// uncleStats is a custom wrapper around an uncle array to force serializing
// empty arrays instead of returning null for them.

type snailUncleStats []*types.SnailHeader

func (s snailUncleStats) MarshalJSON() ([]byte, error) {
	if uncles := ([]*types.SnailHeader)(s); len(uncles) > 0 {
		return json.Marshal(uncles)
	}
	return []byte("[]"), nil
}

// reportBlock retrieves the current chain head and reports it to the stats server.
func (s *Service) reportBlock(conn *websocket.Conn, block *types.Block) error {
	// Gather the block details from the header or block chain
	details := s.assembleBlockStats(block)

	// Assemble the block report and send it to the server
	log.Trace("Sending new block to abeystats", "number", details.Number, "hash", details.Hash)

	stats := map[string]interface{}{
		"id":    s.node,
		"block": details,
	}
	report := map[string][]interface{}{
		"emit": {"block", stats},
	}
	return websocket.JSON.Send(conn, report)
}

// reportBlock retrieves the current chain head and reports it to the stats server.
func (s *Service) reportSnailBlock(conn *websocket.Conn, block *types.SnailBlock) error {
	// Gather the block details from the header or block chain
	details := s.assembleSnailBlockStats(block)

	// Assemble the block report and send it to the server
	log.Trace("Sending new snailBlock to abeystats", "number", details.Number, "hash", details.Hash)

	stats := map[string]interface{}{
		"id":    s.node,
		"block": details,
	}
	report := map[string][]interface{}{
		"emit": {"snailBlock", stats},
	}
	return websocket.JSON.Send(conn, report)
}

// assembleBlockStats retrieves any required metadata to report a single block
// and assembles the block stats. If block is nil, the current head is processed.
func (s *Service) assembleBlockStats(block *types.Block) *blockStats {
	// Gather the block infos from the local blockchain
	var (
		header *types.Header
		txs    []txStats
	)
	if s.abey != nil {
		// Full nodes have all needed information available
		if block == nil {
			block = s.abey.BlockChain().CurrentBlock()
		}
		header = block.Header()

		txs = make([]txStats, len(block.Transactions()))
		for i, tx := range block.Transactions() {
			txs[i].Hash = tx.Hash()
		}
	} else {
		// Light nodes would need on-demand lookups for transactions/uncles, skip
		if block != nil {
			header = block.Header()
		} else {
			header = s.les.BlockChain().CurrentHeader()
		}
		txs = []txStats{}
	}
	return &blockStats{
		Number:     header.Number,
		Hash:       header.Hash(),
		ParentHash: header.ParentHash,
		Timestamp:  header.Time,
		GasUsed:    header.GasUsed,
		GasLimit:   header.GasLimit,
		Txs:        txs,
		TxHash:     header.TxHash,
		Root:       header.Root,
	}
}

// assembleBlockStats retrieves any required metadata to report a single block
// and assembles the block stats. If block is nil, the current head is processed.
func (s *Service) assembleSnailBlockStats(block *types.SnailBlock) *snailBlockStats {
	// Gather the block infos from the local blockchain
	var (
		header      *types.SnailHeader
		td          *big.Int
		fruitNumber *big.Int
		diff        string
		maxFruit    *big.Int
	)
	if s.abey == nil {
		return nil
	}
	// Full nodes have all needed information available
	if block == nil {
		block = s.abey.SnailBlockChain().CurrentBlock()
	}
	header = block.Header()
	td = s.abey.SnailBlockChain().GetTd(header.Hash(), header.Number.Uint64())
	fruitNumber = big.NewInt(int64(len(block.Fruits())))
	diff = block.Difficulty().String()
	maxFruit = block.MaxFruitNumber()
	// Assemble and return the block stats
	author, _ := s.engine.AuthorSnail(header)
	return &snailBlockStats{
		Number:      header.Number,
		Hash:        header.Hash(),
		ParentHash:  header.ParentHash,
		Timestamp:   header.Time,
		Miner:       author,
		Diff:        diff,
		TotalDiff:   td.String(),
		Uncles:      nil,
		FruitNumber: fruitNumber,
		LastFruit:   maxFruit,
	}
}

// reportHistory retrieves the most recent batch of blocks and reports it to the
// stats server.
func (s *Service) reportHistory(conn *websocket.Conn, list []uint64) error {
	// Figure out the indexes that need reporting
	indexes := make([]uint64, 0, historyUpdateRange)
	if len(list) > 0 {
		// Specific indexes requested, send them back in particular
		indexes = append(indexes, list...)
	} else {
		// No indexes requested, send back the top ones
		var head int64
		if s.abey != nil {
			head = s.abey.BlockChain().CurrentHeader().Number.Int64()
		} else {
			head = s.les.BlockChain().CurrentHeader().Number.Int64()
		}
		start := head - historyUpdateRange + 1
		if start < 0 {
			start = 0
		}
		for i := uint64(start); i <= uint64(head); i++ {
			indexes = append(indexes, i)
		}
	}
	// Gather the batch of blocks to report
	history := make([]*blockStats, len(indexes))
	for i, number := range indexes {
		// Retrieve the next block if it's known to us
		var block *types.Block
		if s.abey != nil {
			block = s.abey.BlockChain().GetBlockByNumber(number)
		} else {
			if header := s.les.BlockChain().GetHeaderByNumber(number); header != nil {
				block = types.NewBlockWithHeader(header)
			}
		}
		// If we do have the block, add to the history and continue
		if block != nil {
			history[len(history)-1-i] = s.assembleBlockStats(block)
			continue
		}
		// Ran out of blocks, cut the report short and send
		history = history[len(history)-i:]
		break
	}
	// Assemble the history report and send it to the server
	if len(history) > 0 {
		log.Trace("Sending historical blocks to abeystats", "first", history[0].Number, "last", history[len(history)-1].Number)
	} else {
		log.Trace("No history to send to stats server")
	}
	stats := map[string]interface{}{
		"id":      s.node,
		"history": history,
	}
	report := map[string][]interface{}{
		"emit": {"history", stats},
	}
	return websocket.JSON.Send(conn, report)
}
func (s *Service) reportSnailHistory(conn *websocket.Conn, list []uint64) error {
	// Figure out the indexes that need reporting
	indexes := make([]uint64, 0, historyUpdateRange)
	if len(list) > 0 {
		// Specific indexes requested, send them back in particular
		indexes = append(indexes, list...)
	} else {
		// No indexes requested, send back the top ones
		var head int64
		if s.abey != nil {
			head = s.abey.SnailBlockChain().CurrentHeader().Number.Int64()
			start := head - historyUpdateRange + 1
			if start < 0 {
				start = 0
			}
			for i := uint64(start); i <= uint64(head); i++ {
				indexes = append(indexes, i)
			}
		}
	}
	// Gather the batch of blocks to report
	history := make([]*snailBlockStats, len(indexes))
	for i, number := range indexes {
		// Retrieve the next block if it's known to us
		var snailBlock *types.SnailBlock
		if s.abey != nil {
			snailBlock = s.abey.SnailBlockChain().GetBlockByNumber(number)
			// If we do have the block, add to the history and continue
			if snailBlock != nil {
				history[len(history)-1-i] = s.assembleSnailBlockStats(snailBlock)
				continue
			}
			// Ran out of blocks, cut the report short and send
			history = history[len(history)-i:]
		}
		break
	}
	// Assemble the history report and send it to the server
	if len(history) > 0 {
		log.Trace("Sending historical snaiBlocks to abeystats", "first", history[0].Number, "last", history[len(history)-1].Number)
	} else {
		log.Trace("No history to send to stats server")
	}
	stats := map[string]interface{}{
		"id":      s.node,
		"history": history,
	}
	report := map[string][]interface{}{
		"emit": {"snailHistory", stats},
	}
	return websocket.JSON.Send(conn, report)
}

// pendStats is the information to report about pending transactions.
type pendStats struct {
	Pending int `json:"pending"`
}

// reportPending retrieves the current number of pending transactions and reports
// it to the stats server.
func (s *Service) reportPending(conn *websocket.Conn) error {
	// Retrieve the pending count from the local blockchain
	var pending int
	if s.abey != nil {
		pending, _ = s.abey.TxPool().Stats()
	} else {
		pending = s.les.TxPool().Stats()
	}
	// Assemble the transaction stats and send it to the server
	log.Trace("Sending pending transactions to abeystats", "count", pending)

	stats := map[string]interface{}{
		"id": s.node,
		"stats": &pendStats{
			Pending: pending,
		},
	}
	report := map[string][]interface{}{
		"emit": {"pending", stats},
	}
	return websocket.JSON.Send(conn, report)
}

// nodeStats is the information to report about the local node.
type nodeStats struct {
	Active            bool `json:"active"`
	Syncing           bool `json:"syncing"`
	Mining            bool `json:"mining"`
	IsCommitteeMember bool `json:"isCommitteeMember"`
	IsLeader          bool `json:"isLeader"`
	Hashrate          int  `json:"hashrate"`
	Peers             int  `json:"peers"`
	GasPrice          int  `json:"gasPrice"`
	Uptime            int  `json:"uptime"`
}

// reportPending retrieves various stats about the node at the networking and
// mining layer and reports it to the stats server.
func (s *Service) reportStats(conn *websocket.Conn) error {
	// Gather the syncing and mining infos from the local miner instance
	var (
		mining            bool
		isCommitteeMember bool
		isLeader          bool
		hashrate          int
		syncing           bool
		gasprice          int
	)
	if s.abey != nil {
		mining = s.abey.Miner().Mining()
		hashrate = int(s.abey.Miner().HashRate())

		sync := s.abey.Downloader().Progress()
		syncing = s.abey.BlockChain().CurrentHeader().Number.Uint64() >= sync.HighestFastBlock

		price, _ := s.abey.APIBackend.SuggestPrice(context.Background())
		gasprice = int(price.Uint64())

		isCommitteeMember = s.abey.PbftAgent().IsCommitteeMember()
		isLeader = s.abey.PbftAgent().IsLeader()
	} else {
		sync := s.les.Downloader().Progress()
		syncing = s.les.BlockChain().CurrentHeader().Number.Uint64() >= sync.HighestFastBlock
	}
	// Assemble the node stats and send it to the server
	log.Trace("Sending node details to abeystats")
	nodeStats := &nodeStats{
		Active:            true,
		Mining:            mining,
		Hashrate:          hashrate,
		Peers:             s.server.PeerCount(),
		GasPrice:          gasprice,
		Syncing:           syncing,
		Uptime:            100,
		IsCommitteeMember: isCommitteeMember,
		IsLeader:          isLeader,
	}
	stats := map[string]interface{}{
		"id":    s.node,
		"stats": nodeStats,
	}
	report := map[string][]interface{}{
		"emit": {"stats", stats},
	}
	return websocket.JSON.Send(conn, report)
}
