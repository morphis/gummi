package cardrun

import (
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// Run is everything one card's record can say about how it ran.
//
// Every field is a number a surface can print without further work, and
// every number is either derived from the event log or read off the spend
// rollup — nothing here is a fresh measurement, because Report takes no
// clock and does no I/O.
type Run struct {
	ID    domain.FeatureID
	Title string
	Kind  domain.Kind
	Stage domain.Stage

	// Sessions is every run of every stage, oldest first: the card's
	// history at the grain a person actually thinks in.
	Sessions []Session

	Money    Money
	Clock    Clock
	Hands    Hands
	Judgment Judgment
	Envelope Envelope
}

// Session is one run of one stage by one role — a plan written, a plan
// critiqued, an implementation, the implementation again.
type Session struct {
	Stage domain.Stage
	Role  string
	// Flavor distinguishes the passes a stage can host: the stage's own
	// work, the critique that judges it, or a rebase resolve. It is part
	// of what makes two sessions the same piece of work done twice.
	Flavor string
	Model  string
	// Key is the session's generation — the value stage_spend files its
	// realized spend under. Empty on a session whose spend predates the
	// session key.
	Key string

	Started time.Time
	// Ended is zero while the session is still open, which is what a live
	// card's last session always is.
	Ended  time.Time
	Closed bool
	// EndInferred marks a pass whose end the log never recorded, closed at
	// the moment the next pass began because a card runs one stage session
	// at a time. Its duration is therefore an upper bound, and it has no
	// verdict or context peak to report — not because it had none, but
	// because nothing wrote them down.
	EndInferred bool

	// Turns is the conversation this pass had: message events between its
	// boundaries. A stage that only wrote and critiqued a document has
	// none, and says so by reporting zero rather than pretending to one.
	Turns int
	// Tools is every call this pass made, and ToolFails the subset that
	// reported a failure. A pass whose backend reports no outcomes has
	// calls and no failures, which is not the same as having no failures.
	Tools     int
	ToolFails int

	// Credits is what this pass actually cost, from the session-keyed
	// rollup. Estimated is the token-derived subset a later settle may
	// still correct.
	Credits   float64
	Estimated float64

	InputTokens  int64
	CachedTokens int64
	OutputTokens int64

	// Verdict is the pass's submitted verdict, for the roles that submit
	// one. Empty is not a pass: it is a session that never said.
	Verdict string

	// ContextPeak and ContextLimit are how close this pass came to its
	// window. Zero means the backend never reported occupancy.
	ContextPeak  int64
	ContextLimit int64

	// Redo marks a pass the card had already done: a second or later
	// session of the same (stage, role, flavour). RedoReason says which
	// kind — Corrected for work sent back by a verdict, Reproved for work
	// repeated because the base moved under it.
	Redo       bool
	RedoReason string

	// Reconstructed marks Credits as read off this pass's stage_exit
	// rather than measured by the session-keyed rollup — the only figure
	// available for a card that ran before the rollup carried session
	// keys. It is a weaker number in one specific way: a pass that never
	// exited left no payload, so its cost is missing rather than wrong.
	// Displays say so; nothing silently presents it as measured.
	Reconstructed bool

	// exitCredits is the stage_exit payload's own figure, held for the
	// fallback above and never surfaced directly.
	exitCredits float64
}

// The two reasons a card does a piece of work twice. They are told apart
// because they mean different things about a run: one says the work was
// wrong, the other says only that the ground moved.
const (
	// Corrected: a verdict sent the work back.
	Corrected = "corrected"
	// Reproved: a rebase happened in this stage first, so the pass is the
	// same work proved again over a new base rather than a fix.
	Reproved = "reproved"
)

// Duration is how long the session ran; zero while it is still open.
func (s Session) Duration() time.Duration {
	if !s.Closed || s.Started.IsZero() {
		return 0
	}
	return s.Ended.Sub(s.Started)
}

// Money is where the credits went.
type Money struct {
	// Credits is the card's realized total, and Estimated the portion of
	// it a provider has not yet settled — the part of the number that may
	// still be corrected, and which a display therefore has to mark.
	Credits   float64
	Estimated float64

	// FirstPass and Rework split the total by whether the card had done
	// that work before. Rework is the figure the per-stage rollup cannot
	// produce, because it sums the passes it would have to tell apart.
	FirstPass float64
	Rework    float64
	// Corrected and Reproved split Rework by why it happened.
	Corrected float64
	Reproved  float64

	ByStage []Bucket
	ByRole  []Bucket
	ByModel []Bucket

	InputTokens  int64
	CachedTokens int64
	OutputTokens int64
}

// ReworkShare is the fraction of realized spend that went on work the
// card had already done. Zero when nothing was spent.
func (m Money) ReworkShare() float64 {
	if m.Credits <= 0 {
		return 0
	}
	return m.Rework / m.Credits
}

// CacheReadRatio is the share of the input side served from the prompt
// cache. Zero when the backend reports no cache reads, which is not the
// same as a cache that never hit — some adapters simply do not say.
func (m Money) CacheReadRatio() float64 {
	total := m.InputTokens + m.CachedTokens
	if total <= 0 {
		return 0
	}
	return float64(m.CachedTokens) / float64(total)
}

// Bucket is one named share of a total, largest first in the slices that
// hold them.
type Bucket struct {
	Name    string
	Credits float64
}

// Clock is where the hours went.
type Clock struct {
	// Agent is the time a session was actually running, summed over the
	// card's closed sessions.
	Agent time.Duration
	// Elapsed is first session start to last session end — the card's own
	// life, not counting whatever happened after the work stopped.
	Elapsed time.Duration
	// Waiting is Elapsed less Agent: the time the card existed with
	// nothing running, which on most cards is the time it spent waiting
	// for a person. It is never negative.
	Waiting time.Duration

	// ToFirstGate is how long until the card first crossed a design gate,
	// and ToVerified until its verify stage first passed. Zero means it
	// has not happened.
	ToFirstGate time.Duration
	ToVerified  time.Duration
}

// WaitingShare is the fraction of the card's life that nothing was
// running. Zero when the card has no measured elapsed time.
func (c Clock) WaitingShare() float64 {
	if c.Elapsed <= 0 {
		return 0
	}
	return float64(c.Waiting) / float64(c.Elapsed)
}

// Hands is what the card did, as against what it decided.
type Hands struct {
	Turns int

	// Tools is every recorded agent tool call, by name and descending
	// count. It is nil — not empty — when the card's backend never
	// recorded any, so a reader can tell "this backend reports no tool
	// calls" from "this card made none". They are different facts and
	// only one of them is about the card.
	Tools []ToolUse
	// ToolCalls and ToolFails are those counts totalled.
	ToolCalls int
	ToolFails int

	// Skills and Subagents are the two kinds of delegation worth naming
	// on their own: what the card handed to a skill, and what it handed
	// to a subagent.
	Skills    []ToolUse
	Subagents []ToolUse

	// Checks is gummi's own verification runs — a different thing from an
	// agent's tools, since gummi ran them and knows how they went.
	Checks []CheckRun
}

// ToolUse is one tool the card called, and how that went.
type ToolUse struct {
	Name  string
	Calls int
	Fails int
	// Detail is the argument of the most recent call, kept because on a
	// skill or a subagent it is the only thing that says what was
	// delegated. Empty once the retention sweep has been past, or when
	// the call carried nothing displayable.
	Detail string
	// Total is the summed duration of the calls that reported one.
	Total time.Duration
}

// CheckRun is one named gummi check and its tally across the card.
type CheckRun struct {
	Name    string
	Runs    int
	Fails   int
	Excused bool // written off as pre-existing by the card's baseline
}

// Judgment is what the card decided, and who decided it.
type Judgment struct {
	// Gates and Asks count the human checkpoints the card reached, split
	// by who answered them. ByMachine is every crossing made without a
	// person present — not one actor, since the set of automatic actors
	// is open-ended and enumerating it is how a reader starts silently
	// miscounting.
	Gates Answered
	Asks  Answered

	// Verdicts counts each submitted verdict by its value.
	Verdicts map[string]int
	// Rounds is each round kind's persisted counter. Plan and review are
	// live budgets cleared at their gate; corrective is cumulative and is
	// the only one that answers "how much was redone".
	Rounds map[domain.RoundKind]int

	Parks []Park
}

// Answered is a count of decisions split by who took them.
type Answered struct {
	Total     int
	ByYou     int
	ByMachine int
}

// Park is one time the card stopped and waited for someone.
type Park struct {
	Reason string
	Detail string
	At     time.Time
}

// Envelope is what the card was granted against what it used.
type Envelope struct {
	Granted int
	Spent   float64
}

// Utilization is the share of the granted envelope the card actually
// spent. Zero when nothing was granted.
func (e Envelope) Utilization() float64 {
	if e.Granted <= 0 {
		return 0
	}
	return e.Spent / float64(e.Granted)
}
