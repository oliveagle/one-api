package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/songquanpeng/one-api/common/client"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/monitor"
	"github.com/songquanpeng/one-api/relay/channeltype"

	"github.com/gin-gonic/gin"
)

// https://github.com/songquanpeng/one-api/issues/79

type OpenAISubscriptionResponse struct {
	Object             string  `json:"object"`
	HasPaymentMethod   bool    `json:"has_payment_method"`
	SoftLimitUSD       float64 `json:"soft_limit_usd"`
	HardLimitUSD       float64 `json:"hard_limit_usd"`
	SystemHardLimitUSD float64 `json:"system_hard_limit_usd"`
	AccessUntil        int64   `json:"access_until"`
}

type OpenAIUsageDailyCost struct {
	Timestamp float64 `json:"timestamp"`
	LineItems []struct {
		Name string  `json:"name"`
		Cost float64 `json:"cost"`
	}
}

type OpenAICreditGrants struct {
	Object         string  `json:"object"`
	TotalGranted   float64 `json:"total_granted"`
	TotalUsed      float64 `json:"total_used"`
	TotalAvailable float64 `json:"total_available"`
}

type OpenAIUsageResponse struct {
	Object string `json:"object"`
	//DailyCosts []OpenAIUsageDailyCost `json:"daily_costs"`
	TotalUsage float64 `json:"total_usage"` // unit: 0.01 dollar
}

type OpenAISBUsageResponse struct {
	Msg  string `json:"msg"`
	Data *struct {
		Credit string `json:"credit"`
	} `json:"data"`
}

type AIProxyUserOverviewResponse struct {
	Success   bool   `json:"success"`
	Message   string `json:"message"`
	ErrorCode int    `json:"error_code"`
	Data      struct {
		TotalPoints float64 `json:"totalPoints"`
	} `json:"data"`
}

type API2GPTUsageResponse struct {
	Object         string  `json:"object"`
	TotalGranted   float64 `json:"total_granted"`
	TotalUsed      float64 `json:"total_used"`
	TotalRemaining float64 `json:"total_remaining"`
}

type APGC2DGPTUsageResponse struct {
	//Grants         interface{} `json:"grants"`
	Object         string  `json:"object"`
	TotalAvailable float64 `json:"total_available"`
	TotalGranted   float64 `json:"total_granted"`
	TotalUsed      float64 `json:"total_used"`
}

type SiliconFlowUsageResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  bool   `json:"status"`
	Data    struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Image         string `json:"image"`
		Email         string `json:"email"`
		IsAdmin       bool   `json:"isAdmin"`
		Balance       string `json:"balance"`
		Status        string `json:"status"`
		Introduction  string `json:"introduction"`
		Role          string `json:"role"`
		ChargeBalance string `json:"chargeBalance"`
		TotalBalance  string `json:"totalBalance"`
		Category      string `json:"category"`
	} `json:"data"`
}

type DeepSeekUsageResponse struct {
	IsAvailable  bool `json:"is_available"`
	BalanceInfos []struct {
		Currency        string `json:"currency"`
		TotalBalance    string `json:"total_balance"`
		GrantedBalance  string `json:"granted_balance"`
		ToppedUpBalance string `json:"topped_up_balance"`
	} `json:"balance_infos"`
}

type OpenRouterResponse struct {
	Data struct {
		TotalCredits float64 `json:"total_credits"`
		TotalUsage   float64 `json:"total_usage"`
	} `json:"data"`
}

// AIHubMixUserResponse models GET /api/user/self on AIHubMix.
// AIHubMix is a one-api derivative, so `quota` / `used_quota` are integers in
// AIHubMix's own quota unit and must be divided by *its* quota_per_unit (not
// this instance's config.QuotaPerUnit) to get USD. See aihubmixQuotaPerUnit.
type AIHubMixUserResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    struct {
		Username  string `json:"username"`
		Quota     int64  `json:"quota"`
		UsedQuota int64  `json:"used_quota"`
	} `json:"data"`
}

