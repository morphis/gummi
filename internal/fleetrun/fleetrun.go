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
	// Spend is the same session-grained rollup Run was built from. The
	// fold reads it raw for one thing cardrun deliberately does not give
	// back: a freeform card logs no stage_enter/stage_exit (DESIGN
	// §19.3a), so it never gets a cardrun.Session — the spend lands as
	// Money.Charges, which have a moment but no span. Its rows still
	// each name a span, though: Session is the backend's own start (the
	// same generation stamp a stage pass's key already is), so a block
	// is read back out of it directly (see freeformBlocks).
	Spend []state.StageSpend
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
	// Busy is the board's own reading of which cards are running at Now:
	// the set its header counts, which is what a person reads as
	// running. A card's record cannot say it — a session left open by a
	// process that died looks exactly like one working, and one blocked
	// on its own question is open on the record while the board says it
	// needs you. Nil when the reader has no live board (a cold CLI, a
	// test); the fold then reads it off the record as best the record
	// can: an open pass on a card with no decision standing open.
	Busy map[domain.FeatureID]bool
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

	// Credits is what this card spent in the window — the passes it
	// started there, and the spend no pass holds whose moment
	// (cardrun.Charge.At) fell there — Estimated the subset a provider
	// may still settle differently, and Redo the subset spent doing
	// work the card had already done. All are window figures; over a
	// window holding the card's whole life, Credits is the card's own
	// total (cardrun.Money.Credits), the figure its board row prints.
	Credits, Estimated, Redo float64

	// Tokens is what those same passes spent in tokens. It is charged by
	// the lane and on the same condition as Credits, so the two can
	// never come to cover different passes.
	Tokens Tokens

	// Running is whether the card is running at the right edge, by the
	// board's reading when the fold was given one (Input.Busy).
	Running bool

	// working is the stretches of Blocks an agent was actually working —
	// each block less the time it stood blocked on its own question
	// (cardrun.WorkingSpans). Concurrency and the busiest stretch are
	// read off these, not off the blocks the lane draws.
	working []Span

	// Last is the lane's newest moment inside the window — the ordering
	// key. Zero on a lane with nothing in the window, which the fold
	// drops instead of ordering.
	Last time.Time

	// Note is the one-line diagnosis the top-cards list carries — why
	// this lane is on that list. Empty on a lane that is merely costly.
	Note string
}

// Tokens is a token count split the way the adapters report it: input
// the provider read fresh, input it served from the prompt cache, and
// output. It is a separate figure from credits and not a second way of
// spelling them — a provider bills what it bills, and a window whose
// cache did the work costs less than its token count suggests.
type Tokens struct {
	Input, Cached, Output int64
}

// Total is every token the report accounts for, both sides of the
// conversation.
func (t Tokens) Total() int64 { return t.Input + t.Cached + t.Output }

// Zero reports a count with nothing in it — a window whose passes ran
// on a backend that never said, as much as one where nothing ran.
func (t Tokens) Zero() bool { return t.Total() == 0 }

// CacheReadRatio is the share of the input side served from the prompt
// cache. It is cardrun.Money.CacheReadRatio one scale up, deliberately
// the same arithmetic: two surfaces that meant different things by
// "cached" would be worse than one that said nothing. Zero when the
// backend reports no cache reads, which is not the same as a cache that
// never hit.
func (t Tokens) CacheReadRatio() float64 {
	in := t.Input + t.Cached
	if in <= 0 {
		return 0
	}
	return float64(t.Cached) / float64(in)
}

func (t *Tokens) add(o Tokens) {
	t.Input += o.Input
	t.Cached += o.Cached
	t.Output += o.Output
}

