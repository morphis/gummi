package schedule

import (
	"slices"
	"strings"
	"testing"
	"time"
)

var previewNow = time.Date(2026, 10, 3, 10, 30, 0, 0, time.UTC)

// TestPreviewCompilesPresets: a preset previews the canonical cron the
// store would hold — the same Compile the write boundary applies — and
// its coming fires.
func TestPreviewCompilesPresets(t *testing.T) {
	for _, tc := range []struct{ preset, cron string }{
		{"@daily", "0 0 * * *"},
		{"1h", "0 * * * *"},
		{"15m", "*/15 * * * *"},
	} {
		got := Preview(tc.preset, "", "", previewNow)
		if got.Err != nil {
			t.Errorf("Preview(%q) = %v, want ok", tc.preset, got.Err)
			continue
		}
		if got.Cron != tc.cron {
			t.Errorf("Preview(%q).Cron = %q, want %q", tc.preset, got.Cron, tc.cron)
		}
		if len(got.Next) != previewFires {
			t.Errorf("Preview(%q) returned %d fires, want %d", tc.preset, len(got.Next), previewFires)
		}
	}
}

// TestPreviewDailyFires: @daily's coming fires are the coming midnights,
// strictly after the ask, in the zone the preview was given.
func TestPreviewDailyFires(t *testing.T) {
	got := Preview("@daily", "", "UTC", previewNow)
	if got.Err != nil {
		t.Fatalf("Preview(@daily) = %v", got.Err)
	}
	want := []time.Time{
		time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC),
	}
	if !slices.EqualFunc(got.Next, want, func(a, b time.Time) bool { return a.Equal(b) }) {
		t.Errorf("fires = %v, want %v", got.Next, want)
	}
}

// TestPreviewBareCron: an expression previews itself, trimmed — the
// stored form is already canonical.
func TestPreviewBareCron(t *testing.T) {
	got := Preview("", "  0 5 * * *  ", "UTC", previewNow)
	if got.Err != nil {
		t.Fatalf("Preview(0 5 * * *) = %v", got.Err)
	}
	if got.Cron != "0 5 * * *" {
		t.Errorf("Cron = %q, want the trimmed expression", got.Cron)
	}
	if len(got.Next) != previewFires {
		t.Errorf("returned %d fires, want %d", len(got.Next), previewFires)
	}
}

// TestPreviewBothFilledCronWins: with both inputs present the
// expression wins and the preset is ignored — the write boundary's own
// precedence — so a both-filled form previews the cadence it saves.
func TestPreviewBothFilledCronWins(t *testing.T) {
	got := Preview("1h", "0 5 * * *", "UTC", previewNow)
	if got.Err != nil {
		t.Fatalf("Preview = %v, want ok", got.Err)
	}
	if got.Cron != "0 5 * * *" {
		t.Errorf("Cron = %q, want the expression (cron wins)", got.Cron)
	}
	if got.Next[0].Hour() != 5 {
		t.Errorf("first fire = %v, want an 05:00 fire, not the preset's", got.Next[0])
	}
}

// TestPreviewEmptyCadence: no cadence at all is the form's starting
// state, refused with the words the write boundary uses.
func TestPreviewEmptyCadence(t *testing.T) {
	got := Preview("", "", "", previewNow)
	if got.Err == nil {
		t.Fatal("Preview(,,) = ok, want a refusal")
	}
	if got.Cron != "" || len(got.Next) != 0 {
		t.Errorf("empty cadence answered cron=%q fires=%v", got.Cron, got.Next)
	}
}

// TestPreviewRefusesBadPreset: a preset the package cannot state exactly
// is refused, in Compile's own words.
func TestPreviewRefusesBadPreset(t *testing.T) {
	got := Preview("7m", "", "", previewNow)
	if got.Err == nil || !strings.Contains(got.Err.Error(), "invalid cadence") {
		t.Fatalf("Preview(7m) = %v, want Compile's refusal", got.Err)
	}
}

// TestPreviewNeverFires: an expression no wall-clock minute matches is
// refused before save — the store would take it, but it would never
// fire, and a form that accepted it would be a trap.
func TestPreviewNeverFires(t *testing.T) {
	got := Preview("", "0 0 30 2 *", "", previewNow)
	if got.Err == nil {
		t.Fatal("Preview(0 0 30 2 *) = ok, want the never-fires refusal")
	}
	if !strings.Contains(got.Err.Error(), "never") {
		t.Errorf("refusal = %v, want the feasibility one", got.Err)
	}
	if len(got.Next) != 0 {
		t.Errorf("a refused cadence answered with fires %v", got.Next)
	}
}

// TestPreviewUnknownZone: a zone name that does not load is refused, the
// same refusal an enable would hit later — surfaced in the form instead.
func TestPreviewUnknownZone(t *testing.T) {
	got := Preview("1h", "", "Mars/Olympus", previewNow)
	if got.Err == nil || !strings.Contains(got.Err.Error(), "unknown timezone") {
		t.Fatalf("Preview(Mars/Olympus) = %v, want the unknown-zone refusal", got.Err)
	}
}

// TestPreviewEmptyZoneIsHostZone: the empty timezone reads as the host's
// zone, the same reading a stored row gets — so the default pick
// previews without naming a zone.
func TestPreviewEmptyZoneIsHostZone(t *testing.T) {
	got := Preview("@hourly", "", "", previewNow)
	if got.Err != nil {
		t.Fatalf("Preview = %v", got.Err)
	}
	if zone := got.Next[0].Location(); zone != time.Local {
		t.Errorf("fires came in %v, want the host zone", zone)
	}
}
