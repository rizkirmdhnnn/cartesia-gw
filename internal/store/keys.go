package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrNoKeys means no active key exists; the gateway answers 503 with setup
// instructions rather than a confusing upstream error.
var ErrNoKeys = errors.New("no active cartesia api key configured")

// Cooldown bounds. A key that fails is parked with an exponential backoff and
// released automatically; nothing here is unbounded.
const (
	cooldownBase   = 60 * time.Second
	cooldownMax    = 30 * time.Minute
	creditCooldown = 30 * time.Minute
)

// Key is one Cartesia API key plus its failure state.
type Key struct {
	ID            int64  `json:"id"`
	Label         string `json:"label"`
	Secret        string `json:"secret"`
	IsActive      bool   `json:"isActive"`
	Strikes       int    `json:"strikes"`
	CooldownUntil int64  `json:"cooldownUntil"`
	LastError     string `json:"lastError"`
	LastUsedAt    int64  `json:"lastUsedAt"`
	CreatedAt     int64  `json:"createdAt"`
}

// Cooling reports whether the key is currently parked.
func (k Key) Cooling(now time.Time) bool { return k.CooldownUntil > now.Unix() }

const keyColumns = `id, label, secret, is_active, strikes, cooldown_until, last_error, last_used_at, created_at`

// CreateKey inserts a new key. The secret is unique, so pasting the same key
// twice is a visible error rather than a silent duplicate that doubles the
// rotation weight.
func (s *Store) CreateKey(ctx context.Context, label, secret string) (Key, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return Key{}, errors.New("api key must not be empty")
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO keys (label, secret, is_active, created_at) VALUES (?, ?, 1, ?)`,
		strings.TrimSpace(label), secret, time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Key{}, errors.New("that api key is already stored")
		}
		return Key{}, fmt.Errorf("insert key: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Key{}, fmt.Errorf("insert key id: %w", err)
	}
	return s.GetKey(ctx, id)
}

// GetKey reads one key by id.
func (s *Store) GetKey(ctx context.Context, id int64) (Key, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+keyColumns+` FROM keys WHERE id = ?`, id)
	return scanKey(row)
}

// ListKeys returns every key, newest first.
func (s *Store) ListKeys(ctx context.Context) ([]Key, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+keyColumns+` FROM keys ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list keys: %w", err)
	}
	defer rows.Close()

	keys := make([]Key, 0, 4)
	for rows.Next() {
		key, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// SetKeyActive enables or disables a key.
func (s *Store) SetKeyActive(ctx context.Context, id int64, active bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE keys SET is_active = ? WHERE id = ?`, boolToInt(active), id)
	if err != nil {
		return fmt.Errorf("update key %d: %w", id, err)
	}
	return nil
}

// DeleteKey removes a key.
func (s *Store) DeleteKey(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM keys WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete key %d: %w", id, err)
	}
	return nil
}

// ResetCooldown lifts a cooldown and clears the strike count.
func (s *Store) ResetCooldown(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE keys SET cooldown_until = 0, strikes = 0, last_error = '' WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("reset key %d: %w", id, err)
	}
	return nil
}

// PickKey returns the next key to try and whether it is still cooling down.
//
// Which key that is depends on the active strategy:
//
//   - least-recently-used: outside its cooldown first, then least recently
//     used, so an idle key is preferred over one that just answered.
//   - round-robin: the next id after the last one picked, wrapping at the end,
//     so the order is predictable.
//   - random: any key, with parked ones deprioritised.
//   - sticky: the most recently used key, so one key carries the traffic until
//     it is parked and the others keep their quota.
//
// Every strategy falls back to the earliest-released parked key rather than
// refusing to try anything: a short upstream hiccup should not become an
// outage. The caller is told the key is cooling down so it can say so.
func (s *Store) PickKey(ctx context.Context) (Key, bool, error) {
	strategy, err := s.RotationStrategy(ctx)
	if err != nil {
		return Key{}, false, err
	}
	switch strategy {
	case StrategyRoundRobin:
		return s.pickRoundRobin(ctx)
	case StrategyRandom:
		return s.pickWith(ctx, `
			SELECT `+keyColumns+` FROM keys
			WHERE is_active = 1
			ORDER BY (cooldown_until <= ?) DESC, RANDOM()
			LIMIT 1`, time.Now().Unix())
	case StrategySticky:
		return s.pickWith(ctx, `
			SELECT `+keyColumns+` FROM keys
			WHERE is_active = 1
			ORDER BY (cooldown_until <= ?) DESC, last_used_ms DESC, id DESC
			LIMIT 1`, time.Now().Unix())
	default:
		return s.pickWith(ctx, `
			SELECT `+keyColumns+` FROM keys
			WHERE is_active = 1
			ORDER BY (cooldown_until <= ?) DESC, last_used_ms ASC, id ASC
			LIMIT 1`, time.Now().Unix())
	}
}

// pickRoundRobin walks ids in order. The cursor is stored, not derived from
// last_used_at, so a key touched by a health check does not shift the cycle.
func (s *Store) pickRoundRobin(ctx context.Context) (Key, bool, error) {
	now := time.Now()
	cursor, err := s.rotationCursor(ctx)
	if err != nil {
		return Key{}, false, err
	}

	key, err := s.queryKey(ctx, `
		SELECT `+keyColumns+` FROM keys
		WHERE is_active = 1 AND cooldown_until <= ? AND id > ?
		ORDER BY id
		LIMIT 1`, now.Unix(), cursor)
	if errors.Is(err, sql.ErrNoRows) {
		// Wrap: the next key is the first one, skipping parked keys.
		key, err = s.queryKey(ctx, `
			SELECT `+keyColumns+` FROM keys
			WHERE is_active = 1 AND cooldown_until <= ?
			ORDER BY id
			LIMIT 1`, now.Unix())
	}
	if errors.Is(err, sql.ErrNoRows) {
		// Everything is parked. Take the one released soonest.
		key, err = s.queryKey(ctx, `
			SELECT `+keyColumns+` FROM keys
			WHERE is_active = 1
			ORDER BY cooldown_until ASC, id ASC
			LIMIT 1`)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Key{}, false, ErrNoKeys
	}
	if err != nil {
		return Key{}, false, err
	}

	if err := s.SetSetting(ctx, settingRotationCursor, strconv.FormatInt(key.ID, 10)); err != nil {
		return Key{}, false, err
	}
	return s.finishPick(ctx, key, now)
}

// pickWith runs a strategy query and treats "no rows" as an empty key set.
func (s *Store) pickWith(ctx context.Context, query string, args ...any) (Key, bool, error) {
	now := time.Now()
	key, err := s.queryKey(ctx, query, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return Key{}, false, ErrNoKeys
	}
	if err != nil {
		return Key{}, false, err
	}
	return s.finishPick(ctx, key, now)
}

// finishPick records the use and reports whether the key is parked.
//
// The order is kept in milliseconds while the dashboard reads the second
// column: two picks inside the same second would otherwise tie, and the
// least-recently-used strategy would fall through to id order and hammer the
// lowest id for the rest of that second.
func (s *Store) finishPick(ctx context.Context, key Key, now time.Time) (Key, bool, error) {
	_, err := s.db.ExecContext(ctx,
		`UPDATE keys SET last_used_at = ?, last_used_ms = ? WHERE id = ?`,
		now.Unix(), now.UnixMilli(), key.ID)
	if err != nil {
		return Key{}, false, fmt.Errorf("touch key %d: %w", key.ID, err)
	}
	return key, key.Cooling(now), nil
}

func (s *Store) queryKey(ctx context.Context, query string, args ...any) (Key, error) {
	return scanKey(s.db.QueryRowContext(ctx, query, args...))
}

// MarkSuccess clears the failure state of a key.
func (s *Store) MarkSuccess(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE keys SET strikes = 0, cooldown_until = 0, last_error = '' WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("mark key %d healthy: %w", id, err)
	}
	return nil
}

// MarkFailure parks a key with an exponential backoff. Credit exhaustion is
// parked for the maximum immediately: waiting 60s to retry a key that has no
// balance left is just noise.
func (s *Store) MarkFailure(ctx context.Context, id int64, status int, message string, retryAfter time.Duration) error {
	key, err := s.GetKey(ctx, id)
	if err != nil {
		return err
	}
	strikes := key.Strikes + 1

	cooldown := cooldownBase * time.Duration(int64(1)<<min(strikes-1, 5))
	if status == 402 {
		cooldown = creditCooldown
	}
	if cooldown > cooldownMax {
		cooldown = cooldownMax
	}
	if retryAfter > cooldown {
		cooldown = min(retryAfter, time.Hour)
	}

	_, err = s.db.ExecContext(ctx, `
		UPDATE keys SET strikes = ?, cooldown_until = ?, last_error = ? WHERE id = ?`,
		strikes, time.Now().Add(cooldown).Unix(), describe(status, message), id)
	if err != nil {
		return fmt.Errorf("mark key %d failed: %w", id, err)
	}
	return nil
}

// KeyStats counts keys by state for the dashboard.
type KeyStats struct {
	Total   int `json:"total"`
	Active  int `json:"active"`
	Cooling int `json:"cooling"`
}

// CountKeys summarises key state.
func (s *Store) CountKeys(ctx context.Context) (KeyStats, error) {
	var stats KeyStats
	now := time.Now().Unix()
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN is_active = 1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN is_active = 1 AND cooldown_until > ? THEN 1 ELSE 0 END), 0)
		FROM keys`, now).Scan(&stats.Total, &stats.Active, &stats.Cooling)
	if err != nil {
		return KeyStats{}, fmt.Errorf("count keys: %w", err)
	}
	return stats, nil
}

func describe(status int, message string) string {
	message = strings.TrimSpace(message)
	if len(message) > 300 {
		message = message[:300]
	}
	if message == "" {
		return fmt.Sprintf("HTTP %d", status)
	}
	return fmt.Sprintf("HTTP %d: %s", status, message)
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

type scanner interface {
	Scan(dest ...any) error
}

func scanKey(row scanner) (Key, error) {
	var (
		key    Key
		active int
	)
	if err := row.Scan(&key.ID, &key.Label, &key.Secret, &active, &key.Strikes,
		&key.CooldownUntil, &key.LastError, &key.LastUsedAt, &key.CreatedAt); err != nil {
		return Key{}, err
	}
	key.IsActive = active == 1
	return key, nil
}
