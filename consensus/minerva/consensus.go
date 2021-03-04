// Copyright 2017 The go-ethereum Authors
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

package minerva

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"runtime"
	"time"

	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/common/math"
	"github.com/abeychain/go-abey/consensus"
	"github.com/abeychain/go-abey/core/state"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/core/vm"
	"github.com/abeychain/go-abey/log"
	"github.com/abeychain/go-abey/params"
)

// Minerva protocol constants.
var (
	allowedFutureBlockTime = 15 * time.Second // Max time from current time allowed for blocks, before they're considered future blocks
)

// Various error messages to mark blocks invalid. These should be private to
// prevent engine specific errors from being referenced in the remainder of the
// codebase, inherently breaking if the engine is swapped out. Please put common
// error types into the consensus package.
var (
	errLargeBlockTime    = errors.New("timestamp too big")
	errZeroBlockTime     = errors.New("timestamp equals parent's")
	errInvalidDifficulty = errors.New("non-positive difficulty")
	errInvalidMixDigest  = errors.New("invalid mix digest")
	errInvalidPoW        = errors.New("invalid proof-of-work")
	errInvalidFast       = errors.New("invalid fast number")
	//ErrRewardedBlock is returned if a block to import is already rewarded.
	ErrRewardedBlock = errors.New("block already rewarded")
	ErrRewardEnd     = errors.New("Reward end")
)

// Author implements consensus.Engine, returning the header's coinbase as the
// proof-of-work verified author of the block.
func (m *Minerva) Author(header *types.Header) (common.Address, error) {
	return common.Address{}, nil
}

//AuthorSnail return Snail mine coinbase
func (m *Minerva) AuthorSnail(header *types.SnailHeader) (common.Address, error) {
	return header.Coinbase, nil
}

// VerifyHeader checks whether a header conforms to the consensus rules of the
// stock Abeychain m engine.
func (m *Minerva) VerifyHeader(chain consensus.ChainReader, header *types.Header) error {
	// Short circuit if the header is known, or it's parent not
	number := header.Number.Uint64()

	parent := chain.GetHeader(header.ParentHash, number-1)
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}

	if chain.GetHeader(header.Hash(), number) != nil {
		return nil
	}

	if chain.GetHeaderByNumber(number) != nil {
		return consensus.ErrForkFastBlock
	}

	return m.verifyHeader(chain, header, parent)
}

func (m *Minerva) getParents(chain consensus.SnailChainReader, header *types.SnailHeader) []*types.SnailHeader {
	return GetParents(chain, header)
}

//GetParents the calc different need parents
func GetParents(chain consensus.SnailChainReader, header *types.SnailHeader) []*types.SnailHeader {
	number := header.Number.Uint64()
	period := params.DifficultyPeriod.Uint64()
	if number < period {
		period = number
	}
	//log.Info("getParents", "number", header.Number, "period", period)
	parents := make([]*types.SnailHeader, period)
	hash := header.ParentHash
	for i := uint64(1); i <= period; i++ {
		if number-i < 0 {
			break
		}
		parent := chain.GetHeader(hash, number-i)
		if parent == nil {
			log.Warn("getParents get parent failed.", "number", number-i, "hash", hash)
			return nil
		}
		parents[period-i] = parent
		hash = parent.ParentHash
	}

	return parents
}

//VerifySnailHeader verify snail Header number
func (m *Minerva) VerifySnailHeader(chain consensus.SnailChainReader, fastchain consensus.ChainReader, header *types.SnailHeader, seal bool, isFruit bool) error {
	// If we're running a full engine faking, accept any input as valid
	if m.config.PowMode == ModeFullFake {
		return nil
	}

	if isFruit {
		pointer := chain.GetHeader(header.PointerHash, header.PointerNumber.Uint64())
		if pointer == nil {
			log.Warn("VerifySnailHeader get pointer failed.", "fNumber", header.FastNumber, "pNumber", header.PointerNumber, "pHash", header.PointerHash)
			return consensus.ErrUnknownPointer
		}
		return m.verifySnailHeader(chain, fastchain, header, pointer, nil, false, seal, isFruit)
	}
	// Short circuit if the header is known, or it's parent not
	if chain.GetHeader(header.Hash(), header.Number.Uint64()) != nil {
		return nil
	}
	parents := m.getParents(chain, header)
	if parents == nil {
		return consensus.ErrUnknownAncestor
	}

	// Sanity checks passed, do a proper verification
	return m.verifySnailHeader(chain, fastchain, header, nil, parents, false, seal, isFruit)
}

// VerifyHeaders is similar to VerifyHeader, but verifies a batch of headers
// concurrently. The method returns a quit channel to abort the operations and
// a results channel to retrieve the async verifications.
func (m *Minerva) VerifyHeaders(chain consensus.ChainReader, headers []*types.Header,
	seals []bool) (chan<- struct{}, <-chan error) {
	// If we're running a full engine faking, accept any input as valid
	if m.config.PowMode == ModeFullFake || len(headers) == 0 {
		abort, results := make(chan struct{}), make(chan error, len(headers))
		for i := 0; i < len(headers); i++ {
			results <- nil
		}
		return abort, results
	}

	// Spawn as many workers as allowed threads
	workers := runtime.GOMAXPROCS(0)
	if len(headers) < workers {
		workers = len(headers)
	}

	// Create a task channel and spawn the verifiers
	var (
		inputs = make(chan int)
		done   = make(chan int, workers)
		errors = make([]error, len(headers))
		abort  = make(chan struct{})
	)
	for i := 0; i < workers; i++ {
		go func() {
			for index := range inputs {
				errors[index] = m.verifyHeaderWorker(chain, headers, seals, index)
				done <- index
			}
		}()
	}

	errorsOut := make(chan error, len(headers))
	go func() {
		defer close(inputs)
		var (
			in, out = 0, 0
			checked = make([]bool, len(headers))
			inputs  = inputs
		)
		for {
			select {
			case inputs <- in:
				if in++; in == len(headers) {
					// Reached end of headers. Stop sending to workers.
					inputs = nil
				}
			case index := <-done:
				for checked[index] = true; checked[out]; out++ {
					errorsOut <- errors[out]
					if out == len(headers)-1 {
						return
					}
				}
			case <-abort:
				return
			}
		}
	}()
	return abort, errorsOut
}

