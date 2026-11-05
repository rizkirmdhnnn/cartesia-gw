package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// authTestCases is the old token table, rewritten for the two-credential model:
// the shared token no longer opens /v1 (a client key does), it no longer opens
// /api (a session does), and the public surface is unchanged.
func TestAuthBoundaries(t *testing.T) {
	_, st, server := newServer(t, emptyUpstream(), "secret-token")
	// A stored client key ends the loopback exemption, so the boundary is the
	// one under test rather than the fresh-install shortcut.
	if _, _, err := st.CreateClientKey(t.Context(), "ci", 0); err != nil {
		t.Fatalf("CreateClientKey() error = %v", err)
	}

	tests := []struct {
		name   string
		method string
		path   string
		header string
		value  string
		want   int
	}{
		{name: "the legacy token is not a client key", method: http.MethodGet, path: "/v1/models", header: "Authorization", value: "Bearer secret-token", want: http.StatusUnauthorized},
		{name: "no credential on /v1", method: http.MethodGet, path: "/v1/models", want: http.StatusUnauthorized},
		{name: "no credential on /api", method: http.MethodGet, path: "/api/keys", want: http.StatusUnauthorized},
		{name: "the page shell is public", method: http.MethodGet, path: "/", want: http.StatusOK},
		{name: "the login assets are public", method: http.MethodGet, path: "/app.js", want: http.StatusOK},
		{name: "health stays open for probes", method: http.MethodGet, path: "/healthz", want: http.StatusOK},
		{name: "the auth state is public", method: http.MethodGet, path: "/auth/state", want: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, server.URL+tt.path, strings.NewReader(""))
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}
			if tt.header != "" {
				req.Header.Set(tt.header, tt.value)
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

// A fresh loopback install with no account and no client key keeps serving /v1
// so a local script does not break merely because authentication exists. The
// moment the operator asks for logins, that exemption ends.
func TestLoopbackExemptionEndsWhenLoginsAreRequired(t *testing.T) {
	_, st, server := newServer(t, emptyUpstream(), "")

	resp, err := http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET /v1/models error = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("fresh loopback install = %d, want 200", resp.StatusCode)
	}

	if err := st.SetPromptLogin(t.Context(), true); err != nil {
		t.Fatalf("SetPromptLogin() error = %v", err)
	}
	resp, err = http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET /v1/models error = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("after opting into logins = %d, want 401", resp.StatusCode)
	}
}

// A health payload must not leak the token itself, only whether one is set.
func TestHealthNeverEchoesTheToken(t *testing.T) {
	srv, _, server := newServer(t, emptyUpstream(), "super-secret")
	_ = server

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
