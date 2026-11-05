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

// ErrNoKeys means no active key exists for the requested provider; the gateway
// answers 503 with setup instructions rather than a confusing upstream error.
var ErrNoKeys = errors.New("no active api key configured")

// Cooldown bounds. A key that fails is parked with an exponential backoff and
// released automatically; nothing here is unbounded.
const (
	cooldownBase   = 60 * time.Second
	cooldownMax    = 30 * time.Minute
	creditCooldown = 30 * time.Minute
)

// DefaultProvider is what a key gets when the caller does not name one. It is
// the provider this gateway shipped with, so an upgraded database keeps its
// existing rows pointing where they always did.
const DefaultProvider = "cartesia"

// Key is one upstream API key plus its failure state.
type Key struct {
	ID            int64  `json:"id"`
	Label         string `json:"label"`
	Provider      string `json:"provider"`
	Secret        string `json:"secret"`
	IsActive      bool   `json:"isActive"`
	Priority      int    `json:"priority"`
	Strikes       int    `json:"strikes"`
	CooldownUntil int64  `json:"cooldownUntil"`
	LastError     string `json:"lastError"`
	LastUsedAt    int64  `json:"lastUsedAt"`
	CreatedAt     int64  `json:"createdAt"`
}

// Cooling reports whether the key is currently parked.
func (k Key) Cooling(now time.Time) bool { return k.CooldownUntil > now.Unix() }

// Eligible reports whether the picker may choose the key right now. It mirrors
// the WHERE clause of the pick queries: enabled keys only, and parked keys only
// when nothing else is left. The visualiser uses it to dim a key.
func (k Key) Eligible(now time.Time) bool {
	return k.IsActive && !k.Cooling(now)
}

const keyColumns = `id, label, provider, secret, is_active, priority, strikes, cooldown_until, last_error, last_used_at, created_at`

// NormalizeProvider lower-cases a provider name and defaults an empty value.
func NormalizeProvider(provider string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" {
		return DefaultProvider
	}
	return p
}

// CreateKey inserts a new key. The secret is unique, so pasting the same key
// twice is a visible error rather than a silent duplicate that doubles the
// rotation weight.
func (s *Store) CreateKey(ctx context.Context, label, secret string) (Key, error) {
	return s.CreateKeyFor(ctx, DefaultProvider, label, secret)
}

