package files

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"dashboard/internal/audit"
)

// Limits for the archive endpoints. Both are deliberately generous for a
// single-user server while still refusing the pathological cases (an endless
// tree, or a zip bomb that expands until the disk is full).
const (
	maxZipEntries   = 20000
	maxUnzipEntries = 20000
	maxUnzipBytes   = 4 << 30
)

// Zip streams a file or a directory as a ZIP archive.
//
// Nothing is buffered: entries are written as the tree is walked, so a multi
// gigabyte directory streams straight to the client. Directories keep a trailing
// slash and their mode, symlinks are stored as links (never followed), and
// special files are skipped because there is nothing meaningful to store.
func (s *Service) Zip(ctx context.Context, relPath string, w io.Writer) (int, error) {
	return s.ZipPaths(ctx, []string{relPath}, w)
}

// ZipPaths packs several paths into one archive. Each one keeps its own base
// name, and a repeated base name is numbered rather than overwritten, so a
// multi-selection download contains everything exactly once.
func (s *Service) ZipPaths(ctx context.Context, paths []string, w io.Writer) (int, error) {
	if len(paths) == 0 {
		return 0, ErrNotFound
	}

	// Everything is resolved and checked before a single byte is written, so a
	// bad path is still an ordinary error response rather than the beginning of
	// an archive. (Mid-walk failures are different: by then the only honest
	// thing left is a truncated download.)
	type source struct {
		abs  string
		info os.FileInfo
		name string
	}
	used := make(map[string]int, len(paths))
	sources := make([]source, 0, len(paths))
	for _, relPath := range paths {
		abs, err := s.ws.Resolve(relPath)
		if err != nil {
			return 0, err
		}
		// Stat rather than Lstat: a symlinked directory is archived as the
		// directory it points at, which is what the file manager shows for it.
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				return 0, ErrNotFound
			}
			return 0, err
		}
		sources = append(sources, source{
			abs:  abs,
			info: info,
			name: uniqueEntryName(used, filepath.Base(abs)),
		})
	}

	archive := zip.NewWriter(w)
	total := 0
	for _, src := range sources {
		if err := writeZipTree(ctx, archive, src.abs, src.name, src.info, &total); err != nil {
			// Deliberately not closed: closing would append a valid end record
			// to a half-written archive.
			return total, err
		}
	}

	if err := archive.Close(); err != nil {
		return total, err
	}
	s.audit.Log(ctx, audit.Entry{
		Action: audit.ActionFileZip, Target: strings.Join(paths, ", "),
		Detail: fmt.Sprintf("%d entries", total), Success: true,
	})
	return total, nil
}

// uniqueEntryName numbers a repeated base name ("a.txt", "a (2).txt") so two
// picked roots with the same name cannot land on top of each other.
func uniqueEntryName(used map[string]int, name string) string {
	if name == "" || name == "." || name == "/" {
		name = "workspace"
	}
	seen := used[name]
	used[name] = seen + 1
	if seen == 0 {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	return fmt.Sprintf("%s (%d)%s", stem, seen+1, ext)
}

// writeZipTree stores one path under prefix, recursing into directories, and
// adds what it wrote to total.
func writeZipTree(
	ctx context.Context,
	archive *zip.Writer,
	abs, prefix string,
	info os.FileInfo,
	total *int,
) error {
	if !info.IsDir() {
		if err := writeZipEntry(archive, prefix, abs, info); err != nil {
			if errors.Is(err, ErrUnsupported) {
				return nil // nothing meaningful to store for this type
			}
			return err
		}
		*total++
		return nil
	}

	return filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == abs {
			// The archive holds the directory itself, implied by the prefix.
			return nil
		}
		if *total >= maxZipEntries {
			return fmt.Errorf("%w: archive would hold more than %d entries", ErrTooLarge, maxZipEntries)
		}
		rel, err := filepath.Rel(abs, p)
		if err != nil {
			return err
		}
		entryInfo, err := d.Info()
		if err != nil {
			return err
		}
		if err := writeZipEntry(archive, path.Join(prefix, filepath.ToSlash(rel)), p, entryInfo); err != nil {
			if errors.Is(err, ErrUnsupported) {
				return nil // sockets, fifos and devices are simply left out
			}
			return err
		}
		*total++
		return nil
	})
}

// writeZipEntry stores one path in the archive.
func writeZipEntry(archive *zip.Writer, name, abs string, info os.FileInfo) error {
	switch {
	case info.IsDir():
		hdr := &zip.FileHeader{Name: name + "/", Method: zip.Store}
		hdr.SetMode(info.Mode())
		hdr.Modified = info.ModTime()
		_, err := archive.CreateHeader(hdr)
		return err

	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(abs)
		if err != nil {
			return err
		}
		// Stored uncompressed with the link inside: the size is written in the
		// data descriptor, so there is nothing to know up front.
		hdr := &zip.FileHeader{Name: name, Method: zip.Store, Modified: info.ModTime()}
		hdr.SetMode(info.Mode())
		writer, err := archive.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_, err = writer.Write([]byte(target))
		return err

	case info.Mode().IsRegular():
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = name
		hdr.Method = zip.Deflate
		writer, err := archive.CreateHeader(hdr)
		if err != nil {
			return err
		}
		file, err := os.Open(abs)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(writer, file)
		return err

	default:
		return fmt.Errorf("%w: %s", ErrUnsupported, info.Mode().Type())
	}
}

