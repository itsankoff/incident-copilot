package audit

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)

// writeChain appends three entries to a fresh log and returns its lines.
func writeChain(t *testing.T) (path string, lines []string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, typ := range []string{"signal.received", "triage.decision", "ticket.created"} {
		if err := l.Append(t0.Add(time.Duration(i)*time.Second), typ, 7, map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, strings.SplitAfter(strings.TrimSuffix(string(b), "\n"), "\n")
}

func TestVerifyIntactChain(t *testing.T) {
	_, lines := writeChain(t)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	if err := Verify(strings.NewReader(strings.Join(lines, ""))); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !strings.Contains(lines[1], `"incident":7`) {
		t.Errorf("entry does not carry the Incident id: %s", lines[1])
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	_, lines := writeChain(t)
	tests := []struct {
		name  string
		lines []string
	}{
		{"modified entry", []string{lines[0], strings.Replace(lines[1], `"n":1`, `"n":9`, 1), lines[2]}},
		{"removed entry", []string{lines[0], lines[2]}},
		{"removed first entry", []string{lines[1], lines[2]}},
		{"reordered entries", []string{lines[0], lines[2], lines[1]}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Verify(strings.NewReader(strings.Join(tt.lines, ""))); err == nil {
				t.Error("Verify accepted a tampered chain")
			}
		})
	}
}

func TestReopenContinuesChain(t *testing.T) {
	path, _ := writeChain(t)
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(t0, "auth.rejected", 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(bytes.NewReader(b)); err != nil {
		t.Fatalf("Verify after reopen: %v", err)
	}
}

func TestOpenRefusesBrokenChain(t *testing.T) {
	path, lines := writeChain(t)
	if err := os.WriteFile(path, []byte(lines[0]+lines[2]), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Error("Open appended to a broken chain")
	}
}
