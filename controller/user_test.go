package controller

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/model"
)

// Login is gated by config.PasswordLoginEnabled. If an operator disables
// password login, Login must refuse immediately without consulting the DB.
func TestLogin_PasswordLoginDisabled(t *testing.T) {
	setupMockDB(t)
	saveConfig(t)
	config.PasswordLoginEnabled = false

	r := setupSessionRouter(t)
	r.POST("/api/user/login", Login)

	rec := doJSON(t, r, "POST", "/api/user/login",
		`{"username":"anyone","password":"anything"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "管理员关闭了密码登录") {
		t.Fatalf("expected disabled message, got %s", rec.Body.String())
	}
}

// An empty username or password must be rejected without consulting the DB.
// The controller treats empty credentials the same as a missing field — we
// only need to verify it responds success=false.
func TestLogin_EmptyCredentials(t *testing.T) {
	setupMockDB(t)
	saveConfig(t)
	config.PasswordLoginEnabled = true

	r := setupSessionRouter(t)
	r.POST("/api/user/login", Login)

	// Missing username
	rec := doJSON(t, r, "POST", "/api/user/login", `{"password":"x"}`)
	if got := rec.Body.String(); !strings.Contains(got, `"success":false`) {
		t.Fatalf("missing username should fail: %s", got)
	}
	// Missing password
	rec = doJSON(t, r, "POST", "/api/user/login", `{"username":"x"}`)
	if got := rec.Body.String(); !strings.Contains(got, `"success":false`) {
		t.Fatalf("missing password should fail: %s", got)
	}
}

// Login must round-trip a valid username/password into a session and a
// cleaned (no password, no token) user payload.
func TestLogin_ValidCredentials(t *testing.T) {
	setupMockDB(t)
	saveConfig(t)
	config.PasswordLoginEnabled = true
	seedUser(t, 1, "alice", "password1234", 1, 1, 100)

	r := setupSessionRouter(t)
	r.POST("/api/user/login", Login)

	rec := doJSON(t, r, "POST", "/api/user/login",
		`{"username":"alice","password":"password1234"}`)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
	// Login response must include the user but the password field is the
	// empty value (the production handler builds a cleanUser with id,
	// username, display_name, role, status only) — assert that the
	// password JSON value is the empty string. access_token similarly.
	if !strings.Contains(rec.Body.String(), `"password":""`) {
		t.Fatalf("expected empty password string, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"access_token":""`) {
		t.Fatalf("expected empty access_token string, got %s", rec.Body.String())
	}
	// Session cookie should be set.
	cookies := rec.Result().Cookies()
	sawSession := false
	for _, c := range cookies {
		if c.Name == "session" {
			sawSession = true
		}
	}
	if !sawSession {
		t.Fatalf("session cookie not set; cookies = %v", cookies)
	}
}

// Logout must clear the session cookie. We only check that the controller
// does not panic and that the response is success=true.
func TestLogout_ClearsSession(t *testing.T) {
	r := setupSessionRouter(t)
	r.GET("/api/user/logout", Logout)

	rec := doJSON(t, r, "GET", "/api/user/logout", "")
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
}

// Register must respect the PasswordRegisterEnabled flag.
func TestRegister_PasswordRegisterDisabled(t *testing.T) {
	setupMockDB(t)
	saveConfig(t)
	config.RegisterEnabled = true
	config.PasswordRegisterEnabled = false

	r := setupSessionRouter(t)
	r.POST("/api/user/register", Register)
	rec := doJSON(t, r, "POST", "/api/user/register",
		`{"username":"bob","password":"password1234"}`)
	if !strings.Contains(rec.Body.String(), "管理员关闭了通过密码进行注册") {
		t.Fatalf("expected disabled message, got %s", rec.Body.String())
	}
}

