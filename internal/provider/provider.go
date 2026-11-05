// Package provider routes an OpenAI-shaped speech request to one of the
// upstream text-to-speech providers this gateway proxies.
//
// The URL and the client credential are the gateway's own: a client sends
//
//	{"model":"cartesia/sonic-3.6", ...}   or   {"model":"elevenlabs/eleven_v3", ...}
//
// to the same /v1/audio/speech, and the model string decides who is called.
// Without a prefix the model's shape decides ("sonic-*" is Cartesia,
// "eleven_*" is ElevenLabs), so clients written before providers existed keep
// working unchanged.
package provider

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/tts"
)

// Provider is one upstream text-to-speech service.
type Provider interface {
	// Name is the canonical id used in model strings, URLs and the database.
	Name() string
	// Label is the human name the console shows.
	Label() string
	// Models lists the catalogue, newest first.
	Models() []string
	// DefaultModel is used when a model string names no model.
	DefaultModel() string
	// DefaultVoice is used when a request names no voice.
	DefaultVoice() string
	// VoicePrefixes are the literal prefixes that identify a voice id this
	// provider issues. A UUID provider has none: its shape is checked by
	// OwnerOf. Prefixes are the cheap first test that avoids a catalogue
	// lookup.
	VoicePrefixes() []string
	// Synthesize sends a plan and returns the raw response; the caller owns
	// the body.
	Synthesize(ctx Ctx, apiKey string, plan tts.Plan) (*http.Response, error)
	// ListVoices fetches one page of the voice catalogue.
	ListVoices(ctx Ctx, apiKey string, query url.Values) (VoicesPage, error)
	// FetchPreview streams a voice preview file from the provider's own host.
	FetchPreview(ctx Ctx, apiKey, rawURL string) (*http.Response, error)
	// Classify turns a non-2xx answer into a cleaned message and whether
	// trying a different key could help.
	Classify(status int, body string) (string, bool)
}

// OwnsPrefix reports whether any of prefix names the start of a voice id this
// provider issues.
func ownsPrefix(prefixes []string, value string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

// Owns reports whether the id looks like one of this provider's voices, from
// its shape or prefix alone.
func Owns(p Provider, voiceID string) bool {
	if ownsPrefix(p.VoicePrefixes(), voiceID) {
		return true
	}
	return OwnerOf(voiceID) == p.Name()
}

// VoicesPage is one page of a catalogue. Data is kept raw so every field the
// upstream returns reaches the client.
type VoicesPage struct {
	Data     []map[string]any
	HasMore  bool
	NextPage string
}

// Registry holds the providers in display order and answers model lookups.
type Registry struct {
	list []Provider
}

// NewRegistry builds the registry. Order is display order: the first provider
// is the default for requests that name no model.
func NewRegistry(list ...Provider) *Registry { return &Registry{list: list} }

// Providers returns the registered providers in order.
func (r *Registry) Providers() []Provider {
	if r == nil {
		return nil
	}
	return r.list
}

// Get returns a provider by canonical name.
func (r *Registry) Get(name string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	for _, p := range r.list {
		if p.Name() == name {
			return p, true
		}
	}
	return nil, false
}

// Default returns the first provider, or nil when the registry is empty.
func (r *Registry) Default() Provider {
	if r == nil || len(r.list) == 0 {
		return nil
	}
	return r.list[0]
}

// Names returns the canonical ids in display order.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.list))
	for _, p := range r.list {
		names = append(names, p.Name())
	}
	return names
}

var modelShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\-]{0,95}$`)

// Model is a parsed model string.
type Model struct {
	// Provider is the canonical provider name, or "" when the string did not
	// name one and the caller must pick (see Resolve).
	Provider string
	// Name is the model id the provider understands.
	Name string
	// Voice is the third segment of "provider/model/voice", or "".
	Voice string
	// Explicit reports whether the provider came from the string itself.
	Explicit bool
}

// alias maps the short provider names clients already use to canonical ones.
var alias = map[string]string{
	"cartesia":   "cartesia",
	"elevenlabs": "elevenlabs",
	"eleven":     "elevenlabs",
	"el":         "elevenlabs",
}

// Prefixes lists every provider prefix a client may write, longest first so a
// greedy match in documentation and the console mirrors this parser.
func Prefixes() []string { return []string{"cartesia/", "elevenlabs/", "eleven/", "el/"} }

// ParseModel splits a model string into provider, model and optional voice.
//
//	"cartesia/sonic-3.6"          -> cartesia, sonic-3.6, "",    explicit
//	"elevenlabs/eleven_v3/Rachel" -> elevenlabs, eleven_v3, Rachel
//	"sonic-3.6"                   -> cartesia, sonic-3.6, "",    by shape
//	""                            -> "", "", ""
//
// Anything that names neither a known provider nor a known shape is returned
// with Provider == "": the caller decides (Resolve falls back), because a
// model the gateway does not recognise is still better answered than refused.
func ParseModel(model string) (Model, error) {
	raw := strings.TrimSpace(model)
	if raw == "" {
		return Model{}, nil
	}
	if strings.ContainsAny(raw, " \t\n") {
		return Model{}, fmt.Errorf("model %q must not contain spaces", raw)
	}

	prefix, rest := "", raw
	if idx := strings.IndexByte(raw, '/'); idx >= 0 {
		prefix, rest = strings.ToLower(raw[:idx]), raw[idx+1:]
	}
	if canonical, ok := alias[prefix]; ok {
		parts := strings.SplitN(rest, "/", 2)
		name := strings.TrimSpace(parts[0])
		if name == "" {
			return Model{}, fmt.Errorf("model %q has no model id after the provider prefix", raw)
		}
		if !modelShape.MatchString(name) {
			return Model{}, fmt.Errorf("model %q is not a valid model id", name)
		}
		m := Model{Provider: canonical, Name: name, Explicit: true}
		if len(parts) == 2 {
			voice := strings.TrimSpace(parts[1])
			if voice == "" {
				return Model{}, fmt.Errorf("model %q ends with an empty voice segment", raw)
			}
			m.Voice = voice
		}
		return m, nil
	}

	// No provider prefix. The shape of the name decides when it is obvious,
	// and the caller falls back to whatever has a key when it is not.
	if !modelShape.MatchString(raw) {
		return Model{}, fmt.Errorf("model %q is not a valid model id; use provider/model, e.g. cartesia/sonic-3.6", raw)
	}
	return Model{Name: raw}, nil
}

// ShapeMatch returns the provider implied by a bare model name, if any.
func (r *Registry) ShapeMatch(name string) (Provider, bool) {
	lower := strings.ToLower(name)
	switch {
	case strings.HasPrefix(lower, "sonic"):
		if p, ok := r.Get("cartesia"); ok {
			return p, true
		}
	case strings.HasPrefix(lower, "eleven_"):
		if p, ok := r.Get("elevenlabs"); ok {
			return p, true
		}
	}
	return nil, false
}

// Known reports whether the provider documents the model. Membership is a
// hint, never a gate: providers ship models faster than this gateway's
// catalogue, and their own rejection is more accurate.
func Known(p Provider, name string) bool {
	for _, m := range p.Models() {
		if strings.EqualFold(m, name) {
			return true
		}
	}
	return false
}

// Resolve picks the provider for a parsed model.
//
//   - an explicit prefix wins;
//   - otherwise the known-model list of each provider, then the name's shape;
//   - otherwise the first provider, whose default model is used.
//
// The returned provider is never nil unless the registry is empty.
func (r *Registry) Resolve(m Model) (Provider, string, error) {
	if len(r.list) == 0 {
		return nil, "", fmt.Errorf("no speech provider configured")
	}
	if m.Explicit {
		p, ok := r.Get(m.Provider)
		if !ok {
			return nil, "", fmt.Errorf("provider %q is not configured", m.Provider)
		}
		return p, m.Name, nil
	}
	if m.Name == "" {
		p := r.Default()
		return p, p.DefaultModel(), nil
	}
	for _, p := range r.list {
		if Known(p, m.Name) {
			return p, m.Name, nil
		}
	}
	if p, ok := r.ShapeMatch(m.Name); ok {
		return p, m.Name, nil
	}
	// Unknown name: hand it to the first provider's default model rather than
	// guess at a vendor, so history shows one fallback instead of a 400.
	return r.Default(), m.Name, nil
}

// Catalog returns every model as "provider/model", grouped in provider order.
// This is what GET /v1/models and the console's picker list.
func (r *Registry) Catalog() []string {
	out := make([]string, 0, 16)
	for _, p := range r.list {
		for _, m := range p.Models() {
			out = append(out, p.Name()+"/"+m)
		}
	}
	return out
}

// SplitCatalogName returns the provider and model of a "provider/model" entry
// from Catalog.
func SplitCatalogName(entry string) (string, string) {
	if idx := strings.IndexByte(entry, '/'); idx > 0 {
		return entry[:idx], entry[idx+1:]
	}
	return "", entry
}

// OwnerOf reports which provider a voice id looks like it belongs to, from its
// shape alone: Cartesia ids are UUIDs, ElevenLabs ids are 20 alphanumeric
// characters. It is a hint used to pick a preview client, never a validation.
func OwnerOf(voiceID string) string {
	if uuidShape.MatchString(voiceID) {
		return "cartesia"
	}
	if elevenShape.MatchString(voiceID) {
		return "elevenlabs"
	}
	return ""
}

var (
	uuidShape   = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	elevenShape = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)
)
