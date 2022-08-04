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

package core

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/abeychain/go-abey/common"
	"math/big"

	"github.com/abeychain/go-abey/consensus"
	"github.com/abeychain/go-abey/core/state"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/log"
	"github.com/abeychain/go-abey/params"
)

// BlockValidator is responsible for validating block headers, uncles and
// processed state.
//
// BlockValidator implements Validator.
type BlockValidator struct {
	config *params.ChainConfig // Chain configuration options
	bc     *BlockChain         // Canonical block chain
	engine consensus.Engine    // Consensus engine used for validating
}

// NewBlockValidator returns a new block validator which is safe for re-use
func NewBlockValidator(config *params.ChainConfig, blockchain *BlockChain, engine consensus.Engine) *BlockValidator {
	validator := &BlockValidator{
		config: config,
		engine: engine,
		bc:     blockchain,
	}
	return validator
}

// ValidateBody validates the given block's uncles and verifies the the block
// header's transaction and uncle roots. The headers are assumed to be already
// validated at this point.
func (fv *BlockValidator) ValidateBody(block *types.Block, validateSign bool) error {
	// Check whether the block's known, and if not, that it's linkable
	if fv.bc.HasBlockAndState(block.Hash(), block.NumberU64()) && fv.bc.CurrentBlock().NumberU64() >= block.NumberU64() {
		return ErrKnownBlock
	}
	if !fv.bc.HasBlockAndState(block.ParentHash(), block.NumberU64()-1) {
		log.Error("ValidateBody method", "number", block.NumberU64()-1,
			"hash", block.ParentHash())
		if !fv.bc.HasBlock(block.ParentHash(), block.NumberU64()-1) {
			return consensus.ErrUnknownAncestor
		}
		return consensus.ErrPrunedAncestor
	}
	// validate snail hash of the sign info for prev block
	if fv.config.IsTIP9(block.Number()) && fv.config.IsTIP9(new(big.Int).Sub(block.Number(), big.NewInt(1))) {
		pHash := block.GetSignHash()
		if !bytes.Equal(pHash.Bytes(), block.Header().SnailHash.Bytes()) {
			return errors.New(fmt.Sprintf("snailhash wrong in tip9,want: %v,get: %v", pHash.Hex(), block.Header().SnailHash.Hex()))
		}
	}
	//validate reward snailBlock
	if block.SnailNumber() != nil && block.SnailNumber().Cmp(fv.config.TIP9.SnailNumber) > 0 {
		if block.SnailNumber().Sign() != 0 || block.SnailHash() != (common.Hash{}) {
			return errors.New("snail number or hash not empty when stop snail mining")
		}
	} else {
		if block.SnailNumber() != nil && block.SnailNumber().Uint64() != 0 {
			snailNumber := block.SnailNumber().Uint64()
			blockReward := fv.bc.GetBlockReward(snailNumber)

			if blockReward != nil && block.NumberU64() != blockReward.FastNumber.Uint64() {
				log.Error("validateRewardError", "rewardFastNumber", blockReward.FastNumber.Uint64(),
					"currentNumber", block.NumberU64(), "err", ErrSnailNumberAlreadyRewarded)
				return ErrSnailNumberAlreadyRewarded
			}
			supposedRewardedNumber := fv.bc.NextSnailNumberReward()
			if supposedRewardedNumber.Uint64() != snailNumber {
				log.Error("validateRewardError", "snailNumber", snailNumber,
					"supposedRewardedNumber", supposedRewardedNumber, "err", ErrRewardSnailNumberWrong)
				return ErrRewardSnailNumberWrong
			}
		}
	}
	// Header validity is known at this point, check the transactions
	header := block.Header()
	if hash := types.DeriveSha(block.Transactions()); hash != header.TxHash {
		return fmt.Errorf("transaction root hash mismatch: have %x, want %x", hash, header.TxHash)
	}

	if hash := types.RlpHash(block.SwitchInfos()); hash != header.CommitteeHash {
		return fmt.Errorf("SwitchInfos root hash mismatch: have %x, want %x", hash, header.TxHash)
	}

	if validateSign {
		if err := fv.bc.engine.VerifySigns(block.Number(), block.Hash(), block.Signs()); err != nil {
			log.Info("Fast VerifySigns Err", "number", block.NumberU64(), "signs", block.Signs())
			return err
		}

		if err := fv.bc.engine.VerifySwitchInfo(block.Number(), block.SwitchInfos()); err != nil {
			log.Info("Fast VerifySwitchInfo Err", "number", block.NumberU64(), "signs", block.SwitchInfos())
			return err
		}
	}
	return nil
}

// ValidateState validates the various changes that happen after a state
// transition, such as amount of used gas, the receipt roots and the state root
// itself. ValidateState returns a database batch if the validation was a success
// otherwise nil and an error is returned.
func (fv *BlockValidator) ValidateState(block, parent *types.Block, statedb *state.StateDB, receipts types.Receipts, usedGas uint64) error {
	header := block.Header()
	if block.GasUsed() != usedGas {
		return fmt.Errorf("invalid gas used (remote: %d local: %d)", block.GasUsed(), usedGas)
	}
	// Validate the received block's bloom with the one derived from the generated receipts.
	// For valid blocks this should always validate to true.
	rbloom := types.CreateBloom(receipts)
	if rbloom != header.Bloom {
		return fmt.Errorf("invalid bloom (remote: %x  local: %x)", header.Bloom, rbloom)
	}
	// Tre receipt Trie's root (R = (Tr [[H1, R1], ... [Hn, R1]]))
	receiptSha := types.DeriveSha(receipts)
	if receiptSha != header.ReceiptHash {
		return fmt.Errorf("invalid receipt root hash (remote: %x local: %x)", header.ReceiptHash, receiptSha)
	}
	// Validate the state root against the received state root and throw
	// an error if they don't match.
	if root := statedb.IntermediateRoot(true); header.Root != root {
		return fmt.Errorf("invalid merkle root (remote: %x local: %x)", header.Root, root)
	}
	return nil
}

// FastCalcGasLimit computes the gas limit of the next block after parent.
// This is miner strategy, not consensus protocol.
func FastCalcGasLimit(parent *types.Block, gasFloor, gasCeil uint64) uint64 {
	// contrib = (parentGasUsed * 3 / 2) / 1024
	contrib := (parent.GasUsed() + parent.GasUsed()/2) / params.GasLimitBoundDivisor

	// decay = parentGasLimit / 1024 -1
	decay := parent.GasLimit()/params.GasLimitBoundDivisor - 1

	/*
		strategy: gasLimit of block-to-mine is set based on parent's
		gasUsed value.  if parentGasUsed > parentGasLimit * (2/3) then we
		increase it, otherwise lower it (or leave it unchanged if it's right
		at that usage) the amount increased/decreased depends on how far away
		from parentGasLimit * (2/3) parentGasUsed is.
	*/
	limit := parent.GasLimit() - decay + contrib
	if limit < params.MinGasLimit {
		limit = params.MinGasLimit
	}
	// however, if we're now below the target (TargetGasLimit) we increase the
	// limit as much as we can (parentGasLimit / 1024 -1)

	// If we're outside our allowed gas range, we try to hone towards them
	if limit < gasFloor {
		limit = parent.GasLimit() + decay
		if limit > gasFloor {
			limit = gasFloor
		}
	} else if limit > gasCeil {
		limit = parent.GasLimit() - decay
		if limit < gasCeil {
			limit = gasCeil
		}
	}
	return limit
}
