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

package abey

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/crypto"
	"github.com/abeychain/go-abey/rlp"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/abey/downloader"
	"github.com/abeychain/go-abey/p2p"
)

func init() {
	//log.Root().SetHandler(log.LvlFilterHandler(log.LvlTrace, log.StreamHandler(os.Stderr, log.TerminalFormat(false))))
}

var testAccount, _ = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")

// Tests that handshake failures are detected and reported correctly.
func TestStatusMsgErrors63(t *testing.T) { testStatusMsgErrors(t, 63) }

func testStatusMsgErrors(t *testing.T, protocol int) {
	pm, _ := newTestProtocolManagerMust(t, downloader.FullSync, 0, 0, nil, nil, nil, nil)
	var (
		genesis       = pm.snailchain.Genesis()
		head          = pm.snailchain.CurrentHeader()
		td            = pm.snailchain.GetTd(head.Hash(), head.Number.Uint64())
		fastHead      = pm.blockchain.CurrentHeader()
		fastHash      = fastHead.Hash()
		fastHeight    = pm.blockchain.CurrentBlock().Number()
		gcHeight      = pm.blockchain.CurrentGcHeight()
		committHeight = pm.blockchain.CurrentCommitHeight()
	)
	defer pm.Stop()
	if protocol == abey63 {
		tests := []struct {
			code      uint64
			data      interface{}
			wantError error
		}{
			{
				code: TxMsg, data: []interface{}{},
				wantError: errResp(ErrNoStatusMsg, "first msg has code 2 (!= 0)"),
			},
			{
				code: StatusMsg, data: statusData{10, DefaultConfig.NetworkId, td, fastHeight, head.Hash(), genesis.Hash(), fastHash},
				wantError: errResp(ErrProtocolVersionMismatch, "10 (!= %d)", protocol),
			},
			{
				code: StatusMsg, data: statusData{uint32(protocol), 999, td, fastHeight, head.Hash(), genesis.Hash(), fastHash},
				wantError: errResp(ErrNetworkIdMismatch, "999 (!= 19330)"),
			},
			{
				code: StatusMsg, data: statusData{uint32(protocol), DefaultConfig.NetworkId, td, fastHeight, head.Hash(), common.Hash{3}, fastHash},
				wantError: errResp(ErrGenesisBlockMismatch, "0300000000000000 (!= %x)", genesis.Hash().Bytes()[:8]),
			},
		}
		for i, test := range tests {
			p, errc := newTestPeer("peer", protocol, pm, false)
			// The send Call might hang until reset because
			// the protocol might not read the payload.
			go p2p.Send(p.app, test.code, test.data)

			select {
			case err := <-errc:
				if err == nil {
					t.Errorf("test %d: protocol returned nil error, want %q", i, test.wantError)
				} else if err.Error() != test.wantError.Error() {
					t.Errorf("test %d: wrong error: got %q, want %q", i, err, test.wantError)
				}
			case <-time.After(2 * time.Second):
				t.Errorf("protocol did not shut down within 2 seconds")
			}
			p.close()
		}
	} else {
		tests := []struct {
			code      uint64
			data      interface{}
			wantError error
		}{
			{
				code: TxMsg, data: []interface{}{},
				wantError: errResp(ErrNoStatusMsg, "first msg has code 2 (!= 0)"),
			},
			{
				code: StatusMsg, data: statusSnapData{10, DefaultConfig.NetworkId, td, fastHeight, head.Hash(), genesis.Hash(), fastHash, gcHeight, committHeight},
				wantError: errResp(ErrProtocolVersionMismatch, "10 (!= %d)", protocol),
			},
			{
				code: StatusMsg, data: statusSnapData{uint32(protocol), 999, td, fastHeight, head.Hash(), genesis.Hash(), fastHash, gcHeight, committHeight},
				wantError: errResp(ErrNetworkIdMismatch, "999 (!= 19330)"),
			},
			{
				code: StatusMsg, data: statusSnapData{uint32(protocol), DefaultConfig.NetworkId, td, fastHeight, head.Hash(), common.Hash{3}, fastHash, gcHeight, committHeight},
				wantError: errResp(ErrGenesisBlockMismatch, "0300000000000000 (!= %x)", genesis.Hash().Bytes()[:8]),
			},
		}
		for i, test := range tests {
			p, errc := newTestPeer("peer", protocol, pm, false)
			// The send Call might hang until reset because
			// the protocol might not read the payload.
			go p2p.Send(p.app, test.code, test.data)

			select {
			case err := <-errc:
				if err == nil {
					t.Errorf("test %d: protocol returned nil error, want %q", i, test.wantError)
				} else if err.Error() != test.wantError.Error() {
					t.Errorf("test %d: wrong error: got %q, want %q", i, err, test.wantError)
				}
			case <-time.After(2 * time.Second):
				t.Errorf("protocol did not shut down within 2 seconds")
			}
			p.close()
		}
	}

}

