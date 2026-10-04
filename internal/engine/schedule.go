package engine

// Schedules and heartbeats (DESIGN §19.9): the engine's half. The board's
// update loop owns the clock — its poll calls ScheduleTick with a now —
// and the engine owns every side effect a fire has: claim the instant,
// deliver a heartbeat turn into a live session, or mint a freeform card
// and kick it off. Nothing here runs on its own goroutine and nothing
// queues: a fire against a busy target is skipped, not deferred, because
// a turn delivered later would land in a conversation that has since
// moved on.
//
// Every fire's result is recorded on the row by the store's
// RecordScheduleOutcome, whatever happened — including the failures,
// which keep the schedule enabled and let the next cadence retry. The
// faces read the recorded outcome as much as the returned fire, so a
// board that comes up late still sees what the last fire did.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/morphis/gummi/internal/cardmint"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/schedule"
	"github.com/morphis/gummi/internal/state"
)

// ScheduleFire is one schedule's fire, as the board's poll and a forced
// run-now see it. Outcome carries the status and detail the row now
// records; Card and Orphan name the card a mint fire left behind.
type ScheduleFire struct {
	ID      domain.ScheduleID
	Name    string
	Kind    domain.ScheduleKind
	Forced  bool
	Outcome domain.ScheduleOutcome
}

// ScheduleTick advances every due schedule one fire. It claims each due
// instant before acting on it — a fire that loses the claim does nothing,
// because whoever won it is firing — and reports one entry per fire that
// happened. Safe to call often; a tick with nothing due costs a list
// read.
func (e *Engine) ScheduleTick(ctx context.Context, now time.Time) ([]ScheduleFire, error) {
	if e.cfg.Store == nil {
		return nil, errors.New("no store: schedules are kept there")
	}
	rows, err := e.cfg.Store.ListSchedules(ctx)
	if err != nil {
		return nil, err
	}
	var fires []ScheduleFire
	for _, s := range rows {
		if !schedule.Due(s, now) {
			continue
		}
		fire, fired := e.fireSchedule(ctx, s, now, false)
		if fired {
			fires = append(fires, fire)
		}
	}
	return fires, nil
}

// RunSchedule forces one fire of a schedule now — a person's run-now
// from the TUI or web board, looking at the row. It is the one fire a
// disabled schedule may take, and it leaves the row as it was: an
// enabled row's cadence advances from now, a disabled row keeps no
// cadence at all. Run-now on a disabled row through the CLI is refused
// at the store; this path exists for the person who is looking at the
// row and wants it once, right now.
func (e *Engine) RunSchedule(ctx context.Context, id domain.ScheduleID, now time.Time) (ScheduleFire, error) {
	if e.cfg.Store == nil {
		return ScheduleFire{}, errors.New("no store: schedules are kept there")
	}
	s, err := e.cfg.Store.Schedule(ctx, id)
	if err != nil {
		return ScheduleFire{}, err
	}
	fire, fired := e.fireSchedule(ctx, s, now, true)
	if !fired {
		return fire, fmt.Errorf("schedule %s: another fire of this instant is already running", id)
	}
	return fire, nil
}

