// session-vault-server stores Claude Code session transcripts uploaded by the
// session-vault plugin (/save) and serves them back (/fetch).
//
// Transcripts are opaque blobs: their JSONL format is internal to Claude Code,
// and clients may upload them end-to-end encrypted (X-Encrypted header), so
// the server never parses or inspects them.
//
// Configuration (environment variables):
//
//	SESSION_VAULT_ADDR          listen address (default ":8080")
//	SESSION_VAULT_DATA          storage directory (default "data")
//	SESSION_VAULT_TOKEN_HASHES  comma-separated "sha256hex:user" pairs
//	                            (preferred: no plaintext tokens on the server)
//	SESSION_VAULT_TOKENS        comma-separated plaintext "token:user" pairs
//	                            (convenience; hashed in memory at startup)
//	SESSION_VAULT_TOKEN         single plaintext token mapped to user "default"
//
// Compute a token hash with: printf '%s' "$TOKEN" | sha256sum
//
// Storage layout: <data>/<user>/<project>/<session-id>.data (the blob,
// exactly as uploaded after transport decompression) plus a
// <session-id>.meta.json sidecar.
package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxUploadBytes bounds a single (possibly gzip-compressed) upload body.
const maxUploadBytes = 256 << 20

var (
	validName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	validHash = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

type Meta struct {
	SessionID string `json:"session_id"`
	// Name is an optional user-chosen label for the session, so /fetch can
	// address sessions by something friendlier than a UUID.
	Name       string    `json:"name,omitempty"`
	Project    string    `json:"project"`
	Cwd        string    `json:"cwd,omitempty"`
	Size       int64     `json:"size"`
	UploadedAt time.Time `json:"uploaded_at"`
	// Encrypted records the client-declared encryption scheme (e.g.
	// "age+gzip"). Empty means the blob is a plaintext transcript.
	Encrypted string `json:"encrypted,omitempty"`
}

type server struct {
	dataDir string
	tokens  map[string]string // sha256(token) hex -> user
	mu      sync.Mutex        // serializes blob+meta commits (fixes interleaved re-uploads)
	// maxUpload bounds a single (possibly gzip-compressed) upload body, and
	// the decompressed size when the body is gzip. Configurable so tests can
	// use a small cap.
	maxUpload int64
}

var version = "dev"

func main() {
	addr := envOr("SESSION_VAULT_ADDR", ":8080")
	dataDir := envOr("SESSION_VAULT_DATA", "data")

	tokens, err := loadTokens()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatal(err)
	}

	s := &server{dataDir: dataDir, tokens: tokens, maxUpload: maxUploadBytes}

	log.Printf("session-vault-server %s listening on %s (data dir: %s, %d token(s))", version, addr, dataDir, len(tokens))
	log.Fatal(http.ListenAndServe(addr, routes(s)))
}

// routes wires the HTTP endpoints; split out from main so tests can run
// them through httptest.
func routes(s *server) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, "ok\n"); err != nil {
			return
		}
	})
	mux.HandleFunc("POST /api/v1/sessions", s.auth(s.upload))
	mux.HandleFunc("GET /api/v1/sessions", s.auth(s.list))
	mux.HandleFunc("GET /api/v1/sessions/{id}", s.auth(s.download))
	mux.HandleFunc("GET /api/v1/sessions/{id}/meta", s.auth(s.meta))
	return mux
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// limitedReader reads from r and fails once the cumulative byte count
// exceeds cap. It bounds decompressed gzip uploads so a small compressed
// body can't expand into a disk-filling stream (gzip bomb).
type limitedReader struct {
	r   io.Reader
	cap int64
	n   int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.n >= l.cap {
		return 0, fmt.Errorf("decompressed upload exceeds %d bytes", l.cap)
	}
	if int64(len(p)) > l.cap-l.n {
		p = p[:l.cap-l.n]
	}
	n, err := l.r.Read(p)
	l.n += int64(n)
	return n, err
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func loadTokens() (map[string]string, error) {
	tokens := map[string]string{}
	if pairs := os.Getenv("SESSION_VAULT_TOKEN_HASHES"); pairs != "" {
		for _, pair := range strings.Split(pairs, ",") {
			hash, user, ok := strings.Cut(strings.TrimSpace(pair), ":")
			if !ok || !validHash.MatchString(hash) || !validName.MatchString(user) {
				return nil, fmt.Errorf("SESSION_VAULT_TOKEN_HASHES: malformed entry %q (want sha256hex:user)", pair)
			}
			tokens[strings.ToLower(hash)] = user
		}
	}
	if pairs := os.Getenv("SESSION_VAULT_TOKENS"); pairs != "" {
		for _, pair := range strings.Split(pairs, ",") {
			token, user, ok := strings.Cut(strings.TrimSpace(pair), ":")
			if !ok || token == "" || !validName.MatchString(user) {
				return nil, fmt.Errorf("SESSION_VAULT_TOKENS: malformed entry %q (want token:user)", pair)
			}
			tokens[hashToken(token)] = user
		}
	}
	if token := os.Getenv("SESSION_VAULT_TOKEN"); token != "" {
		tokens[hashToken(token)] = "default"
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("no tokens configured: set SESSION_VAULT_TOKEN_HASHES, SESSION_VAULT_TOKENS, or SESSION_VAULT_TOKEN")
	}
	return tokens, nil
}

func (s *server) auth(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		user := s.tokens[hashToken(strings.TrimSpace(token))]
		if !ok || user == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r, user)
	}
}

