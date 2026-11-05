package provider

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/tts"
)

// stubProvider is enough of a Provider for routing tests.
type stubProvider struct {
	name    string
	models  []string
	def     string
	voicees []string
}

func (s stubProvider) Name() string            { return s.name }
func (s stubProvider) Label() string           { return s.name }
func (s stubProvider) Models() []string        { return s.models }
func (s stubProvider) DefaultModel() string    { return s.def }
func (s stubProvider) DefaultVoice() string    { return "" }
func (s stubProvider) VoicePrefixes() []string { return s.voicees }
func (s stubProvider) Synthesize(Ctx, string, tts.Plan) (*http.Response, error) {
	return nil, nil
}
func (s stubProvider) ListVoices(Ctx, string, url.Values) (VoicesPage, error) {
	return VoicesPage{}, nil
}
func (s stubProvider) FetchPreview(Ctx, string, string) (*http.Response, error) {
	return nil, nil
}
func (s stubProvider) Classify(int, string) (string, bool) { return "", false }

func testRegistry() *Registry {
	return NewRegistry(
		stubProvider{name: "cartesia", models: []string{"sonic-3.6", "sonic-3"}, def: "sonic-3"},
		stubProvider{name: "elevenlabs", models: []string{"eleven_v3", "eleven_multilingual_v2"}, def: "eleven_multilingual_v2"},
	)
}

func TestParseModel(t *testing.T) {
	tests := []struct {
		in       string
		provider string
		name     string
		voice    string
		explicit bool
		wantErr  bool
	}{
		{in: "", provider: "", wantErr: false},
		{in: "cartesia/sonic-3.6", provider: "cartesia", name: "sonic-3.6", explicit: true},
		{in: "elevenlabs/eleven_v3", provider: "elevenlabs", name: "eleven_v3", explicit: true},
		{in: "el/eleven_v3", provider: "elevenlabs", name: "eleven_v3", explicit: true},
		{in: "EL/eleven_v3", provider: "elevenlabs", name: "eleven_v3", explicit: true},
		{in: "elevenlabs/eleven_v3/Rachel", provider: "elevenlabs", name: "eleven_v3", voice: "Rachel", explicit: true},
		{in: "cartesia/sonic-3/db6b0ed5", provider: "cartesia", name: "sonic-3", voice: "db6b0ed5", explicit: true},
		{in: "sonic-3.6", name: "sonic-3.6"},
		{in: "eleven_v3", name: "eleven_v3"},
		{in: "cartesia/", wantErr: true},
		{in: "cartesia/sonic-3/", wantErr: true},
		{in: "sonic 3", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseModel(tt.in)
		if (err != nil) != tt.wantErr {
			t.Fatalf("ParseModel(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if tt.wantErr {
			continue
		}
		if got.Provider != tt.provider || got.Name != tt.name || got.Voice != tt.voice || got.Explicit != tt.explicit {
			t.Errorf("ParseModel(%q) = %+v, want provider %q name %q voice %q explicit %v",
				tt.in, got, tt.provider, tt.name, tt.voice, tt.explicit)
		}
	}
}

func TestResolvePicksTheProvider(t *testing.T) {
	registry := testRegistry()
	tests := []struct {
		model    string
		provider string
		name     string
	}{
		{model: "", provider: "cartesia", name: "sonic-3"},
		{model: "sonic-3.6", provider: "cartesia", name: "sonic-3.6"},
		{model: "SonIc-3", provider: "cartesia", name: "SonIc-3"},
		{model: "eleven_v3", provider: "elevenlabs", name: "eleven_v3"},
		{model: "elevenlabs/eleven_multilingual_v2", provider: "elevenlabs", name: "eleven_multilingual_v2"},
		{model: "cartesia/sonic-latest", provider: "cartesia", name: "sonic-latest"},
		// An unknown name with no prefix stays with the default provider: one
		// fallback, visible in history, instead of a 400.
		{model: "tts-1", provider: "cartesia", name: "tts-1"},
		{model: "eleven_v9", provider: "elevenlabs", name: "eleven_v9"},
	}
	for _, tt := range tests {
		parsed, err := ParseModel(tt.model)
		if err != nil {
			t.Fatalf("ParseModel(%q) error = %v", tt.model, err)
		}
		p, name, err := registry.Resolve(parsed)
		if err != nil {
			t.Fatalf("Resolve(%q) error = %v", tt.model, err)
		}
		if p.Name() != tt.provider || name != tt.name {
			t.Errorf("Resolve(%q) = %s/%s, want %s/%s", tt.model, p.Name(), name, tt.provider, tt.name)
		}
	}
}

func TestCatalogIsPrefixed(t *testing.T) {
	got := testRegistry().Catalog()
	want := []string{
		"cartesia/sonic-3.6", "cartesia/sonic-3",
		"elevenlabs/eleven_v3", "elevenlabs/eleven_multilingual_v2",
	}
	if len(got) != len(want) {
		t.Fatalf("Catalog() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Catalog()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestOwnerOf(t *testing.T) {
	tests := map[string]string{
		"5c5e3a1c-1b2d-4e5f-8a9b-0c1d2e3f4a5b": "cartesia",
		"JBFqnCBsd6RMkjVDRZzb":                 "elevenlabs",
		"v-1":                                  "",
	}
	for id, want := range tests {
		if got := OwnerOf(id); got != want {
			t.Errorf("OwnerOf(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestSplitCatalogName(t *testing.T) {
	providerName, model := SplitCatalogName("elevenlabs/eleven_v3")
	if providerName != "elevenlabs" || model != "eleven_v3" {
		t.Errorf("SplitCatalogName() = %q, %q", providerName, model)
	}
}
