package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/common/testutil"
	"github.com/songquanpeng/one-api/model"
)

// setupSessionRouter installs an in-process cookie session store so handlers
// that call sessions.Default(c) — Login, SetupLogin, GitHub OAuth state check,
// Logout, etc. — can read and write session values during the test.
//
// The cookie store matches the production wiring in main.go. Without this
// helper the session.Default calls panic with "session middleware not loaded".
func setupSessionRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	store := cookie.NewStore([]byte("test-secret-key-for-unit-tests-only-32-bytes"))
	r.Use(sessions.Sessions("session", store))
	return r
}

// setupMockDB wires a fresh SQLite DB into the package-level model.DB /
// model.LOG_DB so the controllers can call into the data layer. Tests that
// touch controllers touching the DB must call this before exercising handlers.
func setupMockDB(t *testing.T) {
	t.Helper()
	testutil.DisableRedis(t)
	gormDB := testutil.NewMockDBForCommon(t)
	model.DB = gormDB
	model.LOG_DB = gormDB
}

// withUserContext returns a gin.Context that has the auth middleware's
// ctxkey.Id / Role / Username values pre-populated, so handlers that read
// these values via c.GetInt(ctxkey.Id) work without going through the
// middleware chain. We do not install the session middleware here — only
// the keys that the controller functions themselves read.
func withUserContext(t *testing.T, userId int, role int, username string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Set(ctxkey.Id, userId)
	c.Set(ctxkey.Role, role)
	c.Set(ctxkey.Username, username)
	return c, rec
}

// httptestNewGetRequest returns a *http.Request for GET /path that handlers
// reading JSON bodies can ignore — used when the handler ignores request body.
func httptestNewGetRequest(path string) *http.Request {
	return httptest.NewRequest(http.MethodGet, path, nil)
}

// httptestNewPostJSONRequest returns a *http.Request with a JSON content-type
// header set. Callers pass the body inline.
func httptestNewPostJSONRequest(path, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// doJSON posts the body to a controller handler mounted on path. It returns
// the recorder so the caller can assert on status / body.
func doJSON(t *testing.T, r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// seedUser inserts a user with hashed password and returns it. The caller
// chooses the Id so test assertions can refer to a stable primary key.
func seedUser(t *testing.T, id int, username, password string, role, status int, quota int64) *model.User {
	t.Helper()
	hashed, err := common.Password2Hash(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := &model.User{
		Id:          id,
		Username:    username,
		Password:    hashed,
		DisplayName: username,
		Role:        role,
		Status:      status,
		Quota:       quota,
		UsedQuota:   0,
		AccessToken: "test-access-token-" + username,
		Group:       "default",
		AffCode:     "code" + username,
	}
	if err := model.DB.Create(user).Error; err != nil {
		t.Fatalf("seed user %q: %v", username, err)
	}
	return user
}

// jsonBody decodes the recorder body into v. Fails the test if the body is
// not JSON.
func jsonBody(t *testing.T, rec *httptest.ResponseRecorder, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, rec.Body.String())
	}
}

// rawBody returns the recorder body as a string, failing the test if it is
// not JSON-decodable.
func rawBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("non-JSON body: %s", rec.Body.String())
	}
	return rec.Body.String()
}

// saveConfig snapshots every config field the tests touch so they can be
// restored on cleanup. Tests in this file flip booleans like
// config.PasswordLoginEnabled; forgetting to restore them pollutes later
// tests in the same process (Go runs tests serially within a package by
// default unless t.Parallel is opted into).
func saveConfig(t *testing.T) {
	t.Helper()
	orig := struct {
		PasswordLoginEnabled          bool
		PasswordRegisterEnabled       bool
		RegisterEnabled               bool
		EmailVerificationEnabled      bool
		EmailDomainRestrictionEnabled bool
		GitHubOAuthEnabled            bool
		OidcEnabled                   bool
		WeChatAuthEnabled             bool
		TurnstileCheckEnabled         bool
		DisplayInCurrencyEnabled      bool
		DisplayTokenStatEnabled       bool
		QuotaPerUnit                  float64
		ItemsPerPage                  int
		ServerAddress                 string
	}{
		PasswordLoginEnabled:          config.PasswordLoginEnabled,
		PasswordRegisterEnabled:       config.PasswordRegisterEnabled,
		RegisterEnabled:               config.RegisterEnabled,
		EmailVerificationEnabled:      config.EmailVerificationEnabled,
		EmailDomainRestrictionEnabled: config.EmailDomainRestrictionEnabled,
		GitHubOAuthEnabled:            config.GitHubOAuthEnabled,
		OidcEnabled:                   config.OidcEnabled,
		WeChatAuthEnabled:             config.WeChatAuthEnabled,
		TurnstileCheckEnabled:         config.TurnstileCheckEnabled,
		DisplayInCurrencyEnabled:      config.DisplayInCurrencyEnabled,
		DisplayTokenStatEnabled:       config.DisplayTokenStatEnabled,
		QuotaPerUnit:                  config.QuotaPerUnit,
		ItemsPerPage:                  config.ItemsPerPage,
		ServerAddress:                 config.ServerAddress,
	}
	t.Cleanup(func() {
		config.PasswordLoginEnabled = orig.PasswordLoginEnabled
		config.PasswordRegisterEnabled = orig.PasswordRegisterEnabled
		config.RegisterEnabled = orig.RegisterEnabled
		config.EmailVerificationEnabled = orig.EmailVerificationEnabled
		config.EmailDomainRestrictionEnabled = orig.EmailDomainRestrictionEnabled
		config.GitHubOAuthEnabled = orig.GitHubOAuthEnabled
		config.OidcEnabled = orig.OidcEnabled
		config.WeChatAuthEnabled = orig.WeChatAuthEnabled
		config.TurnstileCheckEnabled = orig.TurnstileCheckEnabled
		config.DisplayInCurrencyEnabled = orig.DisplayInCurrencyEnabled
		config.DisplayTokenStatEnabled = orig.DisplayTokenStatEnabled
		config.QuotaPerUnit = orig.QuotaPerUnit
		config.ItemsPerPage = orig.ItemsPerPage
		config.ServerAddress = orig.ServerAddress
	})
}

// ensureOptionMap is called by tests that exercise handlers that read
// config.OptionMap. InitOptionMap requires model.DB to be set, so this
// must run after setupMockDB.
func ensureOptionMap() {
	config.OptionMapRWMutex.Lock()
	for _, k := range []string{"Notice", "About", "HomePageContent", "Footer", "SystemName", "Logo"} {
		if _, ok := config.OptionMap[k]; !ok {
			config.OptionMap[k] = ""
		}
	}
	if config.OptionMap == nil {
		config.OptionMap = map[string]string{}
	}
	config.OptionMapRWMutex.Unlock()
}

// withCtxResp is used for handler functions that don't use sessions — they
// just need a *gin.Context with the right ctxkey values. The returned
// recorder holds the JSON response.
func withCtxResp(t *testing.T, userId int, role int, username string) (*gin.Context, *httptest.ResponseRecorder) {
	return withUserContext(t, userId, role, username)
}

// ginParam is a tiny helper for handlers that read c.Params.
func ginParam(key, value string) gin.Param {
	return gin.Param{Key: key, Value: value}
}
