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
	if _, err := readLimitedLine(reader, 4, make([]byte, 0, 64<<10)); !errors.Is(err, errLineTooLong) {
		t.Fatalf("error = %v, want errLineTooLong", err)
	}
}

func TestReadLimitedLineReusesScratch(t *testing.T) {
	t.Parallel()
	reader := bufio.NewReaderSize(strings.NewReader("first\nsecond\n"), 4)
	scratch := make([]byte, 0, 64<<10)
	first, err := readLimitedLine(reader, clientLineLimit, scratch)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != "first\n" {
		t.Fatalf("first line = %q, want %q", first, "first\n")
	}
	second, err := readLimitedLine(reader, clientLineLimit, scratch)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != "second\n" {
		t.Fatalf("second line = %q, want %q", second, "second\n")
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
