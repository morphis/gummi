package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/cardrun"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// `gummi status <id> --run`: where a card's credits and hours went.
//
// It is opt-in rather than always present because it reads the card's
// whole event log, and status is a thing callers poll. The ordinary
// status stays a cheap question about where a card stands; this is the
// expensive question about how it got there.

// statusRun is the run report on the wire — the same figures the board's
// run tab draws, for a caller who has no board.
type statusRun struct {
	Sessions int             `json:"sessions"`
	Passes   []statusPass    `json:"passes,omitempty"`
	Money    statusRunMoney  `json:"money"`
	Clock    statusRunClock  `json:"clock"`
	Hands    statusRunHands  `json:"hands"`
	Judgment statusRunJudged `json:"judgment"`
}

// statusPass is one run of one stage — the grain at which "the redo cost
// more than the first attempt" is a thing a caller can actually read.
type statusPass struct {
	Stage   string  `json:"stage"`
	Role    string  `json:"role"`
	Flavor  string  `json:"flavor,omitempty"`
	Model   string  `json:"model,omitempty"`
	Turns   int     `json:"turns"`
	Tools   int     `json:"tools,omitempty"`
	Seconds float64 `json:"seconds"`
	Credits float64 `json:"credits"`
	Verdict string  `json:"verdict,omitempty"`
	// Redo and RedoReason say whether the card had done this work before,
	// and why it was doing it again.
	Redo       bool   `json:"redo,omitempty"`
	RedoReason string `json:"redo_reason,omitempty"`
	// Open marks the pass that is still running, which on a live card is
	// the last one and the one a caller is usually asking about.
	Open bool `json:"open,omitempty"`
	// EndInferred marks a pass whose end the log never recorded, closed at
	// the moment the next one began. Its seconds are an upper bound and it
	// has no verdict to report.
	EndInferred bool `json:"end_inferred,omitempty"`
	// ContextPeak/ContextLimit is how close the pass came to its window,
	// absent when the backend never reported occupancy.
	ContextPeak  int64 `json:"context_peak,omitempty"`
	ContextLimit int64 `json:"context_limit,omitempty"`
	// Reconstructed marks Credits as read off this pass's stage_exit
	// rather than measured per usage sample — the only figure a card that
	// ran before the session key exists has. A consumer that cares about
	// precision should treat a reconstructed figure as a lower bound: a
	// pass that never exited left no payload to read.
	Reconstructed bool `json:"reconstructed,omitempty"`
}

type statusRunMoney struct {
	Credits   float64 `json:"credits"`
	Metered   float64 `json:"metered"`
	Estimated float64 `json:"estimated"`
	Envelope  int     `json:"envelope"`
	// Utilization is spend ÷ envelope. An envelope six times larger than
	// anything the card could spend is not an error, but it is a fact
	// about how the estimate was made, and nothing else reports it.
	Utilization float64 `json:"utilization"`

	FirstPass float64 `json:"first_pass"`
	Rework    float64 `json:"rework"`
	// ReworkShare is the headline: the fraction of this card's spend that
	// went on work it had already done. It is the one figure the
	// stage-grained rollup cannot produce.
	ReworkShare float64 `json:"rework_share"`
	Corrected   float64 `json:"corrected"`
	Reproved    float64 `json:"reproved"`

	ByStage []statusBucket  `json:"by_stage,omitempty"`
	ByRole  []statusBucket  `json:"by_role,omitempty"`
	ByModel []statusBucket  `json:"by_model,omitempty"`
	Tokens  statusRunTokens `json:"tokens"`
}

type statusBucket struct {
	Name    string  `json:"name"`
	Credits float64 `json:"credits"`
}

type statusRunTokens struct {
	Input  int64 `json:"input"`
	Cached int64 `json:"cached"`
	Output int64 `json:"output"`
	// CacheReadRatio is the share of the input side served from cache.
	// Zero on a backend that does not report cache reads, which is not the
	// same as a cache that never hit.
	CacheReadRatio float64 `json:"cache_read_ratio"`
}

