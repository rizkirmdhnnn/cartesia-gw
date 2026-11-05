package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Dashboard accounts. Exactly one operator account exists: this binary is a
// single-tenant tool, and a user table would imply permissions and a user
// management screen that nothing here needs.
const (
	settingAuthUsername  = "auth_username"
	settingAuthHash      = "auth_hash"
	settingAuthSessionID = "auth_session_id"
	settingAuthCreatedAt = "auth_created_at"
	// settingAuthLegacyImport marks the one-time import of CARTESIA_GW_TOKEN
	// as the first client key.
	settingAuthLegacyImport = "auth_legacy_import"

	// settingAuthMadeDefault marks an account that still carries the shipped
	// default password. While it is "1", every session is confined to the
	// change-password screen and the API refuses to serve content.
	settingAuthMadeDefault = "auth_default_password"

	// MinPasswordLen is enforced on every change. Shorter passwords are
	// brute-forceable long before bcrypt's cost becomes the bottleneck.
	MinPasswordLen = 12
	maxPasswordLen = 256
	maxUsernameLen = 64

	// DefaultUsername / DefaultPassword seed a fresh install so the first
	// login is predictable (admin/admin). ValidatePassword deliberately allows
	// this one value: it exists only to force the change that follows.
	DefaultUsername = "admin"
	DefaultPassword = "admin"

	// bcryptCost 12 is ~250ms on a small VPS: slow enough to make guessing
	// expensive, fast enough that a login feels instant.
	bcryptCost = 12
)

// ErrAccountExists is returned when setup runs on an installed instance.
var ErrAccountExists = errors.New("an operator account already exists")

// ErrInvalidCredentials is the only failure a login reports; it deliberately
// does not say whether the username or the password was wrong.
var ErrInvalidCredentials = errors.New("invalid username or password")

// Account is the operator account as the dashboard sees it. It never carries
// the password hash.
type Account struct {
	Username  string `json:"username"`
	CreatedAt int64  `json:"createdAt"`
	// MustChangePassword is true while the account still has the shipped
	// default. The dashboard then shows only the change-password form.
	MustChangePassword bool `json:"mustChangePassword"`
}

// ValidateUsername trims and bounds the account name.
func ValidateUsername(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("username must not be empty")
	}
	if len(name) > maxUsernameLen {
		return "", fmt.Errorf("username must be at most %d characters", maxUsernameLen)
	}
	if strings.ContainsAny(name, " \t\r\n") {
		return "", errors.New("username must not contain spaces")
	}
	return name, nil
}

// ValidatePassword applies the only rule that matters for a single-operator
// console: length.
func ValidatePassword(password string) error {
	if len(password) < MinPasswordLen {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	}
	if len(password) > maxPasswordLen {
		return fmt.Errorf("password must be at most %d characters", maxPasswordLen)
	}
	return nil
}

// ValidateNewPassword is ValidatePassword plus the rule a change adds: the new
// password may not reuse the shipped default or the account name. The default
// is allowed through ValidatePassword only because the seed account needs it;
// here it is exactly what must not survive.
func ValidateNewPassword(newPassword, username string) error {
	if newPassword == DefaultPassword {
		return errors.New("password baru tidak boleh sama dengan password default")
	}
	name := strings.TrimSpace(username)
	if name != "" && strings.EqualFold(newPassword, name) {
		return errors.New("password baru tidak boleh sama dengan username")
	}
	return ValidatePassword(newPassword)
}

