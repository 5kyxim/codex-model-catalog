package modelcatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
)

type clientAction struct {
	forward []byte
	reply   []byte
}

type rpcEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type threadBinding struct {
	model    string
	provider string
}

type router struct {
	cfg catalogConfig

	mu             sync.RWMutex
	threadBindings map[string]threadBinding
}

func newRouter(cfg catalogConfig) *router {
	return &router{
		cfg:            cfg,
		threadBindings: make(map[string]threadBinding),
	}
}

func (r *router) clientLine(line []byte) clientAction {
	var envelope rpcEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil || envelope.Method == "" {
		return clientAction{forward: line}
	}

	switch envelope.Method {
	case "thread/list":
		return r.routeThreadList(line, envelope)
	case "thread/start":
		return r.routeThreadStart(line, envelope)
	case "thread/resume":
		return r.routeThreadResume(line, envelope)
	case "thread/fork":
		return r.routeThreadFork(line, envelope)
	case "turn/start", "thread/settings/update":
		return r.routeExistingThread(line, envelope)
	default:
		return clientAction{forward: line}
	}
}

func (r *router) routeThreadList(original []byte, envelope rpcEnvelope) clientAction {
	params, err := decodeParams(envelope.Params)
	if err != nil {
		return r.invalidParams(envelope.ID, err.Error())
	}
	if providers, present := params["modelProviders"]; present && providers != nil {
		return clientAction{forward: original}
	}

	// Codex Desktop sends null here, while the bundled app-server interprets
	// null as "current provider only". An empty array includes every provider.
	params["modelProviders"] = []string{}
	return encodeForward(original, envelope, params)
}

func (r *router) routeThreadStart(original []byte, envelope rpcEnvelope) clientAction {
	params, err := decodeParams(envelope.Params)
	if err != nil {
		return r.invalidParams(envelope.ID, err.Error())
	}
	model := modelFromParams(params)
	provider := r.cfg.providerFor(model)
	if model == "" {
		provider = r.cfg.DefaultProvider
	}

	changed, err := ensureProvider(params, provider)
	if err != nil {
		return r.invalidParams(envelope.ID, err.Error())
	}
	if spec, ok := r.cfg.model(model); ok {
		effortChanged, effortErr := normalizeConfigEffort(params, model, spec, true)
		if effortErr != nil {
			return r.invalidParams(envelope.ID, effortErr.Error())
		}
		changed = changed || effortChanged
	}
	if !changed {
		return clientAction{forward: original}
	}
	return encodeForward(original, envelope, params)
}

func (r *router) routeThreadResume(original []byte, envelope rpcEnvelope) clientAction {
	params, err := decodeParams(envelope.Params)
	if err != nil {
		return r.invalidParams(envelope.ID, err.Error())
	}
	threadID, _ := stringValue(params["threadId"])
	binding := r.bindingForThread(threadID)
	knownProvider := binding.provider
	model := modelFromParams(params)
	explicitProvider, hasExplicitProvider := nonEmptyStringField(params, "modelProvider")

	if knownProvider != "" && hasExplicitProvider && explicitProvider != knownProvider {
		return r.providerChangeError(envelope.ID, model)
	}
	if model != "" {
		targetProvider := r.cfg.providerFor(model)
		if knownProvider != "" && targetProvider != knownProvider {
			return r.providerChangeError(envelope.ID, model)
		}
		if hasExplicitProvider && explicitProvider != targetProvider {
			return r.invalidParams(envelope.ID, providerConflictMessage(model, explicitProvider, targetProvider))
		}
	}

	changed := false
	effortModel, spec, handlesEffort := r.reasoningSpec(model, explicitProvider, binding)
	if handlesEffort {
		changed, err = normalizeConfigEffort(params, effortModel, spec, false)
		if err != nil {
			return r.invalidParams(envelope.ID, err.Error())
		}
	}
	if !changed {
		return clientAction{forward: original}
	}
	return encodeForward(original, envelope, params)
}

