package httpapi

import (
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"dashboard/internal/audit"
	"dashboard/internal/files"
	"dashboard/internal/workspace"
)

// mapFileError converts a domain error into a safe HTTP status, a stable code
// and a user-facing message. Internal details are never leaked.
func mapFileError(err error) (int, string, string) {
	switch {
	case err == nil:
		return http.StatusOK, "", ""
	case errors.Is(err, files.ErrNotFound), errors.Is(err, os.ErrNotExist):
		return http.StatusNotFound, CodeNotFound, "path not found"
	case errors.Is(err, files.ErrIsDirectory):
		return http.StatusBadRequest, CodeBadRequest, "path is a directory, not a file"
	case errors.Is(err, files.ErrNotDirectory):
		return http.StatusBadRequest, CodeBadRequest, "path is not a directory"
	case errors.Is(err, files.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, CodeTooLarge, "file exceeds the configured size limit"
	case errors.Is(err, files.ErrAlreadyExists):
		return http.StatusConflict, CodeConflict, "a file or folder with that name already exists"
	case errors.Is(err, files.ErrInvalidName):
		return http.StatusBadRequest, CodeBadRequest, "the name is not allowed"
	case errors.Is(err, files.ErrRootProtected):
		return http.StatusForbidden, CodeForbidden, "the workspace root cannot be modified"
	case errors.Is(err, workspace.ErrEscape):
		return http.StatusForbidden, CodeForbidden, "path is outside the workspace"
	case errors.Is(err, workspace.ErrEmptyPath):
		return http.StatusBadRequest, CodeBadRequest, "path must not be empty"
	case errors.Is(err, files.ErrReadOnly):
		return http.StatusForbidden, CodeForbidden, "target is read-only"
	case errors.Is(err, files.ErrBinary):
		return http.StatusUnsupportedMediaType, CodeNotAllowed, "binary files cannot be edited"
	case errors.Is(err, files.ErrInvalidTarget):
		return http.StatusBadRequest, CodeBadRequest, "invalid destination path"
	case errors.Is(err, files.ErrUnsafeMode):
		return http.StatusBadRequest, CodeBadRequest, "that permission mode is not allowed"
	case errors.Is(err, files.ErrUnsupported):
		return http.StatusBadRequest, CodeBadRequest, "that file type is not supported here"
	case errors.Is(err, files.ErrArchive):
		return http.StatusBadRequest, CodeBadRequest, "the archive is not valid"
	default:
		return http.StatusInternalServerError, CodeInternal, "file operation failed"
	}
}

func (s *Server) writeFileError(w http.ResponseWriter, r *http.Request, err error, action string) {
	status, code, msg := mapFileError(err)
	if status == http.StatusForbidden || status == http.StatusRequestEntityTooLarge {
		u := userFrom(r.Context())
		entry := audit.Entry{
			Action: audit.ActionSecurityDenied, Target: r.URL.Query().Get("path"),
			Detail: action + ": " + err.Error(), IP: s.clientIP(r), Success: false,
		}
		if u != nil {
			entry.UserID, entry.Username = u.ID, u.Username
		}
		s.audit.Log(r.Context(), entry)
	}
	writeError(w, status, code, msg)
}

// downloadMaxDuration bounds one download request. Streaming a large file to a
// slow client is a legitimate long request, so the server-wide write deadline is
// lifted for this route only.
const downloadMaxDuration = 60 * time.Minute

// handleFileDownload streams a file to the browser.
//
// The body is served straight from the file descriptor through
// http.ServeContent, so range requests (resume / partial downloads) work and the
// file never has to fit in memory. The response is always an octet-stream
// attachment: a downloaded HTML or SVG file must never be rendered inside the
// dashboard's own origin.
func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "path is required")
		return
	}
	file, info, err := s.files.Open(r.Context(), path)
	if err != nil {
		s.writeFileError(w, r, err, "download")
		return
	}
	defer file.Close()

	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(downloadMaxDuration)); err != nil {
		slog.Warn("download: cannot extend write deadline", "error", err)
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", attachmentHeader(info.Name()))
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}

// attachmentHeader builds a Content-Disposition value that survives quotes,
// spaces and non-ASCII file names (RFC 6266 / RFC 2231).
func attachmentHeader(name string) string {
	if value := mime.FormatMediaType("attachment", map[string]string{"filename": name}); value != "" {
		return value
	}
	return "attachment"
}

// handleFilesList returns the contents of a directory.
func (s *Server) handleFilesList(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	listing, err := s.files.List(r.Context(), path)
	if err != nil {
		s.writeFileError(w, r, err, "list")
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

// handleFileContentGet returns the text content of a file.
func (s *Server) handleFileContentGet(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "path is required")
		return
	}
	content, err := s.files.Read(r.Context(), path)
	if err != nil {
		s.writeFileError(w, r, err, "read")
		return
	}
	writeJSON(w, http.StatusOK, content)
}

type writeContentRequest struct {
	Content string `json:"content"`
}

