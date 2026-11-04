package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/cartesia"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/config"
	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

// fakeUpstream records what the gateway sent and answers from a script.
type fakeUpstream struct {
	mu       sync.Mutex
	requests []recorded
	answer   func(recorded) (int, string, map[string]string)
}

type recorded struct {
	Path    string
	Query   url.Values
	Header  http.Header
	Payload cartesia.SynthesisRequest
	Raw     string
}

func (f *fakeUpstream) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		call := recorded{Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(), Raw: string(body)}
		_ = json.Unmarshal(body, &call.Payload)

		f.mu.Lock()
		f.requests = append(f.requests, call)
		answer := f.answer
		f.mu.Unlock()

		status, payload, headers := answer(call)
		for key, value := range headers {
			w.Header().Set(key, value)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	})
}

func (f *fakeUpstream) calls() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.requests...)
}

func newServer(t *testing.T, upstream *fakeUpstream, token string) (*Server, *store.Store, *httptest.Server) {
	t.Helper()

	upstreamServer := httptest.NewServer(upstream.handler())
	t.Cleanup(upstreamServer.Close)

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		Addr:            "127.0.0.1:0",
		GatewayToken:    token,
		CartesiaBaseURL: upstreamServer.URL,
		CartesiaVersion: config.DefaultCartesiaVersion,
		Retention:       24 * time.Hour,
		RequestTimeout:  30 * time.Second,
		Version:         "test",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(cfg, st, cartesia.New(upstreamServer.URL, cfg.CartesiaVersion, cfg.RequestTimeout), logger)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return server, st, httpServer
}

