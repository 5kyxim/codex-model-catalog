package modelcatalog

import (
	"encoding/json"
	"strings"
	"testing"
)

func testRouter() *router {
	return newRouter(testCatalogConfig())
}

func TestRouterPassesUnrelatedAndMalformedLinesByteForByte(t *testing.T) {
	t.Parallel()
	router := testRouter()
	for _, line := range [][]byte{
		[]byte("not json\n"),
		[]byte("  {\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"model/list\",\"params\":{}}  \n"),
	} {
		action := router.clientLine(line)
		if string(action.forward) != string(line) || len(action.reply) != 0 {
			t.Fatalf("line was not passed byte-for-byte: %#v", action)
		}
	}
}

func TestRouterListsThreadsAcrossProvidersByDefault(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"missing params":         `{"jsonrpc":"2.0","id":1,"method":"thread/list"}`,
		"null params":            `{"jsonrpc":"2.0","id":1,"method":"thread/list","params":null}`,
		"missing modelProviders": `{"jsonrpc":"2.0","id":1,"method":"thread/list","params":{}}`,
		"null modelProviders":    `{"jsonrpc":"2.0","id":1,"method":"thread/list","params":{"modelProviders":null}}`,
	}
	for name, input := range tests {
		input := input
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			message := decodeForwarded(t, testRouter().clientLine([]byte(input+"\n")))
			params, ok := message["params"].(map[string]any)
			if !ok {
				t.Fatalf("params = %#v, want an object", message["params"])
			}
			providers, ok := params["modelProviders"].([]any)
			if !ok || len(providers) != 0 {
				t.Fatalf("modelProviders = %#v, want an empty array", params["modelProviders"])
			}
		})
	}
}

func TestRouterPreservesExplicitThreadListProviderFilters(t *testing.T) {
	t.Parallel()

	for _, line := range [][]byte{
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"thread/list","params":{"modelProviders":[]}}` + "\n"),
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"thread/list","params":{"modelProviders":["openai"]}}` + "\n"),
	} {
		action := testRouter().clientLine(line)
		if string(action.forward) != string(line) || len(action.reply) != 0 {
			t.Fatalf("explicit provider filter was not preserved: %#v", action)
		}
	}
}

func TestRouterAddsOpenAIProviderAndPreservesUnknownFields(t *testing.T) {
	t.Parallel()
	router := testRouter()
	line := []byte(`{"jsonrpc":"2.0","id":9007199254740993,"method":"thread/start","future":"keep","params":{"model":"gpt-5.6-sol","futureParam":9007199254740993}}` + "\n")
	action := router.clientLine(line)
	message := decodeForwarded(t, action)
	if message["future"] != "keep" {
		t.Fatalf("unknown top-level field lost: %#v", message)
	}
	params := message["params"].(map[string]any)
	if params["modelProvider"] != "openai" {
		t.Fatalf("unexpected routed params: %#v", params)
	}
	if !strings.Contains(string(action.forward), `"id":9007199254740993`) ||
		!strings.Contains(string(action.forward), `"futureParam":9007199254740993`) {
		t.Fatalf("JSON number lost precision: %s", action.forward)
	}
}

func TestRouterAddsConfiguredProviderAndDefaultEffort(t *testing.T) {
	t.Parallel()
	router := testRouter()
	line := []byte(`{"jsonrpc":"2.0","id":"start","method":"thread/start","params":{"model":"custom-reasoning-model","config":null}}` + "\n")
	params := decodeForwarded(t, router.clientLine(line))["params"].(map[string]any)
	if params["modelProvider"] != testProviderID {
		t.Fatalf("provider = %#v", params["modelProvider"])
	}
	config := params["config"].(map[string]any)
	if config["model_reasoning_effort"] != "high" {
		t.Fatalf("default effort = %#v", config["model_reasoning_effort"])
	}
}

