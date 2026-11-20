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
	"sync"
	"testing"
	"time"
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

func (s planStore) recordSession(context.Context, string, string, string) {}
func (s planStore) forgetSession(context.Context, string, string)         {}

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

// recordingStore is a planStore that also logs the usage bookkeeping
// calls, to prove upload and delete reach the token store.
type recordingStore struct {
	planStore
	recorded  []string
	forgotten []string
}

func (s *recordingStore) recordSession(_ context.Context, user, id, project string) {
	s.recorded = append(s.recorded, user+"/"+id+"@"+project)
}

func (s *recordingStore) forgetSession(_ context.Context, user, id string) {
	s.forgotten = append(s.forgotten, user+"/"+id)
}

func TestUploadAndDeleteBookkeeping(t *testing.T) {
	store := &recordingStore{planStore: planStore{m: map[string]principal{hashToken("tok"): {user: "u"}}}}
	s := &server{dataDir: t.TempDir(), tokens: store, maxUpload: maxUploadBytes}
	ts := httptest.NewServer(routes(s))
	t.Cleanup(ts.Close)

	if code, _ := uploadBody(t, ts, "tok", "abc", "proj1"); code != http.StatusCreated {
		t.Fatalf("upload = %d, want 201", code)
	}
	// Re-save: still booked (the upsert bumps updated_at server-side).
	if code, _ := uploadBody(t, ts, "tok", "abc", "proj1"); code != http.StatusCreated {
		t.Fatalf("re-save = %d, want 201", code)
	}
	if len(store.recorded) != 2 || store.recorded[0] != "u/abc@proj1" {
		t.Fatalf("recorded = %v, want two u/abc@proj1 entries", store.recorded)
	}

	resp := doReq(t, ts, http.MethodDelete, ts.URL+"/api/v1/sessions/abc", "tok", nil, nil)
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d, want 200", resp.StatusCode)
	}
	if len(store.forgotten) != 1 || store.forgotten[0] != "u/abc" {
		t.Fatalf("forgotten = %v, want [u/abc]", store.forgotten)
	}
}

