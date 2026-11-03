// Package llm is the Control plane's provider-neutral view of an LLM with tool
// calling. Provider packages implement Client and are wired in at main;
// nothing outside them sees a provider's types.
package llm

import (
	"context"
	"encoding/json"
)

type Role string

const (
	System    Role = "system"
	User      Role = "user"
	Assistant Role = "assistant"
	Tool      Role = "tool"
)

// Message is one turn of a conversation. An Assistant message may ask for
// tool calls; each is answered by a Tool message with its ToolCallID.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
	ToolCallID string     `json:"toolCallId,omitempty"`
}

// ToolCall is the model asking to run one tool.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON text, as the model wrote it
}

// ToolSpec offers one tool to the model.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema of the arguments
}

// Request is one completion request. Model is the configured model id, which
// may be a router such as openrouter/free.
type Request struct {
	Model    string     `json:"model"`
	Messages []Message  `json:"messages"`
	Tools    []ToolSpec `json:"tools,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// Response is the model's reply. Model is the id of the model that actually
// answered, which a router chooses per request.
type Response struct {
	Model        string  `json:"model"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finishReason"`
	Usage        Usage   `json:"usage"`
}

type Client interface {
	Complete(ctx context.Context, req Request) (Response, error)
}