// handleFileContentPut writes text content to a file.
func (s *Server) handleFileContentPut(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "path is required")
		return
	}
	var req writeContentRequest
	if err := decodeJSON(w, r, &req, s.cfg.MaxWriteSize*2+(64<<10)); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body or payload too large")
		return
	}
	content, err := s.files.Write(r.Context(), path, []byte(req.Content))
	if err != nil {
		s.writeFileError(w, r, err, "write")
		return
	}
	writeJSON(w, http.StatusOK, content)
}

type createRequest struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Overwrite bool   `json:"overwrite"`
}

// handleFilesCreate creates a file or folder.
func (s *Server) handleFilesCreate(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "path is required")
		return
	}
	kind := files.CreateKind(req.Kind)
	entry, err := s.files.Create(r.Context(), req.Path, kind, req.Overwrite)
	if err != nil {
		s.writeFileError(w, r, err, "create")
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

// uploadMaxDuration bounds one upload request. A large file over a slow line is
// a legitimate long request, so the server-wide read/write deadlines are lifted
// for this route only; everything else keeps its normal timeouts.
const uploadMaxDuration = 30 * time.Minute

// handleFileUpload stores one uploaded file.
//
// The body is the raw file content (no multipart wrapper): the browser uploads
// files one at a time so each one gets its own progress, its own error and its
// own conflict prompt, and the server never has to buffer a request just to find
// out what it contains. The stream goes straight to a temporary file which is
// renamed into place at the end, so a failed upload never leaves a partial file
// behind.
func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	// Names are used verbatim (a leading or trailing space is a legal, if
	// unusual, file name) but an entirely blank one is refused up front.
	dir := query.Get("path")
	name := query.Get("name")
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(name) == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "path and name are required")
		return
	}
	overwrite := query.Get("overwrite") == "1" || strings.EqualFold(query.Get("overwrite"), "true")

	// maxRead, maxWrite, maxUpload
	_, _, maxUpload := s.files.Limits()
	if r.ContentLength > maxUpload {
		s.writeFileError(w, r, fmt.Errorf("%w: %d bytes exceeds the %d byte upload limit",
			files.ErrTooLarge, r.ContentLength, maxUpload), "upload")
		return
	}

	// Lift the per-connection deadlines while this request is being read and
	// answered; the next request on the connection gets them back (the server
	// resets them from its own timeout settings).
	deadline := time.Now().Add(uploadMaxDuration)
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(deadline); err != nil {
		slog.Warn("upload: cannot extend read deadline", "error", err)
	}
	if err := rc.SetWriteDeadline(deadline); err != nil {
		slog.Warn("upload: cannot extend write deadline", "error", err)
	}

	entry, err := s.files.Store(r.Context(), dir, name, r.Body, overwrite)
	if err != nil {
		s.writeFileError(w, r, err, "upload")
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

type renameRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// handleFilesRename renames or moves a file or folder.
func (s *Server) handleFilesRename(w http.ResponseWriter, r *http.Request) {
	var req renameRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.From) == "" || strings.TrimSpace(req.To) == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "from and to are required")
		return
	}
	entry, err := s.files.Rename(r.Context(), req.From, req.To)
	if err != nil {
		s.writeFileError(w, r, err, "rename")
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// handleFilesDelete removes a file or folder.
func (s *Server) handleFilesDelete(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "path is required")
		return
	}
	if err := s.files.Delete(r.Context(), path); err != nil {
		s.writeFileError(w, r, err, "delete")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": path})
}

type transferRequest struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Overwrite bool   `json:"overwrite"`
}

// handleFilesCopy duplicates a file or folder.
func (s *Server) handleFilesCopy(w http.ResponseWriter, r *http.Request) {
	var req transferRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.From) == "" || strings.TrimSpace(req.To) == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "from and to are required")
		return
	}
	entry, err := s.files.Copy(r.Context(), req.From, req.To, req.Overwrite)
	if err != nil {
		s.writeFileError(w, r, err, "copy")
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

// handleFilesMove relocates a file or folder.
func (s *Server) handleFilesMove(w http.ResponseWriter, r *http.Request) {
	var req transferRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.From) == "" || strings.TrimSpace(req.To) == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "from and to are required")
		return
	}
	entry, err := s.files.Move(r.Context(), req.From, req.To)
	if err != nil {
		s.writeFileError(w, r, err, "move")
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// handleFileStat returns the detailed properties of one path.
func (s *Server) handleFileStat(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "path is required")
		return
	}
	stat, err := s.files.Stat(r.Context(), path)
	if err != nil {
		s.writeFileError(w, r, err, "stat")
		return
	}
	writeJSON(w, http.StatusOK, stat)
}

