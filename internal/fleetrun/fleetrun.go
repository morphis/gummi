package fleetrun

import (
	"sort"
	"strconv"
	"time"

	"github.com/morphis/gummi/internal/cardrun"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// Span re-uses cardrun's interval type: the timeline draws the same
// spans the per-card clock sums, and giving them a second shape would
// be one more thing to translate without gaining a word.
type Span = cardrun.Span

// Window is the half-open interval a report covers: [From, To). A zero
// From is the workspace's whole history.
type Window struct {
	From, To time.Time
}

// Contains reports whether t falls inside the window.
func (w Window) Contains(t time.Time) bool {
	return !t.Before(w.From) && t.Before(w.To)
}

// Card is one card's record as the fold reads it: the feature (whose
// own counters are the authoritative totals), whether the branch has
// reached the base branch, when the card settled, and the whole-life
// run and log the timeline draws from. The fold does its own
// windowing, so a caller passes whole records, never clipped ones —
// the same rule cardrun.Input states for its own reads.
type Card struct {
	Feature  domain.Feature
	Landed   bool
	LandedAt time.Time
	Run      cardrun.Run
	Events   []state.CardEvent
}

// AllTimeRow is one board row's contribution to the all-time ledger:
// counters only, no log. All-time is read off the rollups the board
// already carries, and no surface should pay a query per card to draw
// a column of totals.
type AllTimeRow struct {
	Feature    domain.Feature
	Landed     bool
	StageSpend []state.StageSpend
}

// Input is everything Report needs, gathered by whoever has a store.
type Input struct {
	// Now is the moment the report is measured at. Open ends — a session
	// still running, a decision still unanswered — are drawn and summed
	// to it.
	Now time.Time
	// Window is the interval the window figures cover.
	Window Window
	// Cards is every card that may have activity inside the window. The
	// fold skips those whose spans miss it entirely.
	Cards []Card
	// Rows is every card on the board, for the all-time ledger.
	Rows []AllTimeRow
}

// Block is one session on a timeline lane, already clipped to the
// window. Open marks a session the record has not closed — its block
// runs to the window's right edge, which is the honest reading of
// still running.
type Block struct {
	From, To time.Time
	Stage    domain.Stage
	Open     bool
}

// Lane is one card's track on the timeline: what ran, when it waited on
// a person, and the marks it left. Every time in here is clipped to the
// report's window; the card's window clock is not on the lane — it is
// summed into the report, where the headline reads it.
type Lane struct {
	ID      domain.FeatureID
	Title   string
	Kind    domain.Kind
	Ending  domain.Ending
	Settled bool

	// Blocks are the stage sessions, oldest first; Waits the
	// waiting-on-you spans, unioned; Gates the crossings; LandedAt the
	// landing mark. Zero when the mark fell outside the window.
	Blocks   []Block
	Waits    []Span
	Gates    []time.Time
	LandedAt time.Time

	// OpenWaitFrom is the start of the wait that is still open at the
	// right edge — the clause a lane with nobody home carries ("parked
	// on you since 14:02"). Zero when nothing is.
	OpenWaitFrom time.Time

	// Credits is what this card's passes started in the window cost, and
	// Redo the subset of that spent doing work the card had already
	// done. Both are window figures; the card's whole-life total lives
	// on its own run tab.
	Credits, Redo float64

	// Running is whether a session is still live at the right edge.
	Running bool

	// Last is the lane's newest moment inside the window — the ordering
	// key. Zero on a lane with nothing in the window, which the fold
	// drops instead of ordering.
	Last time.Time

	// Note is the one-line diagnosis the top-cards list carries — why
	// this lane is on that list. Empty on a lane that is merely costly.
	Note string
}

// AllTime is the ledger the window sits beside: the workspace's own
// counters, folded over every card the board holds. Unlike the window
// figures it needs no attribution rule — the counters are already
// totals — which is exactly why the two columns never try to agree.
type AllTime struct {
	Cards, Settled, Landed int
	ByEnding               map[domain.Ending]int
	Credits, Estimated     float64
	ByStage, ByModel       []cardrun.Bucket
}

// Report is the workspace's fold: the window money, the window clock,
// the timeline, and the all-time ledger beside them.
type Report struct {
	Window Window
	// RateSpan is the horizon the burn rate is read over: the window
	// itself, or — on an all-history window — first activity to now, so
	// a rate is never divided by a span nothing ran in.
	RateSpan time.Duration

	// Credits is what the window's passes cost, Estimated the subset a
	// provider may still settle differently, and Rework the subset spent
	// doing work the card had already done — Corrected after a verdict,
	// Reproved over a new base.
	Credits, Estimated          float64
	Rework, Corrected, Reproved float64
	ByStage, ByModel            []cardrun.Bucket

	// The window clock, summed across the window's cards: agent working,
	// waiting on a person, nothing running and nobody asked, and the
	// summed card lives the two shares are read against.
	Agent, OnYou, Idle, Elapsed time.Duration

	// Running is how many lanes have a session live at the right edge —
	// the headline's "3 running". A field of the fold rather than
	// something the caller counts, so a surface reading the report
	// cannot disagree with the lanes it was drawn from.
	Running int

	// PeakLanes is how many cards ran at once at the busiest moment, and
	// Busiest the start of the stretch with the most agent time — an
	// hour on a window short enough to read in hours, a day on a longer
	// one. Both zero when nothing ran.
	PeakLanes    int
	Busiest      time.Time
	BusiestLen   time.Duration
	BusiestAgent time.Duration

	// Lanes is the timeline, newest activity first.
	Lanes []Lane
	// Top is the window's costliest lanes, costliest first, notes
	// attached.
	Top []Lane

	AllTime AllTime
}

// Empty reports a report with nothing to draw: no lanes, no window
// spend, and a board the all-time ledger cannot count either.
func (r Report) Empty() bool {
	return len(r.Lanes) == 0 && r.Credits == 0 && r.AllTime.Cards == 0
}

// Fold is the workspace's fold, and it is a pure function: no store, no
// context, no clock — the caller owns Now, and an open end is drawn and
// summed to it.
func Fold(in Input) Report {
	rep := Report{Window: in.Window, AllTime: AllTime{ByEnding: map[domain.Ending]int{}}}
	rep.RateSpan = rateSpan(in)
	for _, c := range in.Cards {
		if lane, ok := buildLane(c, in.Window, in.Now); ok {
			rep.Lanes = append(rep.Lanes, lane)
		}
		cl := windowClock(c, in.Window, in.Now)
		rep.Elapsed += cl.Elapsed
		rep.Agent += cl.Agent
		rep.OnYou += cl.OnYou
		rep.Idle += cl.Idle
	}
	sort.SliceStable(rep.Lanes, func(i, j int) bool { return rep.Lanes[i].Last.After(rep.Lanes[j].Last) })
	for _, l := range rep.Lanes {
		rep.Credits += l.Credits
		rep.Rework += l.Redo
		if l.Running {
			rep.Running++
		}
	}
	rep.ByStage, rep.ByModel = windowBuckets(in)
	// The rework split, by why the work was done twice. The lane fold
	// has already charged it; this walk names the reasons, over the same
	// passes the lanes charged, so the two can never disagree.
	for _, c := range in.Cards {
		for _, s := range c.Run.Sessions {
			if !in.Window.Contains(s.Started) || !s.Redo {
				continue
			}
			if s.RedoReason == cardrun.Reproved {
				rep.Reproved += s.Credits
			} else {
				rep.Corrected += s.Credits
			}
		}
	}
	rep.PeakLanes, rep.Busiest, rep.BusiestLen, rep.BusiestAgent =
		concurrency(rep.Lanes, in.Window, busiestLen(in.Window))
	rep.Top = topCards(rep.Lanes, 3)
	rep.AllTime = allTime(in.Rows)
	return rep
}

// rateSpan resolves the horizon a burn rate is read over. A window with
// a fixed start is its own horizon; an all-history window runs from the
// earliest activity on record, because credits-per-hour divided by a
// span that predates the first card would understate the rate by
// whatever the workspace sat idle.
func rateSpan(in Input) time.Duration {
	w := in.Window
	if !w.From.IsZero() {
		return w.To.Sub(w.From)
	}
	earliest := time.Time{}
	for _, c := range in.Cards {
		for _, s := range c.Run.Sessions {
			if s.Started.IsZero() {
				continue
			}
			if earliest.IsZero() || s.Started.Before(earliest) {
				earliest = s.Started
			}
		}
	}
	if earliest.IsZero() || !earliest.Before(w.To) {
		return 0
	}
	return w.To.Sub(earliest)
}

// buildLane builds one card's timeline lane. The bool reports whether
// the card has anything in the window at all — a card whose whole life
// falls outside it contributes nothing, not even an empty row.
func buildLane(c Card, w Window, now time.Time) (Lane, bool) {
	l := Lane{
		ID:      c.Feature.ID,
		Title:   c.Feature.Title,
		Kind:    c.Feature.Kind,
		Ending:  c.Feature.Ending(c.Landed),
		Settled: c.Feature.Stage == domain.StageDone,
	}

	// Blocks: every pass, clipped to the window; an open pass runs to
	// now and then to the right edge.
	for _, s := range c.Run.Sessions {
		if s.Started.IsZero() {
			continue
		}
		from, to, open := s.Started, s.Ended, !s.Closed
		if open {
			to = now
		}
		if from.Before(w.From) {
			from = w.From
		}
		if to.After(w.To) {
			to = w.To
		}
		if to.After(from) {
			l.Blocks = append(l.Blocks, Block{From: from, To: to, Stage: s.Stage, Open: open})
			l.Last = later(l.Last, to)
		}
		if open {
			l.Running = true
		}
		// The window charges a pass to the window it started in (the
		// package doc owns the rule), and the lane keeps its own share.
		if w.Contains(s.Started) {
			l.Credits += s.Credits
			if s.Redo {
				l.Redo += s.Credits
			}
		}
	}

	// Waits: the same spans the card's own clock sums, clipped to the
	// window. A decision nobody has answered still runs to the right
	// edge — that is the wait the reader is standing in, and the lane
	// names it by its start rather than hiding it in a filled column.
	l.Waits = cardrun.WaitSpans(c.Events, w.From, w.To)
	for _, sp := range l.Waits {
		l.Last = later(l.Last, sp.To)
	}
	for _, sp := range cardrun.DecisionSpans(c.Events) {
		if !sp.To.IsZero() {
			continue
		}
		from := sp.From
		if from.Before(w.From) {
			from = w.From
		}
		if l.OpenWaitFrom.IsZero() || from.Before(l.OpenWaitFrom) {
			l.OpenWaitFrom = from
		}
	}

	// Marks: gate crossings, and the landing where the card settled.
	for _, ev := range c.Events {
		if ev.Kind == state.EventGate && w.Contains(ev.At) {
			l.Gates = append(l.Gates, ev.At)
			l.Last = later(l.Last, ev.At)
		}
	}
	if !c.LandedAt.IsZero() && w.Contains(c.LandedAt) {
		l.LandedAt = c.LandedAt
		l.Last = later(l.Last, c.LandedAt)
	}

	l.Note = laneNote(c, w)
	if len(l.Blocks) == 0 && len(l.Waits) == 0 && len(l.Gates) == 0 && l.LandedAt.IsZero() {
		return Lane{}, false
	}
	return l, true
}

// laneNote is the one-line diagnosis a costly lane carries, read off
// the passes it started in the window — the same comparison the card's
// run tab makes (a redo costing more than its first pass), at fleet
// grain. The first comparison wins; the rest are the fallbacks, and a
// lane that is merely costly gets no clause rather than a filler.
func laneNote(c Card, w Window) string {
	type work struct{ stage, role, flavor string }
	first := map[work]float64{}
	var reproofs, corrections, redone int
	var note string
	for _, s := range c.Run.Sessions {
		if !w.Contains(s.Started) {
			continue
		}
		key := work{string(s.Stage), s.Role, s.Flavor}
		if prev, seen := first[key]; seen {
			redone++
			if s.RedoReason == cardrun.Reproved {
				reproofs++
			} else {
				corrections++
			}
			if note == "" && s.Credits > prev && prev > 0 {
				note = "the redo cost more than the first pass"
			}
			continue
		}
		first[key] = s.Credits
	}
	switch {
	case note != "":
	case reproofs >= 2:
		note = "re-proved " + strconv.Itoa(reproofs) + " times after a rebase"
	case reproofs == 1:
		note = "re-proved once after a rebase"
	case corrections >= 2:
		note = "sent back " + strconv.Itoa(corrections) + " times after a verdict"
	case corrections == 1:
		note = "sent back once after a verdict"
	case redone > 0:
		note = "rework — work this card had already done"
	}
	return note
}

// cardWindow is one card's clock over the window: the four sums the
// report's headline reads. See the package doc for why the window clock
// counts an open session to the right edge where the per-card clock
// stops a card's life at its last closed session.
type cardWindow struct {
	Agent, OnYou, Idle, Elapsed time.Duration
}

func windowClock(c Card, w Window, now time.Time) cardWindow {
	var cl cardWindow
	var first, lifeEnd time.Time
	type interval struct{ from, to time.Time }
	var agent []interval
	for _, s := range c.Run.Sessions {
		if s.Started.IsZero() {
			continue
		}
		if first.IsZero() || s.Started.Before(first) {
			first = s.Started
		}
		to := s.Ended
		if !s.Closed {
			to = now
		}
		if to.After(lifeEnd) {
			lifeEnd = to
		}
		agent = append(agent, interval{s.Started, to})
	}
	// A card parked on an unanswered decision is alive, whatever the
	// sessions say: its life reaches now, and the wait it is standing in
	// belongs to the window.
	for _, sp := range cardrun.DecisionSpans(c.Events) {
		if sp.To.IsZero() {
			lifeEnd = now
			break
		}
	}
	if first.IsZero() {
		return cl
	}
	lifeFrom := later(first, w.From)
	lifeTo := lifeEnd
	if lifeTo.IsZero() || lifeTo.After(w.To) {
		lifeTo = w.To
	}
	if lifeTo.After(lifeFrom) {
		cl.Elapsed = lifeTo.Sub(lifeFrom)
	}
	for _, a := range agent {
		from := later(a.from, w.From)
		to := a.to
		if to.IsZero() || to.After(w.To) {
			to = w.To
		}
		if to.After(from) {
			cl.Agent += to.Sub(from)
		}
	}
	if cl.Agent > cl.Elapsed {
		cl.Agent = cl.Elapsed
	}
	// The residual is the ceiling on what can be charged to a person,
	// exactly as the per-card clock caps it: a decision can stand open
	// while a session runs, and that time is the agent's.
	cl.OnYou = cardrun.WaitTime(c.Events, w.From, w.To)
	if cl.OnYou > cl.Elapsed-cl.Agent {
		cl.OnYou = cl.Elapsed - cl.Agent
	}
	if cl.OnYou < 0 {
		cl.OnYou = 0
	}
	cl.Idle = cl.Elapsed - cl.Agent - cl.OnYou
	return cl
}

// windowBuckets folds the window's passes into the by-stage and
// by-model shares the money column prints. The attribution is the same
// rule the totals follow — a pass counts where it started — so the
// buckets sum back to the credits they sit beside.
func windowBuckets(in Input) (stage, model []cardrun.Bucket) {
	bs, bm := map[string]float64{}, map[string]float64{}
	for _, c := range in.Cards {
		for _, s := range c.Run.Sessions {
			if !in.Window.Contains(s.Started) {
				continue
			}
			bs[string(s.Stage)] += s.Credits
			m := s.Model
			if m == "" {
				m = "unknown"
			}
			bm[m] += s.Credits
		}
	}
	return cardrun.Buckets(bs), cardrun.Buckets(bm)
}

// concurrency reads the two figures nothing sums to: how many lanes ran
// at once at the busiest moment, and which stretch of the window took
// the most agent time. The sweep counts a session as running over its
// half-open interval, so a lane ending exactly as another starts is one
// lane, not two.
func concurrency(lanes []Lane, w Window, bucket time.Duration) (peak int, busiest time.Time, busiestLen, busiestAgent time.Duration) {
	type ev struct {
		at time.Time
		up bool
	}
	var evs []ev
	for _, l := range lanes {
		for _, b := range l.Blocks {
			evs = append(evs, ev{b.From, true}, ev{b.To, false})
		}
	}
	sort.Slice(evs, func(i, j int) bool {
		if !evs[i].at.Equal(evs[j].at) {
			return evs[i].at.Before(evs[j].at)
		}
		// At a shared moment the lane that ends goes first: half-open
		// intervals do not overlap at their shared edge.
		return !evs[i].up && evs[j].up
	})
	cur := 0
	for _, e := range evs {
		if e.up {
			cur++
			if cur > peak {
				peak = cur
			}
		} else {
			cur--
		}
	}
	if bucket <= 0 {
		return peak, busiest, busiestLen, busiestAgent
	}
	for from := w.From; from.Before(w.To); from = from.Add(bucket) {
		to := from.Add(bucket)
		if to.After(w.To) {
			to = w.To
		}
		var agent time.Duration
		for _, l := range lanes {
			for _, b := range l.Blocks {
				agent += overlap(b.From, b.To, from, to)
			}
		}
		if agent > busiestAgent {
			busiestAgent = agent
			busiest = from
			busiestLen = to.Sub(from)
		}
	}
	return peak, busiest, busiestLen, busiestAgent
}

// overlap is the length of two half-open intervals' shared stretch.
func overlap(aFrom, aTo, bFrom, bTo time.Time) time.Duration {
	from, to := later(aFrom, bFrom), earlier(aTo, bTo)
	if !to.After(from) {
		return 0
	}
	return to.Sub(from)
}

func earlier(a, b time.Time) time.Time {
	if a.IsZero() || b.Before(a) {
		return b
	}
	return a
}

func later(a, b time.Time) time.Time {
	if a.IsZero() || b.After(a) {
		return b
	}
	return a
}

// busiestLen picks the stretch a window's busiest-hour figure is read
// in: an hour while the window is short enough to read in hours, a day
// once it is not. Reporting a workspace's busiest 15 minutes of a
// month would be a fact about rounding, not about the board.
func busiestLen(w Window) time.Duration {
	if w.From.IsZero() || w.To.Sub(w.From) > 48*time.Hour {
		return 24 * time.Hour
	}
	return time.Hour
}

// topCards is the window's costliest lanes, costliest first, the
// diagnosis each one earned riding beside it. A lane that spent nothing
// is not "cheap" — it did not run — and is left out, the way the week
// view refuses to crown a card that never ran the week's bargain.
func topCards(lanes []Lane, n int) []Lane {
	var out []Lane
	for _, l := range lanes {
		if l.Credits > 0 {
			out = append(out, l)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Credits > out[j].Credits })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// allTime folds every board row into the all-time ledger. Endings are
// counted only where the record names one — a card that has not ended
// is not a fourth ending, it is an open card.
func allTime(rows []AllTimeRow) AllTime {
	a := AllTime{ByEnding: map[domain.Ending]int{}}
	bs, bm := map[string]float64{}, map[string]float64{}
	for _, r := range rows {
		a.Cards++
		if r.Feature.Stage == domain.StageDone {
			a.Settled++
		}
		if end := r.Feature.Ending(r.Landed); end != domain.EndingNone {
			a.ByEnding[end]++
		}
		if r.Feature.LandedSHA != "" {
			a.Landed++
		}
		a.Credits += r.Feature.Spend.Credits
		a.Estimated += r.Feature.Spend.EstimatedCredits
		for _, s := range r.StageSpend {
			bs[string(s.Stage)] += s.Credits
			bm[s.Model] += s.Credits
		}
	}
	a.ByStage, a.ByModel = cardrun.Buckets(bs), cardrun.Buckets(bm)
	return a
}
