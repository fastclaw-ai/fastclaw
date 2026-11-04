package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/channels"
	"github.com/fastclaw-ai/fastclaw/internal/usage"
)

// chatCompletionRequest mirrors the OpenAI chat completion request.
//
// User is OpenAI's standard "end-user identifier" field. On an api_key
// request it names the app's end-user, exactly like the
// X-Fastclaw-End-User header: the end-user is a data namespace only.
// The agent is still resolved from the app; the end-user decides where
// the session history, USER.md and personal memory live. It never
// changes which agents the request may use.
type chatCompletionRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   *bool         `json:"stream,omitempty"`
	User     string        `json:"user,omitempty"`
	// AgentID is a fastclaw extension: lets the caller pick the agent
	// in the request body instead of (or in addition to) the
	// `x-fastclaw-agent-id` header. Body wins when both are set —
	// matches the pattern used for `user`. Optional.
	AgentID string `json:"agent_id,omitempty"`
	// Params is a fastclaw extension: a freeform structured-parameter
	// blob the calling app submits alongside the chat. Rendered into
	// a per-turn system message so the agent's LLM can honor it when
	// calling tools (e.g. a third-party app's "model selector" +
	// "settings" UI translate to {provider, aspect_ratio, n} here,
	// rather than the user typing those into the prompt). Scope is
	// per-request — params don't persist across turns. OpenAI clients
	// that don't know about this field are unaffected (omitempty).
	Params map[string]any `json:"params,omitempty"`
	// Images is a fastclaw extension: image attachments for the
	// current turn. Each entry is one of:
	//   - HTTPS URL: "https://example.com/photo.jpg" (must be
	//     reachable from the LLM provider; not validated here)
	//   - Data URL:  "data:image/png;base64,iVBORw0KGgo..."
	//
	// Accepted MIME types depend on the LLM model. Anthropic / OpenAI
	// vision models all support png, jpeg, webp; gif is hit-or-miss.
	// Per-image and total-request size limits are also model-side
	// (Anthropic ~5MB/image, OpenAI ~20MB) — fastclaw does not enforce
	// its own ceiling, the upstream provider returns the rejection.
	Images []string `json:"images,omitempty"`
	// ImageURLs is an accepted alias for Images. The web-facing chat
	// endpoint historically calls this field `imageUrls`; allowing it
	// here means a caller writing one client against both endpoints
	// doesn't get silently dropped attachments when they pick the
	// wrong name.
	ImageURLs []string `json:"imageUrls,omitempty"`
	// Attachments is the typed, general-purpose attachment field. Each
	// entry can carry an optional Name which is sanitized and used as
	// the on-disk filename so the LLM sees `report.pdf` instead of
	// `image_3jk7l_0.pdf`. Unlike Images / ImageURLs, entries here are
	// NOT inlined as vision content parts — they only land in
	// /workspace and reach the LLM via the `[Attached: /workspace/X]`
	// breadcrumb. Use Images / ImageURLs (not Attachments) when you
	// want the bytes shown directly to a vision model.
	Attachments []attachmentRequest `json:"attachments,omitempty"`
	// ProjectID optionally files the conversation under one of the
	// agent's projects (in the caller's namespace) so it shares that
	// project's workspace. Omit for a loose chat.
	ProjectID string `json:"project_id,omitempty"`
	// ReturnFiles "inline" adds each returned file's bytes as a data URL
	// (small files only). Files the agent produced always come back in
	// `files` with a download URL.
	ReturnFiles string `json:"return_files,omitempty"`
}

// attachmentRequest is the wire form of a single attachment.
type attachmentRequest struct {
	URL  string `json:"url"`
	Name string `json:"name,omitempty"`
}

// allAttachments flattens the three input shapes (Images, ImageURLs,
// Attachments) into one ordered slice for materialization into
// /workspace. Clients normally pick one; mixing is allowed.
func (r chatCompletionRequest) allAttachments() []agent.Attachment {
	n := len(r.Images) + len(r.ImageURLs) + len(r.Attachments)
	if n == 0 {
		return nil
	}
	out := make([]agent.Attachment, 0, n)
	for _, u := range r.Images {
		out = append(out, agent.Attachment{URL: u})
	}
	for _, u := range r.ImageURLs {
		out = append(out, agent.Attachment{URL: u})
	}
	for _, a := range r.Attachments {
		out = append(out, agent.Attachment{URL: a.URL, Name: a.Name})
	}
	return out
}

