package payment

import (
	"fmt"
	"sync"

	"github.com/vgate-project/vgate-manager/internal/model"
)

// factory builds a Provider from the injected config source.
type factory func(ConfigSource) (Provider, error)

// Registry resolves a Provider by platform name. Providers register
// themselves (see the alipay package's Register) so this package has no
// compile-time dependency on any concrete gateway. Providers are built lazily
// and cached for the life of the process; each provider caches its own gateway
// client and rebuilds it when its credentials change.
type Registry struct {
	getConfig ConfigSource
	mu        sync.Mutex
	factories map[string]factory
	cache     map[string]Provider
}

// NewRegistry builds a Registry. getConfig is typically sysCfg.GetAll.
func NewRegistry(getConfig ConfigSource) *Registry {
	return &Registry{
		getConfig: getConfig,
		factories: make(map[string]factory),
		cache:     make(map[string]Provider),
	}
}

// Register associates a platform name with a Provider factory. Called by each
// gateway package's Register function.
func (r *Registry) Register(platform string, f factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories[platform] = f
}

// Get returns the Provider for platform, building and caching it on first use.
// An empty platform defaults to alipay. Unknown platforms return an error.
func (r *Registry) Get(platform string) (Provider, error) {
	if platform == "" {
		platform = model.OrderPlatformAlipay
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.cache[platform]; ok {
		return p, nil
	}
	f, ok := r.factories[platform]
	if !ok {
		return nil, fmt.Errorf("unsupported payment platform: %s", platform)
	}
	p, err := f(r.getConfig)
	if err != nil {
		return nil, err
	}
	r.cache[platform] = p
	return p, nil
}

// IsEnabled reports whether the admin's explicit toggle for platform is on.
// The key absent means enabled (for backward compatibility with deployments
// that only configured credentials and never set the toggle).
func (r *Registry) IsEnabled(platform string) bool {
	m, err := r.getConfig()
	if err != nil {
		return false
	}
	v, ok := m[PlatformEnabledKey(platform)]
	if !ok {
		return true // absent => enabled
	}
	return v == "true" || v == "1" || v == "yes"
}

// IsConfigured reports whether the provider for platform has the credentials
// it needs. Unregistered platforms are reported as not configured.
func (r *Registry) IsConfigured(platform string) bool {
	p, err := r.Get(platform)
	if err != nil {
		return false
	}
	if csp, ok := p.(ConfigStatusProvider); ok {
		return csp.IsConfigured()
	}
	return true
}

// IsAvailable reports whether platform can be used to collect a payment: it
// must be registered, enabled by the admin, and have its credentials present.
func (r *Registry) IsAvailable(platform string) (bool, error) {
	if r == nil {
		return false, nil
	}
	if platform == "" {
		platform = model.OrderPlatformAlipay
	}
	if _, err := r.Get(platform); err != nil {
		return false, err
	}
	return r.IsEnabled(platform) && r.IsConfigured(platform), nil
}

// List returns every registered payment platform with its label, mode, and
// availability (enabled && configured). The frontend uses this to render a
// payment-method picker.
func (r *Registry) List() []ChannelInfo {
	// Snapshot the registered platforms under the lock, then release it.
	// Get/IsConfigured re-acquire r.mu internally, so we must NOT hold the
	// lock while calling them (sync.Mutex is not reentrant — holding it here
	// deadlocked the previous implementation).
	r.mu.Lock()
	platforms := make([]string, 0, len(r.factories))
	for platform := range r.factories {
		platforms = append(platforms, platform)
	}
	r.mu.Unlock()

	out := make([]ChannelInfo, 0, len(platforms))
	for _, platform := range platforms {
		info := ChannelInfo{
			Platform: platform,
			Label:    PlatformLabel[platform],
			Enabled:  r.IsEnabled(platform),
		}
		info.Configured = r.IsConfigured(platform)
		if p, err := r.Get(platform); err == nil {
			info.Mode = p.Mode()
		}
		out = append(out, info)
	}
	return out
}

// platformPriority defines the stable preference order used when an order is
// created without an explicit platform: we fall through this list and pick the
// first channel that is both enabled and configured.
var platformPriority = []string{
	model.OrderPlatformAlipay,
	model.OrderPlatformWechat,
	model.OrderPlatformStripe,
	model.OrderPlatformPaypal,
	model.OrderPlatformApple,
}

// DefaultPlatform returns the first platform (in platformPriority order) that
// is both enabled and configured, falling back to alipay for backward
// compatibility. It is used when an order is created without an explicit
// platform selection.
func (r *Registry) DefaultPlatform() string {
	if r == nil {
		return model.OrderPlatformAlipay
	}
	for _, p := range platformPriority {
		if avail, err := r.IsAvailable(p); err == nil && avail {
			return p
		}
	}
	return model.OrderPlatformAlipay
}
