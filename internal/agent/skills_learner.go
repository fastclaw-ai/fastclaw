package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// SkillsLearner observes complex tasks and extracts reusable skill patterns.
type SkillsLearner struct {
	workspace    string
	provider     provider.Provider
	model        string
	minToolCalls int    // minimum tool calls to consider extracting (default: 3)
	skillDirs    []string // directories to search for the skill-learner skill
}

// NewSkillsLearner creates a new SkillsLearner.
func NewSkillsLearner(workspace string, p provider.Provider, model string, skillDirs ...string) *SkillsLearner {
	return &SkillsLearner{
		workspace:    workspace,
		provider:     p,
		model:        model,
		minToolCalls: 3,
		skillDirs:    skillDirs,
	}
}

type extractedSkill struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	Content     string `json:"content"`
}

type extractionResponse struct {
	Extract bool           `json:"extract"`
	Skill   extractedSkill `json:"skill"`
}

// MaybeExtract checks if the conversation warrants skill extraction.
// Called after agent turns complete. Extracts and saves to workspace/skills/<name>/SKILL.md.
func (sl *SkillsLearner) MaybeExtract(ctx context.Context, messages []provider.Message, toolCallCount int) error {
	if toolCallCount < sl.minToolCalls {
		return nil
	}

	skill, err := sl.extractSkill(ctx, messages)
	if err != nil {
		return fmt.Errorf("extract skill: %w", err)
	}
	if skill == nil {
		return nil
	}

	// Check if similar skill already exists
	skillDir := filepath.Join(sl.workspace, "skills", skill.Slug)
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err == nil {
		slog.Debug("skill already exists, skipping", "slug", skill.Slug)
		return nil
	}

	// Save the extracted skill
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return fmt.Errorf("create skill dir: %w", err)
	}

	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skill.Content), 0o644); err != nil {
		return fmt.Errorf("write skill: %w", err)
	}

	slog.Info("extracted new skill", "name", skill.Name, "slug", skill.Slug)
	return nil
}

// loadSkillLearnerPrompt loads the skill-learner SKILL.md from disk.
// Falls back to a minimal built-in prompt if not found.
func (sl *SkillsLearner) loadSkillLearnerPrompt() string {
	// Search skill directories for skill-learner SKILL.md
	for _, dir := range sl.skillDirs {
		path := filepath.Join(dir, "fastclaw-skill-learner", "SKILL.md")
		if data, err := os.ReadFile(path); err == nil {
			slog.Debug("loaded skill-learner prompt from file", "path", path)
			return string(data)
		}
	}

	// Fallback: minimal built-in prompt
	return fallbackExtractionPrompt
}

const fallbackExtractionPrompt = `Analyze the following conversation and determine if it demonstrates a reusable multi-step skill.

Criteria for extraction:
- The task involved 3+ tool calls in a clear, repeatable sequence
- The task is general enough to be useful in other contexts
- The steps can be described as a clear procedure

If this conversation demonstrates a reusable skill, output JSON:
{"extract": true, "skill": {"name": "Human readable name", "slug": "kebab-case-slug", "description": "One line description", "content": "Full SKILL.md content with YAML frontmatter"}}

If not reusable, output: {"extract": false}

The SKILL.md format uses YAML frontmatter:
---
name: Skill Name
description: What it does
---
Step-by-step instructions in markdown...

Output ONLY the JSON, no markdown fences.`

// extractSkill uses LLM to generate a SKILL.md from the conversation.
func (sl *SkillsLearner) extractSkill(ctx context.Context, messages []provider.Message) (*extractedSkill, error) {
	// Build a summary of the conversation for the extraction prompt
	var sb strings.Builder
	for _, m := range messages {
		if m.Role == "system" {
			continue
		}
		content := m.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		sb.WriteString(fmt.Sprintf("[%s]: %s\n", m.Role, content))
		for _, tc := range m.ToolCalls {
			sb.WriteString(fmt.Sprintf("  -> tool: %s(%s)\n", tc.Function.Name, truncate(tc.Function.Arguments, 200)))
		}
	}

	prompt := sl.loadSkillLearnerPrompt()

	extractMsgs := []provider.Message{
		{Role: "system", Content: prompt + "\n\nOutput ONLY the JSON, no markdown fences."},
		{Role: "user", Content: sb.String()},
	}

	resp, err := sl.provider.Chat(ctx, extractMsgs, nil, sl.model, 1024, 0.3)
	if err != nil {
		return nil, err
	}

	var result extractionResponse
	// Try to parse response as JSON
	content := strings.TrimSpace(resp.Content)
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		slog.Debug("skill extraction: LLM response not valid JSON", "error", err)
		return nil, nil
	}

	if !result.Extract || result.Skill.Slug == "" || result.Skill.Content == "" {
		return nil, nil
	}

	return &result.Skill, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
