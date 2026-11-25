package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestOpenAIRetriesWithMaxCompletionTokens(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if call == 1 {
			if _, ok := body["max_tokens"]; !ok {
				t.Fatalf("first request missing max_tokens: %#v", body)
			}
			http.Error(w, `{"error":{"message":"Unsupported parameter: 'max_tokens'. Use 'max_completion_tokens' instead."}}`, http.StatusBadRequest)
			return
		}
		if _, ok := body["max_completion_tokens"]; !ok {
			t.Fatalf("retry missing max_completion_tokens: %#v", body)
		}
		if _, ok := body["max_tokens"]; ok {
			t.Fatalf("retry still sent max_tokens: %#v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAI("test-key", srv.URL)
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "test-model", 123, 0)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q, want ok", resp.Content)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestOpenAIRetriesWithoutTemperature(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if call == 1 {
			if _, ok := body["temperature"]; !ok {
				t.Fatalf("first request missing temperature: %#v", body)
			}
			http.Error(w, `{"error":{"message":"Unsupported value: 'temperature' does not support 0.7 with this model. Only the default (1) value is supported.","param":"temperature","code":"unsupported_value"}}`, http.StatusBadRequest)
			return
		}
		if _, ok := body["temperature"]; ok {
			t.Fatalf("retry still sent temperature: %#v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAI("test-key", srv.URL)
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "test-model", 123, 0.7)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q, want ok", resp.Content)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestOpenAIRetriesWithoutTemperatureWhenOnlyOneIsAllowed(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if call == 1 {
			if _, ok := body["temperature"]; !ok {
				t.Fatalf("first request missing temperature: %#v", body)
			}
			http.Error(w, `{"error":{"message":"invalid temperature: only 1 is allowed for this model"}}`, http.StatusBadRequest)
			return
		}
		if _, ok := body["temperature"]; ok {
			t.Fatalf("retry still sent temperature: %#v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAI("test-key", srv.URL)
	resp, err := p.Chat(
		context.Background(),
		[]Message{{Role: "user", Content: "hi"}},
		nil,
		"kimi-k2.6",
		123,
		0.7,
	)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q, want ok", resp.Content)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestOpenAIUsesNewerModelChatParametersWithoutRetry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, ok := body["max_completion_tokens"]; !ok {
			t.Fatalf("request missing max_completion_tokens: %#v", body)
		}
		if _, ok := body["max_tokens"]; ok {
			t.Fatalf("request sent legacy max_tokens: %#v", body)
		}
		if _, ok := body["temperature"]; ok {
			t.Fatalf("request sent unsupported temperature: %#v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAI("test-key", srv.URL)
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "openai/gpt-5.5", 123, 0.7)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q, want ok", resp.Content)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestOpenAIKeepsLegacyChatParametersForUnknownModels(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, ok := body["max_tokens"]; !ok {
			t.Fatalf("request missing legacy max_tokens: %#v", body)
		}
		if _, ok := body["max_completion_tokens"]; ok {
			t.Fatalf("request sent max_completion_tokens for unknown model: %#v", body)
		}
		if _, ok := body["temperature"]; !ok {
			t.Fatalf("request missing temperature for unknown model: %#v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAI("test-key", srv.URL)
	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "compatible-model", 123, 0.7)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q, want ok", resp.Content)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}
