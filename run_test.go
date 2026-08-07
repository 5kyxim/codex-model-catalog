package modelcatalog

import "testing"

func TestInsertCatalogOverrideBeforeAppServer(t *testing.T) {
	t.Parallel()
	args := []string{"--enable", "feature", "app-server", "--stdio"}
	got := insertCatalogOverride(args, 2, "/tmp/model catalog.json")
	want := []string{"--enable", "feature", "-c", `model_catalog_json="/tmp/model catalog.json"`, "app-server", "--stdio"}
	if len(got) != len(want) {
		t.Fatalf("args = %#v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("arg %d = %q, want %q", index, got[index], want[index])
		}
	}
}
