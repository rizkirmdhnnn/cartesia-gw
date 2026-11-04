package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/cartesia"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

const (
	voiceCacheTTL   = 10 * time.Minute
	voicePageMax    = 100
	previewMaxBytes = 16 << 20
)

// ttlCache is a bounded map with an expiry. Every cache in this process is one
// of these: an unbounded map keyed by client input is a memory leak with a
// schedule, and the box this runs on does not have room for one.
type ttlCache[V any] struct {
	mu    sync.Mutex
	max   int
	ttl   time.Duration
	items map[string]ttlEntry[V]
}

type ttlEntry[V any] struct {
	value   V
	expires time.Time
}

func newTTLCache[V any](max int, ttl time.Duration) *ttlCache[V] {
	return &ttlCache[V]{max: max, ttl: ttl, items: make(map[string]ttlEntry[V], max)}
}

func (c *ttlCache[V]) get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok || time.Now().After(entry.expires) {
		var zero V
		delete(c.items, key)
		return zero, false
	}
	return entry.value, true
}

func (c *ttlCache[V]) put(key string, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) >= c.max {
		c.evictLocked()
	}
	c.items[key] = ttlEntry[V]{value: value, expires: time.Now().Add(c.ttl)}
}

// evictLocked drops an expired entry when one exists, and otherwise drops an
// arbitrary one. Precision is not the point; a bounded size is.
func (c *ttlCache[V]) evictLocked() {
	now := time.Now()
	for key, entry := range c.items {
		if now.After(entry.expires) {
			delete(c.items, key)
			return
		}
	}
	for key := range c.items {
		delete(c.items, key)
		return
	}
}

// voiceCache memoises the voice catalogue and the resolved preview file URLs.
type voiceCache struct {
	pages    *ttlCache[cartesia.VoicesPage]
	previews *ttlCache[string]
}

func newVoiceCache(max int, ttl time.Duration) *voiceCache {
	return &voiceCache{
		pages:    newTTLCache[cartesia.VoicesPage](max, ttl),
		previews: newTTLCache[string](max, ttl),
	}
}

// listVoices answers from cache when it can, otherwise calls Cartesia once. It
// borrows a key through the same rotation picker the speech path uses, so a
// key parked in cooldown is not chosen for a lookup either.
func (s *Server) listVoices(ctx context.Context, query url.Values) (cartesia.VoicesPage, error) {
	cacheKey := query.Encode()
	if page, ok := s.voices.pages.get(cacheKey); ok {
		return page, nil
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		key, _, err := s.store.PickKey(ctx)
		if err != nil {
			return cartesia.VoicesPage{}, err
		}
		page, err := s.cart.ListVoices(ctx, key.Secret, query)
		if err == nil {
			s.voices.pages.put(cacheKey, page)
			return page, nil
		}
		lastErr = err
		var upstream *cartesia.UpstreamError
		if errors.As(err, &upstream) && retryable(upstream.Status) {
			_ = s.store.MarkFailure(ctx, key.ID, upstream.Status, upstream.Message, 0)
			continue
		}
		return cartesia.VoicesPage{}, err
	}
	return cartesia.VoicesPage{}, lastErr
}

// handleVoices serves GET /v1/audio/voices.
func (s *Server) handleVoices(w http.ResponseWriter, r *http.Request) {
	page, err := s.listVoices(r.Context(), voiceQuery(r))
	if err != nil {
		s.writeVoiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":      page.Data,
		"has_more":  page.HasMore,
		"next_page": page.NextPage,
	})
}

// handleVoicesAPI is the dashboard's view of the same data.
func (s *Server) handleVoicesAPI(w http.ResponseWriter, r *http.Request) {
	s.handleVoices(w, r)
}

// handleVoicePreview streams a voice's preview file. The URL comes from
// Cartesia and is fetched server-side because it requires the same
// Authorization header the browser does not have.
func (s *Server) handleVoicePreview(w http.ResponseWriter, r *http.Request) {
	voiceID := strings.TrimSpace(r.PathValue("id"))
	if voiceID == "" {
		writeError(w, http.StatusBadRequest, "voice id is required")
		return
	}

	previewURL, ok := s.voices.previews.get(voiceID)
	if !ok {
		resolved, err := s.resolvePreviewURL(r.Context(), voiceID)
		if err != nil {
			s.writeVoiceError(w, err)
			return
		}
		if resolved == "" {
			writeError(w, http.StatusNotFound, "that voice has no preview file")
			return
		}
		s.voices.previews.put(voiceID, resolved)
		previewURL = resolved
	}

	key, _, err := s.store.PickKey(r.Context())
	if err != nil {
		s.writeVoiceError(w, err)
		return
	}
	resp, err := s.previewFetch(r.Context(), key.Secret, previewURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeError(w, clientStatus(resp.StatusCode), fmt.Sprintf("cartesia returned %d for the preview", resp.StatusCode))
		return
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "audio/mpeg"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=600")
	w.WriteHeader(http.StatusOK)
	_, _ = io.CopyBuffer(w, io.LimitReader(resp.Body, previewMaxBytes), make([]byte, streamBufSize))
}

// resolvePreviewURL looks a voice up by id. Cartesia's q parameter searches by
// name, description and id, so one call answers it.
func (s *Server) resolvePreviewURL(ctx context.Context, voiceID string) (string, error) {
	query := url.Values{}
	query.Set("q", voiceID)
	query.Set("limit", "1")
	query.Add("expand[]", "preview_file_url")

	page, err := s.listVoices(ctx, query)
	if err != nil {
		return "", err
	}
	for _, voice := range page.Data {
		if voice.ID == voiceID && voice.PreviewFileURL != nil {
			return *voice.PreviewFileURL, nil
		}
	}
	return "", nil
}

// voiceQuery validates the query parameters the dashboard is allowed to pass
// through. Anything else is dropped rather than forwarded.
func voiceQuery(r *http.Request) url.Values {
	in := r.URL.Query()
	out := url.Values{}
	out.Add("expand[]", "preview_file_url")

	if q := strings.TrimSpace(in.Get("q")); q != "" {
		out.Set("q", truncate(q, 200))
	}
	if language := strings.TrimSpace(in.Get("language")); language != "" {
		out.Set("language", truncate(language, 32))
	}
	if cursor := strings.TrimSpace(in.Get("starting_after")); cursor != "" {
		out.Set("starting_after", truncate(cursor, 64))
	}
	limit := 100
	if raw := in.Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = min(parsed, voicePageMax)
		}
	}
	out.Set("limit", strconv.Itoa(limit))
	return out
}

func (s *Server) writeVoiceError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNoKeys) {
		writeError(w, http.StatusServiceUnavailable, "no active Cartesia API key: open the dashboard and add one")
		return
	}
	var upstream *cartesia.UpstreamError
	if errors.As(err, &upstream) {
		writeError(w, clientStatus(upstream.Status), upstream.Error())
		return
	}
	s.logError("list voices", err)
	writeError(w, http.StatusBadGateway, err.Error())
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
