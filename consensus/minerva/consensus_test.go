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
	"encoding/json"
	"fmt"
	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/common/math"
	"github.com/abeychain/go-abey/core/types"
	"github.com/abeychain/go-abey/params"
	osMath "math"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var (
	FrontierBlockReward = big.NewInt(5e+18) // Block reward in wei for successfully mining a block
	//SnailBlockRewardsInitial Snail block rewards initial 116.48733*10^18
	SnailBlockRewardsInitial = new(big.Int).Mul(big.NewInt(11648733), big.NewInt(1e13))
)

type diffTest struct {
	ParentTimestamp    uint64
	ParentDifficulty   *big.Int
	CurrentTimestamp   uint64
	CurrentBlocknumber *big.Int
	CurrentDifficulty  *big.Int
}

func (d *diffTest) UnmarshalJSON(b []byte) (err error) {
	var ext struct {
		ParentTimestamp    string
		ParentDifficulty   string
		CurrentTimestamp   string
		CurrentBlocknumber string
		CurrentDifficulty  string
	}
	if err := json.Unmarshal(b, &ext); err != nil {
		return err
	}

	d.ParentTimestamp = math.MustParseUint64(ext.ParentTimestamp)
	d.ParentDifficulty = math.MustParseBig256(ext.ParentDifficulty)
	d.CurrentTimestamp = math.MustParseUint64(ext.CurrentTimestamp)
	d.CurrentBlocknumber = math.MustParseBig256(ext.CurrentBlocknumber)
	d.CurrentDifficulty = math.MustParseBig256(ext.CurrentDifficulty)

	return nil
}

//func (d *diffTest) MakeSnailChain（）{
//	blocks := snailchain.GenerateChain

//}

func TestCalcSnailDifficulty(t *testing.T) {
	var SnailHeadersMap map[*big.Int][]*types.SnailHeader
	var targeDiff []*big.Int
	var timeCurrent []*big.Int
	n := 4
	SnailHeadersMap = make(map[*big.Int][]*types.SnailHeader)
	parents := make([]*types.SnailHeader, n)
	//timeCurrent = make([]*big.Int,n)

	parents[0] = &types.SnailHeader{
		Number:     new(big.Int).SetUint64(0),
		Time:       new(big.Int).SetUint64(60),
		Difficulty: new(big.Int).SetUint64(2000000),
	}
	timeCurrent = append(timeCurrent, new(big.Int).SetUint64(90))
	//SnailHeadersMap[]
	parents[1] = &types.SnailHeader{
		Number:     new(big.Int).SetUint64(1),
		Time:       new(big.Int).SetUint64(90),
		Difficulty: new(big.Int).SetUint64(2000000),
	}
	//SnailHeadersMap[]
	timeCurrent = append(timeCurrent, new(big.Int).SetUint64(100))
	parents[2] = &types.SnailHeader{
		Number:     new(big.Int).SetUint64(2),
		Time:       new(big.Int).SetUint64(100),
		Difficulty: new(big.Int).SetUint64(2983333),
	}
	timeCurrent = append(timeCurrent, new(big.Int).SetUint64(150))
	parents[3] = &types.SnailHeader{
		Number:     new(big.Int).SetUint64(3),
		Time:       new(big.Int).SetUint64(150),
		Difficulty: new(big.Int).SetUint64(3675207),
	}
	timeCurrent = append(timeCurrent, new(big.Int).SetUint64(160))

	SnailHeadersMap[new(big.Int).SetUint64(0)] = parents[:1]
	SnailHeadersMap[new(big.Int).SetUint64(1)] = parents[:2]
	SnailHeadersMap[new(big.Int).SetUint64(2)] = parents[:3]
	SnailHeadersMap[new(big.Int).SetUint64(3)] = parents[:4]

	tdiff := new(big.Int).SetUint64(2000000)
	//for i:0;i<n;i++{
	targeDiff = append(targeDiff, tdiff)
	tdiff = new(big.Int).SetUint64(2983333)
	//for i:0;i<n;i++{
	targeDiff = append(targeDiff, tdiff)
	tdiff = new(big.Int).SetUint64(3675207)
	//for i:0;i<n;i++{
	targeDiff = append(targeDiff, tdiff)
	tdiff = new(big.Int).SetUint64(4273149)
	//for i:0;i<n;i++{
	targeDiff = append(targeDiff, tdiff)

	config := &params.ChainConfig{ChainID: big.NewInt(1), Minerva: &params.MinervaConfig{MinimumDifficulty: params.MinimumDifficulty, MinimumFruitDifficulty: params.MinimumFruitDifficulty, DurationLimit: params.DurationLimit}, TIP3: &params.BlockConfig{FastNumber: common.Big0}, TIP5: &params.BlockConfig{FastNumber: common.Big0}}

	for k, v := range SnailHeadersMap {
		//i:= timeCurrent[k.Uint64()]
		//t.Error( "failed. Expected", targeDiff[k.Uint64()], "time", timeCurrent[k.Uint64()],"v lenght",len(v))

		diff := CalcDifficulty(config, timeCurrent[k.Uint64()].Uint64(), v)

		if targeDiff[k.Uint64()].Cmp(diff) != 0 {
			t.Error("fail targ", targeDiff[k.Uint64()], "and calculated", diff, "K", k, "v len", len(v))
		} else {
			fmt.Println("success tag diff", targeDiff[k.Uint64()], "and and calculated", diff, "K", k, "v len", len(v))
		}
	}

}
func TestCalcDifficulty(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "tests", "testdata", "BasicTests", "difficulty.json"))
	if err != nil {
		t.Skip(err)
	}
	defer file.Close()

	tests := make(map[string]diffTest)
	err = json.NewDecoder(file).Decode(&tests)
	if err != nil {
		t.Fatal(err)
	}

	//config := &params.ChainConfig{HomesteadBlock: big.NewInt(1150000)}
	config := &params.ChainConfig{}
	var parents []*types.SnailHeader

	for name, test := range tests {
		number := new(big.Int).Sub(test.CurrentBlocknumber, big.NewInt(1))
		/*parents[1]=&types.SnailHeader{
			Number:     number,
			Time:       new(big.Int).SetUint64(test.ParentTimestamp),
			Difficulty: test.ParentDifficulty,
		}*/
		parents = append(parents, &types.SnailHeader{Number: number, Time: new(big.Int).SetUint64(test.ParentTimestamp), Difficulty: test.ParentDifficulty})

		diff := CalcDifficulty(config, test.CurrentTimestamp, parents)
		if diff.Cmp(test.CurrentDifficulty) != 0 {
			t.Error(name, "failed. Expected", test.CurrentDifficulty, "and calculated", diff)
		}
	}
}

