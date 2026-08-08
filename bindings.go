package modelcatalog

import "sync"

// threadBinding records the model and provider a thread is bound to for its
// lifetime. An empty model or provider means that half of the binding has not
// been observed yet.
type threadBinding struct {
	model    string
	provider string
}

const defaultThreadBindingsLimit = 4096

// providerResolver is the slice of the model catalog a binding needs to map
// models to providers and infer a provider's unique model.
type providerResolver interface {
	providerFor(model string) string
	uniqueModelForProvider(provider string) (string, modelSpec, bool)
}

// threadBindings keeps the model/provider binding of every live thread. The
// binding map is capped so a long-running app-server session cannot grow
// without bound; the oldest binding is evicted first. Eviction only weakens
// the local provider-conflict check for threads not seen in a very long time.
type threadBindings struct {
	mu       sync.RWMutex
	bindings map[string]threadBinding
	ring     []string
	next     int
	limit    int
}

func newThreadBindings(limit int) *threadBindings {
	if limit < 1 {
		limit = 1
	}
	return &threadBindings{
		bindings: make(map[string]threadBinding),
		ring:     make([]string, limit),
		limit:    limit,
	}
}

// set records a thread's model/provider binding, merging non-empty fields
// into any existing binding.
func (b *threadBindings) set(threadID, model, provider string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	binding := b.bindings[threadID]
	if model != "" {
		binding.model = model
	}
	if provider != "" {
		binding.provider = provider
	}
	if _, exists := b.bindings[threadID]; exists {
		b.bindings[threadID] = binding
		return
	}
	if len(b.bindings) >= b.limit {
		if oldest := b.ring[b.next]; oldest != "" {
			delete(b.bindings, oldest)
		}
	}
	b.bindings[threadID] = binding
	b.ring[b.next] = threadID
	b.next = (b.next + 1) % b.limit
}

func (b *threadBindings) get(threadID string) threadBinding {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.bindings[threadID]
}

// stableFor reports whether model may be used in a thread that already has a
// binding. A thread with no known provider accepts any model; a bound thread
// must stay on the provider it was bound to.
func (b *threadBindings) stableFor(binding threadBinding, model string, cfg providerResolver) bool {
	provider := binding.provider
	return provider == "" || cfg.providerFor(model) == provider
}

// sourceForFork returns the model/provider a model-less fork should inherit
// from its source thread. It prefers the observed model, then infers the
// provider's unique model, and refuses to guess when the provider is shared
// or the binding is unknown.
func (b *threadBindings) sourceForFork(threadID string, cfg providerResolver) (model, provider string, ok bool) {
	binding := b.get(threadID)
	model = binding.model
	if model == "" {
		if inferred, _, ok := cfg.uniqueModelForProvider(binding.provider); ok {
			model = inferred
		}
	}
	if model == "" || binding.provider == "" || cfg.providerFor(model) != binding.provider {
		return "", "", false
	}
	return model, binding.provider, true
}
