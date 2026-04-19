package provider

import (
	"context"
	"encoding/json"
	"strings"
)

// Message represents a chat message.
// When storing in session, keep ALL fields exactly as returned by the LLM
// to ensure prompt cache hits on subsequent turns.
type Message struct {
	Role         string        `json:"role"`
	Content      string        `json:"content,omitempty"`
	ContentParts []ContentPart `json:"content_parts,omitempty"` // multimodal input (user messages)
	ToolCalls    []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID   string        `json:"tool_call_id,omitempty"`
	Name         string        `json:"name,omitempty"`
	Thinking     string        `json:"thinking,omitempty"`      // model's reasoning (for memory extraction)
	Timestamp    int64         `json:"timestamp,omitempty"`     // unix ms, for memory timeline

	// RawAssistant preserves the exact assistant message JSON as returned by the API.
	// When sending history back to the LLM, use this instead of re-serializing from
	// parsed fields — guarantees prompt cache hits by maintaining byte-identical prefix.
	// Only set on role="assistant" messages.
	RawAssistant json.RawMessage `json:"_raw,omitempty"`
}

// ContentPart represents a part of multimodal content.
type ContentPart struct {
	Type     string    `json:"type"`                // "text" or "image_url"
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL represents an image URL for vision messages.
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"` // "auto", "low", "high"
}

// ToolCall represents a function call requested by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall contains the function name and arguments.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool describes a tool available to the model.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction describes a function tool.
type ToolFunction struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// Response is the result of a Chat call.
type Response struct {
	Content      string
	ToolCalls    []ToolCall
	Thinking     string          // model's reasoning/thinking content (extracted for memory)
	RawAssistant json.RawMessage // exact API response message JSON (for cache-safe replay)
}

// HasToolCalls returns true if the response contains tool calls.
func (r *Response) HasToolCalls() bool {
	return len(r.ToolCalls) > 0
}

// StreamChunk represents a single chunk from a streaming response.
type StreamChunk struct {
	Content   string
	ToolCalls []ToolCall
	Done      bool
}

// StreamReader reads streaming chunks from an LLM response.
type StreamReader struct {
	ch  chan StreamChunk
	err error
}

// NewStreamReader creates a new StreamReader with the given channel.
func NewStreamReader(ch chan StreamChunk) *StreamReader {
	return &StreamReader{ch: ch}
}

// Next returns the next chunk and whether more chunks are available.
func (r *StreamReader) Next() (StreamChunk, bool) {
	chunk, ok := <-r.ch
	return chunk, ok
}

// Err returns any error that occurred during streaming.
func (r *StreamReader) Err() error {
	return r.err
}

// SetErr sets the error on the stream reader.
func (r *StreamReader) SetErr(err error) {
	r.err = err
}

// Provider is the LLM provider interface.
type Provider interface {
	Chat(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64) (*Response, error)
	ChatStream(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64) (*StreamReader, error)
}

// StripProviderPrefix removes the "provider/" prefix from a model string.
// e.g. "minimax-coding-plan/MiniMax-M2.7" -> "MiniMax-M2.7"
func StripProviderPrefix(model string) string {
	if idx := strings.Index(model, "/"); idx >= 0 {
		return model[idx+1:]
	}
	return model
}

// NewProvider creates a Provider based on apiType.
// "anthropic-messages" creates an Anthropic provider, anything else creates OpenAI-compatible.
func NewProvider(apiKey, apiBase, apiType string) Provider {
	if apiType == "anthropic-messages" {
		return NewAnthropic(apiKey, apiBase)
	}
	return NewOpenAI(apiKey, apiBase)
}
