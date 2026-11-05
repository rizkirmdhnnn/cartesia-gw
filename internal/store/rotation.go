package store

import (
	"context"
	"database/sql"
	"strconv"
)

// rotationCursorKey is the setting that stores the round-robin cursor. Each
// provider walks its own keys, so each needs its own cursor.
func rotationCursorKey(provider string) string {
	return settingRotationCursor + ":" + NormalizeProvider(provider)
}

func (s *Store) rotationCursor(ctx context.Context, provider string) (int64, error) {
	raw, ok, err := s.GetSetting(ctx, rotationCursorKey(provider))
	if err != nil || !ok {
		// An upgraded database has the cursor under the old, unprefixed key and
		// no way to tell which provider last used it. Starting from zero only
		// means the first round begins at the lowest id again.
		return 0, err
	}
	cursor, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, nil
	}
	return cursor, nil
}

// DefaultVoiceSetting is the settings key holding a provider's fallback voice,
// used when a request names none or names one the provider does not know.
func DefaultVoiceSetting(provider string) string {
	return "default_voice_" + NormalizeProvider(provider)
}

// DefaultVoice reads the configured fallback voice for a provider.
func (s *Store) DefaultVoice(ctx context.Context, provider string) (string, error) {
	value, _, err := s.GetSetting(ctx, DefaultVoiceSetting(provider))
	if err != nil {
		return "", err
	}
	return value, nil
}

// SetDefaultVoice stores the fallback voice for a provider. An empty value
// removes it, which makes the provider's own default apply.
func (s *Store) SetDefaultVoice(ctx context.Context, provider, voice string) error {
	key := DefaultVoiceSetting(provider)
	if voice == "" {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
			return err
		}
		return nil
	}
	return s.SetSetting(ctx, key, voice)
}

// SQLNullString is a small helper so callers reading a nullable column do not
// each import database/sql.
func SQLNullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}
