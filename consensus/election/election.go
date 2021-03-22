// Copyright 2018 The AbeyChain Authors
// This file is part of the abey library.
//
// The abey library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The abey library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the abey library. If not, see <http://www.gnu.org/licenses/>.

package election

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sync"

	lru "github.com/hashicorp/golang-lru"
	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/consensus"
	"github.com/abeychain/go-abey/core/snailchain"
	"github.com/abeychain/go-abey/core/snailchain/rawdb"
	"github.com/abeychain/go-abey/core/state"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/core/vm"
	"github.com/abeychain/go-abey/crypto"
	"github.com/abeychain/go-abey/abeydb"
	"github.com/abeychain/go-abey/event"
	"github.com/abeychain/go-abey/log"
	"github.com/abeychain/go-abey/params"
)

const (
	snailchainHeadSize  = 64
	committeeCacheLimit = 256
)

type ElectMode uint

const (
	// ElectModeAbey for abey
	ElectModeAbey = iota
	// ElectModeFake for Test purpose
	ElectModeFake
)

var (
	// maxUint256 is a big integer representing 2^256-1
	maxUint256 = new(big.Int).Exp(big.NewInt(2), big.NewInt(256), big.NewInt(0))
)

var (
	ErrCommittee     = errors.New("get committee failed")
	ErrInvalidMember = errors.New("invalid committee member")
	ErrInvalidSwitch = errors.New("invalid switch block info")
)

type candidateMember struct {
	coinbase   common.Address
	address    common.Address
	publickey  *ecdsa.PublicKey
	difficulty *big.Int
	upper      *big.Int
	lower      *big.Int
}

type committee struct {
	id                  *big.Int
	beginFastNumber     *big.Int // the first fast block proposed by this committee
	endFastNumber       *big.Int // the last fast block proposed by this committee
	firstElectionNumber *big.Int // the begin snailblock to elect members
	lastElectionNumber  *big.Int // the end snailblock to elect members
	switchCheckNumber   *big.Int // the snailblock that start switch next committee
	members             types.CommitteeMembers
	backupMembers       types.CommitteeMembers
	switches            []*big.Int // blocknumbers whose block include switchinfos
}

// Members returns dump of the committee members
func (c *committee) Members() []*types.CommitteeMember {
	members := make([]*types.CommitteeMember, len(c.members))
	copy(members, c.members)
	return members
}

// Members returns dump of the backup committee members
func (c *committee) BackupMembers() []*types.CommitteeMember {
	members := make([]*types.CommitteeMember, len(c.backupMembers))
	copy(members, c.backupMembers)
	return members
}

func (c *committee) setMemberState(pubkey []byte, flag uint32) {
	for i, m := range c.members {
		if bytes.Equal(m.Publickey, pubkey) {
			c.members[i] = &types.CommitteeMember{
				Coinbase:  m.Coinbase,
				Publickey: m.Publickey,
				Flag:      flag,
			}
			break
		}
	}
	for i, m := range c.backupMembers {
		if bytes.Equal(m.Publickey, pubkey) {
			c.backupMembers[i] = &types.CommitteeMember{
				Coinbase:  m.Coinbase,
				Publickey: m.Publickey,
				Flag:      flag,
			}
			break
		}
	}
}

type Election struct {
	chainConfig *params.ChainConfig

	genesisCommittee []*types.CommitteeMember
	defaultMembers   []*types.CommitteeMember

	commiteeCache *lru.Cache
	epochCache    *lru.Cache

	electionMode    ElectMode
	committee       *committee
	nextCommittee   *committee
	mu              sync.RWMutex
	testPrivateKeys []*ecdsa.PrivateKey

	startSwitchover bool //Flag bit for handling event switching
	singleNode      bool

	electionFeed event.Feed
	scope        event.SubscriptionScope

	prepare    bool
	disabled   bool
	switchNext chan struct{}

	snailChainEventCh  chan types.SnailChainEvent
	snailChainEventSub event.Subscription

	fastchain  BlockChain
	snailchain SnailBlockChain

	engine consensus.Engine
}

type BlockChain interface {
	CurrentBlock() *types.Block

	CurrentHeader() *types.Header

	GetBlockByNumber(number uint64) *types.Block

	StateAt(root common.Hash) (*state.StateDB, error)
}

// SnailLightChain encapsulates functions required to synchronise a light chain.
type SnailLightChain interface {
	// CurrentHeader retrieves the head header from the local chain.
	CurrentHeader() *types.SnailHeader
}

// SnailBlockChain encapsulates functions required to sync a (full or fast) blockchain.
type SnailBlockChain interface {
	SnailLightChain

	SubscribeChainEvent(ch chan<- types.SnailChainEvent) event.Subscription

	GetDatabase() abeydb.Database

	GetFruitByFastHash(fastHash common.Hash) (*types.SnailBlock, uint64)

	GetBlockByNumber(number uint64) *types.SnailBlock

	GetFruitsHead(number uint64) []*types.SnailHeader

	GetHeaderByNumber(number uint64) *types.SnailHeader
}

type Config interface {
	GetNodeType() bool
}

// NewElection create election processor and load genesis committee
func NewElection(chainConfig *params.ChainConfig, fastBlockChain BlockChain, snailBlockChain SnailBlockChain, config Config) *Election {
	// init
	election := &Election{
		chainConfig:       chainConfig,
		fastchain:         fastBlockChain,
		snailchain:        snailBlockChain,
		snailChainEventCh: make(chan types.SnailChainEvent, snailchainHeadSize),
		prepare:           false,
		switchNext:        make(chan struct{}),
		singleNode:        config.GetNodeType(),
		electionMode:      ElectModeAbey,
	}

	// get genesis committee
	election.genesisCommittee = election.getGenesisCommittee()
	if len(election.genesisCommittee) == 0 {
		log.Error("Election creation get no genesis committee members")
	}

	election.commiteeCache, _ = lru.New(committeeCacheLimit)
	election.epochCache, _ = lru.New(committeeCacheLimit)

	if election.singleNode {
		committeeMember := election.getGenesisCommittee()
		if committeeMember == nil {
			log.Error("genesis block committee member is nil.")
		}
		election.genesisCommittee = election.getGenesisCommittee()[:1]
	}
	if !election.singleNode && len(election.genesisCommittee) < 4 {
		log.Error("Election creation get insufficient genesis committee members")
	}
	for _, m := range election.genesisCommittee {
		var member = *m
		member.Flag = types.StateUnusedFlag
		election.defaultMembers = append(election.defaultMembers, &member)
	}

	return election
}

func NewLightElection(fastBlockChain BlockChain, snailBlockChain SnailBlockChain) *Election {
	// init
	election := &Election{
		fastchain:    fastBlockChain,
		snailchain:   snailBlockChain,
		electionMode: ElectModeAbey,
	}
	return election
}

