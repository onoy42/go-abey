package types

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"

	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/common/hexutil"
	"github.com/abeychain/go-abey/crypto"
	"github.com/abeychain/go-abey/params"
)

var (
	baseUnit   = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	fbaseUnit  = new(big.Float).SetFloat64(float64(baseUnit.Int64()))
	mixImpawn  = new(big.Int).Mul(big.NewInt(1000), baseUnit)
	Base       = new(big.Int).SetUint64(10000)
	InvalidFee = big.NewInt(65535)
	// StakingAddress is defined as Address('truestaking')
	// i.e. contractAddress = 0x000000000000000000747275657374616b696E67
	StakingAddress = common.BytesToAddress([]byte("truestaking"))
	MixEpochCount  = 2
	whitelist      = []common.Address{
		common.HexToAddress("0xA218B46345B13b0c5E3E5625a1e1bb0b025FDD13"),
		common.HexToAddress("0xd4f226f45a4030FB060e3cDc584D2eD0d3b474FE"),
		common.HexToAddress("0x574e7b464340787A9de1A68784a89edF616768Fe"),
		common.HexToAddress("0x7cb1024f02394CcE5F51dCdE0bb07e6B4358489b"),
		common.HexToAddress("0x36840bBcF8bEfE91BDCc05046D76a5ba970c9317"),
		common.HexToAddress("0x29Cd0c9385604f39966a3D1Ac88eFEca4309272f"),
		common.HexToAddress("0x34b73756bde8d9ba0d3efa090838f8e71ce14bca"),
		common.HexToAddress("0x8436c9882b6dDa3df48d673a623EAd675373914c"),
		common.HexToAddress("0x8818d143773426071068C514Db25106338009363"),
		common.HexToAddress("0x4eD71f64C4Dbd037B02BC4E1bD6Fd6900fcFd396"),
		common.HexToAddress("0x36939d3324bd522Baba28b3F142Fed395A9751B9"),
		common.HexToAddress("0x76fC12940EC8022D0F6D4d570d5cd685D223B29e"),
	}
)

var (
	ErrInvalidParam      = errors.New("Invalid Param")
	ErrOverEpochID       = errors.New("Over epoch id")
	ErrNotSequential     = errors.New("epoch id not sequential")
	ErrInvalidEpochInfo  = errors.New("Invalid epoch info")
	ErrNotFoundEpoch     = errors.New("cann't found the epoch info")
	ErrInvalidStaking    = errors.New("Invalid staking account")
	ErrMatchEpochID      = errors.New("wrong match epoch id in a reward block")
	ErrNotStaking        = errors.New("Not match the staking account")
	ErrNotDelegation     = errors.New("Not match the delegation account")
	ErrNotMatchEpochInfo = errors.New("the epoch info is not match with accounts")
	ErrNotElectionTime   = errors.New("not time to election the next committee")
	ErrAmountOver        = errors.New("the amount more than staking amount")
	ErrDelegationSelf    = errors.New("Cann't delegation myself")
	ErrRedeemAmount      = errors.New("wrong redeem amount")
	ErrForbidAddress     = errors.New("Forbidding Address")
	ErrRepeatPk          = errors.New("repeat PK on staking tx")
)

const (
	// StateStakingOnce can be election only once
	StateStakingOnce uint8 = 1 << iota
	// StateStakingAuto can be election in every epoch
	StateStakingAuto
	StateStakingCancel
	// StateRedeem can be redeem real time (after MaxRedeemHeight block)
	StateRedeem
	// StateRedeemed flag the asset which is staking in the height is redeemed
	StateRedeemed
)
const (
	OpQueryStaking uint8 = 1 << iota
	OpQueryLocked
	OpQueryCancelable
)

type SummayEpochInfo struct {
	EpochID     uint64
	SaCount     uint64
	DaCount     uint64
	BeginHeight uint64
	EndHeight   uint64
	AllAmount   *big.Int
}
type ImpawnSummay struct {
	LastReward uint64
	Accounts   uint64
	AllAmount  *big.Int
	Infos      []*SummayEpochInfo
}