// fireSchedule claims the fire and then acts. Claiming happens before
// any side effect — minting a card or sending a turn must never happen
// twice for one instant — and the outcome is recorded whatever the act
// did. The bool reports whether this caller won the fire; a tick that
// lost the claim reports nothing.
func (e *Engine) fireSchedule(ctx context.Context, s domain.Schedule, now time.Time, forced bool) (ScheduleFire, bool) {
	fire := ScheduleFire{ID: s.ID, Name: s.Name, Kind: s.Kind, Forced: forced}
	record := func(o domain.ScheduleOutcome) ScheduleFire {
		o.At = now
		fire.Outcome = o
		// The row's history is written even when this process is not the
		// board: a fire whose outcome could not be recorded still
		// advanced next_run at the claim, so it will not refire.
		_ = e.cfg.Store.RecordScheduleOutcome(ctx, s.ID, o)
		return fire
	}

	// The cadence's next instant, before anything moves: an enabled row
	// always knows when it fires next. A definition the store validated
	// should always advance; if one ever does not, this is a failure to
	// name and a schedule to turn off, not a fire to skip silently.
	newNext := time.Time{}
	expect := s.NextRun
	if s.Enabled {
		newNext = schedule.Advance(s, now)
		if newNext.IsZero() {
			return record(domain.ScheduleOutcome{
				Status:  domain.ScheduleFailed,
				Detail:  "the cadence produced no next run; the schedule is off until it is re-enabled",
				Disable: true,
			}), true
		}
	}
	claimed, err := e.cfg.Store.ClaimScheduleFire(ctx, s.ID, expect, newNext, forced)
	if err != nil {
		if !forced && s.Enabled {
			// A claim refusal on a due row is unexpected (the row was
			// enabled a moment ago); say so and leave the row alone — the
			// next tick retries the whole decision.
			return record(domain.ScheduleOutcome{
				Status: domain.ScheduleFailed,
				Detail: err.Error(),
			}), true
		}
		return fire, false
	}
	if !claimed {
		return fire, false
	}
	switch s.Kind {
	case domain.ScheduleHeartbeat:
		return record(e.fireHeartbeat(ctx, s)), true
	default:
		return record(e.fireMint(ctx, s)), true
	}
}

// fireHeartbeat sends the schedule's prompt as one turn into its target
// session. The target must be a freeform card that is not closed —
// anything else turns the schedule off, because a heartbeat whose
// conversation no longer exists has nothing to reassess and will never
// have it back on its own.
func (e *Engine) fireHeartbeat(ctx context.Context, s domain.Schedule) domain.ScheduleOutcome {
	closed := func(detail string) domain.ScheduleOutcome {
		return domain.ScheduleOutcome{
			Status:  domain.ScheduleDisabledTargetClosed,
			Detail:  detail,
			Disable: true,
		}
	}
	target, err := e.cfg.Store.GetFeature(ctx, s.Target)
	if errors.Is(err, state.ErrNotFound) {
		return closed(fmt.Sprintf("%s is gone — the heartbeat has nothing to send to", s.Target))
	}
	if err != nil {
		return domain.ScheduleOutcome{Status: domain.ScheduleFailed, Detail: err.Error()}
	}
	if !target.IsFreeform() || target.Stage == domain.StageDone {
		return closed(fmt.Sprintf("%s is closed — the heartbeat has nothing to send to", s.Target))
	}
	// The envelope is the brake, and it is the card's own: an exhausted
	// target pauses the schedule (never raises anything) and says so.
	if target.Budget.Envelope > 0 &&
		target.Budget.Remaining(target.Spend.CreditEquivalent()) <= 0 {
		return domain.ScheduleOutcome{
			Status:  domain.SchedulePausedExhausted,
			Detail:  fmt.Sprintf("%s has spent its envelope of %d credits; raise it to carry on", target.ID, target.Budget.Envelope),
			Disable: true,
		}
	}
	// A fire against a target whose session is working right now is
	// skipped, not queued: the turn would land in a conversation that has
	// since moved on, and the next cadence reassesses anyway.
	if ff := e.Freeform(target.ID); ff != nil && ff.Busy() {
		return domain.ScheduleOutcome{
			Status: domain.ScheduleSkippedBusy,
			Detail: fmt.Sprintf("%s is mid-turn; the fire is skipped, not queued", target.ID),
		}
	}
	// The session a conversation was left in comes back with it: an open
	// restores what the engine holds or what the row persisted.
	ff, err := e.OpenFreeform(ctx, target)
	if err != nil {
		return domain.ScheduleOutcome{Status: domain.ScheduleFailed, Detail: err.Error()}
	}
	if err := ff.SendTurn(WithActor(ctx, scheduleActor(s)), s.Prompt, nil); err != nil {
		return domain.ScheduleOutcome{Status: domain.ScheduleFailed, Detail: err.Error()}
	}
	return domain.ScheduleOutcome{Status: domain.ScheduleOK}
}

