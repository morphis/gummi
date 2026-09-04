package ui

import (
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/workflow"
)

// hasKey reports whether a key table offers key.
func hasKey(bs []binding, key string) bool {
	for _, b := range bs {
		if b.key == key {
			return true
		}
	}
	return false
}

// TestBG091TerminalCardOffersNoGateCrossing: the document and diff
// surfaces each built a fixed key table with "g approve" in it, under a
// comment claiming every verb is live whenever the surface is. Done is
// terminal — the board's own g answers "nothing to advance" — so both
// the status bar and the ? help overlay advertised a crossing that could
// not happen.
//
// Asserted against workflow.Terminal over the canonical stage list for
// every kind rather than the one done research card the drive saw, so a
// kind whose route gains a terminal stage is covered by construction.
func TestBG091TerminalCardOffersNoGateCrossing(t *testing.T) {
	for _, k := range []domain.Kind{domain.KindFeature, domain.KindBug, domain.KindResearch} {
		id, err := domain.NewID(k, 9)
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range domain.Stages {
			f := domain.Feature{ID: id, Kind: k, Stage: st}
			want := !workflow.Terminal(k, st)
			sv := &specView{f: f}
			if got := hasKey(sv.bindings(), "g"); got != want {
				t.Errorf("%s at %s: document surface offers g = %v, want %v", k, st, got, want)
			}
			dv := &diffView{f: f}
			if got := hasKey(dv.bindings(), "g"); got != want {
				t.Errorf("%s at %s: diff surface offers g = %v, want %v", k, st, got, want)
			}
		}
	}
}

// TestBG091EscapeHatchStaysLast pins what the insertion must not break.
// The status bar sheds hints from the second-to-last backwards and never
// the last, so both tables are written to end with the way out; dropping
// or re-adding the gate row must not disturb that (the rule BG-071 and
// 67e5391 exist to keep).
func TestBG091EscapeHatchStaysLast(t *testing.T) {
	id, _ := domain.NewID(domain.KindFeature, 9)
	for _, st := range []domain.Stage{domain.StageReview, domain.StageDone} {
		f := domain.Feature{ID: id, Kind: domain.KindFeature, Stage: st}
		for name, bs := range map[string][]binding{
			"document": (&specView{f: f}).bindings(),
			"diff":     (&diffView{f: f}).bindings(),
		} {
			if len(bs) == 0 {
				t.Fatalf("%s at %s: empty key table", name, st)
			}
			if last := bs[len(bs)-1]; last.key != "esc" {
				t.Errorf("%s at %s: table ends with %q, want the escape hatch last", name, st, last.key)
			}
		}
	}
}

// TestBG091GateLeadsWhereItApplies: on a card that can still cross, the
// gate row keeps the head position both tables' shedding-order comments
// give it — the fix narrows when the row appears, not where.
func TestBG091GateLeadsWhereItApplies(t *testing.T) {
	id, _ := domain.NewID(domain.KindFeature, 9)
	f := domain.Feature{ID: id, Kind: domain.KindFeature, Stage: domain.StageSpec}
	for name, bs := range map[string][]binding{
		"document": (&specView{f: f}).bindings(),
		"diff":     (&diffView{f: f}).bindings(),
	} {
		var firstBar binding
		for _, b := range bs {
			if b.bar {
				firstBar = b
				break
			}
		}
		if firstBar.key != "g" {
			t.Errorf("%s: first bar hint is %q, want the gate row to lead", name, firstBar.key)
		}
	}
}
