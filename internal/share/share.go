// Package share turns a file into a download link on our own domain:
// <FASTCLAW_PUBLIC_URL>/f/<token>/<filename>, valid for TTL.
//
// IM channels can only carry a file through their own upload path (for
// WeChat, iLink's CDN). When that path fails, or the file is too big or
// of a kind the client handles poorly, the person gets this link instead,
// and the model never has to reach for a public file-drop site.
//
// Files live in the workspace object store under the "_shares" scope,
// keyed by expiry day and token:
//
//	_shares/projects/<yyyymmdd>/<token>/<filename>
//
// The token carries the expiry day, so serving needs no lookup table and
// Sweep removes whole expired days.
package share

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"mime"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// TTL is how long a link works.
const TTL = 7 * 24 * time.Hour

// MaxBytes caps a shared file.
const MaxBytes = 100 << 20

const scope = "_shares"

// Service creates and serves share links.
type Service struct {
	ws   workspace.Store
	base string // public origin, no trailing slash
	now  func() time.Time
}

var (
	defMu sync.RWMutex
	def   *Service
)

// SetDefault installs the process-wide service (nil disables sharing).
func SetDefault(s *Service) { defMu.Lock(); def = s; defMu.Unlock() }

// Default returns the process-wide service, or nil when sharing is off.
func Default() *Service { defMu.RLock(); defer defMu.RUnlock(); return def }

// New returns a service storing in ws and linking under base
// (e.g. https://cloud.fastclaw.ai). Either empty → nil.
func New(ws workspace.Store, base string) *Service {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if ws == nil || base == "" {
		return nil
	}
	return &Service{ws: ws, base: base, now: time.Now}
}

const tokenAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func randomString(n int) (string, error) {
	var b strings.Builder
	max := big.NewInt(int64(len(tokenAlphabet)))
	for i := 0; i < n; i++ {
		k, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(tokenAlphabet[k.Int64()])
	}
	return b.String(), nil
}

// safeName keeps a filename usable in a URL path and object key.
func safeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == '/' || r == '"' || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	if len(name) > 160 {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		name = name[:160-len(ext)] + ext
	}
	return name
}

// Create stores data and returns its link.
func (s *Service) Create(ctx context.Context, data []byte, filename string) (string, error) {
	if s == nil {
		return "", errors.New("share: not configured")
	}
	if len(data) > MaxBytes {
		return "", fmt.Errorf("share: file is %d MB, over the %d MB limit", len(data)>>20, MaxBytes>>20)
	}
	day := s.now().UTC().Add(TTL).Format("20060102")
	rnd, err := randomString(24)
	if err != nil {
		return "", err
	}
	token := day + rnd
	name := safeName(filename)
	ctype := mime.TypeByExtension(filepath.Ext(name))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	if err := s.ws.Put(ctx, scope, day, token, name, bytes.NewReader(data), int64(len(data)), ctype); err != nil {
		return "", fmt.Errorf("share: store: %w", err)
	}
	return s.base + "/f/" + token + "/" + url.PathEscape(name), nil
}

// expired reports whether a token's day has passed (links work through
// the end of their expiry day, UTC).
func (s *Service) expired(day string) bool {
	t, err := time.Parse("20060102", day)
	if err != nil {
		return true
	}
	return !s.now().UTC().Before(t.Add(24 * time.Hour))
}

func validToken(tok string) bool {
	if len(tok) != 8+24 {
		return false
	}
	for i, r := range tok {
		if i < 8 {
			if r < '0' || r > '9' {
				return false
			}
		} else if !strings.ContainsRune(tokenAlphabet, r) {
			return false
		}
	}
	return true
}

// inlineTypes may open in the browser; everything else downloads.
var inlineTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	"application/pdf": true, "video/mp4": true, "audio/mpeg": true,
}

// ServeHTTP serves GET /f/{token}/{name}.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	name := r.PathValue("name")
	if !validToken(tok) || name == "" || s.expired(tok[:8]) {
		http.Error(w, "This link has expired or doesn't exist.", http.StatusNotFound)
		return
	}
	day := tok[:8]
	rc, err := s.ws.Get(r.Context(), scope, day, tok, safeName(name))
	if err != nil {
		if !errors.Is(err, workspace.ErrNotFound) {
			slog.Warn("share: get failed", "error", err)
		}
		http.Error(w, "This link has expired or doesn't exist.", http.StatusNotFound)
		return
	}
	defer rc.Close()
	clean := safeName(name)
	ctype := mime.TypeByExtension(filepath.Ext(clean))
	disp := "attachment"
	if inlineTypes[strings.SplitN(ctype, ";", 2)[0]] {
		disp = "inline"
	} else {
		ctype = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Disposition", fmt.Sprintf(`%s; filename*=UTF-8''%s`, disp, url.PathEscape(clean)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; media-src 'self'; sandbox")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Cache-Control", "private, max-age=3600")
	_, _ = io.Copy(w, rc)
}

// ServeDefault serves share links through the process-wide service.
func ServeDefault(w http.ResponseWriter, r *http.Request) {
	s := Default()
	if s == nil {
		http.NotFound(w, r)
		return
	}
	s.ServeHTTP(w, r)
}

// Sweep deletes the files of the last lookbackDays expired days.
func (s *Service) Sweep(ctx context.Context, lookbackDays int) {
	if s == nil {
		return
	}
	today := s.now().UTC()
	for i := 1; i <= lookbackDays; i++ {
		day := today.AddDate(0, 0, -i).Format("20060102")
		objs, err := s.ws.List(ctx, scope, day, "")
		if err != nil {
			slog.Warn("share: sweep list failed", "day", day, "error", err)
			continue
		}
		for _, o := range objs {
			if err := s.ws.Delete(ctx, scope, day, "", path.Clean(o.Path)); err != nil {
				slog.Warn("share: sweep delete failed", "day", day, "path", o.Path, "error", err)
			}
		}
		if len(objs) > 0 {
			slog.Info("share: swept expired links", "day", day, "files", len(objs))
		}
	}
}

// RunSweeper sweeps now and then daily until ctx ends.
func (s *Service) RunSweeper(ctx context.Context) {
	if s == nil {
		return
	}
	for {
		s.Sweep(ctx, 30)
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}
