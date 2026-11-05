package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

// dashboardClient is an http.Client carrying a real operator session cookie.
// The API is session-only now, so every dashboard test signs in once through
// the same endpoint a browser uses, and writes carry the CSRF marker.
type dashboardClient struct{ *http.Client }

func (c dashboardClient) Do(req *http.Request) (*http.Response, error) {
	req.Header.Set(csrfHeader, csrfValue)
	return c.Client.Do(req)
}

func (c dashboardClient) Get(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

func (c dashboardClient) Post(url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return c.Do(req)
}

const (
	testUsername = "operator"
	testPassword = "correct-horse-battery"
)

// signIn creates the operator account (first call) and returns a client whose
// transport attaches the session cookie.
func signIn(t *testing.T, st *store.Store) dashboardClient {
	t.Helper()
	if _, err := st.CreateAccount(context.Background(), testUsername, testPassword); err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}
	token, _, err := st.CreateSession(context.Background(), "test", "127.0.0.1")
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req = req.Clone(req.Context())
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		return http.DefaultTransport.RoundTrip(req)
	})}
	return dashboardClient{client}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// apiGet fetches a dashboard API path and decodes it.
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

// apiGetAs fetches with the operator session. The CSRF marker rides along,
// because a real dashboard fetch sends it even on reads.
func apiGetAs(t *testing.T, client *http.Client, server *httptest.Server, path string) (map[string]any, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(csrfHeader, csrfValue)
	resp, err := client.Do(req)
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
		{path: "/auth.js", contentType: "text/javascript; charset=utf-8", mustContain: "/auth/login", etag: true},
		{path: "/routing.js", contentType: "text/javascript; charset=utf-8", mustContain: "/api/routing", etag: true},
		{path: "/clients.js", contentType: "text/javascript; charset=utf-8", mustContain: "/api/clients", etag: true},
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
	_, st, server := newServer(t, upstream, "")
	client := signIn(t, st)

	body, err := apiGetAs(t, client.Client, server, "/api/overview")
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

func TestRotationSettingIsExposedAndValidated(t *testing.T) {
	upstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusOK, "", nil
	}}
	_, st, server := newServer(t, upstream, "")
	client := signIn(t, st)

	overview, err := apiGetAs(t, client.Client, server, "/api/overview")
	if err != nil {
		t.Fatalf("GET /api/overview error = %v", err)
	}
	if overview["rotation"] != "least-recently-used" {
		t.Errorf("rotation = %v, want the default least-recently-used", overview["rotation"])
	}
	rotations, _ := overview["rotations"].([]any)
	if len(rotations) != 5 {
		t.Errorf("rotations = %v, want the five strategies", rotations)
	}

	// A valid change is stored and reported back.
	body, err := apiJSONAs(t, client.Client, server, http.MethodPatch, "/api/settings", `{"rotation":"round-robin"}`)
	if err != nil {
		t.Fatalf("PATCH /api/settings error = %v", err)
	}
	if body["rotation"] != "round-robin" {
		t.Errorf("PATCH returned rotation = %v, want round-robin", body["rotation"])
	}

	// And it survives: the overview is read from the database, not memory.
	overview, err = apiGetAs(t, client.Client, server, "/api/overview")
	if err != nil {
		t.Fatalf("GET /api/overview error = %v", err)
	}
	if overview["rotation"] != "round-robin" {
		t.Errorf("rotation after the change = %v, want round-robin", overview["rotation"])
	}
}

func TestRotationSettingRejectsUnknownValues(t *testing.T) {
	upstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusOK, "", nil
	}}
	_, st, server := newServer(t, upstream, "")
	client := signIn(t, st)

	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "an unknown strategy", body: `{"rotation":"first"}`, want: http.StatusBadRequest},
		{name: "an empty strategy", body: `{"rotation":""}`, want: http.StatusBadRequest},
		{name: "nothing to change", body: `{}`, want: http.StatusBadRequest},
		{name: "malformed json", body: `{`, want: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := client.Do(mustRequestWithSession(t, http.MethodPatch, server.URL+"/api/settings", tt.body))
			if err != nil {
				t.Fatalf("PATCH error = %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}

	// The rejected values must not have been written.
	overview, err := apiGetAs(t, client.Client, server, "/api/overview")
	if err != nil {
		t.Fatalf("GET /api/overview error = %v", err)
	}
	if overview["rotation"] != "least-recently-used" {
		t.Errorf("rotation = %v, want the default to be untouched", overview["rotation"])
	}
}

func mustRequest(t *testing.T, method, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

// mustRequestWithSession is mustRequest plus the CSRF marker a signed-in
// browser sends; the session cookie comes from the client transport.
func mustRequestWithSession(t *testing.T, method, url, body string) *http.Request {
	t.Helper()
	req := mustRequest(t, method, url, body)
	req.Header.Set(csrfHeader, csrfValue)
	return req
}

func apiJSON(t *testing.T, server *httptest.Server, method, path, body string) (map[string]any, error) {
	t.Helper()
	return apiJSONWith(t, http.DefaultClient, server, method, path, body)
}

func apiJSONAs(t *testing.T, client *http.Client, server *httptest.Server, method, path, body string) (map[string]any, error) {
	t.Helper()
	return apiJSONAsStatus(t, client, server, method, path, body, http.StatusOK)
}

// apiJSONAsStatus is apiJSONAs for endpoints that answer 201.
func apiJSONAsStatus(t *testing.T, client *http.Client, server *httptest.Server, method, path, body string, status int) (map[string]any, error) {
	t.Helper()
	req := mustRequest(t, method, server.URL+path, body)
	req.Header.Set(csrfHeader, csrfValue)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if resp.StatusCode != status {
		return decoded, errors.New("unexpected status")
	}
	return decoded, nil
}

func apiJSONWith(t *testing.T, client *http.Client, server *httptest.Server, method, path, body string) (map[string]any, error) {
	t.Helper()
	req := mustRequest(t, method, server.URL+path, body)
	req.Header.Set(csrfHeader, csrfValue)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return decoded, errors.New("unexpected status")
	}
	return decoded, nil
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
