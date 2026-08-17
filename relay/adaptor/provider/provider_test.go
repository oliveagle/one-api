package provider

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/relay/meta"
)

func newValidDescriptor(channelType int) Descriptor {
	return Descriptor{
		ChannelType: channelType,
		Name:        "test-provider",
		Models:      []string{"model-a"},
		RequestURL: func(m *meta.Meta) (string, error) {
			return "https://example.com/v1/chat", nil
		},
		SetupHeader: func(c *gin.Context, req *http.Request, m *meta.Meta) error {
			req.Header.Set("Authorization", "Bearer test")
			return nil
		},
	}
}

func TestDescriptorValidate(t *testing.T) {
	cases := map[string]Descriptor{
		"zero channel type": {Name: "x", Models: []string{"m"}, RequestURL: func(*meta.Meta) (string, error) { return "", nil }, SetupHeader: func(*gin.Context, *http.Request, *meta.Meta) error { return nil }},
		"empty name":        {ChannelType: 1, Models: []string{"m"}, RequestURL: func(*meta.Meta) (string, error) { return "", nil }, SetupHeader: func(*gin.Context, *http.Request, *meta.Meta) error { return nil }},
		"empty models":      {ChannelType: 1, Name: "x", RequestURL: func(*meta.Meta) (string, error) { return "", nil }, SetupHeader: func(*gin.Context, *http.Request, *meta.Meta) error { return nil }},
		"nil RequestURL":    {ChannelType: 1, Name: "x", Models: []string{"m"}, SetupHeader: func(*gin.Context, *http.Request, *meta.Meta) error { return nil }},
		"nil SetupHeader":   {ChannelType: 1, Name: "x", Models: []string{"m"}, RequestURL: func(*meta.Meta) (string, error) { return "", nil }},
	}
	for label, d := range cases {
		if err := d.validate(); err == nil {
			t.Fatalf("%s: expected validation error", label)
		}
	}
	if err := newValidDescriptor(1).validate(); err != nil {
		t.Fatalf("valid descriptor rejected: %v", err)
	}
}

func TestRegistryRegister(t *testing.T) {
	r := NewRegistry()
	d := newValidDescriptor(1)
	if err := r.Register(d); err != nil {
		t.Fatalf("first register failed: %v", err)
	}
	if err := r.Register(d); err == nil {
		t.Fatalf("duplicate register should fail")
	}
	if err := r.Register(Descriptor{}); err == nil {
		t.Fatalf("zero descriptor register should fail")
	}
}

func TestRegistryMustRegisterPanics(t *testing.T) {
	r := NewRegistry()
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("MustRegister did not panic on invalid descriptor")
		}
	}()
	r.MustRegister(Descriptor{})
}

func TestRegistryFreezePreventsMutation(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(newValidDescriptor(1))
	r.Freeze()
	if err := r.Register(newValidDescriptor(2)); err == nil {
		t.Fatalf("register after freeze should fail")
	}
	if err := r.SetFallback(newValidDescriptor(2)); err == nil {
		t.Fatalf("set fallback after freeze should fail")
	}
}

func TestRegistryGetAndMustGet(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(newValidDescriptor(1))
	if _, ok := r.Get(1); !ok {
		t.Fatalf("Get(1) should succeed")
	}
	if _, ok := r.Get(2); ok {
		t.Fatalf("Get(2) should fail")
	}
	if got := r.MustGet(1).ChannelType; got != 1 {
		t.Fatalf("MustGet(1).ChannelType = %d, want 1", got)
	}
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("MustGet(2) should panic without fallback")
		}
	}()
	r.MustGet(2)
}

func TestRegistryFallback(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(newValidDescriptor(1))
	fallback := newValidDescriptor(99)
	fallback.Name = "fallback"
	if err := r.SetFallback(fallback); err != nil {
		t.Fatalf("set fallback failed: %v", err)
	}
	if got := r.MustGet(2).Name; got != "fallback" {
		t.Fatalf("MustGet(2) fallback name = %q, want %q", got, "fallback")
	}
}

func TestRegistryChannelTypes(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(newValidDescriptor(3))
	r.MustRegister(newValidDescriptor(7))
	got := r.ChannelTypes()
	if len(got) != 2 {
		t.Fatalf("ChannelTypes length = %d, want 2", len(got))
	}
	has := func(v int) bool {
		for _, x := range got {
			if x == v {
				return true
			}
		}
		return false
	}
	if !has(3) || !has(7) {
		t.Fatalf("ChannelTypes = %v, want {3,7}", got)
	}
}

func TestRegistryConcurrentReads(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(newValidDescriptor(1))
	r.Freeze()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if _, ok := r.Get(1); !ok {
					t.Errorf("concurrent Get(1) failed")
				}
			}
		}()
	}
	wg.Wait()
}

func TestDescriptorRequestURLAndHeader(t *testing.T) {
	d := newValidDescriptor(1)
	m := &meta.Meta{}
	url, err := d.RequestURL(m)
	if err != nil || !strings.HasPrefix(url, "https://") {
		t.Fatalf("RequestURL = %q, err = %v", url, err)
	}
	req, _ := http.NewRequest(http.MethodPost, url, nil)
	if err := d.SetupHeader(nil, req, m); err != nil {
		t.Fatalf("SetupHeader failed: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer test" {
		t.Fatalf("Authorization header = %q, want %q", got, "Bearer test")
	}
}

func TestDescriptorRequestURLPropagatesError(t *testing.T) {
	d := newValidDescriptor(1)
	d.RequestURL = func(*meta.Meta) (string, error) { return "", errors.New("boom") }
	if _, err := d.RequestURL(&meta.Meta{}); err == nil {
		t.Fatalf("RequestURL error propagation failed")
	}
}
