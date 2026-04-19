package privacy

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// ThreatType classifies a detected memory safety threat.
type ThreatType string

const (
	ThreatPromptInjection  ThreatType = "prompt_injection"
	ThreatCredentialLeak   ThreatType = "credential_leak"
	ThreatSSHBackdoor      ThreatType = "ssh_backdoor"
	ThreatInvisibleUnicode ThreatType = "invisible_unicode"
)

// Threat represents a single detected memory safety issue.
type Threat struct {
	Type    ThreatType
	Pattern string
	Context string // snippet of matching text
}

// Prompt injection patterns (case-insensitive).
var promptInjectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)ignore\s+previous\s+instructions`),
	regexp.MustCompile(`(?i)disregard\s+all\s+prior`),
	regexp.MustCompile(`(?i)you\s+are\s+now\b`),
	regexp.MustCompile(`(?i)forget\s+everything`),
	regexp.MustCompile(`(?i)new\s+persona`),
	regexp.MustCompile(`(?i)act\s+as\s+[^a-z]`),
}

// Credential leak patterns.
var credentialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\bAKIA[A-Z0-9]{16}\b`),
	regexp.MustCompile(`\bghp_[A-Za-z0-9]{36,}\b`),
	regexp.MustCompile(`\bxoxb-[A-Za-z0-9\-]+\b`),
	regexp.MustCompile(`\d{18,}\.[A-Za-z0-9_\-]{6,}\.[A-Za-z0-9_\-]{20,}`), // Discord token
}

// SSH backdoor patterns.
var sshBackdoorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)authorized_keys`),
	regexp.MustCompile(`(?i)(?:curl|wget)\s+[^\s]+\s*\|\s*(?:bash|sh)`),
}

// Invisible Unicode code points to detect.
var invisibleRunes = map[rune]string{
	'\u200B': "ZERO WIDTH SPACE",
	'\u200C': "ZERO WIDTH NON-JOINER",
	'\u200D': "ZERO WIDTH JOINER",
	'\uFEFF': "BOM / ZERO WIDTH NO-BREAK SPACE",
	'\u2060': "WORD JOINER",
	'\u00AD': "SOFT HYPHEN",
}

// Scan checks text for memory safety threats.
// Returns a list of detected threats (empty = safe).
func Scan(text string) []Threat {
	var threats []Threat

	// Prompt injection
	for _, re := range promptInjectionPatterns {
		if loc := re.FindStringIndex(text); loc != nil {
			threats = append(threats, Threat{
				Type:    ThreatPromptInjection,
				Pattern: re.String(),
				Context: snippet(text, loc[0], loc[1]),
			})
		}
	}

	// Credential leaks
	for _, re := range credentialPatterns {
		if loc := re.FindStringIndex(text); loc != nil {
			threats = append(threats, Threat{
				Type:    ThreatCredentialLeak,
				Pattern: re.String(),
				Context: snippet(text, loc[0], loc[1]),
			})
		}
	}

	// SSH backdoor
	for _, re := range sshBackdoorPatterns {
		if loc := re.FindStringIndex(text); loc != nil {
			threats = append(threats, Threat{
				Type:    ThreatSSHBackdoor,
				Pattern: re.String(),
				Context: snippet(text, loc[0], loc[1]),
			})
		}
	}

	// Invisible Unicode
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if name, ok := invisibleRunes[r]; ok {
			threats = append(threats, Threat{
				Type:    ThreatInvisibleUnicode,
				Pattern: name,
				Context: snippet(text, i, i+size),
			})
			break // one detection is enough
		}
		i += size
	}

	return threats
}

// snippet extracts surrounding context around a match.
func snippet(text string, start, end int) string {
	const pad = 40
	lo := start - pad
	if lo < 0 {
		lo = 0
	}
	hi := end + pad
	if hi > len(text) {
		hi = len(text)
	}
	s := text[lo:hi]
	s = strings.ReplaceAll(s, "\n", " ")
	if lo > 0 {
		s = "..." + s
	}
	if hi < len(text) {
		s = s + "..."
	}
	return s
}