func TestRouterNormalizesConfiguredStartEffort(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"none":    "none",
		"minimal": "low",
		"low":     "low",
		"medium":  "high",
		"high":    "high",
		"xhigh":   "high",
		"max":     "max",
		"ultra":   "max",
	}
	for input, expected := range tests {
		input, expected := input, expected
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			line := []byte(`{"jsonrpc":"2.0","id":1,"method":"thread/start","params":{"model":"custom-reasoning-model","config":{"model_reasoning_effort":"` + input + `"}}}` + "\n")
			params := decodeForwarded(t, testRouter().clientLine(line))["params"].(map[string]any)
			config := params["config"].(map[string]any)
			if config["model_reasoning_effort"] != expected {
				t.Fatalf("normalized effort = %#v, want %q", config["model_reasoning_effort"], expected)
			}
		})
	}
}

func TestRouterAllowsConfiguredNonThinkingEffort(t *testing.T) {
	t.Parallel()
	line := []byte(`{"jsonrpc":"2.0","id":1,"method":"thread/start","params":{"model":"custom-reasoning-model","config":{"model_reasoning_effort":"none"}}}` + "\n")
	params := decodeForwarded(t, testRouter().clientLine(line))["params"].(map[string]any)
	config := params["config"].(map[string]any)
	if config["model_reasoning_effort"] != "none" {
		t.Fatalf("non-thinking effort = %#v, want none", config["model_reasoning_effort"])
	}
}

func TestRouterBlocksProviderChangeWithinThread(t *testing.T) {
	t.Parallel()
	router := testRouter()
	router.observeServerLine([]byte(`{"jsonrpc":"2.0","method":"thread/started","params":{"thread":{"id":"thread-1","modelProvider":"openai"}}}` + "\n"))
	line := []byte(`{"jsonrpc":"2.0","id":2,"method":"turn/start","params":{"threadId":"thread-1","model":"custom-reasoning-model","input":[]}}` + "\n")
	action := router.clientLine(line)
	if len(action.forward) != 0 || !strings.Contains(string(action.reply), "cannot change") {
		t.Fatalf("provider change was not blocked: %#v", action)
	}
}

func TestRouterNormalizesConfiguredTurnEffort(t *testing.T) {
	t.Parallel()
	router := testRouter()
	router.observeServerLine([]byte(`{"jsonrpc":"2.0","id":1,"result":{"thread":{"id":"thread-custom","modelProvider":"third_party"}}}` + "\n"))
	line := []byte(`{"jsonrpc":"2.0","id":2,"method":"turn/start","params":{"threadId":"thread-custom","effort":"medium","input":[]}}` + "\n")
	params := decodeForwarded(t, router.clientLine(line))["params"].(map[string]any)
	if params["effort"] != "high" {
		t.Fatalf("effort = %#v, want high", params["effort"])
	}
}

func TestRouterUsesObservedModelWhenProviderHasMultipleModels(t *testing.T) {
	t.Parallel()
	cfg := testCatalogConfig()
	alternate := testModelSpec()
	alternate.Catalog["display_name"] = "Alternate Model"
	alternate.Catalog["default_reasoning_level"] = "low"
	alternate.ReasoningEffortMap = map[string]string{
		"low":    "low",
		"medium": "low",
	}
	cfg.Models["alternate-model"] = alternate
	router := newRouter(cfg)
	router.observeServerLine([]byte(`{"jsonrpc":"2.0","id":1,"result":{"thread":{"id":"thread-alternate","model":"alternate-model","modelProvider":"third_party"}}}` + "\n"))

	line := []byte(`{"jsonrpc":"2.0","id":2,"method":"turn/start","params":{"threadId":"thread-alternate","effort":"medium","input":[]}}` + "\n")
	params := decodeForwarded(t, router.clientLine(line))["params"].(map[string]any)
	if params["effort"] != "low" {
		t.Fatalf("effort = %#v, want model-specific low", params["effort"])
	}
}

