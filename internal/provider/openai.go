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

// NewOpenAI creates a new OpenAI-compatible provider. apiBase is taken
// verbatim — the operator's configured URL is the only source of truth.
// We intentionally do NOT default to "https://api.openai.com/v1" when
// apiBase is empty: that silent default produced "why is it calling
// OpenAI" mysteries when users had only a deepseek provider configured
// but the resolution path picked up an empty cfg. An empty apiBase now
// causes calls to fail loudly, which is what we want.
func NewOpenAI(apiKey, apiBase string) *OpenAIProvider {
	return &OpenAIProvider{
		apiKey:  apiKey,
		apiBase: NormalizeAPIBase(apiBase, "openai-chat"),
		client:  newLLMHTTPClient(),
	}
}

// apiMessage is the wire format for a message sent to the OpenAI API.
// It uses json.RawMessage for Content to support both string and array formats.
//
// ReasoningContent is DeepSeek's thinking-mode field. DeepSeek requires
// it to be echoed back on subsequent turns or it returns
// `invalid_request_error: The reasoning_content in the thinking mode
// must be passed back to the API.` Pure OpenAI ignores unknown fields,
// so omitempty keeps non-DeepSeek providers unaffected.
type apiMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	Name             string          `json:"name,omitempty"`
}

type chatRequest struct {
	Model               string            `json:"model"`
	Messages            []json.RawMessage `json:"messages"`
	Tools               []Tool            `json:"tools,omitempty"`
	MaxTokens           int               `json:"max_tokens,omitempty"`
	MaxCompletionTokens int               `json:"max_completion_tokens,omitempty"`
	Temperature         *float64          `json:"temperature,omitempty"`
	Stream              bool              `json:"stream"`
	StreamOptions       *streamOptions    `json:"stream_options,omitempty"`
}

// streamOptions.include_usage tells OpenAI-compat APIs to emit one final
// chunk carrying total token counts before [DONE]. Without this flag the
// streaming path returns no usage at all, which breaks per-turn goal
// token accounting and admin metering.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// sseUsage mirrors the `usage` block returned on the final SSE chunk
// when stream_options.include_usage is set. Some OpenAI-compat APIs
// (e.g. DeepSeek) expose prompt-cache hit/miss tokens via
// prompt_tokens_details — we capture them so admin metering can split
// cache reads out of input_tokens later if needed.
type sseUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
}

// toAPIMessages converts provider Messages to wire-format apiMessages,
// handling ContentParts for multimodal messages.
//
// Defensive sanitization: OpenAI/DeepSeek reject any request where an
// assistant message with `tool_calls` is not immediately followed by a
// `tool` message answering each tool_call_id. Dirty sessions can land
// us in that state — e.g. the agent's tool-loop detector at
// loop.go:1218 appends the assistant's tool_calls then breaks out
// without executing the tools, leaving orphans behind. Old sessions
// from before the streamed-RawAssistant fix can also carry orphan
// tool_calls inside the persisted RawAssistant. Either case used to
// surface as `An assistant message with 'tool_calls' must be followed
// by tool messages`. We strip the offending tool_calls (and any
// orphan tool replies) at wire-build time so the request goes through
// — the session keeps its historical record untouched.
func toAPIMessages(msgs []Message) []json.RawMessage {
	orphanAssistant, orphanTool := findOrphanToolCalls(msgs)
	out := make([]json.RawMessage, 0, len(msgs))
	for i, m := range msgs {
		if orphanTool[i] {
			continue
		}

		// For assistant messages with cached raw JSON, use it directly
		// to guarantee prompt cache hits (byte-identical prefix) —
		// unless the cached message has orphan tool_calls, in which
		// case rebuild it without them.
		if m.Role == "assistant" && len(m.RawAssistant) > 0 && !orphanAssistant[i] {
			out = append(out, m.RawAssistant)
			continue
		}

		am := apiMessage{
			Role:       m.Role,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
		if m.Role == "assistant" && m.Thinking != "" {
			am.ReasoningContent = m.Thinking
		}
		if !orphanAssistant[i] {
			am.ToolCalls = m.ToolCalls
		}
		if len(m.ContentParts) > 0 {
			am.Content, _ = json.Marshal(m.ContentParts)
		} else {
			am.Content, _ = json.Marshal(m.Content)
		}
		raw, _ := json.Marshal(am)
		out = append(out, raw)
	}
	return out
}

// findOrphanToolCalls walks msgs and flags assistant messages whose
// declared tool_calls are not fully answered by immediately-following
// tool messages, plus the tool messages that would dangle after the
// strip. Both `m.ToolCalls` and tool_calls embedded in
// `m.RawAssistant` are considered, since old sessions only stored
// them in the raw JSON.
func findOrphanToolCalls(msgs []Message) (orphanAssistant, orphanTool map[int]bool) {
	orphanAssistant = map[int]bool{}
	orphanTool = map[int]bool{}
	for i, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		want := assistantToolCallIDs(m)
		if len(want) == 0 {
			continue
		}
		// Collect IDs from the run of tool messages immediately following.
		got := map[string]bool{}
		j := i + 1
		for j < len(msgs) && msgs[j].Role == "tool" {
			if id := msgs[j].ToolCallID; id != "" {
				got[id] = true
			}
			j++
		}
		missing := false
		for _, id := range want {
			if !got[id] {
				missing = true
				break
			}
		}
		if !missing {
			continue
		}
		orphanAssistant[i] = true
		// Drop any tool messages that referenced this assistant's
		// (now-removed) tool_calls so the API doesn't reject them as
		// dangling tool replies.
		wantSet := map[string]bool{}
		for _, id := range want {
			wantSet[id] = true
		}
		for k := i + 1; k < j; k++ {
			if wantSet[msgs[k].ToolCallID] {
				orphanTool[k] = true
			}
		}
	}
	return
}

