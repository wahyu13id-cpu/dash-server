package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dashboard/internal/workspace"
)

func newTestService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	ws, err := workspace.New(root)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	svc := NewService(ws, nil, Options{
		MaxReadSize:    1024,
		MaxWriteSize:   2048,
		MaxUploadSize:  4096,
		MaxListEntries: 100,
	})
	return svc, root
}

func TestWriteReadRoundTrip(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Write(ctx, "notes/hello.txt", []byte("hello world")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	c, err := svc.Read(ctx, "notes/hello.txt")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if c.Content != "hello world" {
		t.Fatalf("content = %q", c.Content)
	}
	if c.Binary {
		t.Fatal("text file detected as binary")
	}
}

func TestListEntries(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, "dir", KindFolder, false); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if _, err := svc.Create(ctx, "dir/a.txt", KindFile, false); err != nil {
		t.Fatalf("create file: %v", err)
	}
	listing, err := svc.List(ctx, "dir")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if listing.TotalCount != 1 || len(listing.Entries) != 1 {
		t.Fatalf("unexpected listing: %+v", listing)
	}
	if listing.Entries[0].Name != "a.txt" {
		t.Fatalf("unexpected entry: %+v", listing.Entries[0])
	}
}

func TestCreateRejectsDuplicate(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.Create(ctx, "dup.txt", KindFile, false); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := svc.Create(ctx, "dup.txt", KindFile, false); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestWriteEnforcesSizeLimit(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	big := make([]byte, 4096)
	if _, err := svc.Write(ctx, "big.txt", big); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}

func TestReadEnforcesSizeLimit(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(root, "big.txt"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Read(ctx, "big.txt"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}

func TestOperationsRejectTraversal(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Read(ctx, "../../etc/passwd"); !errors.Is(err, workspace.ErrEscape) {
		t.Fatalf("Read traversal: got %v", err)
	}
	if _, err := svc.Write(ctx, "../evil.txt", []byte("x")); !errors.Is(err, workspace.ErrEscape) {
		t.Fatalf("Write traversal: got %v", err)
	}
	if _, err := svc.Create(ctx, "../evil", KindFolder, false); !errors.Is(err, workspace.ErrEscape) {
		t.Fatalf("Create traversal: got %v", err)
	}
	if _, err := svc.Rename(ctx, "../evil", "safe"); !errors.Is(err, workspace.ErrEscape) {
		t.Fatalf("Rename traversal: got %v", err)
	}
	if err := svc.Delete(ctx, "../evil"); !errors.Is(err, workspace.ErrEscape) {
		t.Fatalf("Delete traversal: got %v", err)
	}
}

func TestDeleteProtectsRoot(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.Delete(context.Background(), ""); !errors.Is(err, ErrRootProtected) {
		t.Fatalf("expected ErrRootProtected, got %v", err)
	}
}

func TestWriteRejectsRoot(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.Write(context.Background(), "", []byte("x")); !errors.Is(err, ErrRootProtected) {
		t.Fatalf("expected ErrRootProtected, got %v", err)
	}
}

func TestRenameMovesFile(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.Write(ctx, "old.txt", []byte("data")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Rename(ctx, "old.txt", "new.txt"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := svc.Read(ctx, "new.txt"); err != nil {
		t.Fatalf("read renamed: %v", err)
	}
	if _, err := svc.Read(ctx, "old.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for old path, got %v", err)
	}
}

func TestDeleteRemovesDirectoryRecursively(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()
	if _, err := svc.Write(ctx, "tree/child.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, "tree"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tree")); !os.IsNotExist(err) {
		t.Fatalf("directory still exists: %v", err)
	}
}

func TestReadDetectsBinary(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0x00, 0x01, 0x02, 0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := svc.Read(ctx, "bin.dat")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !c.Binary {
		t.Fatal("expected binary file to be flagged")
	}
	if c.Content != "" {
		t.Fatal("binary content must not be returned")
	}
}

func TestListRejectsFile(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.Write(ctx, "f.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.List(ctx, "f.txt"); !errors.Is(err, ErrNotDirectory) {
		t.Fatalf("expected ErrNotDirectory, got %v", err)
	}
}

func TestCreateRejectsInvalidNames(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	for _, name := range []string{"dir/..", "..", "a\x00b", "/"} {
		if _, err := svc.Create(ctx, name, KindFile, false); err == nil {
			t.Fatalf("expected error for name %q", name)
		}
	}
}

func TestCreateNestedPath(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()
	e, err := svc.Create(ctx, "nested/deep/file.txt", KindFile, false)
	if err != nil {
		t.Fatalf("create nested: %v", err)
	}
	want := filepath.ToSlash(filepath.Join(root, "nested/deep/file.txt"))
	if e.Path != want {
		t.Fatalf("unexpected path %q, want %q", e.Path, want)
	}
}

func TestAbsolutePathInsideRoot(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()
	absFile := filepath.ToSlash(filepath.Join(root, "abs.txt"))
	if _, err := svc.Write(ctx, absFile, []byte("via absolute")); err != nil {
		t.Fatalf("write absolute: %v", err)
	}
	c, err := svc.Read(ctx, absFile)
	if err != nil {
		t.Fatalf("read absolute: %v", err)
	}
	if c.Content != "via absolute" {
		t.Fatalf("content = %q", c.Content)
	}
	if c.Path != absFile {
		t.Fatalf("path = %q, want %q", c.Path, absFile)
	}
}

func TestSymlinkInsideWorkspaceIsReadable(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()
	if _, err := svc.Write(ctx, "target.txt", []byte("inside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "target.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	c, err := svc.Read(ctx, "link.txt")
	if err != nil {
		t.Fatalf("read via internal symlink: %v", err)
	}
	if !strings.Contains(c.Content, "inside") {
		t.Fatalf("unexpected content %q", c.Content)
	}
}
