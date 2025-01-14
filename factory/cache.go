package factory

import (
	"fmt"

	"github.com/TerraDharitri/drt-go-chain-storage/common"
	"github.com/TerraDharitri/drt-go-chain-storage/fifocache"
	"github.com/TerraDharitri/drt-go-chain-storage/lrucache"
	"github.com/TerraDharitri/drt-go-chain-storage/monitoring"
	"github.com/TerraDharitri/drt-go-chain-storage/types"
)

const minimumSizeForLRUCache = 1024

// NewCache creates a new cache from a cache config
func NewCache(config common.CacheConfig) (types.Cacher, error) {
	monitoring.MonitorNewCache(config.Name, config.SizeInBytes)

	cacheType := config.Type
	capacity := config.Capacity
	shards := config.Shards
	sizeInBytes := config.SizeInBytes

	switch cacheType {
	case common.LRUCache:
		if sizeInBytes != 0 {
			return nil, common.ErrLRUCacheWithProvidedSize
		}

		return lrucache.NewCache(int(capacity))
	case common.SizeLRUCache:
		if sizeInBytes < minimumSizeForLRUCache {
			return nil, fmt.Errorf("%w, provided %d, minimum %d",
				common.ErrLRUCacheInvalidSize,
				sizeInBytes,
				minimumSizeForLRUCache,
			)
		}

		return lrucache.NewCacheWithSizeInBytes(int(capacity), int64(sizeInBytes))
	case common.FIFOShardedCache:
		return fifocache.NewShardedCache(int(capacity), int(shards))
	default:
		return nil, common.ErrNotSupportedCacheType
	}
}
