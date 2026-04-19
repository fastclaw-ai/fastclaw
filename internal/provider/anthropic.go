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

// AnthropicProvider implements the Provider interface for Anthropic Messages API.
type AnthropicProvider struct {
	apiKey  string
	apiBase string
	client  *http.Client
}

// NewAnthropic creates a new Anthropic Messages API provider.
func NewAnthropic(apiKey, apiBase string) *AnthropicProvider {
	if apiBase == "" {
		apiBase = "https://api.anthropic.com"
	}
	apiBase = strings.TrimRight(apiBase, "/")
	return &AnthropicProvider{
		apiKey:  apiKey,
		apiBase: apiBase,
		client:  &http.Client{},
	}
}

// anthropicMessage is the wire format for Anthropic Messages API.
type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	System      string             `json:"system,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature,omitempty"`
	Stream      bool               `json:"stream"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
}

// toAnthropicMessages converts provider Messages to Anthropic wire format.
// Extracts the system message and returns the rest.
func toAnthropicMessages(msgs []Message) (string, []anthropicMessage) {
	var system string
	var out []anthropicMessage

	for _, m := range msgs {
		if m.Role == "system" {
			system = m.Content
			continue
		}

		am := anthropicMessage{Role: m.Role}

		// Tool results become role "user" with tool_result content block
		if m.Role == "tool" {
			am.Role = "user"
			block := map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     m.Content,
			}
			am.Content, _ = json.Marshal([]interface{}{block})
			out = append(out, am)
			continue
		}

		// Assistant messages with tool calls
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			var blocks []interface{}
			if m.Content != "" {
				blocks = append(blocks, map[string]interface{}{
					"type": "text",
					"text": m.Content,
				})
			}
			for _, tc := range m.ToolCalls {
				var input interface{}
				json.Unmarshal([]byte(tc.Function.Arguments), &input)
				blocks = append(blocks, map[string]interface{}{
					"type":  "tool_use",
					"id":    tc.ID,
					"name":  tc.Function.Name,
					"input": input,
				})
			}
			am.Content, _ = json.Marshal(blocks)
			out = append(out, am)
			continue
		}

		// Regular text content
		if len(m.ContentParts) > 0 {
			var blocks []interface{}
			for _, part := range m.ContentParts {
				if part.Type == "text" {
					blocks = append(blocks, map[string]interface{}{
						"type": "text",
						"text": part.Text,
					})
				} else if part.Type == "image_url" && part.ImageURL != nil {
					blocks = append(blocks, map[string]interface{}{
						"type": "image",
						"source": map[string]string{
							"type":       "url",
							"url":        part.ImageURL.URL,
						},
					})
				}
			}
			am.Content, _ = json.Marshal(blocks)
		} else if m.Content != "" {
			am.Content, _ = json.Marshal(m.Content)
		}

		out = append(out, am)
	}

	return system, out
}

func (p *AnthropicProvider) buildRequest(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64, stream bool) (*http.Request, error) {
	system, anthropicMsgs := toAnthropicMessages(messages)

	if maxTokens <= 0 {
		maxTokens = 4096
	}

	req := anthropicRequest{
		Model:       StripProviderPrefix(model),
		Messages:    anthropicMsgs,
		System:      system,
		MaxTokens:   maxTokens,
		Temperature: temperature,
		Stream:      stream,
	}

	if len(tools) > 0 {
		for _, t := range tools {
			req.Tools = append(req.Tools, anthropicTool{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				InputSchema: t.Function.Parameters,
			})
		}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := p.apiBase + "/v1/messages"
	slog.Info("anthropic request", "url", url, "model", req.Model)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	return httpReq, nil
}

// Anthropic SSE event types
type anthropicSSEEvent struct {
	Type string `json:"type"`
}

type anthropicContentBlockStart struct {
	Type         string                     `json:"type"`
	Index        int                        `json:"index"`
	ContentBlock anthropicContentBlockEntry `json:"content_block"`
}

type anthropicContentBlockEntry struct {
	Type  string `json:"type"` // "text" or "tool_use"
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Text  string `json:"text,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type anthropicContentBlockDelta struct {
	Type  string                `json:"type"`
	Index int                   `json:"index"`
	Delta anthropicDeltaContent `json:"delta"`
}

