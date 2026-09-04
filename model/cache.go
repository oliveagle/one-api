package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/common/random"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	TokenCacheSeconds         = config.SyncFrequency
	UserId2GroupCacheSeconds  = config.SyncFrequency
	UserId2QuotaCacheSeconds  = config.SyncFrequency
	UserId2StatusCacheSeconds = config.SyncFrequency
	GroupModelsCacheSeconds   = config.SyncFrequency
)

func CacheGetTokenByKey(key string) (*Token, error) {
	keyCol := "`key`"
	if common.UsingPostgreSQL {
		keyCol = `"key"`
	}
	var token Token
	if !common.RedisEnabled {
		err := DB.Where(keyCol+" = ?", key).First(&token).Error
		return &token, err
	}
	tokenObjectString, err := common.RedisGet(fmt.Sprintf("token:%s", key))
	if err != nil {
		err := DB.Where(keyCol+" = ?", key).First(&token).Error
		if err != nil {
			return nil, err
		}
		jsonBytes, err := json.Marshal(token)
		if err != nil {
			return nil, err
		}
		err = common.RedisSet(fmt.Sprintf("token:%s", key), string(jsonBytes), time.Duration(TokenCacheSeconds)*time.Second)
		if err != nil {
			logger.SysError("Redis set token error: " + err.Error())
		}
		return &token, nil
	}
	err = json.Unmarshal([]byte(tokenObjectString), &token)
	return &token, err
}

func CacheGetUserGroup(id int) (group string, err error) {
	if !common.RedisEnabled {
		return GetUserGroup(id)
	}
	group, err = common.RedisGet(fmt.Sprintf("user_group:%d", id))
	if err != nil {
		group, err = GetUserGroup(id)
		if err != nil {
			return "", err
		}
		err = common.RedisSet(fmt.Sprintf("user_group:%d", id), group, time.Duration(UserId2GroupCacheSeconds)*time.Second)
		if err != nil {
			logger.SysError("Redis set user group error: " + err.Error())
		}
	}
	return group, err
}

func fetchAndUpdateUserQuota(ctx context.Context, id int) (quota int64, err error) {
	quota, err = GetUserQuota(id)
	if err != nil {
		return 0, err
	}
	err = common.RedisSet(fmt.Sprintf("user_quota:%d", id), fmt.Sprintf("%d", quota), time.Duration(UserId2QuotaCacheSeconds)*time.Second)
	if err != nil {
		logger.Error(ctx, "Redis set user quota error: "+err.Error())
	}
	return
}

func CacheGetUserQuota(ctx context.Context, id int) (quota int64, err error) {
	if !common.RedisEnabled {
		return GetUserQuota(id)
	}
	quotaString, err := common.RedisGet(fmt.Sprintf("user_quota:%d", id))
	if err != nil {
		return fetchAndUpdateUserQuota(ctx, id)
	}
	quota, err = strconv.ParseInt(quotaString, 10, 64)
	if err != nil {
		return 0, nil
	}
	if quota <= config.PreConsumedQuota { // when user's quota is less than pre-consumed quota, we need to fetch from db
		logger.Infof(ctx, "user %d's cached quota is too low: %d, refreshing from db", quota, id)
		return fetchAndUpdateUserQuota(ctx, id)
	}
	return quota, nil
}

func CacheUpdateUserQuota(ctx context.Context, id int) error {
	if !common.RedisEnabled {
		return nil
	}
	quota, err := CacheGetUserQuota(ctx, id)
	if err != nil {
		return err
	}
	err = common.RedisSet(fmt.Sprintf("user_quota:%d", id), fmt.Sprintf("%d", quota), time.Duration(UserId2QuotaCacheSeconds)*time.Second)
	return err
}

func CacheDecreaseUserQuota(id int, quota int64) error {
	if !common.RedisEnabled {
		return nil
	}
	err := common.RedisDecrease(fmt.Sprintf("user_quota:%d", id), int64(quota))
	return err
}

