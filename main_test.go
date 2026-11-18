package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// testServer builds a server rooted at a fresh temp dir with one token per
// user in users, wrapped in an httptest server.
func testServer(t *testing.T, users map[string]string, maxUpload int64) (*httptest.Server, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	tokens := map[string]string{}
	for token, user := range users {
		tokens[hashToken(token)] = user
	}
	s := &server{dataDir: dir, tokens: staticTokenStore{tokens: tokens}, maxUpload: maxUpload}
	ts := httptest.NewServer(routes(s))
	t.Cleanup(ts.Close)
	return ts, users
}

// planStore is a tokenStore whose tokens carry a plan, for quota tests.
type planStore struct{ m map[string]principal }

func (s planStore) lookup(_ context.Context, h string) (principal, bool) {
	p, ok := s.m[h]
	return p, ok
}

// uploadBody posts a session through the vault API and returns the status
// and parsed JSON body.
func uploadBody(t *testing.T, ts *httptest.Server, token, id, project string) (int, map[string]any) {
	t.Helper()
	resp := doReq(t, ts, http.MethodPost, ts.URL+"/api/v1/sessions", token, []byte("line"), map[string]string{
		"X-Session-Id": id,
		"X-Project":    project,
	})
	t.Cleanup(func() { _ = resp.Body.Close() })
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("parsing %d body: %v", resp.StatusCode, err)
	}
	return resp.StatusCode, body
}

func TestPlanQuota(t *testing.T) {
	limit := planLimits["free"]
	planLimits["free"] = 2
	t.Cleanup(func() { planLimits["free"] = limit })

	s := &server{
		dataDir:   t.TempDir(),
		tokens:    planStore{m: map[string]principal{hashToken("tok"): {user: "u", plan: "free"}}},
		maxUpload: maxUploadBytes,
	}
	ts := httptest.NewServer(routes(s))
	t.Cleanup(ts.Close)

	if code, _ := uploadBody(t, ts, "tok", "a1", "p1"); code != http.StatusCreated {
		t.Fatalf("first upload = %d, want 201", code)
	}
	if code, _ := uploadBody(t, ts, "tok", "a2", "p1"); code != http.StatusCreated {
		t.Fatalf("second upload = %d, want 201", code)
	}
	// At the cap: a new id is refused with the plan details...
	code, body := uploadBody(t, ts, "tok", "a3", "p1")
	if code != http.StatusPaymentRequired {
		t.Fatalf("upload over the cap = %d, want 402 (body %v)", code, body)
	}
	if body["plan"] != "free" || body["limit"] != float64(2) {
		t.Fatalf("quota body = %v, want plan=free limit=2", body)
	}
	// ...while re-saving an existing id is always allowed.
	if code, _ := uploadBody(t, ts, "tok", "a2", "p1"); code != http.StatusCreated {
		t.Fatalf("re-save of existing id = %d, want 201", code)
	}
	// The same id under a different project is not a new session: the
	// quota counts unique ids across projects.
	if code, _ := uploadBody(t, ts, "tok", "a1", "p2"); code != http.StatusCreated {
		t.Fatalf("same id, other project = %d, want 201", code)
	}
	// And listing is never blocked by the quota.
	resp := doReq(t, ts, http.MethodGet, ts.URL+"/api/v1/sessions", "tok", nil, nil)
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list at the cap = %d, want 200", resp.StatusCode)
	}
}

func TestQuotaUnknownPlanIsFailClosed(t *testing.T) {
	s := &server{
		dataDir:   t.TempDir(),
		tokens:    planStore{m: map[string]principal{hashToken("tok"): {user: "u", plan: "enterprise"}}},
		maxUpload: maxUploadBytes,
	}
	ts := httptest.NewServer(routes(s))
	t.Cleanup(ts.Close)

	resp := doReq(t, ts, http.MethodPost, ts.URL+"/api/v1/sessions", "tok", []byte("line"), map[string]string{
		"X-Session-Id": "a1",
		"X-Project":    "p1",
	})
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("upload with unknown plan = %d, want 403", resp.StatusCode)
	}
}

func TestStaticStoreHasNoQuota(t *testing.T) {
	// Env-token principals carry no plan: unlimited, as before.
	ts, _ := testServer(t, map[string]string{"tok": "u"}, maxUploadBytes)
	for i := 0; i < planLimits["free"]+1; i++ {
		if code, _ := uploadBody(t, ts, "tok", fmt.Sprintf("a%d", i), "p1"); code != http.StatusCreated {
			t.Fatalf("upload %d = %d, want 201 (env tokens are unlimited)", i, code)
		}
	}
}

