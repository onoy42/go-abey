package fastdownloader

import (
	"errors"
	"fmt"
	"github.com/abeychain/go-abey/common"
	"github.com/abeychain/go-abey/crypto"
	"github.com/abeychain/go-abey/core"
	"github.com/abeychain/go-abey/core/types"
	dtypes "github.com/abeychain/go-abey/abey/types"
	"github.com/abeychain/go-abey/abeydb"
	"github.com/abeychain/go-abey/event"
	"github.com/abeychain/go-abey/trie"
	"math/big"
	"sync"
	"time"
)

// downloadTester is a test simulator for mocking out local block chain.
type DownloadTester struct {
	downloader *Downloader

	genesis *types.Block     // Genesis blocks used by the tester and peers
	stateDb abeydb.Database // Database used by the tester for syncing from peers
	peerDb  abeydb.Database // Database of the peers containing all data

	ownHashes   []common.Hash                  // Hash chain belonging to the tester
	ownHeaders  map[common.Hash]*types.Header  // Headers belonging to the tester
	ownBlocks   map[common.Hash]*types.Block   // Blocks belonging to the tester
	ownReceipts map[common.Hash]types.Receipts // Receipts belonging to the tester
	ownChainNum map[common.Hash]*big.Int       // Total difficulties of the blocks in the local chain

	peerHashes    map[string][]common.Hash                  // Hash chain belonging to different test peers
	peerHeaders   map[string]map[common.Hash]*types.Header  // Headers belonging to different test peers
	peerBlocks    map[string]map[common.Hash]*types.Block   // Blocks belonging to different test peers
	peerReceipts  map[string]map[common.Hash]types.Receipts // Receipts belonging to different test peers
	peerChainNums map[string]map[common.Hash]*big.Int       // Total difficulties of the blocks in the peer chains

	peerMissingStates map[string]map[common.Hash]bool // State entries that fast sync should not return

	lock sync.RWMutex
}

var (
	testKey, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	testAddress = crypto.PubkeyToAddress(testKey.PublicKey)
)

// newTester creates a new downloader test mocker.
func NewTester(testdb *abeydb.MemDatabase,stateDb abeydb.Database) *DownloadTester {

	genesis := core.GenesisFastBlockForTesting(testdb, testAddress, big.NewInt(1000000000))

	tester := &DownloadTester{
		genesis:           genesis,
		peerDb:            testdb,
		ownHashes:         []common.Hash{genesis.Hash()},
		ownHeaders:        map[common.Hash]*types.Header{genesis.Hash(): genesis.Header()},
		ownBlocks:         map[common.Hash]*types.Block{genesis.Hash(): genesis},
		ownReceipts:       map[common.Hash]types.Receipts{genesis.Hash(): nil},
		ownChainNum:       map[common.Hash]*big.Int{genesis.Hash(): genesis.Number()},
		peerHashes:        make(map[string][]common.Hash),
		peerHeaders:       make(map[string]map[common.Hash]*types.Header),
		peerBlocks:        make(map[string]map[common.Hash]*types.Block),
		peerReceipts:      make(map[string]map[common.Hash]types.Receipts),
		peerChainNums:     make(map[string]map[common.Hash]*big.Int),
		peerMissingStates: make(map[string]map[common.Hash]bool),
	}
	tester.stateDb = stateDb
	tester.stateDb.Put(genesis.Root().Bytes(), []byte{0x00})
	tester.downloader = New(FullSync, tester.stateDb, new(event.TypeMux), tester, nil, tester.dropPeer)

	return tester
}

func (dl *DownloadTester) GetGenesis() *types.Block {
	return dl.genesis
}

type FloodingTestPeer struct {
	peer   dtypes.Peer
	tester *DownloadTester
	pend   sync.WaitGroup
}

type DownloadTesterPeer struct {
	dl    *DownloadTester
	id    string
	delay time.Duration
	lock  sync.RWMutex
}

