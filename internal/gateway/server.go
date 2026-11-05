// Package gateway is the HTTP surface: the OpenAI-compatible speech endpoint,
// the voice catalogue, and the single-page dashboard.
package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

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

	// Login throttling: a fixed-window counter per client address.
	loginMu       sync.Mutex
	loginFailures map[string]loginEntry

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
	"/auth.js":     {"auth.js", "text/javascript; charset=utf-8"},
	"/router.js":   {"router.js", "text/javascript; charset=utf-8"},
	"/routing.js":  {"routing.js", "text/javascript; charset=utf-8"},
	"/clients.js":  {"clients.js", "text/javascript; charset=utf-8"},
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
		cfg:           cfg,
		store:         st,
		cart:          client,
		logger:        logger,
		voices:        newVoiceCache(256, voiceCacheTTL),
		assets:        assets,
		index:         assets["/"],
		previewFetch:  client.FetchPreview,
		loginFailures: make(map[string]loginEntry),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)

	// Authentication: the login page talks to these before it has a session.
	mux.HandleFunc("GET /auth/state", s.handleAuthState)
	mux.HandleFunc("POST /auth/setup", s.handleSetup)
	mux.HandleFunc("POST /auth/login", s.handleLogin)
	mux.HandleFunc("POST /auth/logout", s.handleLogout)

	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("POST /v1/audio/speech", s.handleSpeech)
	mux.HandleFunc("GET /v1/audio/voices", s.handleVoices)
	mux.HandleFunc("GET /v1/audio/voices/{id}/preview", s.handleVoicePreview)

	mux.HandleFunc("GET /api/overview", s.handleOverview)
	mux.HandleFunc("GET /api/routing", s.handleRouting)
	mux.HandleFunc("GET /api/account", s.handleAccount)
	mux.HandleFunc("PATCH /api/account", s.handleUpdateAccount)
	// A browser without JavaScript cannot issue PATCH; the change-password form
	// posts here and the handler treats the form body exactly like the JSON.
	mux.HandleFunc("POST /api/account", s.handleUpdateAccount)
	mux.HandleFunc("POST /api/account/logout-all", s.handleLogoutAll)
	mux.HandleFunc("GET /api/keys", s.handleListKeys)
	mux.HandleFunc("POST /api/keys", s.handleCreateKey)
	mux.HandleFunc("PATCH /api/keys/{id}", s.handlePatchKey)
	mux.HandleFunc("DELETE /api/keys/{id}", s.handleDeleteKey)
	mux.HandleFunc("POST /api/keys/{id}/reset", s.handleResetKey)
	mux.HandleFunc("GET /api/keys/{id}/reveal", s.handleRevealKey)
	mux.HandleFunc("GET /api/clients", s.handleListClientKeys)
	mux.HandleFunc("POST /api/clients", s.handleCreateClientKey)
	mux.HandleFunc("PATCH /api/clients/{id}", s.handlePatchClientKey)
	mux.HandleFunc("DELETE /api/clients/{id}", s.handleDeleteClientKey)
	mux.HandleFunc("POST /api/clients/{id}/rotate", s.handleRotateClientKey)
	mux.HandleFunc("GET /api/requests", s.handleRequests)
	mux.HandleFunc("PATCH /api/settings", s.handlePatchSettings)
	mux.HandleFunc("GET /api/voices", s.handleVoicesAPI)
	mux.HandleFunc("POST /api/test", s.handleTest)

	mux.HandleFunc("GET /app.css", s.handleAsset("/app.css"))
	mux.HandleFunc("GET /app.js", s.handleAsset("/app.js"))
	mux.HandleFunc("GET /auth.js", s.handleAsset("/auth.js"))
	mux.HandleFunc("GET /router.js", s.handleAsset("/router.js"))
	mux.HandleFunc("GET /routing.js", s.handleAsset("/routing.js"))
	mux.HandleFunc("GET /clients.js", s.handleAsset("/clients.js"))
	mux.HandleFunc("GET /favicon.ico", s.handleAsset("/favicon.ico"))
	mux.HandleFunc("GET /", s.handleIndex)

	// Order matters: logging wraps everything, sessions are resolved before the
	// gate decides, and CORS answers preflight for /v1 even without a session.
	s.handler = chain(mux, s.logMiddleware, s.corsMiddleware, s.sessionGuard, s.authMiddleware)
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
