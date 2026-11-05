package gateway

import (
	"net/http"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

// Client-key API. These routes live under /api/ and therefore require an
// operator session: only a signed-in human may mint credentials.

// clientKeyView is a key as the dashboard sees it. It never carries the
// secret — that exists only in the response to creation and to reveal.
type clientKeyView struct {
	ID           int64  `json:"id"`
	Label        string `json:"label"`
	Prefix       string `json:"prefix"`
	IsActive     bool   `json:"isActive"`
	CreatedAt    int64  `json:"createdAt"`
	LastUsedAt   int64  `json:"lastUsedAt"`
	RequestCount int64  `json:"requestCount"`
	ExpiresAt    int64  `json:"expiresAt"`
	Expired      bool   `json:"expired"`
}

// handleListClientKeys answers GET /api/clients.
func (s *Server) handleListClientKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.ListClientKeys(r.Context())
	if err != nil {
		s.logError("list client keys", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	now := time.Now()
	views := make([]clientKeyView, 0, len(keys))
	for _, key := range keys {
		views = append(views, toClientKeyView(key, now))
	}
	stats, err := s.store.CountClientKeys(r.Context())
	if err != nil {
		s.logError("count client keys", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": views, "stats": stats})
}

// handleCreateClientKey answers POST /api/clients. The secret appears exactly
// once, in this response.
func (s *Server) handleCreateClientKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label     string `json:"label"`
		ExpiresIn int    `json:"expiresInDays"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.ExpiresIn < 0 || body.ExpiresIn > 3650 {
		writeError(w, http.StatusBadRequest, "expiresInDays must be between 0 (never) and 3650")
		return
	}
	s.importLegacyToken(r)

	key, secret, err := s.store.CreateClientKey(r.Context(), body.Label,
		time.Duration(body.ExpiresIn)*24*time.Hour)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.logger.Info("client key created", "id", key.ID, "label", key.Label)
	writeJSON(w, http.StatusCreated, map[string]any{
		"key":    toClientKeyView(key, time.Now()),
		"secret": secret,
	})
}

// handlePatchClientKey answers PATCH /api/clients/{id}, currently only the
// active flag. Disabling takes effect on the next request.
func (s *Server) handlePatchClientKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		IsActive *bool `json:"isActive"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.IsActive == nil {
		writeError(w, http.StatusBadRequest, `nothing to change: send {"isActive":true|false}`)
		return
	}
	if err := s.store.SetClientKeyActive(r.Context(), id, *body.IsActive); err != nil {
		s.logError("patch client key", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	key, err := s.store.GetClientKey(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	s.logger.Info("client key updated", "id", id, "active", *body.IsActive)
	writeJSON(w, http.StatusOK, toClientKeyView(key, time.Now()))
}

// handleDeleteClientKey answers DELETE /api/clients/{id}. The secret cannot be
// recovered afterwards.
func (s *Server) handleDeleteClientKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteClientKey(r.Context(), id); err != nil {
		s.logError("delete client key", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	s.logger.Info("client key deleted", "id", id)
	w.WriteHeader(http.StatusNoContent)
}

// handleRotateClientKey answers POST /api/clients/{id}/rotate: a fresh secret
// for the same key, for when one leaks but the client's setup should stay.
func (s *Server) handleRotateClientKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	existing, err := s.store.GetClientKey(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	if err := s.store.DeleteClientKey(r.Context(), id); err != nil {
		s.logError("rotate: delete old", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	var expiresIn time.Duration
	if existing.ExpiresAt > 0 {
		remaining := time.Until(time.Unix(existing.ExpiresAt, 0))
		if remaining > 0 {
			expiresIn = remaining
		}
	}
	key, secret, err := s.store.CreateClientKey(r.Context(), existing.Label, expiresIn)
	if err != nil {
		s.logError("rotate: create replacement", err)
		writeError(w, http.StatusInternalServerError, "rotasi gagal; key lama sudah dihapus, buat key baru")
		return
	}
	s.logger.Info("client key rotated", "oldId", id, "id", key.ID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"key":    toClientKeyView(key, time.Now()),
		"secret": secret,
	})
}

func toClientKeyView(key store.ClientKey, now time.Time) clientKeyView {
	return clientKeyView{
		ID:           key.ID,
		Label:        key.Label,
		Prefix:       key.Prefix,
		IsActive:     key.IsActive,
		CreatedAt:    key.CreatedAt,
		LastUsedAt:   key.LastUsedAt,
		RequestCount: key.RequestCount,
		ExpiresAt:    key.ExpiresAt,
		Expired:      key.Expired(now),
	}
}