// NewFakeElection create fake mode election only for testing
func NewFakeElection() *Election {
	var priKeys []*ecdsa.PrivateKey
	var members []*types.CommitteeMember

	for i := 0; i < params.MinimumCommitteeNumber; i++ {
		priKey, err := crypto.GenerateKey()
		priKeys = append(priKeys, priKey)
		if err != nil {
			log.Error("initMembers", "error", err)
		}
		coinbase := crypto.PubkeyToAddress(priKey.PublicKey)
		m := &types.CommitteeMember{Coinbase: coinbase, CommitteeBase: coinbase, Publickey: crypto.FromECDSAPub(&priKey.PublicKey), Flag: types.StateUsedFlag, MType: types.TypeFixed}
		members = append(members, m)
	}

	// Backup members are empty in FakeMode Election
	elected := &committee{
		id:                  new(big.Int).Set(common.Big0),
		beginFastNumber:     new(big.Int).Set(common.Big1),
		endFastNumber:       new(big.Int).Set(common.Big0),
		firstElectionNumber: new(big.Int).Set(common.Big0),
		lastElectionNumber:  new(big.Int).Set(common.Big0),
		switchCheckNumber:   params.ElectionPeriodNumber,
		members:             members,
	}

	election := &Election{
		fastchain:         nil,
		snailchain:        nil,
		snailChainEventCh: make(chan types.SnailChainEvent, snailchainHeadSize),
		singleNode:        false,
		committee:         elected,
		electionMode:      ElectModeFake,
		testPrivateKeys:   priKeys,
	}
	return election
}

func (e *Election) GenerateFakeSigns(fb *types.Block) ([]*types.PbftSign, error) {
	var signs []*types.PbftSign
	for _, privateKey := range e.testPrivateKeys {
		voteSign := &types.PbftSign{
			Result:     types.VoteAgree,
			FastHeight: fb.Header().Number,
			FastHash:   fb.Hash(),
		}
		var err error
		signHash := voteSign.HashWithNoSign().Bytes()
		voteSign.Sign, err = crypto.Sign(signHash, privateKey)
		if err != nil {
			log.Error("fb GenerateSign error ", "err", err)
		}
		signs = append(signs, voteSign)
	}
	return signs, nil
}

func (e *Election) GetGenesisCommittee() []*types.CommitteeMember {
	return e.genesisCommittee
}

func (e *Election) GetCurrentCommittee() *committee {
	return e.committee
}

func (e *Election) GetCurrentCommitteeNumber() *big.Int {
	return e.committee.id
}

// GetMemberByPubkey returns committeeMember specified by public key bytes
func (e *Election) GetMemberByPubkey(members []*types.CommitteeMember, publickey []byte) *types.CommitteeMember {
	if len(members) == 0 {
		log.Error("GetMemberByPubkey method len(members)= 0")
		return nil
	}
	for _, member := range members {
		if bytes.Equal(publickey, member.Publickey) {
			return member
		}
	}
	return nil
}

// IsCommitteeMember reports whether the provided public key is in committee
func (e *Election) GetMemberFlag(members []*types.CommitteeMember, publickey []byte) uint32 {
	if len(members) == 0 {
		log.Error("IsCommitteeMember method len(members)= 0")
		return 0
	}
	for _, member := range members {
		if bytes.Equal(publickey, member.Publickey) {
			return member.Flag
		}
	}
	return 0
}

func (e *Election) IsCommitteeMember(members []*types.CommitteeMember, publickey []byte) bool {
	flag := e.GetMemberFlag(members, publickey)
	return flag == types.StateUsedFlag
}

// VerifyPublicKey get the committee member by public key
func (e *Election) VerifyPublicKey(fastHeight *big.Int, pubKeyByte []byte) (*types.CommitteeMember, error) {
	members := e.GetCommittee(fastHeight)
	if members == nil {
		log.Info("GetCommittee members is nil", "fastHeight", fastHeight)
		return nil, ErrCommittee
	}
	member := e.GetMemberByPubkey(members, pubKeyByte)
	/*if member == nil {
		return nil, ErrInvalidMember
	}*/
	return member, nil
}

// VerifySign lookup the pbft sign and return the committee member who signs it
func (e *Election) VerifySign(sign *types.PbftSign) (*types.CommitteeMember, error) {
	pubkey, err := crypto.SigToPub(sign.HashWithNoSign().Bytes(), sign.Sign)
	if err != nil {
		return nil, err
	}
	pubkeyByte := crypto.FromECDSAPub(pubkey)
	member, err := e.VerifyPublicKey(sign.FastHeight, pubkeyByte)
	return member, err
}

// VerifySigns verify signatures of bft committee in batches
func (e *Election) VerifySigns(signs []*types.PbftSign) ([]*types.CommitteeMember, []error) {
	members := make([]*types.CommitteeMember, len(signs))
	errs := make([]error, len(signs))

	if len(signs) == 0 {
		log.Warn("Veriry signs get nil pbftsigns")
		return nil, nil
	}
	// All signs should have the same fastblock height
	committeeMembers := e.GetCommittee(signs[0].FastHeight)
	if len(committeeMembers) == 0 {
		log.Error("Election get none committee for verify pbft signs")
		for i := range errs {
			errs[i] = ErrCommittee
		}
		return members, errs
	}

	for i, sign := range signs {
		// member, err := e.VerifySign(sign)
		pubkey, _ := crypto.SigToPub(sign.HashWithNoSign().Bytes(), sign.Sign)
		member := e.GetMemberByPubkey(committeeMembers, crypto.FromECDSAPub(pubkey))
		if member == nil {
			errs[i] = errors.New(fmt.Sprintf("%s %d ", ErrInvalidMember.Error(), len(committeeMembers)))
		} else {
			members[i] = member
		}
	}
	return members, errs
}

// VerifySwitchInfo verify committee members and it's state
func (e *Election) VerifySwitchInfo(fastNumber *big.Int, info []*types.CommitteeMember) error {
	if e.singleNode == true {
		return nil
	}
	begin, members := e.getMembers(fastNumber)
	if begin == nil || members == nil {
		log.Error("Failed to fetch elected committee", "fast", fastNumber)
		return ErrCommittee
	}
	if begin.Cmp(fastNumber) == 0 && len(members) == len(info) {
		for i := range info {
			if !info[i].Compared(members[i]) {
				log.Error("SwitchInfo members invalid", "num", fastNumber)
				return ErrInvalidSwitch
			}
		}
	}

	return nil
}

func (e *Election) getGenesisCommittee() []*types.CommitteeMember {
	block := e.fastchain.GetBlockByNumber(0)
	if block != nil {
		return block.SwitchInfos()
	}
	return nil
}