func (r *router) routeThreadFork(original []byte, envelope rpcEnvelope) clientAction {
	params, err := decodeParams(envelope.Params)
	if err != nil {
		return r.invalidParams(envelope.ID, err.Error())
	}
	model := modelFromParams(params)
	if model == "" {
		return clientAction{forward: original}
	}

	provider := r.cfg.providerFor(model)
	changed, err := ensureProvider(params, provider)
	if err != nil {
		return r.invalidParams(envelope.ID, err.Error())
	}
	if spec, ok := r.cfg.model(model); ok {
		effortChanged, effortErr := normalizeConfigEffort(params, model, spec, true)
		if effortErr != nil {
			return r.invalidParams(envelope.ID, effortErr.Error())
		}
		changed = changed || effortChanged
	}
	if !changed {
		return clientAction{forward: original}
	}
	return encodeForward(original, envelope, params)
}

func (r *router) routeExistingThread(original []byte, envelope rpcEnvelope) clientAction {
	params, err := decodeParams(envelope.Params)
	if err != nil {
		return r.invalidParams(envelope.ID, err.Error())
	}
	threadID, _ := stringValue(params["threadId"])
	binding := r.bindingForThread(threadID)
	knownProvider := binding.provider
	model := modelFromParams(params)
	if model != "" && knownProvider != "" && r.cfg.providerFor(model) != knownProvider {
		return r.providerChangeError(envelope.ID, model)
	}

	effortModel, spec, handlesEffort := r.reasoningSpec(model, "", binding)
	if !handlesEffort {
		return clientAction{forward: original}
	}
	changed, err := normalizeTurnEffort(params, effortModel, spec)
	if err != nil {
		return r.invalidParams(envelope.ID, err.Error())
	}
	if !changed {
		return clientAction{forward: original}
	}
	return encodeForward(original, envelope, params)
}

func (r *router) observeServerLine(line []byte) {
	var message map[string]any
	if json.Unmarshal(line, &message) != nil {
		return
	}
	for _, containerName := range []string{"result", "params"} {
		container, ok := message[containerName].(map[string]any)
		if !ok {
			continue
		}
		thread, ok := container["thread"].(map[string]any)
		if !ok {
			continue
		}
		threadID, _ := stringValue(thread["id"])
		model, _ := stringValue(thread["model"])
		provider, _ := stringValue(thread["modelProvider"])
		if threadID != "" && (model != "" || provider != "") {
			r.mu.Lock()
			binding := r.threadBindings[threadID]
			if model != "" {
				binding.model = model
			}
			if provider != "" {
				binding.provider = provider
			}
			r.threadBindings[threadID] = binding
			r.mu.Unlock()
		}
	}
}

func (r *router) bindingForThread(threadID string) threadBinding {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.threadBindings[threadID]
}

func (r *router) reasoningSpec(model, explicitProvider string, binding threadBinding) (string, modelSpec, bool) {
	if model != "" {
		spec, ok := r.cfg.model(model)
		return model, spec, ok
	}
	if binding.model != "" {
		spec, ok := r.cfg.model(binding.model)
		return binding.model, spec, ok
	}
	for _, provider := range []string{explicitProvider, binding.provider} {
		if inferredModel, spec, ok := r.cfg.uniqueModelForProvider(provider); ok {
			return inferredModel, spec, true
		}
	}
	return "", modelSpec{}, false
}

func decodeParams(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return make(map[string]any), nil
	}
	var params map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&params); err != nil {
		return nil, fmt.Errorf("params must be a JSON object")
	}
	if params == nil {
		params = make(map[string]any)
	}
	return params, nil
}

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

