package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Session durations. Absolute expiry is fixed and short enough that a stolen
// cookie is not usable forever; the last_seen column is refreshed on use so an
// operator can see which sessions are alive.
const (
	SessionTTL        = 7 * 24 * time.Hour
	SessionTouchEvery = time.Hour
	maxUserAgentLen   = 200
)

// ErrNoSession covers both "no cookie" and "cookie no longer valid". The caller
// must not distinguish them.
var ErrNoSession = errors.New("session not found or expired")

// Session is one logged-in browser.
type Session struct {
	ID        int64  `json:"id"`
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt int64  `json:"expiresAt"`
	LastSeen  int64  `json:"lastSeen"`
	UserAgent string `json:"userAgent"`
	Remote    string `json:"remote"`
}

const sessionColumns = `id, created_at, expires_at, last_seen, user_agent, remote`

// CreateSession stores a new session and returns the cookie value. Only the
// SHA-256 of the value is persisted: a database dump does not hand over live
// sessions, and the cookie remains the only copy of the secret.
//
// The value is also written as the session generation, so a session is valid
// from the moment it is issued; any later password change or "log out
// everywhere" rotates that generation and invalidates every earlier cookie.
func (s *Store) CreateSession(ctx context.Context, userAgent, remote string) (string, Session, error) {
	token, err := newRandomToken()
	if err != nil {
		return "", Session{}, err
	}
	now := time.Now()
	session := Session{
		CreatedAt: now.Unix(),
		ExpiresAt: now.Add(SessionTTL).Unix(),
		LastSeen:  now.Unix(),
		UserAgent: truncate(userAgent, maxUserAgentLen),
		Remote:    truncate(remote, 64),
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	// Opportunistic cleanup: every login trims what has already expired, so the
	// table cannot grow without bound even if nothing else prunes it.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.Unix()); err != nil {
		return "", Session{}, fmt.Errorf("prune sessions: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (token_hash, created_at, expires_at, last_seen, user_agent, remote)
		VALUES (?, ?, ?, ?, ?, ?)`,
		hashTokenValue(token), session.CreatedAt, session.ExpiresAt, session.LastSeen,
		session.UserAgent, session.Remote)
	if err != nil {
		return "", Session{}, fmt.Errorf("insert session: %w", err)
	}
	if session.ID, err = res.LastInsertId(); err != nil {
		return "", Session{}, fmt.Errorf("insert session id: %w", err)
	}
	// CreateSession holds writeMu, so the generation is written with a direct
	// statement rather than SetSetting's own path.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, settingAuthSessionID, hashTokenValue(token)); err != nil {
		return "", Session{}, fmt.Errorf("write session generation: %w", err)
	}
	return token, session, nil
}

// LookupSession resolves a cookie value to a live session. last_seen is only
// written once per SessionTouchEvery window, so a busy dashboard does not turn
// every request into a write.
func (s *Store) LookupSession(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrNoSession
	}
	var session Session
	row := s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+`
		FROM sessions WHERE token_hash = ?`, hashTokenValue(token))
	if err := row.Scan(&session.ID, &session.CreatedAt, &session.ExpiresAt,
		&session.LastSeen, &session.UserAgent, &session.Remote); err != nil {
		return Session{}, ErrNoSession
	}
	now := time.Now()
	if session.ExpiresAt <= now.Unix() {
		_ = s.DeleteSession(ctx, token)
		return Session{}, ErrNoSession
	}
	if now.Unix()-session.LastSeen >= int64(SessionTouchEvery/time.Second) {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE sessions SET last_seen = ? WHERE id = ?`, now.Unix(), session.ID); err == nil {
			session.LastSeen = now.Unix()
		}
	}
	return session, nil
}

// DeleteSession logs one browser out.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE token_hash = ?`, hashTokenValue(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteAllSessions logs every browser out.
func (s *Store) DeleteAllSessions(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return fmt.Errorf("delete sessions: %w", err)
	}
	return nil
}

// ListSessions returns live sessions, newest first. Cookie values cannot be
// recovered from this, only their hashes are stored.
func (s *Store) ListSessions(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionColumns+`
		FROM sessions WHERE expires_at > ? ORDER BY id DESC`, time.Now().Unix())
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	sessions := make([]Session, 0, 4)
	for rows.Next() {
		var session Session
		if err := rows.Scan(&session.ID, &session.CreatedAt, &session.ExpiresAt,
			&session.LastSeen, &session.UserAgent, &session.Remote); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}
