package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Setting keys. They are stored in the settings table because the dashboard
// can change them while the process is running.
const (
	settingRotation       = "rotation"
	settingRotationCursor = "rotation_cursor"
	// settingPromptLogin makes presentable requests ask for a credential even
	// when none exists yet. It is off for loopback-only installs, which is why
	// the previous release could run without any credential at all.
	settingPromptLogin = "auth_prompt_login"
)

// Strategy is how the next API key is chosen.
type Strategy string

const (
	// StrategyLeastRecentlyUsed picks the key that has waited longest. Even
	// spread without a cursor, and it self-corrects when keys come and go.
	StrategyLeastRecentlyUsed Strategy = "least-recently-used"
	// StrategyRoundRobin walks the keys in id order and wraps, which makes the
	// next key predictable.
	StrategyRoundRobin Strategy = "round-robin"
	// StrategyRandom spreads without keeping any order.
	StrategyRandom Strategy = "random"
	// StrategySticky keeps using one key until it is parked, so the other keys
	// keep their quota untouched.
	StrategySticky Strategy = "sticky"
	// StrategyPriority tries keys in the operator's manual order: the lowest
	// priority number first, least recently used among equals. It is how a
	// "primary + overflow" pair is expressed without a second concept.
	StrategyPriority Strategy = "priority"
)

// Strategies is the closed set the dashboard offers and the API accepts.
var Strategies = []Strategy{
	StrategyLeastRecentlyUsed,
	StrategyRoundRobin,
	StrategyRandom,
	StrategySticky,
	StrategyPriority,
}

// DefaultStrategy is what a fresh install uses. It is the behaviour this
// gateway shipped with, so an existing database does not change how it rotates
// keys because a setting appeared.
const DefaultStrategy = StrategyLeastRecentlyUsed

func (s Strategy) String() string { return string(s) }

// StrategyNames returns the accepted values, for error messages and the API.
func StrategyNames() []string {
	names := make([]string, 0, len(Strategies))
	for _, strategy := range Strategies {
		names = append(names, strategy.String())
	}
	return names
}

// ParseStrategy validates a strategy value.
func ParseStrategy(value string) (Strategy, error) {
	candidate := Strategy(strings.ToLower(strings.TrimSpace(value)))
	for _, known := range Strategies {
		if candidate == known {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("rotation %q is not one of: %s", value, strings.Join(StrategyNames(), ", "))
}

// Option adjusts a Store at Open time.
type Option func(*Store)

// WithDefaultRotation sets the strategy used before one is stored. The flag is
// not consulted again once the dashboard has written a value.
func WithDefaultRotation(strategy Strategy) Option {
	return func(s *Store) { s.defaultRotation = strategy }
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}

// GetSetting reads one setting. The second return value distinguishes "absent"
// from "empty", which matters for a cursor that may legitimately be 0.
func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read setting %s: %w", key, err)
	}
	return value, true, nil
}

// SetSetting writes one setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("write setting %s: %w", key, err)
	}
	return nil
}

// RotationStrategy returns the active strategy. A stored value that no longer
// parses (a downgrade, a hand-edited row) falls back to the default instead of
// failing every request that needs a key.
func (s *Store) RotationStrategy(ctx context.Context) (Strategy, error) {
	raw, ok, err := s.GetSetting(ctx, settingRotation)
	if err != nil {
		return s.defaultRotation, err
	}
	if !ok {
		return s.defaultRotation, nil
	}
	parsed, err := ParseStrategy(raw)
	if err != nil {
		return s.defaultRotation, nil
	}
	return parsed, nil
}

// SetRotationStrategy stores a validated strategy.
func (s *Store) SetRotationStrategy(ctx context.Context, strategy Strategy) error {
	if _, err := ParseStrategy(strategy.String()); err != nil {
		return err
	}
	return s.SetSetting(ctx, settingRotation, strategy.String())
}

// PromptLogin reports whether presentable requests must carry a credential
// even when none exists yet. When false (the default on a loopback install
// with no web-login reachable), an unauthenticated caller is let through only
// on loopback; anything remote is refused.
func (s *Store) PromptLogin(ctx context.Context) (bool, error) {
	raw, ok, err := s.GetSetting(ctx, settingPromptLogin)
	if err != nil || !ok {
		return false, err
	}
	return raw == "1", nil
}

// SetPromptLogin stores the flag.
func (s *Store) SetPromptLogin(ctx context.Context, on bool) error {
	value := "0"
	if on {
		value = "1"
	}
	return s.SetSetting(ctx, settingPromptLogin, value)
}
