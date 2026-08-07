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
)

var errLineTooLong = errors.New("JSONL message exceeds 64 MiB")

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

func readLimitedLine(reader *bufio.Reader, limit int) ([]byte, error) {
	line := make([]byte, 0, 64<<10)
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > limit {
			return nil, errLineTooLong
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

func pumpClient(input io.Reader, child io.Writer, output *lockedWriter, router *router) error {
	reader := bufio.NewReaderSize(input, 64<<10)
	for {
		line, err := readLimitedLine(reader, clientLineLimit)
		if err != nil {
			return err
		}
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
	for {
		line := make([]byte, 0, 64<<10)
		for {
			fragment, err := reader.ReadSlice('\n')
			if len(line)+len(fragment) > serverObserveLimit {
				if err := streamServerLine(reader, output, line, fragment, err); err != nil {
					return err
				}
				goto nextLine
			}
			line = append(line, fragment...)
			switch {
			case err == nil:
				router.observeServerLine(line)
				if err := output.write(line); err != nil {
					return fmt.Errorf("write app stdout: %w", err)
				}
				goto nextLine
			case errors.Is(err, bufio.ErrBufferFull):
				continue
			case errors.Is(err, io.EOF) && len(line) > 0:
				router.observeServerLine(line)
				if err := output.write(line); err != nil {
					return fmt.Errorf("write app stdout: %w", err)
				}
				return io.EOF
			default:
				return err
			}
		}
	nextLine:
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
