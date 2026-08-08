package modelcatalog

import "testing"

func TestThreadBindingsStableFor(t *testing.T) {
	t.Parallel()
	cfg := testCatalogConfig()
	store := newThreadBindings(2)

	if !store.stableFor(threadBinding{}, testModelID, cfg) {
		t.Fatal("unknown thread must accept any model")
	}
	if store.stableFor(threadBinding{provider: testProviderID}, "native-model", cfg) {
		t.Fatal("bound thread must reject a different provider")
	}
	if !store.stableFor(threadBinding{provider: testProviderID}, testModelID, cfg) {
		t.Fatal("bound thread must accept a model on its own provider")
	}
}

func TestThreadBindingsSourceForFork(t *testing.T) {
	t.Parallel()
	cfg := testCatalogConfig()
	store := newThreadBindings(4)

	store.set("source-observed", testModelID, testProviderID)
	if model, provider, ok := store.sourceForFork("source-observed", cfg); !ok || model != testModelID || provider != testProviderID {
		t.Fatalf("observed sourceForFork = %q/%q/%v", model, provider, ok)
	}

	store.set("source-provider-only", "", testProviderID)
	if model, provider, ok := store.sourceForFork("source-provider-only", cfg); !ok || model != testModelID || provider != testProviderID {
		t.Fatalf("inferred sourceForFork = %q/%q/%v", model, provider, ok)
	}

	if _, _, ok := store.sourceForFork("missing", cfg); ok {
		t.Fatal("unknown source must not produce a binding")
	}
}

func TestThreadBindingsSourceForForkRefusesSharedProvider(t *testing.T) {
	t.Parallel()
	cfg := testCatalogConfig()
	cfg.Models["alternate-model"] = testModelSpec()
	store := newThreadBindings(2)
	store.set("source", "", testProviderID)

	if _, _, ok := store.sourceForFork("source", cfg); ok {
		t.Fatal("shared provider must not be inferred")
	}
}
