package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

func emptyUpstream() *fakeUpstream {
	return &fakeUpstream{answer: func(recorded) (int, string, map[string]string) {
		return http.StatusOK, `{"data":[],"has_more":false}`, nil
	}}
}

// /api is a dashboard surface: only a session opens it now. The old shared
// token was a client credential and must not manage the gateway.
func TestDashboardAPIRefusesAnythingButASession(t *testing.T) {
	_, st, server := newServer(t, emptyUpstream(), "legacy-token")
	_, secret, err := st.CreateClientKey(context.Background(), "ci", 0)
	if err != nil {
		t.Fatalf("CreateClientKey() error = %v", err)
	}

	tests := []struct {
		name   string
		header string
		value  string
		want   int
	}{
		{name: "no credential", want: http.StatusUnauthorized},
		{name: "a client key", header: "Authorization", value: "Bearer " + secret, want: http.StatusUnauthorized},
		{name: "the legacy token", header: "Authorization", value: "Bearer legacy-token", want: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, server.URL+"/api/keys", nil)
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

// /v1 takes client keys. A key that exists means the loopback exemption is
// over: a missing key is refused, a disabled key stops working immediately.
func TestVoiceAPIAcceptsOnlyClientKeys(t *testing.T) {
	upstream := emptyUpstream()
	_, st, server := newServer(t, upstream, "")
	// Voices are listed through Cartesia, so this store needs a Cartesia key;
	// the client key below is what the caller presents.
	addKey(t, st, "primary", "sk_car_primary")
	key, secret, err := st.CreateClientKey(context.Background(), "ci", 0)
	if err != nil {
		t.Fatalf("CreateClientKey() error = %v", err)
	}

	get := func(header, value string) int {
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/audio/voices", nil)
		if header != "" {
			req.Header.Set(header, value)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request error = %v", err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	if got := get("", ""); got != http.StatusUnauthorized {
		t.Errorf("no key = %d, want 401 once a key exists", got)
	}
	if got := get("Authorization", "Bearer "+secret); got != http.StatusOK {
		t.Errorf("valid key = %d, want 200", got)
	}
	if got := get("X-Gateway-Token", secret); got != http.StatusOK {
		t.Errorf("X-Gateway-Token alias = %d, want 200", got)
	}

	if err := st.SetClientKeyActive(context.Background(), key.ID, false); err != nil {
		t.Fatalf("SetClientKeyActive() error = %v", err)
	}
	if got := get("Authorization", "Bearer "+secret); got != http.StatusUnauthorized {
		t.Errorf("disabled key = %d, want 401 on the next request", got)
	}
}

// Writes through a cookie need the CSRF marker; a form on another origin cannot
// set a custom header without a preflight this server never grants.
func TestSessionWritesRequireTheCSRFMarker(t *testing.T) {
	_, st, server := newServer(t, emptyUpstream(), "")
	client := signIn(t, st)

	req := mustRequest(t, http.MethodPatch, server.URL+"/api/settings", `{"rotation":"round-robin"}`)
	resp, err := client.Client.Do(req) // no X-Requested-With
	if err != nil {
		t.Fatalf("PATCH error = %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("PATCH without the marker = %d, want 403", resp.StatusCode)
	}

	// Reads too: the key reveal returns a secret, so a bare GET with the cookie
	// must not be enough either.
	read := awaitGET(t, client.Client, server.URL+"/api/keys")
	if read != http.StatusForbidden {
		t.Errorf("GET without the marker = %d, want 403", read)
	}

	ok, err := apiJSONAs(t, client.Client, server, http.MethodPatch, "/api/settings", `{"rotation":"round-robin"}`)
	if err != nil {
		t.Fatalf("PATCH with the marker error = %v", err)
	}
	if ok["rotation"] != "round-robin" {
		t.Errorf("rotation = %v, want round-robin", ok["rotation"])
	}
}

// Signing in is the only way to get a session cookie, and the cookie is not
// the password: a wrong password must not hand one out.
func TestLoginIssuesASessionAndRejectsBadPasswords(t *testing.T) {
	_, st, server := newServer(t, emptyUpstream(), "")
	if _, err := st.CreateAccount(context.Background(), "op", "correct-horse-battery"); err != nil {
		t.Fatalf("CreateAccount() error = %v", err)
	}

	bad := postJSON(t, server.URL+"/auth/login", `{"username":"op","password":"wrong-password"}`)
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d, want 401", bad.StatusCode)
	}
	if len(bad.Cookies()) != 0 {
		t.Error("a failed login set a cookie")
	}

	good := postJSON(t, server.URL+"/auth/login", `{"username":"op","password":"correct-horse-battery"}`)
	if good.StatusCode != http.StatusOK {
		t.Fatalf("login = %d, want 200", good.StatusCode)
	}
	cookies := good.Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName || !cookies[0].HttpOnly {
		t.Fatalf("login cookies = %v, want one HttpOnly session cookie", cookies)
	}

	// The cookie opens the dashboard; the password does not.
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/api/keys", nil)
	req.AddCookie(cookies[0])
	req.Header.Set(csrfHeader, csrfValue)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/keys error = %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/keys with the session = %d, want 200", resp.StatusCode)
	}
}

// First visit creates the account; a second setup call must not replace it.
func TestSetupRunsExactlyOnce(t *testing.T) {
	_, _, server := newServer(t, emptyUpstream(), "")

	first := postJSON(t, server.URL+"/auth/setup", `{"username":"op","password":"correct-horse-battery"}`)
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first setup = %d, want 201", first.StatusCode)
	}
	second := postJSON(t, server.URL+"/auth/setup", `{"username":"evil","password":"another-long-password"}`)
	if second.StatusCode != http.StatusConflict {
		t.Errorf("second setup = %d, want 409", second.StatusCode)
	}

	state, err := http.Get(server.URL + "/auth/state")
	if err != nil {
		t.Fatalf("GET /auth/state error = %v", err)
	}
	defer state.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(state.Body).Decode(&body); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if body["configured"] != true || body["username"] != "op" || body["setup"] != false {
		t.Errorf("state = %v, want configured with username op", body)
	}
}

// A short password is refused where it should be: at creation.
func TestSetupRejectsShortPasswords(t *testing.T) {
	_, _, server := newServer(t, emptyUpstream(), "")
	resp := postJSON(t, server.URL+"/auth/setup", `{"username":"op","password":"short"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("short password setup = %d, want 400", resp.StatusCode)
	}
}

// Logging out kills the cookie server-side, not just in the browser.
func TestLogoutInvalidatesTheSessionRow(t *testing.T) {
	_, st, server := newServer(t, emptyUpstream(), "")
	client := signIn(t, st)

	resp, err := client.Post(server.URL+"/auth/logout", "application/json", nil)
	if err != nil {
		t.Fatalf("logout error = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", resp.StatusCode)
	}

	after, err := client.Get(server.URL + "/api/keys")
	if err != nil {
		t.Fatalf("GET after logout error = %v", err)
	}
	defer after.Body.Close()
	_, _ = io.Copy(io.Discard, after.Body)
	if after.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET after logout = %d, want 401", after.StatusCode)
	}
}

// Client keys are manageable only by a session, and the secret is returned
// once — the list carries a prefix, never the full key.
func TestClientKeysAreCreatedWithASessionAndListedMasked(t *testing.T) {
	_, st, server := newServer(t, emptyUpstream(), "")
	addKey(t, st, "primary", "sk_car_primary")
	client := signIn(t, st)

	created, err := apiJSONAsStatus(t, client.Client, server, http.MethodPost, "/api/clients", `{"label":"n8n"}`, http.StatusCreated)
	if err != nil {
		t.Fatalf("POST /api/clients error = %v", err)
	}
	secret, _ := created["secret"].(string)
	if len(secret) < 20 || secret[:3] != store.ClientKeyPrefix {
		t.Fatalf("secret = %q, want a %s… value", secret, store.ClientKeyPrefix)
	}

	list, err := apiGetAs(t, client.Client, server, "/api/clients")
	if err != nil {
		t.Fatalf("GET /api/clients error = %v", err)
	}
	encoded, _ := json.Marshal(list)
	if strings.Contains(string(encoded), secret) {
		t.Errorf("the list leaks the full secret: %s", encoded)
	}

	req, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/audio/voices", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/audio/voices error = %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("new client key = %d, want 200", resp.StatusCode)
	}
}

// The old gateway token is carried forward as a client key on setup, so an
// upgrade does not break scripts that already authenticate with it.
func TestLegacyGatewayTokenKeepsWorkingAfterSetup(t *testing.T) {
	upstream := emptyUpstream()
	_, st, server := newServer(t, upstream, "legacy-token-123")
	addKey(t, st, "primary", "sk_car_primary")

	login := postJSON(t, server.URL+"/auth/setup", `{"username":"op","password":"correct-horse-battery"}`)
	if login.StatusCode != http.StatusCreated {
		t.Fatalf("setup = %d, want 201", login.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/audio/voices", nil)
	req.Header.Set("Authorization", "Bearer legacy-token-123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/audio/voices error = %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("legacy token after setup = %d, want 200", resp.StatusCode)
	}
}

// awaitGET issues a plain GET (no CSRF marker) with the given client.
func awaitGET(t *testing.T, client *http.Client, url string) int {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s error = %v", url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// postJSON sends a JSON body and returns the response with its cookies intact.
// The body is NOT drained: callers that decode it can still read it.
func postJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s error = %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}
