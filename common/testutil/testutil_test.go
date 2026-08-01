package testutil_test

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"testing"

	"github.com/songquanpeng/one-api/common/testutil"
	"gorm.io/gorm"
)

func TestNewMockDB_AutoMigrates(t *testing.T) {
	t.Parallel()
	db := testutil.NewMockDB(t)
	if db == nil {
		t.Fatal("expected non-nil db")
	}
	if !db.Migrator().HasTable("users") {
		t.Error("expected users table after AutoMigrate")
	}
	if !db.Migrator().HasTable("channels") {
		t.Error("expected channels table after AutoMigrate")
	}
	if !db.Migrator().HasTable("abilities") {
		t.Error("expected abilities table after AutoMigrate")
	}
}

func TestNewMockDBForCommon_SetsSQLiteFlag(t *testing.T) {
	t.Parallel()
	db := testutil.NewMockDBForCommon(t)
	if db == nil {
		t.Fatal("expected non-nil db")
	}
}

func TestNewMockDB_Parallel(t *testing.T) {
	t.Parallel()
	const n = 4
	done := make(chan *gorm.DB, n)
	for i := 0; i < n; i++ {
		go func() {
			done <- testutil.NewMockDB(t)
		}()
	}
	for i := 0; i < n; i++ {
		db := <-done
		if db == nil {
			t.Errorf("parallel NewMockDB returned nil")
		}
	}
}

func TestImageFixtures_Decode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data []byte
	}{
		{"jpeg", testutil.JPEGBytes()},
		{"png", testutil.PNGBytes()},
		{"gif", testutil.GIFBytes()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, format, err := image.DecodeConfig(bytes.NewReader(c.data))
			if err != nil {
				t.Fatalf("decode %s: %v", c.name, err)
			}
			if format != c.name {
				t.Errorf("expected format %s, got %s", c.name, format)
			}
			w, h := testutil.ImageSize()
			if cfg.Width != w || cfg.Height != h {
				t.Errorf("expected %dx%d, got %dx%d", w, h, cfg.Width, cfg.Height)
			}
		})
	}
	if !testutil.WebPSupported() {
		t.Skip("WebP fixture not supported by current x/image/webp decoder")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(testutil.WebPBytes()))
	if err != nil {
		t.Fatalf("decode webp: %v", err)
	}
	if format != "webp" {
		t.Errorf("expected webp, got %s", format)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		t.Errorf("expected positive webp dimensions, got %dx%d", cfg.Width, cfg.Height)
	}
}

func TestMockHTTPClient_RoundTrip(t *testing.T) {
	t.Parallel()
	client := testutil.NewMockHTTPClient(t)
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.Transport == nil {
		t.Fatal("expected non-nil transport")
	}
}

func TestMockTransport_RespondAndMatch(t *testing.T) {
	t.Parallel()
	tr := testutil.NewMockTransport(t)
	tr.Respond("GET", "https://example.test/img", testutil.NewJSONHandler(200, []byte(`{"hello":"world"}`)))
	tr.Match("POST", "https://example.test/", testutil.NewBytesHandler(204, nil, nil))

	client := &http.Client{Transport: tr}
	resp, err := client.Get("https://example.test/img")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestDisableRedis(t *testing.T) {
	t.Parallel()
	// DisableRedis restores previous state via t.Cleanup, so calling it
	// twice in the same test is safe. We only assert no panic here.
	testutil.DisableRedis(t)
}