// inlineImageURLs returns just the URLs eligible for vision inline
// (PhotoURLs → image_url content blocks). Only Images and ImageURLs
// qualify — by contract they're caller-asserted images. The general
// Attachments field is excluded: feeding a PDF / zip URL through the
// vision channel returns HTTP 400 from upstream providers and sinks
// the whole turn. Attachments reach the LLM via the
// `[Attached: /workspace/<file>]` breadcrumb instead.
func (r chatCompletionRequest) inlineImageURLs() []string {
	if len(r.Images) == 0 && len(r.ImageURLs) == 0 {
		return nil
	}
	out := make([]string, 0, len(r.Images)+len(r.ImageURLs))
	out = append(out, r.Images...)
	out = append(out, r.ImageURLs...)
	return out
}

type chatMessage struct {
	Role    string         `json:"role"`
	Content messageContent `json:"content"`
}

// messageContent is a chat message's content. Requests may send a plain
// string or an array of parts, as OpenAI-compatible and Anthropic clients
// do for images:
//
//	[{"type": "text", "text": "..."},
//	 {"type": "image_url", "image_url": {"url": "https://… or data:…"}},
//	 {"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "…"}}]
//
// Text parts are joined; images are collected so the last user message's
// images are treated like the `images` field. Responses always write the
// content as a plain string.
type messageContent struct {
	Text   string
	Images []string
}

func (c messageContent) MarshalJSON() ([]byte, error) { return json.Marshal(c.Text) }

func (c *messageContent) UnmarshalJSON(data []byte) error {
	*c = messageContent{}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if trimmed[0] == '"' {
		return json.Unmarshal(data, &c.Text)
	}
	var parts []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ImageURL json.RawMessage `json:"image_url"`
		Source   *struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
			URL       string `json:"url"`
		} `json:"source"`
	}
	if err := json.Unmarshal(data, &parts); err != nil {
		return fmt.Errorf("content must be a string or an array of parts: %w", err)
	}
	var texts []string
	for _, p := range parts {
		switch p.Type {
		case "text", "input_text":
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		case "image_url", "input_image":
			// image_url is {"url": "..."} (OpenAI) or a bare string.
			var obj struct {
				URL string `json:"url"`
			}
			var u string
			if json.Unmarshal(p.ImageURL, &obj) == nil && obj.URL != "" {
				u = obj.URL
			} else {
				_ = json.Unmarshal(p.ImageURL, &u)
			}
			if u != "" {
				c.Images = append(c.Images, u)
			}
		case "image":
			if p.Source == nil {
				continue
			}
			switch {
			case p.Source.Type == "base64" && p.Source.Data != "":
				mt := p.Source.MediaType
				if mt == "" {
					mt = "image/png"
				}
				c.Images = append(c.Images, "data:"+mt+";base64,"+p.Source.Data)
			case p.Source.URL != "":
				c.Images = append(c.Images, p.Source.URL)
			}
		}
	}
	c.Text = strings.Join(texts, "\n\n")
	return nil
}

// chatCompletionChunk is a single SSE chunk in streaming mode.
type chatCompletionChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	// Usage and Files (FastClaw extension) ride on the final chunk: the
	// turn's model usage and the files the agent produced.
	Usage *completionUsage `json:"usage,omitempty"`
	Files []turnFile       `json:"files,omitempty"`
	// Tool (FastClaw extension) reports a tool call starting or finishing
	// while the turn runs, on a chunk whose delta is empty.
	Tool *chunkTool `json:"tool,omitempty"`
	// Error (FastClaw extension), on the final chunk, says the turn failed
	// (e.g. the model API returned an error); same shape as an error body.
	Error *apiError `json:"error,omitempty"`
}

