package provider

import (
	"context"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"testing"
)

func TestBYOKBlocksPrivateDial(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:80", "[::1]:80", "10.0.0.1:443", "169.254.169.254:80", "100.64.0.1:80"} {
		if c, err := publicDial(context.Background(), "tcp", addr); err == nil {
			c.Close()
			t.Fatalf("dial permitted %s", addr)
		}
	}
	for _, kind := range []string{"openai-completions", "anthropic-messages"} {
		p := FromConfig(config.ProviderConfig{APIKey: "test", APIBase: "https://api.example.com", APIType: kind, BYOK: true})
		switch v := p.(type) {
		case *OpenAIProvider:
			if v.client.CheckRedirect == nil {
				t.Fatal("missing redirect guard")
			}
		case *AnthropicProvider:
			if v.client.CheckRedirect == nil {
				t.Fatal("missing redirect guard")
			}
		default:
			t.Fatal("unsupported provider")
		}
	}
}
