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
	"crypto/ecdsa"
	"fmt"
	"github.com/abeychain/go-abey/core/state"
	"io"
	"math/big"

	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/event"
	"github.com/abeychain/go-abey/rlp"
)

// Constants to match up protocol versions and messages
const (
	abey63 = 63
	abey64 = 64
)

// ProtocolName is the official short name of the protocol used during capability negotiation.
var ProtocolName = "abey"

// ProtocolVersions are the upported versions of the abey protocol (first is primary).
var ProtocolVersions = []uint{abey64, abey63}

// ProtocolLengths are the number of implemented message corresponding to different protocol versions.
var ProtocolLengths = []uint64{32, 20}

const ProtocolMaxMsgSize = 10 * 1024 * 1024 // Maximum cap on the size of a protocol message

// abey protocol message codes
const (
	// Protocol messages belonging to abey/63
	StatusMsg              = 0x00
	NewFastBlockHashesMsg  = 0x01
	TxMsg                  = 0x02
	GetFastBlockHeadersMsg = 0x03
	FastBlockHeadersMsg    = 0x04
	GetFastBlockBodiesMsg  = 0x05
	FastBlockBodiesMsg     = 0x06
	NewFastBlockMsg        = 0x07
	TbftNodeInfoMsg        = 0x08

	//snail sync
	NewFruitMsg             = 0x09
	GetSnailBlockHeadersMsg = 0x0a
	SnailBlockHeadersMsg    = 0x0b
	GetSnailBlockBodiesMsg  = 0x0c
	SnailBlockBodiesMsg     = 0x0d
	NewSnailBlockMsg        = 0x0e

	GetNodeDataMsg         = 0x0f
	NodeDataMsg            = 0x10
	GetReceiptsMsg         = 0x11
	ReceiptsMsg            = 0x12
	NewSnailBlockHashesMsg = 0x13

	TbftNodeInfoHashMsg = 0x15
	GetTbftNodeInfoMsg  = 0x16
)

type errCode int

const (
	ErrMsgTooLarge = iota
	ErrDecode
	ErrInvalidMsgCode
	ErrProtocolVersionMismatch
	ErrNetworkIdMismatch
	ErrGenesisBlockMismatch
	ErrNoStatusMsg
	ErrExtraStatusMsg
	ErrSuspendedPeer
)

func (e errCode) String() string {
	return errorToString[int(e)]
}

// XXX change once legacy code is out
var errorToString = map[int]string{
	ErrMsgTooLarge:             "Message too long",
	ErrDecode:                  "Invalid message",
	ErrInvalidMsgCode:          "Invalid message code",
	ErrProtocolVersionMismatch: "Protocol version mismatch",
	ErrNetworkIdMismatch:       "NetworkId mismatch",
	ErrGenesisBlockMismatch:    "Genesis block mismatch",
	ErrNoStatusMsg:             "No status message",
	ErrExtraStatusMsg:          "Extra status message",
	ErrSuspendedPeer:           "Suspended peer",
}

type txPool interface {
	// AddRemotes should add the given transactions to the pool.
	AddRemotes([]*types.Transaction) []error

	// Pending should return pending transactions.
	// The slice should be modifiable by the caller.
	Pending() (map[common.Address]types.Transactions, error)

	// SubscribeNewTxsEvent should return an event subscription of
	// NewTxsEvent and send events to the given channel.
	SubscribeNewTxsEvent(chan<- types.NewTxsEvent) event.Subscription
	State() *state.ManagedState
}

type SnailPool interface {
	// AddRemoteFruits should add the given fruits to the pool.
	AddRemoteFruits([]*types.SnailBlock, bool) []error

	// PendingFruits should return pending fruits.
	PendingFruits() map[common.Hash]*types.SnailBlock

	// SubscribeNewFruitEvent should return an event subscription of
	// NewFruitsEvent and send events to the given channel.
	SubscribeNewFruitEvent(chan<- types.NewFruitsEvent) event.Subscription

	RemovePendingFruitByFastHash(fasthash common.Hash)
}

type AgentNetworkProxy interface {
	// SubscribeNewPbftSignEvent should return an event subscription of
	// PbftSignEvent and send events to the given channel.
	SubscribeNewPbftSignEvent(chan<- types.PbftSignEvent) event.Subscription
	// SubscribeNodeInfoEvent should return an event subscription of
	// NodeInfoEvent and send events to the given channel.
	SubscribeNodeInfoEvent(chan<- types.NodeInfoEvent) event.Subscription
	// AddRemoteNodeInfo should add the given NodeInfo to the pbft agent.
	AddRemoteNodeInfo(*types.EncryptNodeMessage) error
	//GetNodeInfoByHash get crypto nodeInfo  by hash
	GetNodeInfoByHash(nodeInfoHash common.Hash) (*types.EncryptNodeMessage, bool)
	//GetPrivateKey get crypto privateKey
	GetPrivateKey() *ecdsa.PrivateKey
}