func TestPlanQuota(t *testing.T) {
	limit := planLimits["free"]
	planLimits["free"] = 2
	t.Cleanup(func() { planLimits["free"] = limit })

	s := &server{
		dataDir:    t.TempDir(),
		tokens:     planStore{m: map[string]principal{hashToken("tok"): {user: "u", plan: "free"}}},
		maxUpload:  maxUploadBytes,
		upgradeURL: upgradeURLFor("https://vault.example"),
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
	if body["plan"] != "free" || body["limit"] != float64(2) || body["used"] != float64(2) {
		t.Fatalf("quota body = %v, want plan=free limit=2 used=2", body)
	}
	// The configured upgrade URL is offered to the client.
	if body["upgrade_url"] != "https://vault.example/account" {
		t.Fatalf("quota body upgrade_url = %v, want the configured URL", body["upgrade_url"])
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

// Concurrent uploads of new ids must not overshoot the plan limit: the
// quota is re-checked under the commit lock.
func TestPlanQuotaConcurrentUploads(t *testing.T) {
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

	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Plain client calls: t.Fatal must not run off the test goroutine.
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/sessions", bytes.NewReader([]byte("line")))
			req.Header.Set("Authorization", "Bearer tok")
			req.Header.Set("X-Session-Id", fmt.Sprintf("c%d", i))
			req.Header.Set("X-Project", "p")
			req.Header.Set("X-Encrypted", encryptionScheme)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	ids, err := s.sessionIDs("u")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("stored %d sessions, want exactly the limit (2)", len(ids))
	}
}

// upgradeURLFor derives the upgrade page from the web app's base URL and
// invents nothing when there is no web app.
func TestUpgradeURLFor(t *testing.T) {
	cases := []struct{ base, want string }{
		{"", ""},
		{"https://vault.example", "https://vault.example/account"},
		{"https://vault.example/", "https://vault.example/account"},
	}
	for _, c := range cases {
		if got := upgradeURLFor(c.base); got != c.want {
			t.Errorf("upgradeURLFor(%q) = %q, want %q", c.base, got, c.want)
		}
	}
}

// A deployment with no upgrade URL (self-hosted, no billing page) must
// not invent one: the 402 keeps its details but omits the key.
func TestQuotaWithoutUpgradeURL(t *testing.T) {
	limit := planLimits["free"]
	planLimits["free"] = 1
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
	code, body := uploadBody(t, ts, "tok", "a2", "p1")
	if code != http.StatusPaymentRequired {
		t.Fatalf("upload over the cap = %d, want 402 (body %v)", code, body)
	}
	if _, ok := body["upgrade_url"]; ok {
		t.Fatalf("quota body = %v, must not offer an upgrade URL when none is configured", body)
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
	// Uploads carry the encryption header the client always sends,
	// unless the test sets it itself (an empty value means "omit it").
	if method == http.MethodPost {
		if _, set := headers["X-Encrypted"]; !set {
			req.Header.Set("X-Encrypted", encryptionScheme)
		}
	}
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
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
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
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

func TestPurgedAccountIsRefused(t *testing.T) {
	dir := t.TempDir()
	userDir := filepath.Join(dir, "gone@example.com", "proj")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"s1.data", "s1.meta.json", "s2.data"} {
		if err := os.WriteFile(filepath.Join(userDir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	s := &server{
		dataDir:   dir,
		tokens:    planStore{m: map[string]principal{hashToken("tok"): {user: "gone@example.com", deleteAt: &past}}},
		maxUpload: maxUploadBytes,
	}
	ts := httptest.NewServer(routes(s))
	defer ts.Close()

	resp := doReq(t, ts, http.MethodGet, ts.URL+"/api/v1/sessions", "tok", nil, nil)
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("status = %d, want 410", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, "gone@example.com")); !os.IsNotExist(err) {
		t.Fatalf("user data dir still exists after purge: %v", err)
	}
}

// Re-saving a session under another project (a session restored on a
// second machine) supersedes the old copy: one list entry, the label
// kept, and download serves the new bytes.
func TestResaveUnderOtherProjectSupersedes(t *testing.T) {
	ts, _ := testServer(t, map[string]string{"tok": "u"}, maxUploadBytes)
	up := func(project, body, name string) {
		t.Helper()
		h := map[string]string{"X-Session-Id": "sid", "X-Project": project}
		if name != "" {
			h["X-Name"] = name
		}
		resp := doReq(t, ts, http.MethodPost, ts.URL+"/api/v1/sessions", "tok", []byte(body), h)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("upload to %s = %d", project, resp.StatusCode)
		}
	}
	up("-data-me-repo", "from linux", "feature")
	up("-Users-me-repo", "from mac", "")

	resp := doReq(t, ts, http.MethodGet, ts.URL+"/api/v1/sessions", "tok", nil, nil)
	var metas []Meta
	if err := json.NewDecoder(resp.Body).Decode(&metas); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if len(metas) != 1 {
		t.Fatalf("list = %+v, want one entry", metas)
	}
	if metas[0].Project != "-Users-me-repo" || metas[0].Name != "feature" {
		t.Fatalf("meta = %+v, want the new project and the kept label", metas[0])
	}

	resp = doReq(t, ts, http.MethodGet, ts.URL+"/api/v1/sessions/sid", "tok", nil, nil)
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(got) != "from mac" {
		t.Fatalf("download = %q, want the newest save", got)
	}
}

// Duplicates left by older servers: download serves the newest copy, not
// whichever project sorts first.
func TestDownloadPrefersNewestExistingCopy(t *testing.T) {
	dir := t.TempDir()
	s := &server{dataDir: dir, tokens: staticTokenStore{tokens: map[string]string{hashToken("tok"): "u"}}, maxUpload: maxUploadBytes}
	ts := httptest.NewServer(routes(s))
	t.Cleanup(ts.Close)
	write := func(project, body string, at time.Time) {
		d := filepath.Join(dir, "u", project)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "sid.data"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		m, _ := json.Marshal(Meta{SessionID: "sid", Project: project, UploadedAt: at})
		if err := os.WriteFile(filepath.Join(d, "sid.meta.json"), m, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	write("-a-first", "old", now.Add(-time.Hour))
	write("-z-last", "new", now)

	resp := doReq(t, ts, http.MethodGet, ts.URL+"/api/v1/sessions/sid", "tok", nil, nil)
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(got) != "new" {
		t.Fatalf("download = %q, want the newest copy", got)
	}
}

func TestSafeNamespace(t *testing.T) {
	for _, ok := range []string{"default", "alice", "bob@example.com", "a.b+tag@example.co.uk", "u-0123abcd"} {
		if !safeNamespace(ok) {
			t.Errorf("safeNamespace(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", ".hidden", "x/../victim@example.com", `a\b`, "/abs", "a\x00b", `"z/../bob"@x.com`} {
		if safeNamespace(bad) {
			t.Errorf("safeNamespace(%q) = true, want false", bad)
		}
	}
}

// A store-supplied user name that would escape its directory is refused
// before any path is built from it.
func TestUnsafeUserNameIsRefused(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim@example.com", "p")
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "sid.data"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &server{
		dataDir:   dir,
		tokens:    planStore{m: map[string]principal{hashToken("tok"): {user: "x/../victim@example.com"}}},
		maxUpload: maxUploadBytes,
	}
	ts := httptest.NewServer(routes(s))
	t.Cleanup(ts.Close)

	resp := doReq(t, ts, http.MethodGet, ts.URL+"/api/v1/sessions/sid", "tok", nil, nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("download with an unsafe user name = %d, want 403", resp.StatusCode)
	}
	s.purgeAccount(context.Background(), "x/../victim@example.com")
	if _, err := os.Stat(filepath.Join(victim, "sid.data")); err != nil {
		t.Fatalf("purge with an unsafe user name touched another user's data: %v", err)
	}
}

// Only age+gzip uploads are stored: a missing or different scheme is
// refused before anything touches the disk.
func TestUploadRequiresEncryption(t *testing.T) {
	ts, _ := testServer(t, map[string]string{"tok": "u"}, maxUploadBytes)
	for _, scheme := range []string{"", "none", "AGE+GZIP", "age"} {
		resp := doReq(t, ts, http.MethodPost, ts.URL+"/api/v1/sessions", "tok", []byte(`{"plain":1}`), map[string]string{
			"X-Session-Id": "abc", "X-Project": "proj", "X-Encrypted": scheme,
		})
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("X-Encrypted=%q: status %d, want 400", scheme, resp.StatusCode)
		}
	}
	resp := doReq(t, ts, http.MethodGet, ts.URL+"/api/v1/sessions", "tok", nil, nil)
	var metas []Meta
	if err := json.NewDecoder(resp.Body).Decode(&metas); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if len(metas) != 0 {
		t.Fatalf("refused uploads were stored: %+v", metas)
	}
}
