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

	"github.com/rizkirmdhnnn/cartesia-gw/internal/provider"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

const (
	voiceCacheTTL   = 10 * time.Minute
	voicePageMax    = 100
	previewMaxBytes = 16 << 20
)

// genders is the closed set Cartesia documents for the voice filter.
var genders = []string{"masculine", "feminine", "gender_neutral"}

func isCartesiaGender(gender string) bool {
	for _, known := range genders {
		if gender == known {
			return true
		}
	}
	return false
}

func parseLimit(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, errors.New("empty")
	}
	return strconv.Atoi(raw)
}

func itoa(value int) string { return strconv.Itoa(value) }

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

// voiceCache memoises the voice catalogue, the resolved preview file URLs and
// the name→id index.
type voiceCache struct {
	pages    *ttlCache[provider.VoicesPage]
	previews *ttlCache[string]
	index    *ttlCache[voiceNameResult]
}

func newVoiceCache(max int, ttl time.Duration) *voiceCache {
	return &voiceCache{
		pages:    newTTLCache[provider.VoicesPage](max, ttl),
		previews: newTTLCache[string](max*4, ttl),
		index:    newTTLCache[voiceNameResult](max*2, ttl),
	}
}

// normalizeVoiceKey is the case- and space-insensitive form voice names and
// ids are compared in.
func normalizeVoiceKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// handleVoices serves GET /v1/audio/voices.
//
// One endpoint lists every provider's voices, each entry tagged with its
// provider, so a client does not have to know which vendors the gateway
// fronts. ?provider= narrows it to one. When a provider fails (a revoked key,
// an outage) the response stays 200 with the voices that did load plus a
// warning, because one broken vendor should not hide the other.
func (s *Server) handleVoices(w http.ResponseWriter, r *http.Request) {
	targets, err := s.voiceTargets(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	data := make([]map[string]any, 0, 100)
	warnings := make([]map[string]string, 0)
	hasMore := false
	nextPage := ""
	for _, p := range targets {
		page, err := s.listProviderVoices(r.Context(), p, voiceQueryFor(p, queryValues(r.URL.Query())))
		if err != nil {
			warnings = append(warnings, map[string]string{"provider": p.Name(), "error": voiceErrorText(err)})
			continue
		}
		for _, voice := range page.Data {
			voice["provider"] = p.Name()
			data = append(data, voice)
		}
		if page.HasMore {
			hasMore = true
			if nextPage == "" {
				nextPage = page.NextPage
			}
		}
	}
	if len(targets) > 0 && len(warnings) == len(targets) {
		s.writeVoiceError(w, errors.New(warnings[0]["error"]))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"object":    "list",
		"data":      data,
		"has_more":  hasMore,
		"next_page": nextPageOrNil(nextPage),
		"providers": s.providers.Names(),
		"warnings":  warnings,
	})
}

func nextPageOrNil(page string) any {
	if page == "" {
		return nil
	}
	return page
}

// voiceTargets decides which providers a listing covers.
func (s *Server) voiceTargets(r *http.Request) ([]provider.Provider, error) {
	raw := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
	if raw == "" {
		return s.providers.Providers(), nil
	}
	if canonical, ok := aliasProvider(raw); ok {
		raw = canonical
	}
	if p, found := s.providers.Get(raw); found {
		return []provider.Provider{p}, nil
	}
	return nil, fmt.Errorf("provider %q is not configured; known: %s", raw, strings.Join(s.providers.Names(), ", "))
}

// handleVoicesAPI is the dashboard's view of the same data.
func (s *Server) handleVoicesAPI(w http.ResponseWriter, r *http.Request) {
	s.handleVoices(w, r)
}

// handleVoicePreview streams a voice's preview file. The URL comes from the
// provider and is fetched server-side because it requires the same credential
// the browser does not have.
func (s *Server) handleVoicePreview(w http.ResponseWriter, r *http.Request) {
	voiceID := strings.TrimSpace(r.PathValue("id"))
	if voiceID == "" {
		writeError(w, http.StatusBadRequest, "voice id is required")
		return
	}

	p, err := s.previewProvider(r, voiceID)
	if err != nil {
		s.writeVoiceError(w, err)
		return
	}

	cacheKey := p.Name() + "|" + voiceID
	previewURL, ok := s.voices.previews.get(cacheKey)
	if !ok {
		resolved, err := s.previewURLFor(r.Context(), p, voiceID)
		if err != nil {
			s.writeVoiceError(w, err)
			return
		}
		if resolved == "" {
			writeError(w, http.StatusNotFound, "that voice has no preview file")
			return
		}
		s.voices.previews.put(cacheKey, resolved)
		previewURL = resolved
	}

	key, _, err := s.store.PickKeyFor(r.Context(), p.Name())
	if err != nil {
		s.writeVoiceError(w, err)
		return
	}
	fetcher, ok := s.providerByName(p.Name())
	if !ok {
		fetcher = p
	}
	resp, err := fetcher.FetchPreview(r.Context(), key.Secret, previewURL)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeError(w, clientStatus(resp.StatusCode), fmt.Sprintf("%s returned %d for the preview", p.Name(), resp.StatusCode))
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

// previewProvider picks the provider whose host serves a voice preview: an
// explicit ?provider= wins, then the id's shape, then the last provider with
// a key.
func (s *Server) previewProvider(r *http.Request, voiceID string) (provider.Provider, error) {
	raw := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
	if raw != "" {
		if alias, ok := aliasProvider(raw); ok {
			raw = alias
		}
		if p, found := s.providers.Get(raw); found {
			return p, nil
		}
		return nil, fmt.Errorf("provider %q is not configured", raw)
	}
	if owner := provider.OwnerOf(voiceID); owner != "" {
		if p, found := s.providers.Get(owner); found {
			return p, nil
		}
	}
	var chosen provider.Provider
	for _, p := range s.providers.Providers() {
		keys, err := s.store.KeysFor(r.Context(), p.Name())
		if err == nil && len(keys) > 0 {
			chosen = p
		}
	}
	if chosen == nil {
		return nil, store.ErrNoKeys
	}
	return chosen, nil
}

func (s *Server) writeVoiceError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNoKeys) {
		writeError(w, http.StatusServiceUnavailable, "no active API key: open the dashboard and add one")
		return
	}
	if status, ok := upstreamStatus(err); ok {
		writeError(w, clientStatus(status), err.Error())
		return
	}
	s.logError("list voices", err)
	writeError(w, http.StatusBadGateway, err.Error())
}

func voiceErrorText(err error) string {
	if errors.Is(err, store.ErrNoKeys) {
		return "no active API key for this provider"
	}
	return err.Error()
}

// resolveProviderPreviewURL resolves one voice's preview URL without fetching
// it. Exported for the preview tests.
func (s *Server) resolveProviderPreviewURL(ctx context.Context, p provider.Provider, voiceID string) (string, error) {
	return s.previewURLFor(ctx, p, voiceID)
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}

var _ = url.Values{}
