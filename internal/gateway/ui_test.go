package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// apiGet fetches a dashboard API path and decodes it, without a gateway token
// (the fixtures for these tests do not configure one).
func apiGet(t *testing.T, server *httptest.Server, path string) (map[string]any, error) {
	t.Helper()
	resp, err := http.Get(server.URL + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body, nil
}

// The dashboard is three embedded files. A missing one is a runtime 404 in a
// browser and nothing else notices, so each is pinned here by name and type.
func TestDashboardAssetsAreServed(t *testing.T) {
	upstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusOK, "", nil
	}}
	_, _, server := newServer(t, upstream, "")

	tests := []struct {
		path        string
		contentType string
		mustContain string
		etag        bool
	}{
		{path: "/", contentType: "text/html; charset=utf-8", mustContain: "voiceList"},
		{path: "/app.css", contentType: "text/css; charset=utf-8", mustContain: ".voice-card", etag: true},
		{path: "/app.js", contentType: "text/javascript; charset=utf-8", mustContain: "MediaSource", etag: true},
		{path: "/favicon.ico", contentType: "image/svg+xml", mustContain: "<svg", etag: true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			resp, err := http.Get(server.URL + tt.path)
			if err != nil {
				t.Fatalf("GET %s error = %v", tt.path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if got := resp.Header.Get("Content-Type"); got != tt.contentType {
				t.Errorf("Content-Type = %q, want %q", got, tt.contentType)
			}
			if tt.etag && resp.Header.Get("ETag") == "" {
				t.Error("no ETag, so the browser cannot revalidate the asset")
			}
			if !tt.etag && resp.Header.Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store for the shell", resp.Header.Get("Cache-Control"))
			}
			body, _ := io.ReadAll(resp.Body)
			if !strings.Contains(string(body), tt.mustContain) {
				t.Errorf("body does not contain %q", tt.mustContain)
			}
		})
	}
}

// A revalidated asset answers 304 rather than shipping the bytes again.
func TestDashboardAssetRevalidation(t *testing.T) {
	upstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusOK, "", nil
	}}
	_, _, server := newServer(t, upstream, "")

	first, err := http.Get(server.URL + "/app.js")
	if err != nil {
		t.Fatalf("GET /app.js error = %v", err)
	}
	first.Body.Close()
	etag := first.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the first response")
	}

	req, err := http.NewRequest(http.MethodGet, server.URL+"/app.js", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("If-None-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("revalidation error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("status = %d, want 304 for a matching ETag", resp.StatusCode)
	}
}

// The overview is where the dashboard learns the bounds it renders, so a
// control the server would reject must never be offered by the UI.
func TestOverviewPublishesTheControlBounds(t *testing.T) {
	upstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusOK, "", nil
	}}
	_, _, server := newServer(t, upstream, "")

	body, err := apiGet(t, server, "/api/overview")
	if err != nil {
		t.Fatalf("GET /api/overview error = %v", err)
	}
	controls, ok := body["controls"].(map[string]any)
	if !ok {
		t.Fatalf("overview has no controls block: %v", body)
	}
	if emotions, _ := controls["emotions"].([]any); len(emotions) == 0 {
		t.Error("controls.emotions is empty, so the picker would render nothing")
	}
	if primary, _ := controls["primaryEmotions"].([]any); len(primary) == 0 {
		t.Error("controls.primaryEmotions is empty")
	}
	for _, key := range []string{"speedRange", "volumeRange"} {
		pair, _ := controls[key].([]any)
		if len(pair) != 2 {
			t.Errorf("controls.%s = %v, want a two-element range", key, controls[key])
		}
	}
}

func TestVoicesGenderFilterIsForwardedAndBounded(t *testing.T) {
	upstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusOK, `{"data":[],"has_more":false}`, nil
	}}
	_, st, server := newServer(t, upstream, "")
	addKey(t, st, "primary", "sk_car_primary")

	if _, err := http.Get(server.URL + "/v1/audio/voices?gender=feminine"); err != nil {
		t.Fatalf("GET voices error = %v", err)
	}
	if _, err := http.Get(server.URL + "/v1/audio/voices?gender=nonsense"); err != nil {
		t.Fatalf("GET voices error = %v", err)
	}

	calls := upstream.calls()
	if len(calls) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(calls))
	}
	if got := calls[0].Query.Get("gender"); got != "feminine" {
		t.Errorf("gender = %q, want feminine forwarded", got)
	}
	if calls[1].Query.Has("gender") {
		t.Errorf("an undocumented gender was forwarded: %v", calls[1].Query)
	}
}
