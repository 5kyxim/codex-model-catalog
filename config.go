package modelcatalog

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

const catalogConfigVersion = 1

type catalogConfig struct {
	Version         int                  `json:"version"`
	DefaultProvider string               `json:"default_provider"`
	Models          map[string]modelSpec `json:"models"`
}

type modelSpec struct {
	Provider           string            `json:"provider"`
	Catalog            map[string]any    `json:"catalog"`
	ReasoningEffortMap map[string]string `json:"reasoning_effort_map,omitempty"`
}

func loadCatalogConfig(path string) (catalogConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return catalogConfig{}, fmt.Errorf("read model catalog config %q: %w", path, err)
	}

	var cfg catalogConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return catalogConfig{}, fmt.Errorf("parse model catalog config %q: %w", path, err)
	}
	if cfg.Version != catalogConfigVersion {
		return catalogConfig{}, fmt.Errorf("model catalog config version must be %d, got %d", catalogConfigVersion, cfg.Version)
	}
	if cfg.DefaultProvider == "" {
		return catalogConfig{}, fmt.Errorf("model catalog config default_provider is required")
	}
	if cfg.Models == nil {
		cfg.Models = make(map[string]modelSpec)
	}
	for model, spec := range cfg.Models {
		if err := validateModelSpec(model, spec); err != nil {
			return catalogConfig{}, err
		}
	}

	return cfg, nil
}

func validateModelSpec(model string, spec modelSpec) error {
	if model == "" {
		return fmt.Errorf("configured model ID must be non-empty")
	}
	if spec.Provider == "" {
		return fmt.Errorf("model %q provider is required", model)
	}
	if spec.Catalog == nil {
		return fmt.Errorf("model %q catalog is required", model)
	}
	if _, present := spec.Catalog["slug"]; present {
		return fmt.Errorf("model %q catalog must not set slug; the models key is the model ID", model)
	}
	displayName, ok := spec.Catalog["display_name"].(string)
	if !ok || displayName == "" {
		return fmt.Errorf("model %q catalog.display_name is required", model)
	}

	defaultEffort, hasDefault := spec.Catalog["default_reasoning_level"]
	if hasDefault {
		value, ok := defaultEffort.(string)
		if !ok || value == "" {
			return fmt.Errorf("model %q catalog.default_reasoning_level must be a non-empty string", model)
		}
	}
	for input, output := range spec.ReasoningEffortMap {
		if input == "" || output == "" {
			return fmt.Errorf("model %q reasoning effort mapping must use non-empty values", model)
		}
	}
	if len(spec.ReasoningEffortMap) > 0 {
		if !hasDefault {
			return fmt.Errorf("model %q catalog.default_reasoning_level is required when reasoning_effort_map is set", model)
		}
		value := defaultEffort.(string)
		if _, ok := spec.ReasoningEffortMap[value]; !ok {
			return fmt.Errorf("model %q reasoning_effort_map must include the default effort %q", model, value)
		}
	}

	return nil
}

func (c catalogConfig) providerFor(model string) string {
	if spec, ok := c.Models[model]; ok {
		return spec.Provider
	}
	return c.DefaultProvider
}

func (c catalogConfig) model(model string) (modelSpec, bool) {
	spec, ok := c.Models[model]
	return spec, ok
}

func (c catalogConfig) models() []string {
	models := make([]string, 0, len(c.Models))
	for model := range c.Models {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

func (c catalogConfig) uniqueModelForProvider(provider string) (string, modelSpec, bool) {
	if provider == "" || provider == c.DefaultProvider {
		return "", modelSpec{}, false
	}
	var foundModel string
	var foundSpec modelSpec
	for model, spec := range c.Models {
		if spec.Provider != provider {
			continue
		}
		if foundModel != "" {
			return "", modelSpec{}, false
		}
		foundModel = model
		foundSpec = spec
	}
	return foundModel, foundSpec, foundModel != ""
}

func (s modelSpec) defaultEffort() (string, bool) {
	raw, ok := s.Catalog["default_reasoning_level"]
	if !ok {
		return "", false
	}
	effort, ok := raw.(string)
	if !ok || effort == "" {
		return "", false
	}
	normalized, ok := s.normalizeEffort(effort)
	return normalized, ok
}

func (s modelSpec) normalizeEffort(effort string) (string, bool) {
	if len(s.ReasoningEffortMap) == 0 {
		return effort, true
	}
	normalized, ok := s.ReasoningEffortMap[effort]
	return normalized, ok
}