// terminate aborts any operations on the embedded downloader and releases all
// held resources.
func (dl *DownloadTester) Terminate() {
	dl.downloader.Terminate()
}

func (dl *DownloadTester) GetDownloader() *Downloader {
	return dl.downloader
}

// HasFastBlock checks if a block is present in the testers canonical chain.
func (dl *DownloadTester) HasFastBlock(hash common.Hash, number uint64) bool {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	_, ok := dl.ownReceipts[hash]
	return ok
}

// HasHeader checks if a header is present in the testers canonical chain.
func (dl *DownloadTester) HasHeader(hash common.Hash, number uint64) bool {
	return dl.GetHeaderByHash(hash) != nil
}

// HasBlock checks if a block is present in the testers canonical chain.
func (dl *DownloadTester) HasBlock(hash common.Hash, number uint64) bool {
	return dl.GetBlockByHash(hash) != nil
}

// GetHeader retrieves a header from the testers canonical chain.
func (dl *DownloadTester) GetHeaderByHash(hash common.Hash) *types.Header {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	return dl.ownHeaders[hash]
}

// GetBlock retrieves a block from the testers canonical chain.
func (dl *DownloadTester) GetBlockByHash(hash common.Hash) *types.Block {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	return dl.ownBlocks[hash]
}

// CurrentHeader retrieves the current head header from the canonical chain.
func (dl *DownloadTester) CurrentHeader() *types.Header {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	for i := len(dl.ownHashes) - 1; i >= 0; i-- {
		if header := dl.ownHeaders[dl.ownHashes[i]]; header != nil {
			return header
		}
	}
	return dl.genesis.Header()
}

// CurrentBlock retrieves the current head block from the canonical chain.
func (dl *DownloadTester) CurrentBlock() *types.Block {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	for i := len(dl.ownHashes) - 1; i >= 0; i-- {
		if block := dl.ownBlocks[dl.ownHashes[i]]; block != nil {
			if _, err := dl.stateDb.Get(block.Root().Bytes()); err == nil {
				return block
			}
		}
	}
	return dl.genesis
}

// CurrentFastBlock retrieves the current head fast-sync block from the canonical chain.
func (dl *DownloadTester) CurrentFastBlock() *types.Block {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	for i := len(dl.ownHashes) - 1; i >= 0; i-- {
		if block := dl.ownBlocks[dl.ownHashes[i]]; block != nil {
			return block
		}
	}
	return dl.genesis
}

// FastSyncCommitHead manually sets the head block to a given hash.
func (dl *DownloadTester) FastSyncCommitHead(hash common.Hash) error {
	// For now only check that the state trie is correct
	if block := dl.GetBlockByHash(hash); block != nil {
		_, err := trie.NewSecure(block.Root(), trie.NewDatabase(dl.stateDb), 0)
		return err
	}
	return fmt.Errorf("non existent block: %x", hash[:4])
}

// GetTd retrieves the block's total difficulty from the canonical chain.
func (dl *DownloadTester) GetTd(hash common.Hash, number uint64) *big.Int {
	dl.lock.RLock()
	defer dl.lock.RUnlock()

	return dl.ownChainNum[hash]
}

// InsertHeaderChain injects a new batch of headers into the simulated chain.
func (dl *DownloadTester) InsertHeaderChain(headers []*types.Header, checkFreq int) (int, error) {
	dl.lock.Lock()
	defer dl.lock.Unlock()

	// Do a quick check, as the blockchain.InsertHeaderChain doesn't insert anything in case of errors
	if _, ok := dl.ownHeaders[headers[0].ParentHash]; !ok {
		return 0, errors.New("unknown parent")
	}
	for i := 1; i < len(headers); i++ {
		if headers[i].ParentHash != headers[i-1].Hash() {
			return i, errors.New("unknown parent")
		}
	}
	// Do a full insert if pre-checks passed
	for i, header := range headers {
		if _, ok := dl.ownHeaders[header.Hash()]; ok {
			continue
		}
		if _, ok := dl.ownHeaders[header.ParentHash]; !ok {
			return i, errors.New("unknown parent")
		}
		dl.ownHashes = append(dl.ownHashes, header.Hash())
		dl.ownHeaders[header.Hash()] = header
		dl.ownChainNum[header.Hash()] = header.Number
	}
	return len(headers), nil
}

