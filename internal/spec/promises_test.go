package spec

import "testing"

const promiseDoc = "## Plan claims\n" +
	"\n" +
	"- invariant: every filter string valid today parses exactly as it does today.\n" +
	"- **invariant**: a hand-built ClauseSet still evaluates.\n" +
	"- this line is ordinary prose about invariants and is not one.\n" +
	"- golden `TestParse_Error[\"name eq c1)\"] = \"unbalanced parentheses\"` because the stack is at its root.\n" +
	"- golden: TestMatch[\"(a eq 1) and b eq 2\"] = true\n" +
	"- golden this one quotes nothing at all\n" +
	"\n" +
	"## Verification plan\n" +
	"\n" +
	"- INV-1: pass — re-ran every pre-existing case.\n" +
	"- INV-2: fail — hand-built sets now panic.\n" +
	"- UNPROVEN: lxd/images.go — needs cgo/dqlite; no check in this container compiles it\n" +
	"- UNPROVEN: lxd/instances_get.go\n"

func TestInvariantsAreNumberedInOrder(t *testing.T) {
	got := Invariants(promiseDoc)
	if len(got) != 2 {
		t.Fatalf("got %d invariants, want 2: %+v", len(got), got)
	}
	if got[0].ID != "INV-1" || got[1].ID != "INV-2" {
		t.Errorf("ids are not positional: %+v", got)
	}
	if got[0].Text != "every filter string valid today parses exactly as it does today." {
		t.Errorf("first invariant text = %q", got[0].Text)
	}
	if got[1].Text != "a hand-built ClauseSet still evaluates." {
		t.Errorf("a bolded invariant was missed or mangled: %q", got[1].Text)
	}
}

func TestGoldensCarryTheirQuotedInput(t *testing.T) {
	got := Goldens(promiseDoc)
	if len(got) != 3 {
		t.Fatalf("got %d goldens, want 3: %+v", len(got), got)
	}
	// The input is what a test file must contain — not the expected value
	// that follows it on the line.
	if got[0].Literal != `name eq c1)` {
		t.Errorf("backticked golden literal = %q, want the input a test table would hold", got[0].Literal)
	}
	if got[1].Literal != `(a eq 1) and b eq 2` {
		t.Errorf("quoted golden literal = %q", got[1].Literal)
	}
	if got[2].Literal != "" {
		t.Errorf("a golden that quotes nothing reported a literal: %q", got[2].Literal)
	}
}

func TestInvariantVerdictsReadBackByID(t *testing.T) {
	got := InvariantVerdicts(promiseDoc)
	if got["INV-1"] != "pass" || got["INV-2"] != "fail" {
		t.Errorf("verdicts = %+v", got)
	}
	if _, ok := got["INV-3"]; ok {
		t.Error("an unanswered invariant reported a verdict")
	}
}

func TestUnprovenFilesKeepTheirReason(t *testing.T) {
	got := UnprovenFiles(promiseDoc)
	if len(got) != 2 {
		t.Fatalf("got %d unproven files, want 2: %+v", len(got), got)
	}
	if got[0].Path != "lxd/images.go" || got[0].Reason == "" {
		t.Errorf("first unproven file = %+v", got[0])
	}
	if got[1].Path != "lxd/instances_get.go" || got[1].Reason != "" {
		t.Errorf("a reasonless declaration was mangled: %+v", got[1])
	}
}

func TestPromisesOfAnEmptyDocument(t *testing.T) {
	if len(Invariants("")) != 0 || len(Goldens("")) != 0 || len(UnprovenFiles("")) != 0 {
		t.Error("an empty artifact made promises")
	}
	if len(InvariantVerdicts("")) != 0 {
		t.Error("an empty artifact answered one")
	}
}

// The words are ordinary English. Only the table binds — otherwise a
// reviewer's aside would mint a numbered, gate-blocking promise nobody
// agreed to, and everyone would learn to avoid the words.
func TestPromisesComeOnlyFromTheClaimsTable(t *testing.T) {
	doc := "## Problem\n" +
		"The invariant: nothing here is a promise, it is prose.\n" +
		"- golden `\"not a promise either\"`\n" +
		"\n" +
		"### Plan claims\n" +
		"- invariant: the public signature does not change.\n" +
		"%% @reviewer: invariant: this aside must not become INV-2.\n" +
		"\n" +
		"## Progress\n" +
		"- golden `\"nor this\"` = 1\n"

	inv := Invariants(doc)
	if len(inv) != 1 || inv[0].ID != "INV-1" {
		t.Fatalf("promises leaked out of the table: %+v", inv)
	}
	if got := Goldens(doc); len(got) != 0 {
		t.Errorf("goldens leaked out of the table: %+v", got)
	}
}

// A card whose plan wrote no claims table makes no promises, and the
// floor above this has nothing to hold it to.
func TestNoClaimsTableMeansNoPromises(t *testing.T) {
	doc := "## Chosen approach\n- invariant: this heading is not the table.\n"
	if got := Invariants(doc); len(got) != 0 {
		t.Errorf("a card with no claims table made promises: %+v", got)
	}
}

// TestUnprovenNoneIsNotAFile locks the shape verify actually writes when
// every changed file was exercised. The instruction asks for one
// `UNPROVEN: <path> — <why>` line per unproven file; a stage with none
// answers it rather than skipping it, and "UNPROVEN: none" reached the
// `done` event as unproven_files:["none"] — one unproven file, named
// "none", on a card that had none. Observed on the lxd autopilot drive.
func TestUnprovenNoneIsNotAFile(t *testing.T) {
	for _, in := range []string{
		"UNPROVEN: none",
		"UNPROVEN: none — every changed file is covered by the units tests",
		"- **UNPROVEN**: N/A",
		"UNPROVEN: nothing",
	} {
		if got := UnprovenFiles(in); len(got) != 0 {
			t.Errorf("UnprovenFiles(%q) = %+v, want none — the word is the stage "+
				"saying there are no unproven files", in, got)
		}
	}
	// A real path still lands, and so does a file that merely looks like one.
	for _, c := range []struct{ in, want string }{
		{"UNPROVEN: doc/reference/instance_units.md — prose, no check reads it",
			"doc/reference/instance_units.md"},
		{"UNPROVEN: none.go — no test builds it", "none.go"},
	} {
		got := UnprovenFiles(c.in)
		if len(got) != 1 || got[0].Path != c.want {
			t.Errorf("UnprovenFiles(%q) = %+v, want one file %q", c.in, got, c.want)
		}
	}
}