// VerifySnailHeaders verify snail headers
func (m *Minerva) VerifySnailHeaders(chain consensus.SnailChainReader, headers []*types.SnailHeader,
	seals []bool) (chan<- struct{}, <-chan error) {
	// If we're running a full engine faking, accept any input as valid
	if m.config.PowMode == ModeFullFake || len(headers) == 0 {
		abort, results := make(chan struct{}), make(chan error, len(headers))
		for i := 0; i < len(headers); i++ {
			results <- nil
		}
		return abort, results
	}

	// Spawn as many workers as allowed threads
	workers := runtime.GOMAXPROCS(0)
	if len(headers) < workers {
		workers = len(headers)
	}

	// Create a task channel and spawn the verifiers
	var (
		inputs = make(chan int)
		done   = make(chan int, workers)
		errs   = make([]error, len(headers))
		abort  = make(chan struct{})
	)

	parents := m.getParents(chain, headers[0])
	if parents == nil {
		abort, results := make(chan struct{}), make(chan error, len(headers))
		for i := 0; i < len(headers); i++ {
			results <- errors.New("invalid parents")
		}
		return abort, results
	}
	parents = append(parents, headers...)

	for i := 0; i < workers; i++ {
		//m.verifySnailHeader(chain, nil, nil, par, false, seals[i])
		go func() {
			for index := range inputs {
				errs[index] = m.verifySnailHeaderWorker(chain, headers, parents, seals, index)
				done <- index
			}
		}()
	}

	errorsOut := make(chan error, len(headers))
	go func() {
		defer close(inputs)
		var (
			in, out = 0, 0
			checked = make([]bool, len(headers))
			inputs  = inputs
		)
		for {
			select {
			case inputs <- in:
				if in++; in == len(headers) {
					// Reached end of headers. Stop sending to workers.
					inputs = nil
				}
			case index := <-done:
				for checked[index] = true; checked[out]; out++ {
					errorsOut <- errs[out]
					if out == len(headers)-1 {
						return
					}
				}
			case <-abort:
				return
			}
		}
	}()
	return abort, errorsOut
}

//ValidateRewarded verify whether the block has been rewarded.
func (m *Minerva) ValidateRewarded(number uint64, hash common.Hash, fastchain consensus.ChainReader) error {
	if br := fastchain.GetBlockReward(number); br != nil && br.SnailHash != hash {
		log.Info("err reward snail block", "number", number, "reward hash", br.SnailHash, "this snail hash", hash, "fast number", br.FastNumber, "fast hash", br.FastHash)
		return ErrRewardedBlock
	}
	return nil
}

//ValidateFruitHeader is to verify if the fruit is legal
func (m *Minerva) ValidateFruitHeader(block *types.SnailHeader, fruit *types.SnailHeader, chain consensus.SnailChainReader, fastchain consensus.ChainReader, checkpoint uint64) error {
	//check number(fb)
	//
	currentNumber := fastchain.CurrentHeader().Number
	if fruit.FastNumber.Cmp(currentNumber) > 0 {
		log.Warn("ValidateFruitHeader", "currentHeaderNumber", fastchain.CurrentHeader().Number, "currentBlockNumber", fastchain.CurrentHeader().Number)
		return consensus.ErrFutureBlock
	}

	fb := fastchain.GetHeader(fruit.FastHash, fruit.FastNumber.Uint64())
	if fb == nil {
		log.Warn("ValidateFruitHeader", "fasthash", fruit.FastHash, "number", fruit.FastNumber, "currentHeader", fastchain.CurrentHeader().Number)
		return consensus.ErrInvalidFast
	}

	//check fruit's time
	if fruit.Time == nil || fb.Time == nil || fruit.Time.Cmp(fb.Time) < 0 {
		return consensus.ErrFruitTime
	}
	if block.PointerNumber.Uint64() >= checkpoint {
		err := m.VerifyFreshness(chain, fruit, block.Number, false)
		if err != nil {
			log.Debug("ValidateFruitHeader verify freshness error.", "err", err, "fruit", fruit.FastNumber)
			return err
		}
	}

	if err := m.VerifySnailHeader(chain, fastchain, fruit, true, true); err != nil {
		log.Info("VerifySnailHeader verify failed.", "err", err)
		return err
	}

	return nil
}

func (m *Minerva) verifyHeaderWorker(chain consensus.ChainReader, headers []*types.Header,
	seals []bool, index int) error {
	var parent *types.Header

	if index == 0 {
		parent = chain.GetHeader(headers[0].ParentHash, headers[0].Number.Uint64()-1)
	} else if headers[index-1].Hash() == headers[index].ParentHash {
		parent = headers[index-1]
	}
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}
	if chain.GetHeader(headers[index].Hash(), headers[index].Number.Uint64()) != nil {
		return nil // known block
	}

	return m.verifyHeader(chain, headers[index], parent)
	//return nil
}

func (m *Minerva) verifySnailHeaderWorker(chain consensus.SnailChainReader, headers, parents []*types.SnailHeader,
	seals []bool, index int) error {
	//var parent *types.SnailHeader

	if chain.GetHeader(headers[index].Hash(), headers[index].Number.Uint64()) != nil {
		return nil // known block
	}
	count := len(parents) - len(headers) + index
	var parentHeaders []*types.SnailHeader
	if count < int(params.DifficultyPeriod.Int64()) {
		parentHeaders = parents[:count]
	} else {
		parentHeaders = parents[count-int(params.DifficultyPeriod.Int64()) : count]
	}
	return m.verifySnailHeader(chain, nil, headers[index], nil, parentHeaders, false, seals[index], false)
}

