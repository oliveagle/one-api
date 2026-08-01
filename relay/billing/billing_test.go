package billing

import (
	"context"
	"testing"
	"time"

	"github.com/songquanpeng/one-api/common/testutil"
	"github.com/songquanpeng/one-api/model"
	"gorm.io/gorm"
)

// setupMockDB wires a fresh SQLite DB into the package-level model.DB /
// model.LOG_DB so the billing helpers can call into the data layer.
//
// Tests in this file must NOT run in parallel because each test rebinds the
// shared package-level DB handle.
func setupMockDB(t *testing.T) {
	t.Helper()
	testutil.DisableRedis(t)
	db := testutil.NewMockDBForCommon(t)
	model.DB = db
	model.LOG_DB = db
}

// seedUserAndToken inserts a fresh user with quota and a matching token so
// the billing path can resolve FKs without ErrRecordNotFound.
func seedUserAndToken(t *testing.T, db *gorm.DB, tokenQuota int64) (userId, tokenId int) {
	t.Helper()
	user := model.User{
		Id:          1,
		Username:    "billing-test",
		Password:    "test-test-test-test",
		Role:        model.RoleCommonUser,
		Status:      model.UserStatusEnabled,
		DisplayName: "Test",
		AccessToken: "test-access-token",
		Quota:       1000000,
		Group:       "default",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	token := model.Token{
		Id:           1,
		UserId:       user.Id,
		Key:          "sk-test-billing-token",
		Status:       model.TokenStatusEnabled,
		Name:         "billing-test-token",
		CreatedTime:  time.Now().Unix(),
		AccessedTime: time.Now().Unix(),
		ExpiredTime:  -1,
		RemainQuota:  tokenQuota,
	}
	if err := db.Create(&token).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}
	return user.Id, token.Id
}

// Pre-consume path: a 0 quota must be a silent no-op. The function spawns
// no goroutine, writes to no DB, and returns immediately. If this regressed
// to calling PostConsumeTokenQuota unconditionally, every "trusted user"
// path would incur an extra DB roundtrip.
func TestReturnPreConsumedQuota_ZeroIsNoop(t *testing.T) {
	setupMockDB(t)
	start := time.Now()
	ReturnPreConsumedQuota(context.Background(), 0, 42)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("zero-quota path should not block; took %v", elapsed)
	}
}

// With a non-zero pre-consumed quota the function dispatches a goroutine
// that calls PostConsumeTokenQuota against the test DB. Caller must not
// block.
func TestReturnPreConsumedQuota_NonZeroDispatchesAsync(t *testing.T) {
	setupMockDB(t)
	_, _ = seedUserAndToken(t, model.DB, 1000)

	done := make(chan struct{})
	go func() {
		defer close(done)
		ReturnPreConsumedQuota(context.Background(), 100, 1)
	}()
	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("ReturnPreConsumedQuota blocked the caller")
	}
}

// PostConsumeQuota: zero total quota should still post the remaining delta
// but skip the log/usage recording branches.
func TestPostConsumeQuota_TotalZero(t *testing.T) {
	setupMockDB(t)
	seedUserAndToken(t, model.DB, 1000)

	PostConsumeQuota(context.Background(),
		/*tokenId*/ 1,
		/*quotaDelta*/ -100, // returning 100 quota to user
		/*totalQuota*/ 0,
		/*userId*/ 1,
		/*channelId*/ 0,
		/*modelRatio*/ 1,
		/*groupRatio*/ 1,
		/*modelName*/ "gpt-4o-mini",
		/*tokenName*/ "billing-test-token",
	)
}

// PostConsumeQuota: a positive total quota must record a consume log row.
func TestPostConsumeQuota_RecordsUsage(t *testing.T) {
	setupMockDB(t)
	userId, tokenId := seedUserAndToken(t, model.DB, 100000)

	PostConsumeQuota(context.Background(),
		tokenId,
		/*quotaDelta*/ 100,
		/*totalQuota*/ 250,
		userId,
		/*channelId*/ 0,
		/*modelRatio*/ 1,
		/*groupRatio*/ 1,
		/*modelName*/ "gpt-4o-mini",
		/*tokenName*/ "billing-test-token",
	)

	var logCount int64
	if err := model.LOG_DB.Model(&model.Log{}).Count(&logCount).Error; err != nil {
		t.Fatalf("count logs: %v", err)
	}
	if logCount == 0 {
		t.Fatal("expected at least one consume log row")
	}
}

// PostConsumeQuota: negative totalQuota is permitted by the production
// code (it logs an error and skips the bookkeeping). We just assert it
// returns without panicking.
func TestPostConsumeQuota_NegativeTotalLogsError(t *testing.T) {
	setupMockDB(t)
	userId, tokenId := seedUserAndToken(t, model.DB, 100000)

	PostConsumeQuota(context.Background(),
		tokenId,
		/*quotaDelta*/ -50,
		/*totalQuota*/ -10, // negative — invalid
		userId,
		/*channelId*/ 0,
		/*modelRatio*/ 1,
		/*groupRatio*/ 1,
		/*modelName*/ "gpt-4o-mini",
		/*tokenName*/ "billing-test-token",
	)
}