// AIHubMixStatusResponse models GET /api/status on AIHubMix, which is
// unauthenticated and exposes the quota unit used by that deployment.
type AIHubMixStatusResponse struct {
	Success bool `json:"success"`
	Data    struct {
		QuotaPerUnit float64 `json:"quota_per_unit"`
	} `json:"data"`
}

// GetAuthHeader get auth header
func GetAuthHeader(token string) http.Header {
	h := http.Header{}
	h.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	return h
}

func GetResponseBody(method, url string, channel *model.Channel, headers http.Header) ([]byte, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	for k := range headers {
		req.Header.Add(k, headers.Get(k))
	}
	res, err := client.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status code: %d", res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	err = res.Body.Close()
	if err != nil {
		return nil, err
	}
	return body, nil
}

func updateChannelCloseAIBalance(channel *model.Channel) (float64, error) {
	url := fmt.Sprintf("%s/dashboard/billing/credit_grants", channel.GetBaseURL())
	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))

	if err != nil {
		return 0, err
	}
	response := OpenAICreditGrants{}
	err = json.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	channel.UpdateBalance(response.TotalAvailable)
	return response.TotalAvailable, nil
}

func updateChannelOpenAISBBalance(channel *model.Channel) (float64, error) {
	url := fmt.Sprintf("https://api.openai-sb.com/sb-api/user/status?api_key=%s", channel.Key)
	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := OpenAISBUsageResponse{}
	err = json.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	if response.Data == nil {
		return 0, errors.New(response.Msg)
	}
	balance, err := strconv.ParseFloat(response.Data.Credit, 64)
	if err != nil {
		return 0, err
	}
	channel.UpdateBalance(balance)
	return balance, nil
}

func updateChannelAIProxyBalance(channel *model.Channel) (float64, error) {
	url := "https://aiproxy.io/api/report/getUserOverview"
	headers := http.Header{}
	headers.Add("Api-Key", channel.Key)
	body, err := GetResponseBody("GET", url, channel, headers)
	if err != nil {
		return 0, err
	}
	response := AIProxyUserOverviewResponse{}
	err = json.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	if !response.Success {
		return 0, fmt.Errorf("code: %d, message: %s", response.ErrorCode, response.Message)
	}
	channel.UpdateBalance(response.Data.TotalPoints)
	return response.Data.TotalPoints, nil
}

func updateChannelAPI2GPTBalance(channel *model.Channel) (float64, error) {
	url := "https://api.api2gpt.com/dashboard/billing/credit_grants"
	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))

	if err != nil {
		return 0, err
	}
	response := API2GPTUsageResponse{}
	err = json.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	channel.UpdateBalance(response.TotalRemaining)
	return response.TotalRemaining, nil
}

func updateChannelAIGC2DBalance(channel *model.Channel) (float64, error) {
	url := "https://api.aigc2d.com/dashboard/billing/credit_grants"
	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := APGC2DGPTUsageResponse{}
	err = json.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	channel.UpdateBalance(response.TotalAvailable)
	return response.TotalAvailable, nil
}

func updateChannelSiliconFlowBalance(channel *model.Channel) (float64, error) {
	url := "https://api.siliconflow.cn/v1/user/info"
	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := SiliconFlowUsageResponse{}
	err = json.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	if response.Code != 20000 {
		return 0, fmt.Errorf("code: %d, message: %s", response.Code, response.Message)
	}
	balance, err := strconv.ParseFloat(response.Data.TotalBalance, 64)
	if err != nil {
		return 0, err
	}
	channel.UpdateBalance(balance)
	return balance, nil
}

func updateChannelDeepSeekBalance(channel *model.Channel) (float64, error) {
	url := "https://api.deepseek.com/user/balance"
	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := DeepSeekUsageResponse{}
	err = json.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	index := -1
	for i, balanceInfo := range response.BalanceInfos {
		if balanceInfo.Currency == "CNY" {
			index = i
			break
		}
	}
	if index == -1 {
		return 0, errors.New("currency CNY not found")
	}
	balance, err := strconv.ParseFloat(response.BalanceInfos[index].TotalBalance, 64)
	if err != nil {
		return 0, err
	}
	channel.UpdateBalance(balance)
	return balance, nil
}

