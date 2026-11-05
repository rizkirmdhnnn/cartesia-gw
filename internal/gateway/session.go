package gateway

import (
	"context"
	"net/http"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

// The session lives on the request context rather than in a handler field, so
// a request can never observe another request's session.

type sessionCtxKey struct{}

func withSession(r *http.Request, session store.Session) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), sessionCtxKey{}, session))
}

func sessionFrom(r *http.Request) *store.Session {
	session, ok := r.Context().Value(sessionCtxKey{}).(store.Session)
	if !ok {
		return nil
	}
	return &session
}

func sessionCookie(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// The client key that authorised a request rides along the same way, so the
// request log can attribute usage without a second lookup.

type clientKeyCtxKey struct{}

func withClientKey(r *http.Request, key store.ClientKey) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), clientKeyCtxKey{}, key))
}

// clientKeyFrom returns the authenticated client key, or zero for a dashboard
// session or an open loopback request.
func clientKeyFrom(r *http.Request) store.ClientKey {
	key, _ := r.Context().Value(clientKeyCtxKey{}).(store.ClientKey)
	return key
}

// clientIDFromContext reads the client id from a (possibly detached) context,
// for the request log.
func clientIDFromContext(ctx context.Context) int64 {
	key, _ := ctx.Value(clientKeyCtxKey{}).(store.ClientKey)
	return key.ID
}
