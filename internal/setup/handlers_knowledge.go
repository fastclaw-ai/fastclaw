package setup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Knowledge base endpoints: owner-uploaded reference files an agent
// answers from. Raw files live in agent_files under the knowledge/
// prefix (source of truth); each upload is also chunked into
// agent_knowledge_chunks (derived, rebuildable) for the agent's
// knowledge_search tool. Prompt assembly picks full-injection vs
// search-tool mode by corpus size — see internal/agent/knowledge.go.

const maxKnowledgeFileBytes = 256 * 1024
const knowledgeFilePrefix = "knowledge/"

// knowledgeFileExts is the upload allowlist. Text-native formats only:
// the chunker and prompt injection treat bytes as UTF-8 text, so a PDF
// or docx would index as garbage. Binary formats need an extraction
// pipeline first — reject them with a clear error instead.
var knowledgeFileExts = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".csv": true,
	".json": true, ".yaml": true, ".yml": true, ".log": true,
}

// validateKnowledgeFile rejects non-text uploads: unknown extensions,
// invalid UTF-8, or NUL bytes (a binary payload with a text extension).
func validateKnowledgeFile(name string, data []byte) error {
	ext := strings.ToLower(filepath.Ext(name))
	if !knowledgeFileExts[ext] {
		return fmt.Errorf("unsupported file type %q; supported: .md .markdown .txt .csv .json .yaml .yml .log", ext)
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return errors.New("file content is not valid UTF-8 text; convert it to a plain-text format first")
	}
	return nil
}

func (s *Server) handleListAgentKnowledgeFiles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	rec, err := s.dataStore.GetAgent(r.Context(), id)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	docs, err := s.dataStore.ListAgentKnowledgeDocs(r.Context(), id, rec.UserID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(docs))
	for _, doc := range docs {
		out = append(out, map[string]any{
			"name":       knowledgeDisplayName(doc.Path),
			"storedName": strings.TrimPrefix(doc.Path, knowledgeFilePrefix),
			"path":       doc.Path,
			"size":       len(doc.Content),
			"hash":       knowledgeFileHash([]byte(doc.Content)),
		})
	}
	jsonResponse(w, http.StatusOK, map[string]any{"files": out})
}

func (s *Server) handleUploadAgentKnowledgeFile(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	rec, err := s.dataStore.GetAgent(r.Context(), id)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	caller := s.effectiveUserID(r)
	ident, _ := auth.FromContext(r.Context())
	if rec.UserID != caller && !ident.CanAdminPlatform() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"error": "not your agent"})
		return
	}
	if err := r.ParseMultipartForm(maxKnowledgeFileBytes + 64*1024); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "missing file"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxKnowledgeFileBytes+1))
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if len(data) > maxKnowledgeFileBytes {
		jsonResponse(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "knowledge file is too large; maximum size is 256KB"})
		return
	}
	name := sanitizeKnowledgeFilename(header.Filename)
	if name == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "invalid filename"})
		return
	}
	if err := validateKnowledgeFile(name, data); err != nil {
		jsonResponse(w, http.StatusUnsupportedMediaType, map[string]any{"error": err.Error()})
		return
	}
	hash := knowledgeFileHash(data)
	if existing, ok := s.findKnowledgeFileByHash(r.Context(), id, rec.UserID, hash); ok {
		jsonResponse(w, http.StatusOK, map[string]any{
			"ok":        true,
			"duplicate": true,
			"file": map[string]any{
				"name":       knowledgeDisplayName(existing),
				"storedName": strings.TrimPrefix(existing, knowledgeFilePrefix),
				"path":       existing,
				"size":       len(data),
				"hash":       hash,
			},
		})
		return
	}
	path := uniqueKnowledgePath(r.Context(), s.dataStore, id, rec.UserID, hash, name)
	if err := s.dataStore.SaveAgentFile(r.Context(), id, rec.UserID, path, data); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := s.indexKnowledgeFile(r.Context(), id, rec.UserID, path, data); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateUser(rec.UserID)
	jsonResponse(w, http.StatusOK, map[string]any{
		"ok": true,
		"file": map[string]any{
			"name":       knowledgeDisplayName(path),
			"storedName": strings.TrimPrefix(path, knowledgeFilePrefix),
			"path":       path,
			"size":       len(data),
			"hash":       hash,
		},
	})
}

func (s *Server) handleGetAgentKnowledgeFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := sanitizeKnowledgeFilename(r.PathValue("name"))
	if name == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "invalid filename"})
		return
	}
	if !s.requireAgentReadable(w, r, id) {
		return
	}
	rec, err := s.dataStore.GetAgent(r.Context(), id)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	path := knowledgeFilePrefix + name
	data, err := s.dataStore.GetAgentFileExact(r.Context(), id, rec.UserID, path)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"name":       knowledgeDisplayName(path),
		"storedName": name,
		"path":       path,
		"content":    string(data),
		"size":       len(data),
		"hash":       knowledgeFileHash(data),
	})
}

func (s *Server) handleDeleteAgentKnowledgeFile(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	name := sanitizeKnowledgeFilename(r.PathValue("name"))
	if name == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "invalid filename"})
		return
	}
	rec, err := s.dataStore.GetAgent(r.Context(), id)
	if err != nil || rec == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	caller := s.effectiveUserID(r)
	ident, _ := auth.FromContext(r.Context())
	if rec.UserID != caller && !ident.CanAdminPlatform() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"error": "not your agent"})
		return
	}
	path := knowledgeFilePrefix + name
	if err := s.dataStore.DeleteAgentFile(r.Context(), id, rec.UserID, path); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := s.dataStore.DeleteAgentKnowledgeChunks(r.Context(), id, rec.UserID, path); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.invalidateUser(rec.UserID)
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

