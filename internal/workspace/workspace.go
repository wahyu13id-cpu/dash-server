// Package workspace defines the filesystem boundary for the file manager.
//
// The configured root is the top-most directory the file manager may touch. In
// the default "full access" deployment the root is "/" (the whole filesystem);
// with a narrower root the same code enforces a real sandbox. Both absolute
// paths (e.g. "/var/log") and root-relative paths (e.g. "var/log") are accepted,
// and every path is validated to stay inside the root — including escapes via
// ".." and symlinks.
package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Common errors returned by the workspace.
var (
	ErrEscape      = errors.New("path escapes the workspace root")
	ErrEmptyPath   = errors.New("path must not be empty")
	ErrNotAbsolute = errors.New("workspace root must be an absolute path")
)

// Workspace represents the accessible directory tree.
type Workspace struct {
	// root is the cleaned, absolute, symlink-resolved root directory.
	root string
}

// New validates root and returns a Workspace bound to it.
func New(root string) (*Workspace, error) {
	if strings.TrimSpace(root) == "" {
		return nil, ErrNotAbsolute
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("create root: %w", err)
	}
	// Resolve symlinks once so comparisons are against the real root.
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve root symlinks: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, fmt.Errorf("stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace root %q is not a directory", real)
	}
	return &Workspace{root: real}, nil
}

// Root returns the resolved absolute root directory.
func (w *Workspace) Root() string { return w.root }

// RelFromAbs converts an absolute path inside the workspace to a
// slash-separated relative path.
func (w *Workspace) RelFromAbs(abs string) (string, error) {
	rel, err := filepath.Rel(w.root, abs)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return "", nil
	}
	return filepath.ToSlash(rel), nil
}

// Resolve maps a user-supplied path to an absolute filesystem path inside the
// workspace root. It accepts:
//
//   - the workspace root itself (empty, ".", "/");
//   - absolute paths (e.g. "/etc/hosts"), when they are inside the root;
//   - paths relative to the root (e.g. "sub/file.txt").
//
// It rejects paths whose real location leaves the root through ".." or a
// symlink. The returned path may not exist yet (useful for create/write
// operations); validation is performed against the nearest existing ancestor.
func (w *Workspace) Resolve(p string) (string, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("%w: path contains NUL byte", ErrEscape)
	}

	var abs string
	switch {
	case p == "" || p == "/" || p == "." || p == "./":
		abs = w.root
	case strings.HasPrefix(p, "/"):
		abs = filepath.Clean(filepath.FromSlash(p))
	default:
		abs = filepath.Join(w.root, filepath.FromSlash(p))
	}
	if !isWithin(w.root, abs) {
		return "", fmt.Errorf("%w: %q", ErrEscape, p)
	}

	// Walk up to the nearest existing ancestor and resolve its symlinks to
	// make sure the real location is still inside the root.
	ancestor := abs
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
		ancestor = parent
	}
	realAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			realAncestor = ancestor
		} else {
			return "", err
		}
	}
	if !isWithin(w.root, realAncestor) {
		return "", fmt.Errorf("%w: %q resolves outside root", ErrEscape, p)
	}

	// If the target itself exists, its resolved path must also be inside.
	if _, err := os.Lstat(abs); err == nil {
		realTarget, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", err
		}
		if !isWithin(w.root, realTarget) {
			return "", fmt.Errorf("%w: %q is a symlink outside root", ErrEscape, p)
		}
	}
	return abs, nil
}

// isWithin reports whether child is root itself or nested inside root.
func isWithin(root, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(child))
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return !filepath.IsAbs(rel)
}
