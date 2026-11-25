package setup

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeKnowledgeFilename(t *testing.T) {
	cases := map[string]string{
		"客户管理手册.md":          "客户管理手册.md",
		"01-销售流程（新版）.md":     "01-销售流程（新版）.md",
		"../../etc/passwd":      "passwd",
		`C:\Users\me\笔记.txt`:   "笔记.txt",
		"a/b:c*?.md":            "b-c--.md",
		"..":                    "",
		"  .hidden.md  ":        "hidden.md",
		"report v2 (final).csv": "report v2 (final).csv",
	}
	for in, want := range cases {
		if got := sanitizeKnowledgeFilename(in); got != want {
			t.Errorf("sanitizeKnowledgeFilename(%q) = %q, want %q", in, got, want)
		}
	}

	long := strings.Repeat("客户", 50) + ".md"
	got := sanitizeKnowledgeFilename(long)
	if len(got) > maxKnowledgeFilenameBytes || !utf8.ValidString(got) || !strings.HasSuffix(got, ".md") {
		t.Fatalf("long name truncated badly: %q (%d bytes)", got, len(got))
	}
	if again := sanitizeKnowledgeFilename(got); again != got {
		t.Fatalf("not idempotent: %q -> %q", got, again)
	}
}