func (e *Election) getElectionMembers(snailBeginNumber *big.Int, snailEndNumber *big.Int) *types.ElectionCommittee {
	// Locate committee id by election snailblock interval
	committeeNum := new(big.Int).Div(new(big.Int).Add(snailEndNumber, params.SnailConfirmInterval), params.ElectionPeriodNumber)

	if new(big.Int).Add(snailEndNumber, params.SnailConfirmInterval).Cmp(params.ElectionPeriodNumber) < 0 {
		committeeNum = common.Big0
	}

	if cache, ok := e.commiteeCache.Get(committeeNum.Uint64()); ok {
		committee := cache.(*types.ElectionCommittee)
		return committee
	}

	blockNum := new(big.Int).Add(e.getLastNumber(snailBeginNumber, snailEndNumber), common.Big1).Uint64()
	block := e.fastchain.GetBlockByNumber(blockNum)
	if block != nil {
		var (
			members []*types.CommitteeMember
			backups []*types.CommitteeMember
		)
		for _, m := range e.fastchain.GetBlockByNumber(blockNum).SwitchInfos() {
			if m.Flag == types.StateUsedFlag {
				members = append(members, m)
			}
			if m.Flag == types.StateUnusedFlag {
				backups = append(backups, m)
			}
		}
		committee := &types.ElectionCommittee{Members: members, Backups: backups}
		e.commiteeCache.Add(committeeNum.Uint64(), committee)
		return committee
	}

	// Elect members from snailblock
	members := ElectCommittee(e.snailchain, e.defaultMembers, snailBeginNumber, snailEndNumber)

	// Cache committee members for next access
	e.commiteeCache.Add(committeeNum.Uint64(), members)

	return members
}

func (e *Election) getValidators(fastNumber *big.Int) []*types.CommitteeMember {
	epoch := types.GetEpochFromHeight(fastNumber.Uint64())
	current := e.fastchain.CurrentBlock().Number()

	if cache, ok := e.epochCache.Get(epoch.EpochID); ok {
		members := cache.(*[]*types.CommitteeMember)
		return *members
	}

	if current.Cmp(fastNumber) > 0 {
		// Read committee from block body
		block := e.fastchain.GetBlockByNumber(epoch.BeginHeight)
		if block != nil {
			var (
				members []*types.CommitteeMember
				backups []*types.CommitteeMember
			)
			for _, m := range e.fastchain.GetBlockByNumber(epoch.BeginHeight).SwitchInfos() {
				if m.Flag == types.StateUsedFlag {
					members = append(members, m)
				}
				if m.Flag == types.StateUnusedFlag {
					backups = append(backups, m)
				}
			}
			committee := &types.ElectionCommittee{Members: members, Backups: backups}
			// cache validators by epoch
			e.epochCache.Add(epoch.EpochID, &committee.Members)
			return committee.Members
		}
	}
	log.Info("getValidators in state", "number", fastNumber, "current", current)
	block := e.fastchain.CurrentBlock()
	stateDb, err := e.fastchain.StateAt(block.Root())
	if err != nil {
		log.Warn("Fetch committee from state failed", "number", fastNumber, "err", err)
		return nil
	}
	validators := vm.GetValidatorsByEpoch(stateDb, epoch.EpochID, fastNumber.Uint64())
	if len(validators) > 0 {
		e.epochCache.Add(epoch.EpochID, &validators)
	}
	return validators
}

// getCommittee returns the committee members who propose this fast block
func (e *Election) getCommittee(fastNumber *big.Int, snailNumber *big.Int) *committee {
	log.Debug("get committee ..", "fastnumber", fastNumber, "snailnumber", snailNumber)
	committeeNumber := new(big.Int).Div(snailNumber, params.ElectionPeriodNumber)
	lastSnailNumber := new(big.Int).Mul(committeeNumber, params.ElectionPeriodNumber)
	firstSnailNumber := new(big.Int).Add(new(big.Int).Sub(lastSnailNumber, params.ElectionPeriodNumber), common.Big1)

	switchCheckNumber := new(big.Int).Sub(lastSnailNumber, params.SnailConfirmInterval)

	log.Debug("get pre committee ", "committee", committeeNumber, "first", firstSnailNumber, "last", lastSnailNumber, "switchcheck", switchCheckNumber)

	if committeeNumber.Cmp(common.Big0) == 0 {
		// genesis committee
		log.Debug("get genesis committee")
		return &committee{
			id:                  new(big.Int).Set(common.Big0),
			beginFastNumber:     new(big.Int).Set(common.Big1),
			endFastNumber:       new(big.Int).Set(common.Big0),
			firstElectionNumber: new(big.Int).Set(common.Big0),
			lastElectionNumber:  new(big.Int).Set(common.Big0),
			switchCheckNumber:   params.ElectionPeriodNumber,
			members:             e.genesisCommittee,
			switches:            rawdb.ReadCommitteeStates(e.snailchain.GetDatabase(), 0),
		}
	}

	endElectionNumber := new(big.Int).Set(switchCheckNumber)
	beginElectionNumber := new(big.Int).Add(new(big.Int).Sub(endElectionNumber, params.ElectionPeriodNumber), common.Big1)
	if beginElectionNumber.Cmp(common.Big0) <= 0 {
		beginElectionNumber = new(big.Int).Set(common.Big1)
	}

	// find the last committee end fastblock number
	lastFastNumber := e.getLastNumber(beginElectionNumber, endElectionNumber)
	if lastFastNumber == nil {
		return nil
	}

	log.Debug("check last fast block", "committee", committeeNumber, "last fast", lastFastNumber, "current", fastNumber)
	//genesis committee is long committee ,it's 180 snail block and 9600 fast black
	if lastFastNumber.Cmp(fastNumber) >= 0 {
		if committeeNumber.Cmp(common.Big1) == 0 {
			// still at genesis committee
			log.Debug("get genesis committee")
			return &committee{
				id:                  new(big.Int).Set(common.Big0),
				beginFastNumber:     new(big.Int).Set(common.Big1),
				endFastNumber:       lastFastNumber,
				firstElectionNumber: new(big.Int).Set(common.Big0),
				lastElectionNumber:  new(big.Int).Set(common.Big0),
				switchCheckNumber:   params.ElectionPeriodNumber,
				members:             e.genesisCommittee,
				switches:            rawdb.ReadCommitteeStates(e.snailchain.GetDatabase(), 0),
			}
		}
		// get pre snail block to elect current committee
		preEndElectionNumber := new(big.Int).Sub(switchCheckNumber, params.ElectionPeriodNumber)
		preBeginElectionNumber := new(big.Int).Add(new(big.Int).Sub(preEndElectionNumber, params.ElectionPeriodNumber), common.Big1)
		if preBeginElectionNumber.Cmp(common.Big0) < 1 {
			preBeginElectionNumber = new(big.Int).Set(common.Big1)
		}
		preEndFast := e.getLastNumber(preBeginElectionNumber, preEndElectionNumber)
		if preEndFast == nil {
			return nil
		}

		log.Debug("get committee", "electFirst", preBeginElectionNumber, "electLast", preEndElectionNumber, "lastFast", preEndFast)

		members := e.getElectionMembers(preBeginElectionNumber, preEndElectionNumber)
		return &committee{
			id:                  new(big.Int).Sub(committeeNumber, common.Big1),
			beginFastNumber:     new(big.Int).Add(preEndFast, common.Big1),
			endFastNumber:       lastFastNumber,
			firstElectionNumber: preBeginElectionNumber,
			lastElectionNumber:  preEndElectionNumber,
			switchCheckNumber:   lastSnailNumber,
			members:             members.Members,
			backupMembers:       members.Backups,
			switches:            rawdb.ReadCommitteeStates(e.snailchain.GetDatabase(), new(big.Int).Sub(committeeNumber, common.Big1).Uint64()),
		}
	}

	log.Debug("get committee", "electFirst", beginElectionNumber, "electLast", endElectionNumber, "lastFast", lastFastNumber)

	members := e.getElectionMembers(beginElectionNumber, endElectionNumber)
	return &committee{
		id:                  committeeNumber,
		beginFastNumber:     new(big.Int).Add(lastFastNumber, common.Big1),
		endFastNumber:       new(big.Int).Set(common.Big0),
		firstElectionNumber: beginElectionNumber,
		lastElectionNumber:  endElectionNumber,
		switchCheckNumber:   new(big.Int).Add(lastSnailNumber, params.ElectionPeriodNumber),
		members:             members.Members,
		backupMembers:       members.Backups,
		switches:            rawdb.ReadCommitteeStates(e.snailchain.GetDatabase(), committeeNumber.Uint64()),
	}
}

