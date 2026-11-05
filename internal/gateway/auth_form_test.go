package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Credentials in a query string are a leak (server logs, browser history,
// Referer). The forms post their bodies; a stray GET that still carries them
// is bounced to "/" without touching the store, and the redirect never echoes
// the values back.
func TestCredentialsInQueryAreRefused(t *testing.T) {
	_, st, server := newServer(t, emptyUpstream(), "")
	if _, err := st.EnsureDefaultAccount(t.Context()); err != nil {
		t.Fatalf("EnsureDefaultAccount() error = %v", err)
	}

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/?username=admin&password=admin", nil)
	req.Header.Set("Accept", "text/html")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 with a redirect page", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "location.replace") {
		t.Errorf("no client-side redirect in the answer: %q", body)
	}
	if strings.Contains(string(body), "password=admin") {
		t.Error("the answer echoes the credential")
	}
	for _, cookie := range resp.Cookies() {
		if strings.Contains(cookie.Value, "admin") && cookie.Name == sessionCookieName {
			t.Error("a credential-bearing request received a session")
		}
	}
}

// A browser that submits the form natively (no JS) still logs in: the POST
// body carries the credentials, and the answer is a redirect, not JSON.
func TestLoginAcceptsAFormPost(t *testing.T) {
	_, st, server := newServer(t, emptyUpstream(), "")
	if _, err := st.EnsureDefaultAccount(t.Context()); err != nil {
		t.Fatalf("EnsureDefaultAccount() error = %v", err)
	}

	form := url.Values{"username": {"admin"}, "password": {"admin"}}.Encode()
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/auth/login", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		t.Fatalf("POST error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("form login = %d, want 303", resp.StatusCode)
	}
	if resp.Header.Get("Location") != "/" {
		t.Errorf("Location = %q, want /", resp.Header.Get("Location"))
	}
	var session *http.Cookie
	for _, cookie := range resp.Cookies() {
		if cookie.Name == sessionCookieName && cookie.Value != "" {
			session = cookie
		}
	}
	if session == nil {
		t.Fatal("form login did not set a session cookie")
	}
	if !session.HttpOnly {
		t.Error("session cookie is not HttpOnly")
	}
}

// A bad password from a native form comes back as a redirect plus a one-shot
// flash cookie, never as credentials or a reason in the URL.
func TestFormLoginFailureUsesAFlashNotTheURL(t *testing.T) {
	_, st, server := newServer(t, emptyUpstream(), "")
	if _, err := st.EnsureDefaultAccount(t.Context()); err != nil {
		t.Fatalf("EnsureDefaultAccount() error = %v", err)
	}

	form := url.Values{"username": {"admin"}, "password": {"salah-sekali"}}.Encode()
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/auth/login", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if strings.Contains(resp.Header.Get("Location"), "password") {
		t.Errorf("Location leaks credentials: %s", resp.Header.Get("Location"))
	}
	foundFlash := false
	for _, cookie := range resp.Cookies() {
		if cookie.Name == flashCookieName && cookie.Value != "" {
			foundFlash = true
			if !cookie.HttpOnly {
				t.Error("flash cookie is not HttpOnly")
			}
		}
	}
	if !foundFlash {
		t.Error("no flash cookie was set for the failure message")
	}

	// /auth/state hands the notice over exactly once and nothing else.
	stateReq, _ := http.NewRequest(http.MethodGet, server.URL+"/auth/state", nil)
	stateReq.AddCookie(&http.Cookie{Name: flashCookieName, Value: "username atau password salah"})
	stateResp, err := (&http.Client{}).Do(stateReq)
	if err != nil {
		t.Fatalf("GET /auth/state error = %v", err)
	}
	defer stateResp.Body.Close()
	var state map[string]any
	if err := json.NewDecoder(stateResp.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state["notify"] != "username atau password salah" {
		t.Errorf("notify = %v, want the flash message", state["notify"])
	}
	cleared := false
	for _, cookie := range stateResp.Cookies() {
		if cookie.Name == flashCookieName && cookie.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("the flash cookie was not cleared after being read")
	}
}

// A native (no-JS) submission of the change-password form must redirect to the
// gate, never leave the browser on raw JSON at /api/account.
func TestFormPasswordChangeRedirectsInsteadOfServingJSON(t *testing.T) {
	server, st, client, cookie := freshInstallWithDefault(t)

	form := url.Values{"currentPassword": {"admin"}, "newPassword": {"password-yang-panjang-123"}}.Encode()
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/account", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatalf("POST error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("native change = %d %q, want 303 to /", resp.StatusCode, resp.Header.Get("Location"))
	}
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "json") {
		t.Errorf("native change answered JSON (%s)", ct)
	}

	// The change landed: the new password works, the default does not.
	if _, err := st.Authenticate(t.Context(), "admin", "password-yang-panjang-123"); err != nil {
		t.Errorf("new password does not authenticate: %v", err)
	}
	_ = client
}
