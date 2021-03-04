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
	"github.com/abeychain/go-abey/core/types"
	"io/ioutil"
	"time"
	"fmt"
	//"math/big"
	"math/rand"
	"os"
	"sync"
	"testing"

	"github.com/abeychain/go-abey/params"
	"math/big"
)

// Tests that minerva works correctly in test mode
func TestTestMode(t *testing.T) {
	header := &types.SnailHeader{Number: big.NewInt(1), Difficulty: params.MinimumFruitDifficulty /*big.NewInt(150)*/, FruitDifficulty: params.MinimumFruitDifficulty /*big.NewInt(3)*/, FastNumber: big.NewInt(2)}
	minerva := NewTester()
	results := make(chan *types.SnailBlock)

	block := types.NewSnailBlockWithHeader(header)
	block.SetSnailBlockSigns(nil)
	go minerva.ConSeal(nil, block, nil, results)

	var isFruit bool
	if len(block.Fruits()) > 0 {
		isFruit = false
	} else {
		isFruit = true
	}

	select {
	case block := <-results:
		header.Nonce = types.EncodeNonce(block.Nonce())
		header.MixDigest = block.MixDigest()
		if err := minerva.VerifySnailSeal(nil, header, isFruit); err != nil {
			t.Fatalf("unexpected verification error: %v", err)
		}
	case <-time.NewTimer(time.Second * 500).C:
		t.Error("sealing result timeout")
	}
}

// This test checks that cache lru logic doesn't crash under load.
// It reproduces https://github.com/abeychain/go-abey/issues/14943
func TestCacheFileEvict(t *testing.T) {
	tmpdir, err := ioutil.TempDir("", "minerva-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpdir)
	e := New(Config{CachesInMem: 3, CachesOnDisk: 10, CacheDir: tmpdir, PowMode: ModeTest})

	workers := 8
	epochs := 0
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go verifyTest(&wg, e, i, epochs)
	}
	wg.Wait()
}

func verifyTest(wg *sync.WaitGroup, e *Minerva, workerIndex, epochs int) {

	defer wg.Done()
	const wiggle = 4 * epochLength
	r := rand.New(rand.NewSource(int64(workerIndex)))
	for epoch := 0; epoch < epochs; epoch++ {
		block := int64(epoch)*epochLength - wiggle/2 + r.Int63n(wiggle)
		if block < 0 {
			block = 1
		}
		head := &types.SnailHeader{Number: big.NewInt(block), Difficulty: big.NewInt(180), FruitDifficulty: big.NewInt(100)}
		e.VerifySnailSeal(nil, head, true)
	}
}

func TestAwardTest(t *testing.T) {
	//getCurrentBlockCoins(big.NewInt(5000));
	//fmt.Println(getCurrentCoin(big.NewInt(1)))
	//fmt.Println(getCurrentCoin(big.NewInt(5000)))
	//fmt.Println(getCurrentCoin(big.NewInt(9000)))
	//
	//fmt.Println(getBlockReward(big.NewInt(9000)))

	//snailchain.MakeChain(160,2)
	//sblock := snailChain.GetBlockByNumber(uint64(1))
	//header := &types.SnailHeader{Number: big.NewInt(1), Difficulty: big.NewInt(150), FruitDifficulty: big.NewInt(100), FastNumber: big.NewInt(2)}
	//minerva := NewTester()
	//results := make(chan *types.SnailBlock)
	//
	////block := types.NewSnailBlockWithHeader(header)
	//header :=sblock.Header()
	//go minerva.ConSeal(nil, sblock, nil, results)
	//
	//select {
	//case block := <-results:
	//	header.Fruit = block.IsFruit()
	//	header.Nonce = types.EncodeNonce(block.Nonce())
	//	header.MixDigest = block.MixDigest()
	//
	//	if err := minerva.VerifySnailSeal(nil, header); err != nil {
	//		t.Fatalf("unexpected verification error: %v", err)
	//	}
	//case <-time.NewTimer(time.Second * 500).C:
	//	t.Error("sealing result timeout")
	//}

}

func TestNewAlgorithm(t *testing.T) {
	config := Config{
		CacheDir:       "minerva",
		CachesInMem:    2,
		CachesOnDisk:   3,
		DatasetsInMem:  1,
		DatasetsOnDisk: 2,
		Tip9:	 uint64(47000),
	}
	minerva := &Minerva{
		config: config,
		datasets: newlru("dataset", config.DatasetsInMem, NewDataset),
		update:   make(chan struct{}),
	}
	minerva.getDataset(1)

	block := uint64(47000)
	for i:= 0;i<1000;i++ {
		block = block + uint64(i)
		ds := minerva.getDataset(block)
		if ds != nil {
			fmt.Println("len:",len(ds.dataset))
		}
	}
	fmt.Println("finish")
}
