// Package files implements the file-manager business logic. Every path is
// resolved through the workspace sandbox and every operation is bounded by
// size and type limits configured at startup.
package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"dashboard/internal/audit"
	"dashboard/internal/workspace"
)

// Errors surfaced to the API layer.
var (
	ErrNotFound      = errors.New("path not found")
	ErrIsDirectory   = errors.New("path is a directory")
	ErrNotDirectory  = errors.New("path is not a directory")
	ErrTooLarge      = errors.New("file is too large")
	ErrAlreadyExists = errors.New("path already exists")
	ErrReadOnly      = errors.New("target is not writable")
	ErrBinary        = errors.New("file appears to be binary")
	ErrInvalidName   = errors.New("invalid name")
	ErrRootProtected = errors.New("the workspace root cannot be modified")
	// ErrInvalidTarget covers operations that make no sense structurally, such
	// as copying a directory into its own subtree.
	ErrInvalidTarget = errors.New("invalid target path")
	// ErrUnsafeMode covers permission changes the dashboard refuses to make.
	ErrUnsafeMode = errors.New("permission mode not allowed")
	// ErrUnsupported covers file types with no useful copy/archive meaning,
	// such as sockets and FIFOs.
	ErrUnsupported = errors.New("unsupported file type")
	// ErrArchive covers malformed or oversized zip archives.
	ErrArchive = errors.New("invalid archive")
)

// Entry describes one file or directory in a listing.
type Entry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	IsDir    bool   `json:"isDir"`
	IsLink   bool   `json:"isLink"`
	Size     int64  `json:"size"`
	Mode     string `json:"mode"`
	Modified string `json:"modified"`
	Editable bool   `json:"editable"`
}

// Listing is the response for a directory listing.
type Listing struct {
	Path       string  `json:"path"`
	Entries    []Entry `json:"entries"`
	Truncated  bool    `json:"truncated"`
	TotalCount int     `json:"totalCount"`
}

// Content is the response for reading a text file.
type Content struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Size      int64  `json:"size"`
	Modified  string `json:"modified"`
	Truncated bool   `json:"truncated"`
	Binary    bool   `json:"binary"`
	ReadOnly  bool   `json:"readOnly"`
	Language  string `json:"language"`
}

// Service provides sandboxed file operations.
type Service struct {
	ws         *workspace.Workspace
	audit      *audit.Logger
	maxRead    int64
	maxWrite   int64
	maxUpload  int64
	maxEntries int
}

// Options configures the file service.
type Options struct {
	MaxReadSize    int64
	MaxWriteSize   int64
	MaxUploadSize  int64
	MaxListEntries int
}

// NewService creates a file service.
func NewService(ws *workspace.Workspace, auditLog *audit.Logger, opts Options) *Service {
	if opts.MaxReadSize <= 0 {
		opts.MaxReadSize = 2 << 20
	}
	if opts.MaxWriteSize <= 0 {
		opts.MaxWriteSize = 5 << 20
	}
	if opts.MaxUploadSize <= 0 {
		opts.MaxUploadSize = 512 << 20
	}
	if opts.MaxListEntries <= 0 {
		opts.MaxListEntries = 5000
	}
	return &Service{
		ws:         ws,
		audit:      auditLog,
		maxRead:    opts.MaxReadSize,
		maxWrite:   opts.MaxWriteSize,
		maxUpload:  opts.MaxUploadSize,
		maxEntries: opts.MaxListEntries,
	}
}

// Workspace exposes the underlying sandbox (used by the terminal package).
func (s *Service) Workspace() *workspace.Workspace { return s.ws }

// Limits returns the configured size limits for documentation/UI hints.
func (s *Service) Limits() (maxRead, maxWrite, maxUpload int64) {
	return s.maxRead, s.maxWrite, s.maxUpload
}

