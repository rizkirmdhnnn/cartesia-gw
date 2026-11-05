package gateway

import (
	"net/http"
	"strings"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/provider"
)

// browserProvider is a provider's catalogue shaped for the console: the model
// picker groups by provider, and the voice library filters by one.
type browserProvider struct {
	Name         string   `json:"name"`
	Label        string   `json:"label"`
	Models       []string `json:"models"`
	DefaultModel string   `json:"defaultModel"`
	DefaultVoice string   `json:"defaultVoice"`
	HasKeys      bool     `json:"hasKeys"`
	// OwnerOf is the set of id shapes this provider is recognised by, so the
	// console can tag a voice without a round trip.
	VoiceShape string `json:"voiceShape"`
}

// providersForConsole describes every registered provider.
func (s *Server) providersForConsole(r *http.Request) []browserProvider {
	out := make([]browserProvider, 0, len(s.providers.Providers()))
	for _, p := range s.providers.Providers() {
		keys, err := s.store.KeysFor(r.Context(), p.Name())
		hasKeys := err == nil && len(keys) > 0
		voice, err := s.store.DefaultVoice(r.Context(), p.Name())
		if err != nil {
			s.logError("read default voice", err)
		}
		models := make([]string, 0, len(p.Models()))
		for _, model := range p.Models() {
			models = append(models, p.Name()+"/"+model)
		}
		out = append(out, browserProvider{
			Name:         p.Name(),
			Label:        p.Label(),
			Models:       models,
			DefaultModel: p.Name() + "/" + p.DefaultModel(),
			DefaultVoice: voice,
			HasKeys:      hasKeys,
			VoiceShape:   voiceShapeOf(p.Name()),
		})
	}
	return out
}

// voiceShapeOf names the id shape a provider uses, for the console's labels.
func voiceShapeOf(name string) string {
	switch name {
	case "cartesia":
		return "uuid"
	case "elevenlabs":
		return "eleven"
	}
	return ""
}

// voiceProviderPath reads ?provider= for the catalogue endpoints and returns
// the canonical name, or "" for all providers.
func voiceProviderPath(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	if canonical, ok := aliasProvider(value); ok {
		return canonical
	}
	return value
}

var _ = provider.OwnerOf