// Tests that handshake failures are detected and reported correctly.
func TestStatusMsgErrors64(t *testing.T) { testStatusMsgErrors(t, 64) }

// This test checks that received transactions are added to the local pool.
func TestRecvTransactions63(t *testing.T) { testRecvTransactions(t, 63) }

func testRecvTransactions(t *testing.T, protocol int) {
	txAdded := make(chan []*types.Transaction)
	pm, _ := newTestProtocolManagerMust(t, downloader.FullSync, 0, 0, nil, nil, txAdded, nil)
	pm.acceptTxs = 1 // mark synced to accept transactions
	p, _ := newTestPeer("peer", protocol, pm, true)
	defer pm.Stop()
	defer p.close()

	tx := newTestTransaction(testAccount, 0, 0)
	if err := p2p.Send(p.app, TxMsg, []interface{}{tx}); err != nil {
		t.Fatalf("send error: %v", err)
	}
	select {
	case added := <-txAdded:
		if len(added) != 1 {
			t.Errorf("wrong number of added transactions: got %d, want 1", len(added))
		} else if added[0].Hash() != tx.Hash() {
			t.Errorf("added wrong tx hash: got %v, want %v", added[0].Hash(), tx.Hash())
		}
	case <-time.After(2 * time.Second):
		t.Errorf("no NewTxsEvent received within 2 seconds")
	}
}

// This test checks that pending transactions are sent.
func TestSendTransactions63(t *testing.T) { testSendTransactions(t, 63) }

func testSendTransactions(t *testing.T, protocol int) {
	pm, _ := newTestProtocolManagerMust(t, downloader.FullSync, 0, 0, nil, nil, nil, nil)
	defer pm.Stop()

	// Fill the pool with big transactions.
	const txsize = txsyncPackSize / 10
	alltxs := make([]*types.Transaction, 100)
	for nonce := range alltxs {
		alltxs[nonce] = newTestTransaction(testAccount, uint64(nonce), txsize)
	}

	pm.txpool.AddRemotes(alltxs)

	// Connect several peers. They should all receive the pending transactions.
	var wg sync.WaitGroup
	checktxs := func(p *testPeer) {
		defer wg.Done()
		defer p.close()
		seen := make(map[common.Hash]bool)
		for _, tx := range alltxs {
			seen[tx.Hash()] = false
		}
		for n := 0; n < len(alltxs) && !t.Failed(); {
			var txs []*types.Transaction
			msg, err := p.app.ReadMsg()
			if err != nil {
				t.Errorf("%v: read error: %v", p.Peer, err)
			} else if msg.Code != TxMsg {
				t.Errorf("%v: got code %d, want TxMsg", p.Peer, msg.Code)
			}
			if err := msg.Decode(&txs); err != nil {
				t.Errorf("%v: %v", p.Peer, err)
			}
			for _, tx := range txs {
				hash := tx.Hash()
				seentx, want := seen[hash]
				if seentx {
					t.Errorf("%v: got tx more than once: %x", p.Peer, hash)
				}
				if !want {
					t.Errorf("%v: got unexpected tx: %x", p.Peer, hash)
				}
				seen[hash] = true
				n++
			}
		}
	}
	for i := 0; i < 3; i++ {
		p, _ := newTestPeer(fmt.Sprintf("peer #%d", i), protocol, pm, true)
		wg.Add(1)
		go checktxs(p)
	}
	wg.Wait()
}

