package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// A database written by an earlier build has no last_used_ms column, and
// CREATE TABLE IF NOT EXISTS will not add one. Without the migration every
// pick on an upgraded install would fail on an unknown column.
func TestMigrateAddsTheMillisecondOrderColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw database error = %v", err)
	}
	legacySchema := `
		CREATE TABLE keys (
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
		INSERT INTO keys (label, secret, is_active, created_at) VALUES ('legacy', 'sk_car_legacy', 1, 0);`
	if _, err := raw.Exec(legacySchema); err != nil {
		t.Fatalf("create legacy schema error = %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw database error = %v", err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open() on a legacy database error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	columns, err := st.tableColumns(context.Background(), "keys")
	if err != nil {
		t.Fatalf("tableColumns() error = %v", err)
	}
	if !columns["last_used_ms"] {
		t.Fatal("last_used_ms was not added to the legacy table")
	}

	key, _, err := st.PickKey(context.Background())
	if err != nil {
		t.Fatalf("PickKey() on a migrated database error = %v", err)
	}
	if key.Label != "legacy" {
		t.Errorf("PickKey() = %q, want the row that was already there", key.Label)
	}
}

// Opening an already-migrated database twice must not try to add the column
// again.
func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "twice.sqlite")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	if _, err := second.CreateKey(context.Background(), "after", "sk_car_after"); err != nil {
		t.Fatalf("CreateKey() after reopening error = %v", err)
	}
}
