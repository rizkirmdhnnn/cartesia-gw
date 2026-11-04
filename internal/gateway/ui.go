package gateway

import (
	_ "embed"
	"net/http"
)

// indexHTML is embedded so the binary has no runtime asset dependency: no
// build step, no CDN, no file that can go missing on the target host.
//
//go:embed ui/index.html
var indexHTML []byte

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(indexHTML)
}