// Tests that the custom union field encoder and decoder works correctly.
func TestGetBlockHeadersDataEncodeDecode(t *testing.T) {
	// Create a "random" hash for testing
	var hash common.Hash
	for i := range hash {
		hash[i] = byte(i)
	}
	// Assemble some table driven tests
	tests := []struct {
		packet *getBlockHeadersData
		fail   bool
	}{
		// Providing the origin as either a hash or a number should both work
		{fail: false, packet: &getBlockHeadersData{Origin: hashOrNumber{Number: 314}}},
		{fail: false, packet: &getBlockHeadersData{Origin: hashOrNumber{Hash: hash}}},

		// Providing arbitrary query field should also work
		{fail: false, packet: &getBlockHeadersData{Origin: hashOrNumber{Number: 314}, Amount: 314, Skip: 1, Reverse: true, Call: types.FetcherCall}},
		{fail: false, packet: &getBlockHeadersData{Origin: hashOrNumber{Hash: hash}, Amount: 314, Skip: 1, Reverse: true, Call: types.DownloaderCall}},

		// Providing both the origin hash and origin number must fail
		{fail: true, packet: &getBlockHeadersData{Origin: hashOrNumber{Hash: hash, Number: 314}}},
	}
	// Iterate over each of the tests and try to encode and then decode
	for i, tt := range tests {
		bytes, err := rlp.EncodeToBytes(tt.packet)
		if err != nil && !tt.fail {
			t.Fatalf("test %d: failed to encode packet: %v", i, err)
		} else if err == nil && tt.fail {
			t.Fatalf("test %d: encode should have failed", i)
		}
		if !tt.fail {
			packet := new(getBlockHeadersData)
			if err := rlp.DecodeBytes(bytes, packet); err != nil {
				t.Fatalf("test %d: failed to decode packet: %v", i, err)
			}
			if packet.Origin.Hash != tt.packet.Origin.Hash || packet.Origin.Number != tt.packet.Origin.Number || packet.Amount != tt.packet.Amount ||
				packet.Skip != tt.packet.Skip || packet.Reverse != tt.packet.Reverse || packet.Call != tt.packet.Call {
				t.Fatalf("test %d: encode decode mismatch: have %+v, want %+v", i, packet, tt.packet)
			}
		}
	}
}

// This test checks that received fruits are added to the local pool.
func TestRecvFruits63(t *testing.T) { testRecvFruits(t, 63) }

func testRecvFruits(t *testing.T, protocol int) {
	ftAdded := make(chan []*types.SnailBlock)
	pm, _ := newTestProtocolManagerMust(t, downloader.FullSync, 64, 64, nil, nil, nil, ftAdded)
	pm.acceptFruits = 1 // mark synced to accept fruits
	p, _ := newTestPeer("peer", protocol, pm, true)
	defer pm.Stop()
	defer p.close()

	ft := pm.snailchain.CurrentBlock().Fruits()[0]
	if err := p2p.Send(p.app, NewFruitMsg, []interface{}{ft}); err != nil {
		t.Fatalf("send error: %v", err)
	}
	select {
	case added := <-ftAdded:
		if len(added) != 1 {
			t.Errorf("wrong number of added transactions: got %d, want 1", len(added))
		} else if added[0].Hash() != ft.Hash() {
			t.Errorf("added wrong tx hash: got %v, want %v", added[0].Hash(), ft.Hash())
		}
	case <-time.After(2 * time.Second):
		t.Errorf("no NewTxsEvent received within 2 seconds")
	}
}

// This test checks that pending transactions are sent.
func TestSendFruits63(t *testing.T) { testSendFruits(t, 63) }

func testSendFruits(t *testing.T, protocol int) {
	pm, _ := newTestProtocolManagerMust(t, downloader.FullSync, 120, 120, nil, nil, nil, nil)
	defer pm.Stop()

	// Fill the pool with big transactions.
	allfts := make([]*types.SnailBlock, 0)

	currentNumber := pm.snailchain.CurrentBlock().Number()
	for i := uint64(1); i <= currentNumber.Uint64(); i++ {
		block := pm.snailchain.GetBlockByNumber(i)
		allfts = append(allfts, block.Fruits()...)
	}

	pm.SnailPool.AddRemoteFruits(allfts, false)

	// Connect several peers. They should all receive the pending transactions.
	var wg sync.WaitGroup
	checkfts := func(p *testPeer) {
		defer wg.Done()
		defer p.close()
		seen := make(map[common.Hash]bool)
		for _, ft := range allfts {
			seen[ft.Hash()] = false
		}
		for n := 0; n < len(allfts) && !t.Failed(); {
			var fts []*types.SnailBlock
			msg, err := p.app.ReadMsg()
			if err != nil {
				t.Errorf("%v: read error: %v", p.Peer, err)
			} else if msg.Code != NewFruitMsg {
				t.Errorf("%v: got code %d, want TxMsg", p.Peer, msg.Code)
			}
			if err := msg.Decode(&fts); err != nil {
				t.Errorf("%v: %v", p.Peer, err)
			}
			for _, ft := range fts {
				hash := ft.Hash()
				seentx, want := seen[hash]
				if seentx {
					t.Errorf("%v: got tx more than once: %x", p.Peer, hash)
				}
				if !want {
					t.Errorf("%v: got unexpected tx: %x", p.Peer, hash)
				}
				seen[hash] = true
				n++
			}
		}
	}
	for i := 0; i < 3; i++ {
		p, _ := newTestPeer(fmt.Sprintf("peer #%d", i), protocol, pm, true)
		wg.Add(1)
		go checkfts(p)
	}
	wg.Wait()
}
