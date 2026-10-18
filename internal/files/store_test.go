package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// leftoverTempFiles reports the staging files an upload must never leave behind.
func leftoverTempFiles(t *testing.T, dir string) []string {
	t.Helper()
	names, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var leftovers []string
	for _, n := range names {
		if strings.HasPrefix(n.Name(), ".dash-upload-") {
			leftovers = append(leftovers, n.Name())
		}
	}
	return leftovers
}

func TestStoreCreatesFile(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	entry, err := svc.Store(ctx, "/", "upload.txt", strings.NewReader("halo dunia"), false)
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if entry.Name != "upload.txt" || entry.IsDir {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.Size != int64(len("halo dunia")) {
		t.Fatalf("size = %d", entry.Size)
	}
	if !entry.Editable {
		t.Fatal("text upload should be editable")
	}

	abs := filepath.Join(root, "upload.txt")
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if string(got) != "halo dunia" {
		t.Fatalf("content = %q", got)
	}
	info, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, want 0640", info.Mode().Perm())
	}
	if leftovers := leftoverTempFiles(t, root); len(leftovers) != 0 {
		t.Fatalf("staging files left behind: %v", leftovers)
	}

	// The listing must show it exactly like Store reported it.
	listing, err := svc.List(ctx, "/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, e := range listing.Entries {
		if e.Name == "upload.txt" {
			found = true
			if e.Size != entry.Size || e.Editable != entry.Editable {
				t.Fatalf("listing entry differs: %+v vs %+v", e, entry)
			}
		}
	}
	if !found {
		t.Fatal("uploaded file missing from listing")
	}
}

func TestStoreIntoSubdirectory(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, "sub", KindFolder, false); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if _, err := svc.Store(ctx, "sub", "data.bin", strings.NewReader("abc"), false); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "data.bin")); err != nil {
		t.Fatalf("stat uploaded file: %v", err)
	}
}

func TestStoreRefusesExistingWithoutOverwrite(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Write(ctx, "keep.txt", []byte("original")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_, err := svc.Store(ctx, "/", "keep.txt", strings.NewReader("replacement"), false)
	if !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("err = %v, want ErrAlreadyExists", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "keep.txt"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("existing file was modified: %q", got)
	}

	if _, err := svc.Store(ctx, "/", "keep.txt", strings.NewReader("replacement"), true); err != nil {
		t.Fatalf("Store with overwrite: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(root, "keep.txt"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "replacement" {
		t.Fatalf("content = %q, want replacement", got)
	}
	if leftovers := leftoverTempFiles(t, root); len(leftovers) != 0 {
		t.Fatalf("staging files left behind: %v", leftovers)
	}
}

func TestStoreRefusesOversizedBody(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	// 4096 byte limit in the test service.
	big := strings.Repeat("x", 4097)
	_, err := svc.Store(ctx, "/", "big.bin", strings.NewReader(big), false)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if _, err := os.Stat(filepath.Join(root, "big.bin")); !os.IsNotExist(err) {
		t.Fatalf("oversized upload must not be stored (stat err = %v)", err)
	}
	if leftovers := leftoverTempFiles(t, root); len(leftovers) != 0 {
		t.Fatalf("staging files left behind: %v", leftovers)
	}

	// Exactly at the limit is accepted.
	if _, err := svc.Store(ctx, "/", "edge.bin", strings.NewReader(strings.Repeat("y", 4096)), false); err != nil {
		t.Fatalf("Store at limit: %v", err)
	}
}

func TestStoreRejectsBadNames(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	bad := []string{"", "../escape.txt", "a/b.txt", "..", ".", "sub/../x.txt", "a\\b.txt"}
	for _, name := range bad {
		if _, err := svc.Store(ctx, "/", name, strings.NewReader("data"), true); err == nil {
			t.Fatalf("Store(%q) succeeded, want error", name)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); err == nil {
			t.Fatalf("Store(%q) escaped the workspace", name)
		}
	}
}

func TestStoreTargetChecks(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	// A directory cannot be replaced by a file, even with overwrite.
	if _, err := svc.Create(ctx, "adir", KindFolder, false); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if _, err := svc.Store(ctx, "/", "adir", strings.NewReader("x"), true); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("dir target: err = %v, want ErrAlreadyExists", err)
	}

	// The destination directory must exist and be a directory.
	if _, err := svc.Store(ctx, "missing", "x.txt", strings.NewReader("x"), false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing dir: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Write(ctx, "afile.txt", []byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := svc.Store(ctx, "afile.txt", "x.txt", strings.NewReader("x"), false); !errors.Is(err, ErrNotDirectory) {
		t.Fatalf("file as dir: err = %v, want ErrNotDirectory", err)
	}
}

// An upload must replace the symlink itself, never write through it.
func TestStoreDoesNotFollowSymlink(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := svc.Store(ctx, "/", "link.txt", strings.NewReader("replaced"), true); err != nil {
		t.Fatalf("Store: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("symlink survived the upload")
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if string(got) != "secret" {
		t.Fatalf("outside file was modified: %q", got)
	}
}

func TestStoreBinaryUploadNotEditable(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	entry, err := svc.Store(ctx, "/", "image.png", strings.NewReader("not really a png"), false)
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if entry.Editable {
		t.Fatal("binary upload should not be marked editable")
	}
}
