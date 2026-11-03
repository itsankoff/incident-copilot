// Package openrouter is an llm.Client for OpenRouter's chat completions API
// (https://openrouter.ai/docs/api-reference/chat-completion). With the
// openrouter/free router, each request may be answered by a different free
// model; the response names the one that did.
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/itsankoff/incident-copilot/internal/llm"
)

// APIURL is OpenRouter's API base URL.
const APIURL = "https://openrouter.ai/api/v1"

type Client struct {
	apiURL string // e.g. https://openrouter.ai/api/v1
	key    string
	client *http.Client
}

func New(apiURL, key string, client *http.Client) *Client {
	return &Client{apiURL: strings.TrimSuffix(apiURL, "/"), key: key, client: client}
}

type function struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Arguments   *string         `json:"arguments,omitempty"`
}

type toolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function function `json:"function"`
}

type message struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type tool struct {
	Type     string   `json:"type"`
	Function function `json:"function"`
}

type request struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Tools    []tool    `json:"tools,omitempty"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string  `json:"finish_reason"`
		Message      message `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *apiError `json:"error"`
}

// Complete sends one chat completion request.
func (c *Client) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	b, err := json.Marshal(toWire(req))
	if err != nil {
		return llm.Response{}, fmt.Errorf("openrouter: %w", err)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return llm.Response{}, fmt.Errorf("openrouter: %w", err)
	}
	hreq.Header.Set("Authorization", "Bearer "+c.key)
	hreq.Header.Set("Content-Type", "application/json")

	hresp, err := c.client.Do(hreq)
	if err != nil {
		return llm.Response{}, fmt.Errorf("openrouter: %w", err)
	}
	defer func() { _ = hresp.Body.Close() }()
	if hresp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(hresp.Body, 512))
		return llm.Response{}, fmt.Errorf("openrouter: status %d: %s", hresp.StatusCode, bytes.TrimSpace(msg))
	}
	var out response
	if err := json.NewDecoder(hresp.Body).Decode(&out); err != nil {
		return llm.Response{}, fmt.Errorf("openrouter: %w", err)
	}
	if out.Error != nil { // an upstream failure can arrive with a 200
		return llm.Response{}, fmt.Errorf("openrouter: error %d: %s", out.Error.Code, out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return llm.Response{}, errors.New("openrouter: response has no choices")
	}
	ch := out.Choices[0]
	resp := llm.Response{
		Model:        out.Model,
		Message:      llm.Message{Role: llm.Role(ch.Message.Role)},
		FinishReason: ch.FinishReason,
		Usage:        llm.Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens},
	}
	if ch.Message.Content != nil {
		resp.Message.Content = *ch.Message.Content
	}
	for _, tc := range ch.Message.ToolCalls {
		args := "{}"
		if a := tc.Function.Arguments; a != nil && *a != "" {
			args = *a
		}
		resp.Message.ToolCalls = append(resp.Message.ToolCalls, llm.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: args})
	}
	return resp, nil
}

func toWire(req llm.Request) request {
	out := request{Model: req.Model}
	for _, m := range req.Messages {
		w := message{Role: string(m.Role), ToolCallID: m.ToolCallID}
		if m.Content != "" || len(m.ToolCalls) == 0 {
			w.Content = &m.Content
		}
		for _, tc := range m.ToolCalls {
			w.ToolCalls = append(w.ToolCalls, toolCall{ID: tc.ID, Type: "function", Function: function{Name: tc.Name, Arguments: &tc.Arguments}})
		}
		out.Messages = append(out.Messages, w)
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, tool{Type: "function", Function: function{Name: t.Name, Description: t.Description, Parameters: t.Parameters}})
	}
	return out
}
