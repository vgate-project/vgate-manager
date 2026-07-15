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