// verifyHeader checks whether a header conforms to the consensus rules of the
// stock Abeychain minerva engine.
func (m *Minerva) verifyHeader(chain consensus.ChainReader, header, parent *types.Header) error {
	// Ensure that the header's extra-data section is of a reasonable size
	if uint64(len(header.Extra)) > params.MaximumExtraDataSize {
		return fmt.Errorf("extra-data too long: %d > %d", len(header.Extra), params.MaximumExtraDataSize)
	}
	// Verify the header's timestamp
	if header.Time.Cmp(big.NewInt(time.Now().Add(allowedFutureBlockTime).Unix())) > 0 {
		fmt.Println(consensus.ErrFutureBlock.Error(), "header", header.Time, "now", time.Now().Unix(),
			"cmp:", big.NewInt(time.Now().Add(allowedFutureBlockTime).Unix()))
		return consensus.ErrFutureBlock
	}

	if header.Time.Cmp(parent.Time) < 0 {
		return errZeroBlockTime
	}

	// Verify that the gas limit is <= 2^63-1
	cap := uint64(0x7fffffffffffffff)
	if header.GasLimit > cap {
		return fmt.Errorf("invalid gasLimit: have %v, max %v", header.GasLimit, cap)
	}
	// Verify that the gasUsed is <= gasLimit
	if header.GasUsed > header.GasLimit {
		return fmt.Errorf("invalid gasUsed: have %d, gasLimit %d", header.GasUsed, header.GasLimit)
	}

	// Verify that the gas limit remains within allowed bounds
	diff := int64(parent.GasLimit) - int64(header.GasLimit)
	if diff < 0 {
		diff *= -1
	}
	limit := parent.GasLimit / params.GasLimitBoundDivisor

	if uint64(diff) >= limit || header.GasLimit < params.MinGasLimit {
		return fmt.Errorf("invalid gas limit: have %d, want %d += %d", header.GasLimit, parent.GasLimit, limit)
	}
	// Verify that the block number is parent's +1
	if diff := new(big.Int).Sub(header.Number, parent.Number); diff.Cmp(big.NewInt(1)) != 0 {
		return consensus.ErrInvalidNumber
	}

	return nil
}
func (m *Minerva) verifySnailHeader(chain consensus.SnailChainReader, fastchain consensus.ChainReader, header, pointer *types.SnailHeader,
	parents []*types.SnailHeader, uncle bool, seal bool, isFruit bool) error {
	// Ensure that the header's extra-data section is of a reasonable size
	if uint64(len(header.Extra)) > params.MaximumExtraDataSize {
		return fmt.Errorf("extra-data too long: %d > %d", len(header.Extra), params.MaximumExtraDataSize)
	}
	// Verify the header's timestamp
	if uncle {
		if header.Time.Cmp(math.MaxBig256) > 0 {
			return errLargeBlockTime
		}
	} else {
		if !isFruit {
			if header.Time.Cmp(big.NewInt(time.Now().Add(allowedFutureBlockTime).Unix())) > 0 {
				return consensus.ErrFutureBlock
			}
		}
	}
	if !isFruit {
		if header.Time.Cmp(parents[len(parents)-1].Time) <= 0 {
			return errZeroBlockTime
		}

		// Verify the block's difficulty based in it's timestamp and parent's difficulty
		expected := m.CalcSnailDifficulty(chain, header.Time.Uint64(), parents)

		if expected.Cmp(header.Difficulty) != 0 {
			return fmt.Errorf("invalid difficulty: have %v, want %v", header.Difficulty, expected)
		}
	} else {
		fastHeader := fastchain.GetHeader(header.FastHash, header.FastNumber.Uint64())
		if fastHeader == nil {
			log.Warn("verifySnailHeader get fast failed.", "fNumber", header.FastNumber, "fHash", header.FastHash)
			return errInvalidFast
		}
		// Verify the block's difficulty based in it's timestamp and parent's difficulty
		expected := m.CalcFruitDifficulty(chain, header.Time.Uint64(), fastHeader.Time.Uint64(), pointer)

		if expected.Cmp(header.FruitDifficulty) != 0 {
			return fmt.Errorf("invalid fruit difficulty: have %v, want %v", header.FruitDifficulty, expected)
		}
	}

	// Verify the engine specific seal securing the block
	if seal {
		if err := m.VerifySnailSeal(chain, header, isFruit); err != nil {
			return err
		}
	}

	return nil
}

// CalcSnailDifficulty is the difficulty adjustment algorithm. It returns
// the difficulty that a new block should have when created at time
// given the parent block's time and difficulty.
func (m *Minerva) CalcSnailDifficulty(chain consensus.SnailChainReader, time uint64, parents []*types.SnailHeader) *big.Int {
	return CalcDifficulty(chain.Config(), time, parents)
}

//CalcFruitDifficulty is Calc the Fruit difficulty again and compare the header diff
func (m *Minerva) CalcFruitDifficulty(chain consensus.SnailChainReader, time uint64, fastTime uint64, pointer *types.SnailHeader) *big.Int {
	return CalcFruitDifficulty(chain.Config(), time, fastTime, pointer)
}

// VerifySigns check the sings included in fast block or fruit
func (m *Minerva) VerifySigns(fastnumber *big.Int, fastHash common.Hash, signs []*types.PbftSign) error {
	// validate the signatures of this fruit
	ms := make(map[common.Address]uint)
	members := m.election.GetCommittee(fastnumber)
	if members == nil {
		log.Warn("VerifySigns get committee failed.", "number", fastnumber)
		return consensus.ErrInvalidSign
	}
	for _, member := range members {
		addr := member.CommitteeBase
		ms[addr] = 0
	}

	count := 0
	for _, sign := range signs {
		if sign.FastHash != fastHash || sign.FastHeight.Cmp(fastnumber) != 0 {
			log.Warn("VerifySigns signs hash error", "number", fastnumber, "hash", fastHash, "signHash", sign.FastHash, "signNumber", sign.FastHeight)
			return consensus.ErrInvalidSign
		}
		if sign.Result == types.VoteAgree {
			count++
		}
	}
	if count <= len(members)*2/3 {
		log.Warn("VerifySigns number error", "signs", len(signs), "agree", count, "members", len(members))
		return consensus.ErrInvalidSign
	}

	signMembers, errs := m.election.VerifySigns(signs)
	for i, err := range errs {
		if err != nil {
			log.Warn("VerifySigns error", "err", err)
			return err
		}
		addr := signMembers[i].CommitteeBase
		if _, ok := ms[addr]; !ok {
			// is not a committee member
			log.Warn("VerifySigns member error", "signs", len(signs), "member", hex.EncodeToString(members[i].Publickey))
			return consensus.ErrInvalidSign
		}
		if ms[addr] == 1 {
			// the committee member's sign is already exist
			log.Warn("VerifySigns member already exist", "signs", len(signs), "member", hex.EncodeToString(members[i].Publickey))
			return consensus.ErrInvalidSign
		}
		ms[addr] = 1
	}

	return nil
}

//VerifySwitchInfo verify the switch info of Committee
func (m *Minerva) VerifySwitchInfo(fastnumber *big.Int, info []*types.CommitteeMember) error {

	return m.election.VerifySwitchInfo(fastnumber, info)

}

//VerifyFreshness the fruit have fresh is 17 blocks
func (m *Minerva) VerifyFreshness(chain consensus.SnailChainReader, fruit *types.SnailHeader, headerNumber *big.Int, canonical bool) error {
	// check freshness
	pointer := chain.GetHeaderByNumber(fruit.PointerNumber.Uint64())
	if pointer == nil {
		return types.ErrSnailHeightNotYet
	}
	if canonical {
		if pointer.Hash() != fruit.PointerHash {
			log.Debug("VerifyFreshness get pointer failed.", "fruit", fruit.FastNumber, "pointerNumber", fruit.PointerNumber, "pointerHash", fruit.PointerHash,
				"fruitNumber", fruit.Number, "pointer", pointer.Hash())
			return consensus.ErrUnknownPointer
		}
	} else {
		pointer = chain.GetHeader(fruit.PointerHash, fruit.PointerNumber.Uint64())
		if pointer == nil {
			return consensus.ErrUnknownPointer
		}
	}
	freshNumber := new(big.Int).Sub(headerNumber, pointer.Number)
	if freshNumber.Cmp(params.FruitFreshness) > 0 {
		log.Debug("VerifyFreshness failed.", "fruit sb", fruit.Number, "fruit fb", fruit.FastNumber, "poiner", pointer.Number, "current", headerNumber)
		return consensus.ErrFreshness
	}

	return nil
}