func doReq(t *testing.T, ts *httptest.Server, method, url, token string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestLoadTokens(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		t.Setenv("SESSION_VAULT_TOKEN", "")
		t.Setenv("SESSION_VAULT_TOKENS", "")
		t.Setenv("SESSION_VAULT_TOKEN_HASHES", "")
		if _, err := loadTokens(); err == nil {
			t.Fatal("expected error when no tokens configured")
		}
	})
	t.Run("single", func(t *testing.T) {
		t.Setenv("SESSION_VAULT_TOKEN", "tok")
		tokens, err := loadTokens()
		if err != nil {
			t.Fatal(err)
		}
		if tokens[hashToken("tok")] != "default" {
			t.Fatalf("got user %q, want default", tokens[hashToken("tok")])
		}
	})
	t.Run("multi plaintext", func(t *testing.T) {
		t.Setenv("SESSION_VAULT_TOKEN", "")
		t.Setenv("SESSION_VAULT_TOKENS", "a:alice, b:bob")
		tokens, err := loadTokens()
		if err != nil {
			t.Fatal(err)
		}
		if tokens[hashToken("a")] != "alice" || tokens[hashToken("b")] != "bob" {
			t.Fatalf("unexpected tokens: %v", tokens)
		}
	})
	t.Run("multi hashes", func(t *testing.T) {
		t.Setenv("SESSION_VAULT_TOKEN", "")
		t.Setenv("SESSION_VAULT_TOKENS", "")
		t.Setenv("SESSION_VAULT_TOKEN_HASHES", hashToken("a")+":alice, "+hashToken("b")+":bob")
		tokens, err := loadTokens()
		if err != nil {
			t.Fatal(err)
		}
		if tokens[hashToken("a")] != "alice" || tokens[hashToken("b")] != "bob" {
			t.Fatalf("unexpected tokens: %v", tokens)
		}
	})
	t.Run("malformed hash", func(t *testing.T) {
		t.Setenv("SESSION_VAULT_TOKEN", "")
		t.Setenv("SESSION_VAULT_TOKENS", "")
		t.Setenv("SESSION_VAULT_TOKEN_HASHES", "nothex:alice")
		if _, err := loadTokens(); err == nil {
			t.Fatal("expected error for malformed hash")
		}
	})
}

func TestAuth(t *testing.T) {
	ts, _ := testServer(t, map[string]string{"tok": "default"}, maxUploadBytes)
	if resp := doReq(t, ts, "GET", ts.URL+"/api/v1/sessions", "", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", resp.StatusCode)
	}
	if resp := doReq(t, ts, "GET", ts.URL+"/api/v1/sessions", "wrong", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: got %d, want 401", resp.StatusCode)
	}
	if resp := doReq(t, ts, "GET", ts.URL+"/api/v1/sessions", "tok", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("good token: got %d, want 200", resp.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	ts, _ := testServer(t, map[string]string{"tok": "default"}, maxUploadBytes)
	resp := doReq(t, ts, "GET", ts.URL+"/healthz", "", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok\n" {
		t.Fatalf("got %q, want ok", body)
	}
}

func TestUploadDownloadRoundtrip(t *testing.T) {
	ts, _ := testServer(t, map[string]string{"tok": "default"}, maxUploadBytes)

	payload := []byte(`{"a":1}`)
	gz := gzipBytes(t, payload)

	resp := doReq(t, ts, "POST", ts.URL+"/api/v1/sessions", "tok", gz, map[string]string{
		"Content-Encoding": "gzip",
		"X-Session-Id":     "abc",
		"X-Project":        "proj",
		"X-Cwd":            "/tmp/proj",
		"X-Name":           "my-name",
	})
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload: got %d (%s), want 201", resp.StatusCode, body)
	}
	var meta Meta
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	if meta.Size != int64(len(payload)) {
		t.Fatalf("size: got %d, want %d", meta.Size, len(payload))
	}

	// download
	resp = doReq(t, ts, "GET", ts.URL+"/api/v1/sessions/abc", "tok", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download: got %d, want 200", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: got %q, want %q", got, payload)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("content-type: got %q", ct)
	}
}

