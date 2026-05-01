package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

const (
	// DefaultTokenThreshold is the default threshold at which compaction triggers (150K tokens, for 200K context models).
	DefaultTokenThreshold = 150000
	// PruneTurnAge is the number of recent turns to keep intact; older messages get pruned.
	PruneTurnAge = 20
	// truncatedPlaceholder replaces pruned tool results.
	truncatedPlaceholder = "[Result truncated - see memory logs]"
)

// EstimateTokens provides a rough token estimate: chars/4.
func EstimateTokens(messages []provider.Message) int {
	total := 0
	for _, m := range messages {
		total += len(m.Content) / 4
		for _, tc := range m.ToolCalls {
			total += len(tc.Function.Arguments) / 4
			total += len(tc.Function.Name) / 4
		}
	}
	return total
}

// CompactResult holds the result of a compaction operation.
type CompactResult struct {
	Messages []provider.Message
	Pruned   bool
	LogFile  string
}

// CompactMessages prunes and optionally compresses the message history when it exceeds the token threshold.
// Step 1 (Pruning): For messages older than PruneTurnAge, strip tool result content.
// Step 2 (Compression): If still over threshold after pruning, summarize older messages
// using the LLM and write full history to a log file.
func CompactMessages(messages []provider.Message, workspace string, prov provider.Provider, model string) (*CompactResult, error) {
	tokens := EstimateTokens(messages)
	if tokens < DefaultTokenThreshold {
		return &CompactResult{Messages: messages}, nil
	}

	slog.Info("context compaction triggered", "tokens", tokens, "threshold", DefaultTokenThreshold, "message_count", len(messages))

	// Write full history to log file before any modifications
	logFile, err := writeHistoryLog(messages, workspace)
	if err != nil {
		slog.Warn("failed to write history log", "error", err)
	}

	// Step 1: Pruning - strip tool results from older messages
	pruned := pruneOldToolResults(messages)
	prunedTokens := EstimateTokens(pruned)

	slog.Info("after pruning", "tokens_before", tokens, "tokens_after", prunedTokens)

	if prunedTokens < DefaultTokenThreshold {
		return &CompactResult{
			Messages: pruned,
			Pruned:   true,
			LogFile:  logFile,
		}, nil
	}

	// Step 2: Compression - summarize older messages
	compressed, err := compressOlderMessages(pruned, prov, model)
	if err != nil {
		slog.Warn("compression failed, using pruned messages", "error", err)
		return &CompactResult{
			Messages: pruned,
			Pruned:   true,
			LogFile:  logFile,
		}, nil
	}

	slog.Info("after compression", "tokens_before", prunedTokens, "tokens_after", EstimateTokens(compressed))

	return &CompactResult{
		Messages: compressed,
		Pruned:   true,
		LogFile:  logFile,
	}, nil
}

// pruneOldToolResults strips tool result content from messages older than PruneTurnAge.
func pruneOldToolResults(messages []provider.Message) []provider.Message {
	if len(messages) <= PruneTurnAge {
		return messages
	}

	cutoff := len(messages) - PruneTurnAge
	result := make([]provider.Message, len(messages))
	copy(result, messages)

	for i := 0; i < cutoff; i++ {
		if result[i].Role == "tool" && len(result[i].Content) > 200 {
			result[i] = provider.Message{
				Role:       "tool",
				Content:    truncatedPlaceholder,
				ToolCallID: result[i].ToolCallID,
				Name:       result[i].Name,
			}
		}
	}

	return result
}

// compressOlderMessages asks the LLM to summarize older messages into a compact summary.
func compressOlderMessages(messages []provider.Message, prov provider.Provider, model string) ([]provider.Message, error) {
	if len(messages) <= PruneTurnAge {
		return messages, nil
	}

	cutoff := len(messages) - PruneTurnAge
	olderMessages := messages[:cutoff]

	// Build a text representation of older messages for summarization
	var text string
	for _, m := range olderMessages {
		text += fmt.Sprintf("[%s] %s\n", m.Role, m.Content)
	}

	summaryPrompt := []provider.Message{
		{
			Role:    "system",
			Content: "You are a conversation summarizer. Summarize the following conversation history into a compact summary that preserves key facts, decisions, and context. Be concise but don't lose important details.",
		},
		{
			Role:    "user",
			Content: fmt.Sprintf("Summarize this conversation:\n\n%s", text),
		},
	}

	resp, err := prov.Chat(nil, summaryPrompt, nil, model, 2048, 0.3)
	if err != nil {
		return nil, fmt.Errorf("summarize conversation: %w", err)
	}

	// Build new message list: summary + recent messages
	compressed := make([]provider.Message, 0, PruneTurnAge+1)
	compressed = append(compressed, provider.Message{
		Role:    "user",
		Content: fmt.Sprintf("[Conversation Summary]\n%s", resp.Content),
	})
	compressed = append(compressed, messages[cutoff:]...)

	return compressed, nil
}

// writeHistoryLog writes the full message history to a JSONL log file.
func writeHistoryLog(messages []provider.Message, workspace string) (string, error) {
	logDir := filepath.Join(workspace, "memory", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", fmt.Errorf("create log dir: %w", err)
	}

	timestamp := time.Now().Format("20060102_150405")
	logFile := filepath.Join(logDir, fmt.Sprintf("history_%s.jsonl", timestamp))

	f, err := os.Create(logFile)
	if err != nil {
		return "", fmt.Errorf("create log file: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, m := range messages {
		if err := enc.Encode(m); err != nil {
			return logFile, fmt.Errorf("encode message: %w", err)
		}
	}

	slog.Info("wrote history log", "file", logFile, "messages", len(messages))
	return logFile, nil
}
