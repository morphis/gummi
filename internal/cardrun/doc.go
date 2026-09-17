// Package cardrun answers one question about a card: how did it run.
//
// gummi already answers the other two. The inbox says what needs a human;
// the thread says what happened; the week view says what a run produced
// and whether it was worth it. None of them says where the money and the
// hours went — which pass burned the credits, how long the card sat
// waiting on a person, what it did with its hands. The nearest thing is
// the thread's folded receipt, one line per finished stage session, which
// is the right idea at the wrong scale: per session, never totalled and
// never compared.
//
// This package is that at the scale of a whole card. It takes the
// durable record — the event log, the realized-spend rollup, the round
// counters, the check baseline — and returns one Run value holding every
// figure a surface might want. It renders nothing, reads nothing, and
// touches no clock: Report is a pure function of its arguments, so the
// TUI's run tab, `gummi status --json` and the week view all read the
// same numbers and cannot drift apart on what a card cost. That is the
// same seam internal/verdict, internal/rounds and internal/gatepolicy
// already are, for the same reason.
//
// # What is derived and what is stored
//
// Almost everything here is derived. Turn counts, tool counts, session
// durations, who crossed which gate — all of it is read back out of the
// event log rather than written down beside it, because a stored copy of
// a derivable fact is a second source of truth free to drift from the
// first (DESIGN §6.3, the rule that makes a folded receipt read credits
// from stage_spend rather than from its own payload).
//
// Two facts are exceptions, and both are exceptions for the same reason:
// nothing can recover them afterwards. A session's realized spend is
// keyed by session in stage_spend because the rollup is written per usage
// sample, so it is complete even for a pass that never exited. And a
// session's peak context occupancy is stamped on its stage_exit, because
// the row holding the live figure is deleted the moment the stage ends.
//
// # What a pass is
//
// A session is one run of one stage by one role in one flavour — a plan
// written, a plan critiqued, an implementation, the critique that sent it
// back, the implementation again. Two sessions of the same (stage, role,
// flavour) mean the card did that work twice, and the second one is
// rework: the single most useful fact about a run, and the one the
// per-stage rollup cannot express, since it sums exactly the dimension
// that tells them apart.
package cardrun