type statusRunClock struct {
	AgentSeconds   float64 `json:"agent_seconds"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	// WaitingSeconds is elapsed less agent time: the part of the card's
	// life when nothing was running, which is usually the part it spent
	// waiting for a person.
	WaitingSeconds     float64 `json:"waiting_seconds"`
	WaitingShare       float64 `json:"waiting_share"`
	ToFirstGateSeconds float64 `json:"to_first_gate_seconds,omitempty"`
	ToVerifiedSeconds  float64 `json:"to_verified_seconds,omitempty"`
}

type statusRunHands struct {
	Turns int `json:"turns"`
	// Tools is null — not an empty object — when this card's backend
	// recorded no tool calls at all. A caller must be able to tell "this
	// backend reports no tool calls" from "this card made none": they are
	// different facts and only one of them is about the card.
	Tools     map[string]statusToolUse `json:"tools"`
	ToolCalls int                      `json:"tool_calls"`
	ToolFails int                      `json:"tool_fails"`
	Skills    map[string]statusToolUse `json:"skills,omitempty"`
	Subagents map[string]statusToolUse `json:"subagents,omitempty"`
	Checks    map[string]statusCheck   `json:"checks,omitempty"`
}

type statusToolUse struct {
	Calls   int     `json:"calls"`
	Fails   int     `json:"fails,omitempty"`
	Detail  string  `json:"detail,omitempty"`
	Seconds float64 `json:"seconds,omitempty"`
}

type statusCheck struct {
	Runs  int `json:"runs"`
	Fails int `json:"fails,omitempty"`
	// Excused marks a check the card's baseline already had failing, so
	// verify wrote it off rather than flooring the verdict for it.
	Excused bool `json:"excused,omitempty"`
}

type statusRunJudged struct {
	Gates    statusAnswered `json:"gates"`
	Asks     statusAnswered `json:"asks"`
	Verdicts map[string]int `json:"verdicts,omitempty"`
	Rounds   statusRounds   `json:"rounds"`
	Parks    []statusPark   `json:"parks,omitempty"`
}

type statusAnswered struct {
	Total     int `json:"total"`
	ByYou     int `json:"by_you"`
	ByMachine int `json:"by_machine"`
}

type statusPark struct {
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
	At     string `json:"at"`
}

// buildRun reads the card's record and projects cardrun's report onto the
// wire. Every read degrades to its zero value, the same way the rest of
// the status view does: a caller asking how a card ran must still get the
// part of the answer that is readable.
func buildRun(ctx context.Context, store *state.Store, f *domain.Feature) *statusRun {
	evs, err := store.Events(ctx, f.ID)
	if err != nil {
		return nil
	}
	spend, err := store.SessionBreakdown(ctx, f.ID)
	if err != nil {
		spend = nil
	}
	rnds := map[domain.RoundKind]int{}
	for _, k := range []domain.RoundKind{domain.RoundKindPlan, domain.RoundKindReview, domain.RoundKindCorrective} {
		if n, err := store.Rounds(ctx, f.ID, k); err == nil {
			rnds[k] = n
		}
	}
	baseline, err := store.CheckBaseline(ctx, f.ID)
	if err != nil {
		baseline = nil
	}
	r := cardrun.Report(cardrun.Input{
		Feature: *f, Events: evs, Spend: spend, Rounds: rnds, Baseline: baseline,
	})
	return runPayload(r)
}

// runPayload is the projection itself, kept apart from the reads so it
// can be exercised without a store.
func runPayload(r cardrun.Run) *statusRun {
	out := &statusRun{
		Sessions: len(r.Sessions),
		Money: statusRunMoney{
			Credits:     round2(r.Money.Credits),
			Metered:     round2(r.Money.Credits - r.Money.Estimated),
			Estimated:   round2(r.Money.Estimated),
			Envelope:    r.Envelope.Granted,
			Utilization: round4(r.Envelope.Utilization()),
			FirstPass:   round2(r.Money.FirstPass),
			Rework:      round2(r.Money.Rework),
			ReworkShare: round4(r.Money.ReworkShare()),
			Corrected:   round2(r.Money.Corrected),
			Reproved:    round2(r.Money.Reproved),
			ByStage:     bucketPayload(r.Money.ByStage),
			ByRole:      bucketPayload(r.Money.ByRole),
			ByModel:     bucketPayload(r.Money.ByModel),
			Tokens: statusRunTokens{
				Input: r.Money.InputTokens, Cached: r.Money.CachedTokens,
				Output: r.Money.OutputTokens, CacheReadRatio: round4(r.Money.CacheReadRatio()),
			},
		},
		Clock: statusRunClock{
			AgentSeconds:       r.Clock.Agent.Seconds(),
			ElapsedSeconds:     r.Clock.Elapsed.Seconds(),
			WaitingSeconds:     r.Clock.Waiting.Seconds(),
			WaitingShare:       round4(r.Clock.WaitingShare()),
			ToFirstGateSeconds: r.Clock.ToFirstGate.Seconds(),
			ToVerifiedSeconds:  r.Clock.ToVerified.Seconds(),
		},
		Hands: statusRunHands{
			Turns:     r.Hands.Turns,
			Tools:     toolPayload(r.Hands.Tools),
			ToolCalls: r.Hands.ToolCalls,
			ToolFails: r.Hands.ToolFails,
			Skills:    toolPayload(r.Hands.Skills),
			Subagents: toolPayload(r.Hands.Subagents),
		},
		Judgment: statusRunJudged{
			Gates: statusAnswered{r.Judgment.Gates.Total, r.Judgment.Gates.ByYou, r.Judgment.Gates.ByMachine},
			Asks:  statusAnswered{r.Judgment.Asks.Total, r.Judgment.Asks.ByYou, r.Judgment.Asks.ByMachine},
			Rounds: statusRounds{
				Plan:       r.Judgment.Rounds[domain.RoundKindPlan],
				Review:     r.Judgment.Rounds[domain.RoundKindReview],
				Corrective: r.Judgment.Rounds[domain.RoundKindCorrective],
			},
		},
	}
	if len(r.Judgment.Verdicts) > 0 {
		out.Judgment.Verdicts = r.Judgment.Verdicts
	}
	for _, p := range r.Judgment.Parks {
		out.Judgment.Parks = append(out.Judgment.Parks, statusPark{
			Reason: p.Reason, Detail: p.Detail, At: p.At.UTC().Format(time.RFC3339),
		})
	}
	if len(r.Hands.Checks) > 0 {
		out.Hands.Checks = map[string]statusCheck{}
		for _, c := range r.Hands.Checks {
			out.Hands.Checks[c.Name] = statusCheck{Runs: c.Runs, Fails: c.Fails, Excused: c.Excused}
		}
	}
	for _, s := range r.Sessions {
		out.Passes = append(out.Passes, statusPass{
			Stage: string(s.Stage), Role: s.Role, Flavor: s.Flavor, Model: s.Model,
			Turns: s.Turns, Tools: s.Tools,
			Seconds: s.Duration().Seconds(), Credits: round2(s.Credits),
			Verdict: s.Verdict, Redo: s.Redo, RedoReason: s.RedoReason,
			Open:          !s.Closed,
			EndInferred:   s.EndInferred,
			ContextPeak:   s.ContextPeak,
			ContextLimit:  s.ContextLimit,
			Reconstructed: s.Reconstructed,
		})
	}
	return out
}

func bucketPayload(bs []cardrun.Bucket) []statusBucket {
	var out []statusBucket
	for _, b := range bs {
		out = append(out, statusBucket{Name: b.Name, Credits: round2(b.Credits)})
	}
	return out
}

// toolPayload keeps nil as nil. An empty map would marshal to {} and read
// as "this card called nothing", which is a claim this record cannot
// make on a backend that reports no tool calls at all.
func toolPayload(ts []cardrun.ToolUse) map[string]statusToolUse {
	if len(ts) == 0 {
		return nil
	}
	out := map[string]statusToolUse{}
	for _, t := range ts {
		out[t.Name] = statusToolUse{
			Calls: t.Calls, Fails: t.Fails, Detail: t.Detail, Seconds: t.Total.Seconds(),
		}
	}
	return out
}

func round2(v float64) float64 { return float64(int64(v*100+sign(v)*0.5)) / 100 }
func round4(v float64) float64 { return float64(int64(v*10000+sign(v)*0.5)) / 10000 }

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// renderRun writes the run report as the text summary — the same shape
// the board's run tab draws, in a terminal that has no board.
func renderRun(w io.Writer, view statusView, r *statusRun) {
	if r == nil {
		return
	}
	fmt.Fprintf(w, "%s  %s\n", view.ID, view.Title)
	ending := string(view.Ending)
	if ending == "" {
		ending = view.Stage
	}
	fmt.Fprintf(w, "%s · %d session%s\n\n", ending, r.Sessions, plural(r.Sessions))

	fmt.Fprintln(w, "where it went")
	for _, b := range r.Money.ByStage {
		fmt.Fprintf(w, "  %-12s %s %8.2f  %3.0f%%\n",
			b.Name, bar(b.Credits, r.Money.Credits, 24),
			b.Credits, pct(b.Credits, r.Money.Credits))
	}
	fmt.Fprintf(w, "  %-12s %s %8.2f  credits\n", "", strings.Repeat(" ", 24), r.Money.Credits)
	if r.Money.Estimated > 0 {
		fmt.Fprintf(w, "  ~%.2f of it estimated — not yet settled by the provider\n", r.Money.Estimated)
	}
	fmt.Fprintln(w)

	// The redo gets its own block rather than a column in a table of
	// eleven passes: a run report that makes you count rows to find the
	// expensive mistake has buried its own headline. It is absent
	// entirely on a card that never did anything twice.
	if redone := redoPasses(r.Passes); len(redone) > 0 {
		fmt.Fprintln(w, "the redo")
		for _, p := range redone {
			fmt.Fprintf(w, "  %s · %s · %s · %d turns · %s · %.2f%s\n",
				p.Stage, p.Role, p.RedoReason, p.Turns, dur(p.Seconds), p.Credits, recon(p))
		}
		fmt.Fprintf(w, "  %.2f of %.2f credits was work already done (%.0f%%)\n\n",
			r.Money.Rework, r.Money.Credits, r.Money.ReworkShare*100)
	}

	fmt.Fprintln(w, "the clock")
	fmt.Fprintf(w, "  agent working   %10s\n", dur(r.Clock.AgentSeconds))
	fmt.Fprintf(w, "  waiting on you  %10s  (%.0f%%)\n", dur(r.Clock.WaitingSeconds), r.Clock.WaitingShare*100)
	fmt.Fprintf(w, "  elapsed         %10s\n\n", dur(r.Clock.ElapsedSeconds))

	fmt.Fprintln(w, "its hands")
	fmt.Fprintf(w, "  turns           %d\n", r.Hands.Turns)
	if r.Hands.Tools == nil {
		fmt.Fprintln(w, "  tools           none recorded — this backend reports no tool outcomes")
	} else {
		fmt.Fprintf(w, "  tools           %d call%s, %d failed\n",
			r.Hands.ToolCalls, plural(r.Hands.ToolCalls), r.Hands.ToolFails)
	}
	for name, c := range r.Hands.Checks {
		excused := ""
		if c.Excused {
			excused = " (pre-existing, excused)"
		}
		fmt.Fprintf(w, "  check %-9s %d run%s, %d failed%s\n", name, c.Runs, plural(c.Runs), c.Fails, excused)
	}
	fmt.Fprintf(w, "  gates           %d — %d you, %d machine\n",
		r.Judgment.Gates.Total, r.Judgment.Gates.ByYou, r.Judgment.Gates.ByMachine)
	fmt.Fprintf(w, "  asks            %d — %d you, %d autopilot\n\n",
		r.Judgment.Asks.Total, r.Judgment.Asks.ByYou, r.Judgment.Asks.ByMachine)

	if r.Money.Envelope > 0 {
		fmt.Fprintf(w, "the envelope\n  granted %d · spent %.0f · %.0f%% used\n",
			r.Money.Envelope, r.Money.Credits, r.Money.Utilization*100)
	}
}

// redoPasses is every pass the card had already done once.
func redoPasses(ps []statusPass) []statusPass {
	var out []statusPass
	for _, p := range ps {
		if p.Redo {
			out = append(out, p)
		}
	}
	return out
}

// recon marks a figure read off a pass's stage_exit rather than measured
// by the session-keyed rollup. It appears only on cards that ran before
// the rollup carried session keys, and it is printed rather than hidden
// because a reconstruction that looks like a measurement is the one thing
// a ledger must never do.
func recon(p statusPass) string {
	if p.Reconstructed {
		return " ~"
	}
	return ""
}

// bar draws a proportional magnitude bar; one hue, since these are shares
// of one total rather than different things.
func bar(v, total float64, width int) string {
	if total <= 0 || v <= 0 {
		return strings.Repeat(" ", width)
	}
	n := int(v/total*float64(width) + 0.5)
	if n < 1 {
		n = 1
	}
	if n > width {
		n = width
	}
	return strings.Repeat("█", n) + strings.Repeat(" ", width-n)
}

func pct(v, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return v / total * 100
}

// dur renders a span the way a person reads one: under a second it says
// so rather than rounding to "0s" (a zero beside a nonzero share reads as
// a broken number), then seconds, minutes, and hours and minutes.
func dur(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	switch {
	case d <= 0:
		return "—"
	case d < time.Second:
		return "<1s"
	case d < time.Minute:
		return fmt.Sprintf("%.0fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%.1fm", d.Minutes())
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
