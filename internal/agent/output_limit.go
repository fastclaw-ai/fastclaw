package agent

import (
	"fmt"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/session"
)

// maxOutputLimitRetries is how many times a turn re-asks the model after a
// reply that stopped at the output token limit with nothing usable: no text,
// or tool calls whose arguments may be cut off.
const maxOutputLimitRetries = 2

// maxEmptyReplyRetries is how many times a reply with no text and no
// tool calls is asked for again before the turn fails.
const maxEmptyReplyRetries = 2

// cutOffAtOutputLimit reports whether resp stopped at the output limit and
// can't be used as is. A cut-off answer with text and no tool calls is kept:
// the user still gets most of it.
func cutOffAtOutputLimit(resp *provider.Response) bool {
	return resp.Truncated && (resp.HasToolCalls() || resp.Content == "")
}

// outputLimitMessage tells the model its last reply was cut off and dropped,
// and how to fit the next one under the limit.
func outputLimitMessage(maxTokens int) provider.Message {
	limit := "the output limit"
	if maxTokens > 0 {
		limit = fmt.Sprintf("the output limit (%d tokens)", maxTokens)
	}
	return provider.Message{
		Role: "system",
		Content: "Your last reply was cut off at " + limit + " and was discarded: nothing in it ran. " +
			"Keep every reply and every tool call well under that limit. Don't put a long script, prompt or document into one tool call; " +
			"write long content to a file in several smaller parts, or do the work in smaller steps.",
	}
}

// recordTurnFailure saves why a turn ended without an answer as the turn's
// assistant message, so the conversation shows it after the live events
// are gone — API and IM turns have no other record of it.
func (a *Agent) recordTurnFailure(sess *session.Session, text string) {
	sess.Append(provider.Message{
		Role:      "assistant",
		Content:   text,
		Timestamp: time.Now().UnixMilli(),
		Metadata:  map[string]any{"error": true},
	})
}