// InsertChain injects a new batch of blocks into the simulated chain.
func (dl *DownloadTester) InsertChain(blocks types.Blocks) (int, error) {
	dl.lock.Lock()
	defer dl.lock.Unlock()

	for i, block := range blocks {
		if parent, ok := dl.ownBlocks[block.ParentHash()]; !ok {
			return i, errors.New("unknown parent")
		} else if _, err := dl.stateDb.Get(parent.Root().Bytes()); err != nil {
			return i, fmt.Errorf("unknown parent state %x: %v", parent.Root(), err)
		}
		if _, ok := dl.ownHeaders[block.Hash()]; !ok {
			dl.ownHashes = append(dl.ownHashes, block.Hash())
			dl.ownHeaders[block.Hash()] = block.Header()
		}
		dl.ownBlocks[block.Hash()] = block
		dl.stateDb.Put(block.Root().Bytes(), []byte{0x00})
		dl.ownChainNum[block.Hash()] = block.Number()
	}
	return len(blocks), nil
}

// InsertReceiptChain injects a new batch of receipts into the simulated chain.
func (dl *DownloadTester) InsertReceiptChain(blocks types.Blocks, receipts []types.Receipts) (int, error) {
	dl.lock.Lock()
	defer dl.lock.Unlock()

	for i := 0; i < len(blocks) && i < len(receipts); i++ {
		if _, ok := dl.ownHeaders[blocks[i].Hash()]; !ok {
			return i, errors.New("unknown owner")
		}
		if _, ok := dl.ownBlocks[blocks[i].ParentHash()]; !ok {
			return i, errors.New("unknown parent")
		}
		dl.ownBlocks[blocks[i].Hash()] = blocks[i]
		dl.ownReceipts[blocks[i].Hash()] = receipts[i]
	}
	return len(blocks), nil
}

// Rollback removes some recently added elements from the chain.
func (dl *DownloadTester) Rollback(hashes []common.Hash) {
	dl.lock.Lock()
	defer dl.lock.Unlock()

	for i := len(hashes) - 1; i >= 0; i-- {
		if dl.ownHashes[len(dl.ownHashes)-1] == hashes[i] {
			dl.ownHashes = dl.ownHashes[:len(dl.ownHashes)-1]
		}
		delete(dl.ownChainNum, hashes[i])
		delete(dl.ownHeaders, hashes[i])
		delete(dl.ownReceipts, hashes[i])
		delete(dl.ownBlocks, hashes[i])
	}
}

func (dl *DownloadTester) GetBlockNumber() uint64 {

	if dl.CurrentFastBlock().NumberU64() > dl.CurrentBlock().NumberU64() {
		return dl.CurrentFastBlock().NumberU64()
	}
	return dl.CurrentBlock().NumberU64()

}

// newPeer registers a new block download source into the downloader.
func (dl *DownloadTester) NewPeer(id string, version int, hashes []common.Hash, headers map[common.Hash]*types.Header, blocks map[common.Hash]*types.Block, receipts map[common.Hash]types.Receipts) error {
	return dl.NewSlowPeer(id, version, hashes, headers, blocks, receipts, 0)
}

