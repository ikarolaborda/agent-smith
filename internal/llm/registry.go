package llm

import (
	"errors"
	"fmt"
	"sync"
)

/*
Factory builds a Provider from raw configuration. The opaque cfg argument is
passed through from the application's config layer; each provider package
type-asserts (or unmarshals) it as needed.
*/
type Factory func(cfg any) (Provider, error)

/* ErrUnknownProvider is returned when New is called with an unregistered name. */
var ErrUnknownProvider = errors.New("llm: unknown provider")

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

/*
Register makes a provider factory available under name. Calling Register
twice with the same name panics, which is acceptable in init().
*/
func Register(name string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("llm: provider %q already registered", name))
	}
	registry[name] = f
}

/* New constructs the provider registered under name. */
func New(name string, cfg any) (Provider, error) {
	registryMu.RLock()
	f, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, name)
	}
	return f(cfg)
}

/* Names returns all registered provider names. Intended for diagnostics. */
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	return out
}
