package abey

import (
	"fmt"
	"github.com/abeychain/go-abey/core/types"
	"time"

	"bytes"
	"crypto/ecdsa"
	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/crypto"
	"github.com/abeychain/go-abey/log"
	"github.com/abeychain/go-abey/core"
	"github.com/abeychain/go-abey/abeydb"
	"github.com/abeychain/go-abey/params"
	"testing"
)

const (
	sendNodeTimeTest = 30 * time.Second
	memberNumber     = 16
	backMemberNumber = 14
)

var (
	agent *PbftAgent
)

func init() {
	agent = NewPbftAgetTest()
}

func NewPbftAgetTest() *PbftAgent {
	priKey, _ := crypto.GenerateKey()
	coinbase := crypto.PubkeyToAddress(priKey.PublicKey) //coinbase
	committeeNode := &types.CommitteeNode{
		IP:        "127.0.0.1",
		Port:      8080,
		Port2:     8090,
		Coinbase:  coinbase,
		Publickey: crypto.FromECDSAPub(&priKey.PublicKey),
	}
	//PrintNode("send", committeeNode)
	pbftAgent := &PbftAgent{
		privateKey:    priKey,
		committeeNode: committeeNode,
	}
	return pbftAgent
}

func generateCommitteeMemberBySelfPriKey() *types.CommitteeMember {
	priKey := agent.privateKey
	committeeBase := crypto.PubkeyToAddress(priKey.PublicKey) //coinbase
	pubKeyBytes := crypto.FromECDSAPub(&priKey.PublicKey)
	committeeMember := &types.CommitteeMember{
		Coinbase: common.Address{}, CommitteeBase: committeeBase,
		Publickey: pubKeyBytes, Flag: 0xa1, MType: 0}

	return committeeMember
}

func generateMember() (*ecdsa.PrivateKey, *types.CommitteeMember) {
	priKey, _ := crypto.GenerateKey()
	committeeBase := crypto.PubkeyToAddress(priKey.PublicKey) //coinbase
	pubKeyBytes := crypto.FromECDSAPub(&priKey.PublicKey)
	committeeMember := &types.CommitteeMember{
		Coinbase: common.Address{}, CommitteeBase: committeeBase,
		Publickey: pubKeyBytes, Flag: 0xa1, MType: 0}
	return priKey, committeeMember
}

func initCommitteeInfo() (*types.CommitteeInfo, []*ecdsa.PrivateKey) {
	var priKeys []*ecdsa.PrivateKey
	committeeInfo := &types.CommitteeInfo{
		Id: common.Big1,
	}
	for i := 0; i < memberNumber; i++ {
		priKey, committeMember := generateMember()
		priKeys = append(priKeys, priKey)
		committeeInfo.Members = append(committeeInfo.Members, committeMember)
	}
	for i := 0; i < backMemberNumber; i++ {
		priKey, backCommitteMember := generateMember()
		priKeys = append(priKeys, priKey)
		committeeInfo.BackMembers = append(committeeInfo.BackMembers, backCommitteMember)
	}
	return committeeInfo, priKeys
}

func initCommitteeInfoIncludeSelf() *types.CommitteeInfo {
	committeeInfo, _ := initCommitteeInfo()
	committeeMember := generateCommitteeMemberBySelfPriKey()
	committeeInfo.Members = append(committeeInfo.Members, committeeMember)
	return committeeInfo
}

func TestSendAndReceiveCommitteeNode(t *testing.T) {
	committeeInfo := initCommitteeInfoIncludeSelf()
	t.Log(agent.committeeNode)
	cryNodeInfo := encryptNodeInfo(committeeInfo, agent.committeeNode, agent.privateKey)
	t.Log(len(cryNodeInfo.Nodes))
	pk := &agent.privateKey.PublicKey // received pk
	receivedCommitteeNode := decryptNodeInfo(cryNodeInfo, agent.privateKey, pk)
	t.Log(receivedCommitteeNode)
}

func TestSendAndReceiveCommitteeNode2(t *testing.T) {
	committeeInfo, _ := initCommitteeInfo()
	t.Log(agent.committeeNode)
	cryNodeInfo := encryptNodeInfo(committeeInfo, agent.committeeNode, agent.privateKey)
	pk := &agent.privateKey.PublicKey // received pk
	receivedCommitteeNode := decryptNodeInfo(cryNodeInfo, agent.privateKey, pk)
	t.Log(receivedCommitteeNode)
}