// GetDifficulty get difficulty by header
func (m *Minerva) GetDifficulty(header *types.SnailHeader, isFruit bool) (*big.Int, *big.Int) {
	result := header.MixDigest

	if isFruit {
		last := result[16:]
		actDiff := new(big.Int).Div(maxUint128, new(big.Int).SetBytes(last))

		return actDiff, header.FruitDifficulty
	}
	actDiff := new(big.Int).Div(maxUint128, new(big.Int).SetBytes(result[:16]))
	return actDiff, header.Difficulty
}

// Some weird constants to avoid constant memory allocs for them.
var (
	expDiffPeriod = big.NewInt(100000)
	big1          = big.NewInt(1)
	big2          = big.NewInt(2)
	big8          = big.NewInt(8)
	big9          = big.NewInt(9)
	big10         = big.NewInt(10)
	big32         = big.NewInt(32)

	big90 = big.NewInt(90)

	bigMinus1  = big.NewInt(-1)
	bigMinus99 = big.NewInt(-99)
	big2999999 = big.NewInt(2999999)
)

// CalcDifficulty is the difficulty adjustment algorithm. It returns
// the difficulty that a new block should have when created at time
// given the parent block's time and difficulty.
func CalcDifficulty(config *params.ChainConfig, time uint64, parents []*types.SnailHeader) *big.Int {

	return calcDifficulty(config, time, parents)

}

//CalcFruitDifficulty is the Fruit difficulty adjustment algorithm
// need calc fruit difficulty each new fruit
func CalcFruitDifficulty(config *params.ChainConfig, time uint64, fastTime uint64, pointer *types.SnailHeader) *big.Int {
	diff := new(big.Int).Div(pointer.Difficulty, params.FruitBlockRatio)

	delta := time - fastTime

	if delta > 20 {
		diff = new(big.Int).Div(diff, big.NewInt(2))
		diff.Add(diff, common.Big1)
	} else if delta > 10 && delta <= 20 {
		diff = new(big.Int).Mul(diff, big.NewInt(2))
		diff = new(big.Int).Div(diff, big.NewInt(3))
	}

	minimum := config.Minerva.MinimumFruitDifficulty
	if diff.Cmp(minimum) < 0 {
		diff.Set(minimum)
	}

	return diff
}

func calcDifficulty(config *params.ChainConfig, time uint64, parents []*types.SnailHeader) *big.Int {
	// algorithm:
	// diff = (averageDiff +
	//         (averageDiff / 2) * (max(86400 - (block_timestamp - parent_timestamp), -86400) // 86400)
	//        )

	period := big.NewInt(int64(len(parents)))
	parentHeaders := parents

	/* get average diff */
	diff := big.NewInt(0)
	if parents[0].Number.Cmp(common.Big0) == 0 {
		period.Sub(period, common.Big1)
		parentHeaders = parents[1:]
	}
	if period.Cmp(common.Big0) == 0 {
		// only have genesis block
		return parents[0].Difficulty
	}

	for _, parent := range parentHeaders {
		diff.Add(diff, parent.Difficulty)
	}
	averageDiff := new(big.Int).Div(diff, period)

	durationDivisor := new(big.Int).Mul(config.Minerva.DurationLimit, period)

	bigTime := new(big.Int).SetUint64(time)
	bigParentTime := new(big.Int).Set(parentHeaders[0].Time)

	// holds intermediate values to make the algo easier to read & audit
	x := new(big.Int)
	y := new(big.Int)

	// 86400 - (block_timestamp - parent_timestamp)
	x.Add(durationDivisor, bigParentTime)
	x.Sub(x, bigTime)

	// (max(86400 - (block_timestamp - parent_timestamp), -86400)
	y.Mul(durationDivisor, bigMinus1)
	if x.Cmp(y) < 0 {
		x.Set(y)
	}

	// (averageDiff / 2) * (max(86400 - (block_timestamp - parent_timestamp), -86400) // 86400)
	y.Div(averageDiff, params.DifficultyBoundDivisor)
	x.Mul(y, x)

	x.Div(x, durationDivisor)

	x.Add(averageDiff, x)

	// minimum difficulty can ever be (before exponential factor)
	if x.Cmp(config.Minerva.MinimumDifficulty) < 0 {
		x.Set(config.Minerva.MinimumDifficulty)
	}

	log.Debug("Calc diff", "parent", parentHeaders[0].Difficulty, "avg", averageDiff, "diff", x,
		"time", new(big.Int).Sub(bigTime, bigParentTime), "period", period)

	return x
}

// VerifySnailSeal implements consensus.Engine, checking whether the given block satisfies
// the PoW difficulty requirements.
func (m *Minerva) VerifySnailSeal(chain consensus.SnailChainReader, header *types.SnailHeader, isFruit bool) error {
	// If we're running a fake PoW, accept any seal as valid
	if m.config.PowMode == ModeFake || m.config.PowMode == ModeFullFake {
		time.Sleep(m.fakeDelay)
		if m.fakeFail == header.Number.Uint64() {
			return errInvalidPoW
		}
		return nil
	}
	// If we're running a shared PoW, delegate verification to it
	if m.shared != nil {
		return m.shared.VerifySnailSeal(chain, header, isFruit)
	}
	// Ensure that we have a valid difficulty for the block
	if header.Difficulty.Sign() <= 0 {
		return errInvalidDifficulty
	}
	if header.FruitDifficulty.Sign() <= 0 {
		return errInvalidDifficulty
	}
	// Recompute the digest and PoW value and verify against the header
	dataset := m.getDataset(header.Number.Uint64())
	if dataset == nil {
		return errors.New("get dataset is nil")
	}
	//m.CheckDataSetState(header.Number.Uint64())
	digest, result := truehashLight(dataset.dataset, header.HashNoNonce().Bytes(), header.Nonce.Uint64())

	if !bytes.Equal(header.MixDigest[:], digest) {
		log.Error("VerifySnailSeal error  ", "block is", header.Number, "epoch is:", dataset.epoch, "consistent is:", dataset.consistent, "datasethash", dataset.datasetHash, "---header.MixDigest is:", header.MixDigest, "---digest is:", common.BytesToHash(digest))
		return errInvalidMixDigest
	}

	if isFruit {
		fruitTarget := new(big.Int).Div(maxUint128, header.FruitDifficulty)

		last := result[16:]
		if new(big.Int).SetBytes(last).Cmp(fruitTarget) > 0 {
			return errInvalidPoW
		}
	} else {
		target := new(big.Int).Div(maxUint128, header.Difficulty)
		last := result[:16]
		if new(big.Int).SetBytes(last).Cmp(target) > 0 {
			return errInvalidPoW
		}
	}

	return nil
}

