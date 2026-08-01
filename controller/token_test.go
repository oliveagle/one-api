package controller

import (
	"strings"
	"testing"
	"time"

	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/common/helper"
	"github.com/songquanpeng/one-api/model"
)

// seedToken inserts a token row tied to userId.
func seedToken(t *testing.T, id, userId int, key, name string, status int, remain int64, unlimited bool) *model.Token {
	t.Helper()
	tok := &model.Token{
		Id:             id,
		UserId:         userId,
		Key:            key,
		Name:           name,
		Status:         status,
		CreatedTime:    helper.GetTimestamp(),
		AccessedTime:   helper.GetTimestamp(),
		ExpiredTime:    -1,
		RemainQuota:    remain,
		UnlimitedQuota: unlimited,
	}
	if err := model.DB.Create(tok).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}
	return tok
}

// GetAllTokens returns zero results with no tokens seeded.
func TestGetAllTokens_Empty(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 1, 1, "alice")
	GetAllTokens(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
}

// GetAllTokens filters by userId.
func TestGetAllTokens_FiltersByUser(t *testing.T) {
	setupMockDB(t)
	seedToken(t, 1, 1, "sk-token-alice", "alice-token", model.TokenStatusEnabled, 100, false)
	seedToken(t, 2, 2, "sk-token-bob", "bob-token", model.TokenStatusEnabled, 100, false)

	c, rec := withUserContext(t, 1, 1, "alice")
	GetAllTokens(c)
	if !strings.Contains(rec.Body.String(), "alice-token") {
		t.Fatalf("missing alice's token: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "bob-token") {
		t.Fatalf("bleed: bob's token visible to alice: %s", rec.Body.String())
	}
}

// SearchTokens filters by user + keyword prefix.
func TestSearchTokens(t *testing.T) {
	setupMockDB(t)
	seedToken(t, 1, 1, "sk-1", "alpha", model.TokenStatusEnabled, 100, false)
	seedToken(t, 2, 1, "sk-2", "alphabet", model.TokenStatusEnabled, 100, false)
	seedToken(t, 3, 1, "sk-3", "gamma", model.TokenStatusEnabled, 100, false)

	c, rec := withUserContext(t, 1, 1, "alice")
	c.Request = httptestNewGetRequest("/api/token/search?keyword=alph")
	SearchTokens(c)
	body := rec.Body.String()
	if !strings.Contains(body, "alpha") || !strings.Contains(body, "alphabet") {
		t.Fatalf("missing match: %s", body)
	}
	if strings.Contains(body, "gamma") {
		t.Fatalf("keyword filter leaked: %s", body)
	}
}

// GetToken returns the requested token when owned by the caller.
func TestGetToken_Found(t *testing.T) {
	setupMockDB(t)
	seedToken(t, 1, 1, "sk-1", "alpha", model.TokenStatusEnabled, 100, false)

	c, rec := withUserContext(t, 1, 1, "alice")
	c.Params = append(c.Params, ginParam("id", "1"))
	GetToken(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "alpha") {
		t.Fatalf("missing name in body: %s", rec.Body.String())
	}
}

// GetToken with the wrong id format yields a parse failure.
func TestGetToken_BadID(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 1, 1, "alice")
	c.Params = append(c.Params, ginParam("id", "not-a-number"))
	GetToken(c)
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
}

// GetTokenStatus returns a credit_summary with the expected fields.
func TestGetTokenStatus(t *testing.T) {
	setupMockDB(t)
	seedToken(t, 1, 1, "sk-1", "alpha", model.TokenStatusEnabled, 5000, false)

	c, rec := withUserContext(t, 1, 1, "alice")
	c.Set(ctxkey.TokenId, 1)
	GetTokenStatus(c)
	body := rec.Body.String()
	if !strings.Contains(body, `"object":"credit_summary"`) {
		t.Fatalf("missing credit_summary object: %s", body)
	}
	if !strings.Contains(body, `"total_available":5000`) {
		t.Fatalf("missing total_available: %s", body)
	}
}

// AddToken with no JSON body fails fast.
func TestAddToken_BadJSON(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 1, 1, "alice")
	AddToken(c)
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
}

// AddToken with an oversized name (>30 chars) fails with a parameter error.
func TestAddToken_TooLongName(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 1, 1, "alice")
	c.Request = httptestNewPostJSONRequest("/api/token/",
		`{"name":"`+strings.Repeat("a", 31)+`"}`)
	AddToken(c)
	if !strings.Contains(rec.Body.String(), "令牌名称过长") {
		t.Fatalf("expected name length error: %s", rec.Body.String())
	}
}

// AddToken with a bad subnet string fails with the subnet validation error.
func TestAddToken_BadSubnet(t *testing.T) {
	setupMockDB(t)
	badSubnet := "not-a-subnet"
	c, rec := withUserContext(t, 1, 1, "alice")
	c.Request = httptestNewPostJSONRequest("/api/token/",
		`{"name":"okname","subnet":"`+badSubnet+`"}`)
	AddToken(c)
	if !strings.Contains(rec.Body.String(), "无效的网段") {
		t.Fatalf("expected subnet error: %s", rec.Body.String())
	}
}

// AddToken with valid payload inserts a row and returns success.
func TestAddToken_Success(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 1, 1, "alice")
	c.Request = httptestNewPostJSONRequest("/api/token/", `{"name":"alpha","remain_quota":100}`)
	AddToken(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
	var tok model.Token
	if err := model.DB.First(&tok, "name = ?", "alpha").Error; err != nil {
		t.Fatalf("token not created: %v", err)
	}
	if len(tok.Key) != 48 {
		t.Fatalf("key length = %d, want 48", len(tok.Key))
	}
	if tok.RemainQuota != 100 {
		t.Fatalf("remain_quota = %d, want 100", tok.RemainQuota)
	}
}