// GetCommittee gets committee members which propose this fast block
func (e *Election) electedCommittee(fastNumber *big.Int) *committee {
	if e.electionMode == ElectModeFake {
		return e.committee
	}

	fastHeadNumber := e.fastchain.CurrentHeader().Number
	snailHeadNumber := e.snailchain.CurrentHeader().Number
	e.mu.RLock()
	currentCommittee := e.committee
	nextCommittee := e.nextCommittee
	e.mu.RUnlock()

	if nextCommittee != nil && fastNumber.Cmp(nextCommittee.beginFastNumber) >= 0 {
		return nextCommittee
	}
	if currentCommittee != nil && fastNumber.Cmp(currentCommittee.beginFastNumber) >= 0 {
		if common.Big0.Cmp(currentCommittee.endFastNumber) == 0 {
			return currentCommittee
		} else {
			if fastNumber.Cmp(currentCommittee.endFastNumber) <= 0 {
				return currentCommittee
			}
		}
	}

	fastBlock := e.fastchain.GetBlockByNumber(fastNumber.Uint64())
	if fastBlock == nil {
		log.Info("get committee failed (no fast block)", "fastnumber", fastNumber, "currentNumber", fastHeadNumber)
		return nil
	}
	// get snail number
	var snailNumber *big.Int
	snailBlock, _ := e.snailchain.GetFruitByFastHash(fastBlock.Hash())
	if snailBlock == nil {
		// fast block has not stored in snail chain
		// TODO: when fast number is so far away from snail block
		snailNumber = snailHeadNumber
	} else {
		snailNumber = snailBlock.Number()
	}

	committee := e.getCommittee(fastNumber, snailNumber)
	if committee == nil {
		return nil
	}

	return committee
}

// GetCommittee gets committee members propose this fast block
func (e *Election) GetCommittee(fastNumber *big.Int) []*types.CommitteeMember {
	var members []*types.CommitteeMember

	if e.IsTIP8(fastNumber) {
		// Apply validators at stake from contract and blockchain
		return e.getValidators(fastNumber)
	}

	committee := e.electedCommittee(fastNumber)
	if committee == nil {
		log.Error("Failed to fetch elected committee", "fast", fastNumber)
		return nil
	}
	if len(committee.switches) == 0 {
		return committee.Members()
	}

	states := make(map[common.Address]uint32)
	if fastNumber.Cmp(committee.switches[len(committee.switches)-1]) > 0 {
		// Apply all committee state switches for latest block
		for _, num := range committee.switches {
			b := e.fastchain.GetBlockByNumber(num.Uint64())
			if b == nil {
				log.Warn("Switch block not exists", "number", num)
				break
			}
			for _, s := range b.SwitchInfos() {
				switch s.Flag {
				case types.StateAppendFlag:
					states[s.CommitteeBase] = types.StateAppendFlag
				case types.StateRemovedFlag:
					states[s.CommitteeBase] = types.StateRemovedFlag
				}
			}
		}
	} else {
		for _, num := range committee.switches {
			if num.Cmp(fastNumber) >= 0 {
				break
			}
			b := e.fastchain.GetBlockByNumber(num.Uint64())
			if b == nil {
				log.Warn("Switch block not exists", "number", num)
				break
			}
			for _, s := range b.SwitchInfos() {
				switch s.Flag {
				case types.StateAppendFlag:
					states[s.CommitteeBase] = types.StateAppendFlag
				case types.StateRemovedFlag:
					states[s.CommitteeBase] = types.StateRemovedFlag
				}
			}
		}
	}

	for _, m := range committee.Members() {
		if flag, ok := states[m.CommitteeBase]; ok {
			if flag != types.StateRemovedFlag {
				members = append(members, m)
			}
		} else {
			members = append(members, m)
		}
	}
	for _, m := range committee.BackupMembers() {
		if flag, ok := states[m.CommitteeBase]; ok {
			if flag == types.StateAppendFlag {
				members = append(members, m)
			}
		}
	}

	return members
}

