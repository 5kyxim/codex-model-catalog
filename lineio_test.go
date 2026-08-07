package modelcatalog

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestReadLimitedLineRejectsOversizedMessage(t *testing.T) {
	t.Parallel()
	reader := bufio.NewReaderSize(strings.NewReader("12345\n"), 2)
	if _, err := readLimitedLine(reader, 4); !errors.Is(err, errLineTooLong) {
		t.Fatalf("error = %v, want errLineTooLong", err)
	}
}

func TestPumpServerStreamsOversizedLineAndContinues(t *testing.T) {
	t.Parallel()
	large := bytes.Repeat([]byte("x"), serverObserveLimit+1)
	input := append(append(large, '\n'), []byte("not-json\n")...)
	var output bytes.Buffer
	err := pumpServer(bytes.NewReader(input), &lockedWriter{w: &output}, testRouter())
	if err == nil || err.Error() != "EOF" {
		t.Fatalf("pumpServer error = %v, want EOF", err)
	}
	if !bytes.Equal(output.Bytes(), input) {
		t.Fatalf("streamed output length = %d, want %d", output.Len(), len(input))
	}
}