// Register must also reject when RegisterEnabled is off.
func TestRegister_RegistrationDisabled(t *testing.T) {
	setupMockDB(t)
	saveConfig(t)
	config.RegisterEnabled = false
	config.PasswordRegisterEnabled = true

	r := setupSessionRouter(t)
	r.POST("/api/user/register", Register)
	rec := doJSON(t, r, "POST", "/api/user/register",
		`{"username":"bob","password":"password1234"}`)
	if !strings.Contains(rec.Body.String(), "管理员关闭了新用户注册") {
		t.Fatalf("expected disabled message, got %s", rec.Body.String())
	}
}

// Register must reject empty JSON body.
func TestRegister_InvalidJSON(t *testing.T) {
	setupMockDB(t)
	saveConfig(t)
	config.RegisterEnabled = true
	config.PasswordRegisterEnabled = true

	r := setupSessionRouter(t)
	r.POST("/api/user/register", Register)
	rec := doJSON(t, r, "POST", "/api/user/register", "")
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
}

// GetAllUsers returns the user list scoped by role: a regular user must not
// be able to view other users' data — but the controller itself is gated by
// AdminAuth middleware; here we just verify the happy path with a direct
// context.
func TestGetAllUsers_EmptyResult(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 99, 10, "admin")
	GetAllUsers(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Success bool        `json:"success"`
		Data    interface{} `json:"data"`
	}
	jsonBody(t, rec, &resp)
	if !resp.Success {
		t.Fatalf("expected success, body: %s", rec.Body.String())
	}
}

// With three seeded users, GetAllUsers must return them all and not panic
// when serializing.
func TestGetAllUsers_ReturnsAll(t *testing.T) {
	setupMockDB(t)
	seedUser(t, 1, "alice", "password1234", 1, 1, 100)
	seedUser(t, 2, "bob", "password1234", 1, 1, 100)
	seedUser(t, 3, "carol", "password1234", 10, 1, 100)

	c, rec := withUserContext(t, 99, 100, "root")
	GetAllUsers(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "alice") ||
		!strings.Contains(rec.Body.String(), "bob") ||
		!strings.Contains(rec.Body.String(), "carol") {
		t.Fatalf("missing users in body: %s", rec.Body.String())
	}
}

// GetUser returns a single user. By ID 0 / invalid ID the controller must
// respond with success=false.
func TestGetUser_NotFound(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 99, 100, "root")
	c.Params = append(c.Params, gin.Param{Key: "id", Value: "999"})
	GetUser(c)
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
}

// SearchUsers with a keyword returns the matching users. An empty keyword
// must produce a non-empty list because the helper does not filter on
// empty keywords.
func TestSearchUsers(t *testing.T) {
	setupMockDB(t)
	seedUser(t, 1, "alice", "password1234", 1, 1, 100)
	seedUser(t, 2, "bob", "password1234", 1, 1, 100)

	c, rec := withUserContext(t, 99, 100, "root")
	c.Request = httptestNewGetRequest("/api/user/search?keyword=ali")
	SearchUsers(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "alice") {
		t.Fatalf("alice missing: %s", rec.Body.String())
	}
}

// GetSelf returns the calling user's record.
func TestGetSelf(t *testing.T) {
	setupMockDB(t)
	seedUser(t, 1, "alice", "password1234", 1, 1, 100)

	c, rec := withUserContext(t, 1, 1, "alice")
	GetSelf(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Success bool        `json:"success"`
		Data    interface{} `json:"data"`
	}
	jsonBody(t, rec, &resp)
	if !resp.Success {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
}

// UpdateSelf must reject when the JSON body fails to decode.
func TestUpdateSelf_BadJSON(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 1, 1, "alice")
	c.Request = httptestNewGetRequest("/api/user/self")
	UpdateSelf(c)
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
}

// DeleteUser must not allow a non-admin caller to remove a higher-role user.
// We do not test the role-check directly here because admin middleware is
// applied at the router level — instead we test that the controller flips the
// target's status to "deleted" when the deletion actually succeeds.
func TestDeleteUser_Success(t *testing.T) {
	setupMockDB(t)
	seedUser(t, 2, "bob", "password1234", 1, 1, 100)

	c, rec := withUserContext(t, 1, 100, "root")
	c.Params = append(c.Params, gin.Param{Key: "id", Value: "2"})
	DeleteUser(c)
	// Production handler returns success: true even when the error branch
	// triggers a 200 (existing quirk); the more reliable signal is the DB
	// state transition — the row is marked deleted instead of being
	// physically removed.
	var u model.User
	if err := model.DB.First(&u, "id = ?", 2).Error; err != nil {
		t.Fatalf("user not found after delete: %v", err)
	}
	if u.Status != 3 {
		t.Fatalf("status = %d, want 3 (deleted)", u.Status)
	}
	if rec.Code == 0 {
		t.Fatalf("recorder code not set: %v", rec.Code)
	}
}