// EnsureDefaultAccount seeds admin/admin when no account exists. It runs at
// startup, so the first visit is a login rather than a setup form, and the
// account it creates is flagged to force a password change on first sign-in.
func (s *Store) EnsureDefaultAccount(ctx context.Context) (bool, error) {
	exists, err := s.AccountExists(ctx)
	if err != nil || exists {
		return false, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(DefaultPassword), bcryptCost)
	if err != nil {
		return false, fmt.Errorf("hash default password: %w", err)
	}
	sessionID, err := newRandomToken()
	if err != nil {
		return false, err
	}
	created := time.Now().Unix()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		for key, value := range map[string]string{
			settingAuthUsername:    DefaultUsername,
			settingAuthHash:        string(hash),
			settingAuthSessionID:   sessionID,
			settingAuthCreatedAt:   fmt.Sprint(created),
			settingAuthMadeDefault: "1",
			// A default account also means logins are on: nothing may stay open.
			settingPromptLogin: "1",
		} {
			if err := setSettingTx(ctx, tx, key, value); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// AccountExists reports whether setup has already run.
func (s *Store) AccountExists(ctx context.Context) (bool, error) {
	raw, ok, err := s.GetSetting(ctx, settingAuthHash)
	if err != nil {
		return false, err
	}
	return ok && raw != "", nil
}

// CreateAccount installs the first operator account. It refuses to run a
// second time so a stray setup request cannot take over an existing install.
func (s *Store) CreateAccount(ctx context.Context, username, password string) (Account, error) {
	name, err := ValidateUsername(username)
	if err != nil {
		return Account{}, err
	}
	if err := ValidatePassword(password); err != nil {
		return Account{}, err
	}
	exists, err := s.AccountExists(ctx)
	if err != nil {
		return Account{}, err
	}
	if exists {
		return Account{}, ErrAccountExists
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return Account{}, fmt.Errorf("hash password: %w", err)
	}
	sessionID, err := newRandomToken()
	if err != nil {
		return Account{}, err
	}
	created := time.Now().Unix()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		for key, value := range map[string]string{
			settingAuthUsername:  name,
			settingAuthHash:      string(hash),
			settingAuthSessionID: sessionID,
			settingAuthCreatedAt: fmt.Sprint(created),
		} {
			if err := setSettingTx(ctx, tx, key, value); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	return Account{Username: name, CreatedAt: created}, nil
}

// Authenticate checks a username and password. Both a missing account and a
// wrong password return ErrInvalidCredentials after a bcrypt comparison, so
// the response time does not reveal whether the account exists.
func (s *Store) Authenticate(ctx context.Context, username, password string) (Account, error) {
	account, hash, err := s.accountWithHash(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		// Spend the same time as a real check so the timing stays flat.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return Account{}, ErrInvalidCredentials
	}
	if err != nil {
		return Account{}, err
	}
	if !subtleEqualFold(account.Username, strings.TrimSpace(username)) {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return Account{}, ErrInvalidCredentials
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return Account{}, ErrInvalidCredentials
	}
	return account, nil
}

// Account reads the operator account without its hash.
func (s *Store) Account(ctx context.Context) (Account, error) {
	account, _, err := s.accountWithHash(ctx)
	return account, err
}

// ResetToDefaultPassword restores the shipped admin/admin and re-arms the
// forced change. It exists for the one case that has no other door: the only
// operator forgot the password. Called from the CLI on the host, never over
// HTTP.
func (s *Store) ResetToDefaultPassword(ctx context.Context) (Account, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(DefaultPassword), bcryptCost)
	if err != nil {
		return Account{}, fmt.Errorf("hash default password: %w", err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		for key, value := range map[string]string{
			settingAuthUsername:    DefaultUsername,
			settingAuthHash:        string(hash),
			settingAuthMadeDefault: "1",
			settingPromptLogin:     "1",
		} {
			if err := setSettingTx(ctx, tx, key, value); err != nil {
				return err
			}
		}
		return dropSessionsTx(ctx, tx)
	})
	if err != nil {
		return Account{}, err
	}
	return s.Account(ctx)
}

func (s *Store) accountWithHash(ctx context.Context) (Account, string, error) {
	raw, ok, err := s.GetSetting(ctx, settingAuthHash)
	if err != nil {
		return Account{}, "", err
	}
	if !ok || raw == "" {
		return Account{}, "", sql.ErrNoRows
	}
	name, _, err := s.GetSetting(ctx, settingAuthUsername)
	if err != nil {
		return Account{}, "", err
	}
	created, _, err := s.GetSetting(ctx, settingAuthCreatedAt)
	if err != nil {
		return Account{}, "", err
	}
	var ts int64
	_, _ = fmt.Sscan(created, &ts)
	madeDefault, _, err := s.GetSetting(ctx, settingAuthMadeDefault)
	if err != nil {
		return Account{}, "", err
	}
	return Account{Username: name, CreatedAt: ts, MustChangePassword: madeDefault == "1"}, raw, nil
}

// VerifyCurrentPassword is used before a destructive account change.
func (s *Store) VerifyCurrentPassword(ctx context.Context, username, password string) error {
	_, err := s.Authenticate(ctx, username, password)
	return err
}

// verifyPassword proves the presented password belongs to the stored account
// without re-checking the username. UpdateAccount has already read the account;
// re-authenticating by name would fail the moment the name changes in the same
// call, which is exactly the "rename + rotate password" case.
func (s *Store) verifyPassword(ctx context.Context, password string) error {
	_, hash, err := s.accountWithHash(ctx)
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return ErrInvalidCredentials
	}
	return nil
}

// UpdateAccount changes the username and/or the password. A new random session
// generation is written in the same transaction, so every existing cookie —
// on every device — stops working the moment the change lands.
//
// A password change also clears the default-password flag: the forced-change
// screen ends here.
func (s *Store) UpdateAccount(ctx context.Context, currentPassword, newUsername, newPassword string) (Account, error) {
	account, err := s.Account(ctx)
	if err != nil {
		return Account{}, err
	}
	if err := s.verifyPassword(ctx, currentPassword); err != nil {
		return Account{}, err
	}

	values := map[string]string{}
	name := account.Username
	if strings.TrimSpace(newUsername) != "" && strings.TrimSpace(newUsername) != account.Username {
		name, err = ValidateUsername(newUsername)
		if err != nil {
			return Account{}, err
		}
		values[settingAuthUsername] = name
	}
	if account.MustChangePassword && newPassword == "" {
		// The forced-change screen exists for exactly one action.
		return Account{}, errors.New("password default wajib diganti sebelum memakai gateway")
	}
	if newPassword != "" {
		// The username rule is checked after the rename above so "set the
		// password to the new username" is caught too.
		if err := ValidateNewPassword(newPassword, name); err != nil {
			return Account{}, err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcryptCost)
		if err != nil {
			return Account{}, fmt.Errorf("hash password: %w", err)
		}
		values[settingAuthHash] = string(hash)
		values[settingAuthMadeDefault] = "0"
	}
	if len(values) == 0 {
		return Account{}, errors.New("nothing to change")
	}
	if err := s.invalidateSessionsLocked(ctx, &values); err != nil {
		return Account{}, err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.inTx(ctx, func(tx *sql.Tx) error {
		for key, value := range values {
			if err := setSettingTx(ctx, tx, key, value); err != nil {
				return err
			}
		}
		return dropSessionsTx(ctx, tx)
	}); err != nil {
		return Account{}, err
	}
	return Account{Username: name, CreatedAt: account.CreatedAt, MustChangePassword: values[settingAuthMadeDefault] != "0" && account.MustChangePassword}, nil
}

// InvalidateSessions rotates the session generation, logging every browser out.
func (s *Store) InvalidateSessions(ctx context.Context) error {
	values := map[string]string{}
	if err := s.invalidateSessionsLocked(ctx, &values); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for key, value := range values {
			if err := setSettingTx(ctx, tx, key, value); err != nil {
				return err
			}
		}
		return dropSessionsTx(ctx, tx)
	})
}

