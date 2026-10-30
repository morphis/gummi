package branchlog

import (
	"strings"
	"testing"

	"hegel.dev/go/hegel"

	"github.com/morphis/gummi/internal/domain"
)

// TestPropertyARefusalNamesTheCardByItsTitle: whatever card is refused
// a rewrite, the sentence a person reads names it by its title, never by
// its id — the id is the machine's key (AGENTS.md).
func TestPropertyARefusalNamesTheCardByItsTitle(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		f := domain.Feature{
			ID:           "FF-178",
			Title:        hegel.Draw(ht, hegel.SampledFrom([]string{"Fix the login", "", "a very long title that runs on past the width a name is cut at"})),
			Kind:         hegel.Draw(ht, hegel.SampledFrom([]domain.Kind{domain.KindFreeform, domain.KindResearch, domain.KindGoal, domain.KindFeature})),
			Stage:        hegel.Draw(ht, hegel.SampledFrom([]domain.Stage{domain.StageTodo, domain.StageOpen, domain.StageDone})),
			MainCheckout: hegel.Draw(ht, hegel.Booleans()),
		}
		if hegel.Draw(ht, hegel.Booleans()) {
			f.BranchScheme = domain.BranchSchemeAdopted
		}
		busy := hegel.Draw(ht, hegel.Booleans())
		landed := hegel.Draw(ht, hegel.Booleans())
		why := Refusal(f, State{Busy: busy, Landed: landed})
		if why == "" {
			return
		}
		if strings.Contains(why, string(f.ID)) || !strings.Contains(why, f.Name()) {
			ht.Fatalf("Refusal(%+v) = %q: not named %s", f, why, f.Name())
		}
	}, hegel.WithTestCases(2000), hegel.WithDatabase(""))
}
