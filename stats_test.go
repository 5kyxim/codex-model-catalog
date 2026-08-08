package modelcatalog

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func completeTurn(store *statsStore, threadID, turnID, model string, tokens uint64, at time.Time) {
	key := turnKey{threadID: threadID, turnID: turnID}
	store.turnStarted(key, at.Add(-time.Second))
	store.addUsage(key, &tokenUsageInfo{
		Total: &tokenUsage{TotalTokens: tokens},
		Last:  &tokenUsage{OutputTokens: tokens},
	}, at.Add(-500*time.Millisecond))
	store.turnCompleted(key, "completed", model, at)
}

func TestStatsTurnLifecycle(t *testing.T) {
	t.Parallel()
	store := newStatsStore(30*time.Minute, 200)
	start := time.Unix(1700000000, 0)
	key := turnKey{threadID: "thread-1", turnID: "turn-1"}

	store.turnStarted(key, start)
	store.addUsage(key, &tokenUsageInfo{
		Total: &tokenUsage{TotalTokens: 100},
		Last:  &tokenUsage{OutputTokens: 20, ReasoningOutputTokens: 10},
	}, start.Add(time.Second))
	store.turnCompleted(key, "completed", "deepseek-v4-flash", start.Add(2*time.Second))

	snapshot := store.snapshotAt(start.Add(2 * time.Second))
	if len(snapshot.Models) != 1 {
		t.Fatalf("models = %#v, want one", snapshot.Models)
	}
	model := snapshot.Models[0]
	if model.Model != "deepseek-v4-flash" {
		t.Fatalf("model = %q", model.Model)
	}
	if model.Samples != 1 || model.OutputTokens != 30 {
		t.Fatalf("sample = %#v, want 1 sample / 30 tokens", model)
	}
	if model.TokensPerSecond != 15 {
		t.Fatalf("tokens per second = %v, want 15", model.TokensPerSecond)
	}
}

func TestStatsActiveTurnFallback(t *testing.T) {
	t.Parallel()
	store := newStatsStore(30*time.Minute, 200)
	start := time.Unix(1700000000, 0)

	store.turnStarted(turnKey{threadID: "thread-1", turnID: "turn-1"}, start)
	if got := store.activeTurnFor("thread-1"); got != "turn-1" {
		t.Fatalf("active turn = %q, want turn-1", got)
	}
	store.turnCompleted(turnKey{threadID: "thread-1", turnID: "turn-1"}, "completed", "m", start.Add(2*time.Second))
	if got := store.activeTurnFor("thread-1"); got != "" {
		t.Fatalf("active turn after completion = %q, want empty", got)
	}
}

func TestStatsDebugCounters(t *testing.T) {
	t.Parallel()
	store := newDefaultStatsStore()
	store.noteMethod("thread/tokenUsage/updated")
	store.noteMethod("thread/tokenUsage/updated")
	store.noteMethod("turn/started")
	store.noteSkippedUsage()

	debug := store.debug()
	if debug.Methods["thread/tokenUsage/updated"] != 2 || debug.Methods["turn/started"] != 1 {
		t.Fatalf("method counts = %#v", debug.Methods)
	}
	if debug.SkippedUsageEvents != 1 {
		t.Fatalf("skipped usage events = %d", debug.SkippedUsageEvents)
	}
}

func TestStatsDeduplicatesRepeatedUsage(t *testing.T) {
	t.Parallel()
	store := newStatsStore(30*time.Minute, 200)
	start := time.Unix(1700000000, 0)
	key := turnKey{threadID: "thread-1", turnID: "turn-1"}
	usage := &tokenUsageInfo{
		Total: &tokenUsage{TotalTokens: 100},
		Last:  &tokenUsage{OutputTokens: 20, ReasoningOutputTokens: 10},
	}

	store.turnStarted(key, start)
	store.addUsage(key, usage, start.Add(time.Second))
	store.addUsage(key, usage, start.Add(2*time.Second))
	store.turnCompleted(key, "completed", "m", start.Add(3*time.Second))

	snapshot := store.snapshotAt(start.Add(3 * time.Second))
	if got := snapshot.Models[0].OutputTokens; got != 30 {
		t.Fatalf("output tokens = %d, want 30 (duplicate usage counted twice)", got)
	}
}

