package common

import (
	"github.com/songquanpeng/one-api/common/env"
)

var UsingSQLite = false
var UsingPostgreSQL = false
var UsingMySQL = false
// UsingRQLite is set when SQL_DSN (or LOG_SQL_DSN) uses the rqlite:// scheme.
// RQLite is dialect-wise SQLite, so every "SQLite branch" in model/ also
// checks `|| common.UsingRQLite` (see ADR-0004 D3).
var UsingRQLite = false

var SQLitePath = "one-api.db"
var SQLiteBusyTimeout = env.Int("SQLITE_BUSY_TIMEOUT", 3000)
