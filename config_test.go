package modelcatalog

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	testModelID    = "custom-reasoning-model"
	testProviderID = "third_party"
)

func testModelSpec() modelSpec {
	return modelSpec{
		Provider: testProviderID,
		Catalog: map[string]any{
			"display_name":            "Custom Reasoning Model",
			"description":             "Test provider",
			"default_reasoning_level": "high",
			"supported_reasoning_levels": []any{
				map[string]any{"effort": "none", "description": "No thinking"},
				map[string]any{"effort": "low", "description": "Low thinking"},
				map[string]any{"effort": "high", "description": "Thinking"},
				map[string]any{"effort": "max", "description": "Maximum thinking"},
			},
			"input_modalities":     []any{"text"},
			"supports_search_tool": false,
			"web_search_tool_type": "text",
		},
		ReasoningEffortMap: map[string]string{
			"none":    "none",
			"minimal": "low",
			"low":     "low",
			"medium":  "high",
			"high":    "high",
			"xhigh":   "high",
			"max":     "max",
			"ultra":   "max",
		},
	}
}

func testCatalogConfig() catalogConfig {
	return catalogConfig{
		Version:         catalogConfigVersion,
		DefaultProvider: "openai",
		Models: map[string]modelSpec{
			testModelID: testModelSpec(),
		},
	}
}

func TestLoadCatalogConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`{
  "version": 1,
  "default_provider": "openai",
  "expose_hidden_models": true,
  "models": {
    "custom-reasoning-model": {
      "provider": "third_party",
      "catalog": {
        "display_name": "Custom Reasoning Model",
        "default_reasoning_level": "high"
      },
      "reasoning_effort_map": {
        "high": "high",
        "xhigh": "high"
      }
    }
  }
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadCatalogConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.providerFor(testModelID); got != testProviderID {
		t.Fatalf("providerFor(custom model) = %q", got)
	}
	if got := cfg.providerFor("native-model"); got != "openai" {
		t.Fatalf("providerFor(native model) = %q", got)
	}
	if got, ok := cfg.Models[testModelID].normalizeEffort("xhigh"); !ok || got != "high" {
		t.Fatalf("normalizeEffort(xhigh) = %q, %v", got, ok)
	}
	if !cfg.ExposeHiddenModels {
		t.Fatal("expose_hidden_models was not loaded")
	}
}

func TestLoadCatalogConfigRejectsInvalidVersion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"default_provider":"openai","models":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCatalogConfig(path); err == nil {
		t.Fatal("expected invalid version error")
	}
}

func TestLoadCatalogConfigRequiresDefaultEffortMapping(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`{
  "version": 1,
  "default_provider": "openai",
  "models": {
    "custom-model": {
      "provider": "third_party",
      "catalog": {
        "display_name": "Custom Model",
        "default_reasoning_level": "high"
      },
      "reasoning_effort_map": {"low": "low"}
    }
  }
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCatalogConfig(path); err == nil {
		t.Fatal("expected missing default effort mapping error")
	}
}

func TestLoadCatalogConfigRejectsInvalidCatalogMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`{
  "version": 1,
  "default_provider": "openai",
  "models": {
    "custom-model": {
      "provider": "third_party",
      "catalog_mode": "unknown",
      "catalog": {"display_name": "Custom Model"}
    }
  }
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCatalogConfig(path); err == nil {
		t.Fatal("expected invalid catalog mode error")
	}
}