// GetCommitteeById return committee info sepecified by Committee ID
func (e *Election) GetCommitteeById(id *big.Int) map[string]interface{} {
	info := make(map[string]interface{})
	if id.Cmp(e.chainConfig.TIP8.CID) >= 0 {
		epoch := types.GetEpochFromID(id.Uint64())
		members := e.getValidators(big.NewInt(int64(epoch.BeginHeight)))
		if members == nil {
			log.Error("GetCommitteeById failed", "epoch", epoch)
			return nil
		}
		info["id"] = id.Uint64()
		info["memberCount"] = len(members)
		// info["beginSnailNumber"] = beginElectionNumber.Uint64()
		// info["endSnailNumber"] = endElectionNumber.Uint64()
		info["members"] = membersDisplay(members)
		// info["backups"] = membersDisplay(elected.Backups)
		info["beginNumber"] = epoch.BeginHeight
		info["endNumber"] = epoch.EndHeight
		return info
	} else {

		e.mu.RLock()
		currentCommittee := e.committee
		e.mu.RUnlock()

		if currentCommittee != nil && currentCommittee.id.Cmp(id) < 0 {
			return nil
		}
		if id.Cmp(common.Big0) <= 0 {
			// Use genesis committee
			info["id"] = 0
			info["beginSnailNumber"] = 0
			info["endSnailNumber"] = 0
			info["memberCount"] = len(e.genesisCommittee)
			info["members"] = membersDisplay(e.genesisCommittee)
			info["beginNumber"] = 1
			info["endNumber"] = nil
			if currentCommittee != nil && currentCommittee.id.Cmp(id) == 0 {
				// Committee end fast number may not be available when current snail lower than commiteeId * period
				if currentCommittee.endFastNumber != nil && currentCommittee.endFastNumber.Uint64() > 0 {
					info["endNumber"] = currentCommittee.endFastNumber.Uint64()
				}
			} else {
				end := new(big.Int).Sub(params.ElectionPeriodNumber, params.SnailConfirmInterval)
				info["endNumber"] = e.getLastNumber(big.NewInt(1), end).Uint64()
			}
			return info
		}
		// Calclulate election members from previous election period
		endElectionNumber := new(big.Int).Mul(id, params.ElectionPeriodNumber)
		endElectionNumber.Sub(endElectionNumber, params.SnailConfirmInterval)
		beginElectionNumber := new(big.Int).Add(new(big.Int).Sub(endElectionNumber, params.ElectionPeriodNumber), common.Big1)
		if beginElectionNumber.Cmp(common.Big0) <= 0 {
			beginElectionNumber = new(big.Int).Set(common.Big1)
		}

		elected := e.getElectionMembers(beginElectionNumber, endElectionNumber)
		if elected != nil {
			info["id"] = id.Uint64()
			info["memberCount"] = len(elected.Members) + len(elected.Backups)
			info["beginSnailNumber"] = beginElectionNumber.Uint64()
			info["endSnailNumber"] = endElectionNumber.Uint64()
			info["members"] = membersDisplay(elected.Members)
			info["backups"] = membersDisplay(elected.Backups)
			info["beginNumber"] = new(big.Int).Add(e.getLastNumber(beginElectionNumber, endElectionNumber), common.Big1).Uint64()
			info["endNumber"] = nil
			// Committee end fast number may be nil if current committee is working on
			if currentCommittee != nil && currentCommittee.id.Cmp(id) == 0 {
				// Committee end fast number may not be available when current snail lower than commiteeId * period
				if currentCommittee.endFastNumber != nil && currentCommittee.endFastNumber.Uint64() > 0 {
					info["endNumber"] = currentCommittee.endFastNumber.Uint64()
				}
			} else {
				begin := new(big.Int).Add(beginElectionNumber, params.ElectionPeriodNumber)
				end := new(big.Int).Add(endElectionNumber, params.ElectionPeriodNumber)
				info["endNumber"] = e.getLastNumber(begin, end).Uint64()
			}
			return info
		}
	}

	return nil
}
func (e *Election) getMembers(fastNumber *big.Int) (*big.Int, []*types.CommitteeMember) {
	if e.IsTIP8(fastNumber) {
		epoch := types.GetEpochFromHeight(fastNumber.Uint64())
		return new(big.Int).SetUint64(epoch.BeginHeight), e.getValidators(fastNumber)
	} else {
		committee := e.electedCommittee(fastNumber)
		if committee == nil {
			log.Error("Failed to fetch elected committee", "fast", fastNumber)
			return nil, nil
		}
		members := committee.members
		members = append(members, committee.backupMembers...)
		return committee.beginFastNumber, members
	}
}
func membersDisplay(members []*types.CommitteeMember) []map[string]interface{} {
	var attrs []map[string]interface{}
	for _, member := range members {
		attrs = append(attrs, map[string]interface{}{
			"coinbase": member.Coinbase,
			"PKey":     hex.EncodeToString(member.Publickey),
			"flag":     member.Flag,
			"type":     member.MType,
		})
	}
	return attrs
}

type snailReader interface {
	GetFruitsHead(number uint64) []*types.SnailHeader

	GetHeaderByNumber(number uint64) *types.SnailHeader
}

// getCandinates get candinate miners and seed from given snail blocks
func getCandinates(snailchain snailReader, snailBeginNumber *big.Int, snailEndNumber *big.Int) (common.Hash, []*candidateMember) {
	var fruitsCount = make(map[common.Address]uint64)
	var members []*candidateMember

	var seed []byte

	// get all fruits want to be elected and their pubic key is valid
	for blockNumber := snailBeginNumber; blockNumber.Cmp(snailEndNumber) <= 0; {
		block := snailchain.GetHeaderByNumber(blockNumber.Uint64())
		if block == nil {
			return common.Hash{}, nil
		}

		seed = append(seed, block.Hash().Bytes()...)
		fruits := snailchain.GetFruitsHead(blockNumber.Uint64())
		for _, f := range fruits {
			if len(f.Publickey) > 0 {
				pubkey, err := crypto.UnmarshalPubkey(f.Publickey)
				if err != nil {
					continue
				}
				addr := crypto.PubkeyToAddress(*pubkey)

				act, diff := f.GetDifficulty(true)

				member := &candidateMember{
					coinbase:   f.Coinbase,
					publickey:  pubkey,
					address:    addr,
					difficulty: new(big.Int).Sub(act, diff),
				}

				members = append(members, member)
				if _, ok := fruitsCount[addr]; ok {
					fruitsCount[addr]++
				} else {
					fruitsCount[addr] = 1
				}
			}
		}
		blockNumber = new(big.Int).Add(blockNumber, big.NewInt(1))
	}

	log.Debug("get committee candidate", "fruit", len(members), "members", len(fruitsCount))

	var candidates []*candidateMember
	td := big.NewInt(0)
	for _, member := range members {
		if cnt, ok := fruitsCount[member.address]; ok {
			log.Trace("get committee candidate", "keyAddr", member.address, "count", cnt, "diff", member.difficulty)
			if cnt >= params.ElectionFruitsThreshold {
				td.Add(td, member.difficulty)

				candidates = append(candidates, member)
			}
		}
	}
	log.Debug("get final candidate", "count", len(candidates), "td", td)
	if len(candidates) == 0 {
		log.Debug("Get none candidates")
		return common.Hash{}, nil
	}

	dd := big.NewInt(0)
	rate := new(big.Int).Div(maxUint256, td)
	for i, member := range candidates {
		member.lower = new(big.Int).Mul(rate, dd)

		dd = new(big.Int).Add(dd, member.difficulty)

		if i == len(candidates)-1 {
			member.upper = new(big.Int).Set(maxUint256)
		} else {
			member.upper = new(big.Int).Mul(rate, dd)
		}

		log.Trace("get power", "member", member.address, "lower", member.lower, "upper", member.upper)
	}

	return crypto.Keccak256Hash(seed), candidates
}

//getLastNumber is the endSanil's last fruit's number add 9600
func (e *Election) getLastNumber(beginSnail, endSnail *big.Int) *big.Int {

	beginElectionBlock := e.snailchain.GetBlockByNumber(beginSnail.Uint64())
	if beginElectionBlock == nil {
		return nil
	}
	endElectionBlock := e.snailchain.GetBlockByNumber(endSnail.Uint64())
	if endElectionBlock == nil {
		return nil
	}

	fruits := endElectionBlock.Fruits()
	lastFruitNumber := fruits[len(fruits)-1].FastNumber()
	lastFastNumber := new(big.Int).Add(lastFruitNumber, params.ElectionSwitchoverNumber)

	return lastFastNumber
}

