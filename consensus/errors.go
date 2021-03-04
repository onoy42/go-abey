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

package consensus

import "errors"

var (
	// ErrUnknownAncestor is returned when validating a block requires an ancestor
	// that is unknown.
	ErrUnknownAncestor = errors.New("unknown ancestor")

	//ErrBlockOnChain is returned when validating a block but it is
	//already on the fast chain.
	ErrBlockOnChain = errors.New("block already insert fastchain")

	// ErrForkFastBlock is returned when validating a block but the same number other
	// block already in the chain.
	ErrForkFastBlock = errors.New("fork fastBlock")

	// ErrPrunedAncestor is returned when validating a block requires an ancestor
	// that is known, but the state of which is not available.
	ErrPrunedAncestor = errors.New("pruned ancestor")

	// ErrFutureBlock is returned when a block's timestamp is in the future according
	// to the current node.
	ErrFutureBlock = errors.New("block in the future")

	// ErrTooFutureBlock is returned when a block's number is too future than
	// the current fastblock.
	ErrTooFutureBlock = errors.New("fruit is too higher than current fastblock")
	// ErrTooOldBlock is returned when a block's number is too old than
	// the current fastblock.
	ErrTooOldBlock = errors.New("this hight's fruit already packed in the snailchain")
	// ErrInvalidNumber is returned if a block's number doesn't equal it's parent's
	// plus one.
	ErrInvalidNumber = errors.New("invalid block number")

	//ErrInvalidSignsLength If the number of returned committees and the results are inconsistent, return ErrInvalidSignsLength
	ErrInvalidSignsLength = errors.New("invalid signs length")

	// ErrValidSignsZero is returned if a block's signs length is zero
	ErrValidSignsZero = errors.New("valid signs length equal zero in fruit")

	ErrInvalidBlock = errors.New("invalid snail block")

	ErrUnknownPointer = errors.New("unknown pointer hash")

	ErrFreshness = errors.New("invalid fruit freshness")

	ErrInvalidSign = errors.New("invalid sign")

	ErrInvalidSwitchInfo = errors.New("invalid switch info")

	ErrUnknownFast = errors.New("unknown fast block")

	//ErrInvalidFast is returned if the fastchain not have the hash
	ErrInvalidFast = errors.New("invalid fast hash")

	//ErrFruitTime is returned if the fruit's time less than fastblock's time
	ErrFruitTime = errors.New("invalid fruit time")
)
