package modelcatalog

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// statsServer exposes token-speed statistics over a unix socket so curl can
// query it without any TCP listener.
type statsServer struct {
	store      *statsStore
	socketPath string
	server     *http.Server
	listener   net.Listener
}

func startStatsServer(store *statsStore, socketPath string) (*statsServer, error) {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return nil, fmt.Errorf("create stats socket directory: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		if !staleUnixSocket(socketPath) {
			return nil, err
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, fmt.Errorf("remove stale stats socket: %w", err)
		}
		listener, err = net.Listen("unix", socketPath)
		if err != nil {
			return nil, err
		}
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, fmt.Errorf("restrict stats socket permissions: %w", err)
	}

	mux := http.NewServeMux()
	server := &statsServer{
		store:      store,
		socketPath: socketPath,
		server:     &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second},
		listener:   listener,
	}
	mux.HandleFunc("/", server.handleStats)
	mux.HandleFunc("/stats", server.handleStats)
	mux.HandleFunc("/debug", server.handleDebug)
	go func() { _ = server.server.Serve(listener) }()
	return server, nil
}

// staleUnixSocket reports whether path is a leftover unix socket with no live
// listener. A regular file is never removed.
func staleUnixSocket(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return false
	}
	conn, err := net.DialTimeout("unix", path, 200*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return false
	}
	return true
}

func (s *statsServer) handleStats(w http.ResponseWriter, r *http.Request) {
	snapshot := s.store.snapshot()
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshot)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, renderStatsText(snapshot))
}

func (s *statsServer) handleDebug(w http.ResponseWriter, r *http.Request) {
	debug := s.store.debug()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "observed server notifications:\n")
	for _, method := range []string{"thread/tokenUsage/updated", "turn/started", "turn/completed"} {
		fmt.Fprintf(w, "  %-28s %d\n", method, debug.Methods[method])
	}
	fmt.Fprintf(w, "skipped usage events without a turn: %d\n", debug.SkippedUsageEvents)
}

func (s *statsServer) Close() error {
	if s == nil {
		return nil
	}
	_ = s.server.Close()
	_ = s.listener.Close()
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func renderStatsText(snapshot statsSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "TOKEN SPEED · LAST %s\n",
		formatHistoryDuration(time.Duration(snapshot.WindowSeconds)*time.Second))
	fmt.Fprintf(&b, "Token-weighted average · updated %s\n",
		snapshot.UpdatedAt.Local().Format("15:04:05"))
	if len(snapshot.Models) == 0 {
		b.WriteString("\nNo completed runs with token usage recorded yet.\n")
		return b.String()
	}

	models := append([]modelStatsSnapshot(nil), snapshot.Models...)
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].OutputTokens == models[j].OutputTokens {
			return models[i].Model < models[j].Model
		}
		return models[i].OutputTokens > models[j].OutputTokens
	})

	maxRate := 0.0
	var totalTokens uint64
	totalRuns := 0
	for _, model := range models {
		maxRate = max(maxRate, model.TokenWeightedTokensPerSecond)
		totalTokens += model.OutputTokens
		totalRuns += model.Samples
	}

	b.WriteByte('\n')
	writer := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "#\tMODEL\tRELATIVE SPEED\tTOK/S\tTOKENS\tRUNS")
	for i, model := range models {
		fmt.Fprintf(writer, "%d\t%s\t%s\t%.1f\t%s\t%d\n",
			i+1,
			model.Model,
			renderRelativeBar(model.TokenWeightedTokensPerSecond, maxRate, 24),
			model.TokenWeightedTokensPerSecond,
			formatUint(model.OutputTokens),
			model.Samples)
	}
	_ = writer.Flush()
	fmt.Fprintf(&b, "\n%d runs · %s output tokens\n", totalRuns, formatUint(totalTokens))
	return b.String()
}

func windowRate(model modelStatsSnapshot, label string) float64 {
	for _, window := range model.Windows {
		if window.Label == label {
			return window.TokensPerSecond
		}
	}
	return model.TokensPerSecond
}

func renderRelativeBar(value, maximum float64, width int) string {
	if value <= 0 || maximum <= 0 || width <= 0 {
		return "—"
	}
	const unitsPerBlock = 8
	units := int(math.Round(value / maximum * float64(width*unitsPerBlock)))
	if units < 1 {
		units = 1
	}
	if units > width*unitsPerBlock {
		units = width * unitsPerBlock
	}

	fullBlocks := units / unitsPerBlock
	partialBlock := units % unitsPerBlock
	bar := strings.Repeat("█", fullBlocks)
	if partialBlock > 0 {
		bar += string([]rune("▏▎▍▌▋▊▉")[partialBlock-1])
	}
	return bar
}

func formatHistoryDuration(duration time.Duration) string {
	if duration%time.Hour == 0 {
		hours := int(duration / time.Hour)
		unit := "HOUR"
		if hours != 1 {
			unit = "HOURS"
		}
		return fmt.Sprintf("%d %s", hours, unit)
	}
	if duration%time.Minute == 0 {
		minutes := int(duration / time.Minute)
		unit := "MINUTE"
		if minutes != 1 {
			unit = "MINUTES"
		}
		return fmt.Sprintf("%d %s", minutes, unit)
	}
	return strings.ToUpper(duration.Round(time.Second).String())
}

func formatUint(value uint64) string {
	digits := strconv.FormatUint(value, 10)
	if len(digits) <= 3 {
		return digits
	}

	firstGroup := len(digits) % 3
	if firstGroup == 0 {
		firstGroup = 3
	}
	var b strings.Builder
	b.Grow(len(digits) + (len(digits)-1)/3)
	b.WriteString(digits[:firstGroup])
	for i := firstGroup; i < len(digits); i += 3 {
		b.WriteByte(',')
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}
