package domain

import "testing"

// TestArtifactNounNamesEveryKind pins the wording each kind uses for its
// own design artifact, and that every kind has one.
//
// This lives on Kind rather than in a UI helper because two very
// different consumers must agree: the engine's stage kickoff, which
// tells the agent which document to go read, and every surface the
// reader sees — the thread's pinned line, the action inventory row, the
// header of the view that row opens. BG-079 and BG-081 were both the
// same defect, a document renamed between the line pointing at it and
// the thing that line opens, and both came from a second copy of this
// answer that knew about fewer kinds than existed.
func TestArtifactNounNamesEveryKind(t *testing.T) {
	want := map[Kind]string{
		KindFeature:  "spec",
		KindBug:      "bug report",
		KindResearch: "research document",
	}
	for k, w := range want {
		if got := k.ArtifactNoun(); got != w {
			t.Errorf("Kind(%q).ArtifactNoun() = %q, want %q", k, got, w)
		}
	}

	// the empty default is a feature, matching prefix()'s own default —
	// a card scanned before kinds existed reads as one
	if got := Kind("").ArtifactNoun(); got != "spec" {
		t.Errorf("the empty kind names its artifact %q, want %q", got, "spec")
	}

	// every kind the type admits has a wording, so a fourth kind cannot
	// be added without one and silently inherit "spec"
	for _, k := range []Kind{KindFeature, KindBug, KindResearch} {
		if !k.Valid() {
			t.Fatalf("precondition: %q is not a valid kind", k)
		}
		if k.ArtifactNoun() == "" {
			t.Errorf("kind %q has no artifact noun", k)
		}
	}

	// the three are distinct: a shared word would let one kind's document
	// be described in another's vocabulary without any test noticing
	seen := map[string]Kind{}
	for k := range want {
		n := k.ArtifactNoun()
		if other, dup := seen[n]; dup {
			t.Errorf("kinds %q and %q share the noun %q", other, k, n)
		}
		seen[n] = k
	}
}
