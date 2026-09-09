package adapter

import (
	"fmt"
	"sync"
)

type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

func NewRegistry() *Registry {
	return &Registry{
		adapters: make(map[string]Adapter),
	}
}

func (r *Registry) Register(a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[a.Type()] = a
}

func (r *Registry) Get(adapterType string) (Adapter, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[adapterType]
	if !ok {
		return nil, fmt.Errorf("adapter type '%s' not found", adapterType)
	}
	return a, nil
}

// Count returns the number of registered adapters.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.adapters)
}

// Len returns the number of registered adapters.
func (r *Registry) Len() int {
	return r.Count()
}
