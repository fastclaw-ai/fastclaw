package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The SSE spec makes the space after the "data:" field name optional, and
// some gateways emit `data:{...}` without it. These tests pin the parsers
// to keep accepting both forms.

func TestOpenAIChatParsesDataLinesWithoutSpace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data:{\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n")
		fmt.Fprint(w, "data:{\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n")
		fmt.Fprint(w, "data:[DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAI("test-key", srv.URL)
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "test-model", 123, 0)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "hello" {
		t.Fatalf("content = %q, want hello", resp.Content)
	}
}

func TestOpenAIParseSSEDataLinesWithoutSpace(t *testing.T) {
	p := NewOpenAI("test-key", "http://unused")
	resp, err := p.parseSSE(strings.NewReader(
		"data:{\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"!\"}}]}\n\n" +
			"data: [DONE]\n\n"))
	if err != nil {
		t.Fatalf("parseSSE: %v", err)
	}
	if resp.Content != "ok!" {
		t.Fatalf("content = %q, want ok!", resp.Content)
	}
}

func TestAnthropicParseSSEDataLinesWithoutSpace(t *testing.T) {
	p := NewAnthropic("test-key", "http://unused")
	resp, err := p.parseSSE(strings.NewReader(
		"event: message_start\n" +
			"data:{\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3}}}\n\n" +
			"event: content_block_delta\n" +
			"data:{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
			"event: message_delta\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
			"event: message_stop\n" +
			"data: {\"type\":\"message_stop\"}\n\n"))
	if err != nil {
		t.Fatalf("parseSSE: %v", err)
	}
	if resp.Content != "hi" {
		t.Fatalf("content = %q, want hi", resp.Content)
	}
	if resp.Usage.OutputTokens != 2 {
		t.Fatalf("output tokens = %d, want 2", resp.Usage.OutputTokens)
	}
}