func TestRouterDoesNotGuessModelFromSharedProvider(t *testing.T) {
	t.Parallel()
	cfg := testCatalogConfig()
	alternate := testModelSpec()
	alternate.Catalog["display_name"] = "Alternate Model"
	cfg.Models["alternate-model"] = alternate
	router := newRouter(cfg)
	router.observeServerLine([]byte(`{"jsonrpc":"2.0","id":1,"result":{"thread":{"id":"thread-unknown","modelProvider":"third_party"}}}` + "\n"))

	line := []byte(`{"jsonrpc":"2.0","id":2,"method":"turn/start","params":{"threadId":"thread-unknown","effort":"medium","input":[]}}` + "\n")
	action := router.clientLine(line)
	if string(action.forward) != string(line) || len(action.reply) != 0 {
		t.Fatalf("ambiguous provider should pass through unchanged: %#v", action)
	}
}

func TestRouterNormalizesCollaborationModeEffort(t *testing.T) {
	t.Parallel()
	router := testRouter()
	router.observeServerLine([]byte(`{"jsonrpc":"2.0","id":1,"result":{"thread":{"id":"thread-custom","model":"custom-reasoning-model","modelProvider":"third_party"}}}` + "\n"))
	line := []byte(`{"jsonrpc":"2.0","id":2,"method":"thread/settings/update","params":{"threadId":"thread-custom","effort":"low","collaborationMode":{"mode":"default","settings":{"model":"custom-reasoning-model","reasoning_effort":"xhigh"}}}}` + "\n")
	params := decodeForwarded(t, router.clientLine(line))["params"].(map[string]any)
	settings := params["collaborationMode"].(map[string]any)["settings"].(map[string]any)
	if settings["reasoning_effort"] != "high" || params["effort"] != "low" {
		t.Fatalf("unexpected collaboration effort mapping: %#v", params)
	}
}

func TestRouterResumePreservesProviderAndEffortDefaults(t *testing.T) {
	t.Parallel()
	router := testRouter()
	line := []byte("  " + `{"jsonrpc":"2.0","id":3,"method":"thread/resume","params":{"threadId":"old","model":"custom-reasoning-model"}}` + "  \n")
	action := router.clientLine(line)
	if string(action.forward) != string(line) {
		t.Fatalf("resume was unexpectedly rewritten: got %q", action.forward)
	}
}

func TestRouterForkWithConfiguredModelGetsNewBinding(t *testing.T) {
	t.Parallel()
	line := []byte(`{"jsonrpc":"2.0","id":4,"method":"thread/fork","params":{"threadId":"source","model":"custom-reasoning-model"}}` + "\n")
	params := decodeForwarded(t, testRouter().clientLine(line))["params"].(map[string]any)
	if params["modelProvider"] != testProviderID {
		t.Fatalf("fork provider = %#v", params["modelProvider"])
	}
	if params["config"].(map[string]any)["model_reasoning_effort"] != "high" {
		t.Fatalf("fork default effort = %#v", params["config"])
	}
}

func TestRouterRejectsConflictingExplicitProvider(t *testing.T) {
	t.Parallel()
	line := []byte(`{"jsonrpc":"2.0","id":5,"method":"thread/start","params":{"model":"custom-reasoning-model","modelProvider":"openai"}}` + "\n")
	action := testRouter().clientLine(line)
	if len(action.forward) != 0 || !strings.Contains(string(action.reply), "requires modelProvider") {
		t.Fatalf("provider conflict was not rejected: %#v", action)
	}
}

func decodeForwarded(t *testing.T, action clientAction) map[string]any {
	t.Helper()
	if len(action.reply) != 0 {
		t.Fatalf("unexpected local reply: %s", action.reply)
	}
	if len(action.forward) == 0 {
		t.Fatal("missing forwarded message")
	}
	var message map[string]any
	if err := json.Unmarshal(action.forward, &message); err != nil {
		t.Fatalf("decode forwarded message: %v", err)
	}
	return message
}