// CreateKeyFor inserts a key for one provider.
func (s *Store) CreateKeyFor(ctx context.Context, provider, label, secret string) (Key, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return Key{}, errors.New("api key must not be empty")
	}
	provider = NormalizeProvider(provider)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO keys (label, provider, secret, is_active, created_at) VALUES (?, ?, ?, 1, ?)`,
		strings.TrimSpace(label), provider, secret, time.Now().Unix())
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

// orderedKeys sorts by priority under the priority strategy, so the
// dashboard agrees with the picker about which key comes first.
func (s *Store) OrderedKeys(ctx context.Context, strategy Strategy) ([]Key, error) {
	query := `SELECT ` + keyColumns + ` FROM keys ORDER BY provider ASC, id DESC`
	if strategy == StrategyPriority {
		query = `SELECT ` + keyColumns + ` FROM keys ORDER BY provider ASC, priority ASC, id ASC`
	}
	return s.scanKeys(ctx, query)
}

// ListKeys returns every key, newest first.
func (s *Store) ListKeys(ctx context.Context) ([]Key, error) {
	return s.scanKeys(ctx, `SELECT `+keyColumns+` FROM keys ORDER BY provider ASC, id DESC`)
}

// KeysFor returns the keys of one provider, newest first.
func (s *Store) KeysFor(ctx context.Context, provider string) ([]Key, error) {
	return s.scanKeys(ctx, `SELECT `+keyColumns+` FROM keys WHERE provider = ? ORDER BY id DESC`,
		NormalizeProvider(provider))
}

func (s *Store) scanKeys(ctx context.Context, query string, args ...any) ([]Key, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
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

// PickKey returns the next key of the default provider to try and whether it
// is still cooling down.
func (s *Store) PickKey(ctx context.Context) (Key, bool, error) {
	return s.PickKeyFor(ctx, DefaultProvider)
}

// PickKeyFor returns the next key of one provider.
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
//   - priority: the operator's numeric order, least recently used among
//     equals.
//
// Rotation is per provider: a Cartesia request never borrows an ElevenLabs
// key, because the two are billed and limited separately. Every strategy falls
// back to the earliest-released parked key rather than refusing to try
// anything: a short upstream hiccup should not become an outage.
func (s *Store) PickKeyFor(ctx context.Context, provider string) (Key, bool, error) {
	provider = NormalizeProvider(provider)
	strategy, err := s.RotationStrategy(ctx)
	if err != nil {
		return Key{}, false, err
	}
	now := time.Now()
	args := []any{provider, now.Unix()}
	switch strategy {
	case StrategyRoundRobin:
		return s.pickRoundRobin(ctx, provider)
	case StrategyRandom:
		return s.pickWith(ctx, `
			SELECT `+keyColumns+` FROM keys
			WHERE provider = ? AND is_active = 1
			ORDER BY (cooldown_until <= ?) DESC, RANDOM()
			LIMIT 1`, args...)
	case StrategySticky:
		return s.pickWith(ctx, `
			SELECT `+keyColumns+` FROM keys
			WHERE provider = ? AND is_active = 1
			ORDER BY (cooldown_until <= ?) DESC, last_used_ms DESC, id DESC
			LIMIT 1`, args...)
	case StrategyPriority:
		return s.pickWith(ctx, `
			SELECT `+keyColumns+` FROM keys
			WHERE provider = ? AND is_active = 1
			ORDER BY (cooldown_until <= ?) DESC, priority ASC, last_used_ms ASC, id ASC
			LIMIT 1`, args...)
	default:
		return s.pickWith(ctx, `
			SELECT `+keyColumns+` FROM keys
			WHERE provider = ? AND is_active = 1
			ORDER BY (cooldown_until <= ?) DESC, last_used_ms ASC, id ASC
			LIMIT 1`, args...)
	}
}

// pickRoundRobin walks ids in order within one provider. The cursor is stored
// per provider, not derived from last_used_at, so a key touched by a lookup
// does not shift the cycle.
func (s *Store) pickRoundRobin(ctx context.Context, provider string) (Key, bool, error) {
	now := time.Now()
	cursor, err := s.rotationCursor(ctx, provider)
	if err != nil {
		return Key{}, false, err
	}

	key, err := s.queryKey(ctx, `
		SELECT `+keyColumns+` FROM keys
		WHERE provider = ? AND is_active = 1 AND cooldown_until <= ? AND id > ?
		ORDER BY id
		LIMIT 1`, provider, now.Unix(), cursor)
	if errors.Is(err, sql.ErrNoRows) {
		// Wrap: the next key is the first one, skipping parked keys.
		key, err = s.queryKey(ctx, `
			SELECT `+keyColumns+` FROM keys
			WHERE provider = ? AND is_active = 1 AND cooldown_until <= ?
			ORDER BY id
			LIMIT 1`, provider, now.Unix())
	}
	if errors.Is(err, sql.ErrNoRows) {
		// Everything is parked. Take the one released soonest.
		key, err = s.queryKey(ctx, `
			SELECT `+keyColumns+` FROM keys
			WHERE provider = ? AND is_active = 1
			ORDER BY cooldown_until ASC, id ASC
			LIMIT 1`, provider)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Key{}, false, ErrNoKeys
	}
	if err != nil {
		return Key{}, false, err
	}

	if err := s.SetSetting(ctx, rotationCursorKey(provider), strconv.FormatInt(key.ID, 10)); err != nil {
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

// PickPreview returns the id of the default provider's key that the active
// strategy would choose next, computed with the same ORDER BY as PickKey but
// without recording the use.
func (s *Store) PickPreview(ctx context.Context) (int64, error) {
	return s.PickPreviewFor(ctx, DefaultProvider)
}

// PickPreviewFor is the per-provider "next up".
func (s *Store) PickPreviewFor(ctx context.Context, provider string) (int64, error) {
	provider = NormalizeProvider(provider)
	strategy, err := s.RotationStrategy(ctx)
	if err != nil {
		return 0, err
	}
	if strategy == StrategyRoundRobin {
		return s.previewRoundRobin(ctx, provider)
	}
	now := time.Now().Unix()
	var sqlText string
	switch strategy {
	case StrategySticky:
		sqlText = `SELECT id FROM keys WHERE provider = ? AND is_active = 1
			ORDER BY (cooldown_until <= ?) DESC, last_used_ms DESC, id DESC LIMIT 1`
	case StrategyRandom:
		sqlText = `SELECT id FROM keys WHERE provider = ? AND is_active = 1
			ORDER BY (cooldown_until <= ?) DESC, id LIMIT 1`
	case StrategyPriority:
		sqlText = `SELECT id FROM keys WHERE provider = ? AND is_active = 1
			ORDER BY (cooldown_until <= ?) DESC, priority ASC, last_used_ms ASC, id ASC LIMIT 1`
	default:
		sqlText = `SELECT id FROM keys WHERE provider = ? AND is_active = 1
			ORDER BY (cooldown_until <= ?) DESC, last_used_ms ASC, id ASC LIMIT 1`
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, sqlText, provider, now).Scan(&id); err != nil {
		return 0, nil // no keys: nothing is next
	}
	return id, nil
}

// previewRoundRobin mirrors pickRoundRobin's two-step walk.
func (s *Store) previewRoundRobin(ctx context.Context, provider string) (int64, error) {
	now := time.Now().Unix()
	cursor, err := s.rotationCursor(ctx, provider)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM keys
		WHERE provider = ? AND is_active = 1 AND cooldown_until <= ? AND id > ? ORDER BY id LIMIT 1`,
		provider, now, cursor).Scan(&id)
	if err == nil {
		return id, nil
	}
	err = s.db.QueryRowContext(ctx, `SELECT id FROM keys
		WHERE provider = ? AND is_active = 1 AND cooldown_until <= ? ORDER BY id LIMIT 1`,
		provider, now).Scan(&id)
	if err != nil {
		return 0, nil
	}
	return id, nil
}

