package meta

import (
	"net/http"
	"net/http/httptest"
)

// newRequest builds a minimal *http.Request with the given path and a single
// Authorization header. It exists so the meta package tests do not have to
// reach into the standard library's request builder in every test.
func newRequest(method, path, authHeader string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if authHeader != "" {
		r.Header.Set("Authorization", authHeader)
	}
	r.URL.RawQuery = ""
	return r
}