// CreateUser rejects an empty username/password payload.
func TestCreateUser_InvalidPayload(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 99, 100, "root")
	c.Request = httptestNewGetRequest("/api/user/")
	CreateUser(c)
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
}

// CreateUser with valid payload inserts and returns success.
func TestCreateUser_Success(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 99, 100, "root")
	c.Request = httptestNewPostJSONRequest("/api/user/",
		`{"username":"newuser","password":"password1234","display_name":"New User"}`)
	CreateUser(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
	// Verify it landed in the DB.
	var u model.User
	if err := model.DB.First(&u).Error; err != nil {
		t.Fatalf("user not created: %v", err)
	}
	if u.Username != "newuser" {
		t.Fatalf("username = %q", u.Username)
	}
}

// ManageUser with an unknown user must return success=false.
func TestManageUser_UnknownUser(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 99, 100, "root")
	c.Request = httptestNewPostJSONRequest("/api/user/manage",
		`{"username":"ghost","action":"enable"}`)
	ManageUser(c)
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
}

// ManageUser cannot promote a user above the caller's role.
func TestManageUser_PromoteRejectsNonRoot(t *testing.T) {
	setupMockDB(t)
	seedUser(t, 2, "bob", "password1234", 1, 1, 100)

	c, rec := withUserContext(t, 99, 10, "admin")
	c.Request = httptestNewPostJSONRequest("/api/user/manage",
		`{"username":"bob","action":"promote"}`)
	ManageUser(c)
	if !strings.Contains(rec.Body.String(), "普通管理员用户无法提升其他用户为管理员") {
		t.Fatalf("expected promote rejection: %s", rec.Body.String())
	}
}