// apiError is the /v1 error object (see writeAPIError).
type apiError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// chunkTool is one step of the turn: the tool call's id, the tool's name
// and "running" or "done".
type chunkTool struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// chunkExtras is what only some chunks carry: usage and files on the
// final one, a tool step on its own.
type chunkExtras struct {
	Usage *completionUsage
	Files []turnFile
	Tool  *chunkTool
	Error *apiError
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

type chunkDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// chatCompletionResponse is the non-streaming response.
type chatCompletionResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   completionUsage    `json:"usage"`
	// Files (FastClaw extension): files the agent produced this turn.
	Files []turnFile `json:"files,omitempty"`
}

type completionChoice struct {
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// completionUsage is the turn's model usage: every model call the agent
// made to answer, tool loops included. prompt_tokens follows OpenAI and
// counts cached input too; the FastClaw fields split input into uncached
// / cache read / cache write — what per-token prices differ on.
type completionUsage struct {
	PromptTokens        int                  `json:"prompt_tokens"`
	CompletionTokens    int                  `json:"completion_tokens"`
	TotalTokens         int                  `json:"total_tokens"`
	PromptTokensDetails *promptTokensDetails `json:"prompt_tokens_details,omitempty"`
	InputTokens         int                  `json:"input_tokens"`
	CacheReadTokens     int                  `json:"cache_read_tokens"`
	CacheCreationTokens int                  `json:"cache_creation_tokens"`
	ModelCalls          int                  `json:"model_calls"`
	// CostUSD is the turn's cost in USD at each called model's unit
	// price; null when any call's model has no price. Never omitted.
	CostUSD *float64 `json:"cost_usd"`
}

type promptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

func usageFromCollector(c *usage.Collector) completionUsage {
	t, calls := c.Totals()
	prompt := t.Input + t.CacheRead + t.CacheCreation
	return completionUsage{
		PromptTokens:        prompt,
		CompletionTokens:    t.Output,
		TotalTokens:         prompt + t.Output,
		PromptTokensDetails: &promptTokensDetails{CachedTokens: t.CacheRead},
		InputTokens:         t.Input,
		CacheReadTokens:     t.CacheRead,
		CacheCreationTokens: t.CacheCreation,
		ModelCalls:          calls,
		CostUSD:             c.CostUSD(),
	}
}

// HandleChatCompletions handles POST /v1/chat/completions.
func (s *Server) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req chatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, "invalid request body")
		return
	}

	if len(req.Messages) == 0 {
		writeBadRequest(w, "messages is required")
		return
	}

	// OpenAI's `user` body field names the end-user, like the
	// X-Fastclaw-End-User header the auth middleware already applied.
	// The body is more specific to this call, so it wins when both are
	// present. Errors are non-fatal — the request continues in the
	// app's own namespace.
	if req.User != "" && s.authResolver != nil {
		if ident, ok := auth.FromContext(r.Context()); ok {
			if next, swErr := s.authResolver.SwitchToAppUser(r.Context(), ident, req.User); swErr == nil {
				r = r.WithContext(auth.WithIdentity(r.Context(), next))
			}
		}
	}
	ident, ok := auth.FromContext(r.Context())
	if !ok {
		writeUnauth(w, "unauthorized")
		return
	}
	// An account on billing hold (its balance ran out in an external
	// billing system) can't start turns; say so in a machine-readable way.
	if s.store != nil {
		if hold, _, err := s.store.GetBillingHold(r.Context(), ident.AccountID()); err == nil && hold {
			writeAPIError(w, http.StatusPaymentRequired, "billing_error", codePaymentRequired,
				"this account is on billing hold; top up its balance to continue")
			return
		}
	}

	// Body field beats header — same precedence as `user`. Lets app
	// callers send everything in one JSON without juggling headers.
	agentID := strings.TrimSpace(r.Header.Get("x-fastclaw-agent-id"))
	if req.AgentID != "" {
		agentID = strings.TrimSpace(req.AgentID)
	}
	// Strict: a named agent that isn't in this app is a 404 — never a
	// silent fallback to the default agent. Agents outside the api key's
	// ACL get the same 404 so their existence isn't revealed.
	ag, err := s.resolveChatAgent(r, agentID)
	if err != nil {
		if errors.Is(err, errAgentNotFound) {
			writeAgentNotFound(w)
			return
		}
		if errors.Is(err, errPrivateAgentNeedsOwner) {
			writeBadRequest(w, err.Error())
			return
		}
		writeUnauth(w, err.Error())
		return
	}

	// Build session key from header
	sessionKey := r.Header.Get("x-fastclaw-session-key")
	if sessionKey == "" {
		sessionKey = "api-" + fmt.Sprintf("%d", time.Now().UnixNano())
	}

	projectID := strings.TrimSpace(req.ProjectID)
	if projectID != "" {
		if s.store == nil {
			writeAPIError(w, http.StatusNotFound, errTypeNotFound, codeProjectNotFound, "project not found")
			return
		}
		if p, perr := s.store.GetProject(r.Context(), ident.EffectiveUserID(), ag.Name(), projectID); perr != nil || p == nil {
			writeAPIError(w, http.StatusNotFound, errTypeNotFound, codeProjectNotFound, "project not found")
			return
		}
	}

	// Extract the last user message. Images in its content parts join
	// the `images` field (workspace + vision).
	var userText string
	found := false
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			userText = req.Messages[i].Content.Text
			req.Images = append(req.Images, req.Messages[i].Content.Images...)
			found = true
			break
		}
	}
	if !found || (userText == "" && len(req.allAttachments()) == 0) {
		writeBadRequest(w, "no user message found")
		return
	}

	// Materialize attached images into the agent's session workspace and
	// prepend the same `[Attached: /workspace/<file>]` breadcrumb the web
	// UI uses (web/src/app/agents/[id]/chat/page.tsx:639-645) so the wire
	// shape is identical across web and API entry points. Verbose "do not
	// probe" notes here actively backfire — models reflexively run
	// which/ls/file to "verify" the path when the prompt foregrounds it.
	// PhotoURLs is preserved so vision LLMs still see the image inline.
	// Attachments land in the project's workspace when the request names
	// a project, otherwise in the loose-chat scope.
	// Snapshot the conversation's workspace before this turn touches it,
	// so the reply can return the files the agent produced.
	wsBefore := s.snapshotWorkspace(r.Context(), ag.Name(), projectID, sessionKey)
	atts := req.allAttachments()
	attachmentPaths := ag.WriteSessionAttachments(r.Context(), sessionKey, projectID, atts)
	if len(attachmentPaths) > 0 {
		var b strings.Builder
		for _, p := range attachmentPaths {
			b.WriteString("[Attached: /workspace/")
			b.WriteString(p)
			b.WriteString("]\n")
		}
		b.WriteString(userText)
		userText = b.String()
	}

	// Build inbound message.
	// X-Fastclaw-Channel lets callers override the reply channel so
	// cron jobs created during this turn route through the right
	// adapter (e.g. "pinclaw" → plugin channel.send → Cloud API).
	channel := r.Header.Get("x-fastclaw-channel")
	if channel == "" {
		channel = "api"
	}
	// The chatter decides whose USER.md and personal memory the turn
	// reads and writes. With an end-user it's that end-user's app_user;
	// without one, every call shares the agent's own "api-user" memory.
	chatter := "api-user"
	if ident.EndUser != "" {
		chatter = ident.EffectiveUserID()
	}
	msg := bus.InboundMessage{
		Channel:   channel,
		ChatID:    sessionKey,
		UserID:    chatter,
		Text:      userText,
		PeerKind:  "dm",
		Params:    turnParams(req.Params),
		PhotoURLs: req.inlineImageURLs(),
		ProjectID: projectID,
	}

	slog.Info("chat completion request",
		"agent", ag.Name(),
		"session", sessionKey,
		"end_user", ident.EndUser,
		"stream", req.Stream != nil && *req.Stream,
	)

	// The request's `model` runs this turn when the agent can reach it
	// ("<provider>/<model>" with that provider configured). Anything else
	// — e.g. the placeholder names OpenAI SDKs require — is ignored and
	// the agent's own model answers. The response always names the model
	// that actually ran.
	model := ag.Model()
	if req.Model != "" && req.Model != model {
		if _, ok := ag.ResolveTurnModel(req.Model); ok {
			model = strings.TrimSpace(req.Model)
			r = r.WithContext(agent.WithTurnModel(r.Context(), model))
		} else {
			slog.Info("chat completion: ignoring unusable request model",
				"agent", ag.Name(), "requested", req.Model, "using", model)
		}
	}
	chatID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	now := time.Now().Unix()

	// Sum every model call of this turn for the response's usage.
	collector := &usage.Collector{}
	r = r.WithContext(usage.WithCollector(r.Context(), collector))
	inline := req.ReturnFiles == "inline"
	files := func(reply string) []turnFile {
		return s.turnFiles(r.Context(), r, ag.Name(), projectID, sessionKey, wsBefore, reply, attachmentPaths, inline)
	}
	isStream := req.Stream != nil && *req.Stream
	if isStream {
		s.streamResponseFromAgent(w, r, ag, msg, chatID, model, now, files, collector)
	} else {
		var turnErr string
		reply := runAgentTurn(r.Context(), ag, msg, func(evt agent.ChatEvent) {
			if evt.Type == "error" {
				turnErr = eventMessage(evt)
			}
		}, nil)
		if turnErr != "" {
			writeAPIError(w, http.StatusBadGateway, errTypeServer, codeAgentError, turnErr)
			return
		}
		s.fullResponse(w, reply, chatID, model, now, files(reply), usageFromCollector(collector))
	}
}

