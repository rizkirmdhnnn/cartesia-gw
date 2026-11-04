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

	_ "modernc.org/sqlite" // pure-Go driver, keeps CGO_ENABLED=0 buildable
)

// Store owns the database handle.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS keys (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	label          TEXT    NOT NULL DEFAULT '',
	secret         TEXT    NOT NULL UNIQUE,
	is_active      INTEGER NOT NULL DEFAULT 1,
	strikes        INTEGER NOT NULL DEFAULT 0,
	cooldown_until INTEGER NOT NULL DEFAULT 0,
	last_error     TEXT    NOT NULL DEFAULT '',
	last_used_at   INTEGER NOT NULL DEFAULT 0,
	created_at     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS requests (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	ts         INTEGER NOT NULL,
	model      TEXT    NOT NULL,
	voice_id   TEXT    NOT NULL,
	chars      INTEGER NOT NULL,
	bytes      INTEGER NOT NULL,
	key_id     INTEGER NOT NULL DEFAULT 0,
	status     INTEGER NOT NULL,
	latency_ms INTEGER NOT NULL,
	error      TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_requests_ts ON requests (ts DESC);
`

// Open opens (and creates) the database, applies the schema and returns it.
func Open(path string) (*Store, error) {
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
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	s := &Store{db: db}
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

func isMemory(path string) bool {
	return path == ":memory:" || strings.Contains(path, "mode=memory")
}
