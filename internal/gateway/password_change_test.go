package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

// freshInstallWithDefault signs in as the seeded admin/admin and returns a
// client carrying that session cookie, plus the raw cookie value.
func freshInstallWithDefault(t *testing.T) (*httptest.Server, *store.Store, dashboardClient, string) {
	t.Helper()
	_, st, server := newServer(t, emptyUpstream(), "")
	if _, err := st.EnsureDefaultAccount(context.Background()); err != nil {
		t.Fatalf("EnsureDefaultAccount() error = %v", err)
	}
	resp := postJSON(t, server.URL+"/auth/login", `{"username":"admin","password":"admin"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("default login = %d, want 200", resp.StatusCode)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("default login cookies = %d, want 1", len(cookies))
	}
	secret := cookies[0].Value
	return server, st, newSessionClient(secret), secret
}

func newSessionClient(secret string) dashboardClient {
	c := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req = req.Clone(req.Context())
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: secret})
		req.Header.Set(csrfHeader, csrfValue)
		return http.DefaultTransport.RoundTrip(req)
	})}
	return dashboardClient{c}
}

// A fresh database starts with admin/admin, and nothing but the change-password
// endpoint answers until that password is replaced.
func TestDefaultPasswordMustBeChangedBeforeUse(t *testing.T) {
	server, _, client, _ := freshInstallWithDefault(t)

	state, err := apiGetAs(t, client.Client, server, "/auth/state")
	if err != nil {
		t.Fatalf("GET /auth/state error = %v", err)
	}
	if state["mustChangePassword"] != true {
		t.Errorf("auth state = %v, want mustChangePassword", state)
	}
	if state["username"] != "admin" {
		t.Errorf("username = %v, want admin", state["username"])
	}

	for _, path := range []string{"/api/overview", "/api/keys", "/api/clients", "/api/routing"} {
		if status := awaitGET(t, client.Client, server.URL+path); status != http.StatusForbidden {
			t.Errorf("GET %s = %d, want 403 until the password is changed", path, status)
		}
	}
}

// The gate needs to tell "you are logged in but must change the password"
// apart from "your session is over": only the first shows the change form.
func TestAuthStateDistinguishesSessionFromAnonymous(t *testing.T) {
	server, _, client, _ := freshInstallWithDefault(t)

	withSession, err := apiGetAs(t, client.Client, server, "/auth/state")
	if err != nil {
		t.Fatalf("GET /auth/state error = %v", err)
	}
	if withSession["session"] != true || withSession["mustChangePassword"] != true {
		t.Errorf("state with a session = %v, want session and mustChangePassword", withSession)
	}

	anon, err := http.Get(server.URL + "/auth/state")
	if err != nil {
		t.Fatalf("GET /auth/state (anonymous) error = %v", err)
	}
	defer anon.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(anon.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["session"] != false {
		t.Errorf("anonymous state = %v, want session false", body)
	}
	if body["mustChangePassword"] != true {
		t.Errorf("anonymous state = %v, want the account-level flag still true", body)
	}
}

// A real change lands: the sessions die and the gate opens on the next login.
func TestPasswordChangeEndsTheSession(t *testing.T) {
	server, _, client, _ := freshInstallWithDefault(t)

	// The default may not be "changed" into itself.
	body, err := apiJSONWith(t, client.Client, server, http.MethodPatch, "/api/account",
		`{"currentPassword":"admin","username":"","newPassword":"admin"}`)
	if err == nil {
		t.Fatalf("PATCH accepted the default as the new password: %v", body)
	}

	// A real change lands: the sessions die and the gate opens on the next login.
	if _, err := apiJSONWith(t, client.Client, server, http.MethodPatch, "/api/account",
		`{"currentPassword":"admin","username":"","newPassword":"password-yang-panjang-123"}`); err != nil {
		t.Fatalf("PATCH /api/account error = %v", err)
	}
	if status := awaitGET(t, client.Client, server.URL+"/api/keys"); status != http.StatusUnauthorized {
		t.Errorf("old session after the change = %d, want 401 (sessions were revoked)", status)
	}

	login := postJSON(t, server.URL+"/auth/login", `{"username":"admin","password":"password-yang-panjang-123"}`)
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login with the new password = %d", login.StatusCode)
	}
	fresh := newSessionClient(login.Cookies()[0].Value)
	state, err := apiGetAs(t, fresh.Client, server, "/auth/state")
	if err != nil {
		t.Fatalf("GET /auth/state error = %v", err)
	}
	if state["mustChangePassword"] != false {
		t.Errorf("mustChangePassword after the change = %v, want false", state["mustChangePassword"])
	}
	if status := awaitGET(t, fresh.Client, server.URL+"/api/overview"); status != http.StatusOK {
		t.Errorf("GET /api/overview after the change = %d, want 200", status)
	}
}

// The store refuses a "change" that keeps the default or reuses the username,
// and a rename + rotate in one call uses the *new* name for the check.
func TestAccountRejectsWeakOrSamePasswords(t *testing.T) {
	_, st, _ := newServer(t, emptyUpstream(), "")
	ctx := context.Background()
	if _, err := st.EnsureDefaultAccount(ctx); err != nil {
		t.Fatalf("EnsureDefaultAccount() error = %v", err)
	}
	for _, next := range []string{"admin", "ADMIN", "short"} {
		if _, err := st.UpdateAccount(ctx, "admin", "", next); err == nil {
			t.Errorf("UpdateAccount accepted %q", next)
		}
	}
	// Rename to "operator" and set the password to "operator" in one call: the
	// rule has to look at the name being set, not the name being replaced.
	if _, err := st.UpdateAccount(ctx, "admin", "operator", "operator"); err == nil {
		t.Error("UpdateAccount accepted a password equal to the new username")
	}
	// A legitimate rename plus rotate succeeds.
	account, err := st.UpdateAccount(ctx, "admin", "operator", "password-yang-panjang-123")
	if err != nil {
		t.Fatalf("UpdateAccount() error = %v", err)
	}
	if account.Username != "operator" || account.MustChangePassword {
		t.Errorf("after change = %+v, want operator without the force flag", account)
	}
	if _, err := st.Authenticate(ctx, "admin", "admin"); err == nil {
		t.Error("the default password still authenticates")
	}
	if _, err := st.Authenticate(ctx, "operator", "password-yang-panjang-123"); err != nil {
		t.Errorf("the new credentials do not authenticate: %v", err)
	}
}

// ResetToDefaultPassword is the CLI escape hatch; it re-arms the flag and
// clears every session.
func TestResetToDefaultPasswordRearmsTheForceChange(t *testing.T) {
	server, st, client, _ := freshInstallWithDefault(t)
	ctx := context.Background()

	if _, err := st.UpdateAccount(ctx, "admin", "operator", "password-yang-panjang-123"); err != nil {
		t.Fatalf("UpdateAccount() error = %v", err)
	}
	if status := awaitGET(t, client.Client, server.URL+"/api/keys"); status != http.StatusUnauthorized {
		t.Fatalf("expected the sessions to be revoked by the change, got %d", status)
	}

	account, err := st.ResetToDefaultPassword(ctx)
	if err != nil {
		t.Fatalf("ResetToDefaultPassword() error = %v", err)
	}
	if !account.MustChangePassword || account.Username != "admin" {
		t.Errorf("after reset = %+v, want admin with the force-change flag", account)
	}
	if _, err := st.Authenticate(ctx, "admin", "admin"); err != nil {
		t.Errorf("default login after reset = %v, want success", err)
	}

	login := postJSON(t, server.URL+"/auth/login", `{"username":"admin","password":"admin"}`)
	if login.StatusCode != http.StatusOK {
		t.Fatalf("login after reset = %d", login.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(login.Body).Decode(&body); err != nil {
		t.Fatalf("decode login body: %v", err)
	}
	if body["mustChangePassword"] != true {
		t.Errorf("login after reset = %v, want mustChangePassword", body)
	}
}
