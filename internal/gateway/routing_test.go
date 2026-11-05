package gateway

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/rizkirmdhnnn/cartesia-gw/internal/store"
)

// The traffic map is one snapshot: clients, keys, shares that add up, and the
// key the strategy would pick next. A second endpoint for any of these would
// let the two columns disagree.
func TestRoutingSnapshotDescribesBothSides(t *testing.T) {
	_, st, server := newServer(t, emptyUpstream(), "")
	client := signIn(t, st)

	ctx := context.Background()
	keyA := addKey(t, st, "utama", "sk_car_a")
	keyB := addKey(t, st, "cadangan", "sk_car_b")
	clientKey, _, err := st.CreateClientKey(ctx, "n8n", 0)
	if err != nil {
		t.Fatalf("CreateClientKey() error = %v", err)
	}

	// Three requests: two on keyA, one on keyB, all from clientKey.
	now := time.Now().Unix()
	for i, keyID := range []int64{keyA.ID, keyA.ID, keyB.ID} {
		if err := st.InsertRequest(ctx, store.RequestRow{TS: now - int64(i), Model: "sonic-3", VoiceID: "v1", Chars: 10, KeyID: keyID, ClientID: clientKey.ID, Status: 200, LatencyMS: 120}); err != nil {
			t.Fatalf("InsertRequest() error = %v", err)
		}
	}

	body, err := apiGetAs(t, client.Client, server, "/api/routing")
	if err != nil {
		t.Fatalf("GET /api/routing error = %v", err)
	}

	keys, _ := body["keys"].([]any)
	if len(keys) != 2 {
		t.Fatalf("keys = %d, want 2", len(keys))
	}
	var total float64
	nextUp := 0
	for _, raw := range keys {
		key := raw.(map[string]any)
		total += key["share"].(float64)
		if key["nextUp"] == true {
			nextUp++
		}
	}
	if total < 0.99 || total > 1.01 {
		t.Errorf("key shares add up to %v, want 1", total)
	}
	if nextUp != 1 {
		t.Errorf("%d keys marked nextUp, want exactly 1", nextUp)
	}

	clients, _ := body["clients"].([]any)
	if len(clients) != 1 {
		t.Fatalf("clients = %d, want 1", len(clients))
	}
	if share := clients[0].(map[string]any)["share"].(float64); share < 0.99 {
		t.Errorf("the only client's share = %v, want 1", share)
	}

	if body["strategy"] != "least-recently-used" {
		t.Errorf("strategy = %v, want least-recently-used", body["strategy"])
	}
}

// Priority is the manual order: the lowest number is tried first, and the
// visualiser reports the same winner the picker uses.
func TestPriorityStrategyFollowsTheOperatorOrder(t *testing.T) {
	_, st, server := newServer(t, emptyUpstream(), "")
	client := signIn(t, st)

	first := addKey(t, st, "backup", "sk_car_backup")
	second := addKey(t, st, "primary", "sk_car_primary")
	if _, err := apiJSONAs(t, client.Client, server, "PATCH",
		"/api/keys/"+strconv.FormatInt(second.ID, 10), `{"priority":5}`); err != nil {
		t.Fatalf("PATCH priority error = %v", err)
	}
	if _, err := apiJSONAs(t, client.Client, server, "PATCH",
		"/api/keys/"+strconv.FormatInt(first.ID, 10), `{"priority":90}`); err != nil {
		t.Fatalf("PATCH priority error = %v", err)
	}
	if _, err := apiJSONAs(t, client.Client, server, "PATCH", "/api/settings", `{"rotation":"priority"}`); err != nil {
		t.Fatalf("PATCH rotation error = %v", err)
	}

	for i := 0; i < 3; i++ {
		key, _, err := st.PickKey(context.Background())
		if err != nil {
			t.Fatalf("PickKey() error = %v", err)
		}
		if key.ID != second.ID {
			t.Fatalf("PickKey() #%d = %d, want the priority-5 key %d", i, key.ID, second.ID)
		}
	}

	// Park the primary: traffic falls through to the backup.
	if err := st.MarkFailure(context.Background(), second.ID, 429, "slow", time.Minute); err != nil {
		t.Fatalf("MarkFailure() error = %v", err)
	}
	key, _, err := st.PickKey(context.Background())
	if err != nil {
		t.Fatalf("PickKey() error = %v", err)
	}
	if key.ID != first.ID {
		t.Errorf("PickKey() after parking the primary = %d, want the backup %d", key.ID, first.ID)
	}
}