// assistantToolCallIDs extracts tool_call IDs from a stored assistant
// Message, checking both the parsed ToolCalls field and tool_calls
// embedded in the raw JSON (older sessions that streamed the message
// only carry the IDs inside RawAssistant).
func assistantToolCallIDs(m Message) []string {
	if len(m.ToolCalls) > 0 {
		ids := make([]string, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			ids = append(ids, tc.ID)
		}
		return ids
	}
	if len(m.RawAssistant) == 0 {
		return nil
	}
	var raw struct {
		ToolCalls []struct {
			ID string `json:"id"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(m.RawAssistant, &raw); err != nil || len(raw.ToolCalls) == 0 {
		return nil
	}
	ids := make([]string, 0, len(raw.ToolCalls))
	for _, tc := range raw.ToolCalls {
		ids = append(ids, tc.ID)
	}
	return ids
}

// sseDelta mirrors the OpenAI streaming delta structure including tool call index.
type sseToolCallDelta struct {
	Index    int          `json:"index"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function FunctionCall `json:"function"`
}

type sseDelta struct {
	Role             string             `json:"role,omitempty"`
	Content          string             `json:"content,omitempty"`
	ReasoningContent string             `json:"reasoning_content,omitempty"`
	ToolCalls        []sseToolCallDelta `json:"tool_calls,omitempty"`
}

type sseChoice struct {
	Delta        sseDelta `json:"delta"`
	FinishReason string   `json:"finish_reason"`
}

type sseResponse struct {
	Choices []sseChoice `json:"choices"`
	Usage   *sseUsage   `json:"usage,omitempty"` // present only on the final chunk when include_usage=true
}

type openAIRequestMode struct {
	maxCompletionTokens bool
	omitTemperature     bool
}

func initialOpenAIRequestMode(model string) openAIRequestMode {
	model = strings.ToLower(StripProviderPrefix(model))
	switch {
	case strings.HasPrefix(model, "gpt-5"):
		return openAIRequestMode{maxCompletionTokens: true, omitTemperature: true}
	case strings.HasPrefix(model, "o1"), strings.HasPrefix(model, "o3"), strings.HasPrefix(model, "o4"):
		return openAIRequestMode{maxCompletionTokens: true, omitTemperature: true}
	default:
		return openAIRequestMode{}
	}
}

func (p *OpenAIProvider) buildRequest(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64, stream bool, mode openAIRequestMode) (*http.Request, error) {
	req := chatRequest{
		Model:    StripProviderPrefix(model),
		Messages: toAPIMessages(messages),
		Stream:   stream,
	}
	if !mode.omitTemperature {
		req.Temperature = &temperature
	}
	if mode.maxCompletionTokens {
		req.MaxCompletionTokens = maxTokens
	} else {
		req.MaxTokens = maxTokens
	}
	if stream {
		// include_usage adds a terminal chunk carrying the call's token
		// counts. Required for both admin metering and goal token-
		// budget accounting on every streaming turn. Providers that
		// don't honor the flag silently drop it.
		req.StreamOptions = &streamOptions{IncludeUsage: true}
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
	SetAppIdentityHeaders(httpReq.Header)
	return httpReq, nil
}

func (p *OpenAIProvider) Chat(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64) (*Response, error) {
	resp, err := p.doChatRequest(ctx, messages, tools, model, maxTokens, temperature, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return p.parseSSE(resp.Body)
}

// ChatStream returns a StreamReader that yields chunks as they arrive from the LLM.
func (p *OpenAIProvider) ChatStream(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64) (*StreamReader, error) {
	resp, err := p.doChatRequest(ctx, messages, tools, model, maxTokens, temperature, true)
	if err != nil {
		return nil, err
	}

	ch := make(chan StreamChunk, 64)
	reader := NewStreamReader(ch)

	go func() {
		defer resp.Body.Close()
		defer close(ch)

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		toolCalls := make(map[int]*ToolCall)
		var contentBuilder, reasoningBuilder strings.Builder
		var usage Usage

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				// Send final chunk with accumulated tool calls and a
				// fully-formed RawAssistant. DeepSeek thinking mode
				// requires reasoning_content to round-trip on the next
				// turn (or the API rejects with 400), so we serialize
				// the assistant message in the OpenAI wire format here
				// and let callers persist it verbatim.
				//
				// tool_calls MUST be included. streamChatToResponse
				// uses ChatStream for every model call (tool iterations
				// included) so the live web UI can render text deltas
				// — when the model emits tool_calls, the wire-format
				// RawAssistant must carry them too, or the next turn's
				// API call ships `assistant.RawAssistant` (no
				// tool_calls) followed by tool replies, and OpenAI
				// 400s with "Messages with role 'tool' must be a
				// response to a preceding message with 'tool_calls'".
				var tcs []ToolCall
				for i := 0; i < len(toolCalls); i++ {
					if tc, ok := toolCalls[i]; ok {
						tcs = append(tcs, *tc)
					}
				}
				reasoning := reasoningBuilder.String()
				rawMsg := apiMessage{
					Role:             "assistant",
					ToolCalls:        tcs,
					ReasoningContent: reasoning,
				}
				rawMsg.Content, _ = json.Marshal(contentBuilder.String())
				raw, _ := json.Marshal(rawMsg)
				select {
				case ch <- StreamChunk{
					ToolCalls:    tcs,
					Done:         true,
					Thinking:     reasoning,
					Usage:        usage,
					RawAssistant: raw,
				}:
				case <-ctx.Done():
				}
				return
			}

			var chunk sseResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				slog.Warn("parse SSE chunk", "error", err, "data", data)
				continue
			}

			// Usage rides on a terminal chunk with empty choices when
			// stream_options.include_usage=true. Capture so the [DONE]
			// chunk emits it exactly once on the StreamChunk.Usage.
			if chunk.Usage != nil {
				usage = openaiUsageToProvider(chunk.Usage)
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

			if delta.ReasoningContent != "" {
				reasoningBuilder.WriteString(delta.ReasoningContent)
			}

			// Yield content chunks
			if delta.Content != "" {
				contentBuilder.WriteString(delta.Content)
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

func (p *OpenAIProvider) doChatRequest(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64, stream bool) (*http.Response, error) {
	mode := initialOpenAIRequestMode(model)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		httpReq, err := p.buildRequest(ctx, messages, tools, model, maxTokens, temperature, stream, mode)
		if err != nil {
			return nil, err
		}

		resp, err := p.client.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("send request: %w", err)
		}
		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		body := string(respBody)
		lastErr = fmt.Errorf("API error %d: %s", resp.StatusCode, body)

		if !mode.maxCompletionTokens && shouldRetryWithMaxCompletionTokens(resp.StatusCode, body) {
			mode.maxCompletionTokens = true
			continue
		}
		if !mode.omitTemperature && shouldRetryWithoutTemperature(resp.StatusCode, body) {
			mode.omitTemperature = true
			continue
		}

		return nil, lastErr
	}
	return nil, lastErr
}

func shouldRetryWithMaxCompletionTokens(status int, body string) bool {
	if status < 400 || status >= 500 {
		return false
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "max_tokens") && strings.Contains(lower, "max_completion_tokens")
}

func shouldRetryWithoutTemperature(status int, body string) bool {
	if status < 400 || status >= 500 {
		return false
	}
	lower := strings.ToLower(body)
	return strings.Contains(lower, "temperature") && strings.Contains(lower, "only the default")
}

func (p *OpenAIProvider) parseSSE(reader io.Reader) (*Response, error) {
	scanner := bufio.NewScanner(reader)
	// Increase buffer size for large SSE chunks
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	toolCalls := make(map[int]*ToolCall)
	var usage Usage

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk sseResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			slog.Warn("parse SSE chunk", "error", err, "data", data)
			continue
		}

		// Stream usage arrives on a terminal chunk with choices=[] when
		// stream_options.include_usage=true. Non-streaming chunks also
		// carry it; either path lands here.
		if chunk.Usage != nil {
			usage = openaiUsageToProvider(chunk.Usage)
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		delta := chunk.Choices[0].Delta

		if delta.Content != "" {
			contentBuilder.WriteString(delta.Content)
		}
		if delta.ReasoningContent != "" {
			reasoningBuilder.WriteString(delta.ReasoningContent)
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

	reasoning := reasoningBuilder.String()
	result := &Response{
		Content:  contentBuilder.String(),
		Thinking: reasoning,
		Usage:    usage,
	}
	for i := 0; i < len(toolCalls); i++ {
		if tc, ok := toolCalls[i]; ok {
			result.ToolCalls = append(result.ToolCalls, *tc)
		}
	}

	// Same leaked-XML recovery the Anthropic provider does — DeepSeek-flash
	// served via api.deepseek.com (openai-chat compat, not the anthropic
	// route) emits its tool calls as DSML-style text instead of, or in
	// addition to, the structured tool_calls field. Strip the XML from
	// content unconditionally; synthesize tool calls from it only when
	// the structured channel was empty.
	if cleaned, calls := extractLeakedToolCalls(result.Content); cleaned != result.Content {
		result.Content = cleaned
		if len(result.ToolCalls) == 0 && len(calls) > 0 {
			slog.Warn("recovered leaked tool-call XML from text content (openai-chat)",
				"count", len(calls))
			result.ToolCalls = calls
		} else if len(calls) > 0 {
			slog.Debug("stripped leaked tool-call XML echoing a structured tool_use (openai-chat)",
				"echo_count", len(calls), "structured_count", len(result.ToolCalls))
		}
	}

	// Capture raw assistant message for cache-safe replay.
	// Reconstruct the exact message format the API would expect back.
	// reasoning_content must round-trip for DeepSeek's thinking mode
	// (otherwise the next turn fails with `The reasoning_content in
	// the thinking mode must be passed back to the API.`).
	rawMsg := apiMessage{
		Role:             "assistant",
		ToolCalls:        result.ToolCalls,
		ReasoningContent: reasoning,
	}
	rawMsg.Content, _ = json.Marshal(result.Content)
	result.RawAssistant, _ = json.Marshal(rawMsg)

	return result, nil
}

// openaiUsageToProvider folds an OpenAI-style usage block into the
// provider-neutral Usage. Cached prompt tokens (if reported) are
// surfaced as CacheReadTokens, and input_tokens is the *uncached*
// remainder so input+cache_read still sums to total prompt size.
func openaiUsageToProvider(u *sseUsage) Usage {
	out := Usage{
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
	}
	if u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens > 0 {
		out.CacheReadTokens = u.PromptTokensDetails.CachedTokens
		out.InputTokens -= u.PromptTokensDetails.CachedTokens
		if out.InputTokens < 0 {
			out.InputTokens = 0
		}
	}
	return out
}
