package modelcatalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type catalogDocument struct {
	Models []json.RawMessage `json:"models"`
}

func refreshCatalog(cachePath, catalogPath string, cfg catalogConfig) (int, error) {
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return useExistingCatalog(catalogPath, cfg, fmt.Errorf("read model cache: %w", err))
	}

	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return useExistingCatalog(catalogPath, cfg, fmt.Errorf("parse model cache: %w", err))
	}
	models, ok := root["models"].([]any)
	if !ok || len(models) == 0 {
		return useExistingCatalog(catalogPath, cfg, errors.New("model cache has no models"))
	}

	template, err := catalogTemplate(models)
	if err != nil {
		return useExistingCatalog(catalogPath, cfg, err)
	}

	merged := make([]any, 0, len(models)+len(cfg.Models))
	seen := make(map[string]bool, len(models))
	for _, item := range models {
		model, ok := item.(map[string]any)
		if !ok {
			return useExistingCatalog(catalogPath, cfg, errors.New("model cache contains an invalid model entry"))
		}
		slug, ok := stringValue(model["slug"])
		if !ok || slug == "" {
			return useExistingCatalog(catalogPath, cfg, errors.New("model cache contains a model without a slug"))
		}
		seen[slug] = true
		if cfg.ExposeHiddenModels && model["visibility"] == "hide" {
			model["visibility"] = "list"
		}

		spec, configured := cfg.model(slug)
		if !configured {
			merged = append(merged, item)
			continue
		}
		entry, err := modelCatalogEntry(model, slug, spec, cfg.DefaultProvider)
		if err != nil {
			return useExistingCatalog(catalogPath, cfg, err)
		}
		merged = append(merged, entry)
	}

	for _, slug := range cfg.models() {
		if seen[slug] {
			continue
		}
		entry, err := modelCatalogEntry(template, slug, cfg.Models[slug], cfg.DefaultProvider)
		if err != nil {
			return useExistingCatalog(catalogPath, cfg, err)
		}
		merged = append(merged, entry)
	}

	root["models"] = merged
	if err := writeJSONAtomically(catalogPath, root, 0o600); err != nil {
		return 0, fmt.Errorf("write model catalog: %w", err)
	}
	return len(merged), nil
}

func catalogTemplate(models []any) (map[string]any, error) {
	var fallback map[string]any
	for _, item := range models {
		model, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if fallback == nil {
			fallback = model
		}
		if instructions, ok := model["base_instructions"].(string); ok && instructions != "" {
			return model, nil
		}
	}
	if fallback == nil {
		return nil, errors.New("model cache has no usable template")
	}
	return fallback, nil
}

func modelCatalogEntry(base map[string]any, slug string, spec modelSpec, defaultProvider string) (map[string]any, error) {
	data, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("copy catalog template for model %q: %w", slug, err)
	}
	var model map[string]any
	if err := json.Unmarshal(data, &model); err != nil {
		return nil, fmt.Errorf("copy catalog template for model %q: %w", slug, err)
	}

	if spec.Provider != defaultProvider {
		model["multi_agent_version"] = "v1"
	}
	for key, value := range spec.Catalog {
		model[key] = value
	}
	model["slug"] = slug

	return model, nil
}

func writeJSONAtomically(path string, value any, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".model-catalog-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		if err != nil {
			_ = os.Remove(tempPath)
		}
	}()

	if err = temp.Chmod(mode); err != nil {
		return err
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(value); err != nil {
		return err
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tempPath, path); err != nil {
		return err
	}
	return nil
}

func useExistingCatalog(path string, cfg catalogConfig, refreshErr error) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("refresh catalog: %w; existing catalog unavailable: %v", refreshErr, err)
	}
	var catalog catalogDocument
	if err := json.Unmarshal(data, &catalog); err != nil {
		return 0, fmt.Errorf("refresh catalog: %w; existing catalog invalid: %v", refreshErr, err)
	}
	if len(catalog.Models) == 0 {
		return 0, fmt.Errorf("refresh catalog: %w; existing catalog has no models", refreshErr)
	}

	missing := make(map[string]bool, len(cfg.Models))
	for model := range cfg.Models {
		missing[model] = true
	}
	for _, raw := range catalog.Models {
		var entry struct {
			Slug string `json:"slug"`
		}
		if json.Unmarshal(raw, &entry) == nil {
			delete(missing, entry.Slug)
		}
	}
	if len(missing) > 0 {
		models := make([]string, 0, len(missing))
		for model := range missing {
			models = append(models, model)
		}
		sort.Strings(models)
		return 0, fmt.Errorf("refresh catalog: %w; existing catalog lacks configured models: %s", refreshErr, strings.Join(models, ", "))
	}

	fmt.Fprintf(os.Stderr, "codex-model-catalog: warning: %v; using existing catalog\n", refreshErr)
	return len(catalog.Models), nil
}
