// Package gateway is the HTTP surface: the OpenAI-compatible speech endpoint,
// the voice catalogue, and the single-page dashboard.
package gateway

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/cartesia"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/config"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

// Server wires the store and the Cartesia client into an http.Handler.
type Server struct {
	cfg     *config.Config
	store   *store.Store
	cart    *cartesia.Client
	logger  *slog.Logger
	voices  *voiceCache
	assets  map[string]*asset
	index   *asset
	handler http.Handler

	// previewFetch is the client used for voice preview files. It is a field so
	// a test can stub the network hop while keeping the host guard in place.
	previewFetch func(context.Context, string, string) (*http.Response, error)
}

// dashboardAssets maps the served URL path to the embedded file and its type.
var dashboardAssets = map[string]struct {
	file        string
	contentType string
}{
	"/":            {"index.html", "text/html; charset=utf-8"},
	"/app.css":     {"app.css", "text/css; charset=utf-8"},
	"/app.js":      {"app.js", "text/javascript; charset=utf-8"},
	"/favicon.ico": {"favicon.svg", "image/svg+xml"},
}

// New builds the server and its route table.
func New(cfg *config.Config, st *store.Store, client *cartesia.Client, logger *slog.Logger) (*Server, error) {
	assets := make(map[string]*asset, len(dashboardAssets))
	for urlPath, spec := range dashboardAssets {
		loaded, err := loadAsset(spec.file, spec.contentType)
		if err != nil {
			return nil, err
		}
		assets[urlPath] = loaded
	}

	s := &Server{
		cfg:          cfg,
		store:        st,
		cart:         client,
		logger:       logger,
		voices:       newVoiceCache(256, voiceCacheTTL),
		assets:       assets,
		index:        assets["/"],
		previewFetch: client.FetchPreview,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("POST /v1/audio/speech", s.handleSpeech)
	mux.HandleFunc("GET /v1/audio/voices", s.handleVoices)
	mux.HandleFunc("GET /v1/audio/voices/{id}/preview", s.handleVoicePreview)

	mux.HandleFunc("GET /api/overview", s.handleOverview)
	mux.HandleFunc("GET /api/keys", s.handleListKeys)
	mux.HandleFunc("POST /api/keys", s.handleCreateKey)
	mux.HandleFunc("PATCH /api/keys/{id}", s.handlePatchKey)
	mux.HandleFunc("DELETE /api/keys/{id}", s.handleDeleteKey)
	mux.HandleFunc("POST /api/keys/{id}/reset", s.handleResetKey)
	mux.HandleFunc("GET /api/keys/{id}/reveal", s.handleRevealKey)
	mux.HandleFunc("GET /api/requests", s.handleRequests)
	mux.HandleFunc("PATCH /api/settings", s.handlePatchSettings)
	mux.HandleFunc("GET /api/voices", s.handleVoicesAPI)
	mux.HandleFunc("POST /api/test", s.handleTest)

	mux.HandleFunc("GET /app.css", s.handleAsset("/app.css"))
	mux.HandleFunc("GET /app.js", s.handleAsset("/app.js"))
	mux.HandleFunc("GET /favicon.ico", s.handleAsset("/favicon.ico"))
	mux.HandleFunc("GET /", s.handleIndex)

	s.handler = chain(mux, s.corsMiddleware, s.authMiddleware, s.logMiddleware)
	return s, nil
}

// Handler returns the composed middleware stack.
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.CountKeys(r.Context())
	if err != nil {
		s.logError("health: count keys", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":            "ok",
		"version":           s.cfg.Version,
		"cartesiaVersion":   s.cfg.CartesiaVersion,
		"keys":              keys,
		"retentionDays":     int(s.cfg.Retention.Hours() / 24),
		"gatewayTokenIsSet": s.cfg.GatewayToken != "",
	})
}

func (s *Server) logError(msg string, err error) {
	s.logger.Error(msg, "error", err)
}
