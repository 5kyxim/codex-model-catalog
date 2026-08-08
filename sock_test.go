package modelcatalog

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRenderStatsText(t *testing.T) {
	t.Parallel()
	snapshot := statsSnapshot{
		WindowSeconds: 1800,
		Models: []modelStatsSnapshot{
			{
				Model:           "deepseek-v4-flash",
				Samples:         3,
				OutputTokens:    120,
				TokensPerSecond: 40,
				WindowStart:     time.Unix(1700000000, 0),
				WindowEnd:       time.Unix(1700000180, 0),
			},
		},
	}
	text := renderStatsText(snapshot)
	for _, want := range []string{"deepseek-v4-flash", "40.0", "120", "30m history"} {
		if !strings.Contains(text, want) {
			t.Fatalf("stats text missing %q:\n%s", want, text)
		}
	}
}

func TestRenderStatsTextEmpty(t *testing.T) {
	t.Parallel()
	text := renderStatsText(statsSnapshot{WindowSeconds: 1800})
	if !strings.Contains(text, "no completed turns") {
		t.Fatalf("empty stats text = %q", text)
	}
}

func TestStatsHTTPTextAndJSON(t *testing.T) {
	t.Parallel()
	server := &statsServer{store: newDefaultStatsStore()}

	rec := httptest.NewRecorder()
	server.handleStats(rec, httptest.NewRequest("GET", "/stats", nil))
	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/plain") {
		t.Fatalf("text content type = %q", contentType)
	}

	req := httptest.NewRequest("GET", "/stats", nil)
	req.Header.Set("Accept", "application/json")
	rec = httptest.NewRecorder()
	server.handleStats(rec, req)
	if contentType := rec.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("json content type = %q", contentType)
	}
	var snapshot statsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode json stats: %v", err)
	}
	if snapshot.WindowSeconds != int64(defaultStatsWindow/time.Second) {
		t.Fatalf("window seconds = %d", snapshot.WindowSeconds)
	}
}

func TestStatsHTTPDebug(t *testing.T) {
	t.Parallel()
	store := newDefaultStatsStore()
	store.noteMethod("thread/tokenUsage/updated")
	server := &statsServer{store: store}

	rec := httptest.NewRecorder()
	server.handleDebug(rec, httptest.NewRequest("GET", "/debug", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "thread/tokenUsage/updated") || !strings.Contains(body, "1") {
		t.Fatalf("debug body = %q", body)
	}
}

func TestStartStatsServerServesAndCleansUp(t *testing.T) {
	t.Parallel()
	store := newDefaultStatsStore()
	socketPath := shortSocketPath(t)
	server, err := startStatsServer(store, socketPath)
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.DialTimeout("unix", socketPath, time.Second)
			},
		},
	}
	resp, err := client.Get("http://unix/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	if resp.StatusCode != 200 || !strings.Contains(string(body[:n]), "no completed turns") {
		t.Fatalf("status = %d body = %q", resp.StatusCode, body[:n])
	}

	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket file still exists after close: %v", err)
	}
}

func TestStartStatsServerReplacesStaleSocket(t *testing.T) {
	t.Parallel()
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = listener.Close()

	server, err := startStatsServer(newDefaultStatsStore(), socketPath)
	if err != nil {
		t.Fatalf("stale socket was not replaced: %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStartStatsServerRejectsLiveSocket(t *testing.T) {
	t.Parallel()
	socketPath := shortSocketPath(t)
	first, err := startStatsServer(newDefaultStatsStore(), socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	if _, err := startStatsServer(newDefaultStatsStore(), socketPath); err == nil {
		t.Fatal("second server on the same live socket should fail")
	}
}

// shortSocketPath returns a unix socket path well under the macOS 104-byte
// limit; t.TempDir() names are too long for this purpose.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}
