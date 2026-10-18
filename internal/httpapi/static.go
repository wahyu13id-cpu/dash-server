package httpapi

import (
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// mountStatic serves the built single-page application from DASH_STATIC_DIR.
// Unknown non-API paths fall back to index.html so client-side routing works.
func (s *Server) mountStatic(mux *http.ServeMux) {
	dir := s.cfg.StaticDir
	indexPath := filepath.Join(dir, "index.html")

	fileServer := http.FileServer(http.Dir(dir))
	serveIndex := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, indexPath)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, CodeNotFound, "not found")
			return
		}
		p := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if p == "." {
			p = ""
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		if info, err := os.Stat(full); err == nil && !info.IsDir() {
			// Immutable assets get long-lived caching.
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		serveIndex(w, r)
	})

	if _, err := fs.Stat(os.DirFS(dir), "index.html"); err != nil {
		slog.Warn("static directory does not contain index.html; SPA will not be served", "dir", dir, "error", err)
	}
	mux.Handle("/", handler)
}