func TestUploadKeepsNameOnUnlabeledResave(t *testing.T) {
	ts, _ := testServer(t, map[string]string{"tok": "default"}, maxUploadBytes)

	// save with a name, then save again without one; label should persist.
	resp := doReq(t, ts, "POST", ts.URL+"/api/v1/sessions", "tok", []byte(`x`), map[string]string{
		"X-Session-Id": "abc", "X-Project": "proj", "X-Name": "first",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal("labeled upload failed")
	}
	resp = doReq(t, ts, "POST", ts.URL+"/api/v1/sessions", "tok", []byte(`x`), map[string]string{
		"X-Session-Id": "abc", "X-Project": "proj",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal("unlabeled re-save failed")
	}
	var m Meta
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "first" {
		t.Fatalf("name not preserved: got %q, want first", m.Name)
	}
}

func TestUploadInvalidHeaders(t *testing.T) {
	ts, _ := testServer(t, map[string]string{"tok": "default"}, maxUploadBytes)
	for _, headers := range []map[string]string{
		{"X-Project": "proj"},                                               // missing session id
		{"X-Session-Id": "abc"},                                             // missing project
		{"X-Session-Id": "a/b", "X-Project": "proj"},                        // path separator
		{"X-Session-Id": "abc", "X-Project": "proj", "X-Name": "bad name!"}, // bad name
	} {
		resp := doReq(t, ts, "POST", ts.URL+"/api/v1/sessions", "tok", []byte(`x`), headers)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("headers %v: got %d, want 400", headers, resp.StatusCode)
		}
	}
}

func TestUploadGzipBomb(t *testing.T) {
	// cap of 100 bytes decompressed; payload expands to 1000.
	ts, _ := testServer(t, map[string]string{"tok": "default"}, 100)
	payload := bytes.Repeat([]byte("x"), 1000)
	gz := gzipBytes(t, payload)
	resp := doReq(t, ts, "POST", ts.URL+"/api/v1/sessions", "tok", gz, map[string]string{
		"Content-Encoding": "gzip",
		"X-Session-Id":     "abc",
		"X-Project":        "proj",
	})
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("bomb: got %d (%s), want 400", resp.StatusCode, body)
	}
}

func TestUserIsolation(t *testing.T) {
	ts, _ := testServer(t, map[string]string{"ta": "alice", "tb": "bob"}, maxUploadBytes)

	// alice uploads
	doReq(t, ts, "POST", ts.URL+"/api/v1/sessions", "ta", []byte(`alice-data`), map[string]string{
		"X-Session-Id": "s1", "X-Project": "proj",
	})

	// bob lists: should see none
	resp := doReq(t, ts, "GET", ts.URL+"/api/v1/sessions", "tb", nil, nil)
	body, _ := io.ReadAll(resp.Body)
	if len(bytes.TrimSpace(body)) != 0 && string(bytes.TrimSpace(body)) != "[]" {
		t.Fatalf("bob saw alice's sessions: %s", body)
	}
	// bob cannot download alice's session
	if resp := doReq(t, ts, "GET", ts.URL+"/api/v1/sessions/s1", "tb", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bob downloaded alice's session: got %d, want 404", resp.StatusCode)
	}
}

func TestEncryptedDownloadContentType(t *testing.T) {
	ts, _ := testServer(t, map[string]string{"tok": "default"}, maxUploadBytes)
	resp := doReq(t, ts, "POST", ts.URL+"/api/v1/sessions", "tok", []byte(`ciphertext`), map[string]string{
		"X-Session-Id": "abc", "X-Project": "proj", "X-Encrypted": "age+gzip",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal("upload failed")
	}
	resp = doReq(t, ts, "GET", ts.URL+"/api/v1/sessions/abc", "tok", nil, nil)
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("encrypted content-type: got %q, want application/octet-stream", ct)
	}
	if enc := resp.Header.Get("X-Encrypted"); enc != "age+gzip" {
		t.Fatalf("encrypted header: got %q", enc)
	}
}

func TestDownloadNotFound(t *testing.T) {
	ts, _ := testServer(t, map[string]string{"tok": "default"}, maxUploadBytes)
	if resp := doReq(t, ts, "GET", ts.URL+"/api/v1/sessions/nope", "tok", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("got %d, want 404", resp.StatusCode)
	}
	if resp := doReq(t, ts, "GET", ts.URL+"/api/v1/sessions/../etc", "tok", nil, nil); resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("path traversal: got %d", resp.StatusCode)
	}
}

func TestLimitedReader(t *testing.T) {
	lr := &limitedReader{r: bytes.NewReader(bytes.Repeat([]byte("y"), 50)), cap: 10}
	buf := make([]byte, 100)
	n, err := lr.Read(buf)
	if n != 10 {
		t.Fatalf("first read: got %d, want 10", n)
	}
	if err != nil {
		t.Fatalf("first read err: %v", err)
	}
	if _, err := lr.Read(buf); err == nil {
		t.Fatal("expected error after cap exceeded")
	}
}

func TestMetaWritten(t *testing.T) {
	dataDir := t.TempDir()
	s := &server{dataDir: dataDir, tokens: staticTokenStore{tokens: map[string]string{hashToken("tok"): "default"}}, maxUpload: maxUploadBytes}
	ts2 := httptest.NewServer(routes(s))
	defer ts2.Close()
	resp := doReq(t, ts2, "POST", ts2.URL+"/api/v1/sessions", "tok", []byte(`x`), map[string]string{
		"X-Session-Id": "abc", "X-Project": "proj",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatal("upload failed")
	}
	metaPath := filepath.Join(dataDir, "default", "proj", "abc.meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.SessionID != "abc" || m.Project != "proj" {
		t.Fatalf("bad meta: %+v", m)
	}
	// meta should be 0600
	info, _ := os.Stat(metaPath)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("meta perm: got %o, want 600", perm)
	}
}
