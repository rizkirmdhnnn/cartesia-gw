package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"
)

// RequestRow is one logged synthesis attempt. The transcript and the audio are
// deliberately not stored: the first is the caller's content and the second is
// large, and neither answers a question this dashboard asks.
type RequestRow struct {
	ID        int64  `json:"id"`
	TS        int64  `json:"ts"`
	Model     string `json:"model"`
	VoiceID   string `json:"voiceId"`
	Chars     int64  `json:"chars"`
	Bytes     int64  `json:"bytes"`
	KeyID     int64  `json:"keyId"`
	Status    int    `json:"status"`
	LatencyMS int64  `json:"latencyMs"`
	Error     string `json:"error"`
}

// UsageStats aggregates the last 24 hours plus today's totals.
type UsageStats struct {
	RequestsToday   int64 `json:"requestsToday"`
	CharsToday      int64 `json:"charsToday"`
	Requests24h     int64 `json:"requests24h"`
	Success24h      int64 `json:"success24h"`
	Bytes24h        int64 `json:"bytes24h"`
	AvgLatencyMS24h int64 `json:"avgLatencyMs24h"`
}

const requestColumns = `id, ts, model, voice_id, chars, bytes, key_id, status, latency_ms, error`

// InsertRequest logs one attempt.
func (s *Store) InsertRequest(ctx context.Context, row RequestRow) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO requests (ts, model, voice_id, chars, bytes, key_id, status, latency_ms, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.TS, row.Model, row.VoiceID, row.Chars, row.Bytes, row.KeyID, row.Status, row.LatencyMS, row.Error)
	if err != nil {
		return fmt.Errorf("insert request log: %w", err)
	}
	return nil
}

// ListRequests returns a page of history, newest first, plus the total count.
func (s *Store) ListRequests(ctx context.Context, limit, offset int) ([]RequestRow, int64, error) {
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count requests: %w", err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+requestColumns+` FROM requests ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list requests: %w", err)
	}
	defer rows.Close()

	out := make([]RequestRow, 0, limit)
	for rows.Next() {
		var row RequestRow
		if err := rows.Scan(&row.ID, &row.TS, &row.Model, &row.VoiceID, &row.Chars, &row.Bytes,
			&row.KeyID, &row.Status, &row.LatencyMS, &row.Error); err != nil {
			return nil, 0, fmt.Errorf("scan request row: %w", err)
		}
		out = append(out, row)
	}
	return out, total, rows.Err()
}

// PruneRequests deletes history older than the cutoff and reports how many
// rows went. Without this the table grows with every request and nothing ever
// removes a row.
func (s *Store) PruneRequests(ctx context.Context, before int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM requests WHERE ts < ?`, before)
	if err != nil {
		return 0, fmt.Errorf("prune requests: %w", err)
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count pruned requests: %w", err)
	}
	return removed, nil
}

// Usage aggregates request statistics.
func (s *Store) Usage(ctx context.Context) (UsageStats, error) {
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	dayAgo := now.Add(-24 * time.Hour).Unix()

	var stats UsageStats
	// AVG returns a real; scanning it straight into an int64 fails the moment
	// the mean is not a whole number, which is most of the time.
	var avgLatency sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN ts >= ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN ts >= ? THEN chars ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN ts >= ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN ts >= ? AND status >= 200 AND status < 300 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN ts >= ? THEN bytes ELSE 0 END), 0),
			AVG(CASE WHEN ts >= ? AND status >= 200 AND status < 300 THEN latency_ms END)
		FROM requests`,
		dayStart, dayStart, dayAgo, dayAgo, dayAgo, dayAgo,
	).Scan(&stats.RequestsToday, &stats.CharsToday, &stats.Requests24h,
		&stats.Success24h, &stats.Bytes24h, &avgLatency)
	if err != nil {
		return UsageStats{}, fmt.Errorf("aggregate usage: %w", err)
	}
	stats.AvgLatencyMS24h = int64(math.Round(avgLatency.Float64))
	return stats, nil
}
