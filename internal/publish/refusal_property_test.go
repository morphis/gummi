package publish

import (
	"strings"
	"testing"

	"hegel.dev/go/hegel"

	"github.com/morphis/gummi/internal/domain"
)

// TestPropertyARefusalNamesNoCardByItsID: whatever stops a card being
// published, the sentence a person reads names no card by its id — not
// the card's own, nor the one it continued as (AGENTS.md).
func TestPropertyARefusalNamesNoCardByItsID(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		f := &domain.Feature{
			ID: "FF-178", Title: "Fix the login",
			Kind:         hegel.Draw(ht, hegel.SampledFrom([]domain.Kind{domain.KindFreeform, domain.KindFeature})),
			Stage:        domain.StageOpen,
			MainCheckout: hegel.Draw(ht, hegel.WeightedBooleans(0.2)),
		}
		if hegel.Draw(ht, hegel.WeightedBooleans(0.2)) {
			f.StackID = "ST-3"
		}
		if hegel.Draw(ht, hegel.WeightedBooleans(0.3)) {
			f.ContinuedAs = "FF-201"
		}
		c := Card{
			F: f, Busy: hegel.Draw(ht, hegel.Booleans()), Dirty: hegel.Draw(ht, hegel.Booleans()),
			Rebasing: hegel.Draw(ht, hegel.Booleans()), Landed: hegel.Draw(ht, hegel.Booleans()),
		}
		e := Refusal(c)
		if e == nil {
			return
		}
		for _, id := range []string{"FF-178", "FF-201"} {
			if strings.Contains(e.Text+" "+e.Fix, id) {
				ht.Fatalf("Refusal(%+v) = %q / %q: names %s", f, e.Text, e.Fix, id)
			}
		}
	}, hegel.WithTestCases(2000), hegel.WithDatabase(""))
}
