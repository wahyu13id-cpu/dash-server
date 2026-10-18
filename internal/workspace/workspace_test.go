package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func newTestWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	root := t.TempDir()
	ws, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return ws, root
}

func TestResolveAcceptsNestedPaths(t *testing.T) {
	ws, root := newTestWorkspace(t)
	got, err := ws.Resolve("a/b/c.txt")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := filepath.Join(root, "a", "b", "c.txt")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResolveEmptyIsRoot(t *testing.T) {
	ws, root := newTestWorkspace(t)
	for _, p := range []string{"", ".", "/", "./"} {
		got, err := ws.Resolve(p)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", p, err)
		}
		if filepath.Clean(got) != filepath.Clean(root) {
			t.Fatalf("Resolve(%q) = %q, want root %q", p, got, root)
		}
	}
}

func TestResolveRejectsTraversal(t *testing.T) {
	ws, _ := newTestWorkspace(t)
	cases := []string{
		"..",
		"../",
		"../etc/passwd",
		"a/../../etc/passwd",
		"a/b/../../../root",
		`..\..\windows\system32`,
	}
	for _, c := range cases {
		if _, err := ws.Resolve(c); err == nil {
			t.Fatalf("Resolve(%q) unexpectedly succeeded", c)
		} else if !errors.Is(err, ErrEscape) {
			t.Fatalf("Resolve(%q) error = %v, want ErrEscape", c, err)
		}
	}
}

func TestResolveAcceptsAbsolutePathInsideRoot(t *testing.T) {
	ws, root := newTestWorkspace(t)
	got, err := ws.Resolve(filepath.Join(root, "a", "b.txt"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := filepath.Join(root, "a", "b.txt")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResolveRejectsAbsolutePathsOutsideRoot(t *testing.T) {
	ws, _ := newTestWorkspace(t)
	for _, c := range []string{"/etc/passwd", "/tmp", "/var/www"} {
		if _, err := ws.Resolve(c); err == nil {
			t.Fatalf("Resolve(%q) unexpectedly succeeded", c)
		} else if !errors.Is(err, ErrEscape) {
			t.Fatalf("Resolve(%q) error = %v, want ErrEscape", c, err)
		}
	}
}

func TestResolveFullAccessRoot(t *testing.T) {
	ws, err := New("/")
	if err != nil {
		t.Fatalf("New(/): %v", err)
	}
	for _, p := range []string{"/", "/root", "/etc/hosts", "/var/log", "/usr/local/bin"} {
		got, err := ws.Resolve(p)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", p, err)
		}
		if got != filepath.Clean(p) {
			t.Fatalf("Resolve(%q) = %q", p, got)
		}
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behaviour differs on windows")
	}
	ws, root := newTestWorkspace(t)

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A symlinked file pointing outside the workspace must be rejected.
	linkFile := filepath.Join(root, "escape.txt")
	if err := os.Symlink(secret, linkFile); err != nil {
		t.Fatalf("symlink file: %v", err)
	}
	if _, err := ws.Resolve("escape.txt"); err == nil {
		t.Fatal("Resolve of symlinked file outside root unexpectedly succeeded")
	}

	// A symlinked directory pointing outside the workspace must be rejected
	// for both the link itself and paths nested under it.
	linkDir := filepath.Join(root, "escape-dir")
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Fatalf("symlink dir: %v", err)
	}
	for _, p := range []string{"escape-dir", "escape-dir/secret.txt"} {
		if _, err := ws.Resolve(p); err == nil {
			t.Fatalf("Resolve(%q) through escaping symlink unexpectedly succeeded", p)
		}
	}
}

func TestResolveAllowsInternalSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behaviour differs on windows")
	}
	ws, root := newTestWorkspace(t)
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(root, "real"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Resolve("link/child.txt"); err != nil {
		t.Fatalf("Resolve of internal symlink failed: %v", err)
	}
}

func TestNewRejectsFileRoot(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(file); err == nil {
		t.Fatal("New accepted a regular file as workspace root")
	}
}
