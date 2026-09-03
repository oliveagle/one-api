package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/helper"
	"github.com/songquanpeng/one-api/common/logger"
	"gorm.io/gorm"
)

const (
	ChannelStatusUnknown          = 0
	ChannelStatusEnabled          = 1 // don't use 0, 0 is the default value!
	ChannelStatusManuallyDisabled = 2 // also don't use 0
	ChannelStatusAutoDisabled     = 3
)

type Channel struct {
	Id                 int     `json:"id"`
	Type               int     `json:"type" gorm:"default:0"`
	Key                string  `json:"key" gorm:"type:text"`
	Status             int     `json:"status" gorm:"default:1"`
	Name               string  `json:"name" gorm:"index"`
	Weight             *uint   `json:"weight" gorm:"default:0"`
	CreatedTime        int64   `json:"created_time" gorm:"bigint"`
	TestTime           int64   `json:"test_time" gorm:"bigint"`
	ResponseTime       int     `json:"response_time"` // in milliseconds
	BaseURL            *string `json:"base_url" gorm:"column:base_url;default:''"`
	Other              *string `json:"other"`   // DEPRECATED: please save config to field Config
	Balance            float64 `json:"balance"` // in USD
	BalanceUpdatedTime int64   `json:"balance_updated_time" gorm:"bigint"`
	Models             string  `json:"models"`
	Group              string  `json:"group" gorm:"type:varchar(32);default:'default'"`
	UsedQuota          int64   `json:"used_quota" gorm:"bigint;default:0"`
	ModelMapping       *string `json:"model_mapping" gorm:"type:varchar(1024);default:''"`
	Priority           *int64  `json:"priority" gorm:"bigint;default:0"`
	Config             string  `json:"config"`
	SystemPrompt       *string `json:"system_prompt" gorm:"type:text"`
	Headers            *string `json:"headers" gorm:"type:varchar(1024);default:''"`
}

type ChannelConfig struct {
	Region            string `json:"region,omitempty"`
	SK                string `json:"sk,omitempty"`
	AK                string `json:"ak,omitempty"`
	UserID            string `json:"user_id,omitempty"`
	APIVersion        string `json:"api_version,omitempty"`
	LibraryID         string `json:"library_id,omitempty"`
	Plugin            string `json:"plugin,omitempty"`
	VertexAIProjectID string `json:"vertex_ai_project_id,omitempty"`
	VertexAIADC       string `json:"vertex_ai_adc,omitempty"`
	// SupportResponses marks a channel whose upstream natively implements the
	// OpenAI Responses API (POST /v1/responses), so requests can be passed
	// through untouched instead of being converted to Chat Completions.
	// Channels that do not set this flag (e.g. opencode-go) get an automatic
	// Responses -> Chat Completions conversion in relayResponsesCreate.
	SupportResponses bool `json:"support_responses,omitempty"`
	// ManageKey is an account-management credential that is distinct from the
	// relay credential stored in Channel.Key. Some upstreams (e.g. AIHubMix)
	// refuse to expose account balance to the regular API key and require a
	// separate system access token instead.
	ManageKey string `json:"manage_key,omitempty"`
	// SkipReasoningInjection disables the reasoning_content placeholder
	// injection for channels that do not require thinking-mode turns to
	// carry reasoning back (echo-prone channels snowball the placeholder).
	SkipReasoningInjection bool `json:"skip_reasoning_injection,omitempty"`
	// ResponsesOnly marks a channel whose upstream implements ONLY the
	// Responses API (POST /v1/responses) with no chat-completions
	// endpoint. The channel test exercises the responses endpoint for
	// such channels (the default chat probe would 404 and auto-disable
	// the channel), and relay routing treats them as native responses.
	ResponsesOnly bool `json:"responses_only,omitempty"`
	// DefaultModel is the upstream model served when the channel is
	// addressed by its bare name (request model == channel name), letting
	// clients whose picker only switches models — not providers (e.g.
	// codex /model) — select a specific channel. "name/model" addresses
	// any other model from the channel's list. See middleware.Distribute.
	DefaultModel string `json:"default_model,omitempty"`
}

