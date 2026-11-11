package sensor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/itsankoff/incident-copilot/internal/evidence"
)

// scriptedLink answers each poll with the next error in polls (nil: connected,
// no Call), then cancels the Sensor.
type scriptedLink struct {
	mu     sync.Mutex
	polls  []error
	cancel context.CancelFunc
}

func (l *scriptedLink) Next(context.Context) (evidence.Call, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.polls) == 0 {
		l.cancel()
		return evidence.Call{}, false, nil
	}
	err := l.polls[0]
	l.polls = l.polls[1:]
	return evidence.Call{}, false, err
}

func (l *scriptedLink) Reply(context.Context, string, evidence.Answer) error { return nil }

func TestSensorLogsLinkUpOnConnectAndAfterAReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	down := errors.New("refused by the Control plane")
	l := &scriptedLink{polls: []error{down, nil, nil, down, down, nil, nil}, cancel: cancel}
	var logs bytes.Buffer
	s := New(l, nil, slog.New(slog.NewJSONHandler(&logs, nil)))
	s.Retry = 0
	s.Run(ctx)

	var got []string
	sc := bufio.NewScanner(&logs)
	for sc.Scan() {
		var line struct{ Msg, Error string }
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		got = append(got, line.Msg)
		if line.Msg == "link.down" && line.Error != down.Error() {
			t.Errorf("link.down error %q, want %q", line.Error, down.Error())
		}
	}
	want := []string{"link.down", "link.up", "link.down", "link.down", "link.up"}
	if len(got) != len(want) {
		t.Fatalf("logged %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("logged %q, want %q", got, want)
		}
	}
}
