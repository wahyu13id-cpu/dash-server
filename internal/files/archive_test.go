package files

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// buildZip assembles an archive in memory so tests can forge names and modes
// that the writer would never produce on its own.
func buildZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range entries {
		hdr := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			hdr.SetMode(entry.mode)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("CreateHeader(%q): %v", entry.name, err)
		}
		if _, err := w.Write([]byte(entry.content)); err != nil {
			t.Fatalf("write %q: %v", entry.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}

type zipEntry struct {
	name    string
	content string
	mode    os.FileMode
}

func writeZipFile(t *testing.T, root, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestZipStreamsDirectoryTree(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, "docs/sub", KindFolder, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Write(ctx, "docs/a.txt", []byte("aaa")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := svc.Write(ctx, "docs/sub/b.txt", []byte("bbb")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink("a.txt", filepath.Join(root, "docs", "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	var buf bytes.Buffer
	count, err := svc.Zip(ctx, "docs", &buf)
	if err != nil {
		t.Fatalf("Zip: %v", err)
	}
	if count != 4 {
		t.Fatalf("count = %d, want 4 (2 files, 1 dir, 1 link)", count)
	}

	reader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	names := map[string]string{}
	for _, f := range reader.File {
		if f.FileInfo().IsDir() {
			names[f.Name] = ""
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %q: %v", f.Name, err)
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %q: %v", f.Name, err)
		}
		names[f.Name] = string(content)
	}
	if names["docs/a.txt"] != "aaa" || names["docs/sub/b.txt"] != "bbb" {
		t.Fatalf("archive contents = %+v", names)
	}
	if _, ok := names["docs/sub/"]; !ok {
		t.Fatalf("directory entry missing: %+v", names)
	}
	if names["docs/link"] != "a.txt" {
		t.Fatalf("symlink stored as %q", names["docs/link"])
	}
}

func TestZipSingleFile(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Write(ctx, "one.txt", []byte("solo")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var buf bytes.Buffer
	count, err := svc.Zip(ctx, "one.txt", &buf)
	if err != nil {
		t.Fatalf("Zip: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d", count)
	}
	reader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if len(reader.File) != 1 || reader.File[0].Name != "one.txt" {
		t.Fatalf("entries = %+v", reader.File)
	}
}

func TestZipPathsPacksSeveralRoots(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Write(ctx, "one.txt", []byte("1")); err != nil {
		t.Fatalf("write one: %v", err)
	}
	if _, err := svc.Create(ctx, "dir", KindFolder, false); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	if _, err := svc.Write(ctx, "dir/two.txt", []byte("2")); err != nil {
		t.Fatalf("write two: %v", err)
	}
	// A second folder with the same base name must not collide in the archive.
	if _, err := svc.Create(ctx, "mirror/dir", KindFolder, false); err != nil {
		t.Fatalf("create mirror: %v", err)
	}
	if _, err := svc.Write(ctx, "mirror/dir/three.txt", []byte("3")); err != nil {
		t.Fatalf("write three: %v", err)
	}

	var buf bytes.Buffer
	count, err := svc.ZipPaths(ctx, []string{"one.txt", "dir", "mirror/dir"}, &buf)
	if err != nil {
		t.Fatalf("ZipPaths: %v", err)
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
	reader, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	names := map[string]bool{}
	for _, f := range reader.File {
		names[f.Name] = true
	}
	for _, want := range []string{"one.txt", "dir/two.txt", "dir (2)/three.txt"} {
		if !names[want] {
			t.Fatalf("archive %v missing %q", names, want)
		}
	}
}

func TestZipPathsRejectsUnknownPathBeforeWriting(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Write(ctx, "real.txt", []byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	var buf bytes.Buffer
	if _, err := svc.ZipPaths(ctx, []string{"real.txt", "ghost.txt"}, &buf); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("bytes written before the failure: %d", buf.Len())
	}
}

func TestUnzipRoundTrip(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, "src/nested", KindFolder, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Write(ctx, "src/nested/file.txt", []byte("payload")); err != nil {
		t.Fatalf("write: %v", err)
	}
	var buf bytes.Buffer
	if _, err := svc.Zip(ctx, "src", &buf); err != nil {
		t.Fatalf("Zip: %v", err)
	}
	writeZipFile(t, root, "bundle.zip", buf.Bytes())

	count, err := svc.Unzip(ctx, "bundle.zip", "out", false)
	if err != nil {
		t.Fatalf("Unzip: %v", err)
	}
	if count != 2 { // the nested directory and the file
		t.Fatalf("count = %d, want 2", count)
	}
	got, err := svc.Read(ctx, "out/src/nested/file.txt")
	if err != nil {
		t.Fatalf("Read extracted: %v", err)
	}
	if got.Content != "payload" {
		t.Fatalf("content = %q", got.Content)
	}
}

func TestUnzipDefaultDestinationSitsBesideTheArchive(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	writeZipFile(t, root, "thing.zip", buildZip(t, []zipEntry{{name: "hello.txt", content: "hi"}}))
	if _, err := svc.Unzip(ctx, "thing.zip", "", false); err != nil {
		t.Fatalf("Unzip: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "thing", "hello.txt")); err != nil {
		t.Fatalf("default destination missing: %v", err)
	}
}

func TestUnzipRefusesZipSlip(t *testing.T) {
	for _, name := range []string{"../escape.txt", "a/../../escape.txt", "/tmp/escape.txt"} {
		t.Run(name, func(t *testing.T) {
			svc, root := newTestService(t)
			ctx := context.Background()

			writeZipFile(t, root, "evil.zip", buildZip(t, []zipEntry{
				{name: "safe.txt", content: "ok"},
				{name: name, content: "pwned"},
			}))

			_, err := svc.Unzip(ctx, "evil.zip", "out", false)
			if !errors.Is(err, ErrArchive) {
				t.Fatalf("err = %v, want ErrArchive", err)
			}
			// Nothing was written: not the escaping file, not the safe one, and
			// not even the destination directory.
			if _, err := os.Stat(filepath.Join(root, "escape.txt")); !os.IsNotExist(err) {
				t.Fatalf("escaping file was written: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "out")); !os.IsNotExist(err) {
				t.Fatalf("destination was created despite the rejection: %v", err)
			}
		})
	}
}

func TestUnzipRefusesBackslashNames(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	writeZipFile(t, root, "win.zip", buildZip(t, []zipEntry{{name: `..\escape.txt`, content: "x"}}))
	if _, err := svc.Unzip(ctx, "win.zip", "out", false); !errors.Is(err, ErrArchive) {
		t.Fatalf("err = %v, want ErrArchive", err)
	}
}

func TestUnzipStopsOnExistingTargets(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	writeZipFile(t, root, "pack.zip", buildZip(t, []zipEntry{
		{name: "keep.txt", content: "from archive"},
		{name: "brand-new.txt", content: "new"},
	}))
	if _, err := svc.Create(ctx, "out", KindFolder, false); err != nil {
		t.Fatalf("create out: %v", err)
	}
	if _, err := svc.Write(ctx, "out/keep.txt", []byte("original")); err != nil {
		t.Fatalf("write keep: %v", err)
	}

	if _, err := svc.Unzip(ctx, "pack.zip", "out", false); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("err = %v, want ErrAlreadyExists", err)
	}
	// The conflict is detected before anything is written.
	if _, err := os.Stat(filepath.Join(root, "out", "brand-new.txt")); !os.IsNotExist(err) {
		t.Fatalf("partial extraction happened: %v", err)
	}

	if _, err := svc.Unzip(ctx, "pack.zip", "out", true); err != nil {
		t.Fatalf("Unzip overwrite: %v", err)
	}
	got, err := svc.Read(ctx, "out/keep.txt")
	if err != nil {
		t.Fatalf("Read keep: %v", err)
	}
	if got.Content != "from archive" {
		t.Fatalf("keep = %q", got.Content)
	}
}

func TestUnzipSkipsSymlinksAndSpecialModes(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	writeZipFile(t, root, "links.zip", buildZip(t, []zipEntry{
		{name: "evil-link", content: "/etc/passwd", mode: os.ModeSymlink | 0o777},
		{name: "setuid.bin", content: "x", mode: os.ModeSetuid | 0o755},
	}))

	count, err := svc.Unzip(ctx, "links.zip", "out", false)
	if err != nil {
		t.Fatalf("Unzip: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1 (symlink skipped)", count)
	}
	if _, err := os.Lstat(filepath.Join(root, "out", "evil-link")); !os.IsNotExist(err) {
		t.Fatalf("symlink entry was recreated: %v", err)
	}
	info, err := os.Lstat(filepath.Join(root, "out", "setuid.bin"))
	if err != nil {
		t.Fatalf("Lstat setuid: %v", err)
	}
	if info.Mode()&os.ModeSetuid != 0 {
		t.Fatal("setuid bit survived extraction")
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("perm = %o, want 755", info.Mode().Perm())
	}
}

func TestUnzipRejectsDirectoryArgument(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, "adir", KindFolder, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Unzip(ctx, "adir", "out", false); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("err = %v, want ErrInvalidName", err)
	}
}

func TestUnzipRejectsGarbage(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	writeZipFile(t, root, "junk.zip", []byte("this is not a zip archive at all"))
	if _, err := svc.Unzip(ctx, "junk.zip", "out", false); !errors.Is(err, ErrArchive) {
		t.Fatalf("err = %v, want ErrArchive", err)
	}
}

func TestUnzipMissingArchive(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.Unzip(context.Background(), "ghost.zip", "out", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