// VerifySnailSeal2 implements consensus.Engine, checking whether the given block satisfies
// the PoW difficulty requirements.
func (m *Minerva) VerifySnailSeal2(hight *big.Int, nonce string, headNoNoncehash string, ftarg *big.Int, btarg *big.Int, haveFruits bool) (bool, bool, []byte) {
	// If we're running a fake PoW, accept any seal as valid

	nonceHash, _ := hex.DecodeString(nonce)
	headHash := common.HexToHash(headNoNoncehash)

	dataset := m.getDataset(hight.Uint64())
	if dataset == nil {
		log.Error(" get dataset is nil")
		return false, false, []byte{}

	}
	//m.CheckDataSetState(header.Number.Uint64())
	digest, result := truehashLight(dataset.dataset, headHash.Bytes(), binary.BigEndian.Uint64(nonceHash[:]))

	headResult := result[:16]
	if new(big.Int).SetBytes(headResult).Cmp(btarg) <= 0 {
		// Correct nonce found, create a new header with it
		if haveFruits {
			return true, false, digest

		}

	} else {
		lastResult := result[16:]

		if new(big.Int).SetBytes(lastResult).Cmp(ftarg) <= 0 {
			return true, true, digest
		}
		return false, false, []byte{}
	}

	return false, false, []byte{}
}

// Prepare implements consensus.Engine, initializing the difficulty field of a
// header to conform to the minerva protocol. The changes are done inline.
func (m *Minerva) Prepare(chain consensus.ChainReader, header *types.Header) error {
	if parent := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1); parent == nil {
		return consensus.ErrUnknownAncestor
	}
	return nil
}

// PrepareSnail implements consensus.Engine, initializing the difficulty field of a
//// header to conform to the minerva protocol. The changes are done inline.
func (m *Minerva) PrepareSnail(fastchain consensus.ChainReader, chain consensus.SnailChainReader, header *types.SnailHeader) error {
	parents := m.getParents(chain, header)
	//parent := m.sbc.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	if parents == nil {
		return consensus.ErrUnknownAncestor
	}
	header.Difficulty = m.CalcSnailDifficulty(chain, header.Time.Uint64(), parents)

	if header.FastNumber == nil {
		header.FruitDifficulty = new(big.Int).Set(chain.Config().Minerva.MinimumFruitDifficulty)
	} else {
		pointer := chain.GetHeader(header.PointerHash, header.PointerNumber.Uint64())
		if pointer == nil {
			return consensus.ErrUnknownPointer
		}
		fast := fastchain.GetHeader(header.FastHash, header.FastNumber.Uint64())
		if fast == nil {
			return consensus.ErrUnknownFast
		}

		header.FruitDifficulty = m.CalcFruitDifficulty(chain, header.Time.Uint64(), fast.Time.Uint64(), pointer)
	}

	return nil
}

// PrepareSnailWithParent implements consensus.Engine, initializing the difficulty field of a
//// header to conform to the minerva protocol. The changes are done inline.
func (m *Minerva) PrepareSnailWithParent(fastchain consensus.ChainReader, chain consensus.SnailChainReader, header *types.SnailHeader, parents []*types.SnailHeader) error {
	//parents := m.getParents(chain, header)
	//parent := m.sbc.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	if parents == nil {
		return consensus.ErrUnknownAncestor
	}
	header.Difficulty = m.CalcSnailDifficulty(chain, header.Time.Uint64(), parents)

	if header.FastNumber == nil {
		header.FruitDifficulty = new(big.Int).Set(chain.Config().Minerva.MinimumFruitDifficulty)
	} else {
		pointer := chain.GetHeader(header.PointerHash, header.PointerNumber.Uint64())
		if pointer == nil {
			return consensus.ErrUnknownPointer
		}
		fast := fastchain.GetHeader(header.FastHash, header.FastNumber.Uint64())
		if fast == nil {
			return consensus.ErrUnknownFast
		}

		header.FruitDifficulty = m.CalcFruitDifficulty(chain, header.Time.Uint64(), fast.Time.Uint64(), pointer)
	}

	return nil
}

// Finalize implements consensus.Engine, accumulating the block fruit and uncle rewards,
// setting the final state and assembling the block.
func (m *Minerva) Finalize(chain consensus.ChainReader, header *types.Header, state *state.StateDB,
	txs []*types.Transaction, receipts []*types.Receipt, feeAmount *big.Int) (*types.Block, *types.ChainReward,error) {
		
	consensus.OnceInitImpawnState(chain.Config(),state,new(big.Int).Set(header.Number))
	if chain.Config().TIP10.FastNumber.Uint64() == header.Number.Uint64() {
		i := vm.NewImpawnImpl()
		if err := i.Load(state, types.StakingAddress); err != nil {
			log.Error("Load impawn:make modify state", "height", header.Number, "err", err)
			return nil,nil, err
		}
		i.MakeModifyStateByTip10()	
		i.Save(state, types.StakingAddress)
		log.Info("MakeModifyStateByTip10")		
	}
	var infos *types.ChainReward
	if header != nil && header.SnailHash != (common.Hash{}) && header.SnailNumber != nil {
		sBlockHeader := m.sbc.GetHeaderByNumber(header.SnailNumber.Uint64())
		if sBlockHeader == nil {
			return nil, nil,types.ErrSnailHeightNotYet
		}
		if sBlockHeader.Hash() != header.SnailHash {
			return nil,nil, types.ErrSnailBlockNotOnTheCain
		}
		sBlock := m.sbc.GetBlock(header.SnailHash, header.SnailNumber.Uint64())
		if sBlock == nil {
			return nil, nil,types.ErrSnailHeightNotYet
		}
		endfast := new(big.Int).Set(header.Number)
		if len(sBlock.Fruits()) > 0 {
			endfast = new(big.Int).Set(sBlock.MinFruitNumber())
		}
		var err error
		if consensus.IsTIP8(endfast, chain.Config(), m.sbc) {
			infos,err = accumulateRewardsFast2(state, sBlock, header.Number.Uint64(),chain.Config().TIP10.CID.Uint64())
			if err != nil {
				log.Error("Finalize Error", "accumulateRewardsFast2", err.Error())
				return nil,nil, err
			}
		} else {
			infos,err = accumulateRewardsFast(m.election, state, sBlock)
			if err != nil {
				log.Error("Finalize Error", "accumulateRewardsFast", err.Error())
				return nil,nil, err
			}
		}
	}
	if err := m.finalizeFastGas(state, header.Number, header.Hash(), feeAmount); err != nil {
		return nil,nil, err
	}

	if err := m.finalizeValidators(chain, state, header.Number); err != nil {
		return nil,nil, err
	}
	header.Root = state.IntermediateRoot(true)
	return types.NewBlock(header, txs, receipts, nil, nil),infos, nil
}

