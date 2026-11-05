package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

const (
	maxAuthBody = 8 << 10
	// maxLoginFailures / loginWindow throttle password guessing from one
	// address. bcrypt already makes each attempt cost ~250ms; the counter stops
	// a script from turning that into an unbounded amount of CPU.
	maxLoginFailures = 10
	loginWindow      = 15 * time.Minute
)

// handleAuthState answers GET /auth/state: what the login page should render.
// It is public on purpose and reveals only whether an account exists, whether
// it still carries the default password, and whether this browser holds a
// valid session — the three facts the gate needs.
func (s *Server) handleAuthState(w http.ResponseWriter, r *http.Request) {
	gates, err := s.store.Gate(r.Context())
	if err != nil {
		s.logError("auth state", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	account, err := s.store.Account(r.Context())
	if err != nil && gates.Configured {
		s.logError("auth state: account", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"configured": gates.Configured,
		// setup is only offered when no account exists at all; a fresh install
		// gets the default admin/admin instead (see store.EnsureDefaultAccount).
		"setup":    !gates.Configured,
		"username": account.Username,
		// mustChangePassword locks the console to the change-password form.
		"mustChangePassword": account.MustChangePassword,
		"defaultUsername":    store.DefaultUsername,
		"defaultPassword":    store.DefaultPassword,
		"minPasswordLen":     store.MinPasswordLen,
		// csrf is the header value the dashboard must send on writes.
		"csrfHeader": csrfHeader,
		"csrfValue":  csrfValue,
		"version":    s.cfg.Version,
		// session is true only when this request carried a live cookie. The gate
		// uses it to explain a locked screen instead of silently showing one.
		"session": sessionFrom(r) != nil,
		// notify carries a one-shot message from a native form submission
		// (wrong password, password changed); credentials never appear in a URL.
		"notify": s.popFlash(w, r),
	})
}

// handleSetup answers POST /auth/setup: create the one operator account.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := s.decodeCredentials(r, &body); err != nil {
		s.writeAuthFailure(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if !isLoopbackRequest(r) && s.cfg.GatewayToken == "" {
		writeError(w, http.StatusForbidden,
			"first-time setup is only offered from the server itself; run it from the VPS, or set CARTESIA_GW_TOKEN and sign in with it")
		return
	}
	account, err := s.store.CreateAccount(r.Context(), body.Username, body.Password)
	if err != nil {
		if errors.Is(err, store.ErrAccountExists) {
			writeError(w, http.StatusConflict, "an operator account already exists; sign in instead")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Creating the account is the point at which an install starts asking for
	// credentials, so remember that choice.
	_ = s.store.SetPromptLogin(r.Context(), true)
	s.importLegacyToken(r)

	token, _, err := s.store.CreateSession(r.Context(), r.UserAgent(), clientIP(r))
	if err != nil {
		s.logError("setup: create session", err)
		writeError(w, http.StatusInternalServerError, "account created but the session could not be saved; sign in instead")
		return
	}
	s.setSessionCookie(w, r, token)
	s.logger.Info("operator account created", "username", account.Username, "remote", clientIP(r))
	writeJSON(w, http.StatusCreated, map[string]any{"username": account.Username, "createdAt": account.CreatedAt})
}

// handleLogin answers POST /auth/login. The legacy CARTESIA_GW_TOKEN is
// accepted as the password only while no account exists yet, so an existing
// deployment can log in and set a real account without losing access.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if rejectCredentialsInURL(w, r) {
		return
	}
	if !s.loginAllowed(keyForAttempt(r)) {
		w.Header().Set("Retry-After", "900")
		s.writeAuthFailure(w, r, http.StatusTooManyRequests, "terlalu banyak percobaan login; coba lagi dalam 15 menit")
		return
	}

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := s.decodeCredentials(r, &body); err != nil {
		s.writeAuthFailure(w, r, http.StatusBadRequest, err.Error())
		return
	}

	configured, err := s.store.AccountExists(r.Context())
	if err != nil {
		s.logError("login: account state", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	if !configured {
		s.handleBootstrapLogin(w, r, body.Username, body.Password)
		return
	}

	account, err := s.store.Authenticate(r.Context(), body.Username, body.Password)
	if err != nil {
		s.recordLoginFailure(keyForAttempt(r))
		s.writeAuthFailure(w, r, http.StatusUnauthorized, "username atau password salah")
		return
	}
	mustChange := account.MustChangePassword
	s.clearLoginFailures(keyForAttempt(r))

	token, _, err := s.store.CreateSession(r.Context(), r.UserAgent(), clientIP(r))
	if err != nil {
		s.logError("login: create session", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	s.setSessionCookie(w, r, token)
	s.logger.Info("operator signed in", "remote", clientIP(r))
	if wantsHTML(r) {
		// A native form submission: land back on the dashboard, which will show
		// the change-password screen if it is still required.
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"mustChangePassword": mustChange,
	})
}

// handleBootstrapLogin turns a first sign-in into account creation. The
// username and password entered become the operator account; the previous
// CARTESIA_GW_TOKEN (if any) also still opens the door, with the same screen.
func (s *Server) handleBootstrapLogin(w http.ResponseWriter, r *http.Request, username, password string) {
	legacy := s.cfg.GatewayToken
	if legacy != "" && store.ConstantTimeEquals(password, legacy) && username != "" {
		// The operator pasted the old token as the password. Create the account
		// with that token as the password so nothing locks out mid-upgrade.
		if _, err := s.store.CreateAccount(r.Context(), username, password); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	} else if _, err := s.store.CreateAccount(r.Context(), username, password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = s.store.SetPromptLogin(r.Context(), true)
	s.importLegacyToken(r)

	token, _, err := s.store.CreateSession(r.Context(), r.UserAgent(), clientIP(r))
	if err != nil {
		s.logError("bootstrap: create session", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	s.setSessionCookie(w, r, token)
	s.logger.Info("operator account created on first login", "username", username, "remote", clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "created": true})
}

// handleLogout answers POST /auth/logout: drop the row and the cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token := sessionCookie(r); token != "" {
		if err := s.store.DeleteSession(r.Context(), token); err != nil {
			s.logError("logout", err)
		}
	}
	s.expireSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// handleAccount answers GET /api/account.
func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	account, err := s.store.Account(r.Context())
	if err != nil {
		s.logError("account", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	sessions, err := s.store.ListSessions(r.Context())
	if err != nil {
		s.logger.Warn("list sessions", "error", err)
	}
	current := int64(0)
	if session := sessionFrom(r); session != nil {
		current = session.ID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username":         account.Username,
		"createdAt":        account.CreatedAt,
		"currentSessionId": current,
		"sessions":         sessions,
		"minPasswordLen":   store.MinPasswordLen,
	})
}

// handleUpdateAccount answers PATCH /api/account. A password change signs every
// session out, including this one, so a stolen cookie cannot outlive it.
func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		// currentPassword is optional only while the default password is still
		// in place; after that the store's own verification applies.
		CurrentPassword string `json:"currentPassword"`
		Username        string `json:"username"`
		NewPassword     string `json:"newPassword"`
	}
	if isFormPost(r) {
		// A native form submission (the change-password screen before JS is
		// ready): the field names mirror the JSON keys.
		if err := r.ParseForm(); err != nil {
			s.writeAuthFailure(w, r, http.StatusBadRequest, "body form tidak valid")
			return
		}
		body.CurrentPassword = r.PostFormValue("currentPassword")
		body.Username = r.PostFormValue("username")
		body.NewPassword = r.PostFormValue("newPassword")
	} else if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.CurrentPassword == "" {
		writeError(w, http.StatusBadRequest, "isi password sekarang dulu")
		return
	}
	account, err := s.store.UpdateAccount(r.Context(), body.CurrentPassword, body.Username, body.NewPassword)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrInvalidCredentials):
			s.writeAuthFailure(w, r, http.StatusUnauthorized, "password sekarang salah")
		default:
			s.writeAuthFailure(w, r, http.StatusBadRequest, err.Error())
		}
		return
	}
	_ = s.store.DeleteAllSessions(r.Context())
	s.expireSessionCookie(w, r)
	s.logger.Info("operator account updated", "username", account.Username)
	if wantsHTML(r) {
		s.setFlash(w, r, "Akun diperbarui. Semua sesi dikeluarkan; masuk lagi dengan password baru.")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	serveJSONToBrowser(w, r, http.StatusOK, map[string]any{
		"username":       account.Username,
		"reauthRequired": true,
		"reauthMessage":  "akun diperbarui; semua sesi dikeluarkan, silakan masuk lagi",
	})
}

// handleLogoutAll answers POST /api/account/logout-all.
func (s *Server) handleLogoutAll(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAllSessions(r.Context()); err != nil {
		s.logError("logout all", err)
		writeError(w, http.StatusInternalServerError, "database unavailable")
		return
	}
	s.expireSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionLifetime / time.Second),
		Expires:  time.Now().Add(sessionLifetime),
		HttpOnly: true,
		Secure:   requestIsSecure(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// requestIsSecure is read from the request, so the same page that received a
// non-Secure cookie over plain HTTP can delete it. Hard-coding Secure here
// would make browsers ignore the deletion on any non-TLS deployment.
func (s *Server) expireSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: true,
		Secure:   requestIsSecure(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// requestIsSecure honours a TLS-terminating proxy without trusting the header
// on a plain-HTTP connection that a client could forge: X-Forwarded-Proto only
// counts when the request arrived from loopback (the proxy) or over TLS.
func requestIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !isLoopbackRequest(r) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

// importLegacyToken carries CARTESIA_GW_TOKEN forward as the first client key,
// once per install, so scripts using the old token keep working.
func (s *Server) importLegacyToken(r *http.Request) {
	if s.cfg.GatewayToken == "" {
		return
	}
	claimed, err := s.store.LegacyTokenClaimed(r.Context())
	if err != nil || claimed {
		return
	}
	if _, err := s.store.ImportClientKey(r.Context(),
		"imported dari CARTESIA_GW_TOKEN", s.cfg.GatewayToken); err != nil &&
		!errors.Is(err, store.ErrDuplicateClientKey) {
		s.logger.Warn("import legacy gateway token", "error", err)
		return
	}
	if err := s.store.ClaimLegacyToken(r.Context()); err != nil {
		s.logger.Warn("record legacy token import", "error", err)
	}
}

// loginAttemptKey keeps raw addresses out of memory; the counter only needs to
// tell clients apart.
func keyForAttempt(r *http.Request) string {
	sum := sha256.Sum256([]byte(clientIP(r)))
	return hex.EncodeToString(sum[:8])
}

func (s *Server) loginAllowed(key string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	entry, ok := s.loginFailures[key]
	if !ok {
		return true
	}
	if time.Since(entry.first) > loginWindow {
		delete(s.loginFailures, key)
		return true
	}
	return entry.count < maxLoginFailures
}

func (s *Server) recordLoginFailure(key string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	entry := s.loginFailures[key]
	if time.Since(entry.first) > loginWindow {
		entry = loginEntry{first: time.Now()}
	}
	entry.count++
	s.loginFailures[key] = entry
	if len(s.loginFailures) > 1024 {
		// A map keyed by client is bounded: drop everything older than the
		// window rather than growing without limit.
		for k, v := range s.loginFailures {
			if time.Since(v.first) > loginWindow {
				delete(s.loginFailures, k)
			}
		}
	}
}

func (s *Server) clearLoginFailures(key string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	delete(s.loginFailures, key)
}

type loginEntry struct {
	first time.Time
	count int
}

// decodeJSON reads a small JSON body and turns decoder noise into a message the
// login screen can show.
func decodeJSON(r *http.Request, into any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxAuthBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return errors.New("body JSON tidak valid")
	}
	return nil
}