const maxKnowledgeFilenameBytes = 120

// sanitizeKnowledgeFilename keeps letters and digits from any script
// (CJK filenames must survive), plus . - _ space and common brackets;
// everything else — path separators, control chars, punctuation that
// could confuse URLs or shells — becomes '-'. Truncation is rune-safe
// and preserves the extension.
func sanitizeKnowledgeFilename(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	name = strings.TrimSpace(filepath.Base(name))
	name = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), unicode.IsMark(r):
			return r
		case r == '.', r == '-', r == '_', r == ' ', r == '(', r == ')',
			r == '（', r == '）', r == '【', r == '】':
			return r
		default:
			return '-'
		}
	}, name)
	name = strings.Trim(name, ". ")
	if name == "" || name == "." || name == ".." {
		return ""
	}
	if len(name) > maxKnowledgeFilenameBytes {
		ext := filepath.Ext(name)
		if len(ext) > 16 {
			ext = ""
		}
		stem := strings.TrimSuffix(name, ext)
		keep := maxKnowledgeFilenameBytes - len(ext)
		for keep > 0 && !utf8.RuneStart(stem[keep]) {
			keep--
		}
		name = strings.TrimRight(stem[:keep], ". ") + ext
	}
	return name
}

func knowledgeFileHash(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

// indexKnowledgeFile (re)builds the derived search chunks for one raw
// knowledge file. Chunks are rebuildable from the raw row at any time —
// e.g. after a chunker change, re-saving the file reindexes it.
func (s *Server) indexKnowledgeFile(ctx context.Context, agentID, userID, path string, data []byte) error {
	return s.dataStore.SaveAgentKnowledgeChunks(ctx, agentID, userID, path, knowledgeFileHash(data), chunkKnowledgeText(data))
}

// knowledgeDisplayName strips the knowledge/ prefix plus the 12-hex
// dedup hash prefix, recovering the filename the owner uploaded.
func knowledgeDisplayName(path string) string {
	name := strings.TrimPrefix(path, knowledgeFilePrefix)
	if len(name) > 13 && name[12] == '-' {
		prefix := name[:12]
		allHex := true
		for _, r := range prefix {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
				allHex = false
				break
			}
		}
		if allHex {
			return name[13:]
		}
	}
	return name
}

// chunkKnowledgeText splits a text file into overlapping ~2400-char
// chunks along blank-line boundaries (paragraphs stay whole when they
// fit; oversized blocks are hard-split with overlap).
func chunkKnowledgeText(data []byte) []string {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r", "\n"))
	if text == "" {
		return nil
	}
	const target = 2400
	const overlap = 240
	var chunks []string
	var current strings.Builder
	flush := func() {
		chunk := strings.TrimSpace(current.String())
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		current.Reset()
	}
	for _, block := range strings.Split(text, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		if current.Len() > 0 && current.Len()+len(block)+2 > target {
			prev := current.String()
			flush()
			runes := []rune(prev)
			if len(runes) > overlap {
				current.WriteString(string(runes[len(runes)-overlap:]))
				current.WriteString("\n\n")
			}
		}
		if len(block) > target {
			runes := []rune(block)
			for start := 0; start < len(runes); {
				end := start + target
				if end > len(runes) {
					end = len(runes)
				}
				if current.Len() > 0 {
					flush()
				}
				chunks = append(chunks, strings.TrimSpace(string(runes[start:end])))
				if end == len(runes) {
					break
				}
				start = end - overlap
				if start < 0 {
					start = end
				}
			}
			continue
		}
		if current.Len() > 0 {
			current.WriteString("\n\n")
		}
		current.WriteString(block)
	}
	flush()
	return chunks
}

func (s *Server) findKnowledgeFileByHash(ctx context.Context, agentID, userID, hash string) (string, bool) {
	docs, err := s.dataStore.ListAgentKnowledgeDocs(ctx, agentID, userID)
	if err != nil {
		return "", false
	}
	for _, doc := range docs {
		if knowledgeFileHash([]byte(doc.Content)) == hash {
			return doc.Path, true
		}
	}
	return "", false
}

type agentFileExactGetter interface {
	GetAgentFileExact(ctx context.Context, agentID, userID, filename string) ([]byte, error)
}

// uniqueKnowledgePath builds the stored path: knowledge/<hash12>-<name>,
// with a numeric suffix on the (hash-collision-grade unlikely) clash.
func uniqueKnowledgePath(ctx context.Context, st agentFileExactGetter, agentID, userID, hash, filename string) string {
	base := knowledgeFilePrefix + hash[:12] + "-" + filename
	if _, err := st.GetAgentFileExact(ctx, agentID, userID, base); errors.Is(err, store.ErrNotFound) {
		return base
	}
	ext := filepath.Ext(filename)
	stem := strings.TrimSuffix(filename, ext)
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s%s-%s-%d%s", knowledgeFilePrefix, hash[:12], stem, i, ext)
		if _, err := st.GetAgentFileExact(ctx, agentID, userID, candidate); errors.Is(err, store.ErrNotFound) {
			return candidate
		}
	}
}