// runAgentTurn runs one agent turn (HandleMessage, the loop the web chat
// and every channel use) and hands each event it emits to onEvent while it
// runs; onIdle, when set, is called every 15s. It returns the reply, its
// separate messages joined by blank lines.
func runAgentTurn(ctx context.Context, ag *agent.Agent, msg bus.InboundMessage, onEvent func(agent.ChatEvent), onIdle func()) string {
	events := make(chan agent.ChatEvent, 256)
	ctx = agent.ContextWithStream(ctx, events, nil, nil, "", ag.Name(), "")
	replyCh := make(chan string, 1)
	go func() { replyCh <- apiReplyText(ag.HandleMessage(ctx, msg)) }()

	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case evt := <-events:
			onEvent(evt)
		case <-tick.C:
			if onIdle != nil {
				onIdle()
			}
		case reply := <-replyCh:
			// Events are sent before HandleMessage returns; take what's
			// still buffered.
			for {
				select {
				case evt := <-events:
					onEvent(evt)
				default:
					return reply
				}
			}
		}
	}
}

// eventMessage is an error event's message.
func eventMessage(evt agent.ChatEvent) string {
	if m, _ := evt.Data["message"].(string); m != "" {
		return m
	}
	return "the agent failed to answer"
}

// streamResponseFromAgent runs the same agent loop as the non-streaming
// path and the web chat (HandleMessage) and relays its events as OpenAI
// SSE chunks while the turn runs: every round's text as delta.content
// (rounds joined by a blank line, the way the full reply joins them) and,
// as a FastClaw extension, each tool call as a top-level `tool` object on
// a chunk with an empty delta — once when it starts and once when it
// finishes. The final chunk carries usage and files.
func (s *Server) streamResponseFromAgent(w http.ResponseWriter, r *http.Request, ag *agent.Agent, msg bus.InboundMessage, chatID, model string, created int64, files func(reply string) []turnFile, collector *usage.Collector) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}

	s.writeSSEChunk(w, chatID, model, created, "assistant", "", nil, nil)
	flush()

	var streamed strings.Builder
	roundOpen := false // text of the current model round has been sent
	writeText := func(text string) {
		if text == "" {
			return
		}
		if !roundOpen && streamed.Len() > 0 {
			text = "\n\n" + text
		}
		roundOpen = true
		streamed.WriteString(text)
		s.writeSSEChunk(w, chatID, model, created, "", text, nil, nil)
		flush()
	}
	var turnErr string
	handle := func(evt agent.ChatEvent) {
		switch evt.Type {
		case "error":
			turnErr = eventMessage(evt)
		case "content_delta":
			if d, _ := evt.Data["delta"].(string); d != "" {
				writeText(d)
			}
		case "content":
			// The round's full text; already relayed when it streamed.
			if c, _ := evt.Data["content"].(string); !roundOpen && strings.TrimSpace(c) != "" {
				writeText(apiReplyText(c))
			}
			roundOpen = false
		case "tool_call", "tool_result":
			id, _ := evt.Data["id"].(string)
			name, _ := evt.Data["name"].(string)
			status := "running"
			if evt.Type == "tool_result" {
				status = "done"
			}
			roundOpen = false
			s.writeSSEChunk(w, chatID, model, created, "", "", nil, &chunkExtras{Tool: &chunkTool{ID: id, Name: name, Status: status}})
			flush()
		}
	}

	// Heartbeat comments keep proxies from closing a connection that is
	// quiet while a long tool runs.
	reply := runAgentTurn(r.Context(), ag, msg, handle, func() {
		fmt.Fprint(w, ": ping\n\n")
		flush()
	})
	done := "stop"
	u := usageFromCollector(collector)
	if turnErr != "" {
		// The reply is then an apology around the error; the caller gets
		// the error itself instead.
		s.writeSSEChunk(w, chatID, model, created, "", "", &done, &chunkExtras{Usage: &u, Error: &apiError{Type: errTypeServer, Code: codeAgentError, Message: turnErr}})
		fmt.Fprint(w, "data: [DONE]\n\n")
		flush()
		return
	}
	// Text the events didn't carry, e.g. a slash command's answer.
	if rest, ok := strings.CutPrefix(reply, streamed.String()); ok {
		if rest != "" {
			streamed.WriteString(rest)
			s.writeSSEChunk(w, chatID, model, created, "", rest, nil, nil)
		}
	} else if streamed.Len() == 0 {
		s.writeSSEChunk(w, chatID, model, created, "", reply, nil, nil)
	}

	s.writeSSEChunk(w, chatID, model, created, "", "", &done, &chunkExtras{Usage: &u, Files: files(reply)})
	fmt.Fprint(w, "data: [DONE]\n\n")
	flush()
}

