package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"dashboard/internal/audit"
)

// This file holds the operations a file manager needs beyond create, read,
// write, rename and delete: copy, move, permission changes and a detailed stat.
// They follow the rules of the rest of the package: every path goes through the
// workspace sandbox, the workspace root itself is never modified, and every
// successful operation is audited.

// Copy duplicates a file or a directory inside the workspace.
//
// Directories are copied recursively; symlinks are recreated as links (never
// followed), and a directory can never be copied into its own subtree. With
// overwrite the destination is replaced, otherwise an existing one is refused.
func (s *Service) Copy(ctx context.Context, from, to string, overwrite bool) (*Entry, error) {
	fromAbs, err := s.ws.Resolve(from)
	if err != nil {
		return nil, err
	}
	toAbs, err := s.ws.Resolve(to)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(fromAbs) == filepath.Clean(s.ws.Root()) {
		return nil, ErrRootProtected
	}
	if err := validateName(filepath.Base(toAbs)); err != nil {
		return nil, err
	}
	if filepath.Clean(fromAbs) == filepath.Clean(toAbs) {
		return nil, ErrAlreadyExists
	}

	info, err := os.Lstat(fromAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// Copying a directory into itself would recurse forever.
	if info.IsDir() && within(fromAbs, toAbs) {
		return nil, fmt.Errorf("%w: destination is inside the source", ErrInvalidTarget)
	}

	if _, err := os.Lstat(toAbs); err == nil {
		if !overwrite {
			return nil, ErrAlreadyExists
		}
		if err := os.RemoveAll(toAbs); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(toAbs), 0o750); err != nil {
		return nil, err
	}
	if err := copyTree(fromAbs, toAbs, info); err != nil {
		return nil, err
	}

	destInfo, err := os.Lstat(toAbs)
	if err != nil {
		return nil, err
	}
	s.audit.Log(ctx, audit.Entry{
		Action: audit.ActionFileCopy, Target: fromAbs, Detail: "-> " + toAbs, Success: true,
	})
	entry := s.entryFor(toAbs, destInfo)
	return &entry, nil
}

// Move relocates a file or directory inside the workspace. It is a rename
// wherever the filesystem allows it, with a copy+delete fallback for a move
// across devices.
func (s *Service) Move(ctx context.Context, from, to string) (*Entry, error) {
	fromAbs, err := s.ws.Resolve(from)
	if err != nil {
		return nil, err
	}
	toAbs, err := s.ws.Resolve(to)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(fromAbs) == filepath.Clean(s.ws.Root()) {
		return nil, ErrRootProtected
	}
	if err := validateName(filepath.Base(toAbs)); err != nil {
		return nil, err
	}
	if filepath.Clean(fromAbs) == filepath.Clean(toAbs) {
		return nil, ErrAlreadyExists
	}
	info, err := os.Lstat(fromAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if info.IsDir() && within(fromAbs, toAbs) {
		return nil, fmt.Errorf("%w: destination is inside the source", ErrInvalidTarget)
	}
	if _, err := os.Lstat(toAbs); err == nil {
		return nil, ErrAlreadyExists
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(toAbs), 0o750); err != nil {
		return nil, err
	}

	if err := os.Rename(fromAbs, toAbs); err != nil {
		if !errors.Is(err, syscall.EXDEV) {
			return nil, err
		}
		// Different filesystems: copy the tree, then drop the original.
		if err := copyTree(fromAbs, toAbs, info); err != nil {
			return nil, err
		}
		if err := os.RemoveAll(fromAbs); err != nil {
			return nil, err
		}
	}

	destInfo, err := os.Lstat(toAbs)
	if err != nil {
		return nil, err
	}
	s.audit.Log(ctx, audit.Entry{
		Action: audit.ActionFileMove, Target: fromAbs, Detail: "-> " + toAbs, Success: true,
	})
	entry := s.entryFor(toAbs, destInfo)
	return &entry, nil
}

// Stat describes a path in more detail than a listing entry does.
type Stat struct {
	Entry
	Owner      string `json:"owner"`
	Group      string `json:"group"`
	UID        uint32 `json:"uid"`
	GID        uint32 `json:"gid"`
	Links      uint64 `json:"links"`
	Inode      uint64 `json:"inode"`
	LinkTarget string `json:"linkTarget,omitempty"`
}

// Stat reports the details of a single file or directory.
func (s *Service) Stat(ctx context.Context, relPath string) (*Stat, error) {
	abs, err := s.ws.Resolve(relPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	out := Stat{Entry: s.entryFor(abs, info)}
	if info.Mode()&os.ModeSymlink != 0 {
		if target, err := os.Readlink(abs); err == nil {
			out.LinkTarget = target
		}
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		out.UID = st.Uid
		out.GID = st.Gid
		out.Links = uint64(st.Nlink)
		out.Inode = uint64(st.Ino)
		out.Owner = ownerName(st.Uid)
		out.Group = groupName(st.Gid)
	}
	s.audit.Log(ctx, audit.Entry{Action: audit.ActionFileStat, Target: abs, Success: true})
	return &out, nil
}

// Chmod sets the permission bits of a file or directory.
//
// Setuid, setgid and sticky bits are refused outright: this dashboard runs as
// root, so `chmod u+s` on any binary would hand a shell to whoever can execute
// it, and that is not something a file browser should be able to do by accident.
func (s *Service) Chmod(ctx context.Context, relPath string, mode os.FileMode) (*Entry, error) {
	abs, err := s.ws.Resolve(relPath)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(abs) == filepath.Clean(s.ws.Root()) {
		return nil, ErrRootProtected
	}
	// Only the nine permission bits may be set. That rejects setuid, setgid and
	// sticky both as Go mode flags and as the raw 04000/02000/01000 bits that
	// reach us from a "chmod" string, and it also refuses nonsense such as a
	// directory bit on a chmod call.
	if mode&^os.ModePerm != 0 {
		return nil, fmt.Errorf("%w: only the permission bits may be changed", ErrUnsafeMode)
	}
	if _, err := os.Lstat(abs); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if err := os.Chmod(abs, mode.Perm()); err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	s.audit.Log(ctx, audit.Entry{
		Action: audit.ActionFileChmod, Target: abs,
		Detail: info.Mode().Perm().String(), Success: true,
	})
	entry := s.entryFor(abs, info)
	return &entry, nil
}

// --- helpers ---------------------------------------------------------------

// within reports whether child is root itself or lives underneath it.
func within(root, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(child))
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// copyTree copies a single path, recursing into directories. Modes are
// preserved; symlinks are recreated as links so a copy can never be tricked into
// reading through one.
func copyTree(src, dst string, info os.FileInfo) error {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)

	case info.IsDir():
		if err := os.MkdirAll(dst, info.Mode().Perm()|0o700); err != nil {
			return err
		}
		children, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, child := range children {
			childSrc := filepath.Join(src, child.Name())
			childInfo, err := os.Lstat(childSrc)
			if err != nil {
				return err
			}
			if err := copyTree(childSrc, filepath.Join(dst, child.Name()), childInfo); err != nil {
				return err
			}
		}
		return os.Chmod(dst, info.Mode().Perm())

	case info.Mode().IsRegular():
		return copyFile(src, dst, info.Mode().Perm())

	default:
		// Sockets, FIFOs and device nodes have no meaningful content to copy.
		return fmt.Errorf("%w: %s", ErrUnsupported, info.Mode().Type())
	}
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}

// ownerName resolves a uid to a user name, falling back to the id itself when
// the account is unknown to the system.
func ownerName(uid uint32) string {
	id := strconv.FormatUint(uint64(uid), 10)
	if u, err := user.LookupId(id); err == nil {
		return u.Username
	}
	return id
}

// groupName resolves a gid to a group name, with the same fallback.
func groupName(gid uint32) string {
	id := strconv.FormatUint(uint64(gid), 10)
	if g, err := user.LookupGroupId(id); err == nil {
		return g.Name
	}
	return id
}
