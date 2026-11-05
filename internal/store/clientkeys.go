package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Client-key constants. The prefix makes a leaked key recognisable in a log or
// a screenshot; the 32 random bytes are what actually guard the endpoint.
const (
	ClientKeyPrefix  = "dk_"
	clientKeyBytes   = 32
	ClientKeyDisplay = 12 // "dk_" + 9 characters, enough to tell keys apart
	maxClientLabel   = 64
)

// ErrClientKeyRejected is returned by AuthenticateClientKey for any reason a
// key cannot be used. The caller must not distinguish invalid, disabled and
// expired: a client that learns which one it holds gains nothing.
var ErrClientKeyRejected = errors.New("invalid or inactive client api key")

// ErrDuplicateClientKey means the same key already exists.
var ErrDuplicateClientKey = errors.New("that client api key already exists")

// ClientKey is a gateway credential. TokenHash is never exposed by the API.
type ClientKey struct {
	ID           int64  `json:"id"`
	Label        string `json:"label"`
	Prefix       string `json:"prefix"`
	IsActive     bool   `json:"isActive"`
	CreatedAt    int64  `json:"createdAt"`
	LastUsedAt   int64  `json:"lastUsedAt"`
	RequestCount int64  `json:"requestCount"`
	ExpiresAt    int64  `json:"expiresAt"`
}

// Expired reports whether the key is past its expiry (0 = no expiry).
func (k ClientKey) Expired(now time.Time) bool {
	return k.ExpiresAt > 0 && k.ExpiresAt <= now.Unix()
}

// GenerateClientKey returns a new secret. It is shown to the operator exactly
// once; only its hash is stored.
func GenerateClientKey() (string, error) {
	buf := make([]byte, clientKeyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return ClientKeyPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// CreateClientKey stores a new key and returns it together with the secret.
// expiresIn of 0 means no expiry.
func (s *Store) CreateClientKey(ctx context.Context, label string, expiresIn time.Duration) (ClientKey, string, error) {
	secret, err := GenerateClientKey()
	if err != nil {
		return ClientKey{}, "", err
	}
	label = strings.TrimSpace(label)
	if len(label) > maxClientLabel {
		return ClientKey{}, "", fmt.Errorf("label must be at most %d characters", maxClientLabel)
	}

	now := time.Now()
	var expires int64
	if expiresIn > 0 {
		expires = now.Add(expiresIn).Unix()
	}
	display := secret
	if len(display) > ClientKeyDisplay {
		display = display[:ClientKeyDisplay]
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO client_keys (label, prefix, token_hash, is_active, created_at, expires_at)
		VALUES (?, ?, ?, 1, ?, ?)`,
		label, display, hashTokenValue(secret), now.Unix(), expires)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ClientKey{}, "", ErrDuplicateClientKey
		}
		return ClientKey{}, "", fmt.Errorf("insert client key: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ClientKey{}, "", fmt.Errorf("insert client key id: %w", err)
	}
	key, err := s.GetClientKey(ctx, id)
	if err != nil {
		return ClientKey{}, "", err
	}
	return key, secret, nil
}

// ImportClientKey stores an externally supplied secret, used once to carry the
// old CARTESIA_GW_TOKEN forward so existing clients keep working after an
// upgrade. A key that is already present is reported as a duplicate, which the
// caller treats as "already imported".
func (s *Store) ImportClientKey(ctx context.Context, label, secret string) (ClientKey, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ClientKey{}, errors.New("api key must not be empty")
	}
	display := secret
	if len(display) > ClientKeyDisplay {
		display = display[:ClientKeyDisplay]
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO client_keys (label, prefix, token_hash, is_active, created_at)
		VALUES (?, ?, ?, 1, ?)`,
		strings.TrimSpace(label), display, hashTokenValue(secret), time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ClientKey{}, ErrDuplicateClientKey
		}
		return ClientKey{}, fmt.Errorf("import client key: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ClientKey{}, fmt.Errorf("import client key id: %w", err)
	}
	return s.GetClientKey(ctx, id)
}

// GetClientKey reads one key by id.
func (s *Store) GetClientKey(ctx context.Context, id int64) (ClientKey, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, label, prefix, is_active, created_at, last_used_at, request_count, expires_at
		FROM client_keys WHERE id = ?`, id)
	return scanClientKey(row)
}

// ListClientKeys returns every key, newest first.
func (s *Store) ListClientKeys(ctx context.Context) ([]ClientKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, label, prefix, is_active, created_at, last_used_at, request_count, expires_at
		FROM client_keys ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list client keys: %w", err)
	}
	defer rows.Close()

	keys := make([]ClientKey, 0, 4)
	for rows.Next() {
		key, err := scanClientKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// SetClientKeyActive enables or disables a key. A disabled key is rejected on
// the very next request; nothing is cached in the request path.
func (s *Store) SetClientKeyActive(ctx context.Context, id int64, active bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE client_keys SET is_active = ? WHERE id = ?`, boolToInt(active), id)
	if err != nil {
		return fmt.Errorf("update client key %d: %w", id, err)
	}
	return nil
}

// DeleteClientKey removes a key permanently.
func (s *Store) DeleteClientKey(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM client_keys WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete client key %d: %w", id, err)
	}
	return nil
}

