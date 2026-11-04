package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestCreateKeyRejectsEmptyAndDuplicates(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	if _, err := st.CreateKey(ctx, "blank", "   "); err == nil {
		t.Error("CreateKey() with a blank secret = nil error, want an error")
	}
	if _, err := st.CreateKey(ctx, "primary", "sk_car_abc"); err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	if _, err := st.CreateKey(ctx, "again", "sk_car_abc"); err == nil {
		t.Error("CreateKey() with a duplicate secret = nil error, want an error")
	}
}

func TestListKeysIsNewestFirst(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	first, err := st.CreateKey(ctx, "first", "sk_car_1")
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}
	second, err := st.CreateKey(ctx, "second", "sk_car_2")
	if err != nil {
		t.Fatalf("CreateKey() error = %v", err)
	}

	keys, err := st.ListKeys(ctx)
	if err != nil {
		t.Fatalf("ListKeys() error = %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("ListKeys() returned %d keys, want 2", len(keys))
	}
	if keys[0].ID != second.ID || keys[1].ID != first.ID {
		t.Errorf("ListKeys() order = [%d %d], want [%d %d]", keys[0].ID, keys[1].ID, second.ID, first.ID)
	}
}

// Rotation must spread load: after the first pick touches the oldest key, the
// next pick has to land on the other one.
func TestPickKeyPrefersLeastRecentlyUsed(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	first, _ := st.CreateKey(ctx, "first", "sk_car_1")
	second, _ := st.CreateKey(ctx, "second", "sk_car_2")

	want := []int64{first.ID, second.ID, first.ID}
	for i, expected := range want {
		key, cooling, err := st.PickKey(ctx)
		if err != nil {
			t.Fatalf("PickKey() #%d error = %v", i, err)
		}
		if cooling {
			t.Errorf("PickKey() #%d cooling = true, want false", i)
		}
		if key.ID != expected {
			t.Errorf("PickKey() #%d = %d, want %d", i, key.ID, expected)
		}
	}
}

func TestPickKeyWithoutKeys(t *testing.T) {
	if _, _, err := newStore(t).PickKey(context.Background()); !errors.Is(err, ErrNoKeys) {
		t.Errorf("PickKey() error = %v, want ErrNoKeys", err)
	}
}

func TestPickKeySkipsDisabledKeys(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	disabled, _ := st.CreateKey(ctx, "off", "sk_car_off")
	enabled, _ := st.CreateKey(ctx, "on", "sk_car_on")
	if err := st.SetKeyActive(ctx, disabled.ID, false); err != nil {
		t.Fatalf("SetKeyActive() error = %v", err)
	}

	key, _, err := st.PickKey(ctx)
	if err != nil {
		t.Fatalf("PickKey() error = %v", err)
	}
	if key.ID != enabled.ID {
		t.Errorf("PickKey() = %d, want the enabled key %d", key.ID, enabled.ID)
	}
	if err := st.SetKeyActive(ctx, enabled.ID, false); err != nil {
		t.Fatalf("SetKeyActive() error = %v", err)
	}
	if _, _, err := st.PickKey(ctx); !errors.Is(err, ErrNoKeys) {
		t.Errorf("PickKey() with every key disabled = %v, want ErrNoKeys", err)
	}
}

func TestMarkFailureParksTheKey(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	key, _ := st.CreateKey(ctx, "only", "sk_car_1")

	if err := st.MarkFailure(ctx, key.ID, 429, "slow down", 0); err != nil {
		t.Fatalf("MarkFailure() error = %v", err)
	}
	updated, err := st.GetKey(ctx, key.ID)
	if err != nil {
		t.Fatalf("GetKey() error = %v", err)
	}
	if updated.Strikes != 1 {
		t.Errorf("strikes = %d, want 1", updated.Strikes)
	}
	if !updated.Cooling(time.Now()) {
		t.Error("key is not cooling down after a failure")
	}
	within(t, updated.CooldownUntil-time.Now().Unix(), int64(cooldownBase.Seconds()))

	// A second failure backs off further rather than repeating the same delay.
	if err := st.MarkFailure(ctx, key.ID, 429, "still slow", 0); err != nil {
		t.Fatalf("MarkFailure() error = %v", err)
	}
	updated, _ = st.GetKey(ctx, key.ID)
	within(t, updated.CooldownUntil-time.Now().Unix(), int64(2*cooldownBase.Seconds()))

	// Picking while everything is parked still returns the key: refusing to try
	// anything would turn a short upstream hiccup into an outage.
	if _, cooling, err := st.PickKey(ctx); err != nil || !cooling {
		t.Errorf("PickKey() = (_, %v, %v), want a cooling key and no error", cooling, err)
	}
}

