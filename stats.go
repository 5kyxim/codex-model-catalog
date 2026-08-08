package modelcatalog

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultStatsWindow     = 24 * time.Hour
	defaultMaxStatsSamples = 10000
	maxStatsLogBytes       = 4 << 20
	hourlySparklineBuckets = 24
	maxActiveTurns         = 256
	maxActiveThreadTurns   = 4096
)

// tokenUsage mirrors the app-server TokenUsageBreakdown object carried by
// thread/tokenUsage/updated notifications.
type tokenUsage struct {
	InputTokens           uint64 `json:"inputTokens"`
	CachedInputTokens     uint64 `json:"cachedInputTokens"`
	CacheWriteInputTokens uint64 `json:"cacheWriteInputTokens"`
	OutputTokens          uint64 `json:"outputTokens"`
	ReasoningOutputTokens uint64 `json:"reasoningOutputTokens"`
	TotalTokens           uint64 `json:"totalTokens"`
}

type tokenUsageInfo struct {
	Total              *tokenUsage `json:"total"`
	Last               *tokenUsage `json:"last"`
	ModelContextWindow *int64      `json:"modelContextWindow"`
}

type statsEventEnvelope struct {
	Method string           `json:"method"`
	Params statsEventParams `json:"params"`
}

type statsEventParams struct {
	ThreadID   string          `json:"threadId"`
	TurnID     string          `json:"turnId"`
	Turn       statsTurnFields `json:"turn"`
	TokenUsage *tokenUsageInfo `json:"tokenUsage"`
}

type statsTurnFields struct {
	ID          string       `json:"id"`
	Status      string       `json:"status"`
	StartedAt   flexibleTime `json:"startedAt,omitempty"`
	CompletedAt flexibleTime `json:"completedAt,omitempty"`
}

// flexibleTime accepts both RFC 3339 strings and Unix timestamps, because the
// app-server JSONL uses either form for turn timing fields.
type flexibleTime struct {
	time.Time
}

func (t *flexibleTime) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	var unix int64
	if json.Unmarshal(data, &unix) == nil {
		t.Time = time.Unix(unix, 0)
		return nil
	}
	var value string
	if json.Unmarshal(data, &value) == nil {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return err
		}
		t.Time = parsed
		return nil
	}
	return errors.New("invalid event time")
}

type turnKey struct {
	threadID string
	turnID   string
}

type turnState struct {
	outputTokens     uint64
	lastTotalTokens  uint64
	lastOutputTokens uint64
	lastReasoning    uint64
	startedAt        time.Time
}

type turnSample struct {
	model        string
	outputTokens uint64
	duration     time.Duration
	endedAt      time.Time
}

// rateWindowSnapshot is one model's pooled rate over a recent time window.
// Pooled means total output tokens divided by total turn duration, which is
// more stable than averaging the per-turn tokens/s values.
type rateWindowSnapshot struct {
	Label           string  `json:"label"`
	Seconds         int64   `json:"seconds"`
	Samples         int     `json:"samples"`
	OutputTokens    uint64  `json:"output_tokens"`
	TokensPerSecond float64 `json:"tokens_per_second"`
}

// modelStatsSnapshot is one model's rolling token-speed summary.
type modelStatsSnapshot struct {
	Model           string               `json:"model"`
	Samples         int                  `json:"samples"`
	OutputTokens    uint64               `json:"output_tokens"`
	TokensPerSecond float64              `json:"tokens_per_second"`
	WindowStart     time.Time            `json:"window_start"`
	WindowEnd       time.Time            `json:"window_end"`
	Windows         []rateWindowSnapshot `json:"windows,omitempty"`
	Sparkline       []float64            `json:"sparkline,omitempty"`
}

// statsSnapshot is the complete read-only view served over the unix socket.
type statsSnapshot struct {
	UpdatedAt     time.Time            `json:"updated_at"`
	WindowSeconds int64                `json:"window_seconds"`
	Models        []modelStatsSnapshot `json:"models"`
}

