package modelcatalog

import (
	"fmt"
	"os"
	"path/filepath"
)

const defaultRealCodex = "/Applications/Codex.app/Contents/Resources/codex"

type paths struct {
	codexHome   string
	cache       string
	catalog     string
	config      string
	statsSocket string
	statsLog    string
	realCodex   string
}

func defaultPaths() (paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return paths{}, fmt.Errorf("find user home: %w", err)
	}

	codexHome := filepath.Join(home, ".codex")
	realCodex := os.Getenv("CODEX_MODEL_CATALOG_REAL_CODEX")
	if realCodex == "" {
		realCodex = defaultRealCodex
	}

	return paths{
		codexHome:   codexHome,
		cache:       filepath.Join(codexHome, "models_cache.json"),
		catalog:     filepath.Join(codexHome, "model-catalog.json"),
		config:      filepath.Join(codexHome, "model-catalog-routes.json"),
		statsSocket: filepath.Join(codexHome, "codex-model-catalog.sock"),
		statsLog:    filepath.Join(codexHome, "codex-model-catalog-stats.jsonl"),
		realCodex:   realCodex,
	}, nil
}