func TestAccountDiv(t *testing.T) {
	r := new(big.Int)
	println(r.Uint64())
	r = big.NewInt(600077777777777)
	println(r.Uint64())
	r.Div(r, big2999999)
	println(r.Uint64(), FrontierBlockReward.Uint64(), SnailBlockRewardsInitial.Bytes())
	fmt.Printf("%v", new(big.Int).Exp(new(big.Int).SetInt64(2),
		new(big.Int).Div(new(big.Int).Add(new(big.Int).SetInt64(5000), new(big.Int).SetInt64(12)), new(big.Int).SetInt64(5000)), nil))
}

func TestOutSqrt(t *testing.T) {
	var AConstSqrt []ConstSqrt
	var ARR []float64
	for i := 1; i <= 10000; i++ {

		tmp := osMath.Sqrt(float64(i)) / (osMath.Sqrt(float64(i)) + float64(MiningConstant))

		if tmp > 0.8 {
			break
		}

		if tmp < 0.2 {
			ARR = append(ARR, tmp)
			continue
		}
		ARR = append(ARR, tmp)
		AConstSqrt = append(AConstSqrt, ConstSqrt{Num: i, Sqrt: tmp})
	}

	b, _ := json.Marshal(AConstSqrt)
	fmt.Println(ARR)
	fmt.Println(string(b))
}

//Calculate the reward distribution corresponding to the slow block height
//There is a new distribution incentive for every 4,500 blocks.
//The unit of output is wei
//6 bits at the end are cleared
func TestSnailAwardForHeight(t *testing.T) {
	for i := 1; i < 1000; i++ {
		snailBlockNumber := new(big.Int).SetInt64(int64(1 + 4500*(i-1)))
		committeeAward, minerAward, minerFruitAward := GetBlockReward(snailBlockNumber)
		fmt.Println("committeeAward:", committeeAward, "minerAward:", minerAward, "minerFruitAward:", minerFruitAward)
	}
}
func TestReward2(t *testing.T) {
	snailNum := 210000 - 2
	allReward := big.NewInt(0)

	for i := 0; i < 25; i++ {
		num := big.NewInt(int64(i * snailNum))

		cc, mm, mf := GetBlockReward(num)
		fmt.Println("committeeAward:", cc, "minerAward:", mm,
			"minerFruitAward:", mf)
		fmt.Println("committeeAward:", toAbeyCoin(cc).Text('f', 6), "minerAward:", toAbeyCoin(mm).Text('f', 6),
			"minerFruitAward:", toAbeyCoin(mf).Text('f', 6),
			"all:", toAbeyCoin(new(big.Int).Add(mm, mf)).Text('f', 6))
		snailReward1 := new(big.Int).Add(cc.Add(cc, mm), mf)
		allReward = new(big.Int).Add(allReward, snailReward1)
	}
	fmt.Println("allReward", allReward)
	fmt.Println("allReward", toAbeyCoin(allReward).Text('f', 10))

	fmt.Println("finish")
}

func toAbeyCoin(val *big.Int) *big.Float {
	return new(big.Float).Quo(new(big.Float).SetInt(val), new(big.Float).SetInt(BaseBig))
}

func TestTime(t *testing.T) {
	t1 := time.Now()
	time.Sleep(time.Millisecond * time.Duration(600))
	t2 := time.Now()
	d := t2.Sub(t1)
	fmt.Println("d:", d.Seconds())
	if d.Seconds() > float64(0.5) {
		fmt.Println("good")
	}
	fmt.Println("finish")
}
func TestRewardWithTimes01(t *testing.T) {
	// update the constant of rewards
	params.StartPosRewardHeight = big.NewInt(0)
	params.BlocksInOneYear = big.NewInt(100)
	fmt.Println("init reward", toAbeyCoin(params.InitReward).Text('f', 6))

	count, old := int64(10000), big.NewInt(0)
	for i := int64(0); i < count; i++ {
		value := getBaseRewardCoinForPos2(big.NewInt(i))
		if old.Cmp(value) != 0 {
			old = new(big.Int).Set(value)
			fmt.Println("block height", i, "reward", toAbeyCoin(value).Text('f', 6))
		}
	}
}
