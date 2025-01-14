package factory

import (
	"github.com/TerraDharitri/drt-go-chain-storage/common"
	"github.com/TerraDharitri/drt-go-chain-storage/leveldb"
	"github.com/TerraDharitri/drt-go-chain-storage/memorydb"
	"github.com/TerraDharitri/drt-go-chain-storage/types"
)

// ArgDB is a structure that is used to create a new storage.Persister implementation
type ArgDB struct {
	DBType            common.DBType
	Path              string
	BatchDelaySeconds int
	MaxBatchSize      int
	MaxOpenFiles      int
}

// NewDB creates a new database from database config
func NewDB(argDB ArgDB) (types.Persister, error) {
	switch argDB.DBType {
	case common.LvlDB:
		return leveldb.NewDB(argDB.Path, argDB.BatchDelaySeconds, argDB.MaxBatchSize, argDB.MaxOpenFiles)
	case common.LvlDBSerial:
		return leveldb.NewSerialDB(argDB.Path, argDB.BatchDelaySeconds, argDB.MaxBatchSize, argDB.MaxOpenFiles)
	case common.MemoryDB:
		return memorydb.New(), nil
	default:
		return nil, common.ErrNotSupportedDBType
	}
}
