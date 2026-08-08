package modelcatalog

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	clientLineLimit    = 64 << 20
	serverObserveLimit = 4 << 20
	// maxReusableLineBuffer caps how much backing array a line buffer may keep
	// between lines. A single huge line should not pin a large allocation for
	// the lifetime of the process.
	maxReusableLineBuffer = 4 << 20
)

var (
	errLineTooLong  = errors.New("JSONL message exceeds 64 MiB")
	errLineStreamed = errors.New("oversized JSONL line streamed")
)

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) write(data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return writeAll(w.w, data)
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func readLimitedLine(reader *bufio.Reader, limit int, scratch []byte) ([]byte, error) {
	return readLine(reader, limit, scratch, nil)
}

// readLine reads one JSONL line into scratch, enforcing limit. When the line
// exceeds the limit and oversized is nil, the read fails with errLineTooLong.
// When oversized is set, the accumulated prefix and over-limit fragment are
// handed to it for streaming, and readLine reports errLineStreamed so the
// caller can move on without treating the line as one to process.
func readLine(reader *bufio.Reader, limit int, scratch []byte, oversized func(prefix, fragment []byte, readErr error) error) ([]byte, error) {
	line := scratch[:0]
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > limit {
			if oversized == nil {
				return nil, errLineTooLong
			}
			if streamErr := oversized(line, fragment, err); streamErr != nil {
				return nil, streamErr
			}
			return nil, errLineStreamed
		}
		line = append(line, fragment...)
		switch {
		case err == nil:
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) > 0:
			return line, nil
		default:
			return nil, err
		}
	}
}

// resetLineBuffer returns scratch cleared for the next JSONL line, or a fresh
// small buffer when scratch grew unusually large.
func resetLineBuffer(scratch []byte) []byte {
	if cap(scratch) > maxReusableLineBuffer {
		return make([]byte, 0, 64<<10)
	}
	return scratch[:0]
}

func pumpClient(input io.Reader, child io.Writer, output *lockedWriter, router *router) error {
	reader := bufio.NewReaderSize(input, 64<<10)
	scratch := make([]byte, 0, 64<<10)
	for {
		line, err := readLimitedLine(reader, clientLineLimit, resetLineBuffer(scratch))
		if err != nil {
			return err
		}
		scratch = line
		action := router.clientLine(line)
		if len(action.reply) > 0 {
			if err := output.write(action.reply); err != nil {
				return fmt.Errorf("write local JSON-RPC response: %w", err)
			}
			continue
		}
		if len(action.forward) > 0 {
			if err := writeAll(child, action.forward); err != nil {
				return fmt.Errorf("write app-server stdin: %w", err)
			}
		}
	}
}

func pumpServer(input io.Reader, output *lockedWriter, router *router) error {
	reader := bufio.NewReaderSize(input, 64<<10)
	scratch := make([]byte, 0, 64<<10)
	streamOversized := func(prefix, fragment []byte, readErr error) error {
		return streamServerLine(reader, output, prefix, fragment, readErr)
	}
	for {
		line, err := readLine(reader, serverObserveLimit, resetLineBuffer(scratch), streamOversized)
		if errors.Is(err, errLineStreamed) {
			continue
		}
		if err != nil {
			return err
		}
		router.observeServerLine(line)
		if err := output.write(line); err != nil {
			return fmt.Errorf("write app stdout: %w", err)
		}
		scratch = line
	}
}

func streamServerLine(reader *bufio.Reader, output *lockedWriter, prefix, fragment []byte, readErr error) error {
	output.mu.Lock()
	defer output.mu.Unlock()

	if err := writeAll(output.w, prefix); err != nil {
		return fmt.Errorf("write oversized app-server line prefix: %w", err)
	}
	if err := writeAll(output.w, fragment); err != nil {
		return fmt.Errorf("write oversized app-server line: %w", err)
	}
	for errors.Is(readErr, bufio.ErrBufferFull) {
		fragment, readErr = reader.ReadSlice('\n')
		if err := writeAll(output.w, fragment); err != nil {
			return fmt.Errorf("write oversized app-server line: %w", err)
		}
	}
	if readErr == nil {
		return nil
	}
	if errors.Is(readErr, io.EOF) {
		return io.EOF
	}
	return readErr
}