// FinalizeSnail implements consensus.Engine, accumulating the block fruit and uncle rewards,
// setting the final state and assembling the block.
func (m *Minerva) FinalizeSnail(chain consensus.SnailChainReader, header *types.SnailHeader,
	uncles []*types.SnailHeader, fruits []*types.SnailBlock, signs []*types.PbftSign) (*types.SnailBlock, error) {

	//header.Root = state.IntermediateRoot(chain.Config().IsEIP158(header.Number))
	// Header seems complete, assemble into a block and return
	return types.NewSnailBlock(header, fruits, signs, uncles, chain.Config()), nil
}

// FinalizeCommittee upddate current committee state
func (m *Minerva) FinalizeCommittee(block *types.Block) error {
	return m.election.FinalizeCommittee(block)
}

// gas allocation
func (m *Minerva) finalizeFastGas(state *state.StateDB, fastNumber *big.Int, fastHash common.Hash, feeAmount *big.Int) error {
	if feeAmount == nil || feeAmount.Uint64() == 0 {
		return nil
	}
	committee := m.election.GetCommittee(fastNumber)
	committeeGas := big.NewInt(0)
	if len(committee) == 0 {
		return errors.New("not have committee")
	}
	committeeGas = new(big.Int).Div(feeAmount, big.NewInt(int64(len(committee))))
	for _, v := range committee {
		state.AddBalance(v.Coinbase, committeeGas)
		LogPrint("committee's gas award", v.Coinbase, committeeGas)
	}
	return nil
}

// gas allocation
func (m *Minerva) finalizeValidators(chain consensus.ChainReader, state *state.StateDB, fastNumber *big.Int) error {

	next := new(big.Int).Add(fastNumber, big1)
	if consensus.IsTIP8(next, chain.Config(), m.sbc) {
		// init the first epoch in the fork
		first := types.GetFirstEpoch()
		// fmt.Println("first.BeginHeight", first.BeginHeight, "next", next)
		if first.BeginHeight == next.Uint64() {
			i := vm.NewImpawnImpl()
			error := i.Load(state, types.StakingAddress)
			if es, err := i.DoElections(first.EpochID, next.Uint64()); err != nil {
				return err
			} else {
				log.Info("init in first forked, Do pre election", "height", next, "epoch:", first.EpochID, "len:", len(es), "err", error)
			}
			if err := i.Shift(first.EpochID,chain.Config().TIP10.FastNumber.Uint64()); err != nil {
				return err
			}
			i.Save(state, types.StakingAddress)
			log.Info("init in first forked,", "height", next, "epoch:", first.EpochID)
		}
	}
	if consensus.IsTIP8(fastNumber, chain.Config(), m.sbc) {
		epoch := types.GetEpochFromHeight(fastNumber.Uint64())

		if fastNumber.Uint64() == epoch.EndHeight-params.ElectionPoint {
			i := vm.NewImpawnImpl()
			error := i.Load(state, types.StakingAddress)
			if es, err := i.DoElections(epoch.EpochID+1, fastNumber.Uint64()); err != nil {
				return err
			} else {
				log.Info("Do validators election", "height", fastNumber, "epoch:", epoch.EpochID+1, "len:", len(es), "err", error)
			}
			i.Save(state, types.StakingAddress)
		}

		if fastNumber.Uint64() == epoch.EndHeight {
			i := vm.NewImpawnImpl()
			err := i.Load(state, types.StakingAddress)
			log.Info("Force new epoch", "height", fastNumber, "err", err)
			if err := i.Shift(epoch.EpochID + 1,chain.Config().TIP10.FastNumber.Uint64()); err != nil {
				return err
			}
			i.Save(state, types.StakingAddress)
		}
	}
	return nil
}

//LogPrint log debug
func LogPrint(info string, addr common.Address, amount *big.Int) {
	log.Debug("[Consensus AddBalance]", "info", info, "CoinBase:", addr.String(), "amount", amount)
}

