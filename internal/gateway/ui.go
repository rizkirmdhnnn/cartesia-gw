package gateway

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"net/http"
	"path"
)

// uiFS holds the dashboard. It is embedded so the binary has no runtime asset
// dependency: no build step, no CDN, no file that can go missing on the target
// host.
//
//go:embed ui
var uiFS embed.FS

// asset is one embedded file plus the metadata needed to serve it well.
type asset struct {
	body        []byte
	contentType string
	etag        string
}

// loadAsset reads a file out of the embedded tree. It returns an error rather
// than panicking: the embed directive guarantees the directory exists, not
// that a file inside it kept its name.
func loadAsset(name, contentType string) (*asset, error) {
	body, err := uiFS.ReadFile(path.Join("ui", name))
	if err != nil {
		return nil, fmt.Errorf("read embedded asset %s: %w", name, err)
	}
	sum := sha256.Sum256(body)
	return &asset{
		body:        body,
		contentType: contentType,
		etag:        `"` + hex.EncodeToString(sum[:8]) + `"`,
	}, nil
}

// handleAsset serves a dashboard asset with an ETag. "no-cache" rather than a
// long max-age: the files change only when the binary does, and a stale script
// served after an upgrade is a debugging trap that costs more than the
// revalidation it saves.
func (s *Server) handleAsset(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, ok := s.assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", a.contentType)
		w.Header().Set("ETag", a.etag)
		w.Header().Set("Cache-Control", "no-cache")
		if r.Header.Get("If-None-Match") == a.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write(a.body)
	}
}

// handleIndex serves the page shell. Its assets stream in alongside, so the
// shell itself is never cached.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(s.index.body)
}
