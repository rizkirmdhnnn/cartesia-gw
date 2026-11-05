package gateway

import (
	"errors"
	"net/http"
	"strings"
)

// Credential bodies may arrive as JSON (the dashboard) or as a form POST (a
// browser that submitted natively because JavaScript was not ready). Both are
// read into the same struct so the handlers stay unaware of the encoding.

func isFormPost(r *http.Request) bool {
	contentType := r.Header.Get("Content-Type")
	return strings.HasPrefix(contentType, "application/x-www-form-urlencoded") ||
		strings.HasPrefix(contentType, "multipart/form-data")
}

// decodeCredentials reads {username,password} from JSON or a form body. A form
// submission is always a top-level navigation, so when it fails we answer with
// HTML instead of a JSON blob the browser would download.
func (s *Server) decodeCredentials(r *http.Request, into *struct {
	Username string `json:"username"`
	Password string `json:"password"`
}) error {
	if isFormPost(r) {
		// ParseForm handles both encodings. A malformed body is the only real
		// failure here; the body size is already bounded by the server.
		if err := r.ParseForm(); err != nil {
			return errors.New("body form tidak valid")
		}
		into.Username = r.PostFormValue("username")
		into.Password = r.PostFormValue("password")
		if into.Username == "" && into.Password == "" {
			return errors.New("username dan password wajib diisi")
		}
		return nil
	}
	return decodeJSON(r, into)
}

// wantsHTML reports whether a non-JavaScript client (a navigated form) is
// asking, so the error can be re-rendered as a page instead of JSON.
func wantsHTML(r *http.Request) bool {
	return isFormPost(r) && strings.Contains(r.Header.Get("Accept"), "text/html")
}

func (s *Server) writeAuthFailure(w http.ResponseWriter, r *http.Request, status int, message string) {
	if wantsHTML(r) {
		// 303 back to the gate with the reason in a query-free location: the
		// message rides a short-lived cookie, never the URL.
		s.setFlash(w, r, message)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	writeError(w, status, message)
}

// serveJSONToBrowser answers a native form submission that reached an API
// route: the browser must never be left staring at raw JSON. Interactive
// callers (fetch with Accept: application/json) keep getting the JSON.
func serveJSONToBrowser(w http.ResponseWriter, r *http.Request, status int, body any) {
	if isFormPost(r) && !strings.Contains(r.Header.Get("Accept"), "application/json") {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	writeJSON(w, status, body)
}

// Flash messages are one-shot notices for the login page, kept in a cookie so
// no credential or reason ends up in a URL or the history.
const flashCookieName = "cartesia_gw_flash"

func (s *Server) setFlash(w http.ResponseWriter, r *http.Request, message string) {
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    message,
		Path:     "/",
		MaxAge:   30,
		HttpOnly: true,
		Secure:   requestIsSecure(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// popFlash reads and clears the notice, if any.
func (s *Server) popFlash(w http.ResponseWriter, r *http.Request) string {
	cookie, err := r.Cookie(flashCookieName)
	if err != nil || cookie.Value == "" {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name: flashCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: requestIsSecure(r), SameSite: http.SameSiteStrictMode,
	})
	return cookie.Value
}

// handleRedirectCredentials answers a request that still carries username /
// password in the query string. Nothing is logged from the URL, nothing is
// authenticated, and the browser is sent to "/" which renders the gate and its
// script immediately scrubs the address bar (history.replaceState). Without
// JavaScript, the answer is a plain page with a meta refresh, so the URL never
// survives in history.
func handleRedirectCredentials(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8">
<meta name="referrer" content="no-referrer">
<meta http-equiv="refresh" content="0; url=/">
<title>cartesia-gw</title></head><body>
<p>Kredensial tidak boleh lewat URL. <a href="/">Lanjut ke login</a>.</p>
<script>location.replace('/');</script>
</body></html>`))
}

// rejectCredentialsInURL is a guard for the old form bug: a request that still
// carries username/password in the query string is refused without touching
// the store, and the answer never echoes them back.
func rejectCredentialsInURL(w http.ResponseWriter, r *http.Request) bool {
	query := r.URL.Query()
	if !query.Has("password") && !query.Has("username") {
		return false
	}
	handleRedirectCredentials(w, r)
	return true
}
