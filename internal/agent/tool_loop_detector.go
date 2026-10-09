package agent

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

const toolLoopDetectionThreshold = 3

type toolLoopDetector struct {
	last        toolLoopSignature
	consecutive int
}

type toolLoopSignature struct {
	name       string
	inputHash  [32]byte
	resultHash [32]byte
}

func (d *toolLoopDetector) Observe(tc provider.ToolCall, result string) bool {
	sig := toolLoopSignature{
		name:       tc.Function.Name,
		inputHash:  sha256.Sum256([]byte(tc.Function.Arguments)),
		resultHash: sha256.Sum256([]byte(strings.TrimSpace(result))),
	}
	if sig.name == d.last.name && sig.inputHash == d.last.inputHash && sig.resultHash == d.last.resultHash {
		d.consecutive++
	} else {
		d.consecutive = 1
		d.last = sig
	}
	return d.consecutive >= toolLoopDetectionThreshold
}

// Reset forgets the current streak, so the next identical call counts
// from one again (used after the loop warning has been given).
func (d *toolLoopDetector) Reset() {
	*d = toolLoopDetector{}
}

// failedCallsBudgetMessage tells the model it has spent the turn's
// error-retry budget (maxFailedToolCallsPerTurn); tools are dropped for
// the call it accompanies.
func failedCallsBudgetMessage(failed int) provider.Message {
	return provider.Message{
		Role: "system",
		Content: fmt.Sprintf(
			"%d tool calls have failed in this turn. Stop retrying. Answer the user with what you have: say what worked, what kept failing and the error it gave, and what they could do next.",
			failed),
	}
}

func repeatedToolCallWarning(content string) provider.Message {
	return provider.Message{Role: "system", Content: content}
}