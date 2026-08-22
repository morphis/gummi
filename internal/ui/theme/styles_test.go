package theme

import "testing"

// TestRS_CardIDResearch_DistinctTint proves a research card's id renders
// in a tint distinct from both a plain feature id (CardID) and a bug's
// warm-tinted id (Warning), on every shipped theme — the board's only way
// to tell the three kinds apart at a glance.
func TestRS_CardIDResearch_DistinctTint(t *testing.T) {
	for name, th := range map[string]Theme{"dark": GummiDark(), "light": GummiLight()} {
		t.Run(name, func(t *testing.T) {
			s := New(th)
			card := s.CardID.Render("RS-001")
			warn := s.Warning.Render("RS-001")
			research := s.CardIDResearch.Render("RS-001")
			if research == card {
				t.Error("CardIDResearch renders identically to CardID")
			}
			if research == warn {
				t.Error("CardIDResearch renders identically to Warning")
			}
		})
	}
}
