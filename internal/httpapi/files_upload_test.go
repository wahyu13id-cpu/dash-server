package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dashboard/internal/files"
)

// uploadRequest performs an authenticated, CSRF-carrying upload request.
func uploadRequest(t *testing.T, env *testEnv, client *http.Client, csrf, query string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, env.ts.URL+"/api/files/upload?"+query, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Origin", testOrig)
	req.Header.Set(csrfHeaderName, csrf)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("upload request: %v", err)
	}
	return resp
}

func decodeError(t *testing.T, resp *http.Response) (string, string) {
	t.Helper()
	var out struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	return out.Error.Code, out.Error.Message
}

func TestFileUploadRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	root := env.srv.files.Workspace().Root()

	resp := uploadRequest(t, env, client, csrf,
		url.Values{"path": {"/"}, "name": {"unggahan.txt"}}.Encode(), strings.NewReader("isi berkas dari laptop"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var entry struct {
		Name     string `json:"name"`
		Path     string `json:"path"`
		Size     int64  `json:"size"`
		Mode     string `json:"mode"`
		Editable bool   `json:"editable"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entry); err != nil {
		t.Fatalf("decode entry: %v", err)
	}
	if entry.Name != "unggahan.txt" {
		t.Fatalf("name = %q", entry.Name)
	}
	if entry.Size != int64(len("isi berkas dari laptop")) {
		t.Fatalf("size = %d", entry.Size)
	}
	if entry.Mode != "-rw-r-----" {
		t.Fatalf("mode = %q, want -rw-r-----", entry.Mode)
	}
	if !entry.Editable {
		t.Fatal("text upload should be editable")
	}

	got, err := os.ReadFile(filepath.Join(root, "unggahan.txt"))
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if string(got) != "isi berkas dari laptop" {
		t.Fatalf("on-disk content = %q", got)
	}

	// The listing must show it right away, and the name must survive intact.
	listResp, err := client.Get(env.ts.URL + "/api/files?path=/")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer listResp.Body.Close()
	var listing struct {
		Entries []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listing); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	found := false
	for _, e := range listing.Entries {
		if e.Name == "unggahan.txt" && e.Size == entry.Size {
			found = true
		}
	}
	if !found {
		t.Fatal("uploaded file missing from listing")
	}
}

func TestFileUploadPreservesOddNames(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	root := env.srv.files.Workspace().Root()

	// A name with spaces must not be trimmed on the way in.
	name := "laporan 2026 (final).txt"
	resp := uploadRequest(t, env, client, csrf,
		url.Values{"path": {"/"}, "name": {name}}.Encode(), strings.NewReader("x"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, name)); err != nil {
		t.Fatalf("stat: %v", err)
	}
}

func TestFileUploadIntoSubdirectory(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	root := env.srv.files.Workspace().Root()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	resp := uploadRequest(t, env, client, csrf,
		url.Values{"path": {"sub"}, "name": {"data.txt"}}.Encode(), strings.NewReader("hello"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "data.txt")); err != nil {
		t.Fatalf("stat: %v", err)
	}
}

func TestFileUploadRequiresAuthAndCSRF(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	query := url.Values{"path": {"/"}, "name": {"x.txt"}}.Encode()

	// Unauthenticated.
	anon := env.client()
	resp := uploadRequest(t, env, anon, "", query, strings.NewReader("x"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous: status = %d, want 401", resp.StatusCode)
	}

	// Authenticated but without the double-submit token.
	client, csrf := login(t, env)
	resp = uploadRequest(t, env, client, "", query, strings.NewReader("x"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("without CSRF: status = %d, want 403", resp.StatusCode)
	}

	// Bad origin is rejected as well.
	req, _ := http.NewRequest(http.MethodPost, env.ts.URL+"/api/files/upload?"+query, strings.NewReader("x"))
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set(csrfHeaderName, csrf)
	resp2, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("bad origin: status = %d, want 403", resp2.StatusCode)
	}
}

func TestFileUploadStatsAdvertiseLimit(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, _ := login(t, env)

	resp, err := client.Get(env.ts.URL + "/api/stats")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	defer resp.Body.Close()
	var stats struct {
		MaxUploadBytes int64 `json:"maxUploadBytes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	// The UI warns about oversized files with this number, so it must match the
	// limit the upload endpoint enforces.
	if stats.MaxUploadBytes != env.cfg.MaxUploadSize {
		t.Fatalf("maxUploadBytes = %d, want %d", stats.MaxUploadBytes, env.cfg.MaxUploadSize)
	}
}

func TestFileUploadConflictAndOverwrite(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	root := env.srv.files.Workspace().Root()
	if err := os.WriteFile(filepath.Join(root, "ada.txt"), []byte("lama"), 0o640); err != nil {
		t.Fatalf("seed: %v", err)
	}
	query := url.Values{"path": {"/"}, "name": {"ada.txt"}}.Encode()

	resp := uploadRequest(t, env, client, csrf, query, strings.NewReader("baru"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if code, _ := decodeError(t, resp); code != CodeConflict {
		t.Fatalf("code = %q, want %q", code, CodeConflict)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "ada.txt")); string(got) != "lama" {
		t.Fatalf("conflicting upload changed the file: %q", got)
	}

	resp2 := uploadRequest(t, env, client, csrf, query+"&overwrite=1", strings.NewReader("baru"))
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("overwrite status = %d, want 201", resp2.StatusCode)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "ada.txt")); string(got) != "baru" {
		t.Fatalf("content = %q, want baru", got)
	}
}

func TestFileUploadDeclaredLengthOverLimit(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)

	big := bytes.Repeat([]byte("a"), int(env.cfg.MaxUploadSize)+1)
	resp := uploadRequest(t, env, client, csrf,
		url.Values{"path": {"/"}, "name": {"big.bin"}}.Encode(), bytes.NewReader(big))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	if code, _ := decodeError(t, resp); code != CodeTooLarge {
		t.Fatalf("code = %q, want %q", code, CodeTooLarge)
	}
	if _, err := os.Stat(filepath.Join(env.srv.files.Workspace().Root(), "big.bin")); !os.IsNotExist(err) {
		t.Fatalf("oversized upload was stored (stat err = %v)", err)
	}
}