func (e *Election) getEndFast(id *big.Int) *big.Int {
	var (
		snailStartNumber *big.Int
		snailEndNumber   *big.Int
	)

	switchCheckNumber := new(big.Int).Mul(new(big.Int).Add(id, common.Big1), params.ElectionPeriodNumber)
	snailEndNumber = new(big.Int).Sub(switchCheckNumber, params.SnailConfirmInterval)
	if snailEndNumber.Cmp(params.ElectionPeriodNumber) < 0 {
		snailStartNumber = new(big.Int).Set(common.Big1)
	} else {
		snailStartNumber = new(big.Int).Add(new(big.Int).Sub(snailEndNumber, params.ElectionPeriodNumber), common.Big1)
	}
	return e.getLastNumber(snailStartNumber, snailEndNumber)
}

// elect is a lottery function that select committee members from candidates miners
func elect(defaultMembers []*types.CommitteeMember, candidates []*candidateMember, seed common.Hash) []*types.CommitteeMember {
	var addrs = make(map[common.Address]uint)
	var members []*types.CommitteeMember
	var defaults = make(map[common.Address]*types.CommitteeMember)

	for _, g := range defaultMembers {
		defaults[g.CommitteeBase] = g
	}
	log.Debug("elect committee members ..", "count", len(candidates), "seed", seed)
	round := new(big.Int).Set(common.Big1)
	for {
		seedNumber := new(big.Int).Add(seed.Big(), round)
		hash := crypto.Keccak256Hash(seedNumber.Bytes())
		//prop := new(big.Int).Div(maxUint256, hash.Big())
		prop := hash.Big()

		for _, cm := range candidates {
			if prop.Cmp(cm.lower) < 0 {
				continue
			}
			if prop.Cmp(cm.upper) >= 0 {
				continue
			}

			log.Trace("get member", "seed", hash, "member", cm.address, "prop", prop)
			if _, ok := defaults[cm.address]; ok {
				// No need to select default committee member
				break
			}
			if _, ok := addrs[cm.address]; ok {
				break
			}
			addrs[cm.address] = 1
			member := &types.CommitteeMember{
				Coinbase:      cm.coinbase,
				CommitteeBase: crypto.PubkeyToAddress(*cm.publickey),
				Publickey:     crypto.FromECDSAPub(cm.publickey),
				Flag:          types.StateUnusedFlag,
			}
			members = append(members, member)

			break
		}

		round = new(big.Int).Add(round, common.Big1)
		if round.Cmp(params.MaximumCommitteeNumber) > 0 {
			break
		}
	}

	log.Debug("get new committee members", "count", len(members))

	return members
}

// ElectCommittee elect committee members from snail block.
func ElectCommittee(snailchain snailReader, defaultMembers []*types.CommitteeMember, snailBeginNumber *big.Int, snailEndNumber *big.Int) *types.ElectionCommittee {
	log.Info("elect new committee..", "begin", snailBeginNumber, "end", snailEndNumber,
		"threshold", params.ElectionFruitsThreshold, "max", params.MaximumCommitteeNumber)

	var (
		committee types.ElectionCommittee
		members   []*types.CommitteeMember
	)
	seed, candidates := getCandinates(snailchain, snailBeginNumber, snailEndNumber)
	if candidates == nil {
		log.Warn("Candidates empty retain default members", "begin", snailBeginNumber, "end", snailEndNumber)
	} else {
		var (
			all      []*types.CommitteeMember
			addrs    = make(map[common.Address]*types.CommitteeMember)
			defaults = make(map[common.Address]*types.CommitteeMember)
		)
		for _, g := range defaultMembers {
			defaults[g.CommitteeBase] = g
		}
		for _, cm := range candidates {
			if _, ok := defaults[cm.address]; ok {
				// Filter default committee members
				continue
			}
			if _, ok := addrs[cm.address]; ok {
				continue
			}
			addrs[cm.address] = &types.CommitteeMember{
				Coinbase:      cm.coinbase,
				CommitteeBase: crypto.PubkeyToAddress(*cm.publickey),
				Publickey:     crypto.FromECDSAPub(cm.publickey),
				Flag:          types.StateUnusedFlag,
			}
			all = append(all, addrs[cm.address])
		}
		log.Info("Candidates addrs", "count", len(all))
		if len(all) > params.ProposalCommitteeNumber {
			members = elect(defaultMembers, candidates, seed)
		} else {
			// Apply the whole candidates
			log.Info("Apply all candidates", "begin", snailBeginNumber, "end", snailEndNumber)
			members = all
		}
	}
	if len(members) > params.ProposalCommitteeNumber {
		// Split elected candidates into members and backups
		committee.Members = members[:params.ProposalCommitteeNumber]
		committee.Backups = members[params.ProposalCommitteeNumber:]
	} else {
		committee.Members = members
	}

	for _, member := range committee.Members {
		member.Flag = types.StateUsedFlag
		member.MType = types.TypeWorked
	}
	for _, member := range committee.Backups {
		member.MType = types.TypeBack
	}

	if len(committee.Members) >= params.MinimumCommitteeNumber {
		committee.Backups = append(committee.Backups, defaultMembers...)
	} else {
		// PBFT need a minimum 3f+1 members
		// Use genesis committee as default committee
		log.Warn("Append default committee members", "elected", len(committee.Members), "begin", snailBeginNumber, "end", snailEndNumber)
		for _, m := range defaultMembers {
			var member = *m
			member.Flag = types.StateUsedFlag
			committee.Members = append(committee.Members, &member)
		}
	}

	return &committee
}

// calcCommittee return the sepecific committee when current block is bigger than switch check number
func (e *Election) calcCommittee(id *big.Int) *committee {
	var (
		snailStartNumber *big.Int
		snailEndNumber   *big.Int
	)
	if id.Cmp(common.Big0) == 0 {
		return nil
	}
	switchCheckNumber := new(big.Int).Mul(id, params.ElectionPeriodNumber)
	snailEndNumber = new(big.Int).Sub(switchCheckNumber, params.SnailConfirmInterval)
	if snailEndNumber.Cmp(params.ElectionPeriodNumber) < 0 {
		snailStartNumber = new(big.Int).Set(common.Big1)
	} else {
		snailStartNumber = new(big.Int).Add(new(big.Int).Sub(snailEndNumber, params.ElectionPeriodNumber), common.Big1)
	}

	members := e.getElectionMembers(snailStartNumber, snailEndNumber)
	lastFastNumber := e.getLastNumber(snailStartNumber, snailEndNumber)

	return &committee{
		id:                  id,
		firstElectionNumber: snailStartNumber,
		lastElectionNumber:  snailEndNumber,
		beginFastNumber:     new(big.Int).Add(lastFastNumber, common.Big1),
		endFastNumber:       big.NewInt(0),
		switchCheckNumber:   new(big.Int).Add(switchCheckNumber, params.ElectionPeriodNumber),
		members:             members.Members,
		backupMembers:       members.Backups,
	}
}