// GetChannelByName returns the enabled channel whose name matches exactly
// and serves the given group. Channel-name model addressing uses this to
// resolve "name" / "name/model" request models to one specific channel.
// The key IS selected: the addressed channel forwards with its own
// credential, exactly like an admin-pinned channel id.
func GetChannelByName(name string, group string) (*Channel, error) {
	// Prefer the in-memory name index (rebuilt with the channel cache every
	// SYNC_FREQUENCY); fall back to a DB query when the cache is disabled so
	// behavior never depends on deployment flags.
	if config.MemoryCacheEnabled {
		channelSyncLock.RLock()
		channels := name2channel[strings.TrimSpace(name)]
		channelSyncLock.RUnlock()
		for _, ch := range channels {
			for _, g := range strings.Split(ch.Group, ",") {
				if strings.TrimSpace(g) == group {
					return ch, nil
				}
			}
		}
		return nil, errors.New("channel not found")
	}
	var channels []*Channel
	if err := DB.Where("name = ? AND status = ?", name, ChannelStatusEnabled).Find(&channels).Error; err != nil {
		return nil, err
	}
	for _, ch := range channels {
		for _, g := range strings.Split(ch.Group, ",") {
			if strings.TrimSpace(g) == group {
				return ch, nil
			}
		}
	}
	return nil, errors.New("channel not found")
}

// ModelServed returns whether model is one of the channel's listed models.
func (channel *Channel) ModelServed(model string) bool {
	for _, m := range strings.Split(channel.Models, ",") {
		if strings.TrimSpace(m) == model {
			return true
		}
	}
	return false
}

func GetAllChannels(startIdx int, num int, scope string) ([]*Channel, error) {
	var channels []*Channel
	var err error
	switch scope {
	case "all":
		err = DB.Order("id desc").Find(&channels).Error
	case "disabled":
		err = DB.Order("id desc").Where("status = ? or status = ?", ChannelStatusAutoDisabled, ChannelStatusManuallyDisabled).Find(&channels).Error
	default:
		err = DB.Order("id desc").Limit(num).Offset(startIdx).Omit("key").Find(&channels).Error
	}
	return channels, err
}

func SearchChannels(keyword string) (channels []*Channel, err error) {
	err = DB.Omit("key").Where("id = ? or name LIKE ?", helper.String2Int(keyword), keyword+"%").Find(&channels).Error
	return channels, err
}

func GetChannelById(id int, selectAll bool) (*Channel, error) {
	channel := Channel{Id: id}
	var err error = nil
	if selectAll {
		err = DB.First(&channel, "id = ?", id).Error
	} else {
		err = DB.Omit("key").First(&channel, "id = ?", id).Error
	}
	return &channel, err
}

func BatchInsertChannels(channels []Channel) error {
	var err error
	err = DB.Create(&channels).Error
	if err != nil {
		return err
	}
	for _, channel_ := range channels {
		err = channel_.AddAbilities()
		if err != nil {
			return err
		}
	}
	return nil
}

func (channel *Channel) GetPriority() int64 {
	if channel.Priority == nil {
		return 0
	}
	return *channel.Priority
}

func (channel *Channel) GetBaseURL() string {
	if channel.BaseURL == nil {
		return ""
	}
	return *channel.BaseURL
}

func (channel *Channel) GetModelMapping() map[string]string {
	if channel.ModelMapping == nil || *channel.ModelMapping == "" || *channel.ModelMapping == "{}" {
		return nil
	}
	modelMapping := make(map[string]string)
	err := json.Unmarshal([]byte(*channel.ModelMapping), &modelMapping)
	if err != nil {
		logger.SysError(fmt.Sprintf("failed to unmarshal model mapping for channel %d, error: %s", channel.Id, err.Error()))
		return nil
	}
	return modelMapping
}

// GetHeaders parses the JSON-encoded Headers field and returns the resulting
// map[string]string. It returns nil when the field is empty or unparseable so
// callers can safely range over the result without a nil check.
func (channel *Channel) GetHeaders() map[string]string {
	if channel.Headers == nil || *channel.Headers == "" || *channel.Headers == "{}" {
		return nil
	}
	headers := make(map[string]string)
	if err := json.Unmarshal([]byte(*channel.Headers), &headers); err != nil {
		logger.SysError(fmt.Sprintf("failed to unmarshal headers for channel %d, error: %s", channel.Id, err.Error()))
		return nil
	}
	return headers
}

