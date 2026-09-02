package modelcatalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefreshCatalogPreservesCachedModelsAndAddsConfiguredModel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "models_cache.json")
	catalogPath := filepath.Join(dir, "model-catalog.json")
	cache := `{
  "client_version": "test",
  "models": [
    {
      "slug": "native-model",
      "display_name": "Native Model",
      "base_instructions": "test instructions",
      "model_messages": {"instructions_template": "test"},
      "input_modalities": ["text", "image"],
      "multi_agent_version": "v2"
    },
    {"slug": "routed-native-model", "display_name": "Routed Native Model", "multi_agent_version": "v2"}
  ]
}`
	if err := os.WriteFile(cachePath, []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := testCatalogConfig()
	cfg.Models["routed-native-model"] = modelSpec{
		Provider: "mirror_provider",
		Catalog: map[string]any{
			"display_name": "Routed Native Model",
		},
	}
	count, err := refreshCatalog(cachePath, catalogPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("model count = %d, want 3", count)
	}

	data, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	models := root["models"].([]any)
	if models[0].(map[string]any)["slug"] != "native-model" || models[1].(map[string]any)["slug"] != "routed-native-model" {
		t.Fatalf("cached model ordering was not preserved: %#v", models)
	}
	if models[0].(map[string]any)["multi_agent_version"] != "v2" {
		t.Fatalf("default-provider model multi-agent version changed: %#v", models[0])
	}
	if models[1].(map[string]any)["multi_agent_version"] != "v1" {
		t.Fatalf("third-party model did not use V1: %#v", models[1])
	}

	custom := models[2].(map[string]any)
	if custom["slug"] != testModelID || custom["display_name"] != "Custom Reasoning Model" {
		t.Fatalf("unexpected configured model entry: %#v", custom)
	}
	if custom["multi_agent_version"] != "v1" {
		t.Fatalf("configured model multi-agent version = %#v, want v1", custom["multi_agent_version"])
	}
	modalities := custom["input_modalities"].([]any)
	if len(modalities) != 1 || modalities[0] != "text" {
		t.Fatalf("configured model modalities = %#v", modalities)
	}
	efforts := custom["supported_reasoning_levels"].([]any)
	want := []string{"none", "low", "high", "max"}
	if len(efforts) != len(want) {
		t.Fatalf("reasoning effort count = %d, want %d", len(efforts), len(want))
	}
	for index, expected := range want {
		if efforts[index].(map[string]any)["effort"] != expected {
			t.Fatalf("effort %d = %#v, want %q", index, efforts[index], expected)
		}
	}
	if custom["base_instructions"] != "test instructions" {
		t.Fatal("configured model did not inherit the current Codex instructions template")
	}
	if custom["supports_search_tool"] != false || custom["web_search_tool_type"] != "text" {
		t.Fatalf("unexpected search configuration: %#v", custom)
	}
	info, err := os.Stat(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("catalog mode = %o, want 600", info.Mode().Perm())
	}
}

func TestRefreshCatalogReplacesTemplateWhenConfigured(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "models_cache.json")
	catalogPath := filepath.Join(dir, "model-catalog.json")
	cache := `{
  "models": [
    {
      "slug": "native-model",
      "display_name": "Native Model",
      "base_instructions": "native instructions",
      "include_plugin_usage_instructions": true,
      "tool_mode": "code_mode_only"
    }
  ]
}`
	if err := os.WriteFile(cachePath, []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := testCatalogConfig()
	spec := cfg.Models[testModelID]
	spec.CatalogMode = "replace"
	spec.Catalog["base_instructions"] = ""
	cfg.Models[testModelID] = spec
	if _, err := refreshCatalog(cachePath, catalogPath, cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	custom := root["models"].([]any)[1].(map[string]any)
	if _, ok := custom["include_plugin_usage_instructions"]; ok {
		t.Fatalf("replacement model inherited plugin metadata: %#v", custom)
	}
	if _, ok := custom["tool_mode"]; ok {
		t.Fatalf("replacement model inherited tool mode: %#v", custom)
	}
	if custom["base_instructions"] != "" {
		t.Fatalf("replacement model instructions = %#v, want empty", custom["base_instructions"])
	}
	if custom["multi_agent_version"] != "v1" {
		t.Fatalf("replacement model multi-agent version = %#v, want v1", custom["multi_agent_version"])
	}
}

func TestRefreshCatalogExposesHiddenModelsWhenConfigured(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "models_cache.json")
	catalogPath := filepath.Join(dir, "model-catalog.json")
	cache := `{
  "models": [
    {
      "slug": "hidden-native-model",
      "display_name": "Hidden Native Model",
      "visibility": "hide",
      "supported_in_api": false
    },
    {
      "slug": "listed-native-model",
      "display_name": "Listed Native Model",
      "visibility": "list"
    }
  ]
}`
	if err := os.WriteFile(cachePath, []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := testCatalogConfig()
	cfg.ExposeHiddenModels = true
	if _, err := refreshCatalog(cachePath, catalogPath, cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	models := root["models"].([]any)
	hidden := models[0].(map[string]any)
	if hidden["visibility"] != "list" {
		t.Fatalf("hidden model visibility = %#v, want list", hidden["visibility"])
	}
	if hidden["supported_in_api"] != false {
		t.Fatalf("hidden model API support changed: %#v", hidden["supported_in_api"])
	}
	if listed := models[1].(map[string]any); listed["visibility"] != "list" {
		t.Fatalf("listed model visibility = %#v, want list", listed["visibility"])
	}
}

func TestRefreshCatalogExplicitVisibilityOverridesHiddenExposure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "models_cache.json")
	catalogPath := filepath.Join(dir, "model-catalog.json")
	cache := `{
  "models": [
    {
      "slug": "hidden-native-model",
      "display_name": "Hidden Native Model",
      "visibility": "hide"
    }
  ]
}`
	if err := os.WriteFile(cachePath, []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := testCatalogConfig()
	cfg.ExposeHiddenModels = true
	cfg.Models["hidden-native-model"] = modelSpec{
		Provider: "openai",
		Catalog: map[string]any{
			"display_name": "Hidden Native Model",
			"visibility":   "hide",
		},
	}
	if _, err := refreshCatalog(cachePath, catalogPath, cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	model := root["models"].([]any)[0].(map[string]any)
	if model["visibility"] != "hide" {
		t.Fatalf("configured visibility = %#v, want hide", model["visibility"])
	}
}

func TestRefreshCatalogFallsBackToExistingCatalog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "models_cache.json")
	catalogPath := filepath.Join(dir, "model-catalog.json")
	if err := os.WriteFile(cachePath, []byte(`not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	existing := `{"models":[{"slug":"native-model"},{"slug":"custom-reasoning-model"}]}`
	if err := os.WriteFile(catalogPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	count, err := refreshCatalog(cachePath, catalogPath, testCatalogConfig())
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("fallback model count = %d", count)
	}
}

func TestRefreshCatalogRejectsFallbackMissingConfiguredModel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "models_cache.json")
	catalogPath := filepath.Join(dir, "model-catalog.json")
	if err := os.WriteFile(cachePath, []byte(`not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalogPath, []byte(`{"models":[{"slug":"native-model"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := refreshCatalog(cachePath, catalogPath, testCatalogConfig())
	if err == nil || !strings.Contains(err.Error(), testModelID) {
		t.Fatalf("error = %v, want missing configured model", err)
	}
}
