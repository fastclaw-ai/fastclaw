package agl

import (
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Capabilities a local agent can have beyond code, as agl reports them
// (AgentInfo.capabilities, agl ≥ 0.3.6). LLM agents get a dedicated tool
// for some (image_generation → generate_image), so FastClaw — not the
// model — writes the local agent's task for them.
const (
	CapImageGeneration = "image_generation"
	CapWebSearch       = "web_search"
	CapBrowser         = "browser"
	CapComputerUse     = "computer_use"
)

// MinCapabilitiesVersion is the first agl that reports capabilities. For
// older ones FastClaw assumes legacyCapabilities.
const MinCapabilitiesVersion = "0.3.6"

// legacyCapabilities is what agents had before agl reported it: Codex's
// image generation is on by default, Claude Code always searches the web.
var legacyCapabilities = map[string][]string{
	"codex":  {CapImageGeneration},
	"claude": {CapWebSearch},
}

// capabilityPhrases describe a capability in the LLM agent's hint. A
// reported capability without a phrase is kept (API, devices page) but
// not offered to the model: CapBrowser, because Codex's browser control
// fails to open pages when driven headless (agl 0.3.7, Codex 0.166).
var capabilityPhrases = map[string]string{
	CapImageGeneration: "GENERATES REAL IMAGES with its image generation model (photos, illustrations, portraits, logos, posters) — for pictures use the generate_image tool, which runs it for you",
	CapWebSearch:       "can search the web",
	CapComputerUse:     "can see and operate the apps on this computer (Computer Use: read what an app shows, click, type); the person is asked to allow each app the first time",
}

// Capabilities returns what agent a can do beyond code on m.
func (m LocalMachine) Capabilities(a store.MachineAgent) []string {
	if m.AglVersion != "" && CompareVersions(m.AglVersion, MinCapabilitiesVersion) >= 0 {
		return a.Capabilities
	}
	return legacyCapabilities[strings.ToLower(a.ID)]
}

// HasCapability reports whether agent a on m has capability cap.
func (m LocalMachine) HasCapability(a store.MachineAgent, cap string) bool {
	for _, c := range m.Capabilities(a) {
		if c == cap {
			return true
		}
	}
	return false
}

// CapableAgent is a local agent with a capability, on one machine.
type CapableAgent struct {
	Machine LocalMachine
	Agent   string
}

// FindCapable lists the agents with cap across machines, online machines
// first; want ("" = any) narrows it to one machine by id or name. Only
// agents in prefer are returned, in that order (FastClaw knows how to
// brief only those).
func FindCapable(machines []LocalMachine, cap, want string, prefer []string) []CapableAgent {
	var online, offline []CapableAgent
	want = strings.TrimSpace(want)
	for _, m := range machines {
		if want != "" && m.ID != want && !strings.EqualFold(m.Name, want) {
			continue
		}
		for _, id := range prefer {
			for _, a := range m.Agents {
				if !strings.EqualFold(a.ID, id) || !m.HasCapability(a, cap) {
					continue
				}
				c := CapableAgent{Machine: m, Agent: a.ID}
				if m.Online {
					online = append(online, c)
				} else {
					offline = append(offline, c)
				}
			}
		}
	}
	return append(online, offline...)
}

// AgentProfile is the hint for agent a on m: what it is good at, plus
// its capabilities there.
func AgentProfile(m LocalMachine, a store.MachineAgent) string {
	parts := []string{}
	if s := AgentStrengths(a.ID); s != "" {
		parts = append(parts, s)
	}
	for _, c := range m.Capabilities(a) {
		if p := capabilityPhrases[c]; p != "" {
			parts = append(parts, p)
		}
	}
	if strings.EqualFold(a.ID, "codex") && !m.HasCapability(a, CapImageGeneration) {
		parts = append(parts, "image generation is turned off on this computer")
	}
	return strings.Join(parts, "; ")
}