func (channel *Channel) Insert() error {
	var err error
	err = DB.Create(channel).Error
	if err != nil {
		return err
	}
	err = channel.AddAbilities()
	return err
}

func (channel *Channel) Update() error {
	var err error
	err = DB.Model(channel).Updates(channel).Error
	if err != nil {
		return err
	}
	DB.Model(channel).First(channel, "id = ?", channel.Id)
	err = channel.UpdateAbilities()
	return err
}

func (channel *Channel) UpdateResponseTime(responseTime int64) {
	err := DB.Model(channel).Select("response_time", "test_time").Updates(Channel{
		TestTime:     helper.GetTimestamp(),
		ResponseTime: int(responseTime),
	}).Error
	if err != nil {
		logger.SysError("failed to update response time: " + err.Error())
	}
}

func (channel *Channel) UpdateBalance(balance float64) {
	err := DB.Model(channel).Select("balance_updated_time", "balance").Updates(Channel{
		BalanceUpdatedTime: helper.GetTimestamp(),
		Balance:            balance,
	}).Error
	if err != nil {
		logger.SysError("failed to update balance: " + err.Error())
	}
}

func (channel *Channel) Delete() error {
	var err error
	err = DB.Delete(channel).Error
	if err != nil {
		return err
	}
	err = channel.DeleteAbilities()
	return err
}

func (channel *Channel) LoadConfig() (ChannelConfig, error) {
	var cfg ChannelConfig
	if channel.Config == "" {
		return cfg, nil
	}
	err := json.Unmarshal([]byte(channel.Config), &cfg)
	if err != nil {
		return cfg, err
	}
	return cfg, nil
}

func UpdateChannelStatusById(id int, status int) {
	err := UpdateAbilityStatus(id, status == ChannelStatusEnabled)
	if err != nil {
		logger.SysError("failed to update ability status: " + err.Error())
	}
	err = DB.Model(&Channel{}).Where("id = ?", id).Update("status", status).Error
	if err != nil {
		logger.SysError("failed to update channel status: " + err.Error())
	}
}

func UpdateChannelUsedQuota(id int, quota int64) {
	if config.BatchUpdateEnabled {
		addNewRecord(BatchUpdateTypeChannelUsedQuota, id, quota)
		return
	}
	updateChannelUsedQuota(id, quota)
}

func updateChannelUsedQuota(id int, quota int64) {
	err := DB.Model(&Channel{}).Where("id = ?", id).Update("used_quota", gorm.Expr("used_quota + ?", quota)).Error
	if err != nil {
		logger.SysError("failed to update channel used quota: " + err.Error())
	}
}

func DeleteChannelByStatus(status int64) (int64, error) {
	result := DB.Where("status = ?", status).Delete(&Channel{})
	return result.RowsAffected, result.Error
}

func DeleteDisabledChannel() (int64, error) {
	result := DB.Where("status = ? or status = ?", ChannelStatusAutoDisabled, ChannelStatusManuallyDisabled).Delete(&Channel{})
	return result.RowsAffected, result.Error
}

// ChannelAddressedModels lists the bare channel names that are addressable
// as request models (enabled, serving the group, default_model configured).
// /v1/models advertises them so client model pickers — e.g. codex /model —
// can offer per-channel selection.
func ChannelAddressedModels(group string) []string {
	var channels []*Channel
	if config.MemoryCacheEnabled {
		channelSyncLock.RLock()
		for _, list := range name2channel {
			channels = append(channels, list...)
		}
		channelSyncLock.RUnlock()
	} else if err := DB.Omit("key").Where("status = ?", ChannelStatusEnabled).Find(&channels).Error; err != nil {
		return nil
	}
	names := make([]string, 0, len(channels))
	for _, ch := range channels {
		servesGroup := false
		for _, g := range strings.Split(ch.Group, ",") {
			if strings.TrimSpace(g) == group {
				servesGroup = true
				break
			}
		}
		if !servesGroup {
			continue
		}
		if cfg, err := ch.LoadConfig(); err == nil && cfg.DefaultModel != "" {
			names = append(names, ch.Name)
		}
	}
	return names
}
