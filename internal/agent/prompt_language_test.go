package agent

import (
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// Persona files are often written in Chinese; the reply must still follow
// the person's language, and the rule must sit after the persona so it
// isn't read as overridden by it.
func TestPrompt_ReplyFollowsTheirLanguage(t *testing.T) {
	for _, mode := range []string{config.PromptModeChatbot, config.PromptModeAgent} {
		cb := newOperatorBuilder(t)
		cb.SetPromptMode(mode)
		prompt := cb.BuildSystemPromptAs(ownerUID, cb.memory.WithUserID(ownerUID), false)
		rule := strings.Index(prompt, "# Reply language")
		if rule < 0 {
			t.Fatalf("%s mode: prompt has no reply-language rule", mode)
		}
		for _, want := range []string{"language of the person's latest message", "use English"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s mode: prompt lacks %q", mode, want)
			}
		}
		if soul := strings.Index(prompt, "SOUL.md"); soul > rule {
			t.Errorf("%s mode: reply-language rule comes before the persona", mode)
		}
	}
}
