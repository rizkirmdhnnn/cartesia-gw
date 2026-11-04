package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/cartesia"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

const (
	defaultRequestPage = 50
	maxRequestPage     = 200
	maxTestBody        = 64 << 10
)

// keyView is a key as the dashboard sees it: the secret is masked unless the
// operator explicitly reveals it.
type keyView struct {
	ID            int64  `json:"id"`
	Label         string `json:"label"`
	MaskedSecret  string `json:"maskedSecret"`
	IsActive      bool   `json:"isActive"`
	Strikes       int    `json:"strikes"`
	CooldownUntil int64  `json:"cooldownUntil"`
	Cooling       bool   `json:"cooling"`
	LastError     string `json:"lastError"`
	LastUsedAt    int64  `json:"lastUsedAt"`
	CreatedAt     int64  `json:"createdAt"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.CountKeys(r.Context())
	if err != nil {
		s.logError("overview: count keys", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	usage, err := s.store.Usage(r.Context())
	if err != nil {
		s.logError("overview: usage", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":         s.cfg.Version,
		"cartesiaVersion": s.cfg.CartesiaVersion,
		"retentionDays":   int(s.cfg.Retention.Hours() / 24),
		"keys":            keys,
		"usage":           usage,
		"models":          cartesia.Models,
		"defaultModel":    cartesia.DefaultModel,
		// The controls and their bounds come from the server so the dashboard
		// never carries a second copy of a value the gateway validates.
		"controls": map[string]any{
			"speedRange":      []float64{cartesia.MinSpeed, cartesia.MaxSpeed},
			"volumeRange":     []float64{cartesia.MinVolume, cartesia.MaxVolume},
			"emotions":        cartesia.Emotions,
			"primaryEmotions": cartesia.PrimaryEmotions,
		},
	})
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.ListKeys(r.Context())
	if err != nil {
		s.logError("list keys", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	views := make([]keyView, 0, len(keys))
	for _, key := range keys {
		views = append(views, toKeyView(key))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": views})
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label  string `json:"label"`
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxTestBody)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	key, err := s.store.CreateKey(r.Context(), body.Label, body.Secret)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toKeyView(key))
}

func (s *Server) handlePatchKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		IsActive *bool `json:"isActive"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxTestBody)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.IsActive != nil {
		if err := s.store.SetKeyActive(r.Context(), id, *body.IsActive); err != nil {
			s.logError("patch key", err)
			writeError(w, http.StatusInternalServerError, "database unavailable")
			return
		}
	}
	key, err := s.store.GetKey(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, toKeyView(key))
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteKey(r.Context(), id); err != nil {
		s.logError("delete key", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResetKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.ResetCooldown(r.Context(), id); err != nil {
		s.logError("reset key", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	key, err := s.store.GetKey(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, toKeyView(key))
}

// handleRevealKey hands back the full secret on an explicit request, so the
// list endpoint never carries it.
func (s *Server) handleRevealKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	key, err := s.store.GetKey(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": key.ID, "secret": key.Secret})
}

func (s *Server) handleRequests(w http.ResponseWriter, r *http.Request) {
	limit, offset := requestPage(r)
	rows, total, err := s.store.ListRequests(r.Context(), limit, offset)
	if err != nil {
		s.logError("list requests", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": rows, "total": total, "limit": limit, "offset": offset})
}

// handleTest runs the dashboard playground through the real speech path, so a
// "Test" button exercises rotation, cooldown and usage logging rather than a
// side door that behaves differently.
func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model          string   `json:"model"`
		Input          string   `json:"input"`
		Voice          string   `json:"voice"`
		ResponseFormat string   `json:"response_format"`
		Speed          *float64 `json:"speed"`
		Volume         *float64 `json:"volume"`
		Emotion        string   `json:"emotion"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxTestBody)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(body.Input) == "" {
		body.Input = "Halo, ini uji suara dari cartesia-gw."
	}
	translation, err := cartesia.Translate(cartesia.SpeechRequest{
		Model:          body.Model,
		Input:          body.Input,
		Voice:          body.Voice,
		ResponseFormat: body.ResponseFormat,
		Speed:          body.Speed,
		Volume:         body.Volume,
		Emotion:        body.Emotion,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.synthesize(w, r, translation, false)
}

func toKeyView(key store.Key) keyView {
	return keyView{
		ID:            key.ID,
		Label:         key.Label,
		MaskedSecret:  maskSecret(key.Secret),
		IsActive:      key.IsActive,
		Strikes:       key.Strikes,
		CooldownUntil: key.CooldownUntil,
		Cooling:       key.Cooling(time.Now()),
		LastError:     key.LastError,
		LastUsedAt:    key.LastUsedAt,
		CreatedAt:     key.CreatedAt,
	}
}

func maskSecret(secret string) string {
	if len(secret) <= 10 {
		return "••••"
	}
	return secret[:7] + "…" + secret[len(secret)-4:]
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

func requestPage(r *http.Request) (int, int) {
	limit := defaultRequestPage
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = min(parsed, maxRequestPage)
		}
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			offset = parsed
		}
	}
	return limit, offset
}