func ToJSON(ii *ImpawnSummay) map[string]interface{} {
	item := make(map[string]interface{})
	item["lastRewardHeight"] = ii.LastReward
	item["AccountsCounts"] = ii.Accounts
	item["currentAllStaking"] = (*hexutil.Big)(ii.AllAmount)
	items := make([]map[string]interface{}, 0, 0)
	for _, val := range ii.Infos {
		info := make(map[string]interface{})
		info["EpochID"] = val.EpochID
		info["SaCount"] = val.SaCount
		info["DaCount"] = val.DaCount
		info["BeginHeight"] = val.BeginHeight
		info["EndHeight"] = val.EndHeight
		info["AllAmount"] = (*hexutil.Big)(val.AllAmount)
		items = append(items, info)
	}
	item["EpochInfos"] = items
	return item
}

type RewardInfo struct {
	Address common.Address `json:"Address"`
	Amount  *big.Int       `json:"Amount"`
	Staking *big.Int       `json:"Staking"`
}

func (e *RewardInfo) clone() *RewardInfo {
	return &RewardInfo{
		Address: e.Address,
		Amount:  new(big.Int).Set(e.Amount),
		Staking: new(big.Int).Set(e.Staking),
	}
}
func (e *RewardInfo) String() string {
	return fmt.Sprintf("[Address:%v,Amount:%s\n]", e.Address.String(), ToAbey(e.Amount).Text('f', 8))
}
func (e *RewardInfo) ToJson() map[string]interface{} {
	item := make(map[string]interface{})
	item["Address"] = e.Address.StringToAbey()
	item["Amount"] = (*hexutil.Big)(e.Amount)
	item["Staking"] = (*hexutil.Big)(e.Staking)

	return item
}
func FetchOne(sas []*SARewardInfos, addr common.Address) []*RewardInfo {
	items := make([]*RewardInfo, 0, 0)
	for _, val := range sas {
		if len(val.Items) > 0 {
			saAddr := val.getSaAddress()
			if bytes.Equal(saAddr.Bytes(), addr.Bytes()) {
				items = mergeRewardInfos(items, val.Items)
			}
		}
	}
	return items
}
func FetchOneToAbey(sas []*SARewardInfos, addr common.Address) map[string]interface{} {
	items := make([]*RewardInfo, 0, 0)
	for _, val := range sas {
		if len(val.Items) > 0 {
			saAddr := val.getSaAddress()
			if bytes.Equal(saAddr.Bytes(), addr.Bytes()) {
				items = mergeRewardInfos(items, val.Items)
			}
		}
	}
	infos := make([]map[string]interface{}, 0, 0)
	for _, v := range items {
		infos = append(infos, v.ToJson())
	}
	return map[string]interface{}{
		"committeeReward": infos,
	}
}
func mergeRewardInfos(items1, itmes2 []*RewardInfo) []*RewardInfo {
	for _, v1 := range itmes2 {
		found := false
		for _, v2 := range items1 {
			if bytes.Equal(v1.Address.Bytes(), v2.Address.Bytes()) {
				found = true
				v2.Amount = new(big.Int).Add(v2.Amount, v1.Amount)
			}
		}
		if !found {
			items1 = append(items1, v1)
		}
	}
	return items1
}

type SARewardInfos struct {
	Items []*RewardInfo `json:"Items"`
}

func (s *SARewardInfos) clone() *SARewardInfos {
	var res SARewardInfos
	for _, v := range s.Items {
		res.Items = append(res.Items, v.clone())
	}
	return &res
}
func (s *SARewardInfos) getSaAddress() common.Address {
	if len(s.Items) > 0 {
		return s.Items[0].Address
	}
	return common.Address{}
}

func (s *SARewardInfos) String() string {
	var ss string
	for _, v := range s.Items {
		ss += v.String()
	}
	return ss
}
func (s *SARewardInfos) StringToAbey() map[string]interface{} {
	ss := make([]map[string]interface{}, 0, 0)
	for _, v := range s.Items {
		ss = append(ss, v.ToJson())
	}
	item := make(map[string]interface{})
	item["SaReward"] = ss
	return item
}

type TimedChainReward struct {
	St     uint64
	Number uint64
	Reward *ChainReward
}

type ChainReward struct {
	Height        uint64
	St            uint64
	CoinBase      *RewardInfo      `json:"blockminer"`
	FruitBase     []*RewardInfo    `json:"fruitminer"`
	CommitteeBase []*SARewardInfos `json:"committeeReward"`
}