func TestCreditExhaustionParksForTheMaximum(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	key, _ := st.CreateKey(ctx, "broke", "sk_car_1")

	if err := st.MarkFailure(ctx, key.ID, 402, "out of credit", 0); err != nil {
		t.Fatalf("MarkFailure() error = %v", err)
	}
	updated, _ := st.GetKey(ctx, key.ID)
	within(t, updated.CooldownUntil-time.Now().Unix(), int64(creditCooldown.Seconds()))
}

func TestRetryAfterWinsOverTheBackoff(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	key, _ := st.CreateKey(ctx, "throttled", "sk_car_1")

	if err := st.MarkFailure(ctx, key.ID, 429, "later", 20*time.Minute); err != nil {
		t.Fatalf("MarkFailure() error = %v", err)
	}
	updated, _ := st.GetKey(ctx, key.ID)
	within(t, updated.CooldownUntil-time.Now().Unix(), int64((20 * time.Minute).Seconds()))
}

func TestMarkSuccessClearsTheFailureState(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	key, _ := st.CreateKey(ctx, "flaky", "sk_car_1")

	if err := st.MarkFailure(ctx, key.ID, 429, "slow down", 0); err != nil {
		t.Fatalf("MarkFailure() error = %v", err)
	}
	if err := st.MarkSuccess(ctx, key.ID); err != nil {
		t.Fatalf("MarkSuccess() error = %v", err)
	}
	updated, _ := st.GetKey(ctx, key.ID)
	if updated.Strikes != 0 || updated.Cooling(time.Now()) || updated.LastError != "" {
		t.Errorf("key state after success = %+v, want a clean key", updated)
	}
}

func TestResetCooldown(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	key, _ := st.CreateKey(ctx, "parked", "sk_car_1")

	if err := st.MarkFailure(ctx, key.ID, 401, "bad key", 0); err != nil {
		t.Fatalf("MarkFailure() error = %v", err)
	}
	if err := st.ResetCooldown(ctx, key.ID); err != nil {
		t.Fatalf("ResetCooldown() error = %v", err)
	}
	updated, _ := st.GetKey(ctx, key.ID)
	if updated.Cooling(time.Now()) || updated.Strikes != 0 {
		t.Errorf("key after reset = %+v, want a released key", updated)
	}
}

func TestDeleteKeyAndCounts(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)

	first, _ := st.CreateKey(ctx, "first", "sk_car_1")
	_, _ = st.CreateKey(ctx, "second", "sk_car_2")
	if err := st.MarkFailure(ctx, first.ID, 429, "slow", 0); err != nil {
		t.Fatalf("MarkFailure() error = %v", err)
	}

	stats, err := st.CountKeys(ctx)
	if err != nil {
		t.Fatalf("CountKeys() error = %v", err)
	}
	if stats.Total != 2 || stats.Active != 2 || stats.Cooling != 1 {
		t.Errorf("CountKeys() = %+v, want {Total:2 Active:2 Cooling:1}", stats)
	}

	if err := st.DeleteKey(ctx, first.ID); err != nil {
		t.Fatalf("DeleteKey() error = %v", err)
	}
	if stats, _ := st.CountKeys(ctx); stats.Total != 1 {
		t.Errorf("CountKeys().Total after delete = %d, want 1", stats.Total)
	}
}

func within(t *testing.T, got, want int64) {
	t.Helper()
	if delta := got - want; delta > 5 || delta < -5 {
		t.Errorf("cooldown %ds, want about %ds", got, want)
	}
}