// filterWithSwitchInfo return committee members which are applied all switchinfo changes
func (e *Election) filterWithSwitchInfo(c *committee) (members, backups []*types.CommitteeMember) {
	members = c.Members()
	backups = c.BackupMembers()
	if len(c.switches) == 0 {
		log.Info("Committee filter get no switch infos", "id", c.id)
		return
	}

	// Apply all committee state switches for latest block
	states := make(map[common.Address]uint32)
	for _, num := range c.switches {
		b := e.fastchain.GetBlockByNumber(num.Uint64())
		for _, s := range b.SwitchInfos() {
			switch s.Flag {
			case types.StateAppendFlag:
				states[s.CommitteeBase] = types.StateAppendFlag
			case types.StateRemovedFlag:
				states[s.CommitteeBase] = types.StateRemovedFlag
			}
		}
	}
	for k, flag := range states {
		enums := map[uint32]string{
			types.StateAppendFlag:  "add",
			types.StateRemovedFlag: "drop",
		}
		log.Info("Committee switch transition", "bftkey", k, "state", enums[flag], "committee", c.id)
	}

	for i, m := range members {
		if flag, ok := states[m.CommitteeBase]; ok {
			if flag == types.StateRemovedFlag {
				// Update the committee member state
				var switched = *m
				switched.Flag = types.StateRemovedFlag
				members[i] = &switched
			}
		}
	}
	for i, m := range backups {
		if flag, ok := states[m.CommitteeBase]; ok {
			if flag == types.StateAppendFlag {
				// Update the committee member state
				var switched = *m
				switched.Flag = types.StateUsedFlag
				backups[i] = &switched
			}
			if flag == types.StateRemovedFlag {
				// Update the committee member state
				var switched = *m
				switched.Flag = types.StateRemovedFlag
				backups[i] = &switched
			}
		}
	}
	return
}

// switchMembers update Committee members if switchinfo found in block
func (e *Election) switchMembers(fastNumber *big.Int, infos []*types.CommitteeMember) {
	if len(infos) == 0 {
		return
	}

	committee := e.committee
	if committee == nil {
		log.Warn("Election update switchinfo get no Committee", "block", fastNumber)
		return
	}
	// Committee switch block numbers array should be nil at committee start block
	if committee.beginFastNumber.Cmp(fastNumber) == 0 {
		if len(committee.switches) > 0 {
			log.Info("Reset committee switchinfo on start block", "committee", committee.id, "current", fastNumber)
			committee.switches = nil
			rawdb.WriteCommitteeStates(e.snailchain.GetDatabase(), committee.id.Uint64(), nil)
		}
		return
	}

	// Store all switch block number
	log.Info("Election update committee member state", "block", fastNumber)
	committee.switches = append(committee.switches, fastNumber)
	rawdb.WriteCommitteeStates(e.snailchain.GetDatabase(), committee.id.Uint64(), committee.switches)

	// Update pbft server's committee info via pbft agent proxy
	members, backups := e.filterWithSwitchInfo(committee)
	endfast := committee.endFastNumber
	if endfast == nil {
		endfast = big.NewInt(0)
	}
	e.electionFeed.Send(types.ElectionEvent{
		Option:           types.CommitteeUpdate,
		CommitteeID:      committee.id,
		BeginFastNumber:  fastNumber,
		EndFastNumber:    endfast,
		CommitteeMembers: members,
		BackupMembers:    backups,
	})
}

// FinalizeCommittee upddate current committee state
func (e *Election) FinalizeCommittee(block *types.Block) error {
	if block == nil {
		log.Error("Finalize committee get nil block")
		return nil
	}
	if e.IsTIP8(new(big.Int).Set(block.Number())) {
		// No need to do retrieve election from PoW
		return nil
	}

	info := block.SwitchInfos()
	if len(info) > 0 {
		log.Info("Election receive committee switch block", "block", block.Number())
		e.switchMembers(block.Number(), info)
	}

	if e.committee.endFastNumber.Cmp(block.Number()) == 0 {
		// Current committee completed, switch next
		log.Info("****switchNext on Last fastblock in current epoch")
		e.initCurrent()
		e.switchNext <- struct{}{}
	}
	return nil
}
func (e *Election) initCurrent() {
	if e.committee != nil {
		return
	}
	fastHeadNumber := e.fastchain.CurrentHeader().Number
	snailHeadNumber := e.snailchain.CurrentHeader().Number
	currentCommittee := e.getCommittee(fastHeadNumber, snailHeadNumber)
	if currentCommittee == nil {
		log.Crit("Election faiiled to get committee on start")
		return
	}
	// Rewind committee swtichinfo storage if blockchain rollbacks
	for i := 0; i < len(currentCommittee.switches); i++ {
		if currentCommittee.switches[i].Cmp(fastHeadNumber) > 0 {
			log.Info("Rewind committee switchinfo", "committee", currentCommittee.id, "current", fastHeadNumber)
			currentCommittee.switches = currentCommittee.switches[:i]
			rawdb.WriteCommitteeStates(e.snailchain.GetDatabase(), currentCommittee.id.Uint64(), currentCommittee.switches)
			break
		}
	}
	switchNum := new(big.Int).Add(currentCommittee.beginFastNumber, common.Big1)
	if len(currentCommittee.switches) > 0 {
		switchNum = new(big.Int).Add(currentCommittee.switches[len(currentCommittee.switches)-1], common.Big1)
	}
	for switchNum.Cmp(fastHeadNumber) <= 0 {
		block := e.fastchain.GetBlockByNumber(switchNum.Uint64())
		if block != nil && len(block.SwitchInfos()) > 0 {
			log.Info("Election append switch block height", "number", switchNum)
			currentCommittee.switches = append(currentCommittee.switches, switchNum)
			rawdb.WriteCommitteeStates(e.snailchain.GetDatabase(), currentCommittee.id.Uint64(), currentCommittee.switches)
		}
		switchNum = new(big.Int).Add(switchNum, common.Big1)
	}

	e.committee = currentCommittee

	if currentCommittee.endFastNumber.Cmp(common.Big0) > 0 {
		if e.committee.endFastNumber.Cmp(fastHeadNumber) == 0 {
			// committee has finish their work, start the new committee
			e.committee = e.calcCommittee(new(big.Int).Add(e.committee.id, common.Big1))
			e.nextCommittee = nil
			e.startSwitchover = false
		} else {
			e.prepare = true
		}
	}
}

