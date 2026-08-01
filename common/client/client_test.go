package client

import (
	"testing"
)

func TestInit_CreatesHTTPClients(t *testing.T) {
	// Reset to nil before init
	HTTPClient = nil
	ImpatientHTTPClient = nil
	UserContentRequestHTTPClient = nil

	Init()

	if HTTPClient == nil {
		t.Fatal("HTTPClient should be initialized")
	}
	if ImpatientHTTPClient == nil {
		t.Fatal("ImpatientHTTPClient should be initialized")
	}
	if UserContentRequestHTTPClient == nil {
		t.Fatal("UserContentRequestHTTPClient should be initialized")
	}
}

func TestHTTPClient_Timeout(t *testing.T) {
	Init()
	if HTTPClient.Timeout != 0 {
		t.Logf("HTTPClient.Timeout = %v (may be set by config)", HTTPClient.Timeout)
	}
}

func TestImpatientHTTPClient_Timeout(t *testing.T) {
	Init()
	if ImpatientHTTPClient.Timeout == 0 {
		t.Error("ImpatientHTTPClient should have a timeout set")
	}
}
