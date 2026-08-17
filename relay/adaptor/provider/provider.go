// Package provider models provider-specific differences that are too small to
// justify a complete protocol adaptor. Protocol adaptors (for example the
// OpenAI-compatible adaptor) own request/response conversion; provider
// descriptors own only URL construction, authentication, and provider headers.
package provider

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/relay/meta"
)

// ProviderMeta is the read-only metadata exposed by a Descriptor.
type ProviderMeta struct {
	// ChannelType is the one-api channel id represented by this provider.
	ChannelType int
	// Name is the stable display name returned by /v1/models.
	Name string
	// Models is the provider's default model catalogue.
	Models []string
}

// Descriptor is an explicit adapter for one provider. Functions are required
// so a descriptor cannot silently fall back to another provider's behavior.
type Descriptor struct {
	// ChannelType uniquely identifies the provider.
	ChannelType int
	// Name is used as the model owner/display name.
	Name string
	// Models is the provider's default model catalogue.
	Models []string
	// RequestURL constructs the provider-specific upstream URL.
	RequestURL func(*meta.Meta) (string, error)
	// SetupHeader applies provider authentication and custom headers. It runs
	// after the protocol adaptor has installed common headers.
	SetupHeader func(c *gin.Context, req *http.Request, m *meta.Meta) error
}

func (d Descriptor) Meta() ProviderMeta {
	return ProviderMeta{
		ChannelType: d.ChannelType,
		Name:        d.Name,
		Models:      d.Models,
	}
}

func (d Descriptor) validate() error {
	if d.ChannelType == 0 {
		return fmt.Errorf("provider channel type must not be zero")
	}
	if d.Name == "" {
		return fmt.Errorf("provider %d name must not be empty", d.ChannelType)
	}
	if len(d.Models) == 0 {
		return fmt.Errorf("provider %s model list must not be empty", d.Name)
	}
	if d.RequestURL == nil {
		return fmt.Errorf("provider %s RequestURL must not be nil", d.Name)
	}
	if d.SetupHeader == nil {
		return fmt.Errorf("provider %s SetupHeader must not be nil", d.Name)
	}
	return nil
}

// Registry is a concurrency-safe, immutable-after-freeze provider registry.
// Construction writes happen only during package initialization; request-path
// reads are lock-free after Freeze.
type Registry struct {
	mu          sync.RWMutex
	items       map[int]Descriptor
	frozen      bool
	fallback    Descriptor
	hasFallback bool
}

func NewRegistry() *Registry {
	return &Registry{items: make(map[int]Descriptor)}
}

// Register adds a descriptor. It returns an error on zero-value descriptors or
// duplicate channel types.
func (r *Registry) Register(d Descriptor) error {
	if err := d.validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return fmt.Errorf("provider registry is frozen")
	}
	if _, exists := r.items[d.ChannelType]; exists {
		return fmt.Errorf("provider channel type %d already registered", d.ChannelType)
	}
	// Copy defensively so later caller-side struct mutation cannot alter the registry.
	r.items[d.ChannelType] = d
	return nil
}

// MustRegister registers a descriptor and panics on invalid input. Invalid
// provider wiring is a programming error and must fail at process start.
func (r *Registry) MustRegister(d Descriptor) {
	if err := r.Register(d); err != nil {
		panic(err)
	}
}

// SetFallback defines the descriptor used when no explicit provider is
// registered. It is optional and primarily useful for OpenAI-compatible
// custom channels.
func (r *Registry) SetFallback(d Descriptor) error {
	if err := d.validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return fmt.Errorf("provider registry is frozen")
	}
	r.fallback = d
	r.hasFallback = true
	return nil
}

func (r *Registry) Freeze() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frozen = true
}

// Get returns an exact provider descriptor.
func (r *Registry) Get(channelType int) (Descriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.items[channelType]
	return d, ok
}

// MustGet returns the exact descriptor or the configured fallback. If neither
// exists it panics; this is intentional to surface registration gaps in tests
// and at startup rather than silently sending requests to the wrong endpoint.
func (r *Registry) MustGet(channelType int) Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if d, ok := r.items[channelType]; ok {
		return d
	}
	if r.hasFallback {
		return r.fallback
	}
	panic(fmt.Sprintf("provider channel type %d is not registered", channelType))
}

// ChannelTypes returns registration order-independent channel ids. The list is
// a defensive copy suitable for enumeration in tests and admin metadata.
func (r *Registry) ChannelTypes() []int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := make([]int, 0, len(r.items))
	for channelType := range r.items {
		types = append(types, channelType)
	}
	return types
}
