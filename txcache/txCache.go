package txcache

import (
	"sync"
	"time"

	"github.com/TerraDharitri/drt-go-chain-core/core"
	"github.com/TerraDharitri/drt-go-chain-core/core/atomic"
	"github.com/TerraDharitri/drt-go-chain-core/core/check"
	"github.com/TerraDharitri/drt-go-chain-storage/monitoring"
	"github.com/TerraDharitri/drt-go-chain-storage/types"
)

var _ types.Cacher = (*TxCache)(nil)

// TxCache represents a cache-like structure (it has a fixed capacity and implements an eviction mechanism) for holding transactions
type TxCache struct {
	name                 string
	txListBySender       *txListBySenderMap
	txByHash             *txByHashMap
	config               ConfigSourceMe
	host                 MempoolHost
	evictionMutex        sync.Mutex
	isEvictionInProgress atomic.Flag
	mutTxOperation       sync.Mutex
}

// NewTxCache creates a new transaction cache
func NewTxCache(config ConfigSourceMe, host MempoolHost) (*TxCache, error) {
	log.Debug("NewTxCache", "config", config.String())
	monitoring.MonitorNewCache(config.Name, uint64(config.NumBytesThreshold))

	err := config.verify()
	if err != nil {
		return nil, err
	}
	if check.IfNil(host) {
		return nil, errNilMempoolHost
	}

	// Note: for simplicity, we use the same "numChunks" for both internal concurrent maps
	numChunks := config.NumChunks
	senderConstraintsObj := config.getSenderConstraints()

	txCache := &TxCache{
		name:           config.Name,
		txListBySender: newTxListBySenderMap(numChunks, senderConstraintsObj),
		txByHash:       newTxByHashMap(numChunks),
		config:         config,
		host:           host,
	}

	return txCache, nil
}

// AddTx adds a transaction in the cache
// Eviction happens if maximum capacity is reached
func (cache *TxCache) AddTx(tx *WrappedTransaction) (ok bool, added bool) {
	if tx == nil || check.IfNil(tx.Tx) {
		return false, false
	}

	logAdd.Trace("TxCache.AddTx", "tx", tx.TxHash, "nonce", tx.Tx.GetNonce(), "sender", tx.Tx.GetSndAddr())

	tx.precomputeFields(cache.host)

	if cache.config.EvictionEnabled {
		_ = cache.doEviction()
	}

	cache.mutTxOperation.Lock()
	addedInByHash := cache.txByHash.addTx(tx)
	addedInBySender, evicted := cache.txListBySender.addTxReturnEvicted(tx)
	cache.mutTxOperation.Unlock()
	if addedInByHash != addedInBySender {
		// This can happen  when two go-routines concur to add the same transaction:
		// - A adds to "txByHash"
		// - B won't add to "txByHash" (duplicate)
		// - B adds to "txListBySender"
		// - A won't add to "txListBySender" (duplicate)
		logAdd.Debug("TxCache.AddTx: slight inconsistency detected:", "tx", tx.TxHash, "sender", tx.Tx.GetSndAddr(), "addedInByHash", addedInByHash, "addedInBySender", addedInBySender)
	}

	if len(evicted) > 0 {
		logRemove.Trace("TxCache.AddTx with eviction", "sender", tx.Tx.GetSndAddr(), "num evicted txs", len(evicted))
		cache.txByHash.RemoveTxsBulk(evicted)
	}

	// The return value "added" is true even if transaction added, but then removed due to limits be sender.
	// This it to ensure that onAdded() notification is triggered.
	return true, addedInByHash || addedInBySender
}

// GetByTxHash gets the transaction by hash
func (cache *TxCache) GetByTxHash(txHash []byte) (*WrappedTransaction, bool) {
	tx, ok := cache.txByHash.getTx(string(txHash))
	return tx, ok
}

// SelectTransactions selects the best transactions to be included in the next miniblock.
// It returns up to "maxNum" transactions, with total gas <= "gasRequested".
func (cache *TxCache) SelectTransactions(session SelectionSession, gasRequested uint64, maxNum int, selectionLoopMaximumDuration time.Duration) ([]*WrappedTransaction, uint64) {
	if check.IfNil(session) {
		log.Error("TxCache.SelectTransactions", "err", errNilSelectionSession)
		return nil, 0
	}

	stopWatch := core.NewStopWatch()
	stopWatch.Start("selection")

	logSelect.Debug(
		"TxCache.SelectTransactions: begin",
		"num bytes", cache.NumBytes(),
		"num txs", cache.CountTx(),
		"num senders", cache.CountSenders(),
	)

	transactions, accumulatedGas := cache.doSelectTransactions(session, gasRequested, maxNum, selectionLoopMaximumDuration)

	stopWatch.Stop("selection")

	logSelect.Debug(
		"TxCache.SelectTransactions: end",
		"duration", stopWatch.GetMeasurement("selection"),
		"num txs selected", len(transactions),
		"gas", accumulatedGas,
	)

	go cache.diagnoseCounters()
	go displaySelectionOutcome(logSelect, "selection", transactions)

	return transactions, accumulatedGas
}

func (cache *TxCache) getSenders() []*txListForSender {
	return cache.txListBySender.getSenders()
}

