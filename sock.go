package modelcatalog

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
	fmt.Fprintf(&b, "codex-model-catalog token speed (%s history, updated %s)\n",
		formatDuration(time.Duration(snapshot.WindowSeconds)*time.Second),
		snapshot.UpdatedAt.Local().Format("15:04:05"))
	if len(snapshot.Models) == 0 {
		b.WriteString("no completed turns with token usage recorded yet\n")
		return b.String()
	}
	writer := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "MODEL\t15M TOK/S\t1H TOK/S\t6H TOK/S\t24H TOK/S\tSAMPLES\tTOKENS\t24H SPARK (TOK/MIN)")
	for _, model := range snapshot.Models {
		fmt.Fprintf(writer, "%s\t%.1f\t%.1f\t%.1f\t%.1f\t%d\t%d\t%s\n",
			model.Model,
			windowRate(model, "15m"),
			windowRate(model, "1h"),
			windowRate(model, "6h"),
			windowRate(model, "24h"),
			model.Samples,
			model.OutputTokens,
			renderSparkline(model.Sparkline))
	}
	_ = writer.Flush()
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

func renderSparkline(values []float64) string {
	if len(values) == 0 {
		return ""
	}
	sparkChars := []rune("▁▂▃▄▅▆▇█")
	max := 0.0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	if max <= 0 {
		return strings.Repeat("▁", len(values))
	}
	var b strings.Builder
	for _, value := range values {
		index := int(value / max * float64(len(sparkChars)-1))
		if index < 0 {
			index = 0
		}
		if index >= len(sparkChars) {
			index = len(sparkChars) - 1
		}
		b.WriteRune(sparkChars[index])
	}
	return b.String()
}

func formatDuration(duration time.Duration) string {
	if duration%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(duration/time.Hour))
	}
	if duration%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(duration/time.Minute))
	}
	return duration.Round(time.Second).String()
}
