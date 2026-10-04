package ui

import (
	"testing"

	"github.com/morphis/gummi/internal/webapi"
)

// A prose line at a stop whose answers take no words is the TUI's to
// read (routeThreadLine hands it to the reader; nothing is answered), and
// the web routes it the same way: "read", never an answer the page would
// give as the highlighted option with the line dropped.
func TestProseAtAStopNoAnswerTakesWordsIsReadNotAnswered(t *testing.T) {
	m := populatedShell(160, 50)
	for i := range m.rows {
		r := m.rows[i]
		func() {
			leave := m.enterCard(r.F.ID, true)
			defer leave()
			od := m.webOpenDecision(r)
			if od == nil || od.api.Kind == webapi.DecisionAsk {
				return
			}
			words := false
			for _, o := range od.api.Options {
				words = words || o.Words
			}
			c := m.webComposer(r, "please also handle the empty state")
			m.threadInput.SetValue("please also handle the empty state")
			label, _ := threadEnterLabel("please also handle the empty state")
			aim := m.wordAim(od.d)
			m.threadInput.Reset()
			t.Logf("%s %s words=%v route=%s says=%q | TUI aim=%d enter-bar=%q; page would answer %q bare",
				r.F.ID, od.api.Kind, words, c.Route, c.Says, aim, label, od.api.Options[0].ID)
			if !words && c.Route == webapi.RouteAnswer {
				t.Errorf("%s: prose is RouteAnswer with no option to carry it — the page answers %q bare; the TUI's enter says %q", r.F.ID, od.api.Options[0].Label, label)
			}
		}()
	}
}

// Words that go with a stop's answer are read before they go (the
// reading is put to the person as a chip of its own), so the composer
// marks them Read and the page says that rather than promising the
// answer itself; an ask's reply answers at once and is never marked.
func TestProseGoingWithAStopsAnswerIsMarkedRead(t *testing.T) {
	m := populatedShell(160, 50)
	seen := 0
	for i := range m.rows {
		r := m.rows[i]
		func() {
			leave := m.enterCard(r.F.ID, true)
			defer leave()
			od := m.webOpenDecision(r)
			if od == nil {
				return
			}
			c := m.webComposer(r, "please also handle the empty state")
			if c.Route != webapi.RouteAnswer {
				if c.Read {
					t.Errorf("%s: route %s is marked read; only an answer's words are", r.F.ID, c.Route)
				}
				return
			}
			ask := od.api.Kind == webapi.DecisionAsk
			if c.Read == ask {
				t.Errorf("%s (%s): read=%v says=%q; want read=%v", r.F.ID, od.api.Kind, c.Read, c.Says, !ask)
			}
			if !ask {
				seen++
			}
		}()
	}
	if seen == 0 {
		t.Fatal("no stop took the line as an answer's words; the case went untested")
	}
}
