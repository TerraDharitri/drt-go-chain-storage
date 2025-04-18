package leveldb

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TerraDharitri/drt-go-chain-core/core"
	logger "github.com/TerraDharitri/drt-go-chain-logger"
	"github.com/TerraDharitri/drt-go-chain-storage/common"
	"github.com/TerraDharitri/drt-go-chain-storage/types"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

var _ types.Persister = (*DB)(nil)

// read + write + execute for owner only
const rwxOwner = 0700
const mkdirAllFunction = "mkdirAll"
const openLevelDBFunction = "openLevelDB"

var log = logger.GetOrCreate("storage/leveldb")

// DB holds a pointer to the leveldb database and the path to where it is stored.
type DB struct {
	*baseLevelDb
	maxBatchSize      int
	batchDelaySeconds int
	sizeBatch         int
	batch             types.Batcher
	mutBatch          sync.RWMutex
	cancel            context.CancelFunc
}

// NewDB is a constructor for the leveldb persister
// It creates the files in the location given as parameter
func NewDB(path string, batchDelaySeconds int, maxBatchSize int, maxOpenFiles int) (s *DB, err error) {
	constructorName := "NewDB"

	sw := core.NewStopWatch()
	sw.Start(constructorName)

	sw.Start(mkdirAllFunction)
	err = os.MkdirAll(path, rwxOwner)
	if err != nil {
		return nil, err
	}
	sw.Stop(mkdirAllFunction)

	if maxOpenFiles < 1 {
		return nil, common.ErrInvalidNumOpenFiles
	}

	options := &opt.Options{
		// disable internal cache
		BlockCacheCapacity:     -1,
		OpenFilesCacheCapacity: maxOpenFiles,
	}

	sw.Start(openLevelDBFunction)
	db, err := openLevelDB(path, options)
	if err != nil {
		return nil, fmt.Errorf("%w for path %s", err, path)
	}
	sw.Stop(openLevelDBFunction)

	bldb := &baseLevelDb{
		db:   db,
		path: path,
	}

	ctx, cancel := context.WithCancel(context.Background())
	dbStore := &DB{
		baseLevelDb:       bldb,
		maxBatchSize:      maxBatchSize,
		batchDelaySeconds: batchDelaySeconds,
		sizeBatch:         0,
		cancel:            cancel,
	}

	dbStore.batch = dbStore.createBatch()

	go dbStore.batchTimeoutHandle(ctx)

	runtime.SetFinalizer(dbStore, func(db *DB) {
		_ = db.Close()
	})

	crtCounter := atomic.AddUint32(&loggingDBCounter, 1)
	sw.Stop(constructorName)

	logArguments := []interface{}{"path", path, "created pointer", fmt.Sprintf("%p", bldb.db), "global db counter", crtCounter}
	logArguments = append(logArguments, sw.GetMeasurements()...)
	log.Debug("opened level db persister", logArguments...)

	return dbStore, nil
}

func (s *DB) batchTimeoutHandle(ctx context.Context) {
	interval := time.Duration(s.batchDelaySeconds) * time.Second
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		timer.Reset(interval)

		select {
		case <-timer.C:
			s.mutBatch.Lock()
			err := s.putBatch(s.batch)
			if err != nil {
				log.Warn("leveldb putBatch", "error", err.Error())
				s.mutBatch.Unlock()
				continue
			}

			s.batch.Reset()
			s.sizeBatch = 0
			s.mutBatch.Unlock()
		case <-ctx.Done():
			log.Debug("closing the timed batch handler", "path", s.path)
			return
		}
	}
}

func (s *DB) updateBatchWithIncrement() error {
	s.mutBatch.Lock()
	defer s.mutBatch.Unlock()

	s.sizeBatch++
	if s.sizeBatch < s.maxBatchSize {
		return nil
	}

	err := s.putBatch(s.batch)
	if err != nil {
		log.Warn("leveldb putBatch", "error", err.Error())
		return err
	}

	s.batch.Reset()
	s.sizeBatch = 0

	return nil
}

// Put adds the value to the (key, val) storage medium
func (s *DB) Put(key, val []byte) error {
	s.mutBatch.RLock()
	err := s.batch.Put(key, val)
	s.mutBatch.RUnlock()

	if err != nil {
		return err
	}

	return s.updateBatchWithIncrement()
}

// Get returns the value associated to the key
func (s *DB) Get(key []byte) ([]byte, error) {
	db := s.getDbPointer()
	if db == nil {
		return nil, common.ErrDBIsClosed
	}

	if s.batch.IsRemoved(key) {
		return nil, common.ErrKeyNotFound
	}

	data := s.batch.Get(key)
	if data != nil {
		return data, nil
	}

	data, err := db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		return nil, common.ErrKeyNotFound
	}
	if err != nil {
		return nil, err
	}

	return data, nil
}

// Has returns nil if the given key is present in the persistence medium
func (s *DB) Has(key []byte) error {
	db := s.getDbPointer()
	if db == nil {
		return common.ErrDBIsClosed
	}

	if s.batch.IsRemoved(key) {
		return common.ErrKeyNotFound
	}

	data := s.batch.Get(key)
	if data != nil {
		return nil
	}

	has, err := db.Has(key, nil)
	if err != nil {
		return err
	}

	if has {
		return nil
	}

	return common.ErrKeyNotFound
}

// CreateBatch returns a batcher to be used for batch writing data to the database
func (s *DB) createBatch() types.Batcher {
	return NewBatch()
}

// putBatch writes the Batch data into the database
func (s *DB) putBatch(b types.Batcher) error {
	dbBatch, ok := b.(*batch)
	if !ok {
		return common.ErrInvalidBatch
	}

	wopt := &opt.WriteOptions{
		Sync: true,
	}

	db := s.getDbPointer()
	if db == nil {
		return common.ErrDBIsClosed
	}

	return db.Write(dbBatch.batch, wopt)
}

// Close closes the files/resources associated to the storage medium
func (s *DB) Close() error {
	s.mutBatch.Lock()
	_ = s.putBatch(s.batch)
	s.sizeBatch = 0
	s.mutBatch.Unlock()

	s.cancel()
	db := s.makeDbPointerNilReturningLast()
	if db != nil {
		return db.Close()
	}

	return nil
}

// Remove removes the data associated to the given key
func (s *DB) Remove(key []byte) error {
	s.mutBatch.Lock()
	_ = s.batch.Delete(key)
	s.mutBatch.Unlock()

	return s.updateBatchWithIncrement()
}

// Destroy removes the storage medium stored data
func (s *DB) Destroy() error {
	s.mutBatch.Lock()
	s.batch.Reset()
	s.sizeBatch = 0
	s.mutBatch.Unlock()

	s.cancel()
	db := s.makeDbPointerNilReturningLast()
	if db != nil {
		err := db.Close()
		if err != nil {
			return err
		}
	}

	return os.RemoveAll(s.path)
}

// DestroyClosed removes the already closed storage medium stored data
func (s *DB) DestroyClosed() error {
	return os.RemoveAll(s.path)
}

// IsInterfaceNil returns true if there is no value under the interface
func (s *DB) IsInterfaceNil() bool {
	return s == nil
}
