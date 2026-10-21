// Package audit is the Control plane's hash-chained JSONL audit log. Each
// entry carries the hash of the entry before it, so a modified, removed, or
// reordered entry breaks the chain and Verify reports it. It is
// tamper-evident, not tamper-proof (ADR 0003 D8): cutting entries off the end
// leaves a valid chain.
package audit

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Entry is one line of the audit log.
type Entry struct {
	Seq      int64           `json:"seq"`
	Time     time.Time       `json:"time"`
	Type     string          `json:"type"`
	Incident int64           `json:"incident"` // 0 when the event belongs to no Incident
	Data     json.RawMessage `json:"data,omitempty"`
	Prev     string          `json:"prev"`
	Hash     string          `json:"hash,omitempty"`
}

// Log appends entries to one chain. It is safe for concurrent use.
type Log struct {
	mu   sync.Mutex
	f    *os.File
	seq  int64
	prev string
}

// Open opens or creates the log at path and continues its chain. It refuses a
// log whose chain does not verify, so nothing is ever appended to a broken one.
func Open(path string) (*Log, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	last, err := verify(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("audit log %s: %w", path, err)
	}
	return &Log{f: f, seq: last.Seq, prev: last.Hash}, nil
}

// Append writes one entry and syncs it to disk before returning.
func (l *Log) Append(t time.Time, typ string, incident int64, data any) error {
	var raw json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("audit %s: %w", typ, err)
		}
		raw = b
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	e := Entry{Seq: l.seq + 1, Time: t.UTC(), Type: typ, Incident: incident, Data: raw, Prev: l.prev}
	e.Hash = hash(e)
	line, _ := json.Marshal(e) // as in hash
	if _, err := l.f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("audit %s: %w", typ, err)
	}
	if err := l.f.Sync(); err != nil {
		return fmt.Errorf("audit %s: %w", typ, err)
	}
	l.seq, l.prev = e.Seq, e.Hash
	return nil
}

// Close closes the underlying file.
func (l *Log) Close() error { return l.f.Close() }

// Verify reads a whole chain and reports the first entry that does not follow
// from the one before it.
func Verify(r io.Reader) error {
	_, err := verify(r)
	return err
}

// verify returns the last entry of a valid chain (the zero Entry if empty).
func verify(r io.Reader) (Entry, error) {
	var last Entry
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return last, fmt.Errorf("entry %d: %w", last.Seq+1, err)
		}
		if e.Seq != last.Seq+1 || e.Prev != last.Hash {
			return last, fmt.Errorf("entry %d: does not follow entry %d", e.Seq, last.Seq)
		}
		want := e.Hash
		e.Hash = ""
		if hash(e) != want {
			return last, fmt.Errorf("entry %d: hash mismatch", e.Seq)
		}
		e.Hash = want
		last = e
	}
	if err := sc.Err(); err != nil {
		return last, err
	}
	return last, nil
}

// hash is the SHA-256 of the entry's JSON without its own hash.
// Marshalling cannot fail: Data is always valid JSON, from json.Marshal or a
// line that just unmarshalled.
func hash(e Entry) string {
	b, _ := json.Marshal(e)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