// AuthenticateClientKey resolves a presented secret to its key and records the
// use. Every rejection is the same error, and a present-but-wrong key costs
// one indexed lookup rather than a scan.
func (s *Store) AuthenticateClientKey(ctx context.Context, presented string) (ClientKey, error) {
	presented = strings.TrimSpace(presented)
	if presented == "" {
		return ClientKey{}, ErrClientKeyRejected
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, label, prefix, is_active, created_at, last_used_at, request_count, expires_at
		FROM client_keys WHERE token_hash = ?`, hashTokenValue(presented))
	key, err := scanClientKey(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ClientKey{}, ErrClientKeyRejected
	}
	if err != nil {
		return ClientKey{}, err
	}
	if !key.IsActive || key.Expired(time.Now()) {
		return ClientKey{}, ErrClientKeyRejected
	}

	now := time.Now().Unix()
	if _, err := s.db.ExecContext(ctx, `
		UPDATE client_keys SET last_used_at = ?, request_count = request_count + 1
		WHERE id = ?`, now, key.ID); err != nil {
		// The request itself is still authorised; a failed counter update must
		// not turn into a 500 the client has to retry.
		return key, nil
	}
	key.LastUsedAt = now
	key.RequestCount++
	return key, nil
}

// CountClientKeys summarises keys for the dashboard.
type ClientKeyStats struct {
	Total   int `json:"total"`
	Active  int `json:"active"`
	Expired int `json:"expired"`
}

// CountClientKeys reports totals.
func (s *Store) CountClientKeys(ctx context.Context) (ClientKeyStats, error) {
	var stats ClientKeyStats
	now := time.Now().Unix()
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN is_active = 1 AND (expires_at = 0 OR expires_at > ?) THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN expires_at > 0 AND expires_at <= ? THEN 1 ELSE 0 END), 0)
		FROM client_keys`, now, now).Scan(&stats.Total, &stats.Active, &stats.Expired)
	if err != nil {
		return ClientKeyStats{}, fmt.Errorf("count client keys: %w", err)
	}
	return stats, nil
}

func scanClientKey(row scanner) (ClientKey, error) {
	var (
		key    ClientKey
		active int
	)
	if err := row.Scan(&key.ID, &key.Label, &key.Prefix, &active, &key.CreatedAt,
		&key.LastUsedAt, &key.RequestCount, &key.ExpiresAt); err != nil {
		return ClientKey{}, err
	}
	key.IsActive = active == 1
	return key, nil
}
