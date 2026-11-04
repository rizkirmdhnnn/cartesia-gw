package store

import (
	"context"
	"testing"
	"time"
)

func TestInsertAndListRequests(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	now := time.Now().Unix()

	for _, row := range []RequestRow{
		{TS: now - 120, Model: "sonic-3", VoiceID: "v-1", Chars: 10, Bytes: 100, Status: 200, LatencyMS: 300},
		{TS: now - 60, Model: "sonic-3", VoiceID: "v-2", Chars: 20, Bytes: 0, Status: 502, LatencyMS: 90, Error: "boom"},
		{TS: now, Model: "sonic-3.5", VoiceID: "v-3", Chars: 30, Bytes: 300, Status: 200, LatencyMS: 400},
	} {
		if err := st.InsertRequest(ctx, row); err != nil {
			t.Fatalf("InsertRequest() error = %v", err)
		}
	}

	rows, total, err := st.ListRequests(ctx, 2, 0)
	if err != nil {
		t.Fatalf("ListRequests() error = %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(rows) != 2 {
		t.Fatalf("page size = %d, want 2", len(rows))
	}
	if rows[0].Model != "sonic-3.5" {
		t.Errorf("first row model = %q, want the newest row", rows[0].Model)
	}
	if rows[0].Bytes != 300 || rows[0].Status != 200 {
		t.Errorf("first row = %+v, want the stored bytes and status", rows[0])
	}

	rest, _, err := st.ListRequests(ctx, 10, 2)
	if err != nil {
		t.Fatalf("ListRequests() with an offset error = %v", err)
	}
	if len(rest) != 1 || rest[0].VoiceID != "v-1" {
		t.Errorf("offset page = %+v, want the single oldest row", rest)
	}
}

func TestPruneRequestsRemovesOnlyExpiredRows(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	now := time.Now().Unix()

	for _, ts := range []int64{now - 40*24*3600, now - 31*24*3600, now - 3600} {
		if err := st.InsertRequest(ctx, RequestRow{TS: ts, Model: "sonic-3", VoiceID: "v", Status: 200}); err != nil {
			t.Fatalf("InsertRequest() error = %v", err)
		}
	}

	removed, err := st.PruneRequests(ctx, now-30*24*3600)
	if err != nil {
		t.Fatalf("PruneRequests() error = %v", err)
	}
	if removed != 2 {
		t.Errorf("PruneRequests() removed %d rows, want 2", removed)
	}
	if _, total, _ := st.ListRequests(ctx, 10, 0); total != 1 {
		t.Errorf("rows left = %d, want 1", total)
	}
}

func TestUsageAggregatesTheWindow(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	now := time.Now()

	rows := []RequestRow{
		{TS: now.Unix(), Chars: 100, Bytes: 1000, Status: 200, LatencyMS: 200},
		// A non-integral mean on purpose: an int64 scan of AVG passes only while
		// the average happens to land on a whole number.
		{TS: now.Add(-time.Hour).Unix(), Chars: 50, Bytes: 500, Status: 200, LatencyMS: 401},
		{TS: now.Add(-2 * time.Hour).Unix(), Chars: 25, Status: 502},
		// Outside the 24h window and outside today.
		{TS: now.Add(-48 * time.Hour).Unix(), Chars: 999, Status: 200},
	}
	for _, row := range rows {
		if err := st.InsertRequest(ctx, row); err != nil {
			t.Fatalf("InsertRequest() error = %v", err)
		}
	}

	stats, err := st.Usage(ctx)
	if err != nil {
		t.Fatalf("Usage() error = %v", err)
	}
	if stats.Requests24h != 3 {
		t.Errorf("Requests24h = %d, want 3", stats.Requests24h)
	}
	if stats.Success24h != 2 {
		t.Errorf("Success24h = %d, want 2", stats.Success24h)
	}
	if stats.Bytes24h != 1500 {
		t.Errorf("Bytes24h = %d, want 1500", stats.Bytes24h)
	}
	if stats.AvgLatencyMS24h != 301 {
		t.Errorf("AvgLatencyMS24h = %d, want 301 (the rounded mean of 200 and 401, successful rows only)", stats.AvgLatencyMS24h)
	}
	if stats.CharsToday < 100 {
		t.Errorf("CharsToday = %d, want at least 100", stats.CharsToday)
	}
}
