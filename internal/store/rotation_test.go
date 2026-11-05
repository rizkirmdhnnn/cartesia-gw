package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func strategyStore(t *testing.T, strategy Strategy) *Store {
	t.Helper()
	st, err := Open(":memory:", WithDefaultRotation(strategy))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestParseStrategy(t *testing.T) {
	tests := []struct {
		in      string
		want    Strategy
		wantErr bool
	}{
		{in: "round-robin", want: StrategyRoundRobin},
		{in: "  Round-Robin  ", want: StrategyRoundRobin},
		{in: "least-recently-used", want: StrategyLeastRecentlyUsed},
		{in: "random", want: StrategyRandom},
		{in: "sticky", want: StrategySticky},
		{in: "", wantErr: true},
		{in: "roundrobin", wantErr: true},
		{in: "first", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseStrategy(tt.in)
		if (err != nil) != tt.wantErr {
			t.Fatalf("ParseStrategy(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ParseStrategy(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The default has to stay what the gateway did before the setting existed, or
// an existing install silently changes how it spends its keys.
func TestDefaultStrategyIsLeastRecentlyUsed(t *testing.T) {
	st := strategyStore(t, DefaultStrategy)
	got, err := st.RotationStrategy(context.Background())
	if err != nil {
		t.Fatalf("RotationStrategy() error = %v", err)
	}
	if got != StrategyLeastRecentlyUsed {
		t.Errorf("RotationStrategy() = %q, want %q", got, StrategyLeastRecentlyUsed)
	}
}

func TestRotationStrategyRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := strategyStore(t, DefaultStrategy)

	for _, strategy := range Strategies {
		if err := st.SetRotationStrategy(ctx, strategy); err != nil {
			t.Fatalf("SetRotationStrategy(%q) error = %v", strategy, err)
		}
		got, err := st.RotationStrategy(ctx)
		if err != nil {
			t.Fatalf("RotationStrategy() error = %v", err)
		}
		if got != strategy {
			t.Errorf("RotationStrategy() = %q, want %q", got, strategy)
		}
	}
}

func TestSetRotationStrategyRejectsUnknownValues(t *testing.T) {
	st := strategyStore(t, DefaultStrategy)
	if err := st.SetRotationStrategy(context.Background(), Strategy("first")); err == nil {
		t.Error("SetRotationStrategy() accepted an unknown strategy")
	}
}

// A row that no longer parses must not take the whole gateway down: key
// selection falls back to the default instead of erroring on every request.
func TestStoredGarbageFallsBackToTheDefault(t *testing.T) {
	ctx := context.Background()
	st := strategyStore(t, StrategySticky)

	if err := st.SetSetting(ctx, settingRotation, "nonsense"); err != nil {
		t.Fatalf("SetSetting() error = %v", err)
	}
	got, err := st.RotationStrategy(ctx)
	if err != nil {
		t.Fatalf("RotationStrategy() error = %v", err)
	}
	if got != StrategySticky {
		t.Errorf("RotationStrategy() = %q, want the configured default %q", got, StrategySticky)
	}
}

// The flag only decides the strategy while nothing is stored; once the
// dashboard writes one, the flag must not override it.
func TestStoredStrategyWinsOverTheDefault(t *testing.T) {
	ctx := context.Background()
	st := strategyStore(t, StrategySticky)

	if err := st.SetRotationStrategy(ctx, StrategyRoundRobin); err != nil {
		t.Fatalf("SetRotationStrategy() error = %v", err)
	}
	got, err := st.RotationStrategy(ctx)
	if err != nil {
		t.Fatalf("RotationStrategy() error = %v", err)
	}
	if got != StrategyRoundRobin {
		t.Errorf("RotationStrategy() = %q, want the stored round-robin", got)
	}
}

// Under burst, several picks land inside the same second. With a
// second-granularity order column they tied, the tie-break fell through to id,
// and the lowest id served every request for the rest of that second. The
// order column is in milliseconds so this actually rotates.
func TestLeastRecentlyUsedRotatesWithinTheSameSecond(t *testing.T) {
	ctx := context.Background()
	st := strategyStore(t, StrategyLeastRecentlyUsed)

	first, _ := st.CreateKey(ctx, "first", "sk_car_1")
	second, _ := st.CreateKey(ctx, "second", "sk_car_2")
	third, _ := st.CreateKey(ctx, "third", "sk_car_3")

	want := []int64{
		first.ID, second.ID, third.ID,
		first.ID, second.ID, third.ID,
	}
	for i, expected := range want {
		time.Sleep(2 * time.Millisecond)
		key, _, err := st.PickKey(ctx)
		if err != nil {
			t.Fatalf("PickKey() #%d error = %v", i, err)
		}
		if key.ID != expected {
			t.Fatalf("PickKey() #%d = %d, want %d (the whole run happens in well under a second)", i, key.ID, expected)
		}
	}
}

func TestRoundRobinWalksIdsAndWraps(t *testing.T) {
	ctx := context.Background()
	st := strategyStore(t, StrategyRoundRobin)

	first, _ := st.CreateKey(ctx, "first", "sk_car_1")
	second, _ := st.CreateKey(ctx, "second", "sk_car_2")
	third, _ := st.CreateKey(ctx, "third", "sk_car_3")

	want := []int64{
		first.ID, second.ID, third.ID,
		first.ID, second.ID, third.ID,
	}
	for i, expected := range want {
		key, cooling, err := st.PickKey(ctx)
		if err != nil {
			t.Fatalf("PickKey() #%d error = %v", i, err)
		}
		if cooling {
			t.Errorf("PickKey() #%d cooling = true on a healthy key", i)
		}
		if key.ID != expected {
			t.Fatalf("PickKey() #%d = %d, want %d", i, key.ID, expected)
		}
	}
}

// A parked key is skipped, and the cycle continues from where it left off
// rather than restarting.
func TestRoundRobinSkipsParkedKeys(t *testing.T) {
	ctx := context.Background()
	st := strategyStore(t, StrategyRoundRobin)

	first, _ := st.CreateKey(ctx, "first", "sk_car_1")
	second, _ := st.CreateKey(ctx, "second", "sk_car_2")
	third, _ := st.CreateKey(ctx, "third", "sk_car_3")

	if err := st.MarkFailure(ctx, second.ID, 429, "slow", 0); err != nil {
		t.Fatalf("MarkFailure() error = %v", err)
	}

	want := []int64{first.ID, third.ID, first.ID}
	for i, expected := range want {
		key, cooling, err := st.PickKey(ctx)
		if err != nil {
			t.Fatalf("PickKey() #%d error = %v", i, err)
		}
		if cooling {
			t.Errorf("PickKey() #%d cooling = true, want a released key", i)
		}
		if key.ID != expected {
			t.Fatalf("PickKey() #%d = %d, want %d", i, key.ID, expected)
		}
	}
	_ = third
}

func TestRoundRobinWithEveryKeyParkedReturnsTheSoonest(t *testing.T) {
	ctx := context.Background()
	st := strategyStore(t, StrategyRoundRobin)

	first, _ := st.CreateKey(ctx, "first", "sk_car_1")
	second, _ := st.CreateKey(ctx, "second", "sk_car_2")

	// Park the first for longer than the second.
	if err := st.MarkFailure(ctx, first.ID, 429, "slow", 0); err != nil {
		t.Fatalf("MarkFailure() error = %v", err)
	}
	if err := st.MarkFailure(ctx, second.ID, 429, "slow", 0); err != nil {
		t.Fatalf("MarkFailure() error = %v", err)
	}
	if err := st.MarkFailure(ctx, second.ID, 429, "slower", 0); err != nil {
		t.Fatalf("MarkFailure() error = %v", err)
	}

	key, cooling, err := st.PickKey(ctx)
	if err != nil {
		t.Fatalf("PickKey() error = %v", err)
	}
	if !cooling {
		t.Error("PickKey() cooling = false, want true when everything is parked")
	}
	if key.ID != first.ID {
		t.Errorf("PickKey() = %d, want the key released soonest (%d)", key.ID, first.ID)
	}
}

func TestStickyReusesOneKeyUntilItFails(t *testing.T) {
	ctx := context.Background()
	st := strategyStore(t, StrategySticky)

	first, _ := st.CreateKey(ctx, "first", "sk_car_1")
	second, _ := st.CreateKey(ctx, "second", "sk_car_2")

	chosen, _, err := st.PickKey(ctx)
	if err != nil {
		t.Fatalf("PickKey() error = %v", err)
	}
	for i := 0; i < 3; i++ {
		again, _, err := st.PickKey(ctx)
		if err != nil {
			t.Fatalf("PickKey() #%d error = %v", i, err)
		}
		if again.ID != chosen.ID {
			t.Fatalf("PickKey() #%d = %d, want the same key %d while it is healthy", i, again.ID, chosen.ID)
		}
	}

	// Park the sticky key: traffic has to move to the other one and stay there.
	if err := st.MarkFailure(ctx, chosen.ID, 429, "slow", 0); err != nil {
		t.Fatalf("MarkFailure() error = %v", err)
	}
	moved, _, err := st.PickKey(ctx)
	if err != nil {
		t.Fatalf("PickKey() after failure error = %v", err)
	}
	if moved.ID == chosen.ID {
		t.Fatalf("PickKey() = %d, want a different key after the sticky one was parked", moved.ID)
	}
	for i := 0; i < 3; i++ {
		again, _, err := st.PickKey(ctx)
		if err != nil {
			t.Fatalf("PickKey() #%d error = %v", i, err)
		}
		if again.ID != moved.ID {
			t.Fatalf("PickKey() #%d = %d, want the new sticky key %d", i, again.ID, moved.ID)
		}
	}
	_ = first
	_ = second
}

// Random has to reach every key, and must never hand back a parked one while a
// released key exists.
func TestRandomSpreadsAndAvoidsParkedKeys(t *testing.T) {
	ctx := context.Background()
	st := strategyStore(t, StrategyRandom)

	var ids []int64
	for _, label := range []string{"a", "b", "c"} {
		key, err := st.CreateKey(ctx, label, "sk_car_"+label)
		if err != nil {
			t.Fatalf("CreateKey() error = %v", err)
		}
		ids = append(ids, key.ID)
	}
	parked := ids[1]
	if err := st.MarkFailure(ctx, parked, 429, "slow", 30*time.Second); err != nil {
		t.Fatalf("MarkFailure() error = %v", err)
	}

	seen := map[int64]int{}
	for i := 0; i < 300; i++ {
		key, cooling, err := st.PickKey(ctx)
		if err != nil {
			t.Fatalf("PickKey() error = %v", err)
		}
		if cooling {
			t.Errorf("PickKey() returned a parked key while released keys existed")
		}
		if key.ID == parked {
			t.Fatalf("PickKey() returned the parked key %d", parked)
		}
		seen[key.ID]++
	}
	for _, id := range []int64{ids[0], ids[2]} {
		if seen[id] == 0 {
			t.Errorf("key %d was never chosen in 300 picks: %v", id, seen)
		}
	}
}

func TestPickKeyWithoutStrategyRowUsesTheDefault(t *testing.T) {
	ctx := context.Background()
	st := strategyStore(t, DefaultStrategy)

	first, _ := st.CreateKey(ctx, "first", "sk_car_1")
	second, _ := st.CreateKey(ctx, "second", "sk_car_2")

	// Least-recently-used: first, then the untouched second.
	for i, expected := range []int64{first.ID, second.ID} {
		key, _, err := st.PickKey(ctx)
		if err != nil {
			t.Fatalf("PickKey() #%d error = %v", i, err)
		}
		if key.ID != expected {
			t.Errorf("PickKey() #%d = %d, want %d", i, key.ID, expected)
		}
	}
}

func TestRotationCursorSurvivesAFailedPick(t *testing.T) {
	ctx := context.Background()
	st := strategyStore(t, StrategyRoundRobin)

	first, _ := st.CreateKey(ctx, "first", "sk_car_1")
	second, _ := st.CreateKey(ctx, "second", "sk_car_2")

	if _, _, err := st.PickKey(ctx); err != nil {
		t.Fatalf("PickKey() error = %v", err)
	}
	// Disable the key just used: the next pick must move on rather than
	// returning a disabled key because the cursor points at it.
	if err := st.SetKeyActive(ctx, first.ID, false); err != nil {
		t.Fatalf("SetKeyActive() error = %v", err)
	}
	key, _, err := st.PickKey(ctx)
	if err != nil {
		t.Fatalf("PickKey() error = %v", err)
	}
	if key.ID != second.ID {
		t.Errorf("PickKey() = %d, want the only enabled key %d", key.ID, second.ID)
	}

	// With nothing enabled the cursor still must not resurrect a key.
	if err := st.SetKeyActive(ctx, second.ID, false); err != nil {
		t.Fatalf("SetKeyActive() error = %v", err)
	}
	if _, _, err := st.PickKey(ctx); !errors.Is(err, ErrNoKeys) {
		t.Errorf("PickKey() with every key disabled = %v, want ErrNoKeys", err)
	}
}
