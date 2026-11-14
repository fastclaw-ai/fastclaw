package agl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Tool mode, files the other way: a coding agent on the person's computer
// can't see the chat, so a photo or document the person sent (or one the
// LLM agent made) is copied to their computer first (agl `fs.write`) and
// the task names where it is. Files land in ~/Downloads/<product>/, never
// in the project the agent works in.

// MaxPushFiles caps the files handed over with one task.
const MaxPushFiles = 10

const pushTimeout = 60 * time.Second

// ToolFile is a file from the chat handed to a local agent.
type ToolFile struct {
	Name  string
	Bytes []byte
}

// pushDir is where handed-over files go on m.
func pushDir(m LocalMachine) string {
	folder := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '-' || r == '_' {
			return r
		}
		return -1
	}, m.AppName)
	folder = strings.TrimSpace(folder)
	if folder == "" {
		folder = "FastClaw"
	}
	return "~/Downloads/" + folder
}

// pushName is a safe file name for f: no folders, not hidden.
func pushName(name string) string {
	name = path.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	name = strings.TrimLeft(name, ".")
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == '/' || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if name == "" {
		name = "file"
	}
	return name
}

// push writes files to m for run r and returns their paths there.
//
// With agl's built-in tools (≥ 0.3.14) the write goes through tools.call,
// so on an `ask` link the person approves it in the chat like any other
// change to their computer (2 = for this chat); `edit` and `full` links
// write at once. Older agl gets a plain fs.write, which needs `edit`.
func (t *Tools) push(ctx context.Context, m LocalMachine, r ToolRun, files []ToolFile) ([]string, error) {
	if len(files) > MaxPushFiles {
		return nil, toolErr("At most %d files can be handed over with one task.", MaxPushFiles)
	}
	dir := pushDir(m)
	used := map[string]bool{}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		if len(f.Bytes) == 0 {
			return nil, toolErr("%s is empty.", f.Name)
		}
		if len(f.Bytes) > MaxFetchBytes {
			return nil, toolErr("%s is larger than %d MB; it can't be handed over.", f.Name, MaxFetchBytes>>20)
		}
		name := pushName(f.Name)
		for i := 2; used[name]; i++ {
			ext := path.Ext(name)
			name = strings.TrimSuffix(pushName(f.Name), ext) + "-" + strconv.Itoa(i) + ext
		}
		used[name] = true
		p := dir + "/" + name
		content := base64.StdEncoding.EncodeToString(f.Bytes)
		if UsesBuiltinTools(m) {
			args, _ := json.Marshal(map[string]any{"path": p, "content_base64": content})
			if _, err := t.CallBuiltin(ctx, BuiltinCall{
				UserID: r.UserID, Machine: m.ID, Name: "write", Args: args, Chat: r.Chat, Notify: r.Notify,
				Title: fmt.Sprintf("Save %s (%s, from the chat) to %s", name, sizeText(len(f.Bytes)), dir),
			}); err != nil {
				return nil, err
			}
			paths = append(paths, p)
			continue
		}
		wctx, cancel := context.WithTimeout(ctx, pushTimeout)
		_, err := t.link.Request(wctx, m.ID, "fs.write", map[string]any{"path": p, "content_base64": content})
		cancel()
		if err != nil {
			if errors.Is(err, ErrOffline) {
				return nil, toolErr("The computer %s went offline. Tell the person to check it and try again.", m.Name)
			}
			if strings.Contains(err.Error(), "more access") {
				return nil, toolErr("Can't put %s on %s: its link only allows reading. Tell the person to save the file there themselves and say where, or relink the computer with more access (agl connect … --access edit).", f.Name, m.Name)
			}
			return nil, toolErr("Could not copy %s to %s: %v", f.Name, m.Name, err)
		}
		paths = append(paths, p)
	}
	return paths, nil
}

func sizeText(n int) string {
	if n < 1<<20 {
		return fmt.Sprintf("%d KB", max(1, n>>10))
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

// filesNote tells the coding agent where the handed-over files are.
func filesNote(paths []string) string {
	return "\n\nFiles from the person (sent in the chat), now on this computer:\n- " +
		strings.Join(paths, "\n- ") + "\nUse them for this task."
}