// A body with no declared length (chunked) must be stopped by the streaming cap.
func TestFileUploadStreamingCapWithoutContentLength(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	root := env.srv.files.Workspace().Root()

	env.srv.files = files.NewService(env.srv.files.Workspace(), nil, files.Options{MaxUploadSize: 16})

	// Hiding the concrete reader type keeps net/http from setting Content-Length.
	body := struct{ io.Reader }{Reader: strings.NewReader(strings.Repeat("z", 100))}
	resp := uploadRequest(t, env, client, csrf,
		url.Values{"path": {"/"}, "name": {"chunked.bin"}}.Encode(), body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".dash-upload-") || e.Name() == "chunked.bin" {
			t.Fatalf("rejected upload left %q behind", e.Name())
		}
	}
}

func TestFileUploadValidation(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	root := env.srv.files.Workspace().Root()
	if err := os.WriteFile(filepath.Join(root, "afile.txt"), []byte("x"), 0o640); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"missing name", url.Values{"path": {"/"}}.Encode(), http.StatusBadRequest},
		{"missing path", url.Values{"name": {"x.txt"}}.Encode(), http.StatusBadRequest},
		{"blank path", url.Values{"path": {"  "}, "name": {"x.txt"}}.Encode(), http.StatusBadRequest},
		{"traversal name", url.Values{"path": {"/"}, "name": {"../../evil.txt"}}.Encode(), http.StatusBadRequest},
		{"separator name", url.Values{"path": {"/"}, "name": {"a/b.txt"}}.Encode(), http.StatusBadRequest},
		{"missing dir", url.Values{"path": {"nope"}, "name": {"x.txt"}}.Encode(), http.StatusNotFound},
		{"file as dir", url.Values{"path": {"afile.txt"}, "name": {"x.txt"}}.Encode(), http.StatusBadRequest},
		{"escapes workspace", url.Values{"path": {"../../"}, "name": {"x.txt"}}.Encode(), http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := uploadRequest(t, env, client, csrf, tc.query, strings.NewReader("x"))
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}

	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "evil.txt")); err == nil {
		t.Fatal("traversal upload escaped the workspace")
	}
	if _, err := os.Stat(filepath.Join(root, "x.txt")); err == nil {
		t.Fatal("invalid upload was stored")
	}
}

// The upload must not be cut off by the connection deadlines: a large file over
// a slow line legitimately outlives the server-wide read/write timeouts, so the
// handler lifts them for this route only.
func TestFileUploadSurvivesServerTimeout(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	root := env.srv.files.Workspace().Root()

	// A second server sharing the same handler but with a short deadline (the
	// login itself must still fit, hence 700ms).
	ts := httptest.NewUnstartedServer(env.srv.Handler())
	ts.Config.ReadTimeout = 700 * time.Millisecond
	ts.Config.WriteTimeout = 700 * time.Millisecond
	ts.Start()
	defer ts.Close()

	slowEnv := *env
	slowEnv.ts = ts
	client, csrf := login(t, &slowEnv)

	// 25 bytes per read with a 200ms pause: ~1.6s of body, far past the deadline.
	payload := strings.Repeat("x", 200)
	body := io.NopCloser(newPacedReader(strings.NewReader(payload), 25, 200*time.Millisecond))
	req, err := http.NewRequest(http.MethodPost,
		ts.URL+"/api/files/upload?"+url.Values{"path": {"/"}, "name": {"slow.txt"}}.Encode(), body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.ContentLength = int64(len(payload))
	req.Header.Set("Origin", testOrig)
	req.Header.Set(csrfHeaderName, csrf)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("upload request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	got, err := os.ReadFile(filepath.Join(root, "slow.txt"))
	if err != nil {
		t.Fatalf("read upload: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("content mismatch: %d bytes", len(got))
	}
}

// pacedReader returns n bytes per Read with a pause in between, which keeps the
// connection busy for longer than the server's own timeouts.
func newPacedReader(r io.Reader, chunk int, pause time.Duration) io.Reader {
	return &pacedReader{r: r, chunk: chunk, pause: pause}
}

type pacedReader struct {
	r     io.Reader
	chunk int
	pause time.Duration
}

func (p *pacedReader) Read(b []byte) (int, error) {
	if len(b) > p.chunk {
		b = b[:p.chunk]
	}
	time.Sleep(p.pause)
	return p.r.Read(b)
}
