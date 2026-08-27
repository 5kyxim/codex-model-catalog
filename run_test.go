package modelcatalog

import "testing"

func TestInsertCatalogOverrideAfterAppServer(t *testing.T) {
	t.Parallel()
	args := []string{"-c", "features.code_mode_host=true", "app-server", "--analytics-default-enabled", "-c", "mcp_servers.codex_app={}"}
	got := insertCatalogOverride(args, 2, "/tmp/model catalog.json")
	want := []string{"-c", "features.code_mode_host=true", "app-server", "-c", `model_catalog_json="/tmp/model catalog.json"`, "--analytics-default-enabled", "-c", "mcp_servers.codex_app={}"}
	if len(got) != len(want) {
		t.Fatalf("args = %#v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("arg %d = %q, want %q", index, got[index], want[index])
		}
	}
}
