package agl

import "strings"

// What each known local agent is good at, so the LLM agent can pick one
// for a request instead of assuming they only write code. Keep entries
// short and factual; unknown agents get none.
var agentStrengths = map[string]string{
	"codex": "code, files and shell; GENERATES REAL IMAGES with its built-in image generation tool (photos, illustrations, portraits, logos, posters). " +
		"For any picture, write in the task: \"use your built-in image generation tool (imagegen), not code/SVG/Pillow\", and ask it to copy the generated PNG " +
		"(saved under ~/.codex/generated_images) to a normal folder such as ~/Downloads and report the absolute path.",
	"claude": "code, files, shell, analysis and writing documents; can't generate images (it can only draw SVG/HTML or write code that makes charts).",
	"gemini": "code, files, shell and analysis.",
	"grok":   "code, files, shell and analysis.",
}

// AgentStrengths returns what the local agent id is good at, or "".
func AgentStrengths(id string) string { return agentStrengths[strings.ToLower(strings.TrimSpace(id))] }
