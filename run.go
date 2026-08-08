package modelcatalog

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Run executes the model catalog wrapper and returns its process exit code.
func Run(args []string) int {
	paths, err := defaultPaths()
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex-model-catalog: %v\n", err)
		return 1
	}
	if len(args) == 1 && args[0] == "doctor" {
		return doctor(paths)
	}
	if len(args) == 1 && args[0] == "stats" {
		return statsCLI(paths)
	}

	appServerIndex := indexOf(args, "app-server")
	if appServerIndex < 0 {
		argv := append([]string{paths.realCodex}, args...)
		if err := syscall.Exec(paths.realCodex, argv, os.Environ()); err != nil {
			fmt.Fprintf(os.Stderr, "codex-model-catalog: execute real Codex: %v\n", err)
			return 1
		}
		return 0
	}

	cfg, err := loadCatalogConfig(paths.config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex-model-catalog: %v\n", err)
		return 1
	}
	modelCount, err := refreshCatalog(paths.cache, paths.catalog, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex-model-catalog: %v\n", err)
		return 1
	}
	if err := requireExecutable(paths.realCodex); err != nil {
		fmt.Fprintf(os.Stderr, "codex-model-catalog: %v\n", err)
		return 1
	}

	childArgs := insertCatalogOverride(args, appServerIndex, paths.catalog)
	fmt.Fprintf(os.Stderr, "codex-model-catalog: routing %d custom model(s); catalog has %d models\n", len(cfg.Models), modelCount)

	router := newRouter(cfg)
	if err := router.stats.useLog(paths.statsLog); err != nil {
		fmt.Fprintf(os.Stderr, "codex-model-catalog: stats log %s: %v (continuing with in-memory stats)\n", paths.statsLog, err)
	}
	statsServer, err := startStatsServer(router.stats, paths.statsSocket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codex-model-catalog: stats socket %s: %v (stats disabled)\n", paths.statsSocket, err)
	} else {
		defer statsServer.Close()
	}
	return runAppServer(paths.realCodex, childArgs, router)
}

func statsCLI(paths paths) int {
	store := newDefaultStatsStore()
	if err := store.useLog(paths.statsLog); err != nil {
		fmt.Fprintf(os.Stderr, "codex-model-catalog: read stats log %s: %v\n", paths.statsLog, err)
		return 1
	}
	fmt.Print(renderStatsText(store.snapshot()))
	return 0
}

func doctor(paths paths) int {
	if err := requireExecutable(paths.realCodex); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL real Codex: %v\n", err)
		return 1
	}
	cfg, err := loadCatalogConfig(paths.config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL config: %v\n", err)
		return 1
	}
	count, err := refreshCatalog(paths.cache, paths.catalog, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL catalog: %v\n", err)
		return 1
	}
	version, err := exec.Command(paths.realCodex, "--version").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL real Codex version: %v\n", err)
		return 1
	}

	fmt.Printf("OK real Codex: %s", version)
	fmt.Printf("OK config: %d custom model(s): %v\n", len(cfg.Models), cfg.models())
	fmt.Printf("OK catalog: %d models at %s\n", count, paths.catalog)
	return 0
}

func requireExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("real Codex %q: %w", path, err)
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("real Codex %q is not executable", path)
	}
	return nil
}

func insertCatalogOverride(args []string, appServerIndex int, catalogPath string) []string {
	override := fmt.Sprintf("model_catalog_json=%q", filepath.Clean(catalogPath))
	result := make([]string, 0, len(args)+2)
	result = append(result, args[:appServerIndex]...)
	result = append(result, "-c", override)
	result = append(result, args[appServerIndex:]...)
	return result
}

func indexOf(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}