func (s *Store) invalidateSessionsLocked(ctx context.Context, into *map[string]string) error {
	sessionID, err := newRandomToken()
	if err != nil {
		return err
	}
	(*into)[settingAuthSessionID] = sessionID
	return nil
}

// dropSessionsTx deletes every session row inside the given transaction.
func dropSessionsTx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return fmt.Errorf("delete sessions: %w", err)
	}
	return nil
}

// SessionGeneration returns the current session generation. A session cookie
// is only valid while its hash equals this value, so rotating it is what makes
// "log out everywhere" and a password change take effect immediately.
func (s *Store) SessionGeneration(ctx context.Context) (string, error) {
	raw, ok, err := s.GetSetting(ctx, settingAuthSessionID)
	if err != nil || !ok || raw == "" {
		return "", err
	}
	return raw, nil
}

// LegacyTokenClaimed reports whether a value was already imported as a client
// key, so the import happens exactly once even across restarts.
func (s *Store) LegacyTokenClaimed(ctx context.Context) (bool, error) {
	raw, ok, err := s.GetSetting(ctx, settingAuthLegacyImport)
	if err != nil {
		return false, err
	}
	return ok && raw == "1", nil
}

// ClaimLegacyToken records that the configured gateway token was imported. It
// is called after the key has been created, never before: a failure between
// the two leaves a re-importable state rather than a silently unimported one.
func (s *Store) ClaimLegacyToken(ctx context.Context) error {
	return s.SetSetting(ctx, settingAuthLegacyImport, "1")
}

// SessionGate is the whole authentication state of a request, read in one
// place so the middleware makes one decision per request.
type SessionGate struct {
	// Configured is true once an operator account exists.
	Configured bool
	// PromptLogin is true when requests must carry a credential even before
	// an account exists (set by the CLI, the first dashboard visit, or config).
	PromptLogin bool
	// KeysExist is true once any client key has been created; the key is then
	// required even from loopback.
	KeysExist bool
}

// Gate reads the authentication state.
func (s *Store) Gate(ctx context.Context) (SessionGate, error) {
	configured, err := s.AccountExists(ctx)
	if err != nil {
		return SessionGate{}, err
	}
	prompt, err := s.PromptLogin(ctx)
	if err != nil {
		return SessionGate{}, err
	}
	stats, err := s.CountClientKeys(ctx)
	if err != nil {
		return SessionGate{}, err
	}
	return SessionGate{Configured: configured, PromptLogin: prompt, KeysExist: stats.Total > 0}, nil
}

func setSettingTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("write setting %s: %w", key, err)
	}
	return nil
}

// inTx runs fn in a transaction. The store keeps a single connection, so the
// BEGIN/COMMIT pair cannot interleave with another caller anyway; it exists so
// multi-setting writes are all-or-nothing.
func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func subtleEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	la, lb := strings.ToLower(a), strings.ToLower(b)
	for i := 0; i < len(la); i++ {
		diff |= la[i] ^ lb[i]
	}
	return diff == 0
}

// dummyHash is compared against when no account or a wrong username arrives,
// so a failed login costs the same bcrypt work as a real one.
var dummyHash = func() []byte {
	hash, err := bcrypt.GenerateFromPassword([]byte("cartesia-gw-dummy-password"), bcryptCost)
	if err != nil {
		panic(err)
	}
	return hash
}()

// newRandomToken returns 32 bytes of entropy as URL-safe base64.
func newRandomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashTokenValue is what the store keeps for a session: the SHA-256 of the
// cookie value. The cookie itself is the only copy of the secret.
func hashTokenValue(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// HashSessionValue is the exported form of hashTokenValue, for the HTTP layer
// that compares a presented cookie against the stored generation.
func HashSessionValue(value string) string { return hashTokenValue(value) }

// ConstantTimeEquals compares two session strings without early exit.
func ConstantTimeEquals(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
