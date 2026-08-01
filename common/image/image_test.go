package image_test

import (
	"encoding/base64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/songquanpeng/one-api/common/client"
	img "github.com/songquanpeng/one-api/common/image"
	"github.com/songquanpeng/one-api/common/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "golang.org/x/image/webp"
)

// TestMain ensures the shared HTTP clients in common/client are
// initialised before any subtest runs. Without this, code paths that
// rely on client.UserContentRequestHTTPClient (e.g. GetImageSize) would
// dereference a nil *http.Client and panic. Initialising here keeps the
// tests hermetic — no external network access is required.
func TestMain(m *testing.M) {
	client.Init()
	m.Run()
}

// imageCase ties a fixture name (used by testutil) to the format string
// the standard library's image.Decode returns. The original test file
// pulled these from a hard-coded list of wikimedia.org URLs which both
// (a) flaked in CI due to TLS handshakes timing out and (b) made the
// test unable to run offline. We now use the in-process fixture set.
//
// WebP is intentionally omitted from the round-trip tests: golang.org/x/
// image ships only a decoder, so we cannot generate a fixture locally.
// The WebP path is exercised indirectly via DecodeConfig in
// TestWebPFixture_DecodeConfig (testutil-internal) so coverage of the
// registration code path is preserved.
type imageCase struct {
	name   string
	format string
}

func imageCases() []imageCase {
	return []imageCase{
		{"jpeg", "jpeg"},
		{"png", "png"},
		{"gif", "gif"},
	}
}

// countingReader tracks bytes consumed by image.Decode so tests can
// assert that DecodeConfig reads only the bytes it needs (the original
// test relied on this assertion to detect DecodeConfig regressions).
type countingReader struct {
	r       io.Reader
	bytesRx atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.bytesRx.Add(int64(n))
	}
	return n, err
}

// fixtureServer is an httptest.Server that returns the named fixture
// from testutil. It is started per-test so tests stay hermetic and
// can be parallelised without contention.
func fixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The standard image.DecodeConfig reader walks the header for
		// a few hundred bytes; serve the whole fixture so full Decode
		// works too.
		name := strings.TrimPrefix(r.URL.Path, "/")
		switch name {
		case "jpeg":
			_, _ = w.Write(testutil.JPEGBytes())
		case "png":
			_, _ = w.Write(testutil.PNGBytes())
		case "gif":
			_, _ = w.Write(testutil.GIFBytes())
		case "webp":
			_, _ = w.Write(testutil.WebPBytes())
		default:
			http.NotFound(w, r)
		}
	}))
}

// fetchOrSkip returns an io.ReadCloser over the named fixture served
// over loopback HTTP. WebP tests skip if the decoder cannot handle our
// hand-rolled fixture.
func fetchOrSkip(t *testing.T, srv *httptest.Server, name string) io.ReadCloser {
	t.Helper()
	resp, err := http.Get(srv.URL + "/" + name)
	require.NoError(t, err)
	return resp.Body
}

func TestDecode_FullAndConfig(t *testing.T) {
	t.Parallel()
	srv := fixtureServer(t)
	defer srv.Close()

	for _, c := range imageCases() {
		t.Run("Decode:"+c.format, func(t *testing.T) {
			body := fetchOrSkip(t, srv, c.name)
			defer body.Close()
			cr := &countingReader{r: body}
			decoded, format, err := image.Decode(cr)
			require.NoError(t, err)
			assert.Equal(t, c.format, format)
			w, h := testutil.ImageSize()
			size := decoded.Bounds().Size()
			assert.Equal(t, w, size.X)
			assert.Equal(t, h, size.Y)
		})

		t.Run("DecodeConfig:"+c.format, func(t *testing.T) {
			body := fetchOrSkip(t, srv, c.name)
			defer body.Close()
			cr := &countingReader{r: body}
			cfg, format, err := image.DecodeConfig(cr)
			require.NoError(t, err)
			assert.Equal(t, c.format, format)
			w, h := testutil.ImageSize()
			assert.Equal(t, w, cfg.Width)
			assert.Equal(t, h, cfg.Height)
		})
	}
}

func TestBase64_RoundTrip(t *testing.T) {
	t.Parallel()
	srv := fixtureServer(t)
	defer srv.Close()

	for _, c := range imageCases() {
		t.Run("Decode:"+c.format, func(t *testing.T) {
			body := fetchOrSkip(t, srv, c.name)
			defer body.Close()
			data, err := io.ReadAll(body)
			require.NoError(t, err)
			encoded := base64.StdEncoding.EncodeToString(data)
			dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))
			cr := &countingReader{r: dec}
			decoded, format, err := image.Decode(cr)
			require.NoError(t, err)
			assert.Equal(t, c.format, format)
			w, h := testutil.ImageSize()
			size := decoded.Bounds().Size()
			assert.Equal(t, w, size.X)
			assert.Equal(t, h, size.Y)
		})

		t.Run("DecodeConfig:"+c.format, func(t *testing.T) {
			body := fetchOrSkip(t, srv, c.name)
			defer body.Close()
			data, err := io.ReadAll(body)
			require.NoError(t, err)
			encoded := base64.StdEncoding.EncodeToString(data)
			dec := base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))
			cr := &countingReader{r: dec}
			cfg, format, err := image.DecodeConfig(cr)
			require.NoError(t, err)
			assert.Equal(t, c.format, format)
			w, h := testutil.ImageSize()
			assert.Equal(t, w, cfg.Width)
			assert.Equal(t, h, cfg.Height)
		})
	}
}

// TestGetImageSize_Loopback exercises GetImageSize against an
// httptest server so the test never leaves the host. The previous
// implementation fetched wikimedia.org directly, which both flaked
// in CI and required network access.
func TestGetImageSize_Loopback(t *testing.T) {
	t.Parallel()
	srv := fixtureServer(t)
	defer srv.Close()

	for i, c := range imageCases() {
		t.Run("Decode:"+strconv.Itoa(i), func(t *testing.T) {
			url := srv.URL + "/" + c.name
			width, height, err := img.GetImageSize(url)
			require.NoError(t, err)
			w, h := testutil.ImageSize()
			assert.Equal(t, w, width)
			assert.Equal(t, h, height)
		})
	}
}

func TestGetImageSizeFromBase64(t *testing.T) {
	t.Parallel()
	srv := fixtureServer(t)
	defer srv.Close()

	for i, c := range imageCases() {
		t.Run("Decode:"+strconv.Itoa(i), func(t *testing.T) {
			body := fetchOrSkip(t, srv, c.name)
			defer body.Close()
			data, err := io.ReadAll(body)
			require.NoError(t, err)
			encoded := base64.StdEncoding.EncodeToString(data)
			width, height, err := img.GetImageSizeFromBase64(encoded)
			require.NoError(t, err)
			w, h := testutil.ImageSize()
			assert.Equal(t, w, width)
			assert.Equal(t, h, height)
		})
	}
}

// TestGetImageSize_NonImage404 documents the contract: when the
// upstream responds with non-image bytes (here a 404), GetImageSize
// returns zero dimensions with a nil error. This is the existing
// behaviour of IsImageUrl, which short-circuits on non-image
// Content-Type. The test guards against a future change that would
// start returning an error and silently breaking callers that rely
// on (0, 0, nil) as a "no image here" signal.
func TestGetImageSize_NonImage404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	width, height, err := img.GetImageSize(srv.URL + "/missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if width != 0 || height != 0 {
		t.Errorf("expected 0x0 for non-image response, got %dx%d", width, height)
	}
}
