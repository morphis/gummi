package state

// Goals (domain.KindGoal) keep their facts here: which cards belong to a
// goal, how each left it, the goal-only settings, and the goal's own log —
// every landing, drop, raise, decision for review, declined finding, note
// and lead turn — as card_events rows of kind EventGoal on the goal card.
// The goal doc carries the prose (objective, done-when, limits, plan); the
// store carries what happened.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// EventGoal is one entry of a goal's log. Its payload is a GoalPayload.
const EventGoal = "goal"

// Goal log actions: the closed vocabulary of GoalPayload.Action.
const (
	GoalMinted        = "minted"         // the goal created a card
	GoalAttached      = "attached"       // an existing card was handed to the goal
	GoalStarted       = "started"        // a card was started
	GoalLanded        = "landed"         // a card landed on the goal branch
	GoalDropped       = "dropped"        // the goal dropped a card
	GoalDetached      = "detached"       // an attached card went back to the board
	GoalRaised        = "raised"         // a card's envelope was raised from the goal budget
	GoalBounced       = "bounced"        // a card was sent back with a note
	GoalAnswered      = "answered"       // the lead answered a card's question
	GoalPlanApproved  = "plan-approved"  // the lead approved a card's plan
	GoalDecision      = "decision"       // a decision for review
	GoalDeclined      = "declined"       // a reviewer finding the lead declined
	GoalNotMet        = "not-met"        // a done-when item marked not met
	GoalItemAdded     = "done-when"      // a done-when item added from your note
	GoalCheckFixed    = "check-fixed"    // a done-when item's check command repaired
	GoalNote          = "note"           // a note you typed into the goal
	GoalFound         = "found"          // a backlog card filed along the way
	GoalLeadTurn      = "lead-turn"      // the lead took a turn
	GoalLeadFailed    = "lead-failed"    // a lead turn failed
	GoalStalled       = "stalled"        // the goal stopped on something it can only wait for — the agent backend, or (Card set) a card's environment; nothing was dropped
	GoalContinued     = "continues"      // this goal continues another; Ref is that goal, and what it knew came with it
	GoalTranche       = "tranche"        // part of the budget held for cards the plan could not name yet; Ref is the tbd row's title, To the credits
	GoalTrancheClosed = "tranche-closed" // a tranche was closed; Ref is its title, To what returned to the goal
	GoalNeedOwner     = "need-owner"     // a question only the owner can answer; Item is what it is about, Alternative the amendment proposed
	GoalKnown         = "known"          // the lead wrote the goal's notebook: a constant decided (Ref is its key) or a finding recorded (Ref is F-N)
	GoalRun           = "run"            // the goal made a run of an experiment; Ref is the run id, Item the experiment
	GoalResumed       = "resumed"        // you picked a goal back up that had stopped to wait for an environment
	GoalCaughtUp      = "caught-up"      // the goal branch caught up with main
	GoalTidied        = "tidied"         // the goal tree was put back after a check run changed tracked files
	GoalCatchUpFail   = "catch-up-failed"
	GoalReserve       = "reserve"     // the lead re-estimated the reserve
	GoalWrapUp        = "wrap-up"     // the goal was told to finish now
	GoalFinished      = "finished"    // the goal's work settled; its review started
	GoalReversed      = "reversed"    // you reversed a decision for review
	GoalRefused       = "refused"     // a card's out-of-sandbox request was refused
	GoalLeadNote      = "lead"        // a free line from the lead for the log
	GoalRework        = "rework"      // work the goal owes: a review's changes, a failed verify, your send-back
	GoalChecks        = "checks"      // the goal's verify-stage check results (Detail is JSON)
	GoalNeedBudget    = "need-budget" // the goal stopped on a card it cannot fund; To is what that card needs
	// GoalSubstrateBudget: the goal's substrate budget was agreed (at the
	// plan gate) or raised (by a person); To is runs, Minutes is minutes.
	GoalSubstrateBudget = "substrate-budget"
	// GoalNeedSubstrate: the goal stopped on a run it cannot afford; Item
	// is the experiment.
	GoalNeedSubstrate = "need-substrate"
)

