package state

import (
	"context"
	"testing"
	"time"
)

// TestCheckBaselineRoundTrip covers the baseline lifecycle: set, read
// back, replace wholesale (stale rows gone), and the empty read for a
// feature that never took one.
func TestCheckBaselineRoundTrip(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := feat(1, "Dark mode")
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	ranAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	first := []CheckResult{
		{Name: "build", Cmd: "go build ./...", OK: true, RanAt: ranAt},
		{Name: "lint", Cmd: "golangci-lint run", OK: false, ExitCode: 1, Output: "unused import", RanAt: ranAt},
	}
	if err := s.SetCheckBaseline(ctx, f.ID, first); err != nil {
		t.Fatal(err)
	}
	got, err := s.CheckBaseline(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("baseline rows = %d, want 2: %+v", len(got), got)
	}
	// ORDER BY name: build then lint
	if got[0].Name != "build" || !got[0].OK || !got[0].RanAt.Equal(ranAt) {
		t.Errorf("build row = %+v", got[0])
	}
	if got[1].Name != "lint" || got[1].OK || got[1].ExitCode != 1 || got[1].Output != "unused import" {
		t.Errorf("lint row = %+v", got[1])
	}

	// a re-baseline replaces the set — the removed lint row must not linger
	second := []CheckResult{{Name: "test", Cmd: "go test ./...", OK: true, RanAt: ranAt.Add(time.Hour)}}
	if err := s.SetCheckBaseline(ctx, f.ID, second); err != nil {
		t.Fatal(err)
	}
	if got, err = s.CheckBaseline(ctx, f.ID); err != nil || len(got) != 1 || got[0].Name != "test" {
		t.Fatalf("re-baseline = %+v (err %v), want the single test row", got, err)
	}

	// a feature without a baseline reads empty, not an error
	f2 := feat(2, "No baseline")
	if err := s.CreateFeature(ctx, f2); err != nil {
		t.Fatal(err)
	}
	if got, err = s.CheckBaseline(ctx, f2.ID); err != nil || len(got) != 0 {
		t.Fatalf("missing baseline = %+v (err %v), want empty", got, err)
	}
}
