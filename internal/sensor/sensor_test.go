package sensor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/itsankoff/incident-copilot/internal/evidence"
)

// fakeLink hands out calls once each and records the replies.
type fakeLink struct {
	mu      sync.Mutex
	calls   []evidence.Call
	replies map[string]evidence.Answer
	done    chan struct{}
}

func (f *fakeLink) Next(ctx context.Context) (evidence.Call, bool, error) {
	f.mu.Lock()
	if len(f.calls) == 0 {
		f.mu.Unlock()
		<-ctx.Done()
		return evidence.Call{}, false, ctx.Err()
	}
	c := f.calls[0]
	f.calls = f.calls[1:]
	f.mu.Unlock()
	return c, true, nil
}

func (f *fakeLink) Reply(_ context.Context, id string, a evidence.Answer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[id] = a
	if len(f.replies) == 2 {
		close(f.done)
	}
	return nil
}

type fakeTool struct {
	name string
	data string
	err  error
}

func (t fakeTool) Name() string { return t.name }
func (t fakeTool) Run(context.Context, evidence.Query) (json.RawMessage, error) {
	return json.RawMessage(t.data), t.err
}

var (
	awsKey = "AKIA" + "IOSFODNN7EXAMPLE"
	ghTok  = "ghp_" + strings.Repeat("0123456789", 3) + "abcdef"
)

// The Sensor is the edge that fetches the data, so it redacts what its tools
// return, data and error text alike, before anything leaves the Location.
func TestTheSensorRedactsAnswersBeforeReplying(t *testing.T) {
	l := &fakeLink{
		calls:   []evidence.Call{{ID: "c1", Query: evidence.Query{Tool: "logs"}}, {ID: "c2", Query: evidence.Query{Tool: "broken"}}},
		replies: map[string]evidence.Answer{},
		done:    make(chan struct{}),
	}
	tools := []evidence.Tool{
		fakeTool{name: "logs", data: `[{"message":"using ` + awsKey + ` from https://b/o?a=1&X-Amz-Signature=abc123"}]`},
		fakeTool{name: "broken", err: errors.New("GET failed: token=" + ghTok)},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-l.done; cancel() }()
	New(l, tools, slog.New(slog.NewTextHandler(io.Discard, nil))).Run(ctx)

	data, errText := string(l.replies["c1"].Data), l.replies["c2"].Error
	for _, secret := range []string{awsKey, "abc123", ghTok} {
		if strings.Contains(data+errText, secret) {
			t.Errorf("reply holds %q: data %s, error %q", secret, data, errText)
		}
	}
	if !strings.Contains(data, "using [REDACTED] from") || errText != "GET failed: token=[REDACTED]" {
		t.Errorf("data %s, error %q", data, errText)
	}
}