func ensureProvider(params map[string]any, expected string) (bool, error) {
	provider, present := nonEmptyStringField(params, "modelProvider")
	if present {
		model := modelFromParams(params)
		if provider != expected {
			return false, fmt.Errorf("%s", providerConflictMessage(model, provider, expected))
		}
		return false, nil
	}
	params["modelProvider"] = expected
	return true, nil
}

func providerConflictMessage(model, actual, expected string) string {
	if model == "" {
		return fmt.Sprintf("modelProvider %q conflicts with provider %q", actual, expected)
	}
	return fmt.Sprintf("model %q requires modelProvider %q, not %q", model, expected, actual)
}

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
	value, ok := stringValue(raw)
	if !ok {
		return false, fmt.Errorf("reasoning effort for model %q must be a string", model)
	}
	normalized, ok := spec.normalizeEffort(value)
	if !ok {
		return false, fmt.Errorf("model %q does not support reasoning effort %q", model, value)
	}
	if normalized == value {
		return false, nil
	}
	config["model_reasoning_effort"] = normalized
	return true, nil
}

func normalizeTurnEffort(params map[string]any, model string, spec modelSpec) (bool, error) {
	if collaboration, ok := params["collaborationMode"].(map[string]any); ok {
		if settings, ok := collaboration["settings"].(map[string]any); ok {
			if raw, present := settings["reasoning_effort"]; present && raw != nil {
				value, ok := stringValue(raw)
				if !ok {
					return false, fmt.Errorf("reasoning effort for model %q must be a string", model)
				}
				normalized, ok := spec.normalizeEffort(value)
				if !ok {
					return false, fmt.Errorf("model %q does not support reasoning effort %q", model, value)
				}
				if normalized != value {
					settings["reasoning_effort"] = normalized
					return true, nil
				}
			}
			return false, nil
		}
	}

	raw, present := params["effort"]
	if !present || raw == nil {
		return false, nil
	}
	value, ok := stringValue(raw)
	if !ok {
		return false, fmt.Errorf("reasoning effort for model %q must be a string", model)
	}
	normalized, ok := spec.normalizeEffort(value)
	if !ok {
		return false, fmt.Errorf("model %q does not support reasoning effort %q", model, value)
	}
	if normalized == value {
		return false, nil
	}
	params["effort"] = normalized
	return true, nil
}

func stringValue(value any) (string, bool) {
	result, ok := value.(string)
	return result, ok
}

func nonEmptyStringField(values map[string]any, key string) (string, bool) {
	value, ok := stringValue(values[key])
	return value, ok && value != ""
}

func encodeForward(original []byte, envelope rpcEnvelope, params map[string]any) clientAction {
	var message map[string]any
	decoder := json.NewDecoder(bytes.NewReader(original))
	decoder.UseNumber()
	if err := decoder.Decode(&message); err != nil {
		return clientAction{reply: rpcError(envelope.ID, -32603, "failed to encode routed request")}
	}
	message["params"] = params
	if len(envelope.ID) != 0 {
		message["id"] = json.RawMessage(envelope.ID)
	}
	data, err := json.Marshal(message)
	if err != nil {
		return clientAction{reply: rpcError(envelope.ID, -32603, "failed to encode routed request")}
	}
	return clientAction{forward: append(data, '\n')}
}

func (r *router) invalidParams(id json.RawMessage, message string) clientAction {
	return clientAction{reply: rpcError(id, -32602, message)}
}

func (r *router) providerChangeError(id json.RawMessage, model string) clientAction {
	message := "Model provider cannot change within an existing task. Start a new task to use the selected model."
	if model != "" {
		message = fmt.Sprintf("Model provider cannot change within an existing task. Start a new task to use %s.", model)
	}
	return r.invalidParams(id, message)
}

func rpcError(id json.RawMessage, code int, message string) []byte {
	response := map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	if len(id) != 0 {
		response["id"] = json.RawMessage(id)
	} else {
		response["id"] = nil
	}
	data, _ := json.Marshal(response)
	return append(data, '\n')
}