func validateSign(fb *types.Block, prikey *ecdsa.PrivateKey) bool {
	sign, err := agent.GenerateSign(fb)
	if err != nil {
		log.Error("err", err)
		return false
	}
	signHash := sign.HashWithNoSign().Bytes()
	pubKey, err := crypto.SigToPub(signHash, sign.Sign)
	if err != nil {
		fmt.Println("get pubKey error", err)
	}
	pubBytes := crypto.FromECDSAPub(pubKey)
	pubBytes2 := crypto.FromECDSAPub(&prikey.PublicKey)
	if bytes.Equal(pubBytes, pubBytes2) {
		return true
	}
	return false
}

func generateFastBlock() *types.Block {
	db := abeydb.NewMemDatabase()
	BaseGenesis := new(core.Genesis)
	genesis := BaseGenesis.MustFastCommit(db)
	header := &types.Header{
		ParentHash: genesis.Hash(),
		Number:     common.Big1,
		GasLimit:   core.FastCalcGasLimit(genesis, params.GenesisGasLimit, params.GenesisGasLimit),
	}
	fb := types.NewBlock(header, nil, nil, nil, nil)
	return fb
}

func TestGenerateSign(t *testing.T) {
	fb := generateFastBlock()
	t.Log(validateSign(fb, agent.privateKey))
}

func TestGenerateSign2(t *testing.T) {
	fb := generateFastBlock()
	priKey, _ := crypto.GenerateKey()
	t.Log(validateSign(fb, priKey))
}

func TestNodeWorkStartAndEnd(t *testing.T) {
	agent.initNodeWork()
	receivedCommitteeInfo := initCommitteeInfoIncludeSelf()
	for i := 0; i < 3; i++ {
		StartNodeWork(receivedCommitteeInfo, true, t)
		time.Sleep(time.Second * 4)
		StopNodeWork(t)
		time.Sleep(time.Second * 4)
	}
}

func StartNodeWork(receivedCommitteeInfo *types.CommitteeInfo, isCommitteeMember bool, t *testing.T) *types.EncryptNodeMessage {
	var cryNodeInfo *types.EncryptNodeMessage
	nodeWork := agent.updateCurrentNodeWork()
	//load nodeWork
	nodeWork.loadNodeWork(receivedCommitteeInfo, isCommitteeMember)
	if nodeWork.isCommitteeMember {
		t.Log("node in pbft committee", "committeeId=", receivedCommitteeInfo.Id)
		nodeWork.ticker = time.NewTicker(sendNodeTimeTest)
		go func() {
			for {
				select {
				case <-nodeWork.ticker.C:
					cryNodeInfo = encryptNodeInfo(nodeWork.committeeInfo, agent.committeeNode, agent.privateKey)
					t.Log("send", cryNodeInfo)
				}
			}
		}()
	} else {
		t.Log("node not in pbft committee", "committeeId", receivedCommitteeInfo.Id)
	}
	printNodeWork(t, nodeWork, "startSend...")
	return cryNodeInfo
}

func StopNodeWork(t *testing.T) {
	nodeWork := agent.getCurrentNodeWork()
	printNodeWork(t, nodeWork, "stopSend...")
	//clear nodeWork
	if nodeWork.isCommitteeMember {
		nodeWork.ticker.Stop() //stop ticker send nodeInfo
	}
	//clear nodeWork
	nodeWork.loadNodeWork(new(types.CommitteeInfo), false)
}

func printNodeWork(t *testing.T, nodeWork *nodeInfoWork, str string) {
	t.Log(str, " tag=", nodeWork.tag, ", isMember=", nodeWork.isCommitteeMember, ", isCurrent=", nodeWork.isCurrent,
		", nodeWork1=", agent.nodeInfoWorks[0].isCurrent, ", nodeWork2=", agent.nodeInfoWorks[1].isCurrent,
		", committeeId=", nodeWork.committeeInfo.Id, ", committeeInfoMembers=", len(nodeWork.committeeInfo.Members))
}