// AllTime is the ledger the window sits beside: the workspace's own
// counters, folded over every card the board holds. Unlike the window
// figures it needs no attribution rule — the counters are already
// totals — and a window that holds every card's whole life comes to the
// same figure, because the window charges every credit a card's counter
// holds somewhere (the package doc owns the rule).
type AllTime struct {
	Cards, Settled, Landed int
	ByEnding               map[domain.Ending]int
	Credits, Estimated     float64
	ByStage, ByModel       []cardrun.Bucket

	// Tokens is every token the board's rollup rows hold, which is the
	// token side of the same counters Credits comes from: whole-card
	// totals, no attribution rule, non-pass turns included.
	Tokens Tokens
}

// Report is the workspace's fold: the window money, the window clock,
// the timeline, and the all-time ledger beside them.
type Report struct {
	Window Window
	// RateSpan is the horizon the burn rate is read over: the window
	// itself, or — on an all-history window — first activity to now, so
	// a rate is never divided by a span nothing ran in.
	RateSpan time.Duration

	// Credits is what the window's cards spent in it, summed off the
	// lanes — passes by where they started, other spend by its one
	// moment (the package doc owns the rule). Estimated is the subset a
	// provider may still settle differently, and Rework the subset spent
	// doing work the card had already done — Corrected after a verdict,
	// Reproved over a new base.
	Credits, Estimated          float64
	Rework, Corrected, Reproved float64
	ByStage, ByModel            []cardrun.Bucket

	// Tokens is what the window's spend came to in tokens, summed off the
	// lanes. It covers exactly the spend Credits covers.
	Tokens Tokens

	// The window clock, summed across the window's cards: agent working,
	// waiting on a person, nothing running and nobody asked, and the
	// summed card lives the two shares are read against.
	Agent, OnYou, Idle, Elapsed time.Duration

	// Running is how many lanes are running at the right edge — the
	// headline's "3 running", by the board's reading (Input.Busy). A field of the fold rather than
	// something the caller counts, so a surface reading the report
	// cannot disagree with the lanes it was drawn from.
	Running int

	// PeakLanes is how many cards had an agent working at once at the
	// busiest moment, and Busiest the start of the stretch with the most
	// agent time — an hour on a window short enough to read in hours, a
	// day on a longer one. Both are read off the lanes' working time,
	// never the time a session stood blocked on its own question, so
	// the busiest stretch is a part of the same agent time Agent sums.
	// Both zero when nothing ran.
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
		if lane, ok := buildLane(c, in.Window, in.Now, in.Busy); ok {
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
		rep.Estimated += l.Estimated
		rep.Rework += l.Redo
		rep.Tokens.add(l.Tokens)
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
		concurrency(rep.Lanes, in.Window, rep.RateSpan, busiestLen(in.Window))
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
	note := func(t time.Time) {
		if !t.IsZero() && (earliest.IsZero() || t.Before(earliest)) {
			earliest = t
		}
	}
	for _, c := range in.Cards {
		for _, s := range c.Run.Sessions {
			note(s.Started)
		}
		for _, ch := range c.Run.Money.Charges {
			note(ch.At)
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
func buildLane(c Card, w Window, now time.Time, busy map[domain.FeatureID]bool) (Lane, bool) {
	l := Lane{
		ID:      c.Feature.ID,
		Title:   c.Feature.Title,
		Kind:    c.Feature.Kind,
		Ending:  c.Feature.Ending(c.Landed),
		Settled: c.Feature.Stage == domain.StageDone,
	}

	// Blocks: every pass, clipped to the window; an open pass runs to
	// now and then to the right edge.
	asks := cardrun.AskSpans(c.Events)
	open := false
	for _, s := range c.Run.Sessions {
		if s.Started.IsZero() {
			continue
		}
		from, to, isOpen := s.Started, s.Ended, !s.Closed
		if isOpen {
			to = now
		}
		if from.Before(w.From) {
			from = w.From
		}
		if to.After(w.To) {
			to = w.To
		}
		if to.After(from) {
			l.Blocks = append(l.Blocks, Block{From: from, To: to, Stage: s.Stage, Open: isOpen})
			l.working = append(l.working, cardrun.WorkingSpans(from, to, asks)...)
			l.Last = later(l.Last, to)
		}
		if isOpen {
			open = true
		}
		// The window charges a pass to the window it started in (the
		// package doc owns the rule), and the lane keeps its own share.
		if w.Contains(s.Started) {
			l.Credits += s.Credits
			l.Estimated += s.Estimated
			l.Tokens.add(Tokens{Input: s.InputTokens, Cached: s.CachedTokens, Output: s.OutputTokens})
			if s.Redo {
				l.Redo += s.Credits
			}
		}
	}
	// A freeform card has no passes above — c.Run.Sessions is always
	// empty for one — so its blocks come straight off its own spend
	// rows instead. Its credits are already counted below, in the
	// Charges loop; this only draws the span.
	for _, b := range freeformBlocks(c.Spend, w, now, busy != nil && busy[c.Feature.ID]) {
		l.Blocks = append(l.Blocks, b)
		l.Last = later(l.Last, b.To)
	}
	// ...and the spend no pass holds to the window its one moment fell in
	// (cardrun.Charge), so the lane adds up to the card.
	for _, ch := range c.Run.Money.Charges {
		if !w.Contains(ch.At) {
			continue
		}
		l.Credits += ch.Credits
		l.Estimated += ch.Estimated
		l.Tokens.add(Tokens{Input: ch.InputTokens, Cached: ch.CachedTokens, Output: ch.OutputTokens})
		l.Last = later(l.Last, ch.At)
	}

	// Waits: the same spans the card's own clock sums, clipped to the
	// window. A decision nobody has answered still runs to the right
	// edge — that is the wait the reader is standing in, and the lane
	// names it by its start rather than hiding it in a filled column.
	l.Waits = cardrun.WaitSpans(c.Events, w.From, w.To)
	for _, sp := range l.Waits {
		l.Last = later(l.Last, sp.To)
	}
	waiting := false
	for _, sp := range cardrun.DecisionSpans(c.Events) {
		if !sp.To.IsZero() {
			continue
		}
		waiting = true
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

	// Running is the board's word where there is a board. Without one,
	// it is an open pass on a card nobody is being asked about — the
	// board's own precedence, where needing you outranks being busy.
	if busy != nil {
		l.Running = busy[c.Feature.ID]
	} else {
		l.Running = open && !waiting
	}

	l.Note = laneNote(c, w)
	if len(l.Blocks) == 0 && len(l.Waits) == 0 && len(l.Gates) == 0 && l.LandedAt.IsZero() &&
		l.Credits == 0 && !l.Running {
		return Lane{}, false
	}
	return l, true
}

// freeformBlocks reads a freeform card's blocks straight out of its own
// spend rows, clipped to the window — the one span a freeform card's
// record still carries, since it has no stage_enter/stage_exit to carry
// one more legibly (DESIGN §19.3a).
//
// Each row's Session is the generation stamp engine.Session.generation
// writes for every rollup row, stage pass or not: the backend's own
// start, as Unix nanoseconds. Grouped by it, a freeform card's rows come
// back exactly as its backends did — one group per spawn, from the
// stamp its session key already is to its last metered sample — without
// guessing at a boundary the record never drew.
//
// running is the board's own word (fleetrun.Input.Busy, the same one
// buildLane reads everywhere else): only the most recently spawned
// backend can still be running, and only then does its block reach to
// now rather than stopping at its last sample.
func freeformBlocks(spend []state.StageSpend, w Window, now time.Time, running bool) []Block {
	type span struct{ from, to time.Time }
	groups := map[string]*span{}
	var keys []string
	for _, r := range spend {
		if r.Stage != domain.StageOpen || r.Session == "" {
			continue
		}
		ns, err := strconv.ParseInt(r.Session, 10, 64)
		if err != nil {
			continue
		}
		start := time.Unix(0, ns).UTC()
		g, ok := groups[r.Session]
		if !ok {
			g = &span{from: start, to: start}
			groups[r.Session] = g
			keys = append(keys, r.Session)
		}
		if r.UpdatedAt.After(g.to) {
			g.to = r.UpdatedAt
		}
	}
	sort.Slice(keys, func(i, j int) bool { return groups[keys[i]].from.Before(groups[keys[j]].from) })

	var out []Block
	for i, k := range keys {
		g := groups[k]
		from, to := g.from, g.to
		isOpen := running && i == len(keys)-1
		if isOpen {
			to = now
		}
		if from.Before(w.From) {
			from = w.From
		}
		if to.After(w.To) {
			to = w.To
		}
		if to.Before(from) {
			continue
		}
		out = append(out, Block{From: from, To: to, Stage: domain.StageOpen, Open: isOpen})
	}
	return out
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
	// an open session blocked on its own question is not working: that
	// stretch is the reader's, and the wait below charges it to them
	asks := cardrun.AskSpans(c.Events)
	for _, a := range agent {
		from := later(a.from, w.From)
		to := a.to
		if to.IsZero() || to.After(w.To) {
			to = w.To
		}
		cl.Agent += cardrun.WorkingTime(from, to, asks)
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
		for _, ch := range c.Run.Money.Charges {
			if !in.Window.Contains(ch.At) {
				continue
			}
			bs[ch.Bucket()] += ch.Credits
			m := ch.Model
			if m == "" {
				m = "unknown"
			}
			bm[m] += ch.Credits
		}
	}
	return cardrun.Buckets(bs), cardrun.Buckets(bm)
}

// concurrency reads the two figures nothing sums to: how many lanes ran
// at once at the busiest moment, and which stretch of the window took
// the most agent time. The sweep counts a session as running over its
// half-open interval, so a lane ending exactly as another starts is one
// lane, not two.
//
// The stretches are read inside the window. An all-history window (no
// From) starts where the history does — history before its end, the
// span the burn rate is read over — and its stretches fall on the
// bucket's own grid (whole days, whole hours), the first of them clipped
// to where the history starts: a busiest day is never reported as
// beginning before anything ran.
func concurrency(lanes []Lane, w Window, history, bucket time.Duration) (peak int, busiest time.Time, busiestLen, busiestAgent time.Duration) {
	type ev struct {
		at time.Time
		up bool
	}
	var evs []ev
	for _, l := range lanes {
		for _, b := range l.working {
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
	start, grid := w.From, w.From
	if start.IsZero() {
		if history <= 0 {
			return peak, busiest, busiestLen, busiestAgent
		}
		start = w.To.Add(-history)
		grid = start.Truncate(bucket)
	}
	for at := grid; at.Before(w.To); at = at.Add(bucket) {
		from, to := later(at, start), at.Add(bucket)
		if to.After(w.To) {
			to = w.To
		}
		var agent time.Duration
		for _, l := range lanes {
			for _, b := range l.working {
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
		// Tokens come off the rollup rows rather than the feature's own
		// counters, which carry no cached figure: one source for all
		// three numbers beats a total and a cache share that were
		// measured differently. The rows are written beside every
		// AddSpend, so the two agree on what they both hold.
		var rows float64
		for _, s := range r.StageSpend {
			bs[string(s.Stage)] += s.Credits
			bm[s.Model] += s.Credits
			rows += s.Credits
			a.Tokens.add(Tokens{Input: s.InputTokens, Cached: s.CachedTokens, Output: s.OutputTokens})
		}
		// What the counter holds and no row does is named, not dropped,
		// by the same derivation the card's own report uses — so the
		// buckets add up to the total they sit under.
		for _, ch := range cardrun.Unrecorded(r.Feature, rows) {
			bs[ch.Bucket()] += ch.Credits
			bm[ch.Model] += ch.Credits
		}
	}
	a.ByStage, a.ByModel = cardrun.Buckets(bs), cardrun.Buckets(bm)
	return a
}