// statsStore keeps small per-turn samples in memory and optionally appends
// them to a JSONL log so the 24-hour view survives process restarts.
type statsStore struct {
	mu         sync.Mutex
	window     time.Duration
	maxSamples int
	samples    []turnSample
	turns      map[turnKey]*turnState
	turnOrder  []turnKey
	activeTurn map[string]string

	methodCounts       map[string]int
	skippedUsageEvents int

	logPath  string
	logBytes int64
}

type statsLogRecord struct {
	EndedAt      time.Time `json:"ended_at"`
	Model        string    `json:"model"`
	OutputTokens uint64    `json:"output_tokens"`
	DurationMS   int64     `json:"duration_ms"`
}

type statsAggregate struct {
	samples  int
	tokens   uint64
	duration time.Duration
	start    time.Time
	end      time.Time
}

func newDefaultStatsStore() *statsStore {
	return newStatsStore(defaultStatsWindow, defaultMaxStatsSamples)
}

func newStatsStore(window time.Duration, maxSamples int) *statsStore {
	if window <= 0 {
		window = defaultStatsWindow
	}
	if maxSamples < 1 {
		maxSamples = defaultMaxStatsSamples
	}
	return &statsStore{
		window:       window,
		maxSamples:   maxSamples,
		turns:        make(map[turnKey]*turnState),
		activeTurn:   make(map[string]string),
		methodCounts: make(map[string]int),
	}
}

func (s *statsStore) turnStarted(key turnKey, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.activeTurn[key.threadID]; !exists && len(s.activeTurn) >= maxActiveThreadTurns {
		for threadID := range s.activeTurn {
			delete(s.activeTurn, threadID)
			break
		}
	}
	s.activeTurn[key.threadID] = key.turnID
	state := s.turns[key]
	if state == nil {
		state = &turnState{}
		s.addTurnLocked(key, state)
	}
	if state.startedAt.IsZero() {
		state.startedAt = at
	}
}

func (s *statsStore) addUsage(key turnKey, info *tokenUsageInfo, at time.Time) {
	if info == nil || info.Last == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.turns[key]
	if state == nil {
		state = &turnState{}
		s.addTurnLocked(key, state)
	}
	if state.startedAt.IsZero() {
		state.startedAt = at
	}

	last := info.Last
	output := last.OutputTokens
	reasoning := last.ReasoningOutputTokens
	var total uint64
	if info.Total != nil {
		total = info.Total.TotalTokens
	}

	// total.totalTokens grows monotonically with each response in a turn. Add
	// the response's generated tokens only when the thread total moved, so
	// repeated notifications for the same response are not counted twice. If
	// the total is unavailable, fall back to a changed usage tuple.
	newResponse := total > state.lastTotalTokens ||
		(total == 0 && (output != state.lastOutputTokens || reasoning != state.lastReasoning))
	if newResponse && output+reasoning > 0 {
		state.outputTokens += output + reasoning
	}
	if total > state.lastTotalTokens {
		state.lastTotalTokens = total
	}
	state.lastOutputTokens = output
	state.lastReasoning = reasoning
}

func (s *statsStore) turnCompleted(key turnKey, status, model string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeTurn[key.threadID] == key.turnID {
		delete(s.activeTurn, key.threadID)
	}
	state := s.turns[key]
	delete(s.turns, key)
	s.removeTurnLocked(key)
	if state == nil || status != "completed" || state.outputTokens == 0 {
		return
	}
	duration := at.Sub(state.startedAt)
	if duration <= 0 {
		duration = time.Millisecond
	}
	if model == "" {
		model = "unknown"
	}
	sample := turnSample{
		model:        model,
		outputTokens: state.outputTokens,
		duration:     duration,
		endedAt:      at,
	}
	s.samples = append(s.samples, sample)
	s.pruneLocked(at)
	s.persistSampleLocked(sample, at)
}

func (s *statsStore) activeTurnFor(threadID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeTurn[threadID]
}