func CacheIsUserEnabled(userId int) (bool, error) {
	if !common.RedisEnabled {
		return IsUserEnabled(userId)
	}
	enabled, err := common.RedisGet(fmt.Sprintf("user_enabled:%d", userId))
	if err == nil {
		return enabled == "1", nil
	}

	userEnabled, err := IsUserEnabled(userId)
	if err != nil {
		return false, err
	}
	enabled = "0"
	if userEnabled {
		enabled = "1"
	}
	err = common.RedisSet(fmt.Sprintf("user_enabled:%d", userId), enabled, time.Duration(UserId2StatusCacheSeconds)*time.Second)
	if err != nil {
		logger.SysError("Redis set user enabled error: " + err.Error())
	}
	return userEnabled, err
}

func CacheGetGroupModels(ctx context.Context, group string) ([]string, error) {
	if !common.RedisEnabled {
		return GetGroupModels(ctx, group)
	}
	modelsStr, err := common.RedisGet(fmt.Sprintf("group_models:%s", group))
	if err == nil {
		return strings.Split(modelsStr, ","), nil
	}
	models, err := GetGroupModels(ctx, group)
	if err != nil {
		return nil, err
	}
	err = common.RedisSet(fmt.Sprintf("group_models:%s", group), strings.Join(models, ","), time.Duration(GroupModelsCacheSeconds)*time.Second)
	if err != nil {
		logger.SysError("Redis set group models error: " + err.Error())
	}
	return models, nil
}

var group2model2channels map[string]map[string][]*Channel
var channelSyncLock sync.RWMutex

// name2channel indexes enabled channels by name for channel-name model
// addressing (GetChannelByName); rebuilt with the rest of the cache.
var name2channel map[string][]*Channel

func InitChannelCache() {
	newChannelId2channel := make(map[int]*Channel)
	var channels []*Channel
	DB.Where("status = ?", ChannelStatusEnabled).Find(&channels)
	for _, channel := range channels {
		newChannelId2channel[channel.Id] = channel
	}
	newName2channel := make(map[string][]*Channel)
	for _, channel := range channels {
		name := strings.TrimSpace(channel.Name)
		if name != "" {
			newName2channel[name] = append(newName2channel[name], channel)
		}
	}
	var abilities []*Ability
	DB.Find(&abilities)
	groups := make(map[string]bool)
	for _, ability := range abilities {
		groups[ability.Group] = true
	}
	newGroup2model2channels := make(map[string]map[string][]*Channel)
	for group := range groups {
		newGroup2model2channels[group] = make(map[string][]*Channel)
	}
	for _, channel := range channels {
		groups := strings.Split(channel.Group, ",")
		for _, group := range groups {
			models := strings.Split(channel.Models, ",")
			for _, model := range models {
				if _, ok := newGroup2model2channels[group][model]; !ok {
					newGroup2model2channels[group][model] = make([]*Channel, 0)
				}
				newGroup2model2channels[group][model] = append(newGroup2model2channels[group][model], channel)
			}
		}
	}

	// sort by routing priority: pay_as_you_go channels (RoutingPriority
	// = priority - 1) naturally sort one tier below plan channels at the
	// same configured priority, so randomTieredPick partitions them out
	// of the first-choice tier.
	for group, model2channels := range newGroup2model2channels {
		for model, channels := range model2channels {
			sort.Slice(channels, func(i, j int) bool {
				return channels[i].RoutingPriority() > channels[j].RoutingPriority()
			})
			newGroup2model2channels[group][model] = channels
		}
	}

	channelSyncLock.Lock()
	group2model2channels = newGroup2model2channels
	name2channel = newName2channel
	channelSyncLock.Unlock()
	logger.SysLog("channels synced from database")
}

func SyncChannelCache(frequency int) {
	for {
		time.Sleep(time.Duration(frequency) * time.Second)
		logger.SysLog("syncing channels from database")
		InitChannelCache()
	}
}

func CacheGetRandomSatisfiedChannel(group string, model string, ignoreFirstPriority bool) (*Channel, error) {
	if !config.MemoryCacheEnabled {
		return GetRandomSatisfiedChannel(group, model, ignoreFirstPriority)
	}
	channelSyncLock.RLock()
	channels := group2model2channels[group][model]
	channelSyncLock.RUnlock()
	if len(channels) == 0 {
		return nil, errors.New("channel not found")
	}
	return randomTieredPick(channels, ignoreFirstPriority), nil
}