// newSlowPeer registers a new block download source into the downloader, with a
// specific delay time on processing the network packets sent to it, simulating
// potentially slow network IO.
func (dl *DownloadTester) NewSlowPeer(id string, version int, hashes []common.Hash, headers map[common.Hash]*types.Header, blocks map[common.Hash]*types.Block, receipts map[common.Hash]types.Receipts, delay time.Duration) error {
	dl.lock.Lock()
	defer dl.lock.Unlock()

	var err = dl.downloader.RegisterPeer(id, version, &DownloadTesterPeer{dl: dl, id: id, delay: delay})
	if err == nil {
		// Assign the owned hashes, headers and blocks to the peer (deep copy)
		dl.peerHashes[id] = make([]common.Hash, len(hashes))
		copy(dl.peerHashes[id], hashes)

		dl.peerHeaders[id] = make(map[common.Hash]*types.Header)
		dl.peerBlocks[id] = make(map[common.Hash]*types.Block)
		dl.peerReceipts[id] = make(map[common.Hash]types.Receipts)
		dl.peerChainNums[id] = make(map[common.Hash]*big.Int)
		dl.peerMissingStates[id] = make(map[common.Hash]bool)

		genesis := hashes[len(hashes)-1]
		if header := headers[genesis]; header != nil {
			dl.peerHeaders[id][genesis] = header
			dl.peerChainNums[id][genesis] = header.Number
		}
		if block := blocks[genesis]; block != nil {
			dl.peerBlocks[id][genesis] = block
			dl.peerChainNums[id][genesis] = block.Number()
		}

		for i := len(hashes) - 2; i >= 0; i-- {
			hash := hashes[i]

			if header, ok := headers[hash]; ok {
				dl.peerHeaders[id][hash] = header
				if _, ok := dl.peerHeaders[id][header.ParentHash]; ok {
					dl.peerChainNums[id][hash] = header.Number
				}
			}
			if block, ok := blocks[hash]; ok {
				dl.peerBlocks[id][hash] = block
				if _, ok := dl.peerBlocks[id][block.ParentHash()]; ok {
					dl.peerChainNums[id][hash] = block.Number()
				}
			}
			if receipt, ok := receipts[hash]; ok {
				dl.peerReceipts[id][hash] = receipt
			}
		}
	}
	return err
}

// dropPeer simulates a hard peer removal from the connection pool.
func (dl *DownloadTester) dropPeer(id string, call uint32) {
	dl.lock.Lock()
	defer dl.lock.Unlock()

	delete(dl.peerHashes, id)
	delete(dl.peerHeaders, id)
	delete(dl.peerBlocks, id)
	delete(dl.peerChainNums, id)

	dl.downloader.UnregisterPeer(id)
}

// setDelay is a thread safe setter for the network delay value.
func (dlp *DownloadTesterPeer) setDelay(delay time.Duration) {
	dlp.lock.Lock()
	defer dlp.lock.Unlock()

	dlp.delay = delay
}

// waitDelay is a thread safe way to sleep for the configured time.
func (dlp *DownloadTesterPeer) waitDelay() {
	dlp.lock.RLock()
	delay := dlp.delay
	dlp.lock.RUnlock()

	time.Sleep(delay)
}

// Head constructs a function to retrieve a peer's current head hash
// and total difficulty.
func (dlp *DownloadTesterPeer) Head() (common.Hash, *big.Int) {
	dlp.dl.lock.RLock()
	defer dlp.dl.lock.RUnlock()

	return dlp.dl.peerHashes[dlp.id][0], nil
}

// RequestHeadersByHash constructs a GetBlockHeaders function based on a hashed
// origin; associated with a particular peer in the download tester. The returned
// function can be used to retrieve batches of headers from the particular peer.
func (dlp *DownloadTesterPeer) RequestHeadersByHash(origin common.Hash, amount int, skip int, reverse bool, isFastchain bool) error {
	// Find the canonical number of the hash
	dlp.dl.lock.RLock()
	number := uint64(0)
	for num, hash := range dlp.dl.peerHashes[dlp.id] {
		if hash == origin {
			number = uint64(len(dlp.dl.peerHashes[dlp.id]) - num - 1)
			break
		}
	}
	dlp.dl.lock.RUnlock()

	// Use the absolute header fetcher to satisfy the query
	return dlp.RequestHeadersByNumber(number, amount, skip, reverse, isFastchain)
}

