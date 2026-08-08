package modelcatalog

import "fmt"

// modelFromParams finds the model a request params object refers to. Codex
// sends the model in one of several shapes depending on the RPC method, so
// this helper owns the shape knowledge once.
func modelFromParams(params map[string]any) string {
	if collaboration, ok := params["collaborationMode"].(map[string]any); ok {
		if settings, ok := collaboration["settings"].(map[string]any); ok {
			if model, ok := stringValue(settings["model"]); ok && model != "" {
				return model
			}
		}
	}
	if model, ok := stringValue(params["model"]); ok && model != "" {
		return model
	}
	if config, ok := params["config"].(map[string]any); ok {
		if model, ok := stringValue(config["model"]); ok && model != "" {
			return model
		}
	}
	return ""
}

// normalizeConfigEffort rewrites the config.model_reasoning_effort field of a
// start/resume/fork request. With useDefault it also fills in the model's
// configured default effort when the field is absent.
func normalizeConfigEffort(params map[string]any, model string, spec modelSpec, useDefault bool) (bool, error) {
	config, present := params["config"].(map[string]any)
	if !present {
		if !useDefault {
			return false, nil
		}
		config = make(map[string]any)
		params["config"] = config
	}

	raw, present := config["model_reasoning_effort"]
	if !present || raw == nil {
		if !useDefault {
			return false, nil
		}
		defaultEffort, ok := spec.defaultEffort()
		if !ok {
			return false, nil
		}
		config["model_reasoning_effort"] = defaultEffort
		return true, nil
	}

	normalized, changed, err := normalizeEffortValue(spec, raw, model)
	if err != nil {
		return false, err
	}
	if changed {
		config["model_reasoning_effort"] = normalized
	}
	return changed, nil
}

// normalizeTurnEffort rewrites the reasoning effort of an in-thread request.
// Codex sends it either inside collaborationMode.settings or as a top-level
// effort field; the collaboration shape wins when present.
func normalizeTurnEffort(params map[string]any, model string, spec modelSpec) (bool, error) {
	if collaboration, ok := params["collaborationMode"].(map[string]any); ok {
		if settings, ok := collaboration["settings"].(map[string]any); ok {
			if raw, present := settings["reasoning_effort"]; present && raw != nil {
				normalized, changed, err := normalizeEffortValue(spec, raw, model)
				if err != nil {
					return false, err
				}
				if changed {
					settings["reasoning_effort"] = normalized
				}
				return changed, nil
			}
			return false, nil
		}
	}

	raw, present := params["effort"]
	if !present || raw == nil {
		return false, nil
	}
	normalized, changed, err := normalizeEffortValue(spec, raw, model)
	if err != nil {
		return false, err
	}
	if changed {
		params["effort"] = normalized
	}
	return changed, nil
}

// normalizeEffortValue maps one raw effort value through the model's
// reasoning_effort_map. It reports whether the value changed and rejects
// non-string or unsupported efforts.
func normalizeEffortValue(spec modelSpec, raw any, model string) (string, bool, error) {
	value, ok := stringValue(raw)
	if !ok {
		return "", false, fmt.Errorf("reasoning effort for model %q must be a string", model)
	}
	normalized, ok := spec.normalizeEffort(value)
	if !ok {
		return "", false, fmt.Errorf("model %q does not support reasoning effort %q", model, value)
	}
	return normalized, normalized != value, nil
}