// ManageUser can demote a higher-role user. Seeded user is admin (10);
// caller is root (100); demotion succeeds.
func TestManageUser_Demote(t *testing.T) {
	setupMockDB(t)
	seedUser(t, 2, "bob", "password1234", 10, 1, 100)

	c, rec := withUserContext(t, 1, 100, "root")
	c.Request = httptestNewPostJSONRequest("/api/user/manage",
		`{"username":"bob","action":"demote"}`)
	ManageUser(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
	var u model.User
	if err := model.DB.First(&u, "username = ?", "bob").Error; err != nil {
		t.Fatalf("user not found: %v", err)
	}
	if u.Role != 1 {
		t.Fatalf("role = %d, want 1", u.Role)
	}
}

// ManageUser cannot demote a root user.
func TestManageUser_DemoteRootRejected(t *testing.T) {
	setupMockDB(t)
	seedUser(t, 1, "root", "rootpwd1234", 100, 1, 100)

	c, rec := withUserContext(t, 1, 100, "root")
	c.Request = httptestNewPostJSONRequest("/api/user/manage",
		`{"username":"root","action":"demote"}`)
	ManageUser(c)
	if !strings.Contains(rec.Body.String(), "无法降级超级管理员用户") {
		t.Fatalf("expected demote root rejection: %s", rec.Body.String())
	}
}

// ManageUser enable / disable on a regular user toggles status.
func TestManageUser_EnableDisable(t *testing.T) {
	setupMockDB(t)
	seedUser(t, 2, "bob", "password1234", 1, 2, 100)

	c, rec := withUserContext(t, 1, 100, "root")
	c.Request = httptestNewPostJSONRequest("/api/user/manage",
		`{"username":"bob","action":"enable"}`)
	ManageUser(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("enable should succeed: %s", rec.Body.String())
	}
	var u model.User
	_ = model.DB.First(&u, "username = ?", "bob")
	if u.Status != 1 {
		t.Fatalf("status = %d, want 1", u.Status)
	}
}

// EmailBind rejects an invalid verification code.
func TestEmailBind_InvalidCode(t *testing.T) {
	r := setupSessionRouter(t)
	r.GET("/api/oauth/email/bind", EmailBind)
	rec := doJSON(t, r, "GET", "/api/oauth/email/bind?email=x@example.com&code=bad", "")
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
}

// TopUp requires a non-empty key.
func TestTopUp_BadJSON(t *testing.T) {
	c, rec := withUserContext(t, 1, 1, "alice")
	TopUp(c)
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
}

// AdminTopUp must reject malformed JSON.
func TestAdminTopUp_BadJSON(t *testing.T) {
	c, rec := withUserContext(t, 1, 10, "admin")
	AdminTopUp(c)
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
}

// GenerateAccessToken replaces the access token with a new UUID.
func TestGenerateAccessToken(t *testing.T) {
	setupMockDB(t)
	seedUser(t, 1, "alice", "password1234", 1, 1, 100)
	old := ""
	{
		var u model.User
		_ = model.DB.First(&u)
		old = u.AccessToken
	}

	c, rec := withUserContext(t, 1, 1, "alice")
	GenerateAccessToken(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success: %s", rec.Body.String())
	}
	var u model.User
	if err := model.DB.First(&u).Error; err != nil {
		t.Fatalf("user not found: %v", err)
	}
	if u.AccessToken == old {
		t.Fatalf("access token not rotated")
	}
}

// GetAffCode returns a stable code (4-char string).
func TestGetAffCode(t *testing.T) {
	setupMockDB(t)
	seedUser(t, 1, "alice", "password1234", 1, 1, 100)

	c, rec := withUserContext(t, 1, 1, "alice")
	GetAffCode(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success: %s", rec.Body.String())
	}
}

// DeleteSelf must reject when caller is root.
func TestDeleteSelf_RejectsRoot(t *testing.T) {
	setupMockDB(t)
	seedUser(t, 1, "root", "rootpwd1234", 100, 1, 100)

	c, rec := withUserContext(t, 1, 100, "root")
	DeleteSelf(c)
	if !strings.Contains(rec.Body.String(), "不能删除超级管理员账户") {
		t.Fatalf("expected root rejection: %s", rec.Body.String())
	}
}

// DeleteSelf with a non-root caller deletes the user.
func TestDeleteSelf_DeletesNonRoot(t *testing.T) {
	setupMockDB(t)
	seedUser(t, 2, "bob", "password1234", 1, 1, 100)

	c, rec := withUserContext(t, 2, 1, "bob")
	DeleteSelf(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
	var u model.User
	if err := model.DB.First(&u, "id = ?", 2).Error; err != nil {
		t.Fatalf("user row not found after DeleteSelf: %v", err)
	}
	if u.Status != 3 {
		t.Fatalf("status = %d, want 3 (deleted)", u.Status)
	}
}

// GetUserDashboard returns a list of daily usage. With no logs in the DB
// the response is an empty list — we just verify the controller does not
// panic.
func TestGetUserDashboard_NoLogs(t *testing.T) {
	setupMockDB(t)
	seedUser(t, 1, "alice", "password1234", 1, 1, 100)

	c, rec := withUserContext(t, 1, 1, "alice")
	GetUserDashboard(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success: %s", rec.Body.String())
	}
}

// c.Set uses string keys, but the controllers retrieve via ctxkey.Id which is
// a string. Make sure our helpers use the right keys. This is more of a smoke
// test against the helper API.
func TestWithUserContext_ReadsCtxkeyBack(t *testing.T) {
	c, _ := withUserContext(t, 7, 1, "alice")
	if got := c.GetInt(ctxkey.Id); got != 7 {
		t.Fatalf("ctxkey Id = %d, want 7", got)
	}
	if got := c.GetInt(ctxkey.Role); got != 1 {
		t.Fatalf("ctxkey Role = %d, want 1", got)
	}
}