// RequestHeadersByNumber constructs a GetBlockHeaders function based on a numbered
// origin; associated with a particular peer in the download tester. The returned
// function can be used to retrieve batches of headers from the particular peer.
func (dlp *DownloadTesterPeer) RequestHeadersByNumber(origin uint64, amount int, skip int, reverse bool, isFastchain bool) error {
	dlp.waitDelay()

	dlp.dl.lock.RLock()
	defer dlp.dl.lock.RUnlock()

	// Gather the next batch of headers
	hashes := dlp.dl.peerHashes[dlp.id]
	headers := dlp.dl.peerHeaders[dlp.id]
	result := make([]*types.Header, 0, amount)
	for i := 0; i < amount && len(hashes)-int(origin)-1-i*(skip+1) >= 0; i++ {
		if header, ok := headers[hashes[len(hashes)-int(origin)-1-i*(skip+1)]]; ok {
			result = append(result, header)
		}
	}
	// Delay delivery a bit to allow attacks to unfold
	go func() {
		time.Sleep(time.Millisecond)
		dlp.dl.downloader.DeliverHeaders(dlp.id, result, types.DownloaderCall)
	}()
	return nil
}

// RequestBodies constructs a getBlockBodies method associated with a particular
// peer in the download tester. The returned function can be used to retrieve
// batches of block bodies from the particularly requested peer.
func (dlp *DownloadTesterPeer) RequestBodies(hashes []common.Hash, isFastchain bool, call uint32) error {
	dlp.waitDelay()

	dlp.dl.lock.RLock()
	defer dlp.dl.lock.RUnlock()

	blocks := dlp.dl.peerBlocks[dlp.id]

	transactions := make([][]*types.Transaction, 0, len(hashes))
	signs := make([][]*types.PbftSign, 0, len(hashes))
	infos := make([][]*types.CommitteeMember, 0, len(hashes))

	//[]*types.PbftSign

	for _, hash := range hashes {
		if block, ok := blocks[hash]; ok {
			transactions = append(transactions, block.Transactions())
			signs = append(signs, block.Signs())
			infos = append(infos, block.SwitchInfos())
		}
	}
	go dlp.dl.downloader.DeliverBodies(dlp.id, transactions, signs, infos, types.DownloaderCall)

	return nil
}

// RequestReceipts constructs a getReceipts method associated with a particular
// peer in the download tester. The returned function can be used to retrieve
// batches of block receipts from the particularly requested peer.
func (dlp *DownloadTesterPeer) RequestReceipts(hashes []common.Hash, isFastchain bool) error {
	dlp.waitDelay()

	dlp.dl.lock.RLock()
	defer dlp.dl.lock.RUnlock()

	receipts := dlp.dl.peerReceipts[dlp.id]

	results := make([][]*types.Receipt, 0, len(hashes))
	for _, hash := range hashes {
		if receipt, ok := receipts[hash]; ok {
			results = append(results, receipt)
		}
	}
	go dlp.dl.downloader.DeliverReceipts(dlp.id, results)

	return nil
}

// RequestNodeData constructs a getNodeData method associated with a particular
// peer in the download tester. The returned function can be used to retrieve
// batches of node state data from the particularly requested peer.
func (dlp *DownloadTesterPeer) RequestNodeData(hashes []common.Hash, isFastchain bool) error {
	//dlp.waitDelay()
	//
	//dlp.dl.lock.RLock()
	//defer dlp.dl.lock.RUnlock()
	//
	//results := make([][]byte, 0, len(hashes))
	//for _, hash := range hashes {
	//	if data, err := dlp.dl.peerDb.Get(hash.Bytes()); err == nil {
	//		if !dlp.dl.peerMissingStates[dlp.id][hash] {
	//			results = append(results, data)
	//		}
	//	}
	//}
	//go dlp.dl.s.DeliverNodeData(dlp.id, results)

	return nil
}
