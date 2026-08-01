package controller

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/model"
)

func TestGetSubscription_NoToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupMockDB(t)

	// Disable token stat so it uses user quota path
	oldDisplayTokenStat := config.DisplayTokenStatEnabled
	config.DisplayTokenStatEnabled = false
	t.Cleanup(func() { config.DisplayTokenStatEnabled = oldDisplayTokenStat })

	user := seedUser(t, 1, "sub-test-user", "password", model.RoleCommonUser, model.UserStatusEnabled, 500000)

	c, rec := withUserContext(t, user.Id, user.Role, user.Username)
	GetSubscription(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp OpenAISubscriptionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Object != "billing_subscription" {
		t.Errorf("object = %q, want billing_subscription", resp.Object)
	}
}

func TestGetSubscription_WithToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupMockDB(t)

	oldDisplayTokenStat := config.DisplayTokenStatEnabled
	config.DisplayTokenStatEnabled = true
	t.Cleanup(func() { config.DisplayTokenStatEnabled = oldDisplayTokenStat })

	user := seedUser(t, 1, "sub-token-test", "password", model.RoleCommonUser, model.UserStatusEnabled, 1000000)
	tok := seedBillingToken(t, user.Id, 5000)

	c, rec := withUserContext(t, user.Id, user.Role, user.Username)
	c.Set(ctxkey.TokenId, tok.Id)
	GetSubscription(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp OpenAISubscriptionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Object != "billing_subscription" {
		t.Errorf("object = %q, want billing_subscription", resp.Object)
	}
}

func TestGetUsage_NoToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupMockDB(t)

	oldDisplayTokenStat := config.DisplayTokenStatEnabled
	config.DisplayTokenStatEnabled = false
	t.Cleanup(func() { config.DisplayTokenStatEnabled = oldDisplayTokenStat })

	user := seedUser(t, 1, "usage-test-user", "password", model.RoleCommonUser, model.UserStatusEnabled, 500000)

	c, rec := withUserContext(t, user.Id, user.Role, user.Username)
	GetUsage(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp OpenAIUsageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Object != "list" {
		t.Errorf("object = %q, want list", resp.Object)
	}
}

func TestGetUsage_WithToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupMockDB(t)

	oldDisplayTokenStat := config.DisplayTokenStatEnabled
	config.DisplayTokenStatEnabled = true
	t.Cleanup(func() { config.DisplayTokenStatEnabled = oldDisplayTokenStat })

	user := seedUser(t, 1, "usage-token-test", "password", model.RoleCommonUser, model.UserStatusEnabled, 1000000)
	tok := seedBillingToken(t, user.Id, 5000)

	c, rec := withUserContext(t, user.Id, user.Role, user.Username)
	c.Set(ctxkey.TokenId, tok.Id)
	GetUsage(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp OpenAIUsageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Object != "list" {
		t.Errorf("object = %q, want list", resp.Object)
	}
}

func TestOpenAISubscriptionResponse_JSON(t *testing.T) {
	resp := OpenAISubscriptionResponse{
		Object:             "billing_subscription",
		HasPaymentMethod:   true,
		SoftLimitUSD:       100.0,
		HardLimitUSD:       100.0,
		SystemHardLimitUSD: 100.0,
		AccessUntil:        0,
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back OpenAISubscriptionResponse
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Object != "billing_subscription" {
		t.Errorf("object = %q", back.Object)
	}
}

func TestOpenAIUsageResponse_JSON(t *testing.T) {
	resp := OpenAIUsageResponse{
		Object:     "list",
		TotalUsage: 500.0,
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back OpenAIUsageResponse
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Object != "list" {
		t.Errorf("object = %q", back.Object)
	}
}

// seedBillingToken creates a token for billing tests with a specific remain quota.
func seedBillingToken(t *testing.T, userId int, remainQuota int64) *model.Token {
	t.Helper()
	token := &model.Token{
		UserId:         userId,
		Key:            "sk-billing-test-" + t.Name(),
		Status:         model.TokenStatusEnabled,
		Name:           "billing-test-token",
		RemainQuota:    remainQuota,
		UnlimitedQuota: false,
	}
	if err := model.DB.Create(token).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}
	return token
}
