package modelcatalog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"
)

func benchmarkLine(withThread bool, size int) []byte {
	var msg map[string]any
	if withThread {
		msg = map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "server/event",
			"params": map[string]any{
				"thread": map[string]any{
					"id":            "t_benchmark",
					"model":         "bench-model",
					"modelProvider": "bench-provider",
				},
				"items": []any{map[string]any{"role": "user", "content": strings.Repeat("a", 256)}},
			},
		}
	} else {
		msg = map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "server/event",
			"params": map[string]any{
				"items": []any{map[string]any{"role": "user", "content": strings.Repeat("a", 256)}},
			},
		}
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		panic(err)
	}
	if len(raw) < size {
		raw = append(raw[:len(raw)-1], []byte(`,"padding":"`+strings.Repeat("x", size-len(raw))+`"`)...)
		raw = append(raw, '}')
	}
	return append(raw, '\n')
}

func benchObserveServerLine(b *testing.B, withThread bool) {
	line := benchmarkLine(withThread, 1024)
	router := newRouter(testCatalogConfig())
	b.ReportAllocs()
	b.SetBytes(int64(len(line)))
	for i := 0; i < b.N; i++ {
		router.observeServerLine(line)
	}
}

func BenchmarkObserveServerLineWithThread(b *testing.B) {
	benchObserveServerLine(b, true)
}

func BenchmarkObserveServerLineNoThread(b *testing.B) {
	benchObserveServerLine(b, false)
}

func BenchmarkReadLimitedLine(b *testing.B) {
	line := benchmarkLine(false, 1024)
	data := bytes.Repeat(line, 1000)
	reader := bufio.NewReaderSize(bytes.NewReader(data), 64<<10)
	scratch := make([]byte, 0, 64<<10)
	b.ReportAllocs()
	b.SetBytes(int64(len(line)))
	for i := 0; i < b.N; i++ {
		if _, err := readLimitedLine(reader, clientLineLimit, scratch); err != nil && err != io.EOF {
			b.Fatal(err)
		}
	}
}

func benchPumpServer(b *testing.B, withThread bool) {
	line := benchmarkLine(withThread, 1024)
	data := bytes.Repeat(line, 100)
	router := newRouter(testCatalogConfig())
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		input := bufio.NewReaderSize(bytes.NewReader(data), 64<<10)
		if err := pumpServer(input, &lockedWriter{w: io.Discard}, router); err != nil && err != io.EOF {
			b.Fatal(err)
		}
	}
}

func BenchmarkPumpServerWithThread(b *testing.B) {
	benchPumpServer(b, true)
}

func BenchmarkPumpServerNoThread(b *testing.B) {
	benchPumpServer(b, false)
}

// BenchmarkPumpServerGCProfile runs a fixed 20k-line workload once and reports
// how much allocation and how many GC cycles it caused. Run with -benchtime=1x.
func BenchmarkPumpServerGCProfile(b *testing.B) {
	line := benchmarkLine(true, 1024)
	data := bytes.Repeat(line, 20_000)
	router := newRouter(testCatalogConfig())
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	input := bufio.NewReaderSize(bytes.NewReader(data), 64<<10)
	if err := pumpServer(input, &lockedWriter{w: io.Discard}, router); err != nil && err != io.EOF {
		b.Fatal(err)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	b.StopTimer()

	b.Logf("lines=%d elapsed=%s totalAllocDelta=%d mallocsDelta=%d freesDelta=%d gcCyclesDelta=%d pauseDelta=%s",
		len(data)/len(line),
		time.Since(start),
		after.TotalAlloc-before.TotalAlloc,
		after.Mallocs-before.Mallocs,
		after.Frees-before.Frees,
		after.NumGC-before.NumGC,
		time.Duration(after.PauseTotalNs-before.PauseTotalNs),
	)
}
