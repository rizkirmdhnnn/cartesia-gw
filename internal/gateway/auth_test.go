package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The dashboard lists API keys, so the routes that read them must be behind the
// token whenever one is configured.
func TestAuthMiddlewareProtectsTheAPI(t *testing.T) {
	upstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusOK, `{"data":[],"has_more":false}`, nil
	}}
	_, _, server := newServer(t, upstream, "secret-token")

	tests := []struct {
		name       string
		method     string
		path       string
		header     string
		headerName string
		want       int
	}{
		{name: "no credential is refused", method: http.MethodGet, path: "/v1/models", want: http.StatusUnauthorized},
		{name: "the bearer token is accepted", method: http.MethodGet, path: "/v1/models", headerName: "Authorization", header: "Bearer secret-token", want: http.StatusOK},
		{name: "the shorthand header is accepted", method: http.MethodGet, path: "/v1/models", headerName: "X-Gateway-Token", header: "secret-token", want: http.StatusOK},
		{name: "a wrong token is refused", method: http.MethodGet, path: "/v1/models", headerName: "Authorization", header: "Bearer nope", want: http.StatusUnauthorized},
		{name: "a near miss is refused", method: http.MethodGet, path: "/v1/models", headerName: "Authorization", header: "Bearer secret-toke", want: http.StatusUnauthorized},
		{name: "the dashboard api is protected too", method: http.MethodGet, path: "/api/keys", want: http.StatusUnauthorized},
		{name: "the dashboard api accepts the token", method: http.MethodGet, path: "/api/keys", headerName: "Authorization", header: "Bearer secret-token", want: http.StatusOK},
		{name: "the static page carries no secret", method: http.MethodGet, path: "/", want: http.StatusOK},
		{name: "health stays open for probes", method: http.MethodGet, path: "/healthz", want: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, server.URL+tt.path, nil)
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}
			if tt.headerName != "" {
				req.Header.Set(tt.headerName, tt.header)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request error = %v", err)
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != tt.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.want)
			}
		})
	}
}

// With no token configured the API is open, which is only reachable on loopback
// because config.Load refuses anything else.
func TestAuthMiddlewareIsTransparentWithoutAToken(t *testing.T) {
	upstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusOK, `{"data":[],"has_more":false}`, nil
	}}
	_, _, server := newServer(t, upstream, "")

	resp, err := http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatalf("request error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 without a token configured", resp.StatusCode)
	}
}

// A health payload must not leak the token itself, only whether one is set.
func TestHealthNeverEchoesTheToken(t *testing.T) {
	upstream := &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusOK, "", nil
	}}
	srv, _, _ := newServer(t, upstream, "super-secret")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "super-secret") {
		t.Errorf("health body leaks the token: %s", body)
	}
	if !strings.Contains(body, `"gatewayTokenIsSet":true`) {
		t.Errorf("health body does not report that a token is configured: %s", body)
	}
}