// Start load current committ and starts election processing
func (e *Election) Start() error {
	// get current committee info
	fastHeadNumber := e.fastchain.CurrentHeader().Number
	// snailHeadNumber := e.snailchain.CurrentHeader().Number

	if e.IsTIP8(fastHeadNumber) {
		// No need to do retrieve committee from PoW and do election
		log.Info("Election enable stake at launch")
		e.disabled = true
		return nil
	} else {
		e.snailChainEventSub = e.snailchain.SubscribeChainEvent(e.snailChainEventCh)
		e.initCurrent()
		// send event to the subscripber
		go func(e *Election) {
			printCommittee(e.committee)
			members, backups := e.filterWithSwitchInfo(e.committee)
			e.electionFeed.Send(types.ElectionEvent{
				Option:           types.CommitteeSwitchover,
				CommitteeID:      e.committee.id,
				CommitteeMembers: members,
				BackupMembers:    backups,
				BeginFastNumber:  e.committee.beginFastNumber,
			})
			e.electionFeed.Send(types.ElectionEvent{
				Option:           types.CommitteeStart,
				CommitteeID:      e.committee.id,
				CommitteeMembers: members,
				BackupMembers:    backups,
				BeginFastNumber:  e.committee.beginFastNumber,
			})
		}(e)
	}
	// Start the event loop and return
	go e.loop()

	return nil
}

// Monitor both chains and trigger elections at the same time
func (e *Election) loop() {
	// Elect next committee on start
	if e.prepare {
		next := new(big.Int).Add(e.committee.id, common.Big1)
		log.Info("Election calc next committee on start", "committee", next)
		e.nextCommittee = e.calcCommittee(next)
		e.startSwitchover = true
		e.electionFeed.Send(types.ElectionEvent{
			Option:           types.CommitteeOver,
			CommitteeID:      e.committee.id,
			CommitteeMembers: e.committee.Members(),
			BackupMembers:    e.committee.BackupMembers(),
			BeginFastNumber:  e.committee.beginFastNumber,
			EndFastNumber:    e.committee.endFastNumber,
		})
		if e.isTIP8FromCID(e.committee.id.Uint64()) {
			e.startSwitchover = false
		} else {
			e.electionFeed.Send(types.ElectionEvent{
				Option:           types.CommitteeSwitchover,
				CommitteeID:      e.nextCommittee.id,
				CommitteeMembers: e.nextCommittee.Members(),
				BackupMembers:    e.nextCommittee.BackupMembers(),
				BeginFastNumber:  e.nextCommittee.beginFastNumber,
			})
			log.Info("Election switchover next on start", "id", e.nextCommittee.id, "startNumber", e.nextCommittee.beginFastNumber)
		}
	}

	// Calculate commitee and switchover via fast and snail event
	for {
		select {
		case se := <-e.snailChainEventCh:
			fastHeadNumber := e.fastchain.CurrentHeader().Number
			if e.IsTIP8(fastHeadNumber) {
				continue
			}
			if e.committee == nil {
				e.initCurrent()
			}
			if se.Block != nil && e.committee != nil && e.committee.switchCheckNumber.Cmp(se.Block.Number()) == 0 {
				//Record Numbers to open elections
				e.committee.endFastNumber = e.getEndFast(e.committee.id)
				e.electionFeed.Send(types.ElectionEvent{
					Option:           types.CommitteeOver, //only update committee end fast black
					CommitteeID:      e.committee.id,
					CommitteeMembers: e.committee.Members(),
					BeginFastNumber:  e.committee.beginFastNumber,
					EndFastNumber:    e.committee.endFastNumber,
				})
				log.Info("Election BFT committee election start..", "snail", se.Block.Number(), "endfast", e.committee.endFastNumber)
				if e.isTIP8FromCID(e.committee.id.Uint64()) {
					continue
				}
				nextCommittee := e.calcCommittee(new(big.Int).Add(e.committee.id, common.Big1))
				if e.nextCommittee != nil && e.nextCommittee.id.Cmp(nextCommittee.id) == 0 {
					// May make a duplicate committee switchover if snail forks
					continue
				}
				e.mu.Lock()
				e.nextCommittee = nextCommittee
				e.startSwitchover = true
				e.mu.Unlock()

				log.Info("Election switchover new committee", "id", e.nextCommittee.id, "startNumber", e.nextCommittee.beginFastNumber)
				printCommittee(e.nextCommittee)
				e.electionFeed.Send(types.ElectionEvent{
					Option:           types.CommitteeSwitchover, //update next committee
					CommitteeID:      e.nextCommittee.id,
					CommitteeMembers: e.nextCommittee.Members(),
					BackupMembers:    e.nextCommittee.BackupMembers(),
					BeginFastNumber:  e.nextCommittee.beginFastNumber,
				})
			}
		case <-e.switchNext:
			if e.startSwitchover && e.committee != nil {
				log.Info("Election stop committee..", "id", e.committee.id)
				e.electionFeed.Send(types.ElectionEvent{
					Option:           types.CommitteeStop,
					CommitteeID:      e.committee.id,
					CommitteeMembers: e.committee.Members(),
					BackupMembers:    e.committee.BackupMembers(),
					BeginFastNumber:  e.committee.beginFastNumber,
					EndFastNumber:    e.committee.endFastNumber,
				})

				e.mu.Lock()
				e.committee = e.nextCommittee
				e.nextCommittee = nil
				e.mu.Unlock()
				e.startSwitchover = false

				if e.committee.id.Cmp(e.chainConfig.TIP8.CID) > 0 {
					continue
				}
				log.Info("Election start new BFT committee", "id", e.committee.id)
				e.electionFeed.Send(types.ElectionEvent{
					Option:           types.CommitteeStart,
					CommitteeID:      e.committee.id,
					CommitteeMembers: e.committee.Members(),
					BackupMembers:    e.committee.BackupMembers(),
					BeginFastNumber:  e.committee.beginFastNumber,
				})
			}
		}
	}
}

// SubscribeElectionEvent adds a channel to feed on committee change event
func (e *Election) SubscribeElectionEvent(ch chan<- types.ElectionEvent) event.Subscription {
	return e.scope.Track(e.electionFeed.Subscribe(ch))
}

// SetEngine set election backend consesus
func (e *Election) SetEngine(engine consensus.Engine) {
	e.engine = engine
}
func (e *Election) IsTIP8(fastHeadNumber *big.Int) bool {
	return consensus.IsTIP8(fastHeadNumber, e.chainConfig, e.getSnailChainReader())
}
func (e *Election) isTIP8FromCID(cid uint64) bool {
	return new(big.Int).SetUint64(cid).Cmp(e.chainConfig.TIP8.CID) >= 0
}
func (e *Election) getSnailChainReader() consensus.SnailChainReader {
	if e.snailchain != nil {
		p, ok := (e.snailchain).(*snailchain.SnailBlockChain)
		if ok {
			return p
		}
	}
	return nil
}

func printCommittee(c *committee) {
	log.Info("Committee Info", "ID", c.id, "count", len(c.members), "start", c.beginFastNumber)
	for _, member := range c.members {
		log.Info("Committee member: ", "PKey", hex.EncodeToString(member.Publickey), "coinbase", member.Coinbase)
	}
	for _, member := range c.backupMembers {
		log.Info("Committee backup: ", "PKey", hex.EncodeToString(member.Publickey), "coinbase", member.Coinbase)
	}
}
