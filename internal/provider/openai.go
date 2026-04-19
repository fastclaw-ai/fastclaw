package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// OpenAIProvider implements the Provider interface for OpenAI-compatible APIs.
type OpenAIProvider struct {
	apiKey  string
	apiBase string
	client  *http.Client
}

// NewOpenAI creates a new OpenAI-compatible provider.
func NewOpenAI(apiKey, apiBase string) *OpenAIProvider {
	if apiBase == "" {
		apiBase = "https://api.openai.com/v1"
	}
	apiBase = strings.TrimRight(apiBase, "/")
	return &OpenAIProvider{
		apiKey:  apiKey,
		apiBase: apiBase,
		client:  &http.Client{},
	}
}

// apiMessage is the wire format for a message sent to the OpenAI API.
// It uses json.RawMessage for Content to support both string and array formats.
type apiMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Name       string          `json:"name,omitempty"`
}

type chatRequest struct {
	Model       string            `json:"model"`
	Messages    []json.RawMessage `json:"messages"`
	Tools       []Tool            `json:"tools,omitempty"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	Temperature float64           `json:"temperature,omitempty"`
	Stream      bool              `json:"stream"`
}

// toAPIMessages converts provider Messages to wire-format apiMessages,
// handling ContentParts for multimodal messages.
func toAPIMessages(msgs []Message) []json.RawMessage {
	out := make([]json.RawMessage, len(msgs))
	for i, m := range msgs {
		// For assistant messages with cached raw JSON, use it directly
		// to guarantee prompt cache hits (byte-identical prefix).
		if m.Role == "assistant" && len(m.RawAssistant) > 0 {
			out[i] = m.RawAssistant
			continue
		}

		am := apiMessage{
			Role:       m.Role,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		if len(m.ContentParts) > 0 {
			am.Content, _ = json.Marshal(m.ContentParts)
		} else {
			am.Content, _ = json.Marshal(m.Content)
		}
		out[i], _ = json.Marshal(am)
	}
	return out
}

// sseDelta mirrors the OpenAI streaming delta structure including tool call index.
type sseToolCallDelta struct {
	Index    int          `json:"index"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

type sseDelta struct {
	Role      string             `json:"role,omitempty"`
	Content   string             `json:"content,omitempty"`
	ToolCalls []sseToolCallDelta `json:"tool_calls,omitempty"`
}

type sseChoice struct {
	Delta        sseDelta `json:"delta"`
	FinishReason string   `json:"finish_reason"`
}

type sseResponse struct {
	Choices []sseChoice `json:"choices"`
}

func (p *OpenAIProvider) buildRequest(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64, stream bool) (*http.Request, error) {
	req := chatRequest{
		Model:       StripProviderPrefix(model),
		Messages:    toAPIMessages(messages),
		MaxTokens:   maxTokens,
		Temperature: temperature,
		Stream:      stream,
	}
	if len(tools) > 0 {
		req.Tools = tools
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := p.apiBase + "/chat/completions"
	slog.Info("openai request", "url", url, "model", req.Model)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	return httpReq, nil
}

func (p *OpenAIProvider) Chat(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64) (*Response, error) {
	httpReq, err := p.buildRequest(ctx, messages, tools, model, maxTokens, temperature, true)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	return p.parseSSE(resp.Body)
}

// ChatStream returns a StreamReader that yields chunks as they arrive from the LLM.
func (p *OpenAIProvider) ChatStream(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64) (*StreamReader, error) {
	httpReq, err := p.buildRequest(ctx, messages, tools, model, maxTokens, temperature, true)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan StreamChunk, 64)
	reader := NewStreamReader(ch)

	go func() {
		defer resp.Body.Close()
		defer close(ch)

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		toolCalls := make(map[int]*ToolCall)

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				// Send final chunk with accumulated tool calls
				var tcs []ToolCall
				for i := 0; i < len(toolCalls); i++ {
					if tc, ok := toolCalls[i]; ok {
						tcs = append(tcs, *tc)
					}
				}
				select {
				case ch <- StreamChunk{ToolCalls: tcs, Done: true}:
				case <-ctx.Done():
				}
				return
			}

			var chunk sseResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				slog.Warn("parse SSE chunk", "error", err, "data", data)
				continue
			}

			if len(chunk.Choices) == 0 {
				continue
			}

			delta := chunk.Choices[0].Delta

			// Accumulate tool calls
			for _, tc := range delta.ToolCalls {
				existing, ok := toolCalls[tc.Index]
				if !ok {
					toolCalls[tc.Index] = &ToolCall{
						ID:   tc.ID,
						Type: tc.Type,
						Function: FunctionCall{
							Name:      tc.Function.Name,
							Arguments: tc.Function.Arguments,
						},
					}
				} else {
					if tc.ID != "" {
						existing.ID = tc.ID
					}
					if tc.Type != "" {
						existing.Type = tc.Type
					}
					if tc.Function.Name != "" {
						existing.Function.Name += tc.Function.Name
					}
					existing.Function.Arguments += tc.Function.Arguments
				}
			}

			// Yield content chunks
			if delta.Content != "" {
				select {
				case ch <- StreamChunk{Content: delta.Content}:
				case <-ctx.Done():
					return
				}
			}
		}

		if err := scanner.Err(); err != nil {
			reader.SetErr(fmt.Errorf("read stream: %w", err))
		}
	}()

	return reader, nil
}

func (p *OpenAIProvider) parseSSE(reader io.Reader) (*Response, error) {
	scanner := bufio.NewScanner(reader)
	// Increase buffer size for large SSE chunks
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var contentBuilder strings.Builder
	toolCalls := make(map[int]*ToolCall)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk sseResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			slog.Warn("parse SSE chunk", "error", err, "data", data)
			continue
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta

		if delta.Content != "" {
			contentBuilder.WriteString(delta.Content)
		}

		for _, tc := range delta.ToolCalls {
			existing, ok := toolCalls[tc.Index]
			if !ok {
				toolCalls[tc.Index] = &ToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: FunctionCall{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				}
			} else {
				if tc.ID != "" {
					existing.ID = tc.ID
				}
				if tc.Type != "" {
					existing.Type = tc.Type
				}
				if tc.Function.Name != "" {
					existing.Function.Name += tc.Function.Name
				}
				existing.Function.Arguments += tc.Function.Arguments
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}

	result := &Response{
		Content: contentBuilder.String(),
	}
	for i := 0; i < len(toolCalls); i++ {
		if tc, ok := toolCalls[i]; ok {
			result.ToolCalls = append(result.ToolCalls, *tc)
		}
	}

	// Capture raw assistant message for cache-safe replay.
	// Reconstruct the exact message format the API would expect back.
	rawMsg := apiMessage{
		Role:      "assistant",
		ToolCalls: result.ToolCalls,
	}
	rawMsg.Content, _ = json.Marshal(result.Content)
	result.RawAssistant, _ = json.Marshal(rawMsg)

	return result, nil
}