// AccumulateRewardsFast credits the coinbase of the given block with the mining
// reward. The total reward consists of the static block reward and rewards for
// included uncles. The coinbase of each uncle block is also rewarded.
func accumulateRewardsFast(election consensus.CommitteeElection, stateDB *state.StateDB, sBlock *types.SnailBlock) (*types.ChainReward,error) {
	committeeCoin, minerCoin, minerFruitCoin,developerCoin, e := GetBlockReward3(sBlock.Header().Number)
	if e == ErrRewardEnd {
		return nil,nil
	}
	if e != nil {
		return nil,e
	}
	var (
		blockFruits    = sBlock.Body().Fruits
		blockFruitsLen = big.NewInt(int64(len(blockFruits)))
	)
	if blockFruitsLen.Uint64() == 0 {
		return nil,consensus.ErrInvalidBlock
	}
	var (
		//fruit award amount
		minerFruitCoinOne = new(big.Int).Div(minerFruitCoin, blockFruitsLen)
		//committee's award amount
		committeeCoinFruit = new(big.Int).Div(committeeCoin, blockFruitsLen)
		//all fail committee coinBase
		failAddr = make(map[common.Address]bool)
	)
	//miner's award
	stateDB.AddBalance(sBlock.Coinbase(), minerCoin)
	LogPrint("miner's award", sBlock.Coinbase(), minerCoin)
	if developerCoin != nil {
		stateDB.AddBalance(types.FoundationAddress, developerCoin)
		LogPrint("developer's award", types.FoundationAddress, developerCoin)
	} else {
		developerCoin = common.Big0
	}
	developer := &types.RewardInfo{
		Address:	types.FoundationAddress,
		Amount:		developerCoin,
	}
	coinbase := &types.RewardInfo{
		Address:	sBlock.Coinbase(),
		Amount:		new(big.Int).Set(minerCoin),
	}
	fruitMap := make(map[common.Address]*big.Int)
	committeeMap := make(map[common.Address]*big.Int)

	for _, fruit := range blockFruits {
		stateDB.AddBalance(fruit.Coinbase(), minerFruitCoinOne)
		LogPrint("minerFruit", fruit.Coinbase(), minerFruitCoinOne)
		if v,ok := fruitMap[fruit.Coinbase()]; ok {
			fruitMap[fruit.Coinbase()] = new(big.Int).Add(v,minerFruitCoinOne)
		} else {
			fruitMap[fruit.Coinbase()] = new(big.Int).Set(minerFruitCoinOne)
		}
		//committee reward
		err,tmp := rewardFruitCommitteeMember(stateDB, election, fruit, committeeCoinFruit, failAddr)
		if err != nil {
			return nil,err
		}
		committeeMap = types.MergeReward(committeeMap,tmp)
	}
	infos := types.NewChainReward(sBlock.NumberU64(),sBlock.Time().Uint64(),developer,coinbase,types.ToRewardInfos1(fruitMap),types.ToRewardInfos2(committeeMap))
	return infos,nil
}
func accumulateRewardsFast2(stateDB *state.StateDB, sBlock *types.SnailBlock, fast,effectid uint64) (*types.ChainReward,error) {
	sHeight := sBlock.Header().Number
	committeeCoin, minerCoin, minerFruitCoin,developerCoin, e := GetBlockReward3(sHeight)
	if e == ErrRewardEnd {
		return nil,nil
	}
	if e != nil {
		return nil,e
	}
	impawn := vm.NewImpawnImpl()
	impawn.Load(stateDB, types.StakingAddress)
	defer impawn.Save(stateDB, types.StakingAddress)

	var (
		blockFruits    = sBlock.Body().Fruits
		blockFruitsLen = big.NewInt(int64(len(blockFruits)))
	)
	if blockFruitsLen.Uint64() == 0 {
		return nil,consensus.ErrInvalidBlock
	}
	var (
		//fruit award amount
		minerFruitCoinOne = new(big.Int).Div(minerFruitCoin, blockFruitsLen)
	)
	//miner's award
	stateDB.AddBalance(sBlock.Coinbase(), minerCoin)
	// LogPrint("miner's award", sBlock.Coinbase(), minerCoin)
	if developerCoin != nil {
		stateDB.AddBalance(types.FoundationAddress, developerCoin)
		// LogPrint("developer's award", types.FoundationAddress, developerCoin)
	} else {
		developerCoin = common.Big0
	}
	developer := &types.RewardInfo{
		Address:	types.FoundationAddress,
		Amount:		developerCoin,
	}
	coinbase := &types.RewardInfo{
		Address:	sBlock.Coinbase(),
		Amount:		new(big.Int).Set(minerCoin),
	}
	fruitMap := make(map[common.Address]*big.Int)

	for _, fruit := range blockFruits {
		stateDB.AddBalance(fruit.Coinbase(), minerFruitCoinOne)
		// LogPrint("minerFruit", fruit.Coinbase(), minerFruitCoinOne)
		if v,ok := fruitMap[fruit.Coinbase()]; ok {
			fruitMap[fruit.Coinbase()] = new(big.Int).Add(v,minerFruitCoinOne)
		} else {
			fruitMap[fruit.Coinbase()] = new(big.Int).Set(minerFruitCoinOne)
		}
	}
	//committee reward
	infos, err := impawn.Reward(sBlock, committeeCoin,effectid)
	if err != nil {
		return nil,err
	}
	for _, v := range infos {
		for _, vv := range v.Items {
			stateDB.AddBalance(vv.Address, vv.Amount)
			LogPrint("committee:", vv.Address, vv.Amount)
		}
	}
	rewardsInfos := types.NewChainReward(sBlock.NumberU64(),sBlock.Time().Uint64(),developer,coinbase,types.ToRewardInfos1(fruitMap),infos)
	// log.Debug("[****accumulateRewardsFast2]", "Height", rewardsInfos.Height, 
	// "committeeCoin",committeeCoin.String(),"minerCoin",minerCoin.String(),
	// "minerFruitCoin",minerFruitCoin.String(),"developerCoin",developerCoin.String(),
	// "Foundation:", rewardsInfos.Foundation.String(), "CoinBase", rewardsInfos.CoinBase.String(),
	// "FruitBase",rewardsInfos.FruitBase,"CommitteeBase",rewardsInfos.CommitteeBase)
	return rewardsInfos,nil
}

func posOfFruitsInFirstEpoch(fruits []*types.SnailBlock, min, max uint64) int {
	first := types.GetFirstEpoch()

	if min <= first.BeginHeight && first.BeginHeight <= max {
		for i, v := range fruits {
			if v.FastNumber().Uint64() == first.BeginHeight {
				return i
			}
		}
	}
	return -1
}

// GetRewardContentBySnailNumber retrieves SnailRewardContenet by snail block.
func (m *Minerva) GetRewardContentBySnailNumber(sBlock *types.SnailBlock) *types.SnailRewardContenet {
	committeeCoin, minerCoin, minerFruitCoin,developerCoin, e := GetBlockReward3(sBlock.Header().Number)
	if e != nil {
		return nil
	}
	var (
		blockFruits    = sBlock.Body().Fruits
		blockFruitsLen = big.NewInt(int64(len(blockFruits)))

		blockMinerReward = make(map[common.Address]*big.Int)
		fruitMinerReward = make([]map[common.Address]*big.Int, len(blockFruits))
		committeeReward  = make(map[common.Address]*big.Int)
	)
	if blockFruitsLen.Uint64() == 0 {
		return nil
	}
	var (
		//fruit award amount
		minerFruitCoinOne = new(big.Int).Div(minerFruitCoin, blockFruitsLen)
		//committee's award amount
		committeeCoinFruit = new(big.Int).Div(committeeCoin, blockFruitsLen)
		//all fail committee coinBase
		failAddr = make(map[common.Address]bool)
	)
	//miner's award
	blockMinerReward[sBlock.Coinbase()] = minerCoin
	for i, fruit := range blockFruits {
		fruitMap := make(map[common.Address]*big.Int)
		fruitMap[fruit.Coinbase()] = minerFruitCoinOne
		fruitMinerReward[i] = fruitMap
		//committee reward
		getCommitteeVoted(committeeReward, m.election, fruit, failAddr, committeeCoinFruit)
	}
	developer := make(map[common.Address]*big.Int)
	if developerCoin == nil {
		developer[types.FoundationAddress] = new(big.Int).Set(common.Big0)
	} else {
		developer[types.FoundationAddress] = developerCoin
	}
	return &types.SnailRewardContenet{
		BlockMinerReward: blockMinerReward,
		FruitMinerReward: fruitMinerReward,
		CommitteeReward:  committeeReward,
		FoundationReward: developer,
	}
}