// DeleteToken with an unknown id returns success=false from the model.
func TestDeleteToken_NotFound(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 1, 1, "alice")
	c.Params = append(c.Params, ginParam("id", "999"))
	DeleteToken(c)
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
}

// DeleteToken with a real token deletes it.
func TestDeleteToken_Success(t *testing.T) {
	setupMockDB(t)
	seedToken(t, 1, 1, "sk-1", "alpha", model.TokenStatusEnabled, 100, false)

	c, rec := withUserContext(t, 1, 1, "alice")
	c.Params = append(c.Params, ginParam("id", "1"))
	DeleteToken(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
	var tok model.Token
	if err := model.DB.First(&tok, "id = ?", 1).Error; err == nil {
		t.Fatalf("token still exists after delete")
	}
}

// UpdateToken with bad JSON fails fast.
func TestUpdateToken_BadJSON(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 1, 1, "alice")
	UpdateToken(c)
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
}

// UpdateToken with an unknown id returns success=false.
func TestUpdateToken_NotFound(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 1, 1, "alice")
	c.Request = httptestNewPostJSONRequest("/api/token/", `{"id":999,"name":"x"}`)
	UpdateToken(c)
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
}

// UpdateToken with status_only=true on an exhausted token succeeds only when
// quota is unlimited. With a depleted quota it must reject the re-enable.
func TestUpdateToken_ExpiredTokenCannotEnable(t *testing.T) {
	setupMockDB(t)
	now := helper.GetTimestamp()
	tok := seedToken(t, 1, 1, "sk-1", "alpha", model.TokenStatusExpired, 0, false)
	tok.ExpiredTime = now - 100 // expired in the past
	if err := model.DB.Save(tok).Error; err != nil {
		t.Fatalf("save token: %v", err)
	}

	c, rec := withUserContext(t, 1, 1, "alice")
	c.Request = httptestNewPostJSONRequest("/api/token/",
		`{"id":1,"status":1}`)
	UpdateToken(c)
	if !strings.Contains(rec.Body.String(), "令牌已过期") {
		t.Fatalf("expected expired error: %s", rec.Body.String())
	}
}

// UpdateToken status_only flips a status field without rewriting other fields.
func TestUpdateToken_StatusOnly(t *testing.T) {
	setupMockDB(t)
	seedToken(t, 1, 1, "sk-1", "alpha", model.TokenStatusEnabled, 5000, true)

	c, rec := withUserContext(t, 1, 1, "alice")
	c.Request = httptestNewPostJSONRequest("/api/token/?status_only=1",
		`{"id":1,"status":2}`)
	UpdateToken(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success: %s", rec.Body.String())
	}
	var tok model.Token
	_ = model.DB.First(&tok, "id = ?", 1)
	if tok.Status != 2 {
		t.Fatalf("status = %d, want 2", tok.Status)
	}
	// status_only means name/remain_quota unchanged
	if tok.RemainQuota != 5000 {
		t.Fatalf("remain_quota = %d, want 5000", tok.RemainQuota)
	}
	if tok.Name != "alpha" {
		t.Fatalf("name = %q, want alpha", tok.Name)
	}
}

// UpdateToken in default mode updates name + quota.
func TestUpdateToken_FullUpdate(t *testing.T) {
	setupMockDB(t)
	seedToken(t, 1, 1, "sk-1", "alpha", model.TokenStatusEnabled, 5000, false)

	c, rec := withUserContext(t, 1, 1, "alice")
	c.Request = httptestNewPostJSONRequest("/api/token/",
		`{"id":1,"name":"beta","remain_quota":3000,"unlimited_quota":false,"expired_time":-1}`)
	UpdateToken(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
	var tok model.Token
	_ = model.DB.First(&tok, "id = ?", 1)
	if tok.Name != "beta" {
		t.Fatalf("name = %q, want beta", tok.Name)
	}
	if tok.RemainQuota != 3000 {
		t.Fatalf("remain_quota = %d, want 3000", tok.RemainQuota)
	}
}

// validateToken must accept a valid CIDR subnet and reject malformed ones.
// We exercise the helper indirectly through AddToken.
func TestValidateToken_ViaAddToken(t *testing.T) {
	setupMockDB(t)
	t.Run("valid subnet ok", func(t *testing.T) {
		c, rec := withUserContext(t, 1, 1, "alice")
		c.Request = httptestNewPostJSONRequest("/api/token/",
			`{"name":"valid","subnet":"10.0.0.0/24"}`)
		AddToken(c)
		if !strings.Contains(rec.Body.String(), `"success":true`) {
			t.Fatalf("expected success: %s", rec.Body.String())
		}
	})
	t.Run("empty subnet ok", func(t *testing.T) {
		c, rec := withUserContext(t, 1, 1, "alice")
		c.Request = httptestNewPostJSONRequest("/api/token/",
			`{"name":"empty","subnet":""}`)
		AddToken(c)
		if !strings.Contains(rec.Body.String(), `"success":true`) {
			t.Fatalf("expected success: %s", rec.Body.String())
		}
	})
}

// The token CreatedTime / AccessedTime are populated by AddToken.
func TestAddToken_PopulatesTimestamps(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 1, 1, "alice")
	c.Request = httptestNewPostJSONRequest("/api/token/", `{"name":"alpha"}`)
	AddToken(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success: %s", rec.Body.String())
	}
	var tok model.Token
	_ = model.DB.First(&tok, "name = ?", "alpha")
	now := helper.GetTimestamp()
	if tok.CreatedTime < now-5 || tok.CreatedTime > now+5 {
		t.Fatalf("created_time = %d, expected near %d", tok.CreatedTime, now)
	}
	_ = time.Now() // keep time imported regardless
}