// GoalPayload is the JSON shape of an EventGoal event. Only the fields an
// action uses are set.
type GoalPayload struct {
	Action string `json:"action"`
	// Card is the goal card this entry is about, when it is about one.
	Card domain.FeatureID `json:"card,omitempty"`
	// Detail is the sentence the log shows: why, what, or the note itself.
	Detail string `json:"detail,omitempty"`
	// N numbers a decision for review (D-N), counted per goal.
	N int `json:"n,omitempty"`
	// Alternative is the option a decision did not take.
	Alternative string `json:"alternative,omitempty"`
	// Item is a done-when id (DW-N) for not-met, done-when and decision
	// entries that trade against one.
	Item string `json:"item,omitempty"`
	// From and To carry amounts: an envelope raise, a reserve estimate.
	From int `json:"from,omitempty"`
	To   int `json:"to,omitempty"`
	// Ref points at what an entry answers: the decision a reversal
	// reverses ("D-3"), the finding a decline declines.
	Ref string `json:"ref,omitempty"`
	// Minutes carries the minutes half of a substrate budget.
	Minutes int `json:"minutes,omitempty"`
	// By is who acted: "lead", "goal" (the conductor's own rule), or
	// "user".
	By string `json:"by,omitempty"`
	// Outage marks a lead-failed entry whose cause was the agent backend
	// being unable to serve the turn at all — a provider quota, a rate
	// limit, an overload (agent.Unavailable) — rather than the lead
	// failing at its job. The two look identical in a log and mean
	// opposite things: one is a reason to stop relying on the lead, the
	// other is a reason to wait.
	Outage bool `json:"outage,omitempty"`
}

// GoalEntry is one decoded goal log entry with its place in the log.
type GoalEntry struct {
	Seq int64
	At  time.Time
	GoalPayload
}

// DecisionRef names a decision for review the way every surface prints it.
func (e GoalEntry) DecisionRef() string { return fmt.Sprintf("D-%d", e.N) }

