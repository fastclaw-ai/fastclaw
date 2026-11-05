package agl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"path"
	"strings"
	"time"
)

// Tool mode, files: an LLM agent fetches a file a coding agent produced
// on the person's own computer (agl `fs.read`) so the IM reply can carry
// it as a photo or document. Only deliverable kinds (images, PDF, office
// documents, plain data) and never anything under a hidden directory
// (~/.ssh, ~/.aws, .git, …), so a fetch can't lift keys or config even if
// the model is talked into trying.

// MaxFetchBytes caps one fetched file (IM channels cap attachments too).
const MaxFetchBytes = 20 << 20

const fetchTimeout = 60 * time.Second

// fetchKinds maps allowed extensions to their content type.
var fetchKinds = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".pdf": "application/pdf", ".txt": "text/plain", ".md": "text/markdown", ".csv": "text/csv",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".zip":  "application/zip", ".mp4": "video/mp4", ".mp3": "audio/mpeg",
}

// FetchRun is one send_local_file call.
type FetchRun struct {
	UserID  string // the chatter (set by the turn)
	Machine string // id or name; "" = the first online machine
	Path    string // absolute, ~/…, or relative to the machine's default cwd
}

// FetchedFile is a file read from the person's computer.
type FetchedFile struct {
	Name        string
	ContentType string
	Bytes       []byte
}

// IsImage reports whether f is shown inline as a photo.
func (f FetchedFile) IsImage() bool { return strings.HasPrefix(f.ContentType, "image/") }

// fetchPath validates p and returns the path agl should read.
func fetchPath(p, defaultCwd string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || strings.ContainsRune(p, 0) || len(p) > 1024 {
		return "", toolErr(`"path" is required.`)
	}
	if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~/") {
		cwd := strings.TrimSpace(defaultCwd)
		if cwd == "" {
			return "", toolErr("Give the full path of the file (absolute or starting with ~/).")
		}
		p = strings.TrimRight(cwd, "/") + "/" + p
	}
	home := strings.HasPrefix(p, "~/")
	clean := path.Clean(strings.TrimPrefix(p, "~"))
	for _, seg := range strings.Split(clean, "/") {
		if strings.HasPrefix(seg, ".") {
			return "", toolErr("Files in hidden folders (names starting with \".\") can't be sent.")
		}
	}
	ext := strings.ToLower(path.Ext(clean))
	if ext == ".svg" {
		return "", toolErr("SVG can't be shown in chat. Ask the coding agent (run_local_agent) to export it as PNG next to the original, then send the PNG.")
	}
	if _, ok := fetchKinds[ext]; !ok {
		return "", toolErr("Only images (png, jpg, gif, webp), PDF, office documents, txt/md/csv, zip, mp4 and mp3 can be sent, not %q.", ext)
	}
	if home {
		return "~" + clean, nil
	}
	return clean, nil
}

// Fetch reads one file from the chatter's computer. Failures the model
// should explain are *ToolError.
func (t *Tools) Fetch(ctx context.Context, r FetchRun) (FetchedFile, error) {
	m, err := t.pickAny(ctx, r.UserID, r.Machine)
	if err != nil {
		return FetchedFile{}, err
	}
	p, err := fetchPath(r.Path, m.DefaultCwd)
	if err != nil {
		return FetchedFile{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	raw, err := t.link.Request(ctx, m.ID, "fs.read", map[string]any{"path": p})
	if err != nil {
		if errors.Is(err, ErrOffline) {
			return FetchedFile{}, toolErr("The computer %s went offline. Tell the person to check it and try again.", m.Name)
		}
		return FetchedFile{}, toolErr("Could not read %s on %s: %v", p, m.Name, err)
	}
	var resp struct {
		Content string `json:"content_base64"`
		Size    int64  `json:"size"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return FetchedFile{}, toolErr("The computer returned an unreadable answer for %s.", p)
	}
	if resp.Size > MaxFetchBytes || int64(base64.StdEncoding.DecodedLen(len(resp.Content))) > MaxFetchBytes+3 {
		return FetchedFile{}, toolErr("%s is larger than %d MB; it can't be sent in chat.", p, MaxFetchBytes>>20)
	}
	data, err := base64.StdEncoding.DecodeString(resp.Content)
	if err != nil || len(data) == 0 {
		return FetchedFile{}, toolErr("%s is empty or could not be read.", p)
	}
	name := path.Base(p)
	ct := fetchKinds[strings.ToLower(path.Ext(name))]
	if ct == "" {
		ct = mime.TypeByExtension(path.Ext(name))
	}
	return FetchedFile{Name: name, ContentType: ct, Bytes: data}, nil
}

// pickAny chooses one of the chatter's machines by id or name, else the
// first online one.
func (t *Tools) pickAny(ctx context.Context, userID, want string) (LocalMachine, error) {
	machines, err := t.Machines(ctx, userID)
	if err != nil {
		return LocalMachine{}, err
	}
	if len(machines) == 0 {
		return LocalMachine{}, toolErr("The person has no computer linked that allows agents to run on it.")
	}
	want = strings.TrimSpace(want)
	for _, m := range machines {
		if want != "" && m.ID != want && !strings.EqualFold(m.Name, want) {
			continue
		}
		if !m.Online {
			if want != "" {
				return LocalMachine{}, toolErr("The computer %s is offline.", m.Name)
			}
			continue
		}
		return m, nil
	}
	if want != "" {
		return LocalMachine{}, toolErr("No computer named %q. The person's computers: %s.", want, machineList(machines))
	}
	return LocalMachine{}, toolErr("The person's computer (%s) is offline.", machineList(machines))
}
