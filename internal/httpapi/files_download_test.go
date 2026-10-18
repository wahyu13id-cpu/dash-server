package httpapi

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileDownload(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, _ := login(t, env)
	root := env.srv.files.Workspace().Root()
	content := "baris satu\nbaris dua\n"
	if err := os.WriteFile(filepath.Join(root, "laporan.txt"), []byte(content), 0o640); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, err := client.Get(env.ts.URL + "/api/files/download?" + url.Values{"path": {"laporan.txt"}}.Encode())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "laporan.txt") {
		t.Fatalf("content-disposition = %q", cd)
	}
	if resp.ContentLength != int64(len(content)) {
		t.Fatalf("content-length = %d, want %d", resp.ContentLength, len(content))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != content {
		t.Fatalf("body = %q", body)
	}
}

// A downloaded HTML file must never be rendered in the dashboard's origin.
func TestFileDownloadForcesAttachmentForWebFiles(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, _ := login(t, env)
	root := env.srv.files.Workspace().Root()
	if err := os.WriteFile(filepath.Join(root, "jahat.html"), []byte("<script>alert(1)</script>"), 0o640); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp, err := client.Get(env.ts.URL + "/api/files/download?" + url.Values{"path": {"jahat.html"}}.Encode())
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("content-type = %q, want application/octet-stream", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("content-disposition = %q", cd)
	}
}

func TestFileDownloadOddName(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, _ := login(t, env)
	root := env.srv.files.Workspace().Root()

	// Quotes, spaces and non-ASCII must survive in the Content-Disposition.
	for _, name := range []string{`lapo"ran.txt`, "laporan akhir (2026).txt", "catatan-räksä.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o640); err != nil {
			t.Fatalf("seed %q: %v", name, err)
		}
		resp, err := client.Get(env.ts.URL + "/api/files/download?" + url.Values{"path": {name}}.Encode())
		if err != nil {
			t.Fatalf("download %q: %v", name, err)
		}
		cd := resp.Header.Get("Content-Disposition")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%q: status = %d, want 200", name, resp.StatusCode)
		}
		if !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "filename") {
			t.Fatalf("%q: content-disposition = %q", name, cd)
		}
	}
}

func TestFileDownloadRangeRequest(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, _ := login(t, env)
	root := env.srv.files.Workspace().Root()
	if err := os.WriteFile(filepath.Join(root, "besar.bin"), []byte("0123456789"), 0o640); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, env.ts.URL+"/api/files/download?"+url.Values{"path": {"besar.bin"}}.Encode(), nil)
	req.Header.Set("Range", "bytes=2-5")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "2345" {
		t.Fatalf("body = %q", body)
	}
}

func TestFileDownloadRequiresAuth(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	query := url.Values{"path": {"/"}}.Encode()

	resp, err := http.Get(env.ts.URL + "/api/files/download?" + query)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous: status = %d, want 401", resp.StatusCode)
	}
}

func TestFileDownloadValidation(t *testing.T) {
	env := newTestEnv(t)
	defer env.close()
	client, _ := login(t, env)
	root := env.srv.files.Workspace().Root()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cases := []struct {
		name string
		path string
		want int
	}{
		{"missing param", "", http.StatusBadRequest},
		{"missing file", "hilang.txt", http.StatusNotFound},
		{"directory", "sub", http.StatusBadRequest},
		{"escape", "../../etc/passwd", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := client.Get(env.ts.URL + "/api/files/download?" + url.Values{"path": {tc.path}}.Encode())
			if err != nil {
				t.Fatalf("download: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}
