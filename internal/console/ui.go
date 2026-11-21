package console

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/kuangjm123/AIGate/internal/respond"
)

//go:embed all:ui/dist
var dist embed.FS

func distRoot() fs.FS {
	sub, err := fs.Sub(dist, "ui/dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// page serves the control page and its built assets. It does not require a session.
func (s *Server) page(w http.ResponseWriter, r *http.Request) int {
	if r.Method != http.MethodGet {
		return 0
	}
	name := "index.html"
	if r.URL.Path != "/" {
		if !strings.HasPrefix(r.URL.Path, "/assets/") || strings.Contains(r.URL.Path, "..") {
			return 0
		}
		name = strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	}
	file, err := distRoot().Open(name)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "not_found")
		return http.StatusNotFound
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		respond.Error(w, http.StatusNotFound, "not_found")
		return http.StatusNotFound
	}
	w.Header().Set("Content-Type", contentType(name))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file)
	return http.StatusOK
}

func contentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}
