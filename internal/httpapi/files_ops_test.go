package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeWorkspaceFile drops a file straight into the workspace root, which is
// what the file manager sees as "/".
func writeWorkspaceFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestFileCopyAndMoveRoutes(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	root := env.srv.files.Workspace().Root()

	writeWorkspaceFile(t, root, "asli.txt", "isi")

	status, body := doJSON(t, env, client, http.MethodPost, "/api/files/copy", csrf,
		`{"from":"asli.txt","to":"salinan.txt"}`)
	mustStatus(t, status, http.StatusCreated, body)
	var entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(body, &entry); err != nil {
		t.Fatalf("decode entry: %v", err)
	}
	if entry.Name != "salinan.txt" {
		t.Fatalf("name = %q", entry.Name)
	}
	if got, err := os.ReadFile(filepath.Join(root, "salinan.txt")); err != nil || string(got) != "isi" {
		t.Fatalf("copy content = %q err = %v", got, err)
	}

	// Without overwrite a second copy is a conflict, not a silent clobber.
	status, body = doJSON(t, env, client, http.MethodPost, "/api/files/copy", csrf,
		`{"from":"asli.txt","to":"salinan.txt"}`)
	mustStatus(t, status, http.StatusConflict, body)
	if code, _ := decodeErrorFrom(t, body); code != CodeConflict {
		t.Fatalf("code = %q, want %q", code, CodeConflict)
	}

	status, body = doJSON(t, env, client, http.MethodPost, "/api/files/copy", csrf,
		`{"from":"asli.txt","to":"salinan.txt","overwrite":true}`)
	mustStatus(t, status, http.StatusCreated, body)

	status, body = doJSON(t, env, client, http.MethodPost, "/api/files/move", csrf,
		`{"from":"salinan.txt","to":"pindah.txt"}`)
	mustStatus(t, status, http.StatusOK, body)
	if _, err := os.Stat(filepath.Join(root, "salinan.txt")); !os.IsNotExist(err) {
		t.Fatalf("source survived a move: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "pindah.txt")); err != nil {
		t.Fatalf("moved file missing: %v", err)
	}
}

func TestFileCopyRejectsOutsideDestination(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	root := env.srv.files.Workspace().Root()

	writeWorkspaceFile(t, root, "a.txt", "a")
	status, body := doJSON(t, env, client, http.MethodPost, "/api/files/copy", csrf,
		`{"from":"a.txt","to":"../keluar.txt"}`)
	mustStatus(t, status, http.StatusForbidden, body)
}

func TestFileStatRoute(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, _ := login(t, env)
	root := env.srv.files.Workspace().Root()

	writeWorkspaceFile(t, root, "stat.txt", "tujuh")
	status, body := doJSON(t, env, client, http.MethodGet, "/api/files/stat?path=stat.txt", "", "")
	mustStatus(t, status, http.StatusOK, body)

	var stat struct {
		Name  string `json:"name"`
		Size  int64  `json:"size"`
		Mode  string `json:"mode"`
		Owner string `json:"owner"`
		Group string `json:"group"`
		Links uint64 `json:"links"`
	}
	if err := json.Unmarshal(body, &stat); err != nil {
		t.Fatalf("decode stat: %v", err)
	}
	if stat.Name != "stat.txt" || stat.Size != 5 {
		t.Fatalf("stat = %+v", stat)
	}
	if stat.Owner == "" || stat.Group == "" || stat.Links == 0 {
		t.Fatalf("stat missing owner/group/links: %+v", stat)
	}

	status, body = doJSON(t, env, client, http.MethodGet, "/api/files/stat?path=hilang.txt", "", "")
	mustStatus(t, status, http.StatusNotFound, body)
}

func TestFileChmodRouteRefusesPrivilegedBits(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	root := env.srv.files.Workspace().Root()

	writeWorkspaceFile(t, root, "skrip.sh", "#!/bin/sh\n")

	status, body := doJSON(t, env, client, http.MethodPatch, "/api/files/mode", csrf,
		`{"path":"skrip.sh","mode":"755"}`)
	mustStatus(t, status, http.StatusOK, body)
	var entry struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(body, &entry); err != nil {
		t.Fatalf("decode entry: %v", err)
	}
	if entry.Mode != "-rwxr-xr-x" {
		t.Fatalf("mode = %q", entry.Mode)
	}

	for _, mode := range []string{"4755", "2755", "1755", "7777", "lala", ""} {
		status, body := doJSON(t, env, client, http.MethodPatch, "/api/files/mode", csrf,
			`{"path":"skrip.sh","mode":"`+mode+`"}`)
		mustStatus(t, status, http.StatusBadRequest, body)
	}

	// Nothing above changed the file.
	info, err := os.Stat(filepath.Join(root, "skrip.sh"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("perm = %o", info.Mode().Perm())
	}
}

func TestFileZipRoute(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, _ := login(t, env)
	root := env.srv.files.Workspace().Root()

	if err := os.MkdirAll(filepath.Join(root, "arsip", "dalam"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeWorkspaceFile(t, root, "arsip/a.txt", "aaa")
	writeWorkspaceFile(t, root, "arsip/dalam/b.txt", "bbb")

	resp, err := client.Get(env.ts.URL + "/api/files/zip?path=" + url.QueryEscape("arsip"))
	if err != nil {
		t.Fatalf("zip request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content-type = %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment;") || !strings.Contains(cd, "arsip.zip") {
		t.Fatalf("content-disposition = %q", cd)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	names := make([]string, 0, len(reader.File))
	for _, f := range reader.File {
		names = append(names, f.Name)
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"arsip/a.txt", "arsip/dalam/", "arsip/dalam/b.txt"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("archive %v missing %q", names, want)
		}
	}

	// A missing path must still answer with a JSON error, not a broken archive.
	resp2, err := client.Get(env.ts.URL + "/api/files/zip?path=hilang")
	if err != nil {
		t.Fatalf("zip missing: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("missing path status = %d", resp2.StatusCode)
	}
}

func TestFileUnzipRoute(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	root := env.srv.files.Workspace().Root()

	// Build a small archive with the standard library, then extract it through
	// the API.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("halo.txt")
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if _, err := w.Write([]byte("halo dunia")); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "paket.zip"), buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	status, body := doJSON(t, env, client, http.MethodPost, "/api/files/unzip", csrf,
		`{"path":"paket.zip","dest":"hasil"}`)
	mustStatus(t, status, http.StatusOK, body)
	var out struct {
		OK      bool `json:"ok"`
		Entries int  `json:"entries"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK || out.Entries != 1 {
		t.Fatalf("unzip result = %+v", out)
	}
	got, err := os.ReadFile(filepath.Join(root, "hasil", "halo.txt"))
	if err != nil || string(got) != "halo dunia" {
		t.Fatalf("extracted content = %q err = %v", got, err)
	}

	// Extracting again conflicts, and overwrite resolves it.
	status, body = doJSON(t, env, client, http.MethodPost, "/api/files/unzip", csrf,
		`{"path":"paket.zip","dest":"hasil"}`)
	mustStatus(t, status, http.StatusConflict, body)
	status, body = doJSON(t, env, client, http.MethodPost, "/api/files/unzip", csrf,
		`{"path":"paket.zip","dest":"hasil","overwrite":true}`)
	mustStatus(t, status, http.StatusOK, body)
}

func TestFileUnzipRouteRejectsZipSlip(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	root := env.srv.files.Workspace().Root()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../kabur.txt")
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if _, err := w.Write([]byte("pwned")); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "jahat.zip"), buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	status, body := doJSON(t, env, client, http.MethodPost, "/api/files/unzip", csrf,
		`{"path":"jahat.zip","dest":"hasil"}`)
	mustStatus(t, status, http.StatusBadRequest, body)
	if _, err := os.Stat(filepath.Join(root, "kabur.txt")); !os.IsNotExist(err) {
		t.Fatalf("escaping file was created: %v", err)
	}
}

func TestTerminalCreateRejectsUnusableDirectory(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, csrf := login(t, env)
	root := env.srv.files.Workspace().Root()

	writeWorkspaceFile(t, root, "berkas.txt", "x")

	for _, dir := range []string{"/tidak-ada", "/etc/passwd", "berkas.txt"} {
		status, body := doJSON(t, env, client, http.MethodPost, "/api/terminal/sessions", csrf,
			`{"kind":"shell","dir":"`+dir+`"}`)
		mustStatus(t, status, http.StatusBadRequest, body)
	}

	// A real directory is accepted, and the session starts there.
	status, body := doJSON(t, env, client, http.MethodPost, "/api/terminal/sessions", csrf,
		`{"kind":"shell","dir":"/","cols":80,"rows":24}`)
	mustStatus(t, status, http.StatusCreated, body)
	var created terminalSessionView
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("session = %+v", created)
	}
	status, body = doJSON(t, env, client, http.MethodDelete,
		"/api/terminal/sessions/"+created.ID, csrf, "")
	mustStatus(t, status, http.StatusOK, body)
}

// decodeErrorFrom reads the {"error":{...}} envelope out of an already-consumed
// response body.
func decodeErrorFrom(t *testing.T, body []byte) (string, string) {
	t.Helper()
	var out struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode error: %v (body %s)", err, body)
	}
	return out.Error.Code, out.Error.Message
}
