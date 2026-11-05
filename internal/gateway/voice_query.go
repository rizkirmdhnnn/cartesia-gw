package gateway

import (
	"net/url"
	"strings"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/provider"
)

// queryValues is the query source the validators read. It has no provider
// type in it so the validators stay pure functions.
type queryValues url.Values

// For the dashboard and the API alike, the accepted filters are the ones
// both providers document; anything else is dropped rather than forwarded.

func (q queryValues) Get(key string) string { return url.Values(q).Get(key) }

func (q queryValues) Has(key string) bool { return url.Values(q).Has(key) }

// voiceQueryOptions is what the console sends, after validation.
type voiceQueryOptions struct {
	Search   string
	Language string
	Gender   string
	Cursor   string
	Limit    int
}

func parseVoiceQuery(q queryValues) voiceQueryOptions {
	options := voiceQueryOptions{
		Search:   truncate(strings.TrimSpace(q.Get("q")), 200),
		Language: truncate(strings.TrimSpace(q.Get("language")), 32),
		Gender:   strings.ToLower(strings.TrimSpace(q.Get("gender"))),
		Cursor:   truncate(strings.TrimSpace(q.Get("starting_after")), 200),
		Limit:    0,
	}
	if parsed, err := parseLimit(q.Get("limit")); err == nil && parsed > 0 {
		options.Limit = parsed
	}
	return options
}

// elevenLabsPageSize clamps a page request to what /v2/voices accepts.
func elevenLabsPageSize(requested int) int {
	const fallback = 30
	if requested <= 0 {
		return fallback
	}
	return min(requested, 100)
}

// voiceQueryFor builds the upstream query for one provider from the console
// options. Cartesia takes q/language/gender, ElevenLabs takes
// search/language[]/gender with page tokens.
func voiceQueryFor(p provider.Provider, q queryValues) url.Values {
	options := parseVoiceQuery(q)
	out := url.Values{}

	if p.Name() == "elevenlabs" {
		if options.Search != "" {
			out.Set("search", options.Search)
		}
		if options.Language != "" {
			// v2/voices filters with language as a repeated parameter; a single
			// value is the same shape with one entry.
			out.Add("language", truncate(options.Language, 8))
		}
		if options.Gender != "" {
			out.Set("gender", truncate(options.Gender, 16))
		}
		if options.Cursor != "" {
			out.Set("next_page_token", options.Cursor)
		}
		out.Set("page_size", itoa(elevenLabsPageSize(options.Limit)))
		return out
	}

	out.Add("expand[]", "preview_file_url")
	if options.Search != "" {
		out.Set("q", options.Search)
	}
	if options.Language != "" {
		out.Set("language", options.Language)
	}
	if isCartesiaGender(options.Gender) {
		out.Set("gender", options.Gender)
	}
	if options.Cursor != "" {
		out.Set("starting_after", truncate(options.Cursor, 64))
	}
	limit := voicePageMax
	if options.Limit > 0 {
		limit = min(options.Limit, voicePageMax)
	}
	out.Set("limit", itoa(limit))
	return out
}