func (s *ChainReward) CoinRewardInfo() map[string]interface{} {
	feild := map[string]interface{}{
		"blockminer": s.CoinBase.ToJson(),
	}
	return feild
}
func (s *ChainReward) FruitRewardInfo() map[string]interface{} {
	infos := make([]map[string]interface{}, 0, 0)
	for _, v := range s.FruitBase {
		infos = append(infos, v.ToJson())
	}
	feild := map[string]interface{}{
		"fruitminer": infos,
	}
	return feild
}
func (s *ChainReward) CommitteeRewardInfo() map[string]interface{} {
	infos := make([]map[string]interface{}, 0, 0)
	for _, v := range s.CommitteeBase {
		infos = append(infos, v.StringToAbey())
	}
	feild := map[string]interface{}{
		"committeeReward": infos,
	}
	return feild
}

func CloneChainReward(reward *ChainReward) *ChainReward {
	var res ChainReward
	res.Height, res.St = reward.Height, reward.St
	res.CoinBase = reward.CoinBase.clone()
	for _, v := range reward.FruitBase {
		res.FruitBase = append(res.FruitBase, v.clone())
	}
	for _, v := range reward.CommitteeBase {
		res.CommitteeBase = append(res.CommitteeBase, v.clone())
	}
	return &res
}

type BalanceInfo struct {
	Address common.Address `json:"address"`
	Valid   *big.Int       `json:"valid"`
	Lock    *big.Int       `json:"lock"`
}

type BlockBalance struct {
	Balance []*BalanceInfo `json:"addrWithBalance"       gencodec:"required"`
}

func (s *BlockBalance) ToMap() map[common.Address]*BalanceInfo {
	infos := make(map[common.Address]*BalanceInfo)
	for _, v := range s.Balance {
		infos[v.Address] = v
	}
	return infos
}

func ToBalanceInfos(items map[common.Address]*BalanceInfo) []*BalanceInfo {
	infos := make([]*BalanceInfo, 0, 0)
	for k, v := range items {
		infos = append(infos, &BalanceInfo{
			Address: k,
			Valid:   new(big.Int).Set(v.Valid),
			Lock:    new(big.Int).Set(v.Lock),
		})
	}
	return infos
}

func NewChainReward(height, tt uint64, coin *RewardInfo, fruits []*RewardInfo, committee []*SARewardInfos) *ChainReward {
	return &ChainReward{
		Height:        height,
		St:            tt,
		CoinBase:      coin,
		FruitBase:     fruits,
		CommitteeBase: committee,
	}
}
func ToRewardInfos1(items map[common.Address]*big.Int) []*RewardInfo {
	infos := make([]*RewardInfo, 0, 0)
	for k, v := range items {
		infos = append(infos, &RewardInfo{
			Address: k,
			Amount:  new(big.Int).Set(v),
		})
	}
	return infos
}
func ToRewardInfos2(items map[common.Address]*big.Int) []*SARewardInfos {
	infos := make([]*SARewardInfos, 0, 0)
	for k, v := range items {
		items := []*RewardInfo{&RewardInfo{
			Address: k,
			Amount:  new(big.Int).Set(v),
		}}

		infos = append(infos, &SARewardInfos{
			Items: items,
		})
	}
	return infos
}
func MergeReward(map1, map2 map[common.Address]*big.Int) map[common.Address]*big.Int {
	for k, v := range map2 {
		if vv, ok := map1[k]; ok {
			map1[k] = new(big.Int).Add(vv, v)
		} else {
			map1[k] = v
		}
	}
	return map1
}

type EpochIDInfo struct {
	EpochID     uint64
	BeginHeight uint64
	EndHeight   uint64
}

func (e *EpochIDInfo) isValid() bool {
	if e.EpochID < 0 {
		return false
	}
	if e.EpochID == 0 && params.DposForkPoint+1 != e.BeginHeight {
		return false
	}
	if e.BeginHeight < 0 || e.EndHeight <= 0 || e.EndHeight <= e.BeginHeight {
		return false
	}
	return true
}
func (e *EpochIDInfo) String() string {
	return fmt.Sprintf("[id:%v,begin:%v,end:%v]", e.EpochID, e.BeginHeight, e.EndHeight)
}

// the key is epochid if StakingValue as a locked asset,otherwise key is block height if StakingValue as a staking asset
type StakingValue struct {
	Value map[uint64]*big.Int
}

type LockedItem struct {
	Amount *big.Int
	Locked bool
}

// LockedValue,the key of Value is epochid
type LockedValue struct {
	Value map[uint64]*LockedItem
}

