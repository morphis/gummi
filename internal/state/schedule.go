package state

// Schedules and heartbeats, in the store (DESIGN §19.9). A schedule row
// is a definition plus its fire state: the cadence (compiled cron), the
// target, and the one outcome of history the row keeps.
//
// The store is the cron-validity enforcement point. Every other writer —
// the CLI, the web face, a test — reaches this table only through these
// methods, and each definition write runs the cron package's Check, so
// no face can store an expression the board would silently never fire.
// The same writes are the off-by-default rule: a definition is inserted
// disabled, and any later change to it forces it off until an explicit
// enable.
//
// A fire is claimed by compare-and-set on next_run, in one transaction,
// so one due instant produces at most one fire even with the board's
// poll and a CLI write landing at the same moment.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/schedule"
)

// scheduleCols is the SELECT list every schedule read shares, so a new
// column cannot be written by one method and read back by none.
const scheduleCols = `id, name, kind, target, repo, cron, timezone, prompt,
	backend, model, envelope, enabled, run_requested,
	last_run, next_run, last_status, last_detail, last_card, orphan_card,
	created_at`

// checkSchedule is the two checks every definition write runs before a
// row is touched: the structural rules, then the cron package's rule that
// the expression parses, the zone loads, and Next is non-zero.
func checkSchedule(sc *domain.Schedule) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	if err := schedule.Check(sc.Cron, sc.Timezone); err != nil {
		return fmt.Errorf("schedule %s: %w", sc.ID, err)
	}
	return nil
}