func TestStatsCountsMultipleResponsesPerTurn(t *testing.T) {
	t.Parallel()
	store := newStatsStore(30*time.Minute, 200)
	start := time.Unix(1700000000, 0)
	key := turnKey{threadID: "thread-1", turnID: "turn-1"}

	store.turnStarted(key, start)
	store.addUsage(key, &tokenUsageInfo{
		Total: &tokenUsage{TotalTokens: 100},
		Last:  &tokenUsage{OutputTokens: 20},
	}, start.Add(time.Second))
	store.addUsage(key, &tokenUsageInfo{
		Total: &tokenUsage{TotalTokens: 180},
		Last:  &tokenUsage{OutputTokens: 25},
	}, start.Add(2*time.Second))
	store.turnCompleted(key, "completed", "m", start.Add(3*time.Second))

	snapshot := store.snapshotAt(start.Add(3 * time.Second))
	if got := snapshot.Models[0].OutputTokens; got != 45 {
		t.Fatalf("output tokens = %d, want 45 across two responses", got)
	}
}

func TestStatsSkipsFailedAndEmptyTurns(t *testing.T) {
	t.Parallel()
	store := newStatsStore(30*time.Minute, 200)
	start := time.Unix(1700000000, 0)

	store.turnStarted(turnKey{threadID: "t", turnID: "failed"}, start)
	store.addUsage(turnKey{threadID: "t", turnID: "failed"}, &tokenUsageInfo{
		Total: &tokenUsage{TotalTokens: 10},
		Last:  &tokenUsage{OutputTokens: 5},
	}, start.Add(time.Second))
	store.turnCompleted(turnKey{threadID: "t", turnID: "failed"}, "failed", "m", start.Add(2*time.Second))

	store.turnStarted(turnKey{threadID: "t", turnID: "empty"}, start)
	store.addUsage(turnKey{threadID: "t", turnID: "empty"}, &tokenUsageInfo{
		Total: &tokenUsage{TotalTokens: 10},
		Last:  &tokenUsage{},
	}, start.Add(time.Second))
	store.turnCompleted(turnKey{threadID: "t", turnID: "empty"}, "completed", "m", start.Add(2*time.Second))

	if snapshot := store.snapshotAt(start.Add(2 * time.Second)); len(snapshot.Models) != 0 {
		t.Fatalf("models = %#v, want none", snapshot.Models)
	}
}

func TestStatsRollingWindowPrunesOldSamples(t *testing.T) {
	t.Parallel()
	store := newStatsStore(10*time.Minute, 200)
	base := time.Unix(1700000000, 0)
	completeTurn(store, "t1", "turn-1", "m", 10, base)
	completeTurn(store, "t2", "turn-2", "m", 20, base.Add(11*time.Minute))

	snapshot := store.snapshotAt(base.Add(11 * time.Minute))
	if len(snapshot.Models) != 1 {
		t.Fatalf("models = %#v, want one", snapshot.Models)
	}
	if got := snapshot.Models[0]; got.Samples != 1 || got.OutputTokens != 20 {
		t.Fatalf("model = %#v, want only the newer sample", got)
	}
}

func TestStatsCapsSamples(t *testing.T) {
	t.Parallel()
	store := newStatsStore(time.Hour, 3)
	base := time.Unix(1700000000, 0)
	for i := 0; i < 5; i++ {
		completeTurn(store, "t", "turn-"+string(rune('a'+i)), "m", 10, base.Add(time.Duration(i)*time.Second))
	}

	snapshot := store.snapshotAt(base.Add(5 * time.Second))
	if got := snapshot.Models[0].Samples; got != 3 {
		t.Fatalf("samples = %d, want capped at 3", got)
	}
}

func TestStatsSnapshotGroupsByModel(t *testing.T) {
	t.Parallel()
	store := newStatsStore(30*time.Minute, 200)
	base := time.Unix(1700000000, 0)
	completeTurn(store, "t1", "turn-1", "alpha", 30, base.Add(time.Second))
	completeTurn(store, "t2", "turn-2", "beta", 10, base.Add(2*time.Second))
	completeTurn(store, "t3", "turn-3", "alpha", 10, base.Add(3*time.Second))

	snapshot := store.snapshotAt(base.Add(3 * time.Second))
	if len(snapshot.Models) != 2 {
		t.Fatalf("models = %#v, want alpha and beta", snapshot.Models)
	}
	alpha := snapshot.Models[0]
	if alpha.Model != "alpha" || alpha.Samples != 2 || alpha.OutputTokens != 40 {
		t.Fatalf("alpha = %#v", alpha)
	}
}

