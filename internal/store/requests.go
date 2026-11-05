package store

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"
)

// RequestRow is one logged synthesis attempt. The transcript and the audio are
// deliberately not stored: the first is the caller's content and the second is
// large, and neither answers a question this dashboard asks.
type RequestRow struct {
	ID        int64  `json:"id"`
	TS        int64  `json:"ts"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	VoiceID   string `json:"voiceId"`
	Chars     int64  `json:"chars"`
	Bytes     int64  `json:"bytes"`
	KeyID     int64  `json:"keyId"`
	ClientID  int64  `json:"clientId"`
	Status    int    `json:"status"`
	LatencyMS int64  `json:"latencyMs"`
	Error     string `json:"error"`
	// Adjustments lists what the gateway changed to serve the request (a
	// clamped speed, a dropped control, a substituted voice). Empty means the
	// request was served exactly as written.
	Adjustments []string `json:"adjustments,omitempty"`
}

// UsageStats aggregates the last 24 hours plus today's totals.
type UsageStats struct {
	RequestsToday   int64 `json:"requestsToday"`
	CharsToday      int64 `json:"charsToday"`
	Requests24h     int64 `json:"requests24h"`
	Success24h      int64 `json:"success24h"`
	Bytes24h        int64 `json:"bytes24h"`
	AvgLatencyMS24h int64 `json:"avgLatencyMs24h"`
	// ClientRequests24h counts requests that arrived with a client key; the
	// remainder are dashboard or open loopback calls.
	ClientRequests24h int64 `json:"clientRequests24h"`
	// Last-hour split: synthesis vs. listings. A listing has no characters.
	SynthRequests1h int64 `json:"synthRequests1h"`
	ListRequests1h  int64 `json:"listRequests1h"`
	// Providers splits the last 24 hours by upstream provider, so the console
	// can show who answered rather than only how much was asked.
	Providers map[string]ProviderUsage `json:"providers"`
}

// ProviderUsage is one provider's share of the last 24 hours.
type ProviderUsage struct {
	Requests int64 `json:"requests"`
	Success  int64 `json:"success"`
	Chars    int64 `json:"chars"`
}

const requestColumns = `id, ts, provider, model, voice_id, chars, bytes, key_id, client_id, status, latency_ms, error, adjustments`

// InsertRequest logs one attempt.
func (s *Store) InsertRequest(ctx context.Context, row RequestRow) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO requests (ts, provider, model, voice_id, chars, bytes, key_id, client_id, status, latency_ms, error, adjustments)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.TS, NormalizeProvider(row.Provider), row.Model, row.VoiceID, row.Chars, row.Bytes, row.KeyID, row.ClientID,
		row.Status, row.LatencyMS, row.Error, joinAdjustments(row.Adjustments))
	if err != nil {
		return fmt.Errorf("insert request log: %w", err)
	}
	return nil
}

// KeyTraffic is one Cartesia key's share of recent traffic.
type KeyTraffic struct {
	KeyID      int64 `json:"keyId"`
	Requests   int64 `json:"requests"`
	Success    int64 `json:"success"`
	Chars      int64 `json:"chars"`
	AvgLatency int64 `json:"avgLatencyMs"`
}

// ClientTraffic is one client key's share of recent traffic.
type ClientTraffic struct {
	ClientID   int64 `json:"clientId"`
	Requests   int64 `json:"requests"`
	Chars      int64 `json:"chars"`
	LastSeenTS int64 `json:"lastSeenTs"`
}

// TrafficSince aggregates requests by key and by client since a timestamp.
// Both maps are read in one pass of the same window so the visualiser's two
// sides always describe the same traffic.
func (s *Store) TrafficSince(ctx context.Context, since int64) (map[int64]KeyTraffic, map[int64]ClientTraffic, error) {
	byKey := make(map[int64]KeyTraffic, 8)
	rows, err := s.db.QueryContext(ctx, `
		SELECT key_id,
			COUNT(*),
			COALESCE(SUM(CASE WHEN status >= 200 AND status < 300 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(chars), 0),
			COALESCE(CAST(AVG(CASE WHEN status >= 200 AND status < 300 THEN latency_ms END) AS INTEGER), 0)
		FROM requests WHERE ts >= ? AND key_id > 0
		GROUP BY key_id`, since)
	if err != nil {
		return nil, nil, fmt.Errorf("traffic by key: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row KeyTraffic
		if err := rows.Scan(&row.KeyID, &row.Requests, &row.Success, &row.Chars, &row.AvgLatency); err != nil {
			return nil, nil, fmt.Errorf("scan traffic by key: %w", err)
		}
		byKey[row.KeyID] = row
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	byClient := make(map[int64]ClientTraffic, 8)
	clientRows, err := s.db.QueryContext(ctx, `
		SELECT client_id, COUNT(*), COALESCE(SUM(chars), 0), COALESCE(MAX(ts), 0)
		FROM requests WHERE ts >= ? AND client_id > 0
		GROUP BY client_id`, since)
	if err != nil {
		return nil, nil, fmt.Errorf("traffic by client: %w", err)
	}
	defer clientRows.Close()
	for clientRows.Next() {
		var row ClientTraffic
		if err := clientRows.Scan(&row.ClientID, &row.Requests, &row.Chars, &row.LastSeenTS); err != nil {
			return nil, nil, fmt.Errorf("scan traffic by client: %w", err)
		}
		byClient[row.ClientID] = row
	}
	return byKey, byClient, clientRows.Err()
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
		var (
			row         RequestRow
			adjustments string
		)
		if err := rows.Scan(&row.ID, &row.TS, &row.Provider, &row.Model, &row.VoiceID, &row.Chars, &row.Bytes,
			&row.KeyID, &row.ClientID, &row.Status, &row.LatencyMS, &row.Error, &adjustments); err != nil {
			return nil, 0, fmt.Errorf("scan request row: %w", err)
		}
		row.Provider = NormalizeProvider(row.Provider)
		row.Adjustments = splitAdjustments(adjustments)
		out = append(out, row)
	}
	return out, total, rows.Err()
}

// joinAdjustments stores the adjustment list in one column. The separator is
// a unit separator, which cannot appear in an identifier or a note.
func joinAdjustments(adjustments []string) string {
	if len(adjustments) == 0 {
		return ""
	}
	return strings.Join(adjustments, "\x1f")
}

func splitAdjustments(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, "\x1f")
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
	hourAgo := now.Add(-time.Hour).Unix()
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN ts >= ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN ts >= ? THEN chars ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN ts >= ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN ts >= ? AND status >= 200 AND status < 300 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN ts >= ? THEN bytes ELSE 0 END), 0),
			AVG(CASE WHEN ts >= ? AND status >= 200 AND status < 300 THEN latency_ms END),
			COALESCE(SUM(CASE WHEN ts >= ? AND client_id > 0 THEN 1 ELSE 0 END), 0),
			-- requests that spent characters (synthesis) vs. those that did not
			-- (voice listings), last hour: the KPI strip splits the two.
			COALESCE(SUM(CASE WHEN ts >= ? AND chars > 0 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN ts >= ? AND chars = 0 AND status >= 200 AND status < 300 THEN 1 ELSE 0 END), 0)
		FROM requests`,
		dayStart, dayStart, dayAgo, dayAgo, dayAgo, dayAgo,
		dayAgo, hourAgo, hourAgo,
	).Scan(&stats.RequestsToday, &stats.CharsToday, &stats.Requests24h,
		&stats.Success24h, &stats.Bytes24h, &avgLatency,
		&stats.ClientRequests24h, &stats.SynthRequests1h, &stats.ListRequests1h)
	if err != nil {
		return UsageStats{}, fmt.Errorf("aggregate usage: %w", err)
	}
	stats.AvgLatencyMS24h = int64(math.Round(avgLatency.Float64))
	providers, err := s.providerUsage(ctx, dayAgo)
	if err != nil {
		return UsageStats{}, err
	}
	stats.Providers = providers
	return stats, nil
}

// providerUsage aggregates the same 24-hour window per provider.
func (s *Store) providerUsage(ctx context.Context, since int64) (map[string]ProviderUsage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT provider,
			COUNT(*),
			COALESCE(SUM(CASE WHEN status >= 200 AND status < 300 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(chars), 0)
		FROM requests WHERE ts >= ?
		GROUP BY provider`, since)
	if err != nil {
		return nil, fmt.Errorf("aggregate usage by provider: %w", err)
	}
	defer rows.Close()

	out := make(map[string]ProviderUsage, 4)
	for rows.Next() {
		var provider string
		var usage ProviderUsage
		if err := rows.Scan(&provider, &usage.Requests, &usage.Success, &usage.Chars); err != nil {
			return nil, fmt.Errorf("scan usage by provider: %w", err)
		}
		out[NormalizeProvider(provider)] = usage
	}
	return out, rows.Err()
}
