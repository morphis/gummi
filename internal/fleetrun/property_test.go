package fleetrun

// Property tests of the workspace fold against the per-card one, driven by
// hegel (https://hegel.dev). The stats tab is made of the cards: over a
// window holding their whole lives each lane is its card's own total,
// the report is its lanes, and splitting the window charges every pass
// to exactly one side. Cards are sessions with spend rows across their
// backends' generations, and spend the rows never recorded.

import (
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"

	"hegel.dev/go/hegel"

	"github.com/morphis/gummi/internal/cardrun"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

const propCases = 1000

var t0 = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

func drawInput(ht *hegel.T) Input {
	in := Input{Now: t0.Add(60 * 24 * time.Hour), Window: Window{To: t0.Add(61 * 24 * time.Hour)}}
	n := hegel.Draw(ht, hegel.Integers(1, 4))
	for i := range n {
		f := domain.Feature{ID: domain.FeatureID(fmt.Sprintf("FF-%d", i+1)), Title: "s", Kind: domain.KindFreeform, Stage: domain.StageOpen}
		var rows []state.StageSpend
		sessions := hegel.Draw(ht, hegel.Integers(0, 3))
		for range sessions {
			startMin := hegel.Draw(ht, hegel.Integers(0, 50*24*60))
			start := t0.Add(time.Duration(startMin) * time.Minute)
			models := hegel.Draw(ht, hegel.Integers(1, 2))
			for m := range models {
				cents := hegel.Draw(ht, hegel.Integers(0, 5000))
				est := hegel.Draw(ht, hegel.Integers(0, cents))
				dur := hegel.Draw(ht, hegel.Integers(0, 600))
				rows = append(rows, state.StageSpend{
					Stage: domain.StageOpen, Session: strconv.FormatInt(start.UnixNano(), 10), Model: fmt.Sprintf("m%d", m),
					Credits: float64(cents), EstimatedCredits: float64(est), UpdatedAt: start.Add(time.Duration(dur) * time.Minute),
				})
				f.Spend.Credits += float64(cents)
			}
		}
		// spend the rows never recorded: the counter runs ahead of them
		if hegel.Draw(ht, hegel.WeightedBooleans(0.3)) {
			extra := hegel.Draw(ht, hegel.Integers(1, 500))
			f.Spend.Credits += float64(extra)
		}
		f.CreatedAt = t0
		f.UpdatedAt = t0.Add(55 * 24 * time.Hour)
		run := cardrun.Report(cardrun.Input{Feature: f, Spend: rows})
		in.Cards = append(in.Cards, Card{Feature: f, Run: run, Spend: rows})
		in.Rows = append(in.Rows, AllTimeRow{Feature: f, StageSpend: rows})
	}
	return in
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// TestPropertyALaneOverAWholeLifeIsItsCardsTotal: over a window holding
// every card's whole life, each lane's credits are its card's own total —
// the figure its board row prints.
func TestPropertyALaneOverAWholeLifeIsItsCardsTotal(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		in := drawInput(ht)
		r := Fold(in)
		total := map[domain.FeatureID]float64{}
		for _, c := range in.Cards {
			total[c.Feature.ID] = c.Run.Money.Credits
		}
		seen := map[domain.FeatureID]bool{}
		for _, l := range r.Lanes {
			seen[l.ID] = true
			if !near(l.Credits, total[l.ID]) {
				ht.Fatalf("lane %s: %v, the card's own total %v", l.ID, l.Credits, total[l.ID])
			}
		}
		for id, c := range total {
			if c > 0 && !seen[id] {
				ht.Fatalf("card %s spent %v and has no lane", id, c)
			}
		}
	}, hegel.WithTestCases(propCases), hegel.WithDatabase(""))
}

// TestPropertyTheReportIsItsLanes: the window's credits are its lanes'
// summed, and its stage and model buckets each sum to them.
func TestPropertyTheReportIsItsLanes(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		in := drawInput(ht)
		if hegel.Draw(ht, hegel.Booleans()) {
			from := hegel.Draw(ht, hegel.Integers(0, 60*24))
			in.Window.From = t0.Add(time.Duration(from) * time.Hour / 24)
		}
		r := Fold(in)
		var lanes, stages, models float64
		for _, l := range r.Lanes {
			lanes += l.Credits
		}
		for _, b := range r.ByStage {
			stages += b.Credits
		}
		for _, b := range r.ByModel {
			models += b.Credits
		}
		if !near(r.Credits, lanes) || !near(r.Credits, stages) || !near(r.Credits, models) {
			ht.Fatalf("credits %v; lanes %v, stages %v, models %v", r.Credits, lanes, stages, models)
		}
		if r.Estimated > r.Credits+1e-6 {
			ht.Fatalf("estimated %v of %v", r.Estimated, r.Credits)
		}
	}, hegel.WithTestCases(propCases), hegel.WithDatabase(""))
}

// TestPropertySplittingTheWindowChargesEachPassOnce: two windows that
// split the whole life at any moment charge between them what the whole
// does — a pass is charged to the window it started in, once.
func TestPropertySplittingTheWindowChargesEachPassOnce(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		in := drawInput(ht)
		whole := Fold(in).Credits
		cut := hegel.Draw(ht, hegel.Integers(0, 60*24*60))
		at := t0.Add(time.Duration(cut) * time.Minute)
		before, after := in, in
		before.Window = Window{From: in.Window.From, To: at}
		after.Window = Window{From: at, To: in.Window.To}
		if a, b := Fold(before).Credits, Fold(after).Credits; !near(a+b, whole) {
			ht.Fatalf("split at %s: %v + %v, the whole window %v", at, a, b, whole)
		}
	}, hegel.WithTestCases(propCases), hegel.WithDatabase(""))
}
