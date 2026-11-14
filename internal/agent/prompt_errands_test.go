package agent

import (
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// A personal agent asked to order a coffee must work the errand through
// the browser instead of declining for lack of an API — in chatbot mode
// (every WeClaw agent) as much as in agent mode.
func TestPrompt_ErrandsWhenBrowserAvailable(t *testing.T) {
	for _, mode := range []string{config.PromptModeChatbot, config.PromptModeAgent} {
		cb := newOperatorBuilder(t)
		cb.SetPromptMode(mode)
		cb.sandboxEnabled = true
		prompt := cb.BuildSystemPromptAs(ownerUID, cb.memory.WithUserID(ownerUID), false)
		for _, want := range []string{
			"# Errands",
			"Not having an API for a",
			"wait for an explicit yes",
			"Payment is always the user's",
		} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s mode: prompt lacks %q", mode, want)
			}
		}
	}
}

// Without a sandbox there is no browser, so the section would promise
// something the agent can't do.
func TestPrompt_NoErrandsWithoutBrowser(t *testing.T) {
	cb := newOperatorBuilder(t)
	cb.SetPromptMode(config.PromptModeChatbot)
	prompt := cb.BuildSystemPromptAs(ownerUID, cb.memory.WithUserID(ownerUID), false)
	if strings.Contains(prompt, "# Errands") {
		t.Error("errands section emitted without a sandbox")
	}
}
