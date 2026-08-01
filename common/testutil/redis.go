// Package testutil — Redis helpers.
//
// one-api keeps a single global redis.Client under common.RDB that
// production code reaches for unconditionally; the only runtime
// decision is common.RedisEnabled. Tests therefore have two
// hermetic options:
//
//  1. DisableRedis — flip the feature flag off and clear RDB for
//     the duration of the test. Production code already branches on
//     the flag and falls back to the database, so this is the
//     cheapest way to make tests deterministic.
//  2. MiniredisAvailable / NewMiniredisClient — when the miniredis
//     dependency is wired in (see Top Of File note), return a real
//     in-process Redis client that satisfies full Redis semantics.
//     Today the function is a no-op placeholder so the project does
//     not need to vendor github.com/alicebob/miniredis/v2.
//
// Use DisableRedis by default. Reach for the miniredis path only
// when the code under test genuinely exercises Redis-only logic that
// a database fallback would mask.

package testutil

import (
	"testing"

	"github.com/go-redis/redis/v8"
	"github.com/songquanpeng/one-api/common"
)

// DisableRedis flips common.RedisEnabled to false and clears the global
// RDB pointer for the duration of the test, restoring both on
// t.Cleanup. Production code in one-api branches on common.RedisEnabled
// and falls back to the database, so disabling Redis is the simplest
// way to make tests hermetic without pulling in a miniredis dependency.
func DisableRedis(t *testing.T) {
	t.Helper()
	prev := common.RedisEnabled
	prevRDB := common.RDB
	common.RedisEnabled = false
	common.RDB = nil
	t.Cleanup(func() {
		common.RedisEnabled = prev
		common.RDB = prevRDB
	})
}

// MiniredisAvailable reports whether the package's optional miniredis
// stand-in is bundled. It always returns false today; see the package
// comment for the rationale and the steps required to flip this to a
// real implementation.
func MiniredisAvailable() bool { return false }

// NewMiniredisClient is the opt-in miniredis stand-in. It currently
// returns (nil, false) so callers can degrade gracefully when the
// dependency is not vendored. When MiniredisAvailable returns true a
// future revision of this helper will wire up
// github.com/alicebob/miniredis/v2 as a real in-process server.
//
// Prefer DisableRedis unless you specifically need true Redis
// semantics (ZADD, Lua scripts, blocking commands, etc.).
func NewMiniredisClient(t *testing.T) (*redis.Client, bool) {
	t.Helper()
	t.Log("testutil: NewMiniredisClient is a no-op; miniredis is not bundled. Use DisableRedis for the common case.")
	return nil, false
}