// Unzip extracts a zip archive into a directory inside the workspace and
// returns how many entries it wrote.
//
// The archive's own names are never trusted. Every entry is cleaned and checked
// to stay under the destination, so neither "../../etc/cron.d/x" nor an absolute
// name can land outside it; the expansion is bounded so a zip bomb cannot fill
// the disk; symlink entries are skipped rather than recreated; and nothing is
// written until the whole archive has been validated, so a rejected archive
// leaves no half-extracted tree behind.
func (s *Service) Unzip(ctx context.Context, relPath, destRel string, overwrite bool) (int, error) {
	abs, err := s.ws.Resolve(relPath)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if info.IsDir() {
		return 0, fmt.Errorf("%w: %q is not an archive", ErrInvalidName, filepath.Base(abs))
	}

	if strings.TrimSpace(destRel) == "" {
		// Default destination: a folder beside the archive, named after it.
		name := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
		destRel = filepath.ToSlash(filepath.Join(filepath.Dir(abs), name))
	}
	destAbs, err := s.ws.Resolve(destRel)
	if err != nil {
		return 0, err
	}
	if err := validateName(filepath.Base(destAbs)); err != nil {
		return 0, err
	}

	reader, err := zip.OpenReader(abs)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrArchive, err)
	}
	defer reader.Close()

	if len(reader.File) > maxUnzipEntries {
		return 0, fmt.Errorf("%w: archive holds more than %d entries", ErrArchive, maxUnzipEntries)
	}

	// Pass one: validate everything before touching the filesystem.
	type planned struct {
		abs  string
		file *zip.File
	}
	targets := make([]planned, 0, len(reader.File))
	var total uint64
	for _, file := range reader.File {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		entryAbs, err := unzipTarget(destAbs, file.Name)
		if err != nil {
			return 0, err
		}
		if entryAbs == "" {
			continue // "." or a skipped entry
		}
		if file.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !file.FileInfo().IsDir() && !file.Mode().IsRegular() {
			continue
		}
		total += file.UncompressedSize64
		if total > maxUnzipBytes {
			return 0, fmt.Errorf("%w: archive expands to more than %d bytes", ErrArchive, int64(maxUnzipBytes))
		}
		if _, err := os.Lstat(entryAbs); err == nil {
			if !overwrite {
				return 0, ErrAlreadyExists
			}
		} else if !os.IsNotExist(err) {
			return 0, err
		}
		targets = append(targets, planned{abs: entryAbs, file: file})
	}

	if err := os.MkdirAll(destAbs, 0o750); err != nil {
		return 0, err
	}

	// Pass two: write, with a running byte budget in case a header lied.
	remaining := int64(maxUnzipBytes)
	written := 0
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		if target.file.FileInfo().IsDir() {
			if err := os.MkdirAll(target.abs, 0o750); err != nil {
				return written, err
			}
			written++
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target.abs), 0o750); err != nil {
			return written, err
		}
		src, err := target.file.Open()
		if err != nil {
			return written, err
		}
		dst, err := os.OpenFile(target.abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, unzipFilePerm(target.file.Mode()))
		if err != nil {
			src.Close()
			return written, err
		}
		copied, copyErr := io.Copy(dst, io.LimitReader(src, remaining+1))
		src.Close()
		if closeErr := dst.Close(); copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			return written, copyErr
		}
		remaining -= copied
		if remaining < 0 {
			return written, fmt.Errorf("%w: archive expands to more than %d bytes", ErrArchive, int64(maxUnzipBytes))
		}
		written++
	}

	s.audit.Log(ctx, audit.Entry{
		Action: audit.ActionFileUnzip, Target: abs,
		Detail: fmt.Sprintf("%d entries -> %s", written, destAbs), Success: true,
	})
	return written, nil
}

// unzipTarget resolves one archive entry against the destination, returning ""
// for entries that should be skipped. Anything that would land outside the
// destination is refused.
func unzipTarget(destAbs, name string) (string, error) {
	if strings.ContainsRune(name, '\x00') || strings.Contains(name, "\\") {
		return "", fmt.Errorf("%w: entry %q has an unsupported name", ErrArchive, name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." {
		return "", nil
	}
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: entry %q escapes the destination", ErrArchive, name)
	}
	target := filepath.Join(destAbs, clean)
	if !within(destAbs, target) {
		return "", fmt.Errorf("%w: entry %q escapes the destination", ErrArchive, name)
	}
	return target, nil
}

// unzipFilePerm normalises an archived mode: the execute bit survives, group
// read is allowed, and group/other write plus setuid, setgid and sticky are
// stripped.
func unzipFilePerm(mode os.FileMode) os.FileMode {
	perm := mode.Perm() & 0o755
	if perm == 0 {
		perm = 0o640
	}
	return perm | 0o040
}
