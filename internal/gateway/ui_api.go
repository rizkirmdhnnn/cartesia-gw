package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/cartesia"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/elevenlabs"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/provider"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/tts"
)

const (
	defaultRequestPage = 50
	maxRequestPage     = 200
	maxTestBody        = 64 << 10
)

// KeyView is one upstream key plus the numbers the visualiser and the table
// both show.
type keyView struct {
	ID            int64   `json:"id"`
	Label         string  `json:"label"`
	Provider      string  `json:"provider"`
	MaskedSecret  string  `json:"maskedSecret"`
	IsActive      bool    `json:"isActive"`
	Priority      int     `json:"priority"`
	Strikes       int     `json:"strikes"`
	CooldownUntil int64   `json:"cooldownUntil"`
	Cooling       bool    `json:"cooling"`
	LastError     string  `json:"lastError"`
	LastUsedAt    int64   `json:"lastUsedAt"`
	CreatedAt     int64   `json:"createdAt"`
	Requests24h   int64   `json:"requests24h"`
	Share         float64 `json:"share"`
	Eligible      bool    `json:"eligible"`
	NextUp        bool    `json:"nextUp"`
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
	rotation, err := s.store.RotationStrategy(r.Context())
	if err != nil {
		s.logError("overview: rotation", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}

	clientStats, err := s.store.CountClientKeys(r.Context())
	if err != nil {
		s.logError("overview: count clients", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"version":         s.cfg.Version,
		"cartesiaVersion": s.cfg.CartesiaVersion,
		"retentionDays":   int(s.cfg.Retention.Hours() / 24),
		"keys":            keys,
		"clients":         clientStats,
		"usage":           usage,
		"models":          s.providers.Catalog(),
		"defaultModel":    defaultModelName(s.providers),
		"providers":       s.providersForConsole(r),
		// The rotation in force and the set that can be chosen. A strategy the
		// flag set but the dashboard has overridden is not reported here — the
		// stored one is what actually picks keys.
		"rotation":  rotation.String(),
		"rotations": store.StrategyNames(),
		// The controls and their bounds come from the server so the dashboard
		// never carries a second copy of a value the gateway validates.
		"controls": map[string]any{
			"speedRange":      []float64{cartesia.MinSpeed, cartesia.MaxSpeed},
			"volumeRange":     []float64{cartesia.MinVolume, cartesia.MaxVolume},
			"emotions":        cartesia.Emotions,
			"primaryEmotions": cartesia.PrimaryEmotions,
			// Per-provider bounds, keyed by provider id, because speed means a
			// different range on each and the console shows the one in force.
			"providerRanges": map[string]any{
				"cartesia": map[string]any{
					"speed":  []float64{cartesia.MinSpeed, cartesia.MaxSpeed},
					"volume": []float64{cartesia.MinVolume, cartesia.MaxVolume},
				},
				"elevenlabs": map[string]any{
					"speed":  []float64{elevenlabs.MinSpeed, elevenlabs.MaxSpeed},
					"volume": nil,
				},
			},
			"formats": []string{"mp3", "wav", "pcm", "opus"},
		},
	})
}

// defaultModelName is the prefixed id of the first provider's default model.
func defaultModelName(registry *provider.Registry) string {
	p := registry.Default()
	if p == nil {
		return ""
	}
	return p.Name() + "/" + p.DefaultModel()
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
		views = append(views, toKeyView(key, time.Now()))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": views})
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Label    string `json:"label"`
		Secret   string `json:"secret"`
		Provider string `json:"provider"`
		Priority *int   `json:"priority"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxTestBody)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// An omitted provider means the one the secret's shape names, so pasting a
	// key is enough; an explicit value always wins.
	providerName := store.NormalizeProvider(body.Provider)
	if strings.TrimSpace(body.Provider) == "" {
		providerName = providerForSecret(body.Secret)
	} else if _, ok := s.providers.Get(providerName); !ok {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("provider %q is not configured; known: %s", body.Provider, strings.Join(s.providers.Names(), ", ")))
		return
	}
	key, err := s.store.CreateKeyFor(r.Context(), providerName, body.Label, body.Secret)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Priority != nil {
		if err := s.store.SetKeyPriority(r.Context(), key.ID, *body.Priority); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		key, _ = s.store.GetKey(r.Context(), key.ID)
	}
	writeJSON(w, http.StatusCreated, toKeyView(key, time.Now()))
}

func (s *Server) handlePatchKey(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		IsActive *bool `json:"isActive"`
		Priority *int  `json:"priority"`
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
	if body.Priority != nil {
		if err := s.store.SetKeyPriority(r.Context(), id, *body.Priority); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	key, err := s.store.GetKey(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "key not found")
		return
	}
	writeJSON(w, http.StatusOK, toKeyView(key, time.Now()))
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
	writeJSON(w, http.StatusOK, toKeyView(key, time.Now()))
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

// handlePatchSettings changes a runtime setting. Rotation and the per-provider
// default voice take effect on the next request without a restart.
func (s *Server) handlePatchSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Rotation     *string           `json:"rotation"`
		DefaultVoice map[string]string `json:"defaultVoice"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxTestBody)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Rotation == nil && len(body.DefaultVoice) == 0 {
		writeError(w, http.StatusBadRequest,
			`nothing to change: send {"rotation":"<strategy>"} or {"defaultVoice":{"<provider>":"<voice>"}}`)
		return
	}
	if body.Rotation != nil {
		strategy, err := store.ParseStrategy(*body.Rotation)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.store.SetRotationStrategy(r.Context(), strategy); err != nil {
			s.logError("set rotation", err)
			writeError(w, http.StatusInternalServerError, "database unavailable")
			return
		}
		s.logger.Info("rotation strategy changed", "strategy", strategy.String())
	}
	for name, voice := range body.DefaultVoice {
		canonical, ok := aliasProvider(strings.ToLower(strings.TrimSpace(name)))
		if !ok {
			canonical = strings.ToLower(strings.TrimSpace(name))
		}
		if _, found := s.providers.Get(canonical); !found {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("provider %q is not configured", name))
			return
		}
		// Store the id when the value is a name, so the setting does not go
		// stale when a voice is renamed.
		voice = strings.TrimSpace(voice)
		if voice != "" {
			if p, found := s.providers.Get(canonical); found {
				if resolved, _ := s.resolveVoice(r.Context(), p, voice, ""); resolved != "" {
					voice = resolved
				}
			}
		}
		if err := s.store.SetDefaultVoice(r.Context(), canonical, voice); err != nil {
			s.logError("set default voice", err)
			writeError(w, http.StatusInternalServerError, "database unavailable")
			return
		}
		s.logger.Info("default voice changed", "provider", canonical, "voice", voice)
	}

	response := map[string]any{}
	if body.Rotation != nil {
		response["rotation"] = body.Rotation
	}
	if len(body.DefaultVoice) > 0 {
		defaults := map[string]string{}
		for _, p := range s.providers.Providers() {
			voice, err := s.store.DefaultVoice(r.Context(), p.Name())
			if err == nil {
				defaults[p.Name()] = voice
			}
		}
		response["defaultVoice"] = defaults
	}
	writeJSON(w, http.StatusOK, response)
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
// "Test" button exercises routing, cooldown and usage logging rather than a
// side door that behaves differently.
func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model           string   `json:"model"`
		Input           string   `json:"input"`
		Voice           string   `json:"voice"`
		ResponseFormat  string   `json:"response_format"`
		Speed           *float64 `json:"speed"`
		Volume          *float64 `json:"volume"`
		Emotion         string   `json:"emotion"`
		Language        string   `json:"language"`
		Locale          string   `json:"locale"`
		Stability       *float64 `json:"stability"`
		SimilarityBoost *float64 `json:"similarity_boost"`
		Style           *float64 `json:"style"`
		UseSpeakerBoost *bool    `json:"use_speaker_boost"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxTestBody)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(body.Input) == "" {
		body.Input = "Halo, ini uji suara dari cartesia-gw."
	}
	request := tts.SpeechRequest{
		Model:           body.Model,
		Input:           body.Input,
		Voice:           body.Voice,
		ResponseFormat:  body.ResponseFormat,
		Speed:           body.Speed,
		Volume:          body.Volume,
		Emotion:         body.Emotion,
		Language:        body.Language,
		Locale:          body.Locale,
		Stability:       body.Stability,
		SimilarityBoost: body.SimilarityBoost,
		Style:           body.Style,
		SpeakerBoost:    body.UseSpeakerBoost,
	}
	plan, providerName, err := s.planFor(r.Context(), request)
	if err != nil {
		s.writePlanError(w, err)
		return
	}
	w.Header().Set("X-Gateway-Provider", providerName)
	s.synthesize(w, r, plan, false)
}

func toKeyView(key store.Key, now time.Time) keyView {
	return keyView{
		ID:            key.ID,
		Label:         key.Label,
		Provider:      key.Provider,
		MaskedSecret:  maskSecret(key.Secret),
		IsActive:      key.IsActive,
		Priority:      key.Priority,
		Strikes:       key.Strikes,
		CooldownUntil: key.CooldownUntil,
		Cooling:       key.Cooling(now),
		LastError:     key.LastError,
		LastUsedAt:    key.LastUsedAt,
		CreatedAt:     key.CreatedAt,
		Eligible:      key.Eligible(now),
	}
}

// providerForSecret guesses which provider a pasted secret belongs to. Cartesia
// keys start with sk_car_; ElevenLabs keys are 32 hex characters without a
// prefix. Anything else is stored as Cartesia, the provider this gateway
// shipped with, and the console lets the operator override it.
func providerForSecret(secret string) string {
	secret = strings.TrimSpace(secret)
	if strings.HasPrefix(secret, "sk_car_") {
		return "cartesia"
	}
	if len(secret) == 32 && isHex(secret) {
		return "elevenlabs"
	}
	return store.DefaultProvider
}

func isHex(value string) bool {
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
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