// statusData is the network packet for the status message.
type statusData struct {
	ProtocolVersion  uint32
	NetworkId        uint64
	TD               *big.Int
	FastHeight       *big.Int
	CurrentBlock     common.Hash
	GenesisBlock     common.Hash
	CurrentFastBlock common.Hash
}

// statusSnapData is the network packet for the status message.
type statusSnapData struct {
	ProtocolVersion  uint32
	NetworkId        uint64
	TD               *big.Int
	FastHeight       *big.Int
	CurrentBlock     common.Hash
	GenesisBlock     common.Hash
	CurrentFastBlock common.Hash
	GcHeight         *big.Int
	CommitHeight     *big.Int
}

// newBlockHashesData is the network packet for the block announcements.
type newBlockHashesData []struct {
	Hash   common.Hash // Hash of one particular block being announced
	Number uint64      // Number of one particular block being announced
	TD     *big.Int
}

// getBlockHeadersData represents a block header query.
type getBlockHeadersData struct {
	Origin  hashOrNumber // Block from which to retrieve headers
	Amount  uint64       // Maximum number of headers to retrieve
	Skip    uint64       // Blocks to skip between consecutive headers
	Reverse bool         // Query direction (false = rising towards latest, true = falling towards genesis)
	Call    uint32       // Distinguish fetcher and downloader
}

// BlockHeadersData represents a block header send.
type BlockHeadersData struct {
	Headers      []*types.Header
	SnailHeaders []*types.SnailHeader
	Call         uint32 // Distinguish fetcher and downloader
}

// hashOrNumber is a combined field for specifying an origin block.
type hashOrNumber struct {
	Hash   common.Hash // Block hash from which to retrieve headers (excludes Number)
	Number uint64      // Block hash from which to retrieve headers (excludes Hash)
}

// getBlockHeadersData represents a block header query.
type nodeInfoHashData struct {
	Hash common.Hash
}

// EncodeRLP is a specialized encoder for hashOrNumber to encode only one of the
// two contained union fields.
func (hn *hashOrNumber) EncodeRLP(w io.Writer) error {
	if hn.Hash == (common.Hash{}) {
		return rlp.Encode(w, hn.Number)
	}
	if hn.Number != 0 {
		return fmt.Errorf("both origin hash (%x) and number (%d) provided", hn.Hash, hn.Number)
	}
	return rlp.Encode(w, hn.Hash)
}

// DecodeRLP is a specialized decoder for hashOrNumber to decode the contents
// into either a block hash or a block number.
func (hn *hashOrNumber) DecodeRLP(s *rlp.Stream) error {
	_, size, _ := s.Kind()
	origin, err := s.Raw()
	if err == nil {
		switch {
		case size == 32:
			err = rlp.DecodeBytes(origin, &hn.Hash)
		case size <= 8:
			err = rlp.DecodeBytes(origin, &hn.Number)
		default:
			err = fmt.Errorf("invalid input size %d for origin", size)
		}
	}
	return err
}

// newFastBlockData is the network packet for the block propagation message.
type newBlockData struct {
	Block      []*types.Block
	SnailBlock []*types.SnailBlock
	TD         *big.Int
}

// getBlockBodiesData represents a block body query.
type getBlockBodiesData struct {
	Hash common.Hash // Block hash from which to retrieve Bodies (excludes Number)
	Call uint32      // Distinguish fetcher and downloader
}

// BlockBodiesRawData represents a block header send.
type BlockBodiesRawData struct {
	Bodies []rlp.RawValue
	Call   uint32 // Distinguish fetcher and downloader
}

// blockBody represents the data content of a single block.
type blockBody struct {
	Transactions []*types.Transaction     // Transactions contained within a block
	Signs        []*types.PbftSign        // Signs contained within a block
	Infos        []*types.CommitteeMember //change info
}

// blockBodiesData is the network packet for block content distribution.
type blockBodiesData struct {
	BodiesData []*blockBody
	Call       uint32 // Distinguish fetcher and downloader
}

// blockBody represents the data content of a single block.
type snailBlockBody struct {
	Fruits []*types.SnailBlock
	Signs  []*types.PbftSign
}

// blockBodiesData is the network packet for block content distribution.
type snailBlockBodiesData struct {
	BodiesData []*snailBlockBody
	Call       uint32 // Distinguish fetcher and downloader
}
