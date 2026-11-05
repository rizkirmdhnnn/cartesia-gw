package gateway

import (
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

// The dashboard is authenticated by a session cookie; API clients are
// authenticated by a client key. They are deliberately different credentials:
// a browser session can manage the gateway, a client key can only spend it.

const (
	sessionCookieName = "cartesia_gw_session"
	// sessionLifetime mirrors store.SessionTTL; the cookie expires with the row
	// so a stale cookie is not sent for a dead session.
	sessionLifetime = store.SessionTTL
	// csrfHeader / csrfValue guard state-changing requests. A form on another
	// origin cannot set a custom header without a CORS preflight this server
	// never grants.
	csrfHeader = "X-Requested-With"
	csrfValue  = "cartesia-gw"
)

// statusRecorder is declared in middleware.go; the guards below compose funcs
// over the whole chain.

// passwordChangePath is the only dashboard route a session may use while the
// account still carries the default password.
func passwordChangePath(path string) bool {
	switch path {
	case "/api/account", "/auth/logout":
		return true
	}
	return false
}

// sessionGuard resolves a session cookie once per request and stashes it, so
// the api and page handlers never look it up twice. Revocation is by deletion:
// a password change and "log out everywhere" delete the rows themselves.
func (s *Server) sessionGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := sessionCookie(r)
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		session, err := s.store.LookupSession(r.Context(), token)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		// Rotation invalidation: a password change or "log out everywhere"
		// rewrites auth_session_id, and every cookie cut before that moment
		// stops matching the stored hash.
		generation, err := s.store.SessionGeneration(r.Context())
		if err != nil || !store.ConstantTimeEquals(store.HashSessionValue(token), generation) {
			next.ServeHTTP(w, r)
			return
		}
		// The shipped default password may not be usable as a working login:
		// until it is changed the session can reach nothing but the form that
		// changes it.
		if account, err := s.store.Account(r.Context()); err == nil && account.MustChangePassword &&
			!passwordChangePath(r.URL.Path) {
			if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
				writeJSON(w, http.StatusForbidden, map[string]any{
					"error": map[string]any{
						"message": "ganti password default dulu sebelum memakai gateway",
						"type":    "password_change_required",
					},
				})
				return
			}
		}
		next.ServeHTTP(w, withSession(r, session))
	})
}

// authMiddleware is the single gate for every request. It decides one of four
// fates:
//
//   - public path (static assets, healthz, auth endpoints): served as-is
//   - valid session cookie: the dashboard, /api/* and /v1/* all allowed
//   - valid client key: only /v1/*
//   - nothing: 401 with a JSON envelope that says what to send
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		if sessionFrom(r) != nil {
			// The CSRF marker is required on everything except the page shell,
			// static assets and health. A GET that returns a secret (the key
			// reveal) is a read of state a cross-origin page must not be able to
			// trigger, so the rule is "any /api or /v1 call", not just writes.
			//
			// Exception: a native form POST. A browser form cannot set a custom
			// header, and SameSite=Strict already keeps the cookie off
			// cross-site requests, so requiring the marker there would only
			// break the no-JS path. The body is still validated as a form.
			wantsMarker := !publicPath(r.URL.Path) && !isFormPost(r)
			if wantsMarker && !hasCSRFMarker(r) {
				writeError(w, http.StatusForbidden, "missing "+csrfHeader+": "+csrfValue)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeUnauthorized(w, "operator login required: open the dashboard and sign in")
			return
		}

		gate, err := s.store.Gate(r.Context())
		if err != nil {
			s.logError("auth gate", err)
			writeError(w, http.StatusInternalServerError, "database unavailable")
			return
		}

		// A key is required whenever the operator asked for logins or anything
		// arrives from the network. Loopback on a fresh install stays open, so
		// an existing local script does not break merely because this layer
		// exists. Once a client key exists on a loopback install it is
		// required too — see KeysExist.
		requireKey := gate.PromptLogin || gate.KeysExist || !isLoopbackRequest(r)
		if !requireKey {
			next.ServeHTTP(w, r)
			return
		}

		presented := presentAPIKey(r)
		if presented == "" {
			writeUnauthorized(w,
				"missing client api key: send Authorization: Bearer dk_… (create one in the dashboard)")
			return
		}
		key, err := s.store.AuthenticateClientKey(r.Context(), presented)
		if err != nil {
			if !errors.Is(err, store.ErrClientKeyRejected) {
				s.logError("authenticate client key", err)
				writeError(w, http.StatusInternalServerError, "database unavailable")
				return
			}
			s.logger.Warn("client key rejected", "remote", clientIP(r))
			writeUnauthorized(w, "invalid, disabled or expired client api key")
			return
		}
		// The request log records which client spent the quota.
		next.ServeHTTP(w, withClientKey(r, key))
		next.ServeHTTP(w, r)
	})
}

// publicPath lists what is served without any credential. The page shell and
// its assets carry no secret; the login screen needs them.
func publicPath(path string) bool {
	switch path {
	case "/", "/app.css", "/app.js", "/auth.js", "/router.js", "/routing.js", "/clients.js",
		"/providers.js", "/favicon.ico", "/healthz":
		return true
	}
	return strings.HasPrefix(path, "/auth/")
}

func hasCSRFMarker(r *http.Request) bool {
	return r.Header.Get(csrfHeader) == csrfValue
}

func writeUnauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="cartesia-gw"`)
	writeError(w, http.StatusUnauthorized, message)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isLoopbackRequest(r *http.Request) bool {
	ip := net.ParseIP(clientIP(r))
	return ip != nil && ip.IsLoopback()
}

// presentAPIKey extracts the bearer credential. X-Gateway-Token is kept as an
// alias so existing callers keep working; it accepts the same client keys.
func presentAPIKey(r *http.Request) string {
	if header := r.Header.Get("Authorization"); header != "" {
		if token, ok := strings.CutPrefix(header, "Bearer "); ok {
			return strings.TrimSpace(token)
		}
		return ""
	}
	return strings.TrimSpace(r.Header.Get("X-Gateway-Token"))
}
