package txcache

import logger "github.com/TerraDharitri/drt-go-chain-logger"

var log = logger.GetOrCreate("txcache/main")
var logAdd = logger.GetOrCreate("txcache/add")
var logRemove = logger.GetOrCreate("txcache/remove")
var logSelect = logger.GetOrCreate("txcache/select")
var logDiagnoseTransactions = logger.GetOrCreate("txcache/diagnose/transactions")