// apiReplyText joins the reply's separate messages with blank lines: the
// split marker is for chat channels that send them as separate bubbles.
func apiReplyText(reply string) string {
	return strings.ReplaceAll(reply, channels.SplitMessageMarker, "\n\n")
}

func (s *Server) writeSSEChunk(w http.ResponseWriter, id, model string, created int64, role, content string, finishReason *string, extras *chunkExtras) {
	chunk := chatCompletionChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []chunkChoice{
			{
				Index: 0,
				Delta: chunkDelta{
					Role:    role,
					Content: content,
				},
				FinishReason: finishReason,
			},
		},
	}
	if extras != nil {
		chunk.Usage = extras.Usage
		chunk.Files = extras.Files
		chunk.Tool = extras.Tool
		chunk.Error = extras.Error
	}
	data, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func (s *Server) fullResponse(w http.ResponseWriter, reply, chatID, model string, created int64, files []turnFile, u completionUsage) {
	resp := chatCompletionResponse{
		ID:      chatID,
		Object:  "chat.completion",
		Created: created,
		Model:   model,
		Choices: []completionChoice{
			{
				Index:        0,
				Message:      chatMessage{Role: "assistant", Content: messageContent{Text: reply}},
				FinishReason: "stop",
			},
		},
		Usage: u,
		Files: files,
	}
	writeJSON(w, http.StatusOK, resp)
}

// turnParams prepares the caller's params for the agent turn. A group
// chat's `speaker` ({"id", "name"}) moves to the internal
// __fastclawSpeaker key: the agent renders it as per-turn context so it
// can tell group members apart, it is never persisted, and it isn't
// echoed back as a tool parameter.
func turnParams(params map[string]any) map[string]any {
	speaker, ok := params["speaker"]
	if !ok {
		return params
	}
	out := make(map[string]any, len(params))
	for k, v := range params {
		if k != "speaker" {
			out[k] = v
		}
	}
	out[agent.SpeakerParamKey] = speaker
	return out
}
