package storageCacherAdapter

import (
	"math"
	"sync"

	"github.com/TerraDharitri/drt-go-chain-core/core/check"
	"github.com/TerraDharitri/drt-go-chain-core/marshal"
	logger "github.com/TerraDharitri/drt-go-chain-logger"
	"github.com/TerraDharitri/drt-go-chain-storage/common"
	"github.com/TerraDharitri/drt-go-chain-storage/types"
)

var log = logger.GetOrCreate("storageCacherAdapter")

type storageCacherAdapter struct {
	cacher     types.AdaptedSizedLRUCache
	db         types.Persister
	lock       sync.RWMutex
	dbIsClosed bool

	storedDataFactory  types.StoredDataFactory
	marshalizer        marshal.Marshalizer
	numValuesInStorage int
}

// NewStorageCacherAdapter creates a new storageCacherAdapter
func NewStorageCacherAdapter(
	cacher types.AdaptedSizedLRUCache,
	db types.Persister,
	storedDataFactory types.StoredDataFactory,
	marshalizer marshal.Marshalizer,
) (*storageCacherAdapter, error) {
	if check.IfNil(cacher) {
		return nil, common.ErrNilCacher
	}
	if check.IfNil(db) {
		return nil, common.ErrNilPersister
	}
	if check.IfNil(marshalizer) {
		return nil, common.ErrNilMarshalizer
	}
	if check.IfNil(storedDataFactory) {
		return nil, common.ErrNilStoredDataFactory
	}

	return &storageCacherAdapter{
		cacher:             cacher,
		db:                 db,
		lock:               sync.RWMutex{},
		storedDataFactory:  storedDataFactory,
		marshalizer:        marshalizer,
		numValuesInStorage: 0,
	}, nil
}

// Clear clears the cache
func (c *storageCacherAdapter) Clear() {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.cacher.Purge()
}

// Put adds the given value in the cacher. If the cacher is full, the evicted values will be persisted to the db
func (c *storageCacherAdapter) Put(key []byte, value interface{}, sizeInBytes int) bool {
	c.lock.Lock()
	defer c.lock.Unlock()

	evictedValues := c.cacher.AddSizedAndReturnEvicted(string(key), value, int64(sizeInBytes))

	if c.dbIsClosed {
		return len(evictedValues) != 0
	}

	for evictedKey, evictedVal := range evictedValues {
		evictedKeyStr, ok := evictedKey.(string)
		if !ok {
			log.Warn("invalid key type", "key", evictedKey)
			continue
		}

		evictedValBytes := getBytes(evictedVal, c.marshalizer)
		if len(evictedValBytes) == 0 {
			continue
		}

		err := c.db.Put([]byte(evictedKeyStr), evictedValBytes)
		if err != nil {
			log.Error("could not save to db", "error", err)
			continue
		}

		c.numValuesInStorage++
	}

	return len(evictedValues) != 0
}

func getBytes(data interface{}, marshalizer marshal.Marshalizer) []byte {
	evictedVal, ok := data.(types.SerializedStoredData)
	if ok {
		return evictedVal.GetSerialized()
	}

	evictedValBytes, err := marshalizer.Marshal(data)
	if err != nil {
		log.Error("could not marshal value", "error", err)
		return nil
	}

	return evictedValBytes
}

// Get returns the value at the given key
func (c *storageCacherAdapter) Get(key []byte) (interface{}, bool) {
	c.lock.RLock()
	defer c.lock.RUnlock()

	val, ok := c.cacher.Get(string(key))
	if ok {
		return val, true
	}

	if c.dbIsClosed {
		return nil, false
	}

	valBytes, err := c.db.Get(key)
	if err != nil {
		return nil, false
	}

	storedData, err := c.getData(valBytes)
	if err != nil {
		log.Error("could not get data", "error", err)
		return nil, false
	}

	return storedData, true
}

func (c *storageCacherAdapter) getData(serializedData []byte) (interface{}, error) {
	storedData := c.storedDataFactory.CreateEmpty()
	data, ok := storedData.(types.SerializedStoredData)
	if ok {
		data.SetSerialized(serializedData)
		return data, nil
	}

	err := c.marshalizer.Unmarshal(storedData, serializedData)
	if err != nil {
		return nil, err
	}

	return storedData, nil
}

// Has checks if the given key is present in the storageUnit
func (c *storageCacherAdapter) Has(key []byte) bool {
	c.lock.RLock()
	defer c.lock.RUnlock()

	isPresent := c.cacher.Contains(string(key))
	if isPresent {
		return true
	}

	if c.dbIsClosed {
		return false
	}

	err := c.db.Has(key)
	return err == nil
}

// Peek returns the value at the given key by searching only in cacher
func (c *storageCacherAdapter) Peek(key []byte) (interface{}, bool) {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.cacher.Peek(string(key))
}

// HasOrAdd checks if the value exists and adds it otherwise
func (c *storageCacherAdapter) HasOrAdd(key []byte, value interface{}, sizeInBytes int) (bool, bool) {
	ok := c.Has(key)
	if ok {
		return true, false
	}

	added := c.Put(key, value, sizeInBytes)

	return false, added
}

// Remove deletes the given key from the storageUnit
func (c *storageCacherAdapter) Remove(key []byte) {
	c.lock.Lock()
	defer c.lock.Unlock()

	removed := c.cacher.Remove(string(key))
	if removed || c.dbIsClosed {
		return
	}

	err := c.db.Remove(key)
	if err == nil {
		c.numValuesInStorage--
	}
}

// Keys returns all the keys present in the storageUnit
func (c *storageCacherAdapter) Keys() [][]byte {
	c.lock.RLock()
	defer c.lock.RUnlock()

	cacherKeys := c.cacher.Keys()
	storedKeys := make([][]byte, 0, len(cacherKeys))
	for i := range cacherKeys {
		key, ok := cacherKeys[i].(string)
		if !ok {
			continue
		}

		storedKeys = append(storedKeys, []byte(key))
	}

	if c.dbIsClosed {
		return storedKeys
	}

	getKeys := func(key []byte, _ []byte) bool {
		storedKeys = append(storedKeys, key)
		return true
	}

	c.db.RangeKeys(getKeys)
	return storedKeys
}

// Len returns the number of elements from the storageUnit
func (c *storageCacherAdapter) Len() int {
	c.lock.RLock()
	defer c.lock.RUnlock()

	cacheLen := c.cacher.Len()
	return cacheLen + c.numValuesInStorage
}

// SizeInBytesContained returns the number of bytes stored in the cache
func (c *storageCacherAdapter) SizeInBytesContained() uint64 {
	c.lock.RLock()
	defer c.lock.RUnlock()

	return c.cacher.SizeInBytesContained()
}

// MaxSize returns MaxInt64
func (c *storageCacherAdapter) MaxSize() int {
	return math.MaxInt64
}

// RegisterHandler does nothing
func (c *storageCacherAdapter) RegisterHandler(_ func(_ []byte, _ interface{}), _ string) {
}

// UnRegisterHandler does nothing
func (c *storageCacherAdapter) UnRegisterHandler(_ string) {
}

// Close closes the underlying db
func (c *storageCacherAdapter) Close() error {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.dbIsClosed = true
	c.numValuesInStorage = 0
	return c.db.Close()
}

// IsInterfaceNil returns true if there is no value under the interface
func (c *storageCacherAdapter) IsInterfaceNil() bool {
	return c == nil
}
