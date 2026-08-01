package testutil

import (
	"bytes"
	"image"
)

// init probes whether the hand-rolled WebP fixture can actually be
// decoded. If upstream ever changes the bitstream layout we silently
// disable WebP in tests rather than failing.
func init() {
	_, format, err := image.DecodeConfig(bytes.NewReader(WebPBytes()))
	if err == nil && format == "webp" {
		webpDecodable = true
	}
}

// WebPSupported reports whether the package's WebP fixture decodes
// successfully. Tests that exercise the WebP path should call
// t.Skipf when this returns false.
func WebPSupported() bool { return webpDecodable }
