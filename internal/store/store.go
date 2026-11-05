// Package store is the SQLite layer: API keys with their cooldown state, and
// the request history the dashboard reads.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // pure-Go driver, keeps CGO_ENABLED=0 buildable
)

// Store owns the database handle.
type Store struct {
	db *sql.DB
	// defaultRotation is used until the dashboard stores a strategy.
	defaultRotation Strategy
	// writeMu serialises multi-statement transactions such as the session
	// insert, so the rotation cursor and a login cannot interleave.
	writeMu sync.Mutex
}

const schema = `
CREATE TABLE IF NOT EXISTS keys (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	label          TEXT    NOT NULL DEFAULT '',
	provider       TEXT    NOT NULL DEFAULT 'cartesia',
	secret         TEXT    NOT NULL UNIQUE,
	is_active      INTEGER NOT NULL DEFAULT 1,
	priority       INTEGER NOT NULL DEFAULT 50,
	strikes        INTEGER NOT NULL DEFAULT 0,
	cooldown_until INTEGER NOT NULL DEFAULT 0,
	last_error     TEXT    NOT NULL DEFAULT '',
	last_used_at   INTEGER NOT NULL DEFAULT 0,
	last_used_ms   INTEGER NOT NULL DEFAULT 0,
	created_at     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS requests (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	ts         INTEGER NOT NULL,
	provider   TEXT    NOT NULL DEFAULT 'cartesia',
	model      TEXT    NOT NULL,
	voice_id   TEXT    NOT NULL,
	chars      INTEGER NOT NULL,
	bytes      INTEGER NOT NULL,
	key_id     INTEGER NOT NULL DEFAULT 0,
	client_id  INTEGER NOT NULL DEFAULT 0,
	status     INTEGER NOT NULL,
	latency_ms INTEGER NOT NULL,
	error      TEXT    NOT NULL DEFAULT '',
	adjustments TEXT   NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_requests_ts ON requests (ts DESC);

CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	token_hash TEXT    NOT NULL UNIQUE,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL,
	user_agent TEXT    NOT NULL DEFAULT '',
	remote     TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions (expires_at);

CREATE TABLE IF NOT EXISTS client_keys (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	label         TEXT    NOT NULL DEFAULT '',
	prefix        TEXT    NOT NULL,
	token_hash    TEXT    NOT NULL UNIQUE,
	is_active     INTEGER NOT NULL DEFAULT 1,
	created_at    INTEGER NOT NULL,
	last_used_at  INTEGER NOT NULL DEFAULT 0,
	request_count INTEGER NOT NULL DEFAULT 0,
	expires_at    INTEGER NOT NULL DEFAULT 0
);
`

// Open opens (and creates) the database, applies the schema and returns it.
func Open(path string, opts ...Option) (*Store, error) {
	if !isMemory(path) {
		if dir := filepath.Dir(path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create database directory %s: %w", dir, err)
			}
		}
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	if isMemory(path) {
		// WAL is meaningless for a database that lives in RAM and SQLite
		// refuses the pragma there.
		dsn = path
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// One connection: this process is the only writer, and serialising here
	// removes SQLITE_BUSY as a class of failure on a small host.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		// SQLite reports "unable to open database file (14)" for every
		// cause — missing directory, permissions, bad path — and the binary
		// runs as nonroot inside a read-only image, so a hint is worth more
		// than the bare errno.
		return nil, fmt.Errorf("ping sqlite %s: %w (the directory must exist and be writable by the user running this process; in Docker, mount a writable volume at the directory and set it to that user's ownership)", path, err)
	}
	s := &Store{db: db, defaultRotation: DefaultStrategy}
	for _, opt := range opts {
		opt(s)
	}
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// migrate adds columns this version needs to databases created by an earlier
// one. CREATE TABLE IF NOT EXISTS is a no-op on an existing table, so a new
// column would otherwise never appear.
func (s *Store) migrate(ctx context.Context) error {
	columns, err := s.tableColumns(ctx, "keys")
	if err != nil {
		return err
	}
	if !columns["last_used_ms"] {
		if _, err := s.db.ExecContext(ctx,
			`ALTER TABLE keys ADD COLUMN last_used_ms INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add keys.last_used_ms: %w", err)
		}
	}
	if !columns["priority"] {
		if _, err := s.db.ExecContext(ctx,
			`ALTER TABLE keys ADD COLUMN priority INTEGER NOT NULL DEFAULT 50`); err != nil {
			return fmt.Errorf("add keys.priority: %w", err)
		}
	}
	// client_id on requests: 0 means "not recorded" (rows written before client
	// keys existed, or a loopback caller on a keyless install).
	requestColumns, err := s.tableColumns(ctx, "requests")
	if err != nil {
		return err
	}
	if !requestColumns["client_id"] {
		if _, err := s.db.ExecContext(ctx,
			`ALTER TABLE requests ADD COLUMN client_id INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add requests.client_id: %w", err)
		}
	}
	// provider on both tables. Existing keys were Cartesia keys because that
	// was the only provider, so the column default is exactly right for them.
	if !columns["provider"] {
		if _, err := s.db.ExecContext(ctx,
			`ALTER TABLE keys ADD COLUMN provider TEXT NOT NULL DEFAULT 'cartesia'`); err != nil {
			return fmt.Errorf("add keys.provider: %w", err)
		}
	}
	if !requestColumns["provider"] {
		if _, err := s.db.ExecContext(ctx,
			`ALTER TABLE requests ADD COLUMN provider TEXT NOT NULL DEFAULT 'cartesia'`); err != nil {
			return fmt.Errorf("add requests.provider: %w", err)
		}
	}
	if !requestColumns["adjustments"] {
		if _, err := s.db.ExecContext(ctx,
			`ALTER TABLE requests ADD COLUMN adjustments TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add requests.adjustments: %w", err)
		}
	}
	return nil
}

func (s *Store) tableColumns(ctx context.Context, table string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("read columns of %s: %w", table, err)
	}
	defer rows.Close()

	columns := make(map[string]bool, 8)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan column name: %w", err)
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

func isMemory(path string) bool {
	return path == ":memory:" || strings.Contains(path, "mode=memory")
}
