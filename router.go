package modelcatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
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

type router struct {
	cfg      catalogConfig
	bindings *threadBindings
	stats    *statsStore
}

func newRouter(cfg catalogConfig) *router {
	return newRouterWithLimit(cfg, defaultThreadBindingsLimit)
}

func newRouterWithLimit(cfg catalogConfig, limit int) *router {
	r := &router{
		cfg:      cfg,
		bindings: newThreadBindings(limit),
		stats:    newDefaultStatsStore(),
	}
	r.stats.setThreadModelSource(r)
	return r
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
		if !r.bindings.stableFor(binding, model, r.cfg) {
			return r.providerChangeError(envelope.ID, model)
		}
		if hasExplicitProvider && explicitProvider != targetProvider {
			return r.invalidParams(envelope.ID, providerConflictMessage(model, explicitProvider, targetProvider))
		}
	}
	if threadID != "" && model != "" {
		r.bindings.set(threadID, model, r.cfg.providerFor(model))
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
	changed := false
	if model == "" {
		// Desktop side chats fork without model overrides, then apply the source
		// model on their first turn. Bind the fork up front so its provider stays
		// stable for the new thread's lifetime.
		if _, hasProvider := nonEmptyStringField(params, "modelProvider"); hasProvider {
			return clientAction{forward: original}
		}
		threadID, _ := stringValue(params["threadId"])
		var inheritedProvider string
		var ok bool
		model, inheritedProvider, ok = r.bindings.sourceForFork(threadID, r.cfg)
		if !ok {
			return clientAction{forward: original}
		}
		params["model"] = model
		params["modelProvider"] = inheritedProvider
		changed = true
	}

	provider := r.cfg.providerFor(model)
	providerChanged, err := ensureProvider(params, provider)
	if err != nil {
		return r.invalidParams(envelope.ID, err.Error())
	}
	changed = changed || providerChanged
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
	model := modelFromParams(params)
	if model != "" && !r.bindings.stableFor(binding, model, r.cfg) {
		return r.providerChangeError(envelope.ID, model)
	}
	if threadID != "" && model != "" {
		r.bindings.set(threadID, model, r.cfg.providerFor(model))
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
	r.observeServerLineAt(line, time.Now())
}

func (r *router) observeServerLineAt(line []byte, at time.Time) {
	if r.stats.observeServerLine(line, at) {
		return
	}
	if !bytes.Contains(line, []byte(`"thread"`)) &&
		!bytes.Contains(line, []byte(`"collabAgentToolCall"`)) &&
		!bytes.Contains(line, []byte(`"subAgentActivity"`)) {
		return
	}
	var envelope serverLineEnvelope
	if json.Unmarshal(line, &envelope) != nil {
		return
	}
	r.observeThreadContainer(envelope.Result)
	r.observeThreadContainer(envelope.Params)
	r.observeCollabAgentSpawn(envelope.Result.Item)
	r.observeCollabAgentSpawn(envelope.Params.Item)
	r.observeSubAgentActivity(envelope.Result)
	r.observeSubAgentActivity(envelope.Params)
}

func (r *router) observeThreadContainer(container serverLineContainer) {
	thread := container.Thread
	if thread.ID == "" || (thread.Model == "" && thread.ModelProvider == "" && container.Model == "") {
		return
	}
	if thread.Model != "" {
		r.bindings.set(thread.ID, thread.Model, r.cfg.providerFor(thread.Model))
	} else if thread.ModelProvider != "" {
		r.bindings.set(thread.ID, "", thread.ModelProvider)
	}
	if container.Model != "" && container.Model != thread.Model {
		r.bindings.set(thread.ID, container.Model, r.cfg.providerFor(container.Model))
	}
}

func (r *router) observeCollabAgentSpawn(item serverItemFields) {
	if item.Type != "collabAgentToolCall" || item.Tool != "spawnAgent" {
		return
	}

	model := item.Model
	provider := ""
	if model == "" {
		parent := r.bindingForThread(item.SenderThreadID)
		model = parent.model
		provider = parent.provider
	}
	if model == "" {
		return
	}
	if provider == "" {
		provider = r.cfg.providerFor(model)
	}
	for _, threadID := range item.ReceiverThreadIDs {
		if threadID != "" {
			r.bindings.set(threadID, model, provider)
		}
	}
}

func (r *router) observeSubAgentActivity(container serverLineContainer) {
	item := container.Item
	if item.Type != "subAgentActivity" || item.Kind != "started" ||
		container.ThreadID == "" || item.AgentThreadID == "" {
		return
	}
	if child := r.bindingForThread(item.AgentThreadID); child.model != "" {
		return
	}

	parent := r.bindingForThread(container.ThreadID)
	if parent.model == "" {
		return
	}
	if parent.provider == "" {
		parent.provider = r.cfg.providerFor(parent.model)
	}
	r.bindings.set(item.AgentThreadID, parent.model, parent.provider)
}

type threadFields struct {
	ID            string `json:"id"`
	Model         string `json:"model"`
	ModelProvider string `json:"modelProvider"`
}

type serverItemFields struct {
	Type              string   `json:"type"`
	Tool              string   `json:"tool"`
	Model             string   `json:"model"`
	SenderThreadID    string   `json:"senderThreadId"`
	ReceiverThreadIDs []string `json:"receiverThreadIds"`
	Kind              string   `json:"kind"`
	AgentThreadID     string   `json:"agentThreadId"`
}

type serverLineContainer struct {
	Thread   threadFields     `json:"thread"`
	ThreadID string           `json:"threadId"`
	Model    string           `json:"model"`
	Item     serverItemFields `json:"item"`
}

type serverLineEnvelope struct {
	Result serverLineContainer `json:"result"`
	Params serverLineContainer `json:"params"`
}

func (r *router) bindingForThread(threadID string) threadBinding {
	return r.bindings.get(threadID)
}

func (r *router) modelForThread(threadID string) string {
	return r.bindingForThread(threadID).model
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
