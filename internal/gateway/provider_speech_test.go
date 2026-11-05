package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/cartesia"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/config"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/elevenlabs"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/provider"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

// voicePage is what a provider's catalogue endpoint answers in these tests.
type voicePage struct {
	calls []recorded
	body  string
}

// multiServer wires both providers: Cartesia at one test server and
// ElevenLabs at another, each recording what it received.
func multiServer(t *testing.T) (*Server, *store.Store, *httptest.Server, *fakeUpstream, *fakeUpstream) {
	t.Helper()

	cartesiaUpstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusOK, "CARTESIA-AUDIO", map[string]string{"Content-Type": "audio/mpeg"}
	}}
	elevenUpstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusOK, "ELEVEN-AUDIO", map[string]string{"Content-Type": "audio/mpeg"}
	}}

	cartesiaHTTP := httptest.NewServer(cartesiaUpstream.handler())
	t.Cleanup(cartesiaHTTP.Close)
	elevenHTTP := httptest.NewServer(elevenUpstream.handler())
	t.Cleanup(elevenHTTP.Close)

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		Addr:              "127.0.0.1:0",
		CartesiaBaseURL:   cartesiaHTTP.URL,
		CartesiaVersion:   config.DefaultCartesiaVersion,
		ElevenLabsBaseURL: elevenHTTP.URL,
		Retention:         24 * time.Hour,
		RequestTimeout:    30 * time.Second,
		Version:           "test",
	}
	registry := provider.NewRegistry(
		cartesia.NewService(cartesia.New(cartesiaHTTP.URL, cfg.CartesiaVersion, cfg.RequestTimeout)),
		elevenlabs.NewService(elevenlabs.New(elevenHTTP.URL, cfg.RequestTimeout)),
	)
	server, err := New(cfg, st, registry, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("gateway.New() error = %v", err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return server, st, httpServer, cartesiaUpstream, elevenUpstream
}

func (f *fakeUpstream) synthesisCalls() []recorded {
	out := make([]recorded, 0, 4)
	for _, call := range f.calls() {
		if call.Path != "/voices" && call.Path != "/v2/voices" {
			out = append(out, call)
		}
	}
	return out
}

// The model string is the only routing input: same URL, same client key, two
// different upstreams.
func TestModelStringSelectsTheProvider(t *testing.T) {
	_, st, server, cartesiaUpstream, elevenUpstream := multiServer(t)
	if _, err := st.CreateKeyFor(context.Background(), "cartesia", "main", "sk_car_main"); err != nil {
		t.Fatalf("CreateKeyFor() error = %v", err)
	}
	if _, err := st.CreateKeyFor(context.Background(), "elevenlabs", "main", "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("CreateKeyFor() error = %v", err)
	}

	post := func(body string) (*http.Response, string) {
		resp, err := http.Post(server.URL+"/v1/audio/speech", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST error = %v", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp, string(raw)
	}

	resp, body := post(`{"model":"cartesia/sonic-3.6","input":"halo","voice":"v-1"}`)
	if resp.StatusCode != http.StatusOK || body != "CARTESIA-AUDIO" {
		t.Fatalf("cartesia = %d %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-Gateway-Provider"); got != "cartesia" {
		t.Errorf("X-Gateway-Provider = %q", got)
	}
	calls := cartesiaUpstream.synthesisCalls()
	if len(calls) != 1 || calls[0].Path != "/tts/bytes" {
		t.Fatalf("cartesia calls = %+v", calls)
	}
	if calls[0].Payload.ModelID != "sonic-3.6" {
		t.Errorf("cartesia model_id = %q", calls[0].Payload.ModelID)
	}

	resp, body = post(`{"model":"elevenlabs/eleven_v3","input":"halo","voice":"JBFqnCBsd6RMkjVDRZzb"}`)
	if resp.StatusCode != http.StatusOK || body != "ELEVEN-AUDIO" {
		t.Fatalf("elevenlabs = %d %q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-Gateway-Provider"); got != "elevenlabs" {
		t.Errorf("X-Gateway-Provider = %q", got)
	}
	calls = elevenUpstream.synthesisCalls()
	if len(calls) != 1 {
		t.Fatalf("elevenlabs calls = %+v", calls)
	}
	call := calls[0]
	if call.Path != "/v1/text-to-speech/JBFqnCBsd6RMkjVDRZzb/stream" {
		t.Errorf("path = %q, want the voice in the path", call.Path)
	}
	if got := call.Header.Get("xi-api-key"); got != "0123456789abcdef0123456789abcdef" {
		t.Errorf("xi-api-key = %q", got)
	}
	if got := call.Query.Get("output_format"); got != "mp3_44100_128" {
		t.Errorf("output_format = %q", got)
	}
	var payload struct {
		Text    string `json:"text"`
		ModelID string `json:"model_id"`
	}
	if err := json.Unmarshal([]byte(call.Raw), &payload); err != nil {
		t.Fatalf("elevenlabs body is not JSON: %v", err)
	}
	if payload.Text != "halo" || payload.ModelID != "eleven_v3" {
		t.Errorf("elevenlabs body = %+v", payload)
	}
}

// A client that predates the second provider sends a bare model name.
func TestBareModelNameStillRoutesByShape(t *testing.T) {
	_, st, server, cartesiaUpstream, elevenUpstream := multiServer(t)
	addProviderKey(t, st, "cartesia", "main", "sk_car_main")
	addProviderKey(t, st, "elevenlabs", "main", "0123456789abcdef0123456789abcdef")

	resp := postSpeech(t, server, `{"model":"sonic-3","input":"hi","voice":"v-1"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sonic-3 status = %d", resp.StatusCode)
	}
	resp = postSpeech(t, server, `{"model":"eleven_v3","input":"hi","voice":"JBFqnCBsd6RMkjVDRZzb"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("eleven_v3 status = %d", resp.StatusCode)
	}
	if len(cartesiaUpstream.synthesisCalls()) != 1 || len(elevenUpstream.synthesisCalls()) != 1 {
		t.Fatalf("cartesia=%d eleven=%d syntheses", len(cartesiaUpstream.synthesisCalls()), len(elevenUpstream.synthesisCalls()))
	}
}

// A Cartesia UUID sent to ElevenLabs is replaced by the configured default
// voice instead of failing the request, and history says so.
func TestForeignVoiceFallsBackToTheDefaultVoice(t *testing.T) {
	_, st, server, _, elevenUpstream := multiServer(t)
	addProviderKey(t, st, "cartesia", "main", "sk_car_main")
	addProviderKey(t, st, "elevenlabs", "main", "0123456789abcdef0123456789abcdef")
	if err := st.SetDefaultVoice(context.Background(), "elevenlabs", "JBFqnCBsd6RMkjVDRZzb"); err != nil {
		t.Fatalf("SetDefaultVoice() error = %v", err)
	}

	resp := postSpeech(t, server,
		`{"model":"elevenlabs/eleven_v3","input":"halo","voice":"5c5e3a1c-1b2d-4e5f-8a9b-0c1d2e3f4a5b"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the request served with the default voice", resp.StatusCode)
	}
	calls := elevenUpstream.synthesisCalls()
	if len(calls) != 1 {
		t.Fatalf("synthesis calls = %d", len(calls))
	}
	if calls[0].Path != "/v1/text-to-speech/JBFqnCBsd6RMkjVDRZzb/stream" {
		t.Errorf("path = %q, want the default voice", calls[0].Path)
	}
	rows, _, err := st.ListRequests(context.Background(), 10, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("history rows = %d (err %v)", len(rows), err)
	}
	if rows[0].VoiceID != "JBFqnCBsd6RMkjVDRZzb" || rows[0].Provider != "elevenlabs" {
		t.Errorf("row = %+v, want the substituted voice and provider", rows[0])
	}
	if len(rows[0].Adjustments) == 0 {
		t.Error("the substitution was not recorded")
	}
}

// A provider named with no key of its own is a setup problem, and says which
// provider to add.
func TestProviderWithoutAKeyAnswers424(t *testing.T) {
	_, st, server, _, _ := multiServer(t)
	addProviderKey(t, st, "cartesia", "main", "sk_car_main")

	resp := postSpeech(t, server, `{"model":"elevenlabs/eleven_v3","input":"hi","voice":"JBFqnCBsd6RMkjVDRZzb"}`)
	if resp.StatusCode != http.StatusFailedDependency {
		t.Fatalf("status = %d, want 424", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ElevenLabs") || !strings.Contains(string(body), "Upstream keys") {
		t.Errorf("body = %s, want it to name the provider and where to add the key", body)
	}
}

// /v1/models lists every model prefixed with its provider.
func TestModelsArePrefixed(t *testing.T) {
	_, _, server, _, _ := multiServer(t)
	resp, err := http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET /v1/models error = %v", err)
	}
	defer resp.Body.Close()
	var page struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode error = %v", err)
	}
	seen := map[string]string{}
	for _, model := range page.Data {
		seen[model.ID] = model.OwnedBy
	}
	for _, id := range []string{"cartesia/sonic-3.6", "elevenlabs/eleven_v3"} {
		if _, ok := seen[id]; !ok {
			t.Errorf("model %q is missing from /v1/models", id)
		}
	}
	if seen["cartesia/sonic-3.6"] != "cartesia" {
		t.Errorf("owned_by = %q", seen["cartesia/sonic-3.6"])
	}
}

// One listing carries both providers, each entry tagged, and ?provider=
// narrows it.
func TestVoicesMergeProviders(t *testing.T) {
	cartesiaUpstream := &fakeUpstream{answer: func(call recorded) (int, string, map[string]string) {
		if call.Path == "/voices" {
			return http.StatusOK, `{"data":[{"id":"5c5e3a1c-1b2d-4e5f-8a9b-0c1d2e3f4a5b","name":"Aria"}],"has_more":false}`, nil
		}
		return http.StatusOK, "AUDIO", nil
	}}
	elevenUpstream := &fakeUpstream{answer: func(call recorded) (int, string, map[string]string) {
		if call.Path == "/v2/voices" {
			return http.StatusOK, `{"voices":[{"voice_id":"JBFqnCBsd6RMkjVDRZzb","name":"Rachel"}],"has_more":false}`, nil
		}
		return http.StatusOK, "AUDIO", nil
	}}

	cartesiaHTTP := httptest.NewServer(cartesiaUpstream.handler())
	t.Cleanup(cartesiaHTTP.Close)
	elevenHTTP := httptest.NewServer(elevenUpstream.handler())
	t.Cleanup(elevenHTTP.Close)

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	addProviderKey(t, st, "cartesia", "main", "sk_car_main")
	addProviderKey(t, st, "elevenlabs", "main", "0123456789abcdef0123456789abcdef")

	cfg := &config.Config{
		CartesiaBaseURL: cartesiaHTTP.URL, CartesiaVersion: config.DefaultCartesiaVersion,
		ElevenLabsBaseURL: elevenHTTP.URL, Retention: time.Hour, RequestTimeout: 5 * time.Second, Version: "test",
	}
	registry := provider.NewRegistry(
		cartesia.NewService(cartesia.New(cartesiaHTTP.URL, cfg.CartesiaVersion, cfg.RequestTimeout)),
		elevenlabs.NewService(elevenlabs.New(elevenHTTP.URL, cfg.RequestTimeout)),
	)
	server, err := New(cfg, st, registry, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("gateway.New() error = %v", err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	type page struct {
		Data []struct {
			ID       string `json:"id"`
			VoiceID  string `json:"voice_id"`
			Provider string `json:"provider"`
		} `json:"data"`
	}
	read := func(path string) page {
		resp, err := http.Get(httpServer.URL + path)
		if err != nil {
			t.Fatalf("GET %s error = %v", path, err)
		}
		defer resp.Body.Close()
		var decoded page
		if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
			t.Fatalf("decode %s error = %v", path, err)
		}
		return decoded
	}

	all := read("/v1/audio/voices")
	if len(all.Data) != 2 {
		t.Fatalf("merged voices = %d, want 2", len(all.Data))
	}
	providers := map[string]bool{}
	for _, voice := range all.Data {
		providers[voice.Provider] = true
	}
	if !providers["cartesia"] || !providers["elevenlabs"] {
		t.Errorf("providers = %v, want both", providers)
	}

	only := read("/v1/audio/voices?provider=el")
	if len(only.Data) != 1 || only.Data[0].Provider != "elevenlabs" {
		t.Errorf("?provider=el = %+v", only.Data)
	}
}

// A provider that fails does not hide the other one.
func TestVoicesPartialFailureStays200(t *testing.T) {
	cartesiaUpstream := &fakeUpstream{answer: func(call recorded) (int, string, map[string]string) {
		if call.Path == "/voices" {
			return http.StatusOK, `{"data":[{"id":"5c5e3a1c-1b2d-4e5f-8a9b-0c1d2e3f4a5b","name":"Aria"}],"has_more":false}`, nil
		}
		return http.StatusOK, "AUDIO", nil
	}}
	elevenUpstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusUnauthorized, `{"detail":{"status":"invalid_api_key"}}`, nil
	}}

	cartesiaHTTP := httptest.NewServer(cartesiaUpstream.handler())
	t.Cleanup(cartesiaHTTP.Close)
	elevenHTTP := httptest.NewServer(elevenUpstream.handler())
	t.Cleanup(elevenHTTP.Close)

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	addProviderKey(t, st, "cartesia", "main", "sk_car_main")
	addProviderKey(t, st, "elevenlabs", "main", "0123456789abcdef0123456789abcdef")

	cfg := &config.Config{
		CartesiaBaseURL: cartesiaHTTP.URL, CartesiaVersion: config.DefaultCartesiaVersion,
		ElevenLabsBaseURL: elevenHTTP.URL, Retention: time.Hour, RequestTimeout: 5 * time.Second, Version: "test",
	}
	registry := provider.NewRegistry(
		cartesia.NewService(cartesia.New(cartesiaHTTP.URL, cfg.CartesiaVersion, cfg.RequestTimeout)),
		elevenlabs.NewService(elevenlabs.New(elevenHTTP.URL, cfg.RequestTimeout)),
	)
	server, err := New(cfg, st, registry, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("gateway.New() error = %v", err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	resp, err := http.Get(httpServer.URL + "/v1/audio/voices")
	if err != nil {
		t.Fatalf("GET voices error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a warning", resp.StatusCode)
	}
	var page struct {
		Data     []map[string]any    `json:"data"`
		Warnings []map[string]string `json:"warnings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode error = %v", err)
	}
	if len(page.Data) != 1 {
		t.Errorf("data = %d, want the provider that answered", len(page.Data))
	}
	if len(page.Warnings) != 1 || page.Warnings[0]["provider"] != "elevenlabs" {
		t.Errorf("warnings = %+v", page.Warnings)
	}
}

// The JSON envelope names the container the audio really is, per provider.
func TestJSONEnvelopeNamesTheContainer(t *testing.T) {
	_, st, server, _, _ := multiServer(t)
	addProviderKey(t, st, "cartesia", "main", "sk_car_main")

	resp, err := http.Post(server.URL+"/v1/audio/speech?response_format=json", "application/json",
		strings.NewReader(`{"model":"cartesia/sonic-3.6","input":"hi","voice":"v-1","response_format":"wav"}`))
	if err != nil {
		t.Fatalf("POST error = %v", err)
	}
	defer resp.Body.Close()
	var envelope struct {
		Audio  string `json:"audio"`
		Format string `json:"format"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode error = %v", err)
	}
	if envelope.Format != "wav" {
		t.Errorf("format = %q, want wav", envelope.Format)
	}
	if decoded, err := base64.StdEncoding.DecodeString(envelope.Audio); err != nil || string(decoded) != "CARTESIA-AUDIO" {
		t.Errorf("audio = %q (err %v)", envelope.Audio, err)
	}
}

func addProviderKey(t *testing.T, st *store.Store, providerName, label, secret string) store.Key {
	t.Helper()
	key, err := st.CreateKeyFor(context.Background(), providerName, label, secret)
	if err != nil {
		t.Fatalf("CreateKeyFor(%s) error = %v", providerName, err)
	}
	return key
}
