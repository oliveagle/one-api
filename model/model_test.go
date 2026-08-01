package model

import (
	"testing"
	"time"

	"github.com/songquanpeng/one-api/common"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupMockDB wires a fresh SQLite DB into the package-level model.DB.
func setupMockDB(t *testing.T) {
	t.Helper()
	common.UsingSQLite = true
	common.UsingMySQL = false
	common.UsingPostgreSQL = false

	db, err := gorm.Open(
		sqlite.Open(":memory:?_busy_timeout=5000&_pragma=foreign_keys(1)"),
		&gorm.Config{
			Logger:                                   logger.Default.LogMode(logger.Silent),
			DisableForeignKeyConstraintWhenMigrating: true,
			PrepareStmt:                              true,
		},
	)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := AutoMigrateAll(db); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	DB = db
	LOG_DB = db
}

func seedUser(t *testing.T) *User {
	t.Helper()
	user := &User{
		Id:          1,
		Username:    "testuser",
		Password:    "hashed-password",
		DisplayName: "Test User",
		Role:        RoleCommonUser,
		Status:      UserStatusEnabled,
		Quota:       1000000,
		UsedQuota:   0,
		AccessToken: "test-access-token-abc",
		Group:       "default",
		AffCode:     "aff-test",
	}
	if err := DB.Create(user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return user
}

func seedToken(t *testing.T, userId int) *Token {
	t.Helper()
	token := &Token{
		Id:             1,
		UserId:         userId,
		Key:            "sk-test-token-key-12345",
		Status:         TokenStatusEnabled,
		Name:           "test-token",
		CreatedTime:    time.Now().Unix(),
		AccessedTime:   time.Now().Unix(),
		ExpiredTime:    -1,
		RemainQuota:    500000,
		UnlimitedQuota: false,
	}
	if err := DB.Create(token).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}
	return token
}

func seedChannel(t *testing.T) *Channel {
	t.Helper()
	ch := &Channel{
		Id:          1,
		Type:        1,
		Key:         "sk-channel-key",
		Status:      ChannelStatusEnabled,
		Name:        "test-channel",
		Models:      "gpt-4o,gpt-4o-mini",
		Group:       "default",
		UsedQuota:   0,
		CreatedTime: time.Now().Unix(),
	}
	if err := DB.Create(ch).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	return ch
}

func TestGetMaxUserId_NoUsers(t *testing.T) {
	setupMockDB(t)
	id := GetMaxUserId()
	if id != 0 {
		t.Errorf("GetMaxUserId with no users = %d, want 0", id)
	}
}

func TestGetMaxUserId_WithUsers(t *testing.T) {
	setupMockDB(t)
	seedUser(t)
	id := GetMaxUserId()
	if id != 1 {
		t.Errorf("GetMaxUserId = %d, want 1", id)
	}
}

func TestGetAllUsers(t *testing.T) {
	setupMockDB(t)
	seedUser(t)

	users, err := GetAllUsers(0, 10, "id")
	if err != nil {
		t.Fatalf("GetAllUsers: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("GetAllUsers returned %d users, want 1", len(users))
	}
	if users[0].Username != "testuser" {
		t.Errorf("username = %q, want testuser", users[0].Username)
	}
}

func TestSearchUsers(t *testing.T) {
	setupMockDB(t)
	seedUser(t)

	users, err := SearchUsers("test")
	if err != nil {
		t.Fatalf("SearchUsers: %v", err)
	}
	if len(users) == 0 {
		t.Fatal("SearchUsers returned no results")
	}
}

func TestGetUserById(t *testing.T) {
	setupMockDB(t)
	seedUser(t)

	user, err := GetUserById(1, true)
	if err != nil {
		t.Fatalf("GetUserById: %v", err)
	}
	if user.Username != "testuser" {
		t.Errorf("username = %q, want testuser", user.Username)
	}
}

func TestTokenCRUD(t *testing.T) {
	setupMockDB(t)
	user := seedUser(t)
	token := seedToken(t, user.Id)

	got, err := GetTokenById(token.Id)
	if err != nil {
		t.Fatalf("GetTokenById: %v", err)
	}
	if got.Key != token.Key {
		t.Errorf("token key = %q, want %q", got.Key, token.Key)
	}

	tokens, err := GetAllUserTokens(user.Id, 0, 10, "id")
	if err != nil {
		t.Fatalf("GetAllUserTokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("expected 1 token, got %d", len(tokens))
	}
}

func TestTokenInsertAndDelete(t *testing.T) {
	setupMockDB(t)
	user := seedUser(t)

	token := &Token{
		UserId:         user.Id,
		Key:            "sk-new-token-key",
		Status:         TokenStatusEnabled,
		Name:           "new-token",
		CreatedTime:    time.Now().Unix(),
		ExpiredTime:    -1,
		RemainQuota:    1000,
		UnlimitedQuota: false,
	}
	err := token.Insert()
	if err != nil {
		t.Fatalf("Insert token: %v", err)
	}
	if token.Id == 0 {
		t.Fatal("token.Id should be set after insert")
	}

	err = token.Delete()
	if err != nil {
		t.Fatalf("Delete token: %v", err)
	}
}

func TestChannelCRUD(t *testing.T) {
	setupMockDB(t)
	ch := seedChannel(t)

	got, err := GetChannelById(ch.Id, true)
	if err != nil {
		t.Fatalf("GetChannelById: %v", err)
	}
	if got.Name != ch.Name {
		t.Errorf("channel name = %q, want %q", got.Name, ch.Name)
	}

	channels, err := GetAllChannels(0, 10, "")
	if err != nil {
		t.Fatalf("GetAllChannels: %v", err)
	}
	if len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
}

func TestSearchChannels(t *testing.T) {
	setupMockDB(t)
	seedChannel(t)

	channels, err := SearchChannels("test")
	if err != nil {
		t.Fatalf("SearchChannels: %v", err)
	}
	if len(channels) == 0 {
		t.Fatal("SearchChannels returned no results")
	}
}

func TestChannelInsertAndDelete(t *testing.T) {
	setupMockDB(t)

	ch := &Channel{
		Type:        1,
		Key:         "sk-new-channel",
		Status:      ChannelStatusEnabled,
		Name:        "new-channel",
		Models:      "gpt-4o",
		Group:       "default",
		CreatedTime: time.Now().Unix(),
	}
	err := ch.Insert()
	if err != nil {
		t.Fatalf("Insert channel: %v", err)
	}
	if ch.Id == 0 {
		t.Fatal("channel.Id should be set after insert")
	}

	err = ch.Delete()
	if err != nil {
		t.Fatalf("Delete channel: %v", err)
	}
}

func TestAbilityAddAndDelete(t *testing.T) {
	setupMockDB(t)
	ch := seedChannel(t)

	err := ch.AddAbilities()
	if err != nil {
		t.Fatalf("AddAbilities: %v", err)
	}

	var count int64
	DB.Model(&Ability{}).Where("channel_id = ?", ch.Id).Count(&count)
	if count == 0 {
		t.Fatal("no abilities created")
	}

	err = ch.DeleteAbilities()
	if err != nil {
		t.Fatalf("DeleteAbilities: %v", err)
	}

	DB.Model(&Ability{}).Where("channel_id = ?", ch.Id).Count(&count)
	if count != 0 {
		t.Fatalf("expected 0 abilities after delete, got %d", count)
	}
}

func TestChannelUpdate(t *testing.T) {
	setupMockDB(t)
	ch := seedChannel(t)

	ch.Name = "updated-channel"
	err := ch.Update()
	if err != nil {
		t.Fatalf("Update channel: %v", err)
	}

	got, err := GetChannelById(ch.Id, true)
	if err != nil {
		t.Fatalf("GetChannelById: %v", err)
	}
	if got.Name != "updated-channel" {
		t.Errorf("channel name = %q, want updated-channel", got.Name)
	}
}

func TestUserDelete(t *testing.T) {
	setupMockDB(t)
	user := seedUser(t)

	err := user.Delete()
	if err != nil {
		t.Fatalf("Delete user: %v", err)
	}

	got, err := GetUserById(user.Id, true)
	if err != nil {
		t.Fatalf("GetUserById: %v", err)
	}
	if got.Status != UserStatusDeleted {
		t.Errorf("user status = %d, want %d (deleted)", got.Status, UserStatusDeleted)
	}
}
