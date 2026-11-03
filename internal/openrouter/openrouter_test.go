package openrouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/itsankoff/incident-copilot/internal/llm"
)

func TestCompleteSendsToolsAndReturnsTheAnsweringModel(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("%s %s auth %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"id":"gen-1","model":"meta-llama/llama-3.3-70b-instruct:free","choices":[{"finish_reason":"tool_calls",
			"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"events","arguments":"{}"}}]}}],
			"usage":{"prompt_tokens":120,"completion_tokens":15,"total_tokens":135}}`))
	}))
	defer srv.Close()

	resp, err := New(srv.URL, "sk-test", srv.Client()).Complete(context.Background(), llm.Request{
		Model: "openrouter/free",
		Messages: []llm.Message{
			{Role: llm.System, Content: "be brief"},
			{Role: llm.Assistant, ToolCalls: []llm.ToolCall{{ID: "call_0", Name: "workload_state", Arguments: "{}"}}},
			{Role: llm.Tool, ToolCallID: "call_0", Content: `{"ok":true}`},
		},
		Tools: []llm.ToolSpec{{Name: "events", Description: "Events.", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}

	want := llm.Response{
		Model:        "meta-llama/llama-3.3-70b-instruct:free",
		Message:      llm.Message{Role: llm.Assistant, ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "events", Arguments: "{}"}}},
		FinishReason: "tool_calls",
		Usage:        llm.Usage{InputTokens: 120, OutputTokens: 15},
	}
	if g, w := mustJSON(resp), mustJSON(want); g != w {
		t.Errorf("response\n got %s\nwant %s", g, w)
	}

	sent := mustJSON(got)
	for _, frag := range []string{
		`"model":"openrouter/free"`,
		`"tools":[{"function":{"description":"Events.","name":"events","parameters":{"properties":{},"type":"object"}},"type":"function"}]`,
		`"tool_calls":[{"function":{"arguments":"{}","name":"workload_state"},"id":"call_0","type":"function"}]`,
		`{"content":"{\"ok\":true}","role":"tool","tool_call_id":"call_0"}`,
	} {
		if !strings.Contains(sent, frag) {
			t.Errorf("request lacks %s:\n%s", frag, sent)
		}
	}
}

func TestCompleteReportsAProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Rate limit exceeded: free-models-per-min"}}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "sk-test", srv.Client()).Complete(context.Background(), llm.Request{Model: "openrouter/free"})
	if err == nil || !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "Rate limit exceeded") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "sk-test") {
		t.Error("error holds the API key")
	}
}

func TestCompleteReportsAnErrorInAnOKBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"code":502,"message":"upstream provider failed"}}`))
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "k", srv.Client()).Complete(context.Background(), llm.Request{}); err == nil || !strings.Contains(err.Error(), "upstream provider failed") {
		t.Fatalf("err = %v", err)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