// RemoveTxByHash removes transactions with nonces lower or equal to the given transaction's nonce
func (cache *TxCache) RemoveTxByHash(txHash []byte) bool {
	cache.mutTxOperation.Lock()
	defer cache.mutTxOperation.Unlock()

	tx, foundInByHash := cache.txByHash.removeTx(string(txHash))
	if !foundInByHash {
		// Transaction might have been removed in the meantime.
		return false
	}

	evicted := cache.txListBySender.removeTransactionsWithLowerOrEqualNonceReturnHashes(tx)
	if len(evicted) > 0 {
		cache.txByHash.RemoveTxsBulk(evicted)
	}

	logRemove.Trace("TxCache.RemoveTxByHash", "tx", txHash, "len(evicted)", len(evicted))
	return true
}

// NumBytes gets the approximate number of bytes stored in the cache
func (cache *TxCache) NumBytes() int {
	return int(cache.txByHash.numBytes.GetUint64())
}

// CountTx gets the number of transactions in the cache
func (cache *TxCache) CountTx() uint64 {
	return cache.txByHash.counter.GetUint64()
}

// Len is an alias for CountTx
func (cache *TxCache) Len() int {
	return int(cache.CountTx())
}

// SizeInBytesContained returns 0
func (cache *TxCache) SizeInBytesContained() uint64 {
	return 0
}

// CountSenders gets the number of senders in the cache
func (cache *TxCache) CountSenders() uint64 {
	return cache.txListBySender.counter.GetUint64()
}

// ForEachTransaction iterates over the transactions in the cache
func (cache *TxCache) ForEachTransaction(function ForEachTransaction) {
	cache.txByHash.forEach(function)
}

// getAllTransactions returns all transactions in the cache
func (cache *TxCache) getAllTransactions() []*WrappedTransaction {
	transactions := make([]*WrappedTransaction, 0, cache.Len())

	cache.ForEachTransaction(func(_ []byte, tx *WrappedTransaction) {
		transactions = append(transactions, tx)
	})

	return transactions
}

// GetTransactionsPoolForSender returns the list of transaction hashes for the sender
func (cache *TxCache) GetTransactionsPoolForSender(sender string) []*WrappedTransaction {
	listForSender, ok := cache.txListBySender.getListForSender(sender)
	if !ok {
		return nil
	}

	return listForSender.getTxs()
}

// Clear clears the cache
func (cache *TxCache) Clear() {
	cache.mutTxOperation.Lock()
	cache.txListBySender.clear()
	cache.txByHash.clear()
	cache.mutTxOperation.Unlock()
}

// Put is not implemented
func (cache *TxCache) Put(_ []byte, _ interface{}, _ int) (evicted bool) {
	log.Error("TxCache.Put is not implemented")
	return false
}

// Get gets a transaction (unwrapped) by hash
// Implemented for compatibility reasons (see txPoolsCleaner.go).
func (cache *TxCache) Get(key []byte) (value interface{}, ok bool) {
	tx, ok := cache.GetByTxHash(key)
	if ok {
		return tx.Tx, true
	}
	return nil, false
}

// Has checks if a transaction exists
func (cache *TxCache) Has(key []byte) bool {
	_, ok := cache.GetByTxHash(key)
	return ok
}

// Peek gets a transaction (unwrapped) by hash
// Implemented for compatibility reasons (see transactions.go, common.go).
func (cache *TxCache) Peek(key []byte) (value interface{}, ok bool) {
	tx, ok := cache.GetByTxHash(key)
	if ok {
		return tx.Tx, true
	}
	return nil, false
}

// HasOrAdd is not implemented
func (cache *TxCache) HasOrAdd(_ []byte, _ interface{}, _ int) (has, added bool) {
	log.Error("TxCache.HasOrAdd is not implemented")
	return false, false
}

// Remove removes tx by hash
func (cache *TxCache) Remove(key []byte) {
	_ = cache.RemoveTxByHash(key)
}

// Keys returns the tx hashes in the cache
func (cache *TxCache) Keys() [][]byte {
	return cache.txByHash.keys()
}

// MaxSize returns the maximum number of transactions that can be stored in the cache.
// See: https://github.com/TerraDharitri/drt-go-chain/blob/v1.8.4/dataRetriever/txpool/shardedTxPool.go#L55
func (cache *TxCache) MaxSize() int {
	return int(cache.config.CountThreshold)
}

// RegisterHandler is not implemented
func (cache *TxCache) RegisterHandler(func(key []byte, value interface{}), string) {
	log.Error("TxCache.RegisterHandler is not implemented")
}

// UnRegisterHandler is not implemented
func (cache *TxCache) UnRegisterHandler(string) {
	log.Error("TxCache.UnRegisterHandler is not implemented")
}

// ImmunizeTxsAgainstEviction does nothing for this type of cache
func (cache *TxCache) ImmunizeTxsAgainstEviction(_ [][]byte) {
}

// Close does nothing for this cacher implementation
func (cache *TxCache) Close() error {
	return nil
}

// IsInterfaceNil returns true if there is no value under the interface
func (cache *TxCache) IsInterfaceNil() bool {
	return cache == nil
}