// randomTieredPick returns one channel from a non-empty, priority-sorted
// candidate list. It partitions at the first priority boundary so the
// highest-priority tier is always preferred; negative-priority tiers
// (last-resort providers) are only picked on explicit retry
// (ignoreFirstPriority) or when no higher tier exists. Callers pass a slice
// they own (or the cache's slice read-only); the picker never mutates it.
func randomTieredPick(channels []*Channel, ignoreFirstPriority bool) *Channel {
	endIdx := len(channels)
	firstPriority := channels[0].RoutingPriority()
	for i := range channels {
		if channels[i].RoutingPriority() != firstPriority {
			endIdx = i
			break
		}
	}
	idx := rand.Intn(endIdx)
	if ignoreFirstPriority {
		if endIdx < len(channels) { // which means there are more than one priority
			idx = random.RandRange(endIdx, len(channels))
		}
	}
	return channels[idx]
}

// CacheGetRandomSatisfiedChannelExcluding picks a random candidate for
// (group, model), preferring channels that neither already failed during this
// request (exclude) nor are under a routing cooldown (see
// MarkChannelCooldown). When every candidate is excluded or cooling it
// degrades to the plain random pick, so a small pool never turns into "no
// channel available".
func CacheGetRandomSatisfiedChannelExcluding(group string, model string, ignoreFirstPriority bool, exclude map[int]bool) (*Channel, error) {
	if !config.MemoryCacheEnabled {
		return GetRandomSatisfiedChannel(group, model, ignoreFirstPriority)
	}
	channelSyncLock.RLock()
	channels := group2model2channels[group][model]
	var eligible []*Channel
	if len(channels) > 0 {
		eligible = make([]*Channel, 0, len(channels))
		for _, ch := range channels {
			if exclude != nil && exclude[ch.Id] {
				continue
			}
			if ChannelCoolingDown(ch.Id) {
				continue
			}
			eligible = append(eligible, ch)
		}
	}
	channelSyncLock.RUnlock()
	if len(eligible) == 0 {
		return CacheGetRandomSatisfiedChannel(group, model, ignoreFirstPriority)
	}
	return randomTieredPick(eligible, ignoreFirstPriority), nil
}

// ---------------------------------------------------------------------------
// Channel cooldown registry
// ---------------------------------------------------------------------------

// channelCooldowns records per-channel routing penalties shared by every
// routing path (random picks and sticky bindings). Quota-exhausted or
// rate-limited channels are skipped while alternatives exist; pickers fall
// back to them once every candidate is cooling, so availability never drops
// to zero. It lives at the model layer because the cache picker must consult
// it and relay/routing already imports model (the reverse would cycle).
var channelCooldowns sync.Map // channelId int -> time.Time deadline

// MarkChannelCooldown penalizes a channel for routing until the deadline.
// Zero or past deadlines are ignored.
func MarkChannelCooldown(channelId int, until time.Time) {
	if until.IsZero() || !until.After(time.Now()) {
		return
	}
	channelCooldowns.Store(channelId, until)
}

// ChannelCoolingDown reports whether a channel is still under a routing
// penalty; sticky routing consults it in addition to its own cooldown store.
func ChannelCoolingDown(channelId int) bool {
	v, ok := channelCooldowns.Load(channelId)
	if !ok {
		return false
	}
	deadline, _ := v.(time.Time)
	if time.Now().After(deadline) {
		channelCooldowns.Delete(channelId) // lazy expiry
		return false
	}
	return true
}

// ResetChannelCooldowns clears the routing-penalty registry (test isolation).
func ResetChannelCooldowns() {
	channelCooldowns.Range(func(key, _ any) bool {
		channelCooldowns.Delete(key)
		return true
	})
}

// CacheGetSatisfiedChannels returns the memory-cached candidate channels for
// (group, model), sorted by priority descending (highest first). It is used by
// the session-sticky routing module to bind a session to a node and to pick
// failover alternatives. It returns nil when the memory cache is disabled.
//
// The returned slice is owned by the channel cache and must not be mutated by
// callers.
func CacheGetSatisfiedChannels(group string, model string) []*Channel {
	if !config.MemoryCacheEnabled {
		return nil
	}
	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()
	return group2model2channels[group][model]
}
