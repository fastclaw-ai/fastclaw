package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Relays that put an OpenAI API in front of Claude number tool calls by
// Anthropic content block, so after a thinking block the first call is
// index 1. Seen in production: a lone call at index 1 was dropped and the
// turn failed with "model returned an empty response".
func TestOpenAIToolCallIndexNotFromZero(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"role":"assistant","content":""}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"\n"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"toolu_a","type":"function","function":{"name":"web_search","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"query\":\"a\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":2,"id":"toolu_b","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"b\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAI("k", srv.URL)
	sr, err := p.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "m", 1024, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []ToolCall
	for {
		c, ok := sr.Next()
		if !ok {
			break
		}
		if c.Done {
			got = c.ToolCalls
		}
	}
	if len(got) != 2 || got[0].ID != "toolu_a" || got[1].ID != "toolu_b" || got[0].Function.Arguments != `{"query":"a"}` {
		t.Fatalf("stream tool calls = %+v", got)
	}

	resp, err := p.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "m", 1024, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 2 || resp.ToolCalls[0].ID != "toolu_a" || resp.ToolCalls[1].ID != "toolu_b" {
		t.Fatalf("chat tool calls = %+v", resp.ToolCalls)
	}
}