// RootEntryCount returns the number of top-level entries in the workspace
// without writing an audit entry (used for dashboard summaries).
func (s *Service) RootEntryCount(ctx context.Context) (int, error) {
	f, err := os.Open(s.ws.Root())
	if err != nil {
		return 0, err
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	return len(names), nil
}

// List returns the entries of a directory.
func (s *Service) List(ctx context.Context, relPath string) (*Listing, error) {
	abs, err := s.ws.Resolve(relPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrNotDirectory
	}

	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	names, err := f.Readdirnames(-1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})

	listing := &Listing{Path: abs, Entries: make([]Entry, 0, len(names)), TotalCount: len(names)}
	for _, name := range names {
		if len(listing.Entries) >= s.maxEntries {
			listing.Truncated = true
			break
		}
		childAbs := filepath.Join(abs, name)
		li, err := os.Lstat(childAbs)
		if err != nil {
			continue // skip entries that vanished between readdir and lstat
		}
		childRel := filepath.ToSlash(childAbs)
		entry := Entry{
			Name:     name,
			Path:     childRel,
			IsDir:    li.IsDir(),
			IsLink:   li.Mode()&os.ModeSymlink != 0,
			Size:     li.Size(),
			Mode:     li.Mode().Perm().String(),
			Modified: li.ModTime().UTC().Format(time.RFC3339),
		}
		if entry.IsLink {
			// Report the link target's type/size when it is reachable inside
			// the sandbox; otherwise keep the link metadata only.
			if target, err := s.ws.Resolve(childRel); err == nil {
				if ti, err := os.Stat(target); err == nil {
					entry.IsDir = ti.IsDir()
					entry.Size = ti.Size()
					entry.Editable = isEditable(name, ti.Size(), s.maxRead)
				}
			}
		} else {
			entry.Editable = !entry.IsDir && isEditable(name, entry.Size, s.maxRead)
		}
		listing.Entries = append(listing.Entries, entry)
	}

	s.audit.Log(ctx, audit.Entry{Action: audit.ActionFileList, Target: abs, Success: true})
	return listing, nil
}

// Open returns a readable handle to a file so the HTTP layer can stream it to
// the client. It follows the same resolution rules as Read (symlinks are
// followed inside the sandbox) and refuses directories. Nothing is loaded into
// memory: the caller streams straight from the file descriptor, which is also
// what makes multi-gigabyte downloads possible.
func (s *Service) Open(ctx context.Context, relPath string) (*os.File, os.FileInfo, error) {
	abs, err := s.ws.Resolve(relPath)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	if info.IsDir() {
		return nil, nil, ErrIsDirectory
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, nil, err
	}
	s.audit.Log(ctx, audit.Entry{
		Action: audit.ActionFileDownload, Target: abs,
		Detail: fmt.Sprintf("%d bytes", info.Size()), Success: true,
	})
	return f, info, nil
}

// Read returns the content of a text file, enforcing the read size limit.
func (s *Service) Read(ctx context.Context, relPath string) (*Content, error) {
	abs, err := s.ws.Resolve(relPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, ErrIsDirectory
	}
	if info.Size() > s.maxRead {
		return nil, fmt.Errorf("%w: %d bytes exceeds the %d byte read limit", ErrTooLarge, info.Size(), s.maxRead)
	}

	data, err := readFileLimited(abs, s.maxRead)
	if err != nil {
		return nil, err
	}

	c := &Content{
		Path:     abs,
		Size:     int64(len(data)),
		Modified: info.ModTime().UTC().Format(time.RFC3339),
		Language: languageOf(abs),
	}
	if isBinary(data) {
		c.Binary = true
		c.Content = ""
	} else {
		c.Content = string(data)
	}
	c.ReadOnly = info.Mode().Perm()&0o200 == 0

	s.audit.Log(ctx, audit.Entry{Action: audit.ActionFileRead, Target: abs, Success: true})
	return c, nil
}

// Write atomically replaces the content of a file (or creates it).
func (s *Service) Write(ctx context.Context, relPath string, content []byte) (*Content, error) {
	if int64(len(content)) > s.maxWrite {
		return nil, fmt.Errorf("%w: %d bytes exceeds the %d byte write limit", ErrTooLarge, len(content), s.maxWrite)
	}
	abs, err := s.ws.Resolve(relPath)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(abs) == filepath.Clean(s.ws.Root()) {
		return nil, ErrRootProtected
	}
	if err := validateName(filepath.Base(abs)); err != nil {
		return nil, err
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return nil, ErrIsDirectory
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return nil, err
	}
	if err := atomicWrite(abs, content, 0o640); err != nil {
		return nil, err
	}

	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	s.audit.Log(ctx, audit.Entry{
		Action: audit.ActionFileWrite, Target: abs,
		Detail: fmt.Sprintf("%d bytes", len(content)), Success: true,
	})
	return &Content{
		Path:     abs,
		Content:  string(content),
		Size:     info.Size(),
		Modified: info.ModTime().UTC().Format(time.RFC3339),
		Language: languageOf(abs),
	}, nil
}

