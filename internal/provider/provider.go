package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// newLLMHTTPClient returns an *http.Client suitable for streaming LLM
// API calls.
//
// Why not the zero-value &http.Client{}: it has no client-level timeout
// (which we want — long SSE streams take minutes to drain) but also no
// ResponseHeaderTimeout, so if the upstream server stops writing AFTER
// completing the TCP/TLS handshake but BEFORE the headers come back,
// `client.Do` blocks forever. We hit this in production: a chat goes
// silent mid-conversation, the per-chat taskQueue serializes behind
// the wedged request, every subsequent user message queues too, only
// a pod restart recovers.
//
// DefaultTransport already provides sensible DialContext/Timeout
// (30s), TLSHandshakeTimeout (10s), and IdleConnTimeout (90s) — we
// only need to add ResponseHeaderTimeout. Clone() first because
// mutating DefaultTransport would affect every other consumer in the
// process (it's a documented global shared resource).
//
// 60s is conservative for LLM APIs: response headers come back well
// under 1s on a healthy path; 60s catches a hung connection without
// false-positives on a slow but live network.
func newLLMHTTPClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{Transport: tr}
}

// Origin tags a Message that was produced by the runtime rather than a
// real user / model exchange. Empty means "this came from the user
// (or the model in response to the user)" — the common case. Hooks
// that filter user-visible history or skip FTS indexing for runtime
// inserts gate on this.
const (
	OriginUser        = "" // default — pre-existing producers stay correct without edits
	OriginGoalContext = "goal_context"
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
	Thinking     string        `json:"thinking,omitempty"`  // model's reasoning (for memory extraction)
	Timestamp    int64         `json:"timestamp,omitempty"` // unix ms, for memory timeline

	// Metadata is UI-only state attached to a tool-role message (e.g.
	// { "sandbox": true } so the chat UI can badge it). Not sent to the
	// LLM — provider.toLLMMessages / anthropic / openai serializers
	// ignore it.
	Metadata map[string]any `json:"metadata,omitempty"`

	// RawAssistant preserves the exact assistant message JSON as returned by the API.
	// When sending history back to the LLM, use this instead of re-serializing from
	// parsed fields — guarantees prompt cache hits by maintaining byte-identical prefix.
	// Only set on role="assistant" messages.
	RawAssistant json.RawMessage `json:"_raw,omitempty"`

	// Origin distinguishes runtime-injected messages from real user /
	// assistant exchanges. Empty (OriginUser) is the default. Currently
	// only OriginGoalContext is set, by the /goal continuation path.
	// User-visible history (WebChatHistory) and the FTS index filter
	// on this so synthetic prompts don't pollute either view. Rides
	// as part of the JSONB sessions.messages working set and as a
	// column on the structured session_messages archive.
	Origin string `json:"origin,omitempty"`

	// Provider and Model record which LLM produced this message.
	// Only set on role="assistant" messages by the agent loop.
	// Not sent to the LLM — used for session_messages archive
	// and billing audit trails.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// TextContent returns the message's user-visible text. Falls back to
// joining the `text` parts of ContentParts when Content is empty —
// which is the shape we store for user messages that arrive with
// attachments (Content="" + ContentParts=[text, image_url, ...]).
// Without this fallback, history/preview/title code that gates on
// `Content != ""` silently drops every multimodal turn.
//
// Also strips the legacy "[Attached: <filename>]\n" breadcrumb that
// older client versions prepended to outgoing chat text. New sends no
// longer add it, but historical sessions still carry it baked into
// stored content; without stripping here the prefix shows up in chat
// bubbles, page titles, and sidebar entries.
func (m Message) TextContent() string {
	if m.Content != "" {
		return StripAttachedPrefix(m.Content)
	}
	var parts []string
	for _, p := range m.ContentParts {
		if p.Type == "text" && p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	return StripAttachedPrefix(strings.Join(parts, "\n"))
}

// StripAttachedPrefix removes one or more leading "[Attached: …]" tags
// (followed by optional whitespace / newline) from a string. Exposed
// so non-Message callers (e.g. raw store rows in session adapters)
// can apply the same cleanup.
func StripAttachedPrefix(s string) string {
	for {
		if !strings.HasPrefix(s, "[Attached:") {
			break
		}
		end := strings.IndexByte(s, ']')
		if end < 0 {
			break
		}
		s = strings.TrimLeft(s[end+1:], " \t\r\n")
	}
	return s
}

// ContentPart represents a part of multimodal content.
type ContentPart struct {
	Type     string    `json:"type"` // "text" or "image_url"
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

// Usage reports token counts returned by the provider. Zero values are
// fine — admin metering and goal budget treat absent fields as "not
// reported"; downstream consumers that strictly need counts (goal
// budget enforcement) check for the zero-value Usage and fall back to
// unbounded behaviour.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int // Anthropic prompt-cache hit tokens (read)
	CacheCreationTokens int // Anthropic prompt-cache write tokens
}

// Response is the result of a Chat call.
type Response struct {
	Content      string
	ToolCalls    []ToolCall
	Thinking     string          // model's reasoning/thinking content (extracted for memory)
	Usage        Usage           // token counts for metering (zero when provider didn't report)
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
	// Thinking is emitted once at message_stop (if the model produced any)
	// so callers can persist it alongside the final assistant message —
	// required so the next turn can echo content[].thinking back to
	// extended-thinking providers (Anthropic + DeepSeek /anthropic compat).
	Thinking          string
	ThinkingSignature string
	// Usage is reported on the final (Done) chunk if the provider
	// emitted token counts (Anthropic message_delta.usage, OpenAI
	// usage block). Zero on intermediate chunks.
	Usage Usage
	// RawAssistant is the fully-serialized assistant message in the
	// provider's wire format, emitted on the final (Done) chunk. When
	// set, callers should persist it verbatim onto Message.RawAssistant
	// instead of reconstructing — required so DeepSeek (OpenAI-compat
	// thinking mode) sees the correct top-level `reasoning_content` on
	// replay, which it does not auto-generate.
	RawAssistant json.RawMessage
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

// App identity sent with every upstream LLM call so model platforms can
// attribute traffic to FastClaw.
const (
	AppName = "FastClaw"
	AppURL  = "https://fastclaw.ai"
)

// SetAppIdentityHeaders tags an outbound LLM request with the FastClaw app
// identity. X-Title/HTTP-Referer are OpenRouter's app-attribution headers;
// platforms that copied its leaderboard convention read the same pair.
// Unknown headers are ignored upstream, so this is safe to send everywhere.
func SetAppIdentityHeaders(h http.Header) {
	h.Set("X-Title", AppName)
	h.Set("HTTP-Referer", AppURL)
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

// SplitProviderModel splits "<providerKey>/<modelId>" into its two parts.
// When there's no slash (model uses the shared provider with no per-agent
// override), the first return is "" and the model name comes back
// unchanged. Inverse of "prov + / + model".
func SplitProviderModel(s string) (provider, model string) {
	if idx := strings.Index(s, "/"); idx >= 0 {
		return s[:idx], s[idx+1:]
	}
	return "", s
}

// NewProvider creates a Provider based on apiType.
// "anthropic-messages" creates an Anthropic provider, anything else creates OpenAI-compatible.
// headers are extra request headers from the provider config (may be nil).
func NewProvider(apiKey, apiBase, apiType string, headers map[string]string) Provider {
	if apiType == "anthropic-messages" {
		p := NewAnthropic(apiKey, apiBase)
		p.headers = headers
		return p
	}
	p := NewOpenAI(apiKey, apiBase)
	p.headers = headers
	return p
}