type chmodRequest struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// handleFileChmod changes the permission bits of a path. Setuid, setgid and
// sticky are refused by the service (see files.Service.Chmod).
func (s *Server) handleFileChmod(w http.ResponseWriter, r *http.Request) {
	var req chmodRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "path is required")
		return
	}
	mode, err := parseFileMode(req.Mode)
	if err != nil {
		s.writeFileError(w, r, err, "chmod")
		return
	}
	entry, err := s.files.Chmod(r.Context(), req.Path, mode)
	if err != nil {
		s.writeFileError(w, r, err, "chmod")
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// parseFileMode reads an octal permission string such as "0644", "644" or
// "0o750". Anything above 0777 is rejected here as well as in the service.
func parseFileMode(value string) (os.FileMode, error) {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(strings.TrimPrefix(trimmed, "0o"), "0O")
	if trimmed == "" || len(trimmed) > 4 {
		return 0, fmt.Errorf("%w: mode must be one to four octal digits", files.ErrUnsafeMode)
	}
	bits, err := strconv.ParseUint(trimmed, 8, 32)
	if err != nil || bits > 0o777 {
		return 0, fmt.Errorf("%w: mode must be octal and at most 0777", files.ErrUnsafeMode)
	}
	return os.FileMode(bits), nil
}

// zipMaxDuration bounds one archive request; like a download, streaming a large
// tree to a slow client is a legitimate long request.
const zipMaxDuration = 60 * time.Minute

// streamTracker notes whether any byte of a streaming response has gone out, so
// a failure that happens before the first write can still be reported as JSON.
type streamTracker struct {
	http.ResponseWriter
	wrote bool
}

func (t *streamTracker) Write(p []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(p)
}

// Unwrap keeps http.NewResponseController (deadline changes) working through
// the wrapper.
func (t *streamTracker) Unwrap() http.ResponseWriter { return t.ResponseWriter }

// handleFileZip streams a file or folder as a ZIP archive.
//
// Unlike a plain download there is no Content-Length: the archive is produced
// while it is sent, which is what makes archiving a large directory possible
// without a temporary file.
func (s *Server) handleFileZip(w http.ResponseWriter, r *http.Request) {
	paths := r.URL.Query()["path"]
	// Ignore empty repeats so "path=&path=/x" still works.
	kept := make([]string, 0, len(paths))
	for _, p := range paths {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "path is required")
		return
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(zipMaxDuration)); err != nil {
		slog.Warn("zip: cannot extend write deadline", "error", err)
	}

	// The client may name the archive ("Unduh 3 item sebagai ZIP"), but only a
	// plain file name is ever echoed back into the header.
	downloadName := archiveName(kept)
	if requested := strings.TrimSpace(r.URL.Query().Get("name")); requested != "" {
		if sanitized, err := safeDownloadName(requested); err == nil {
			downloadName = sanitized
		}
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", attachmentHeader(downloadName))

	tracked := &streamTracker{ResponseWriter: w}
	if _, err := s.files.ZipPaths(r.Context(), kept, tracked); err != nil {
		if !tracked.wrote {
			// Nothing was sent yet, so a normal error response is still possible.
			s.writeFileError(w, r, err, "zip")
			return
		}
		// The archive is already on the wire. Aborting the handler makes net/http
		// close the connection, so the client sees a truncated download instead
		// of a short-but-valid archive.
		slog.Error("zip: stream aborted mid-archive", "paths", kept, "error", err)
		panic(http.ErrAbortHandler)
	}
}

// archiveName derives the download name for one or more zipped paths.
func archiveName(paths []string) string {
	if len(paths) == 1 {
		base := strings.Trim(path.Base(filepath.ToSlash(paths[0])), "/")
		if base == "" || base == "." || base == ".." {
			base = "workspace"
		}
		return base + ".zip"
	}
	if len(paths) > 1 {
		return fmt.Sprintf("pilihan-%d-item.zip", len(paths))
	}
	return "workspace.zip"
}

// safeDownloadName reduces a client-supplied archive name to a plain file name:
// no path separators, no control characters, a bounded length and a .zip suffix.
// The header is only ever built from the result, never from the raw value.
func safeDownloadName(name string) (string, error) {
	cleaned := filepath.Base(filepath.ToSlash(name))
	if len(cleaned) > 120 {
		cleaned = cleaned[:120]
	}
	var b strings.Builder
	for _, r := range cleaned {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' || r == '"' {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		return "", errors.New("empty archive name")
	}
	if !strings.HasSuffix(strings.ToLower(out), ".zip") {
		out += ".zip"
	}
	return out, nil
}

type unzipRequest struct {
	Path      string `json:"path"`
	Dest      string `json:"dest"`
	Overwrite bool   `json:"overwrite"`
}

// handleFileUnzip extracts a zip archive inside the workspace.
func (s *Server) handleFileUnzip(w http.ResponseWriter, r *http.Request) {
	var req unzipRequest
	if err := decodeJSON(w, r, &req, 8<<10); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "path is required")
		return
	}
	entries, err := s.files.Unzip(r.Context(), req.Path, req.Dest, req.Overwrite)
	if err != nil {
		s.writeFileError(w, r, err, "unzip")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "entries": entries, "dest": req.Dest})
}
