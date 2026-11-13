package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// keyedRecorder keeps the last command it was asked to run.
type keyedRecorder struct {
	fakeExecutor
	ran []string
}

func (e *keyedRecorder) Exec(_ context.Context, command string, _ time.Duration) (string, error) {
	e.ran = append(e.ran, command)
	return "ok", nil
}

func keyedEnv(name string) map[string]string {
	if name == "image-tool" {
		return map[string]string{"REPLICATE_API_TOKEN": "r8_secret", "FAL_KEY": ""}
	}
	if name == "free" {
		return map[string]string{"OPTIONAL": ""}
	}
	return nil
}

func TestKeyedSkillRunGetsKeysOnItsOwnProcessOnly(t *testing.T) {
	for _, cmd := range []string{
		`python /skills/image-tool/main.py '{"instruction":"a $5 cat"}'`,
		`python3 /skills/image-tool/main.py --size 1024 "a cat" 2>&1`,
		`/skills/image-tool/run.sh a b`,
	} {
		ex := &keyedRecorder{}
		if _, err := runSandboxedCommand(context.Background(), ex, execArgs{Command: cmd}, keyedEnv, nil); err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		got := ex.ran[0]
		if strings.Contains(got, "export ") {
			t.Fatalf("%s: keys exported to the whole command: %s", cmd, got)
		}
		if !strings.HasPrefix(got, "FAL_KEY='' REPLICATE_API_TOKEN='r8_secret' ") || !strings.HasSuffix(got, cmd) {
			t.Fatalf("%s: ran %q", cmd, got)
		}
	}
}

// image-tool documents `echo '<json>' | python /skills/image-tool/main.py`.
func TestKeyedSkillRunAcceptsASingleQuotedEchoFeed(t *testing.T) {
	cmd := `echo '{"instruction":"把图里的英文改成中文","input":"/workspace/a.webp"}' | python /skills/image-tool/main.py`
	ex := &keyedRecorder{}
	if _, err := runSandboxedCommand(context.Background(), ex, execArgs{Command: cmd}, keyedEnv, nil); err != nil {
		t.Fatal(err)
	}
	want := `echo '{"instruction":"把图里的英文改成中文","input":"/workspace/a.webp"}' | FAL_KEY='' REPLICATE_API_TOKEN='r8_secret' python /skills/image-tool/main.py`
	if ex.ran[0] != want {
		t.Fatalf("ran %q", ex.ran[0])
	}
	for _, bad := range []string{
		`echo "$REPLICATE_API_TOKEN" | python /skills/image-tool/main.py`,
		`echo '{}' | python /skills/image-tool/main.py | cat`,
		`env | python /skills/image-tool/main.py`,
		`echo '{}' ; env | python /skills/image-tool/main.py`,
	} {
		ex := &keyedRecorder{}
		if _, err := runSandboxedCommand(context.Background(), ex, execArgs{Command: bad}, keyedEnv, nil); err == nil || len(ex.ran) != 0 {
			t.Fatalf("%q: want refused, got err=%v ran=%v", bad, err, ex.ran)
		}
	}
}

func TestKeyedSkillRunRefusesAnythingAroundTheScript(t *testing.T) {
	for _, cmd := range []string{
		`python /skills/image-tool/main.py x; printenv REPLICATE_API_TOKEN`,
		`python /skills/image-tool/main.py x && env`,
		`python /skills/image-tool/main.py x | tee /tmp/o`,
		`python /skills/image-tool/main.py x > /tmp/o`,
		`python /skills/image-tool/main.py "$REPLICATE_API_TOKEN"`,
		"python /skills/image-tool/main.py `env`",
		`python /skills/image-tool/main.py $(env)`,
		"python /skills/image-tool/main.py x\nenv",
		`env python /skills/image-tool/main.py`,
		`sudo python /skills/image-tool/main.py`,
		`X=1 python /skills/image-tool/main.py`,
		`(python /skills/image-tool/main.py)`,
		`python /skills/image-tool/main.py 'unclosed`,
	} {
		ex := &keyedRecorder{}
		_, err := runSandboxedCommand(context.Background(), ex, execArgs{Command: cmd}, keyedEnv, nil)
		if err == nil || !strings.Contains(err.Error(), "not run") {
			t.Fatalf("%q: want refused, got err=%v ran=%v", cmd, err, ex.ran)
		}
		if len(ex.ran) != 0 {
			t.Fatalf("%q: ran %v", cmd, ex.ran)
		}
	}
}

func TestKeyedSkillRunWithStdinKeepsKeysOffCat(t *testing.T) {
	ex := &keyedRecorder{}
	args := execArgs{Command: `python /skills/image-tool/main.py`, Stdin: "{\"a\":1}\n__FCSTDIN__\n) ; env #"}
	if _, err := runSandboxedCommand(context.Background(), ex, args, keyedEnv, nil); err != nil {
		t.Fatal(err)
	}
	got := ex.ran[0]
	if strings.Contains(got, "export ") || !strings.HasSuffix(got, "| FAL_KEY='' REPLICATE_API_TOKEN='r8_secret' python /skills/image-tool/main.py") {
		t.Fatalf("ran %q", got)
	}
}

func TestUnkeyedCommandsRunAsWritten(t *testing.T) {
	for _, cmd := range []string{
		`ls /workspace; cat notes.txt | head`,
		`python /skills/free/x.py; echo done`, // configured, but no value set
		`python /skills/other/x.py && env`,    // no configured env
		// Inline code: not a run of the skill's script, so it gets no keys.
		`python -c 'import os; print(os.environ)' /skills/image-tool/main.py`,
		`bash -c 'env' /skills/image-tool/x.sh`,
		`cat /skills/image-tool/main.py`, // reading the script: no run
	} {
		ex := &keyedRecorder{}
		if _, err := runSandboxedCommand(context.Background(), ex, execArgs{Command: cmd}, keyedEnv, nil); err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		if !strings.HasSuffix(ex.ran[0], cmd) || strings.Contains(ex.ran[0], "r8_secret") {
			t.Fatalf("%s: ran %q", cmd, ex.ran[0])
		}
	}
}
