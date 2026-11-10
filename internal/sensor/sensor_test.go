package sensor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsankoff/incident-copilot/internal/evidence"
	"github.com/itsankoff/incident-copilot/internal/signal"
)

// answerLink hands out calls once each and records the replies.
type answerLink struct {
	mu      sync.Mutex
	calls   []evidence.Call
	replies map[string]evidence.Answer
	done    chan struct{}
}

func (f *answerLink) Next(ctx context.Context) (evidence.Call, bool, error) {
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

func (f *answerLink) Send(context.Context, signal.Signal) error { return nil }

func (f *answerLink) Reply(_ context.Context, id string, a evidence.Answer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[id] = a
	if len(f.replies) == 2 {
		close(f.done)
	}
	return nil
}

type answerTool struct {
	name string
	data string
	err  error
}

func (t answerTool) Name() string { return t.name }
func (t answerTool) Run(context.Context, evidence.Query) (json.RawMessage, error) {
	return json.RawMessage(t.data), t.err
}

var (
	awsKey = "AKIA" + "IOSFODNN7EXAMPLE"
	ghTok  = "ghp_" + strings.Repeat("0123456789", 3) + "abcdef"
)

// The Sensor is the edge that fetches the data, so it redacts what its tools
// return, data and error text alike, before anything leaves the Location.
func TestTheSensorRedactsAnswersBeforeReplying(t *testing.T) {
	l := &answerLink{
		calls:   []evidence.Call{{ID: "c1", Query: evidence.Query{Tool: "logs"}}, {ID: "c2", Query: evidence.Query{Tool: "broken"}}},
		replies: map[string]evidence.Answer{},
		done:    make(chan struct{}),
	}
	tools := []evidence.Tool{
		answerTool{name: "logs", data: `[{"message":"using ` + awsKey + ` from https://b/o?a=1&X-Amz-Signature=abc123"}]`},
		answerTool{name: "broken", err: errors.New("GET failed: token=" + ghTok)},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-l.done; cancel() }()
	New(l, tools, nil, slog.New(slog.NewTextHandler(io.Discard, nil))).Run(ctx)

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

func (l *scriptedLink) Send(context.Context, signal.Signal) error { return nil }

func TestSensorLogsLinkUpOnConnectAndAfterAReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	down := errors.New("refused by the Control plane")
	l := &scriptedLink{polls: []error{down, nil, nil, down, down, nil, nil}, cancel: cancel}
	var logs bytes.Buffer
	s := New(l, nil, nil, slog.New(slog.NewJSONHandler(&logs, nil)))
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

var t0 = time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC)

// fakeLink records the Signals sent over it; while down, every send fails.
type fakeLink struct {
	sent []string
	down bool
}

func (f *fakeLink) Next(ctx context.Context) (evidence.Call, bool, error) {
	<-ctx.Done()
	return evidence.Call{}, false, ctx.Err()
}

func (f *fakeLink) Reply(context.Context, string, evidence.Answer) error { return nil }

func (f *fakeLink) Send(_ context.Context, s signal.Signal) error {
	if f.down {
		return errors.New("link down")
	}
	f.sent = append(f.sent, fmt.Sprintf("%s %s", s.Key, s.Status))
	return nil
}

// fakeDetector returns, at each Check, the Signals scripted for that time.
type fakeDetector map[time.Time][]signal.Signal

func (f fakeDetector) Check(_ context.Context, now time.Time) ([]signal.Signal, error) {
	return f[now], nil
}

func sig(key string, status signal.Status) signal.Signal {
	return signal.Signal{Key: key, Status: status}
}

func TestSensorResendsFiringSignalsEveryFiveMinutes(t *testing.T) {
	const m = time.Minute
	at := func(d time.Duration) time.Time { return t0.Add(d) }
	det := fakeDetector{
		at(0):      {sig("a", signal.Firing)},
		at(2 * m):  {sig("b", signal.Firing)},
		at(6 * m):  {sig("a", signal.Resolved)},
		at(11 * m): {sig("c", signal.Firing)},
	}
	l := &fakeLink{}
	s := New(l, nil, []Detector{det}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	for _, step := range []struct {
		at   time.Duration
		down bool
		want []string
	}{
		{at: 0, want: []string{"a firing"}},
		{at: 2 * m, want: []string{"b firing"}},
		{at: 4*m + 59*time.Second},
		{at: 5 * m, want: []string{"a firing"}},
		{at: 6 * m, want: []string{"a resolved"}},
		{at: 7 * m, want: []string{"b firing"}},
		{at: 10 * m}, // a's resolution is never re-sent
		{at: 11 * m, down: true},
		{at: 12 * m, want: []string{"b firing", "c firing"}}, // a failed send is retried
	} {
		l.down, l.sent = step.down, nil
		s.Detect(context.Background(), t0.Add(step.at))
		if fmt.Sprint(l.sent) != fmt.Sprint(step.want) {
			t.Errorf("at %s: sent %q, want %q", step.at, l.sent, step.want)
		}
	}
}

func TestNamespaceAllowlistHasNoEmptyEntries(t *testing.T) {
	env := map[string]string{"IC_SENSOR_LOCATION": "ic-target", "IC_CONTROL_PLANE_URL": "http://cp", "IC_SENSOR_TOKEN": "t"}
	for value, want := range map[string]string{"demo": "[demo]", " demo , shop ": "[demo shop]", "demo,": "", "demo,,shop": "", " ": ""} {
		env["IC_SENSOR_NAMESPACES"] = value
		cfg, err := LoadConfig(func(k string) string { return env[k] })
		if want == "" {
			if err == nil {
				t.Errorf("%q: got %q, want an error", value, cfg.Namespaces)
			}
			continue
		}
		if err != nil || fmt.Sprint(cfg.Namespaces) != want {
			t.Errorf("%q: got %q (err %v), want %s", value, cfg.Namespaces, err, want)
		}
	}
}