func getCommitteeVoted(committeeReward map[common.Address]*big.Int, election consensus.CommitteeElection,
	fruit *types.SnailBlock, failAddr map[common.Address]bool, committeeCoinFruit *big.Int) {
	signs := fruit.Body().Signs
	committeeMembers, errs := election.VerifySigns(signs)
	if len(committeeMembers) != len(errs) {
		return
	}
	//Effective and not evil
	var fruitOkAddr []common.Address
	for i, cm := range committeeMembers {
		if errs[i] != nil {
			continue
		}
		cmPubAddr := cm.CommitteeBase
		if signs[i].Result == types.VoteAgree {
			if _, ok := failAddr[cmPubAddr]; !ok {
				fruitOkAddr = append(fruitOkAddr, cm.Coinbase)
			}
		} else {
			failAddr[cmPubAddr] = false
		}
	}
	// Equal by fruit
	if len(fruitOkAddr) > 0 {
		committeeCoinFruitMember := new(big.Int).Div(committeeCoinFruit, big.NewInt(int64(len(fruitOkAddr))))
		for _, v := range fruitOkAddr {
			if committeeReward[v] != nil {
				committeeReward[v] = new(big.Int).Add(committeeReward[v], committeeCoinFruitMember)
			} else {
				committeeReward[v] = committeeCoinFruitMember
			}
		}
	}	
}

func rewardFruitCommitteeMember(state *state.StateDB, election consensus.CommitteeElection,
	fruit *types.SnailBlock, committeeCoinFruit *big.Int, failAddr map[common.Address]bool) (error,map[common.Address]*big.Int) {
	signs := fruit.Body().Signs
	committeeMembers, errs := election.VerifySigns(signs)
	if len(committeeMembers) != len(errs) {
		return consensus.ErrInvalidSignsLength,nil
	}
	//Effective and not evil
	var fruitOkAddr []common.Address
	for i, cm := range committeeMembers {
		if errs[i] != nil {
			continue
		}
		cmPubAddr := cm.CommitteeBase
		if signs[i].Result == types.VoteAgree {
			if _, ok := failAddr[cmPubAddr]; !ok {
				fruitOkAddr = append(fruitOkAddr, cm.Coinbase)
			}
		} else {
			failAddr[cmPubAddr] = false
		}
	}
	if len(fruitOkAddr) == 0 {
		log.Error("fruitOkAddr", "Error", consensus.ErrValidSignsZero.Error())
		return consensus.ErrValidSignsZero,nil
	}
	// Equal by fruit
	tmp := make(map[common.Address]*big.Int)
	committeeCoinFruitMember := new(big.Int).Div(committeeCoinFruit, big.NewInt(int64(len(fruitOkAddr))))
	for _, v := range fruitOkAddr {
		state.AddBalance(v, committeeCoinFruitMember)
		LogPrint("committee", v, committeeCoinFruitMember)
		if vv,ok := tmp[v]; ok {
			tmp[v] = new(big.Int).Add(vv,committeeCoinFruitMember)
		} else {
			tmp[v] = new(big.Int).Set(committeeCoinFruitMember)
		}
	}
	return nil,tmp
}

//GetBlockReward Reward for block allocation
func GetBlockReward(num *big.Int) (committee, minerBlock, minerFruit *big.Int, e error) {
	base := new(big.Int).Div(getCurrentCoin(num), Big1e6).Int64()
	m, c, e := getDistributionRatio(NetworkFragmentsNuber)
	if e != nil {
		return
	}

	committee = new(big.Int).Mul(big.NewInt(int64(c*float64(base))), Big1e6)
	minerBlock = new(big.Int).Mul(big.NewInt(int64(m*float64(base)/3*2)), Big1e6)
	minerFruit = new(big.Int).Mul(big.NewInt(int64(m*float64(base)/3)), Big1e6)
	return
}

func GetBlockReward3(num *big.Int) (committee, minerBlock, minerFruit,developercoin *big.Int, e error) {
	if num.Cmp(big.NewInt(int64(NewRewardBegin+RewardEndSnailHeight))) >= 0{
		return nil,nil,nil,nil,ErrRewardEnd
	}
	if num.Cmp(big.NewInt(int64(NewRewardBegin))) >= 0 {
		return getBlockReward2(num)
	} else {
		committee,minerBlock,minerFruit,e = GetBlockReward(num)
		return committee,minerBlock,minerFruit,nil,e
	}
}

// get Distribution ratio for miner and committee
func getDistributionRatio(fragmentation int) (miner, committee float64, e error) {
	if fragmentation <= SqrtMin {
		return 0.8, 0.2, nil
	}
	if fragmentation >= SqrtMax {
		return 0.2, 0.8, nil
	}
	committee = SqrtArray[fragmentation]
	return 1 - committee, committee, nil
}

func powerf(x float64, n int64) float64 {
	if n == 0 {
		return 1
	}
	return x * powerf(x, n-1)
}

//Get the total reward for the current block
func getCurrentCoin(h *big.Int) *big.Int {
	d := h.Int64() / int64(SnailBlockRewardsChangeInterval)
	ratio := big.NewInt(int64(powerf(0.98, d) * float64(SnailBlockRewardsBase)))
	return new(big.Int).Mul(ratio, Big1e6)
}
func getRewardCoin(height *big.Int) *big.Int {
	if height.Cmp(big.NewInt(int64(NewRewardBegin))) >= 0 {
		last := new(big.Int).Sub(height,big.NewInt(int64(NewRewardBegin-1)))
		loops := new(big.Int).Div(last,big.NewInt(int64(RewardMinerDecayEpoch))).Int64()
		base := new(big.Int).Set(NewRewardCoin)
		for i:=0;i<int(loops);i++ {
			// decay 20% per epoch
			tmp := new(big.Int).Div(new(big.Int).Mul(base,big.NewInt(20)),big.NewInt(100))
			base = new(big.Int).Sub(base,tmp)
		}
		return base
	}
	return nil
}
func getBlockReward2(num *big.Int) (committee, minerBlock, minerFruit,developercoin *big.Int, e error) {
	base := getRewardCoin(num)
	if base == nil {
		return nil,nil,nil,nil,errors.New("wrong height in reward")
	}
	// committee = base * 75%
	committee = new(big.Int).Div(new(big.Int).Mul(base,big.NewInt(75)),big.NewInt(100))
	// developercoin = base * 19%
	developercoin = new(big.Int).Div(new(big.Int).Mul(base,big.NewInt(19)),big.NewInt(100))
	//  miner = base * 6%
	miner := new(big.Int).Sub(base,new(big.Int).Add(committee,developercoin))
	minerBlock = new(big.Int).Div(new(big.Int).Mul(miner,big.NewInt(2)),big.NewInt(3))
	minerFruit = new(big.Int).Sub(miner,minerBlock)
	return
}