func TestStatsRateWindowsAndSparkline(t *testing.T) {
	t.Parallel()
	store := newStatsStore(24*time.Hour, 10000)
	base := time.Unix(1700000000, 0)
	completeTurn(store, "t1", "turn-old", "alpha", 1000, base.Add(-23*time.Hour-30*time.Minute))
	completeTurn(store, "t2", "turn-mid", "alpha", 600, base.Add(-2*time.Hour-time.Minute))
	completeTurn(store, "t3", "turn-recent", "alpha", 300, base.Add(-10*time.Minute))

	model := store.snapshotAt(base).Models[0]
	if got := windowRate(model, "15m"); got != 300 {
		t.Fatalf("15m rate = %v, want 300", got)
	}
	if got := windowRate(model, "1h"); got != 300 {
		t.Fatalf("1h rate = %v, want 300", got)
	}
	if got := windowRate(model, "6h"); got != 450 {
		t.Fatalf("6h rate = %v, want 450", got)
	}
	if got := windowRate(model, "24h"); got != 1900.0/3.0 {
		t.Fatalf("24h rate = %v, want %v", got, 1900.0/3.0)
	}
	if model.Samples != 3 || model.OutputTokens != 1900 {
		t.Fatalf("24h aggregate = %#v", model)
	}
	if len(model.Sparkline) != 24 {
		t.Fatalf("sparkline length = %d, want 24", len(model.Sparkline))
	}
	if got := model.Sparkline[0]; got != 5 {
		t.Fatalf("most recent sparkline bucket = %v, want 5 tokens/min", got)
	}
	if got := model.Sparkline[2]; got != 10 {
		t.Fatalf("3-hour-old sparkline bucket = %v, want 10 tokens/min", got)
	}
	if got := model.Sparkline[23]; got != 1000.0/60.0 {
		t.Fatalf("oldest sparkline bucket = %v, want %v", got, 1000.0/60.0)
	}
}

func TestStatsPersistenceRoundTrip(t *testing.T) {
	t.Parallel()
	logPath := filepath.Join(t.TempDir(), "stats.jsonl")
	base := time.Unix(1700000000, 0)

	store := newStatsStore(24*time.Hour, 10000)
	if err := store.useLogAt(logPath, base); err != nil {
		t.Fatal(err)
	}
	completeTurn(store, "t1", "turn-1", "alpha", 300, base.Add(-5*time.Minute))
	completeTurn(store, "t2", "turn-2", "beta", 100, base.Add(-10*time.Minute))

	loaded := newStatsStore(24*time.Hour, 10000)
	if err := loaded.useLogAt(logPath, base); err != nil {
		t.Fatal(err)
	}
	snapshot := loaded.snapshotAt(base)
	if len(snapshot.Models) != 2 {
		t.Fatalf("models = %#v, want alpha and beta after reload", snapshot.Models)
	}
	alpha := snapshot.Models[0]
	beta := snapshot.Models[1]
	if alpha.Model != "alpha" || alpha.Samples != 1 || alpha.OutputTokens != 300 || alpha.TokensPerSecond != 300 {
		t.Fatalf("alpha = %#v", alpha)
	}
	if beta.Model != "beta" || beta.Samples != 1 || beta.OutputTokens != 100 || beta.TokensPerSecond != 100 {
		t.Fatalf("beta = %#v", beta)
	}
}

func TestStatsPersistenceDropsSamplesOlderThanWindow(t *testing.T) {
	t.Parallel()
	logPath := filepath.Join(t.TempDir(), "stats.jsonl")
	base := time.Unix(1700000000, 0)

	store := newStatsStore(24*time.Hour, 10000)
	if err := store.useLogAt(logPath, base); err != nil {
		t.Fatal(err)
	}
	completeTurn(store, "t1", "turn-old", "alpha", 100, base.Add(-25*time.Hour))
	completeTurn(store, "t2", "turn-recent", "beta", 200, base.Add(-time.Hour))

	loaded := newStatsStore(24*time.Hour, 10000)
	if err := loaded.useLogAt(logPath, base); err != nil {
		t.Fatal(err)
	}
	snapshot := loaded.snapshotAt(base)
	if len(snapshot.Models) != 1 || snapshot.Models[0].Model != "beta" {
		t.Fatalf("models = %#v, want only beta", snapshot.Models)
	}
}

func TestFlexibleTimeUnmarshal(t *testing.T) {
	t.Parallel()

	var unix flexibleTime
	if err := json.Unmarshal([]byte(`1700000000`), &unix); err != nil {
		t.Fatal(err)
	}
	if got := unix.Unix(); got != 1700000000 {
		t.Fatalf("unix time = %d", got)
	}

	var rfc3339 flexibleTime
	if err := json.Unmarshal([]byte(`"2023-11-14T22:13:20Z"`), &rfc3339); err != nil {
		t.Fatal(err)
	}
	if got := rfc3339.Unix(); got != 1700000000 {
		t.Fatalf("rfc3339 time = %d", got)
	}
}

func TestRenderSparkline(t *testing.T) {
	t.Parallel()
	if got, want := renderSparkline([]float64{0, 50, 100}), "▁▄█"; got != want {
		t.Fatalf("sparkline = %q, want %q", got, want)
	}
	if got, want := renderSparkline([]float64{0, 0}), "▁▁"; got != want {
		t.Fatalf("zero sparkline = %q, want %q", got, want)
	}
}
