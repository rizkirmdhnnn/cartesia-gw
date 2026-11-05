package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/cartesia"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/elevenlabs"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/provider"
)

// resolveVoice turns whatever the client put in "voice" into an id the chosen
// provider understands, without adding a request to the synthesis path.
//
// The accepted inputs, in order of how explicitly they say what they mean:
//
//	"provider/name"  the provider segment is stripped; the model string is
//	                 what actually routes the request
//	an id            used as-is when it has the provider's id shape, or when
//	                 the name→id index already knows it
//	a voice name     looked up in the index; if the index is cold, the name is
//	                 resolved by fetching the catalogue once, which later
//	                 requests answer from cache
//
// Nothing here fails a request. A UUID sent to ElevenLabs — the common case,
// since clients written before this gateway had a second provider keep sending
// their Cartesia voice — falls back to the configured default voice, and the
// substitution is recorded in history.
func (s *Server) resolveVoice(ctx context.Context, p provider.Provider, requested, fallback string) (string, string) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return fallback, ""
	}
	if idx := strings.IndexByte(requested, '/'); idx > 0 {
		if _, known := aliasProvider(strings.ToLower(requested[:idx])); known {
			requested = strings.TrimSpace(requested[idx+1:])
		}
	}
	if requested == "" {
		return fallback, ""
	}

	// An id of this provider's own shape needs no lookup at all.
	if provider.Owns(p, requested) {
		return requested, ""
	}

	if id, ok := s.voiceByName(p, requested); ok {
		if id != requested {
			return id, fmt.Sprintf("voice:name→id %s", id)
		}
		return id, ""
	}

	// Not a name the index knows. A value with no recognisable shape is sent
	// as-is: the provider's own answer names the voice more accurately than a
	// guess made here, and an elevenlabs-style id can belong to a voice the
	// first catalogue page did not list.
	if provider.OwnerOf(requested) == "" {
		return requested, ""
	}
	if fallback == "" {
		// A foreign id and no configured default: send it anyway rather than
		// inventing a voice nobody asked for.
		return requested, "voice:unverified"
	}
	return fallback, "voice:→default " + fallback
}

// aliasProvider reports whether a prefix names a provider, and its canonical
// name.
func aliasProvider(prefix string) (string, bool) {
	switch prefix {
	case "cartesia":
		return "cartesia", true
	case "elevenlabs", "el", "eleven":
		return "elevenlabs", true
	}
	return "", false
}

// voiceByName answers a name from the cached index, and only when the index is
// cold does it fetch the catalogue. That keeps the synthesis path on the
// network only for the first request that uses a name.
func (s *Server) voiceByName(p provider.Provider, requested string) (string, bool) {
	key := normalizeVoiceKey(requested)
	if cached, ok := s.voices.index.get(p.Name()); ok {
		if id, found := cached.names[key]; found {
			return id, true
		}
		if cached.ids[key] {
			return requested, true
		}
		return "", false
	}
	// Cold index: this is the one request that pays for the catalogue. A
	// failure leaves the index unset and the caller keeps the id as-is.
	page, err := s.catalogPage(context.Background(), p)
	if err != nil {
		return "", false
	}
	index := buildVoiceIndex(page)
	s.voices.index.put(p.Name(), index)
	if id, found := index.names[key]; found {
		return id, true
	}
	if index.ids[key] {
		return requested, true
	}
	return "", false
}

// buildVoiceIndex turns a catalogue page into the id set and the lower-cased
// name→id map.
func buildVoiceIndex(page provider.VoicesPage) voiceNameResult {
	result := voiceNameResult{
		ids:   make(map[string]bool, len(page.Data)),
		names: make(map[string]string, len(page.Data)),
	}
	for _, ref := range voiceRefs(page) {
		result.ids[normalizeVoiceKey(ref.ID)] = true
		if key := normalizeVoiceKey(ref.Name); key != "" {
			if _, taken := result.names[key]; !taken {
				result.names[key] = ref.ID
			}
		}
	}
	return result
}

// voiceEntry is the part of a catalogue entry this process reads, whichever
// provider produced it.
type voiceEntry struct {
	ID      string
	Name    string
	Preview string
}

// voiceRefs projects a page onto voiceEntry. Cartesia uses "id" and
// "preview_file_url"; ElevenLabs uses "voice_id" and "preview_url".
func voiceRefs(page provider.VoicesPage) []voiceEntry {
	refs := make([]voiceEntry, 0, len(page.Data))
	for _, voice := range page.Data {
		entry := voiceEntry{
			ID:      firstString(voice, "id", "voice_id"),
			Name:    firstString(voice, "name"),
			Preview: firstString(voice, "preview_file_url", "preview_url"),
		}
		if entry.ID == "" {
			continue
		}
		refs = append(refs, entry)
	}
	return refs
}

func firstString(voice map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := voice[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

// lookupPreviewURL resolves a voice's preview file URL from the catalogue.
func (s *Server) lookupPreviewURL(ctx context.Context, p provider.Provider, voiceID string) (string, error) {
	page, err := s.catalogPage(ctx, p)
	if err != nil {
		return "", err
	}
	for _, ref := range voiceRefs(page) {
		if strings.EqualFold(ref.ID, voiceID) {
			return ref.Preview, nil
		}
	}
	return "", nil
}

// catalogPage reads the provider's catalogue through the shared cache.
func (s *Server) catalogPage(ctx context.Context, p provider.Provider) (provider.VoicesPage, error) {
	return s.listProviderVoices(ctx, p, url.Values{})
}

// listProviderVoices answers from cache when it can, otherwise calls the
// provider once. It borrows a key through the same rotation picker the speech
// path uses, so a key parked in cooldown is not chosen for a lookup either.
func (s *Server) listProviderVoices(ctx context.Context, p provider.Provider, query url.Values) (provider.VoicesPage, error) {
	cacheKey := p.Name() + "?" + query.Encode()
	if page, ok := s.voices.pages.get(cacheKey); ok {
		return page, nil
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		key, _, err := s.store.PickKeyFor(ctx, p.Name())
		if err != nil {
			return provider.VoicesPage{}, err
		}
		page, err := p.ListVoices(ctx, key.Secret, query)
		if err == nil {
			s.voices.pages.put(cacheKey, page)
			return page, nil
		}
		lastErr = err
		if status, ok := upstreamStatus(err); ok {
			if _, retry := p.Classify(status, err.Error()); retry {
				_ = s.store.MarkFailure(ctx, key.ID, status, err.Error(), 0)
				continue
			}
		}
		return provider.VoicesPage{}, err
	}
	return provider.VoicesPage{}, lastErr
}

// upstreamStatus reads the status out of a provider's upstream error.
func upstreamStatus(err error) (int, bool) {
	var eleven *elevenlabs.UpstreamError
	if errors.As(err, &eleven) {
		return eleven.Status, true
	}
	var cartesiaErr *cartesia.UpstreamError
	if errors.As(err, &cartesiaErr) {
		return cartesiaErr.Status, true
	}
	return 0, false
}