// Store streams an upload into a new file named name inside dir, which must
// already exist. The bytes go to a temporary file in the destination directory
// and are renamed into place only after the stream has been written and flushed,
// so an interrupted, oversized or rejected upload never leaves a partial file in
// the listing.
//
// The name has to be a plain file name (no separators), which keeps the write
// inside the sandboxed directory, and an existing target is refused with
// ErrAlreadyExists unless overwrite is set. The body is capped at the configured
// upload limit while streaming, so an enormous request is stopped early instead
// of filling the disk.
func (s *Service) Store(ctx context.Context, dir, name string, r io.Reader, overwrite bool) (*Entry, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	dirAbs, err := s.ws.Resolve(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dirAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrNotDirectory
	}
	abs := filepath.Join(dirAbs, name)

	// Lstat, and therefore no symlink following: uploading through a link must
	// not write to whatever the link points at.
	if existing, err := os.Lstat(abs); err == nil {
		if existing.IsDir() || !overwrite {
			return nil, ErrAlreadyExists
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	tmp, err := os.CreateTemp(dirAbs, ".dash-upload-*")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	written, err := io.Copy(tmp, io.LimitReader(r, s.maxUpload+1))
	if err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if written > s.maxUpload {
		_ = tmp.Close()
		return nil, fmt.Errorf("%w: %d bytes exceeds the %d byte upload limit", ErrTooLarge, written, s.maxUpload)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	// CreateTemp makes the file 0600; uploaded files follow the same 0640
	// convention as every other file the dashboard writes.
	if err := os.Chmod(tmpName, 0o640); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, abs); err != nil {
		return nil, err
	}
	committed = true

	final, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	detail := fmt.Sprintf("%d bytes", written)
	if overwrite {
		detail += " (overwrite)"
	}
	s.audit.Log(ctx, audit.Entry{Action: audit.ActionFileUpload, Target: abs, Detail: detail, Success: true})

	entry := s.entryFor(abs, final)
	return &entry, nil
}

// CreateKind distinguishes file and folder creation requests.
type CreateKind string

// Supported creation kinds.
const (
	KindFile   CreateKind = "file"
	KindFolder CreateKind = "folder"
)

// Create makes a new empty file or a new directory. If the path already
// exists it returns ErrAlreadyExists unless overwrite is true (files only).
func (s *Service) Create(ctx context.Context, relPath string, kind CreateKind, overwrite bool) (*Entry, error) {
	abs, err := s.ws.Resolve(relPath)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(abs) == filepath.Clean(s.ws.Root()) {
		return nil, ErrRootProtected
	}
	if err := validateName(filepath.Base(abs)); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return nil, err
	}

	if _, err := os.Lstat(abs); err == nil {
		if !overwrite || kind != KindFile {
			return nil, ErrAlreadyExists
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	switch kind {
	case KindFolder:
		if err := os.Mkdir(abs, 0o750); err != nil {
			return nil, err
		}
		s.audit.Log(ctx, audit.Entry{Action: audit.ActionFileMkdir, Target: abs, Success: true})
	case KindFile:
		if err := atomicWrite(abs, nil, 0o640); err != nil {
			return nil, err
		}
		s.audit.Log(ctx, audit.Entry{Action: audit.ActionFileCreate, Target: abs, Success: true})
	default:
		return nil, fmt.Errorf("%w: unknown kind %q", ErrInvalidName, kind)
	}

	info, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	entry := s.entryFor(abs, info)
	return &entry, nil
}

// Rename moves or renames a file or directory within the workspace.
func (s *Service) Rename(ctx context.Context, from, to string) (*Entry, error) {
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
	if _, err := os.Lstat(fromAbs); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if _, err := os.Lstat(toAbs); err == nil {
		return nil, ErrAlreadyExists
	}
	if err := os.MkdirAll(filepath.Dir(toAbs), 0o750); err != nil {
		return nil, err
	}
	if err := os.Rename(fromAbs, toAbs); err != nil {
		return nil, err
	}
	info, err := os.Lstat(toAbs)
	if err != nil {
		return nil, err
	}
	s.audit.Log(ctx, audit.Entry{
		Action: audit.ActionFileRename, Target: fromAbs, Detail: "-> " + toAbs, Success: true,
	})
	entry := s.entryFor(toAbs, info)
	return &entry, nil
}

// Delete removes a file or directory (recursively for directories).
func (s *Service) Delete(ctx context.Context, relPath string) error {
	if strings.TrimSpace(relPath) == "" {
		return ErrRootProtected
	}
	abs, err := s.ws.Resolve(relPath)
	if err != nil {
		return err
	}
	if filepath.Clean(abs) == filepath.Clean(s.ws.Root()) {
		return ErrRootProtected
	}
	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return err
	}
	if info.IsDir() {
		// Remove symlinked directories as links, not as trees, to avoid
		// accidentally deleting content outside the sandbox.
		if err := os.RemoveAll(abs); err != nil {
			return err
		}
	} else if err := os.Remove(abs); err != nil {
		return err
	}
	s.audit.Log(ctx, audit.Entry{Action: audit.ActionFileDelete, Target: abs, Success: true})
	return nil
}

// --- helpers ---------------------------------------------------------------

// entryFor describes a path the way List does, so a freshly created, renamed or
// uploaded file is reported identically wherever it appears in the UI.
func (s *Service) entryFor(abs string, info os.FileInfo) Entry {
	return Entry{
		Name:     filepath.Base(abs),
		Path:     filepath.ToSlash(abs),
		IsDir:    info.IsDir(),
		IsLink:   info.Mode()&os.ModeSymlink != 0,
		Size:     info.Size(),
		Mode:     info.Mode().Perm().String(),
		Modified: info.ModTime().UTC().Format(time.RFC3339),
		Editable: !info.IsDir() && isEditable(filepath.Base(abs), info.Size(), s.maxRead),
	}
}

func atomicWrite(abs string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(abs)
	tmp, err := os.CreateTemp(dir, ".dash-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, abs)
}

func readFileLimited(abs string, limit int64) ([]byte, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit+1))
}

// isBinary uses a simple heuristic: NUL bytes or invalid UTF-8 in the sample.
func isBinary(data []byte) bool {
	sample := data
	if len(sample) > 8000 {
		sample = sample[:8000]
	}
	if len(sample) == 0 {
		return false
	}
	for _, b := range sample {
		if b == 0 {
			return true
		}
	}
	return !utf8.Valid(sample)
}

func isEditable(name string, size, maxRead int64) bool {
	if size > maxRead {
		return false
	}
	return editableExtensions[strings.ToLower(path.Ext(name))]
}

// validateName rejects names that would be unsafe or ambiguous.
func validateName(name string) error {
	switch name {
	case "", ".", "..", "/":
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	if strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("%w: %q contains a path separator", ErrInvalidName, name)
	}
	if len(name) > 255 {
		return fmt.Errorf("%w: name too long", ErrInvalidName)
	}
	return nil
}

var editableExtensions = map[string]bool{
	"": true, ".txt": true, ".md": true, ".markdown": true, ".json": true,
	".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".conf": true,
	".cfg": true, ".env": true, ".go": true, ".ts": true, ".tsx": true,
	".js": true, ".jsx": true, ".css": true, ".scss": true, ".html": true,
	".htm": true, ".xml": true, ".csv": true, ".tsv": true, ".sh": true,
	".bash": true, ".py": true, ".rb": true, ".rs": true, ".java": true,
	".c": true, ".h": true, ".cpp": true, ".hpp": true, ".sql": true,
	".log": true, ".svg": true, ".gitignore": true, ".dockerfile": true,
	".makefile": true, ".service": true, ".mod": true, ".sum": true,
}

var languageHints = map[string]string{
	".go": "go", ".ts": "typescript", ".tsx": "typescript", ".js": "javascript",
	".jsx": "javascript", ".json": "json", ".md": "markdown", ".css": "css",
	".html": "html", ".xml": "xml", ".yaml": "yaml", ".yml": "yaml",
	".sh": "shell", ".bash": "shell", ".py": "python", ".sql": "sql",
	".rs": "rust", ".java": "java", ".c": "c", ".cpp": "cpp",
}

func languageOf(relPath string) string {
	if l, ok := languageHints[strings.ToLower(path.Ext(relPath))]; ok {
		return l
	}
	return "plaintext"
}