func updateChannelOpenRouterBalance(channel *model.Channel) (float64, error) {
	url := "https://openrouter.ai/api/v1/credits"
	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	response := OpenRouterResponse{}
	err = json.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	balance := response.Data.TotalCredits - response.Data.TotalUsage
	channel.UpdateBalance(balance)
	return balance, nil
}

// aihubmixAccountBaseURL returns the origin to use for AIHubMix's management
// API. The channel base URL is the *relay* endpoint and is commonly configured
// as "https://aihubmix.com/v1", but the management API lives at the site root:
// keeping the "/v1" suffix would request /v1/api/user/self and get a 404.
func aihubmixAccountBaseURL(channel *model.Channel) string {
	baseURL := channel.GetBaseURL()
	if baseURL == "" {
		baseURL = channeltype.ChannelBaseURLs[channeltype.AIHubMix]
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	return strings.TrimSuffix(baseURL, "/")
}

// aihubmixDefaultQuotaPerUnit is the quota unit AIHubMix reported at the time
// of writing ($1 == 500000 units). Used only if /api/status is unreachable.
const aihubmixDefaultQuotaPerUnit = 500000.0

// aihubmixQuotaPerUnit asks the upstream how many quota units make up one USD.
// This must NOT be read from config.QuotaPerUnit: that option belongs to *this*
// one-api instance and an operator who changes it would silently corrupt the
// reported upstream balance. /api/status is unauthenticated.
func aihubmixQuotaPerUnit(channel *model.Channel) float64 {
	url := fmt.Sprintf("%s/api/status", aihubmixAccountBaseURL(channel))
	body, err := GetResponseBody("GET", url, channel, http.Header{})
	if err != nil {
		logger.SysError("failed to query AIHubMix quota_per_unit, falling back to default: " + err.Error())
		return aihubmixDefaultQuotaPerUnit
	}
	var status AIHubMixStatusResponse
	if err := json.Unmarshal(body, &status); err != nil || status.Data.QuotaPerUnit <= 0 {
		logger.SysError("failed to parse AIHubMix quota_per_unit, falling back to default")
		return aihubmixDefaultQuotaPerUnit
	}
	return status.Data.QuotaPerUnit
}

func updateChannelAIHubMixBalance(channel *model.Channel) (float64, error) {
	cfg, err := channel.LoadConfig()
	if err != nil {
		return 0, err
	}
	// AIHubMix separates the relay credential ("sk-***") from the account
	// management credential (a 32-char hex system access token). Only the latter
	// may read the account; passing the relay key here yields 401 (verified
	// against the live API).
	token := cfg.ManageKey
	if token == "" {
		return 0, errors.New("请在渠道配置中填写 AIHubMix 系统访问令牌（Manage Key），" +
			"可在 https://console.aihubmix.com/setting 点击「生成系统访问令牌」获取；" +
			"用于调用模型的 sk- 开头的 API Key 无法查询余额")
	}
	url := fmt.Sprintf("%s/api/user/self", aihubmixAccountBaseURL(channel))
	// AIHubMix accepts both the bare token and the "Bearer "-prefixed form (it
	// inherits one-api's ValidateAccessToken, which strips the prefix), so the
	// standard helper is fine here.
	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(token))
	if err != nil {
		return 0, err
	}
	response := AIHubMixUserResponse{}
	err = json.Unmarshal(body, &response)
	if err != nil {
		return 0, err
	}
	if !response.Success {
		if response.Message != "" {
			return 0, errors.New(response.Message)
		}
		return 0, errors.New("failed to query AIHubMix account balance")
	}
	balance := float64(response.Data.Quota) / aihubmixQuotaPerUnit(channel)
	channel.UpdateBalance(balance)
	return balance, nil
}

