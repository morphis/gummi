// Package fleetrun answers one question about a workspace: how is it
// running, and how did it get here.
//
// cardrun answers the same question at the scale of one card, from the
// card's own record. This package is that at the scale of the whole
// board: every card's record folded into one report — where the credits
// went over a window and over all time, where the hours went, how many
// lanes ran at once, and the timeline itself, per-card spans a surface
// can draw rather than sums it has to take on faith.
//
// It renders nothing, reads nothing, and touches no clock: Fold is a
// pure function of its arguments, so the stats tab and any future
// reader of the same question cannot drift apart on what the workspace
// spent. That is the same seam cardrun, verdict, rounds and gatepolicy
// already are, one scale up.
//
// # What the window charges, and to what
//
// A pass is charged to the window it started in. The rule is plain and
// therefore auditable: a long pass that spans a boundary belongs
// entirely to the window it began in, no window double-counts, and no
// number is apportioned — a fraction of a pass's cost is not a number
// the record contains. Spend on turns that are not passes (a goal's
// lead, a one-shot scribe) cannot be windowed at all — the rollup
// carries no per-sample time — so the window's money is the passes'
// money, and says so by never claiming to be the total.
//
// Tokens follow the credits they were spent with, at both scales: the
// window's token count covers exactly the passes its credit figure
// covers, and the all-time count comes off the same rollup rows the
// all-time credits are read from. They are reported beside credits and
// never converted into them — a report that quietly priced tokens would
// be inventing the one figure only a provider can state — and the cache
// share is named because a window served from the prompt cache spent
// tokens the bill never saw.
//
// The window clock is the card-run clock re-derived over the window
// rather than summed from the per-card clocks, for one reason: the
// per-card clock stops a card's life at its last closed session, which
// is the right reading for a report read after the fact and the wrong
// one for a window whose right edge is now — a card mid-session would
// contribute nothing to "agent working" while three lanes run. The
// window clock therefore counts an open session to the right edge, and
// extends a card's life to now when anything on it is still live. The
// waiting-on-you spans are the same derivation the card's own clock
// sums (cardrun.DecisionSpans), clamped to the window instead of to the
// card's life — one correlation rule, two horizons.
package fleetrun
