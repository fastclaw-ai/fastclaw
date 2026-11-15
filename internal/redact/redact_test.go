package redact

import (
	"strings"
	"testing"
)

func TestTextHidesRegisteredValuesAndKeyShapes(t *testing.T) {
	Register("my-provider-key-123456", "short")
	RegisterIP("47.237.74.44")
	RegisterIP("10.0.0.5") // private: ignored
	in := strings.Join([]string{
		"key=my-provider-key-123456",
		"short stays",
		"egress 47.237.74.44, lan 10.0.0.5",
		"openai sk-proj-abcdefghijklmnopqrstuvwx",
		"aws AKIAABCDEFGHIJKLMNOP",
		"gh ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"dsn postgres://app:s3cretpass@db:5432/x",
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz012345",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----",
	}, "\n")
	out := Text(in)
	for _, leak := range []string{"my-provider-key-123456", "47.237.74.44", "sk-proj-", "AKIAABCD",
		"ghp_abc", "s3cretpass", "abcdefghijklmnopqrstuvwxyz012345", "MIIE"} {
		if strings.Contains(out, leak) {
			t.Errorf("%q survived:\n%s", leak, out)
		}
	}
	for _, keep := range []string{"short stays", "10.0.0.5", "postgres://app:" + Mask + "@db:5432/x"} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q:\n%s", keep, out)
		}
	}
}

func TestTextLeavesOrdinaryTextAlone(t *testing.T) {
	in := "去 https://example.com/a?b=c 看看，sk 开头的单词 skill 和 eyJ 都没事。"
	if got := Text(in); got != in {
		t.Errorf("changed ordinary text: %q", got)
	}
}