func updateChannelBalance(channel *model.Channel) (float64, error) {
	baseURL := channeltype.ChannelBaseURLs[channel.Type]
	if channel.GetBaseURL() == "" {
		channel.BaseURL = &baseURL
	}
	switch channel.Type {
	case channeltype.OpenAI:
		if channel.GetBaseURL() != "" {
			baseURL = channel.GetBaseURL()
		}
	case channeltype.Azure:
		return 0, errors.New("尚未实现")
	case channeltype.Custom:
		baseURL = channel.GetBaseURL()
	case channeltype.CloseAI:
		return updateChannelCloseAIBalance(channel)
	case channeltype.OpenAISB:
		return updateChannelOpenAISBBalance(channel)
	case channeltype.AIProxy:
		return updateChannelAIProxyBalance(channel)
	case channeltype.API2GPT:
		return updateChannelAPI2GPTBalance(channel)
	case channeltype.AIGC2D:
		return updateChannelAIGC2DBalance(channel)
	case channeltype.SiliconFlow:
		return updateChannelSiliconFlowBalance(channel)
	case channeltype.DeepSeek:
		return updateChannelDeepSeekBalance(channel)
	case channeltype.OpenRouter:
		return updateChannelOpenRouterBalance(channel)
	case channeltype.AIHubMix:
		return updateChannelAIHubMixBalance(channel)
	default:
		return 0, errors.New("尚未实现")
	}
	url := fmt.Sprintf("%s/v1/dashboard/billing/subscription", baseURL)

	body, err := GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	subscription := OpenAISubscriptionResponse{}
	err = json.Unmarshal(body, &subscription)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	startDate := fmt.Sprintf("%s-01", now.Format("2006-01"))
	endDate := now.Format("2006-01-02")
	if !subscription.HasPaymentMethod {
		startDate = now.AddDate(0, 0, -100).Format("2006-01-02")
	}
	url = fmt.Sprintf("%s/v1/dashboard/billing/usage?start_date=%s&end_date=%s", baseURL, startDate, endDate)
	body, err = GetResponseBody("GET", url, channel, GetAuthHeader(channel.Key))
	if err != nil {
		return 0, err
	}
	usage := OpenAIUsageResponse{}
	err = json.Unmarshal(body, &usage)
	if err != nil {
		return 0, err
	}
	balance := subscription.HardLimitUSD - usage.TotalUsage/100
	channel.UpdateBalance(balance)
	return balance, nil
}

func UpdateChannelBalance(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	channel, err := model.GetChannelById(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	balance, err := updateChannelBalance(channel)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"balance": balance,
	})
	return
}

func updateAllChannelsBalance() error {
	channels, err := model.GetAllChannels(0, 0, "all")
	if err != nil {
		return err
	}
	for _, channel := range channels {
		if channel.Status != model.ChannelStatusEnabled {
			continue
		}
		// TODO: support Azure
		if channel.Type != channeltype.OpenAI && channel.Type != channeltype.Custom {
			continue
		}
		balance, err := updateChannelBalance(channel)
		if err != nil {
			continue
		} else {
			// err is nil & balance <= 0 means quota is used up
			if balance <= 0 {
				monitor.DisableChannel(channel.Id, channel.Name, "余额不足")
			}
		}
		time.Sleep(config.RequestInterval)
	}
	return nil
}

func UpdateAllChannelsBalance(c *gin.Context) {
	//err := updateAllChannelsBalance()
	//if err != nil {
	//	c.JSON(http.StatusOK, gin.H{
	//		"success": false,
	//		"message": err.Error(),
	//	})
	//	return
	//}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
	})
	return
}

func AutomaticallyUpdateChannels(frequency int) {
	for {
		time.Sleep(time.Duration(frequency) * time.Minute)
		logger.SysLog("updating all channels")
		_ = updateAllChannelsBalance()
		logger.SysLog("channels update done")
	}
}