func (s *statsStore) noteMethod(method string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.methodCounts) >= 64 {
		for existing := range s.methodCounts {
			delete(s.methodCounts, existing)
			break
		}
	}
	s.methodCounts[method]++
}

func (s *statsStore) noteSkippedUsage() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skippedUsageEvents++
}

type statsDebug struct {
	Methods            map[string]int `json:"methods"`
	SkippedUsageEvents int            `json:"skipped_usage_events"`
}

func (s *statsStore) debug() statsDebug {
	s.mu.Lock()
	defer s.mu.Unlock()
	methods := make(map[string]int, len(s.methodCounts))
	for method, count := range s.methodCounts {
		methods[method] = count
	}
	return statsDebug{
		Methods:            methods,
		SkippedUsageEvents: s.skippedUsageEvents,
	}
}

func (s *statsStore) addTurnLocked(key turnKey, state *turnState) {
	if _, exists := s.turns[key]; exists {
		s.turns[key] = state
		return
	}
	if len(s.turns) >= maxActiveTurns && len(s.turnOrder) > 0 {
		oldest := s.turnOrder[0]
		delete(s.turns, oldest)
		s.turnOrder = s.turnOrder[1:]
	}
	s.turns[key] = state
	s.turnOrder = append(s.turnOrder, key)
}

func (s *statsStore) removeTurnLocked(key turnKey) {
	for i, existing := range s.turnOrder {
		if existing == key {
			s.turnOrder = append(s.turnOrder[:i], s.turnOrder[i+1:]...)
			return
		}
	}
}

func (s *statsStore) pruneLocked(at time.Time) {
	cutoff := at.Add(-s.window)
	keep := s.samples[:0]
	for _, sample := range s.samples {
		if !sample.endedAt.Before(cutoff) {
			keep = append(keep, sample)
		}
	}
	s.samples = keep
	if len(s.samples) > s.maxSamples {
		s.samples = append([]turnSample(nil), s.samples[len(s.samples)-s.maxSamples:]...)
	}
}

func (s *statsStore) snapshot() statsSnapshot {
	return s.snapshotAt(time.Now())
}

func (s *statsStore) snapshotAt(at time.Time) statsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(at)

	type rateWindow struct {
		label  string
		window time.Duration
	}
	rateWindows := []rateWindow{
		{"15m", 15 * time.Minute},
		{"1h", time.Hour},
		{"6h", 6 * time.Hour},
		{"24h", 24 * time.Hour},
	}

	type modelAggregate struct {
		total   statsAggregate
		windows []statsAggregate
		buckets [hourlySparklineBuckets]uint64
	}
	byModel := make(map[string]*modelAggregate)
	for _, sample := range s.samples {
		model := byModel[sample.model]
		if model == nil {
			model = &modelAggregate{
				windows: make([]statsAggregate, len(rateWindows)),
			}
			byModel[sample.model] = model
		}
		addSample := func(agg *statsAggregate) {
			agg.samples++
			agg.tokens += sample.outputTokens
			agg.duration += sample.duration
			if agg.start.IsZero() || sample.endedAt.Before(agg.start) {
				agg.start = sample.endedAt
			}
			if sample.endedAt.After(agg.end) {
				agg.end = sample.endedAt
			}
		}
		addSample(&model.total)
		for i, rw := range rateWindows {
			if sample.endedAt.After(at.Add(-rw.window)) {
				addSample(&model.windows[i])
			}
		}
		age := at.Sub(sample.endedAt)
		if age >= 0 {
			bucket := int(age / time.Hour)
			if bucket > 0 && age%time.Hour == 0 {
				bucket--
			}
			if bucket < hourlySparklineBuckets {
				model.buckets[bucket] += sample.outputTokens
			}
		}
	}

	models := make([]modelStatsSnapshot, 0, len(byModel))
	for model, agg := range byModel {
		perSecond := pooledRate(agg.total)
		windows := make([]rateWindowSnapshot, 0, len(rateWindows))
		for i, rw := range rateWindows {
			windows = append(windows, rateWindowSnapshot{
				Label:           rw.label,
				Seconds:         int64(rw.window / time.Second),
				Samples:         agg.windows[i].samples,
				OutputTokens:    agg.windows[i].tokens,
				TokensPerSecond: pooledRate(agg.windows[i]),
			})
		}
		sparkline := make([]float64, hourlySparklineBuckets)
		for i, tokens := range agg.buckets {
			sparkline[i] = float64(tokens) / 60
		}
		models = append(models, modelStatsSnapshot{
			Model:           model,
			Samples:         agg.total.samples,
			OutputTokens:    agg.total.tokens,
			TokensPerSecond: perSecond,
			WindowStart:     agg.total.start,
			WindowEnd:       agg.total.end,
			Windows:         windows,
			Sparkline:       sparkline,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Model < models[j].Model })

	return statsSnapshot{
		UpdatedAt:     at,
		WindowSeconds: int64(s.window / time.Second),
		Models:        models,
	}
}