// fireMint mints one freeform card and kicks it off with the schedule's
// prompt. A cadence whose previous kickoff failed does not pile up cards:
// while the failed card exists and is not closed, the next fire retries
// its kickoff instead of minting another beside it.
func (e *Engine) fireMint(ctx context.Context, s domain.Schedule) domain.ScheduleOutcome {
	// An older card does not block — only one whose session is working
	// right now does. The skip says nothing about the card's own worth;
	// the next cadence reassesses, exactly like a heartbeat's.
	if s.LastCard != "" {
		if ff := e.Freeform(s.LastCard); ff != nil && ff.Busy() {
			return domain.ScheduleOutcome{
				Status: domain.ScheduleSkippedBusy,
				Detail: fmt.Sprintf("%s is mid-turn; the fire is skipped, not queued", s.LastCard),
			}
		}
	}
	// The orphan check reads OrphanCard alone — never LastStatus or
	// LastCard, which are display state a good fire's card must never be
	// retried from (a card from an earlier successful fire never receives
	// a second opening turn).
	if s.OrphanCard != "" {
		orphan, err := e.cfg.Store.GetFeature(ctx, s.OrphanCard)
		switch {
		case errors.Is(err, state.ErrNotFound):
			// Deleted: counts as closed. This fire mints fresh and the
			// outcome's empty Orphan clears the pointer.
		case err != nil:
			// A store error that is not "gone" is transient: keep the
			// pointer and retry on the next cadence rather than mint a
			// second card beside a live orphan.
			return domain.ScheduleOutcome{
				Status: domain.ScheduleFailed,
				Detail: fmt.Sprintf("reading the failed card %s: %v", s.OrphanCard, err),
				Orphan: s.OrphanCard,
			}
		case orphan.Stage != domain.StageDone:
			// Retry the kickoff on the same card, whether or not a session
			// is open; KickoffWith is a no-op once the conversation has
			// started, so a card a person has picked up and spoken into
			// gets no second opening turn.
			oerr := e.openAndKickoff(ctx, orphan, s)
			if oerr != nil {
				return domain.ScheduleOutcome{
					Status: domain.ScheduleFailed,
					Detail: oerr.Error(),
					Card:   orphan.ID, Orphan: orphan.ID,
				}
			}
			return domain.ScheduleOutcome{Status: domain.ScheduleOK, Card: orphan.ID}
		}
		// A closed orphan is not reused: the next fire mints fresh, and
		// the outcome's empty Orphan clears the pointer.
	}
	f, err := cardmint.Mint(ctx, e.cfg.Store, e.cfg.Workspace, cardmint.Input{
		Kind:           domain.KindFreeform,
		Description:    s.Prompt,
		Envelope:       s.Envelope,
		SessionBackend: s.Backend,
		SessionModel:   s.Model,
		Repo:           s.Repo,
		RequireRepo:    e.RequireRepo,
		// A scheduled card never runs in the checkout a person is using:
		// it gets its own worktree and branch like any other card.
		MainCheckout: false,
		// A cadence mints the same description every fire, and there is
		// nobody present to retitle a collision — the title takes the
		// next free variant, the way a goal's cards do.
		Unattended: true,
	})
	if err != nil {
		// A failure before the mint leaves no card behind: the orphan
		// pointer is cleared (nothing was minted to retry) and the
		// schedule fires again on its next cadence.
		return domain.ScheduleOutcome{Status: domain.ScheduleFailed, Detail: err.Error()}
	}
	if err := e.openAndKickoff(ctx, f, s); err != nil {
		// The card exists; its kickoff failed. The same card is named as
		// the orphan so the next fire retries it instead of minting a
		// second card beside a live one.
		return domain.ScheduleOutcome{
			Status: domain.ScheduleFailed,
			Detail: err.Error(),
			Card:   f.ID, Orphan: f.ID,
		}
	}
	return domain.ScheduleOutcome{Status: domain.ScheduleOK, Card: f.ID}
}

// openAndKickoff opens the card's session and sends its opening turn as
// the schedule: the thread shows who spoke.
func (e *Engine) openAndKickoff(ctx context.Context, f domain.Feature, s domain.Schedule) error {
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		return err
	}
	return ff.KickoffWith(WithActor(ctx, scheduleActor(s)), s.Prompt)
}

// scheduleActor is the actor a fire's turns carry, so a card's thread
// shows who spoke.
func scheduleActor(s domain.Schedule) string { return "schedule:" + string(s.ID) }
