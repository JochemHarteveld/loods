package edge

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// staticHandler serves a built directory. A single-page app is the normal case
// for the projects on this board, so a path that is not a file falls back to
// index.html — but only for what looks like a navigation, so a missing asset
// still 404s instead of silently returning HTML that the browser then fails to
// parse as JavaScript.
func staticHandler(r Route) http.Handler {
	dir := r.Dir
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if exists(dir, req.URL.Path) {
			files.ServeHTTP(w, req)
			return
		}
		if r.SPA && isNavigation(req) {
			req = req.Clone(req.Context())
			req.URL.Path = "/"
			files.ServeHTTP(w, req)
			return
		}
		writeNotFound(w, r, req.URL.Path)
	})
}

// exists reports whether the request maps to a real file, or to a directory
// with an index.html. http.Dir does the escaping and the symlink refusal.
func exists(dir, urlPath string) bool {
	clean := filepath.Clean("/" + strings.TrimPrefix(urlPath, "/"))
	full := filepath.Join(dir, clean)
	info, err := os.Stat(full)
	if err != nil {
		return false
	}
	if !info.IsDir() {
		return true
	}
	_, err = os.Stat(filepath.Join(full, "index.html"))
	return err == nil
}

// isNavigation tells a browser asking for a page from a fetch for an asset.
// Modern browsers say so themselves; older ones are read from Accept.
func isNavigation(req *http.Request) bool {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return false
	}
	if mode := req.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return strings.Contains(req.Header.Get("Accept"), "text/html")
}
