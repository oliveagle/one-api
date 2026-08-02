package model

import (
	"testing"

	"github.com/songquanpeng/one-api/common/config"
)

// seedChannelCacheForTest replaces the in-memory channel cache with a fixed
// (group, model) -> channels map. The slice is expected to be sorted by
// priority descending, as InitChannelCache would produce.
func seedChannelCacheForTest(group, model string, channels []*Channel) {
	channelSyncLock.Lock()
	defer channelSyncLock.Unlock()
	group2model2channels = map[string]map[string][]*Channel{
		group: {
			model: channels,
		},
	}
}

func TestCacheGetRandomSatisfiedChannel_PrefersTopPriorityTier(t *testing.T) {
	prev := config.MemoryCacheEnabled
	config.MemoryCacheEnabled = true
	defer func() { config.MemoryCacheEnabled = prev }()

	mk := func(id int, priority int64) *Channel {
		p := priority
		return &Channel{Id: id, Name: "ch", Priority: &p}
	}

	// Two normal channels (priority 0) + two last-resort channels (negative).
	// Sorted by priority descending, as InitChannelCache does.
	seedChannelCacheForTest("g", "m", []*Channel{
		mk(1, 0),
		mk(2, 0),
		mk(3, -100000),
		mk(4, -100000),
	})

	// Primary selection must never pick a last-resort (negative priority) channel.
	for i := 0; i < 200; i++ {
		ch, err := CacheGetRandomSatisfiedChannel("g", "m", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ch.Id == 3 || ch.Id == 4 {
			t.Fatalf("primary selection picked last-resort channel %d", ch.Id)
		}
	}

	// Explicit retry (ignoreFirstPriority) may fall through to the lower tier.
	seen := map[int]bool{}
	for i := 0; i < 100; i++ {
		ch, err := CacheGetRandomSatisfiedChannel("g", "m", true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		seen[ch.Id] = true
	}
	if !seen[3] && !seen[4] {
		t.Fatalf("retry never reached last-resort tier, seen=%v", seen)
	}
}

func TestCacheGetRandomSatisfiedChannel_AllLastResortStillServed(t *testing.T) {
	prev := config.MemoryCacheEnabled
	config.MemoryCacheEnabled = true
	defer func() { config.MemoryCacheEnabled = prev }()

	mk := func(id int, priority int64) *Channel {
		p := priority
		return &Channel{Id: id, Name: "ch", Priority: &p}
	}

	// Only last-resort channels exist for this model — they must still be served.
	seedChannelCacheForTest("g", "m", []*Channel{
		mk(3, -100000),
		mk(4, -100000),
	})

	for i := 0; i < 50; i++ {
		ch, err := CacheGetRandomSatisfiedChannel("g", "m", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ch.Id != 3 && ch.Id != 4 {
			t.Fatalf("unexpected channel %d", ch.Id)
		}
	}
}