// SetKeyPriority stores the manual precedence used by the priority strategy.
// Lower number = tried first. Values are bounded so a typo cannot park a key
// behind thousands of others.
func (s *Store) SetKeyPriority(ctx context.Context, id int64, priority int) error {
	if priority < 0 || priority > 100 {
		return errors.New("priority must be between 0 and 100")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE keys SET priority = ? WHERE id = ?`, priority, id)
	if err != nil {
		return fmt.Errorf("update key %d priority: %w", id, err)
	}
	return nil
}

// KeyStats counts keys by state for the dashboard.
type KeyStats struct {
	Total   int `json:"total"`
	Active  int `json:"active"`
	Cooling int `json:"cooling"`
	// Providers is the same count split by provider, for the console's
	// per-provider segments.
	Providers map[string]ProviderStats `json:"providers"`
}

// ProviderStats is one provider's key count.
type ProviderStats struct {
	Total   int `json:"total"`
	Active  int `json:"active"`
	Cooling int `json:"cooling"`
}

// CountKeys summarises key state.
func (s *Store) CountKeys(ctx context.Context) (KeyStats, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT provider,
			COUNT(*),
			COALESCE(SUM(CASE WHEN is_active = 1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN is_active = 1 AND cooldown_until > ? THEN 1 ELSE 0 END), 0)
		FROM keys
		GROUP BY provider`, time.Now().Unix())
	if err != nil {
		return KeyStats{}, fmt.Errorf("count keys: %w", err)
	}
	defer rows.Close()

	stats := KeyStats{Providers: map[string]ProviderStats{}}
	for rows.Next() {
		var provider string
		var total, active, cooling int
		if err := rows.Scan(&provider, &total, &active, &cooling); err != nil {
			return KeyStats{}, fmt.Errorf("count keys: %w", err)
		}
		stats.Total += total
		stats.Active += active
		stats.Cooling += cooling
		stats.Providers[provider] = ProviderStats{Total: total, Active: active, Cooling: cooling}
	}
	return stats, rows.Err()
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
	if err := row.Scan(&key.ID, &key.Label, &key.Provider, &key.Secret, &active, &key.Priority, &key.Strikes,
		&key.CooldownUntil, &key.LastError, &key.LastUsedAt, &key.CreatedAt); err != nil {
		return Key{}, err
	}
	key.IsActive = active == 1
	key.Provider = NormalizeProvider(key.Provider)
	return key, nil
}
