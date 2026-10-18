package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFile(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Write(ctx, "src.txt", []byte("isi asli")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	entry, err := svc.Copy(ctx, "src.txt", "salinan.txt", false)
	if err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if entry.Name != "salinan.txt" || entry.IsDir {
		t.Fatalf("entry = %+v", entry)
	}
	copied, err := svc.Read(ctx, "salinan.txt")
	if err != nil {
		t.Fatalf("Read copy: %v", err)
	}
	if copied.Content != "isi asli" {
		t.Fatalf("content = %q", copied.Content)
	}
	// The original is untouched.
	if _, err := os.Stat(filepath.Join(root, "src.txt")); err != nil {
		t.Fatalf("source missing after copy: %v", err)
	}
}

func TestCopyRefusesExistingUnlessOverwrite(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Write(ctx, "a.txt", []byte("a")); err != nil {
		t.Fatalf("Write a: %v", err)
	}
	if _, err := svc.Write(ctx, "b.txt", []byte("b")); err != nil {
		t.Fatalf("Write b: %v", err)
	}
	if _, err := svc.Copy(ctx, "a.txt", "b.txt", false); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("err = %v, want ErrAlreadyExists", err)
	}
	if _, err := svc.Copy(ctx, "a.txt", "b.txt", true); err != nil {
		t.Fatalf("Copy overwrite: %v", err)
	}
	got, err := svc.Read(ctx, "b.txt")
	if err != nil {
		t.Fatalf("Read b: %v", err)
	}
	if got.Content != "a" {
		t.Fatalf("b content = %q, want a", got.Content)
	}
}

func TestCopyDirectoryRecursively(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, "tree/inner", KindFolder, false); err != nil {
		t.Fatalf("create tree: %v", err)
	}
	if _, err := svc.Write(ctx, "tree/inner/deep.txt", []byte("deep")); err != nil {
		t.Fatalf("write deep: %v", err)
	}
	if _, err := svc.Copy(ctx, "tree", "tree-copy", false); err != nil {
		t.Fatalf("Copy tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tree-copy", "inner", "deep.txt")); err != nil {
		t.Fatalf("copied tree incomplete: %v", err)
	}
}

func TestCopyDirectoryIntoItselfIsRefused(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, "outer/inner", KindFolder, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Copy(ctx, "outer", "outer/inner/copy", false); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("err = %v, want ErrInvalidTarget", err)
	}
}

func TestCopyKeepsSymlinksAsLinks(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Write(ctx, "target.txt", []byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := os.Symlink("target.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := svc.Copy(ctx, "link.txt", "link-copy.txt", false); err != nil {
		t.Fatalf("Copy link: %v", err)
	}
	info, err := os.Lstat(filepath.Join(root, "link-copy.txt"))
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("copied symlink is a regular file")
	}
	if dest, _ := os.Readlink(filepath.Join(root, "link-copy.txt")); dest != "target.txt" {
		t.Fatalf("link target = %q", dest)
	}
}

func TestCopyRejectsPathOutsideWorkspace(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Write(ctx, "a.txt", []byte("a")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := svc.Copy(ctx, "a.txt", "../escape.txt", false); err == nil {
		t.Fatal("copy outside the workspace succeeded")
	}
}

func TestMoveRelocatesAndRefusesConflict(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Write(ctx, "old.txt", []byte("payload")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := svc.Create(ctx, "dest", KindFolder, false); err != nil {
		t.Fatalf("create dest: %v", err)
	}
	entry, err := svc.Move(ctx, "old.txt", "dest/new.txt")
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if entry.Path != filepath.ToSlash(filepath.Join(root, "dest", "new.txt")) {
		t.Fatalf("entry path = %q", entry.Path)
	}
	if _, err := os.Stat(filepath.Join(root, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}

	if _, err := svc.Write(ctx, "other.txt", []byte("o")); err != nil {
		t.Fatalf("Write other: %v", err)
	}
	if _, err := svc.Move(ctx, "other.txt", "dest/new.txt"); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("err = %v, want ErrAlreadyExists", err)
	}
}

func TestMoveDirectoryIntoItselfIsRefused(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, "parent/child", KindFolder, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.Move(ctx, "parent", "parent/child/moved"); !errors.Is(err, ErrInvalidTarget) {
		t.Fatalf("err = %v, want ErrInvalidTarget", err)
	}
}

func TestChmodAppliesAndRefusesPrivilegedBits(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Write(ctx, "script.sh", []byte("#!/bin/sh\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	entry, err := svc.Chmod(ctx, "script.sh", 0o755)
	if err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if entry.Mode != "-rwxr-xr-x" {
		t.Fatalf("mode = %q", entry.Mode)
	}
	info, err := os.Lstat(filepath.Join(root, "script.sh"))
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("perm = %o", info.Mode().Perm())
	}

	for _, mode := range []os.FileMode{0o4755, 0o2755, 0o1755, 0o7777} {
		if _, err := svc.Chmod(ctx, "script.sh", mode); !errors.Is(err, ErrUnsafeMode) {
			t.Fatalf("Chmod(%o) err = %v, want ErrUnsafeMode", mode, err)
		}
	}
	// The refused calls changed nothing.
	info, _ = os.Lstat(filepath.Join(root, "script.sh"))
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("perm after refusals = %o", info.Mode().Perm())
	}
}

func TestChmodProtectsWorkspaceRoot(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.Chmod(context.Background(), "", 0o700); !errors.Is(err, ErrRootProtected) {
		t.Fatalf("err = %v, want ErrRootProtected", err)
	}
}

func TestStatReportsOwnerAndLinks(t *testing.T) {
	svc, root := newTestService(t)
	ctx := context.Background()

	if _, err := svc.Write(ctx, "info.txt", []byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := os.Symlink("info.txt", filepath.Join(root, "info-link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	stat, err := svc.Stat(ctx, "info.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Size != 5 {
		t.Fatalf("size = %d", stat.Size)
	}
	if stat.Links == 0 {
		t.Fatal("links = 0")
	}
	if stat.Owner == "" || stat.Group == "" {
		t.Fatalf("owner/group empty: %+v", stat)
	}

	linkStat, err := svc.Stat(ctx, "info-link")
	if err != nil {
		t.Fatalf("Stat link: %v", err)
	}
	if !linkStat.IsLink || linkStat.LinkTarget != "info.txt" {
		t.Fatalf("link stat = %+v", linkStat)
	}
}

func TestStatMissingPath(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.Stat(context.Background(), "nope.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
