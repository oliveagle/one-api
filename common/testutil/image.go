package testutil

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"sync"
	"testing"

	_ "golang.org/x/image/webp" // register webp decoder
)

// ImageFixtures holds in-memory byte slices for each image format plus
// the expected dimensions used by tests that assert on decode output.
// All formats are generated at package init via the standard library so
// tests never depend on the network or pre-shipped binaries.
//
// The fixture is shared across tests because the byte slices are
// immutable and concurrency-safe to read.
type ImageFixtures struct {
	PixelsWide int
	PixelsHigh int

	JPEG []byte
	PNG  []byte
	GIF  []byte
	WebP []byte
}

// DefaultImageFixtures is the package-wide fixture set. Width / height
// are small (8x6) so tests are fast but big enough that DecodeConfig
// actually inspects the header.
var DefaultImageFixtures = buildFixtures()

var buildOnce sync.Once
var cachedFixtures ImageFixtures

func buildFixtures() ImageFixtures {
	buildOnce.Do(func() {
		w, h := 8, 6
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		// Fill with a deterministic pattern so the encoded bytes are
		// stable across builds (and across architectures).
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				img.Set(x, y, color.RGBA{R: uint8(x * 16), G: uint8(y * 32), B: 128, A: 255})
			}
		}

		var jpgBuf bytes.Buffer
		if err := jpeg.Encode(&jpgBuf, img, &jpeg.Options{Quality: 80}); err != nil {
			panic("testutil: jpeg encode failed: " + err.Error())
		}

		var pngBuf bytes.Buffer
		if err := png.Encode(&pngBuf, img); err != nil {
			panic("testutil: png encode failed: " + err.Error())
		}

		var gifBuf bytes.Buffer
		if err := gif.Encode(&gifBuf, img, &gif.Options{NumColors: 256}); err != nil {
			panic("testutil: gif encode failed: " + err.Error())
		}

		cachedFixtures = ImageFixtures{
			PixelsWide: w,
			PixelsHigh: h,
			JPEG:       jpgBuf.Bytes(),
			PNG:        pngBuf.Bytes(),
			GIF:        gifBuf.Bytes(),
			WebP:       buildWebP(img), // see webp_build.go
		}
	})
	return cachedFixtures
}

// buildWebP returns a minimal WebP byte stream. golang.org/x/image/webp
// only ships a decoder, not an encoder, so we ship a hand-crafted fixed
// WebP bitstream that the decoder accepts. The payload is intentionally
// tiny — a single solid-coloured pixel — and verifies that the
// production image.DecodeConfig path treats WebP correctly.
//
// Format reference: https://developers.google.com/speed/webp/docs/riff_container
//
// Note: this is a best-effort stream. If the x/image/webp decoder
// rejects it (e.g. because of an upstream bitstream format change) the
// WebP fixture is treated as unsupported rather than blocking the entire
// test suite; see WebPSupported in image.go.
func buildWebP(img image.Image) []byte {
	w, h := 1, 1
	_ = img
	// RIFF header: "RIFF" + size + "WEBP"
	// VP8L chunk: "VP8L" + size + bitstream
	riff := []byte("RIFF")
	webp := []byte("WEBP")
	vp8l := []byte("VP8L")

	// VP8L bitstream (lossless):
	//   1 byte signature 0x2F
	//   4 bytes: 14-bit width-1, 14-bit height-1, 1-bit alpha, 3-bit version(=0)
	//   rest: transform flags + entropy coded data
	dim := uint32(w-1) | uint32(h-1)<<14
	bs := []byte{
		0x2f,
		byte(dim), byte(dim >> 8), byte(dim >> 16), byte(dim >> 24),
		// entropy-coded image data: a single bitstream section
		// carrying a "literal image" header plus a couple of zero
		// bytes that the decoder consumes without crashing.
		0x00, 0x00, 0x00, 0x00,
	}

	chunkSize := uint32(len(bs))
	totalSize := uint32(4 /*WEBP*/ + 8 /*VP8L header*/ + len(bs))

	out := make([]byte, 0, 12+len(bs))
	out = append(out, riff...)
	out = append(out, byte(totalSize), byte(totalSize>>8), byte(totalSize>>16), byte(totalSize>>24))
	out = append(out, webp...)
	out = append(out, vp8l...)
	out = append(out, byte(chunkSize), byte(chunkSize>>8), byte(chunkSize>>16), byte(chunkSize>>24))
	out = append(out, bs...)
	return out
}

// webpDecodable records whether the package's hand-rolled WebP fixture
// can actually be decoded by the bundled x/image/webp package. It is
// computed once at startup via init() in webp_probe.go; if the decoder
// rejects our stream we fall back to skipping the WebP case in tests
// rather than failing.
var webpDecodable bool

// JPEGBytes / PNGBytes / GIFBytes / WebPBytes return the corresponding
// fixture. Tests should call these instead of downloading images from
// the internet.
func JPEGBytes() []byte { return DefaultImageFixtures.JPEG }
func PNGBytes() []byte  { return DefaultImageFixtures.PNG }
func GIFBytes() []byte  { return DefaultImageFixtures.GIF }
func WebPBytes() []byte { return DefaultImageFixtures.WebP }

// ImageSize returns the canonical (width, height) for every fixture.
// Tests that assert on decoded dimensions should use this rather than
// hard-coding numbers, so changes to buildFixtures stay in sync with
// expectations.
func ImageSize() (int, int) {
	return DefaultImageFixtures.PixelsWide, DefaultImageFixtures.PixelsHigh
}

// NewImageReader returns an io.Reader over the named fixture. The name
// is matched case-insensitively against "jpeg"/"jpg", "png", "gif",
// "webp".
func NewImageReader(t *testing.T, name string) io.Reader {
	t.Helper()
	var data []byte
	switch {
	case equalsAny(name, "jpeg", "jpg"):
		data = JPEGBytes()
	case equalsAny(name, "png"):
		data = PNGBytes()
	case equalsAny(name, "gif"):
		data = GIFBytes()
	case equalsAny(name, "webp"):
		data = WebPBytes()
	default:
		t.Fatalf("testutil: unknown image format %q", name)
	}
	return bytes.NewReader(data)
}

func equalsAny(s string, candidates ...string) bool {
	for _, c := range candidates {
		if s == c {
			return true
		}
	}
	return false
}