func (s *StakingValue) ToLockedValue(height uint64) *LockedValue {
	res := make(map[uint64]*LockedItem)
	for k, v := range s.Value {
		item := &LockedItem{
			Amount: new(big.Int).Set(v),
			Locked: !IsUnlocked(k, height),
		}
		res[k] = item
	}
	return &LockedValue{
		Value: res,
	}
}

func toReward(val *big.Float) *big.Int {
	val = val.Mul(val, fbaseUnit)
	ii, _ := val.Int64()
	return big.NewInt(ii)
}
func ToAbey(val *big.Int) *big.Float {
	return new(big.Float).Quo(new(big.Float).SetInt(val), fbaseUnit)
}
func FromBlock(block *SnailBlock) (begin, end uint64) {
	begin, end = 0, 0
	l := len(block.Fruits())
	if l > 0 {
		begin, end = block.Fruits()[0].FastNumber().Uint64(), block.Fruits()[l-1].FastNumber().Uint64()
	}
	return
}
func GetFirstEpoch() *EpochIDInfo {
	return &EpochIDInfo{
		EpochID:     params.FirstNewEpochID,
		BeginHeight: params.DposForkPoint + 1,
		EndHeight:   params.DposForkPoint + params.NewEpochLength,
	}
}
func GetPreFirstEpoch() *EpochIDInfo {
	return &EpochIDInfo{
		EpochID:     params.FirstNewEpochID - 1,
		BeginHeight: 0,
		EndHeight:   params.DposForkPoint,
	}
}
func GetEpochFromHeight(hh uint64) *EpochIDInfo {
	if hh <= params.DposForkPoint {
		return GetPreFirstEpoch()
	}
	first := GetFirstEpoch()
	if hh <= first.EndHeight {
		return first
	}
	var eid uint64
	if (hh-first.EndHeight)%params.NewEpochLength == 0 {
		eid = (hh-first.EndHeight)/params.NewEpochLength + first.EpochID
	} else {
		eid = (hh-first.EndHeight)/params.NewEpochLength + first.EpochID + 1
	}
	return GetEpochFromID(eid)
}
func GetEpochFromID(eid uint64) *EpochIDInfo {
	preFirst := GetPreFirstEpoch()
	if preFirst.EpochID == eid {
		return preFirst
	}
	first := GetFirstEpoch()
	if first.EpochID >= eid {
		return first
	}
	return &EpochIDInfo{
		EpochID:     eid,
		BeginHeight: first.EndHeight + (eid-first.EpochID-1)*params.NewEpochLength + 1,
		EndHeight:   first.EndHeight + (eid-first.EpochID)*params.NewEpochLength,
	}
}
func GetEpochFromRange(begin, end uint64) []*EpochIDInfo {
	if end == 0 || begin > end || (begin < params.DposForkPoint && end < params.DposForkPoint) {
		return nil
	}
	var ids []*EpochIDInfo
	e1 := GetEpochFromHeight(begin)
	e := uint64(0)

	if e1 != nil {
		ids = append(ids, e1)
		e = e1.EndHeight
	} else {
		e = params.DposForkPoint
	}
	for e < end {
		e2 := GetEpochFromHeight(e + 1)
		if e1.EpochID != e2.EpochID {
			ids = append(ids, e2)
		}
		e = e2.EndHeight
	}

	if len(ids) == 0 {
		return nil
	}
	return ids
}
func CopyVotePk(pk []byte) []byte {
	cc := make([]byte, len(pk))
	copy(cc, pk)
	return cc
}
func ValidPk(pk []byte) error {
	_, err := crypto.UnmarshalPubkey(pk)
	return err
}
func MinCalcRedeemHeight(eid uint64) uint64 {
	e := GetEpochFromID(eid + 1)
	return e.BeginHeight + params.MaxRedeemHeight + 1
}
func ForbidAddress(addr common.Address) error {
	if bytes.Equal(addr[:], StakingAddress[:]) {
		return errors.New(fmt.Sprint("addr error:", addr.String(), " ", ErrForbidAddress))
	}
	for _, addr0 := range whitelist {
		if bytes.Equal(addr[:], addr0[:]) {
			return errors.New(fmt.Sprint("addr error:", addr.String(), " ", ErrForbidAddress))
		}
	}
	return nil
}
func IsUnlocked(eid, height uint64) bool {
	e := GetEpochFromID(eid + 1)
	return height > e.BeginHeight+params.MaxRedeemHeight
}