// CreateSchedule records a new schedule, always disabled: off by default
// is the feature's own rule, and the caller's Enabled — if it set one —
// is not carried. The cadence must pass Check; an unparseable or
// never-matching expression is refused here, before a row exists that
// nothing would ever fire.
func (s *Store) CreateSchedule(ctx context.Context, sc *domain.Schedule) error {
	if err := checkSchedule(sc); err != nil {
		return err
	}
	if _, err := s.Schedule(ctx, sc.ID); err == nil {
		return fmt.Errorf("a schedule named %q already exists (as %s); rm it first", sc.Name, sc.ID)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO schedules (
		id, name, kind, target, repo, cron, timezone, prompt, backend, model,
		envelope, enabled, run_requested, last_run, next_run, last_status,
		last_detail, last_card, orphan_card, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		0, 0, '', '', '', '', '', '', ?)`,
		string(sc.ID), sc.Name, string(sc.Kind), string(sc.Target), sc.Repo,
		sc.Cron, sc.Timezone, sc.Prompt, sc.Backend, sc.Model, sc.Envelope,
		sc.CreatedAt.UTC().Format(timeFmt))
	if err != nil {
		return fmt.Errorf("creating schedule %s: %w", sc.ID, err)
	}
	return nil
}

// UpdateScheduleDefinition writes a definition's own fields. The row's
// identity (id and the name it was derived from) is not part of what an
// edit changes, and neither is its fire state: an edit forces the
// schedule off — enabled=0, run_requested cleared, next_run cleared —
// because a cadence the person has not re-approved is a cadence that
// must not fire. Enabling writes the fresh next run.
func (s *Store) UpdateScheduleDefinition(ctx context.Context, sc *domain.Schedule) error {
	if err := checkSchedule(sc); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE schedules SET
		kind = ?, target = ?, repo = ?, cron = ?, timezone = ?, prompt = ?,
		backend = ?, model = ?, envelope = ?,
		enabled = 0, run_requested = 0, next_run = ''
	WHERE id = ?`,
		string(sc.Kind), string(sc.Target), sc.Repo, sc.Cron, sc.Timezone, sc.Prompt,
		sc.Backend, sc.Model, sc.Envelope, string(sc.ID))
	if err != nil {
		return fmt.Errorf("updating schedule %s: %w", sc.ID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("schedule %s: %w", sc.ID, ErrNotFound)
	}
	return nil
}

// SetScheduleEnabled turns a schedule on or off. Enabling requires the
// caller to have computed the first fire (schedule.NextRun) — a zero
// next run is refused, so an enabled row always has a cadence to be due
// on — and clears run_requested: a request that somehow survived on a
// disabled row must never make the first tick after an enable fire
// off-cadence. Disabling clears the request too, and the next run with
// it, so a disabled row reads clean: nothing pending, nothing armed.
func (s *Store) SetScheduleEnabled(ctx context.Context, id domain.ScheduleID, on bool, nextRun time.Time) error {
	if on && nextRun.IsZero() {
		return fmt.Errorf("enabling schedule %s: no next run; compute it from the cadence first", id)
	}
	var q string
	var args []any
	if on {
		q = `UPDATE schedules SET enabled = 1, run_requested = 0, next_run = ? WHERE id = ?`
		args = []any{nextRun.UTC().Format(timeFmt), string(id)}
	} else {
		q = `UPDATE schedules SET enabled = 0, run_requested = 0, next_run = '' WHERE id = ?`
		args = []any{string(id)}
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("enabling schedule %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("schedule %s: %w", id, ErrNotFound)
	}
	return nil
}

// RequestScheduleRun marks a schedule for the board's next tick — what
// the CLI's run-now is: this process cannot fire a freeform session, so
// it asks the running board to. A disabled schedule is refused here, at
// the write, with the instruction the face shows: enable it first.
func (s *Store) RequestScheduleRun(ctx context.Context, id domain.ScheduleID) error {
	sc, err := s.Schedule(ctx, id)
	if err != nil {
		return err
	}
	if !sc.Enabled {
		return fmt.Errorf("schedule %s is disabled; enable it first", id)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE schedules SET run_requested = 1 WHERE id = ?`, string(id)); err != nil {
		return fmt.Errorf("requesting a run of schedule %s: %w", id, err)
	}
	return nil
}

// ClaimScheduleFire claims one due instant by compare-and-set: the
// update lands only while next_run still equals expectNext (the value
// the claimer read), so a second claimer — the CLI writing concurrently,
// another board, a second tick — finds the row already moved on and gets
// false. The claim writes the advanced next run and clears
// run_requested in the same write, so a fire is never double-claimed by
// the cadence and a run-now together.
//
// Without force the row must be enabled; with force (RunSchedule, a
// person's run-now from the TUI or web board) only the next_run match is
// required, and a disabled row — whose next run is empty — keeps it
// empty: the claim's newNext for such a fire is the zero time. An enabled
// row refusing a zero newNext is the defensive half of the same rule:
// an enabled row must always know when it fires next.
func (s *Store) ClaimScheduleFire(ctx context.Context, id domain.ScheduleID, expectNext, newNext time.Time, force bool) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	var enabled, requested int
	var next string
	err = tx.QueryRowContext(ctx,
		`SELECT enabled, run_requested, next_run FROM schedules WHERE id = ?`, string(id)).
		Scan(&enabled, &requested, &next)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("schedule %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return false, err
	}
	if enabled == 1 && newNext.IsZero() {
		return false, fmt.Errorf("schedule %s: refusing a fire with no next run", id)
	}
	var claimed bool
	if force {
		claimed = next == formatOptTime(expectNext)
	} else {
		claimed = enabled == 1 && (next == formatOptTime(expectNext) || requested == 1)
	}
	if !claimed {
		return false, nil
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE schedules SET next_run = ?, run_requested = 0 WHERE id = ?`,
		formatOptTime(newNext), string(id))
	if err != nil {
		return false, fmt.Errorf("claiming schedule %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	return true, tx.Commit()
}

// RecordScheduleOutcome writes one fire's result: when it ran, what
// happened, which card it minted or retried. It is one UPDATE, and its
// rules are the ones that keep the row's state legible afterwards:
//
//   - last_card moves only when this fire named a card; a fire that
//     minted nothing leaves the previous one on display.
//   - orphan_card is overwritten unconditionally, so every outcome that
//     is not "minted (or retried), then its kickoff failed" clears the
//     pointer — including a failure before the mint, which would
//     otherwise leave a stale pointer pointing at a card whose fire
//     never happened.
//   - every outcome that turns the schedule off (the two pause statuses
//     and the engine's defensive disable) clears run_requested in the
//     same write. A CLI run-now can land between the claim — which
//     already cleared the flag — and this outcome; on a row this write
//     is disabling, letting the request survive would make the next
//     enable fire at once, with the envelope, at a moment nobody chose.
func (s *Store) RecordScheduleOutcome(ctx context.Context, id domain.ScheduleID, o domain.ScheduleOutcome) error {
	disable := o.Disable || o.Status.Disables()
	res, err := s.db.ExecContext(ctx, `UPDATE schedules SET
		last_run = ?, last_status = ?, last_detail = ?,
		last_card = CASE WHEN ? <> '' THEN ? ELSE last_card END,
		orphan_card = ?,
		enabled = CASE WHEN ? THEN 0 ELSE enabled END,
		next_run = CASE WHEN ? THEN '' ELSE next_run END,
		run_requested = CASE WHEN ? THEN 0 ELSE run_requested END
	WHERE id = ?`,
		formatOptTime(o.At), string(o.Status), o.Detail,
		string(o.Card), string(o.Card), string(o.Orphan),
		boolInt(disable), boolInt(disable), boolInt(disable), string(id))
	if err != nil {
		return fmt.Errorf("recording schedule %s outcome: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("schedule %s: %w", id, ErrNotFound)
	}
	return nil
}

// boolInt is a bool as SQLite wants it.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ListSchedules returns every schedule, oldest first.
func (s *Store) ListSchedules(ctx context.Context) ([]domain.Schedule, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+scheduleCols+` FROM schedules ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Schedule
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// Schedule reads one schedule row. Returns ErrNotFound when there is none.
func (s *Store) Schedule(ctx context.Context, id domain.ScheduleID) (domain.Schedule, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+scheduleCols+` FROM schedules WHERE id = ?`, string(id))
	sc, err := scanSchedule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return sc, fmt.Errorf("schedule %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return sc, err
	}
	return sc, nil
}

// DeleteSchedule removes a schedule row. It says nothing about any card
// the schedule minted: those are freeform cards, closed by a person like
// any other.
func (s *Store) DeleteSchedule(ctx context.Context, id domain.ScheduleID) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM schedules WHERE id = ?`, string(id))
	if err != nil {
		return fmt.Errorf("deleting schedule %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("schedule %s: %w", id, ErrNotFound)
	}
	return nil
}

// scanSchedule reads one row of scheduleCols.
func scanSchedule(row interface{ Scan(...any) error }) (domain.Schedule, error) {
	var sc domain.Schedule
	var sid, kind, target, status string
	var enabled, requested, envelope int
	var lastRun, nextRun, createdAt string
	err := row.Scan(&sid, &sc.Name, &kind, &target, &sc.Repo, &sc.Cron, &sc.Timezone,
		&sc.Prompt, &sc.Backend, &sc.Model, &envelope,
		&enabled, &requested,
		&lastRun, &nextRun, &status, &sc.LastDetail, &sc.LastCard, &sc.OrphanCard,
		&createdAt)
	if err != nil {
		return sc, err
	}
	sc.ID = domain.ScheduleID(sid)
	sc.Kind = domain.ScheduleKind(kind)
	sc.Target = domain.FeatureID(target)
	sc.Enabled = enabled == 1
	sc.RunRequested = requested == 1
	sc.Envelope = envelope
	sc.LastStatus = domain.ScheduleStatus(status)
	if sc.LastRun, err = parseOptTime(lastRun); err != nil {
		return sc, fmt.Errorf("schedule %s: corrupt last_run %q", sc.ID, lastRun)
	}
	if sc.NextRun, err = parseOptTime(nextRun); err != nil {
		return sc, fmt.Errorf("schedule %s: corrupt next_run %q", sc.ID, nextRun)
	}
	if sc.CreatedAt, err = parseOptTime(createdAt); err != nil {
		return sc, fmt.Errorf("schedule %s: corrupt created_at %q", sc.ID, createdAt)
	}
	return sc, nil
}