func (s *server) upload(w http.ResponseWriter, r *http.Request, user string) {
	id := r.Header.Get("X-Session-Id")
	project := r.Header.Get("X-Project")
	if !validName.MatchString(id) || !validName.MatchString(project) {
		http.Error(w, "invalid or missing X-Session-Id / X-Project header", http.StatusBadRequest)
		return
	}
	name := r.Header.Get("X-Name")
	if name != "" && (len(name) > 64 || !validName.MatchString(name)) {
		http.Error(w, "invalid X-Name header (want [A-Za-z0-9_-], max 64 chars)", http.StatusBadRequest)
		return
	}
	if name == "" {
		// An unnamed re-save of an already-labeled session keeps its label.
		if old, err := readMeta(filepath.Join(s.dataDir, user, project, id+".meta.json")); err == nil {
			name = old.Name
		}
	}

	body := io.Reader(http.MaxBytesReader(w, r.Body, s.maxUpload))
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(body)
		if err != nil {
			http.Error(w, "invalid gzip body", http.StatusBadRequest)
			return
		}
		defer func() { _ = gz.Close() }()
		body = &limitedReader{r: gz, cap: s.maxUpload}
	}

	// 0700: the directory may hold plaintext transcripts.
	dir := filepath.Join(s.dataDir, user, project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		internalError(w, err)
		return
	}

	// Write to a temp file first so a failed upload never truncates an
	// already-stored blob.
	tmp, err := os.CreateTemp(dir, id+".*.tmp")
	if err != nil {
		internalError(w, err)
		return
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	size, err := io.Copy(tmp, body)
	if err != nil {
		_ = tmp.Close()
		http.Error(w, "upload failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := tmp.Close(); err != nil {
		internalError(w, err)
		return
	}

	meta := Meta{
		SessionID:  id,
		Name:       name,
		Project:    project,
		Cwd:        r.Header.Get("X-Cwd"),
		Size:       size,
		UploadedAt: time.Now().UTC(),
		Encrypted:  r.Header.Get("X-Encrypted"),
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		internalError(w, err)
		return
	}

	// Commit blob and meta as one critical section: the rename and the
	// atomic meta write happen under the same lock, so a re-upload of the
	// same id can never leave a blob and a stale meta from different writes.
	s.mu.Lock()
	if err := os.Rename(tmp.Name(), filepath.Join(dir, id+".data")); err != nil {
		s.mu.Unlock()
		internalError(w, err)
		return
	}
	if err := writeFileAtomic(filepath.Join(dir, id+".meta.json"), metaBytes); err != nil {
		s.mu.Unlock()
		internalError(w, err)
		return
	}
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, meta)
}

func (s *server) list(w http.ResponseWriter, _ *http.Request, user string) {
	metaFiles, err := filepath.Glob(filepath.Join(s.dataDir, user, "*", "*.meta.json"))
	if err != nil {
		internalError(w, err)
		return
	}

	metas := make([]Meta, 0, len(metaFiles))
	for _, path := range metaFiles {
		m, err := readMeta(path)
		if err != nil {
			continue
		}
		metas = append(metas, m)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].UploadedAt.After(metas[j].UploadedAt) })

	writeJSON(w, http.StatusOK, metas)
}

func (s *server) download(w http.ResponseWriter, r *http.Request, user string) {
	path, ok := s.findSession(w, r.PathValue("id"), user)
	if !ok {
		return
	}
	contentType := "application/x-ndjson"
	if m, err := readMeta(metaPath(path)); err == nil && m.Encrypted != "" {
		contentType = "application/octet-stream"
		w.Header().Set("X-Encrypted", m.Encrypted)
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeFile(w, r, path)
}

func (s *server) meta(w http.ResponseWriter, r *http.Request, user string) {
	path, ok := s.findSession(w, r.PathValue("id"), user)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	http.ServeFile(w, r, metaPath(path))
}

// findSession resolves a session id to its blob path for the given user,
// writing the appropriate error response when it can't.
func (s *server) findSession(w http.ResponseWriter, id, user string) (string, bool) {
	if !validName.MatchString(id) {
		http.Error(w, "invalid session id", http.StatusBadRequest)
		return "", false
	}
	matches, err := filepath.Glob(filepath.Join(s.dataDir, user, "*", id+".data"))
	if err != nil {
		internalError(w, err)
		return "", false
	}
	if len(matches) == 0 {
		http.Error(w, "session not found", http.StatusNotFound)
		return "", false
	}
	return matches[0], true
}

func metaPath(blobPath string) string {
	return strings.TrimSuffix(blobPath, ".data") + ".meta.json"
}

// writeFileAtomic writes data to path via a temp file and rename, so a
// failed write never leaves a truncated or stale file in place. The
// resulting file has mode 0600 (the CreateTemp default).
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func readMeta(path string) (Meta, error) {
	var m Meta
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(data, &m)
	return m, err
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writing response: %v", err)
	}
}

func internalError(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
