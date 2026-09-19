package engine

import "testing"

// TestJudgedLineShapes pins the line shapes a verifier really writes.
//
// The verify contract asks for "DW-N: met — <evidence>", but it asks a
// model writing markdown. GL-001's verifier judged DW-6 correctly, wrote
// "- DW-6 (shared ground untouched, checks not weakened): **met** — …",
// and the goal reported the item "not checked" and 5 of 6 met. The
// verdict was there; only the parser missed it.
func TestJudgedLineShapes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		line     string
		id       string
		verdict  string
		evidence string
	}{
		{"the contract's own shape",
			"DW-1: met — the evidence", "DW-1", "met", "the evidence"},
		{"bulleted",
			"- DW-2: not met — no card built it", "DW-2", "not met", "no card built it"},
		{"parenthetical gloss and emphasis, as GL-001's verifier wrote it",
			"- DW-6 (shared ground untouched, checks not weakened): **met** — git diff shows only build.go",
			"DW-6", "met", "git diff shows only build.go"},
		{"emphasis alone",
			"- DW-3: *not met* — the session never established", "DW-3", "not met", "the session never established"},
		{"en dash",
			"- DW-4 (ECMP): met – two equal-cost routes", "DW-4", "met", "two equal-cost routes"},
		{"lowercase id",
			"- dw-5: met — check passed", "dw-5", "met", "check passed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := judgedLineRe.FindStringSubmatch(tc.line)
			if m == nil {
				t.Fatalf("no verdict read from %q", tc.line)
			}
			if m[1] != tc.id || m[2] != tc.verdict {
				t.Errorf("read %q/%q, want %q/%q", m[1], m[2], tc.id, tc.verdict)
			}
			if m[3] != tc.evidence {
				t.Errorf("evidence %q, want %q", m[3], tc.evidence)
			}
		})
	}
}

// TestJudgedLineRejectsProse is the other half: a generous parser must
// still refuse a sentence that merely mentions an item. Reading a verdict
// out of discussion would be worse than missing one, because it would
// report a result nobody reached.
func TestJudgedLineRejectsProse(t *testing.T) {
	for _, line := range []string{
		"DW-6 is not met by any card yet, so I looked at the branch",
		"Neither DW-1 nor DW-2 met the bar for live proof",
		"see DW-3 met in the earlier run",
		"The item DW-4 (ECMP) was judged elsewhere",
	} {
		if m := judgedLineRe.FindStringSubmatch(line); m != nil {
			t.Errorf("read a verdict %q/%q from prose: %q", m[1], m[2], line)
		}
	}
}