func postSpeech(t *testing.T, server *httptest.Server, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(server.URL+"/v1/audio/speech", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/audio/speech error = %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func addKey(t *testing.T, st *store.Store, label, secret string) store.Key {
	t.Helper()
	key, err := st.CreateKey(context.Background(), label, secret)
	if err != nil {
		t.Fatalf("CreateKey(%q) error = %v", label, err)
	}
	return key
}

// The whole point of the translation: Cartesia must receive its own schema,
// not the OpenAI body the client sent.
func TestSpeechTranslatesAndStreamsTheAudio(t *testing.T) {
	upstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusOK, "AUDIO-BYTES", map[string]string{"Content-Type": "application/octet-stream"}
	}}
	_, st, server := newServer(t, upstream, "")
	addKey(t, st, "primary", "sk_car_primary")

	resp := postSpeech(t, server, `{"model":"cartesia/sonic-3.5","input":"halo dunia","voice":"v-9","response_format":"wav"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "audio/wav" {
		t.Errorf("Content-Type = %q, want audio/wav", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "AUDIO-BYTES" {
		t.Errorf("body = %q, want the upstream audio passed through", body)
	}

	calls := upstream.calls()
	if len(calls) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.Path != "/tts/bytes" {
		t.Errorf("upstream path = %q, want /tts/bytes", call.Path)
	}
	if got := call.Header.Get("Authorization"); got != "Bearer sk_car_primary" {
		t.Errorf("Authorization = %q, want the bearer credential", got)
	}
	if got := call.Header.Get("Cartesia-Version"); got != config.DefaultCartesiaVersion {
		t.Errorf("Cartesia-Version = %q, want %q", got, config.DefaultCartesiaVersion)
	}
	if call.Payload.ModelID != "sonic-3.5" {
		t.Errorf("model_id = %q, want the bare id with the provider prefix stripped", call.Payload.ModelID)
	}
	if call.Payload.Transcript != "halo dunia" {
		t.Errorf("transcript = %q, want the input text", call.Payload.Transcript)
	}
	if call.Payload.Voice != "v-9" {
		t.Errorf("voice = %q, want the voice id", call.Payload.Voice)
	}
	if call.Payload.OutputFormat.Container != "wav" || call.Payload.OutputFormat.Encoding == "" {
		t.Errorf("output_format = %+v, want a wav object with an encoding", call.Payload.OutputFormat)
	}
	if strings.Contains(call.Raw, `"input"`) || strings.Contains(call.Raw, `"response_format"`) {
		t.Errorf("upstream body still carries OpenAI fields: %s", call.Raw)
	}
}

func TestSpeechJSONEnvelopeUsesTheQueryFlag(t *testing.T) {
	upstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusOK, "AUDIO", nil
	}}
	_, st, server := newServer(t, upstream, "")
	addKey(t, st, "primary", "sk_car_primary")

	resp, err := http.Post(server.URL+"/v1/audio/speech?response_format=json", "application/json",
		strings.NewReader(`{"model":"sonic-3","input":"hi","voice":"v-1"}`))
	if err != nil {
		t.Fatalf("POST error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var envelope struct {
		Audio  string `json:"audio"`
		Format string `json:"format"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode envelope error = %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(envelope.Audio)
	if err != nil || string(decoded) != "AUDIO" {
		t.Errorf("envelope audio = %q (err %v), want base64 of AUDIO", envelope.Audio, err)
	}
	if envelope.Format != "mp3" {
		t.Errorf("envelope format = %q, want mp3", envelope.Format)
	}
}

// A key that answers 429 is parked and the next key serves the request.
func TestSpeechRotatesToTheNextKeyOnRateLimit(t *testing.T) {
	upstream := &fakeUpstream{answer: func(call recorded) (int, string, map[string]string) {
		if call.Header.Get("Authorization") == "Bearer sk_car_throttled" {
			return http.StatusTooManyRequests, `{"error":"rate limited"}`, map[string]string{"Retry-After": "5"}
		}
		return http.StatusOK, "AUDIO", nil
	}}
	_, st, server := newServer(t, upstream, "")
	throttled := addKey(t, st, "throttled", "sk_car_throttled")
	addKey(t, st, "healthy", "sk_car_healthy")

	resp := postSpeech(t, server, `{"model":"sonic-3","input":"hi","voice":"v-1"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after rotation", resp.StatusCode)
	}

	if calls := upstream.calls(); len(calls) != 2 {
		t.Fatalf("upstream calls = %d, want 2 (one per key)", len(calls))
	}
	parked, err := st.GetKey(context.Background(), throttled.ID)
	if err != nil {
		t.Fatalf("GetKey() error = %v", err)
	}
	if !parked.Cooling(time.Now()) {
		t.Error("the throttled key was not parked")
	}
	if parked.LastError == "" {
		t.Error("the throttled key kept no record of why it failed")
	}
}

// A 400 is the caller's payload, not the key: retrying it on another key would
// burn a second request to get the same answer.
func TestSpeechDoesNotRotateOnARequestError(t *testing.T) {
	upstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusBadRequest, `{"error":"invalid voice id"}`, nil
	}}
	_, st, server := newServer(t, upstream, "")
	key := addKey(t, st, "primary", "sk_car_primary")
	addKey(t, st, "backup", "sk_car_backup")

	resp := postSpeech(t, server, `{"model":"sonic-3","input":"hi","voice":"nope"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want the upstream 400 passed through", resp.StatusCode)
	}
	if calls := upstream.calls(); len(calls) != 1 {
		t.Errorf("upstream calls = %d, want 1", len(calls))
	}
	unchanged, _ := st.GetKey(context.Background(), key.ID)
	if unchanged.Strikes != 0 || unchanged.Cooling(time.Now()) {
		t.Errorf("key after a payload error = %+v, want it untouched", unchanged)
	}
}

func TestSpeechWithoutKeysExplainsTheSetup(t *testing.T) {
	upstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		t.Error("upstream must not be called when no key is configured")
		return http.StatusOK, "", nil
	}}
	_, _, server := newServer(t, upstream, "")

	resp := postSpeech(t, server, `{"model":"sonic-3","input":"hi","voice":"v-1"}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "dashboard") {
		t.Errorf("body = %s, want a message pointing at the dashboard", body)
	}
}

func TestSpeechRecordsUsageWithoutTheTranscript(t *testing.T) {
	upstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusOK, "0123456789", nil
	}}
	_, st, server := newServer(t, upstream, "")
	key := addKey(t, st, "primary", "sk_car_primary")

	resp := postSpeech(t, server, `{"model":"sonic-3","input":"empat lima","voice":"v-42"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	rows, total, err := st.ListRequests(context.Background(), 10, 0)
	if err != nil {
		t.Fatalf("ListRequests() error = %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("usage rows = %d (total %d), want 1", len(rows), total)
	}
	row := rows[0]
	if row.Model != "sonic-3" || row.VoiceID != "v-42" {
		t.Errorf("row = %+v, want the model and voice", row)
	}
	if row.Chars != int64(len([]rune("empat lima"))) {
		t.Errorf("chars = %d, want the transcript length", row.Chars)
	}
	if row.Bytes != 10 || row.Status != 200 {
		t.Errorf("row = %+v, want 10 bytes and status 200", row)
	}
	if row.KeyID != key.ID {
		t.Errorf("key id = %d, want %d", row.KeyID, key.ID)
	}
}

func TestVoicesProxyFiltersTheQuerystring(t *testing.T) {
	upstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusOK, `{"data":[{"id":"v-1","name":"Skylar","language":"en"}],"has_more":false}`, nil
	}}
	_, st, server := newServer(t, upstream, "")
	addKey(t, st, "primary", "sk_car_primary")

	resp, err := http.Get(server.URL + "/v1/audio/voices?language=en&q=sky&limit=500&evil=1&is_owner=true")
	if err != nil {
		t.Fatalf("GET voices error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	calls := upstream.calls()
	if len(calls) != 1 {
		t.Fatalf("upstream calls = %d, want 1", len(calls))
	}
	assertQuery(t, calls[0], "language", "en")
	assertQuery(t, calls[0], "q", "sky")
	assertQuery(t, calls[0], "limit", "100") // 500 is clamped, not forwarded
	if calls[0].Query.Has("evil") || calls[0].Query.Has("is_owner") {
		t.Errorf("an unknown query parameter was forwarded: %v", calls[0].Query)
	}
	if !calls[0].Query.Has("expand[]") {
		t.Errorf("expand[] was not requested, so no voice can offer a preview: %v", calls[0].Query)
	}
}

func TestVoicePreviewRefusesAForeignURL(t *testing.T) {
	// The preview URL is whatever the upstream list returns, so the gateway has
	// to be the one that checks where it points.
	upstream := &fakeUpstream{answer: func(call recorded) (int, string, map[string]string) {
		if call.Path == "/voices" {
			return http.StatusOK, `{"data":[{"id":"v-1","name":"Skylar","preview_file_url":"http://127.0.0.1:9/preview.mp3"}],"has_more":false}`, nil
		}
		t.Errorf("the gateway fetched a non-Cartesia preview URL: %s", call.Path)
		return http.StatusOK, "should not happen", nil
	}}
	_, st, server := newServer(t, upstream, "")
	addKey(t, st, "primary", "sk_car_primary")

	resp, err := http.Get(server.URL + "/v1/audio/voices/v-1/preview")
	if err != nil {
		t.Fatalf("GET preview error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 for a refused preview host", resp.StatusCode)
	}
}

func TestVoicePreviewStreamsFromCartesia(t *testing.T) {
	upstream := &fakeUpstream{answer: func(call recorded) (int, string, map[string]string) {
		if call.Path == "/voices" {
			return http.StatusOK, `{"data":[{"id":"v-1","name":"Skylar","preview_file_url":"https://api.cartesia.ai/preview/v-1.mp3"}],"has_more":false}`, nil
		}
		return http.StatusOK, "PREVIEW", map[string]string{"Content-Type": "audio/mpeg"}
	}}
	server, st, httpServer := newServer(t, upstream, "")
	addKey(t, st, "primary", "sk_car_primary")

	// The upstream here is a test host, so the preview fetch (which refuses any
	// host that is not Cartesia) is swapped for a stub. The guard itself has its
	// own test; what this pins is the streaming and header pass-through.
	server.previewFetch = func(context.Context, string, string) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"audio/mpeg"}},
			Body:       io.NopCloser(strings.NewReader("PREVIEW")),
		}, nil
	}

	resp, err := http.Get(httpServer.URL + "/v1/audio/voices/v-1/preview")
	if err != nil {
		t.Fatalf("GET preview error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "audio/mpeg" {
		t.Errorf("Content-Type = %q, want the upstream type passed through", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "PREVIEW" {
		t.Errorf("body = %q, want the preview bytes", body)
	}
}

func assertQuery(t *testing.T, call recorded, key, want string) {
	t.Helper()
	if got := call.Query.Get(key); got != want {
		t.Errorf("upstream query %s = %q, want %q", key, got, want)
	}
}
