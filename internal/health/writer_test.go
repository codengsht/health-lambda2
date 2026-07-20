package health

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteJSONLineEscapesEmbeddedNewlines(t *testing.T) {
	var output bytes.Buffer
	if err := writeJSONLine(&output, map[string]string{"message": "first\r\nsecond"}); err != nil {
		t.Fatalf("writeJSONLine() error = %v", err)
	}
	if got := strings.Count(output.String(), "\n"); got != 1 {
		t.Fatalf("physical lines = %d; output = %q", got, output.String())
	}
	if !strings.Contains(output.String(), `first\r\nsecond`) {
		t.Errorf("control characters were not JSON escaped: %q", output.String())
	}
}

func TestWriteJSONLineRejectsMarshalAndShortWriteFailures(t *testing.T) {
	if err := writeJSONLine(&bytes.Buffer{}, func() {}); err == nil || !strings.Contains(err.Error(), "marshal") {
		t.Errorf("marshal error = %v", err)
	}
	if err := writeJSONLine(shortWriter{}, map[string]string{"ok": "yes"}); err == nil || !strings.Contains(err.Error(), "short write") {
		t.Errorf("short-write error = %v", err)
	}
}

type shortWriter struct{}

func (shortWriter) Write(payload []byte) (int, error) { return len(payload) - 1, nil }
