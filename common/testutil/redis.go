package testutil

import (
	"testing"

	"github.com/go-redis/redis/v8"
	"github.com/songquanpeng/one-api/common"
)

// DisableRedis flips common.RedisEnabled to false and clears the global
// RDB pointer for the duration of the test. Most one-api code paths
// already branch on common.RedisEnabled and fall back to the database,
// so disabling Redis is the simplest way to make tests hermetic without
// pulling in a miniredis dependency.
//
// If you need a fully featured Redis stand-in, see NewMiniredisClient
// below — it requires the github.com/alicebob/miniredis/v2 package which
// must be added to go.mod before use.
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

// NewMiniredisClient is a placeholder for an optional in-process Redis
// stand-in. It returns (nil, false) when miniredis is not on the module
// graph, so tests can opt in without breaking builds. To enable, add
// `github.com/alicebob/miniredis/v2` to go.mod and replace the body of
// this helper with a real implementation.
func NewMiniredisClient(t *testing.T) (*redis.Client, bool) {
	t.Log("testutil: NewMiniredisClient is a no-op; miniredis is not bundled")
	return nil, false
}
