package files

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenReadsFile(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Write(ctx, "catatan.txt", []byte("isi berkas")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	f, info, err := svc.Open(ctx, "catatan.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	if info.Name() != "catatan.txt" || info.IsDir() {
		t.Fatalf("unexpected info: %+v", info)
	}
	if filepath.Dir(info.Name()) != "." {
		t.Fatalf("name must be a base name, got %q", info.Name())
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "isi berkas" {
		t.Fatalf("content = %q", data)
	}
	if _, err := os.Stat(filepath.Join(root, "catatan.txt")); err != nil {
		t.Fatalf("stat: %v", err)
	}
}

func TestOpenFollowsSymlinkInsideWorkspace(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Write(ctx, "target.txt", []byte("lewat link")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "target.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	f, _, err := svc.Open(ctx, "link.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "lewat link" {
		t.Fatalf("content = %q", data)
	}
}

func TestOpenRejectsDirectoriesAndMissingPaths(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, "adir", KindFolder, false); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if _, _, err := svc.Open(ctx, "adir"); !errors.Is(err, ErrIsDirectory) {
		t.Fatalf("dir: err = %v, want ErrIsDirectory", err)
	}
	if _, _, err := svc.Open(ctx, "tidak-ada.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: err = %v, want ErrNotFound", err)
	}
	if _, _, err := svc.Open(ctx, "../../etc/passwd"); err == nil {
		t.Fatal("escaping path must be refused")
	}
}

// A download must not be limited by the editor's read cap: big binary files are
// exactly what the endpoint is for.
func TestOpenIgnoresReadLimit(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	// The test service caps reads at 1024 bytes.
	big := make([]byte, 4096)
	if err := os.WriteFile(filepath.Join(root, "big.bin"), big, 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Read(ctx, "big.bin"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Read: err = %v, want ErrTooLarge", err)
	}
	f, info, err := svc.Open(ctx, "big.bin")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	if info.Size() != 4096 {
		t.Fatalf("size = %d", info.Size())
	}
	n, err := io.Copy(io.Discard, f)
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if n != 4096 {
		t.Fatalf("copied %d bytes", n)
	}
}
