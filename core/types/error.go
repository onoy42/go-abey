package types

import "errors"

var (
	// ErrHeightNotYet When the height of the committee is higher than the local height, it is issued.
	ErrHeightNotYet = errors.New("pbft send block height not yet")

	// ErrSnailHeightNotYet Snail height not yet
	ErrSnailHeightNotYet = errors.New("Snail height not yet")

	//ErrSnailBlockNotOnTheCain Snail block not on the cain
	ErrSnailBlockNotOnTheCain = errors.New("Snail block not on the chain")

	//ErrSnailBlockTooSlow Snail block too slow
	ErrSnailBlockTooSlow = errors.New("Snail block too slow")

	ErrPayersign = errors.New("signed_addr not equal tx.data.Payer")
)