func pooledRate(agg statsAggregate) float64 {
	if agg.duration <= 0 {
		return 0
	}
	return float64(agg.tokens) / agg.duration.Seconds()
}

// useLog loads prior completed-turn samples from a JSONL log and makes the
// store append new samples there. It is best-effort: a missing log is fine,
// and malformed lines are skipped so one torn write does not lose all history.
func (s *statsStore) useLog(path string) error {
	return s.useLogAt(path, time.Now())
}

func (s *statsStore) useLogAt(path string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logPath = path
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	s.logBytes = info.Size()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	cutoff := at.Add(-s.window)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec statsLogRecord
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		if rec.EndedAt.IsZero() || rec.DurationMS <= 0 || rec.OutputTokens == 0 {
			continue
		}
		model := rec.Model
		if model == "" {
			model = "unknown"
		}
		sample := turnSample{
			model:        model,
			outputTokens: rec.OutputTokens,
			duration:     time.Duration(rec.DurationMS) * time.Millisecond,
			endedAt:      rec.EndedAt,
		}
		if sample.endedAt.Before(cutoff) {
			continue
		}
		s.samples = append(s.samples, sample)
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	sort.Slice(s.samples, func(i, j int) bool {
		return s.samples[i].endedAt.Before(s.samples[j].endedAt)
	})
	if len(s.samples) > s.maxSamples {
		s.samples = append([]turnSample(nil), s.samples[len(s.samples)-s.maxSamples:]...)
	}
	return s.compactLogLocked(at)
}

func (s *statsStore) persistSampleLocked(sample turnSample, at time.Time) {
	if s.logPath == "" {
		return
	}
	rec := statsLogRecord{
		EndedAt:      sample.endedAt,
		Model:        sample.model,
		OutputTokens: sample.outputTokens,
		DurationMS:   sample.duration.Milliseconds(),
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	line = append(line, '\n')
	file, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	n, writeErr := file.Write(line)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return
	}
	s.logBytes += int64(n)
	if s.logBytes >= maxStatsLogBytes {
		_ = s.compactLogLocked(at)
	}
}

func (s *statsStore) compactLogLocked(at time.Time) error {
	if s.logPath == "" {
		return nil
	}
	dir := filepath.Dir(s.logPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".codex-model-catalog-stats-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)

	cutoff := at.Add(-s.window)
	encoder := json.NewEncoder(temp)
	for _, sample := range s.samples {
		if sample.endedAt.Before(cutoff) {
			continue
		}
		if err := encoder.Encode(statsLogRecord{
			EndedAt:      sample.endedAt,
			Model:        sample.model,
			OutputTokens: sample.outputTokens,
			DurationMS:   sample.duration.Milliseconds(),
		}); err != nil {
			_ = temp.Close()
			return err
		}
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		return err
	}
	info, err := os.Stat(tempPath)
	if err != nil {
		return err
	}
	if err := os.Rename(tempPath, s.logPath); err != nil {
		return err
	}
	s.logBytes = info.Size()
	return nil
}