type anthropicDeltaContent struct {
	Type        string `json:"type"` // "text_delta" or "input_json_delta"
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

func (p *AnthropicProvider) Chat(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64) (*Response, error) {
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

func (p *AnthropicProvider) ChatStream(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64) (*StreamReader, error) {
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

		type blockState struct {
			blockType string // "text" or "tool_use"
			id        string
			name      string
			argsJSON  strings.Builder
		}
		blocks := make(map[int]*blockState)

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")

			var event anthropicSSEEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			switch event.Type {
			case "content_block_start":
				var cbs anthropicContentBlockStart
				if json.Unmarshal([]byte(data), &cbs) == nil {
					blocks[cbs.Index] = &blockState{
						blockType: cbs.ContentBlock.Type,
						id:        cbs.ContentBlock.ID,
						name:      cbs.ContentBlock.Name,
					}
					if cbs.ContentBlock.Text != "" {
						select {
						case ch <- StreamChunk{Content: cbs.ContentBlock.Text}:
						case <-ctx.Done():
							return
						}
					}
				}

			case "content_block_delta":
				var cbd anthropicContentBlockDelta
				if json.Unmarshal([]byte(data), &cbd) == nil {
					if cbd.Delta.Type == "text_delta" && cbd.Delta.Text != "" {
						select {
						case ch <- StreamChunk{Content: cbd.Delta.Text}:
						case <-ctx.Done():
							return
						}
					} else if cbd.Delta.Type == "input_json_delta" {
						if bs, ok := blocks[cbd.Index]; ok {
							bs.argsJSON.WriteString(cbd.Delta.PartialJSON)
						}
					}
				}

			case "message_stop":
				var toolCalls []ToolCall
				for i := 0; i < len(blocks); i++ {
					bs, ok := blocks[i]
					if !ok {
						continue
					}
					if bs.blockType == "tool_use" {
						toolCalls = append(toolCalls, ToolCall{
							ID:   bs.id,
							Type: "function",
							Function: FunctionCall{
								Name:      bs.name,
								Arguments: bs.argsJSON.String(),
							},
						})
					}
				}
				select {
				case ch <- StreamChunk{ToolCalls: toolCalls, Done: true}:
				case <-ctx.Done():
				}
				return
			}
		}

		if err := scanner.Err(); err != nil {
			reader.SetErr(fmt.Errorf("read stream: %w", err))
		}
	}()

	return reader, nil
}

func (p *AnthropicProvider) parseSSE(body io.Reader) (*Response, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var contentBuilder strings.Builder

	type blockState struct {
		blockType string
		id        string
		name      string
		argsJSON  strings.Builder
	}
	blocks := make(map[int]*blockState)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var event anthropicSSEEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			slog.Warn("parse anthropic SSE event", "error", err, "data", data)
			continue
		}

		switch event.Type {
		case "content_block_start":
			var cbs anthropicContentBlockStart
			if json.Unmarshal([]byte(data), &cbs) == nil {
				blocks[cbs.Index] = &blockState{
					blockType: cbs.ContentBlock.Type,
					id:        cbs.ContentBlock.ID,
					name:      cbs.ContentBlock.Name,
				}
				if cbs.ContentBlock.Text != "" {
					contentBuilder.WriteString(cbs.ContentBlock.Text)
				}
			}

		case "content_block_delta":
			var cbd anthropicContentBlockDelta
			if json.Unmarshal([]byte(data), &cbd) == nil {
				if cbd.Delta.Type == "text_delta" {
					contentBuilder.WriteString(cbd.Delta.Text)
				} else if cbd.Delta.Type == "input_json_delta" {
					if bs, ok := blocks[cbd.Index]; ok {
						bs.argsJSON.WriteString(cbd.Delta.PartialJSON)
					}
				}
			}

		case "message_stop":
			// Done
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stream: %w", err)
	}

	result := &Response{
		Content: contentBuilder.String(),
	}
	for i := 0; i < len(blocks); i++ {
		bs, ok := blocks[i]
		if !ok {
			continue
		}
		if bs.blockType == "tool_use" {
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:   bs.id,
				Type: "function",
				Function: FunctionCall{
					Name:      bs.name,
					Arguments: bs.argsJSON.String(),
				},
			})
		}
	}

	return result, nil
}