// AppendGoalEvent records one entry in goal's log. Decisions are numbered
// here, inside the store, so two writers can never mint the same D-N.
func (s *Store) AppendGoalEvent(ctx context.Context, goal domain.FeatureID, p GoalPayload, at time.Time) (GoalEntry, error) {
	if at.IsZero() {
		at = time.Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GoalEntry{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var stage string
	if err := tx.QueryRowContext(ctx, `SELECT stage FROM features WHERE id = ?`, string(goal)).Scan(&stage); err != nil {
		return GoalEntry{}, fmt.Errorf("goal %s: %w", goal, err)
	}
	if p.Action == GoalDecision && p.N == 0 {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM card_events WHERE feature_id = ? AND kind = ? AND payload LIKE '%"action":"decision"%'`,
			string(goal), EventGoal).Scan(&n); err != nil {
			return GoalEntry{}, err
		}
		p.N = n + 1
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return GoalEntry{}, fmt.Errorf("encoding goal event for %s: %w", goal, err)
	}
	res, err := tx.ExecContext(ctx, appendEventSQL,
		string(goal), stage, EventGoal, "", at.UTC().Format(timeFmt), string(payload), "", "")
	if err != nil {
		return GoalEntry{}, fmt.Errorf("recording goal event for %s: %w", goal, err)
	}
	seq, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return GoalEntry{}, err
	}
	return GoalEntry{Seq: seq, At: at, GoalPayload: p}, nil
}

// GoalLog returns goal's log, oldest first.
func (s *Store) GoalLog(ctx context.Context, goal domain.FeatureID) ([]GoalEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, at, payload FROM card_events WHERE feature_id = ? AND kind = ? ORDER BY seq`,
		string(goal), EventGoal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GoalEntry
	for rows.Next() {
		var e GoalEntry
		var at, payload string
		if err := rows.Scan(&e.Seq, &at, &payload); err != nil {
			return nil, err
		}
		if e.At, err = time.Parse(timeFmt, at); err != nil {
			return nil, fmt.Errorf("corrupt goal event timestamp %q: %w", at, err)
		}
		if err := json.Unmarshal([]byte(payload), &e.GoalPayload); err != nil {
			return nil, fmt.Errorf("corrupt goal event payload %q: %w", payload, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListGoalCards returns every card that belongs to goal, dropped ones
// included, lowest number first.
func (s *Store) ListGoalCards(ctx context.Context, goal domain.FeatureID) ([]domain.Feature, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+featureCols+` FROM features WHERE goal_id = ? ORDER BY num`, string(goal))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Feature
	for rows.Next() {
		f, err := scanFeature(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SetGoal puts card into goal (attached marks an existing card handed to
// it). Goals do not nest and a card belongs to at most one goal.
func (s *Store) SetGoal(ctx context.Context, card, goal domain.FeatureID, attached bool) error {
	if goal.Kind() != domain.KindGoal {
		return fmt.Errorf("%s is not a goal", goal)
	}
	if card.Kind() == domain.KindGoal {
		return fmt.Errorf("%s: goals do not nest", card)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE features SET goal_id = ?, goal_attached = ?, goal_dropped_at = '' WHERE id = ? AND (goal_id = '' OR goal_id = ?)`,
		string(goal), attached, string(card), string(goal))
	if err != nil {
		return fmt.Errorf("adding %s to %s: %w", card, goal, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("adding %s to %s: the card does not exist or already belongs to another goal", card, goal)
	}
	return nil
}

// ClearGoal takes card out of its goal: back on the open board.
func (s *Store) ClearGoal(ctx context.Context, card domain.FeatureID) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE features SET goal_id = '', goal_attached = 0, goal_dropped_at = '' WHERE id = ?`, string(card))
	if err != nil {
		return fmt.Errorf("taking %s out of its goal: %w", card, err)
	}
	return nil
}

// SetGoalDropped stamps card as dropped by its goal.
func (s *Store) SetGoalDropped(ctx context.Context, card domain.FeatureID, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE features SET goal_dropped_at = ? WHERE id = ?`, at.UTC().Format(timeFmt), string(card))
	if err != nil {
		return fmt.Errorf("dropping %s: %w", card, err)
	}
	return nil
}

// CloseGoalDropped ends a card its goal dropped: it moves straight to
// done — its branch kept, nothing landed — with one transition and gate
// crossing from wherever it stood. It deliberately
// skips the workflow's one-step-at-a-time rule — a dropped card did not
// pass the stages between, and walking it through them would record
// crossings that never happened. Moving off its stage abandons whatever
// decisions it had open there. A card already done is left as it is.
func (s *Store) CloseGoalDropped(ctx context.Context, card domain.FeatureID, actor string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	f, err := scanFeature(tx.QueryRowContext(ctx, `SELECT `+featureCols+` FROM features WHERE id = ?`, string(card)))
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", card, ErrNotFound)
	}
	if err != nil {
		return err
	}
	if f.Stage == domain.StageDone {
		return nil
	}
	now := at.UTC().Format(timeFmt)
	// The stage alone. This used to stamp handed_off_at too, purely to get
	// the card past Advance's landing floor — so every surface afterwards
	// reported a card nobody had handed off as handed off (`done: true,
	// handed_off: true`, no branch, zero credits). The floor reads
	// goal_dropped_at directly now, and the drop keeps its own name.
	if _, err := tx.ExecContext(ctx, `UPDATE features SET stage = ?, updated_at = ? WHERE id = ?`,
		string(domain.StageDone), now, string(card)); err != nil {
		return fmt.Errorf("closing %s: %w", card, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO transitions (feature_id, from_stage, to_stage, actor, at) VALUES (?,?,?,?,?)`,
		string(card), string(f.Stage), string(domain.StageDone), actor, now); err != nil {
		return fmt.Errorf("recording transition for %s: %w", card, err)
	}
	gateInserted, err := appendGateEventTx(ctx, tx, card, f.Stage, domain.StageDone, actor, at.UTC(), "")
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// The dropped card's stop is a crossing like any other to the hook
	// surface: report it when the log took it. No verified event — a
	// dropped card ended with its branch kept, nothing landed.
	if gateInserted {
		s.observeTransition(card, f.Stage, domain.StageDone, actor, at.UTC(), "")
	}
	return nil
}

// SetFoundBy records that goal filed card as found along the way.
func (s *Store) SetFoundBy(ctx context.Context, card, goal domain.FeatureID) error {
	_, err := s.db.ExecContext(ctx, `UPDATE features SET found_by = ? WHERE id = ?`, string(goal), string(card))
	if err != nil {
		return fmt.Errorf("marking %s found by %s: %w", card, goal, err)
	}
	return nil
}

// SetGoalLanes sets how many of goal's cards may run at once.
func (s *Store) SetGoalLanes(ctx context.Context, goal domain.FeatureID, lanes int) error {
	if lanes < 0 {
		return fmt.Errorf("goal %s: negative lanes", goal)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE features SET goal_lanes = ? WHERE id = ?`, lanes, string(goal))
	return err
}

// SetGoalReserve records the lead's reserve estimate for goal.
func (s *Store) SetGoalReserve(ctx context.Context, goal domain.FeatureID, credits int) error {
	if credits < 0 {
		return fmt.Errorf("goal %s: negative reserve", goal)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE features SET goal_reserve = ? WHERE id = ?`, credits, string(goal))
	return err
}

// SetGoalWrapUp tells goal to finish now. The first stamp wins, so the
// log keeps the moment it was first told.
func (s *Store) SetGoalWrapUp(ctx context.Context, goal domain.FeatureID, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE features SET goal_wrapup_at = ? WHERE id = ? AND goal_wrapup_at = ''`, at.UTC().Format(timeFmt), string(goal))
	return err
}

// ReopenGoalDropped is the inverse of CloseGoalDropped: it moves a card
// its goal closed back to the stage it stood at when the drop happened,
// recording the reverse transition.
//
// The stage is read from the closing transition rather than guessed —
// CloseGoalDropped wrote `from` when it closed the card, so the rewind
// target is a recorded fact and not a policy. A card whose closing edge
// is not in the table (a record older than it, or one closed some other
// way) rewinds to verify: the stage where a card with a branch and no
// landing belongs, and the one whose answer set offers every ending
// again.
//
// Like CloseGoalDropped it writes the stage directly. That is the same
// deliberate exception, for the same reason and in the same direction:
// the drop did not walk the graph on the way in, so the way back is not
// a graph walk either. `done` stays terminal in the workflow — nothing
// here adds an edge to it (DESIGN §3).
func (s *Store) ReopenGoalDropped(ctx context.Context, card domain.FeatureID, actor string, at time.Time) (domain.Stage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	f, err := scanFeature(tx.QueryRowContext(ctx, `SELECT `+featureCols+` FROM features WHERE id = ?`, string(card)))
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%s: %w", card, ErrNotFound)
	}
	if err != nil {
		return "", err
	}
	if f.Stage != domain.StageDone {
		// already open: adopting it is about the goal link, not the stage
		return f.Stage, nil
	}
	back := domain.StageVerify
	rows, err := tx.QueryContext(ctx,
		`SELECT from_stage FROM transitions WHERE feature_id = ? AND to_stage = ? ORDER BY seq DESC LIMIT 1`,
		string(card), string(domain.StageDone))
	if err != nil {
		return "", err
	}
	if rows.Next() {
		var from string
		if err := rows.Scan(&from); err == nil && domain.Stage(from).Valid() && domain.Stage(from) != domain.StageDone {
			back = domain.Stage(from)
		}
	}
	rows.Close()
	now := at.UTC().Format(timeFmt)
	if _, err := tx.ExecContext(ctx, `UPDATE features SET stage = ?, updated_at = ? WHERE id = ?`,
		string(back), now, string(card)); err != nil {
		return "", fmt.Errorf("reopening %s: %w", card, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO transitions (feature_id, from_stage, to_stage, actor, at) VALUES (?,?,?,?,?)`,
		string(card), string(domain.StageDone), string(back), actor, now); err != nil {
		return "", fmt.Errorf("recording transition for %s: %w", card, err)
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return back, nil
}

// ClearGoalWrapUp lifts a wrap-up: the goal was sent back with more budget
// or notes and may start work again.
func (s *Store) ClearGoalWrapUp(ctx context.Context, goal domain.FeatureID) error {
	_, err := s.db.ExecContext(ctx, `UPDATE features SET goal_wrapup_at = '', goal_partial = '' WHERE id = ?`, string(goal))
	return err
}

// SetGoalPartial records why goal finished partial ("" = whole).
func (s *Store) SetGoalPartial(ctx context.Context, goal domain.FeatureID, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE features SET goal_partial = ? WHERE id = ?`, strings.TrimSpace(reason), string(goal))
	return err
}

// formatOptTime stores a zero time as the empty string, the convention
// every optional timestamp column follows.
func formatOptTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeFmt)
}

// parseOptTime is formatOptTime's reverse.
func parseOptTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	return time.Parse(timeFmt, v)
}

// ClearVerifiedAt removes a card's verified stamp: its goal sent the
// verified branch back to work (the goal branch moved under it, or its
// checks stopped passing there), so it is no longer ready to land.
func (s *Store) ClearVerifiedAt(ctx context.Context, id domain.FeatureID) error {
	_, err := s.db.ExecContext(ctx, `UPDATE features SET verified_at = '' WHERE id = ?`, string(id))
	if err != nil {
		return fmt.Errorf("clearing the verified stamp on %s: %w", id, err)
	}
	return nil
}

// CardMark is the newest event of one kind on a card.
type CardMark struct {
	Seq     int64
	At      time.Time
	Stage   domain.Stage
	Payload string
}

// CardMarks is the newest park, stage entry and gate crossing on a card —
// what a goal reads to tell a card that stopped and is waiting from one
// that is merely between two steps. Last is the newest event of any kind:
// a card that just finished a turn is between steps, however long ago it
// last crossed a stage.
type CardMarks struct {
	Park, StageEnter, Gate, Last CardMark
}

// ParkReason decodes the newest park's reason and detail.
func (m CardMarks) ParkReason() (reason, detail string) {
	if m.Park.Seq == 0 {
		return "", ""
	}
	var p ParkPayload
	_ = json.Unmarshal([]byte(m.Park.Payload), &p)
	return p.Reason, p.Detail
}

// LatestCardMarks reads a card's CardMarks.
func (s *Store) LatestCardMarks(ctx context.Context, id domain.FeatureID) (CardMarks, error) {
	var out CardMarks
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.kind, e.seq, e.at, e.stage, e.payload FROM card_events e
		JOIN (SELECT kind, MAX(seq) AS seq FROM card_events
		      WHERE feature_id = ? AND kind IN (?, ?, ?) GROUP BY kind) m
		ON e.seq = m.seq
		UNION ALL
		SELECT '', seq, at, stage, '' FROM (SELECT seq, at, stage FROM card_events
		      WHERE feature_id = ? ORDER BY seq DESC LIMIT 1)`,
		string(id), EventPark, EventStageEnter, EventGate, string(id))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, at, stage, payload string
		var mk CardMark
		if err := rows.Scan(&kind, &mk.Seq, &at, &stage, &payload); err != nil {
			return out, err
		}
		if mk.At, err = time.Parse(timeFmt, at); err != nil {
			return out, fmt.Errorf("corrupt card_events timestamp %q: %w", at, err)
		}
		mk.Stage, mk.Payload = domain.Stage(stage), payload
		switch kind {
		case EventPark:
			out.Park = mk
		case EventStageEnter:
			out.StageEnter = mk
		case EventGate:
			out.Gate = mk
		case "":
			out.Last = mk
		}
	}
	return out, rows.Err()
}
