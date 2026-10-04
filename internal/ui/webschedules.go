package ui

// Schedules for the web face (DESIGN §19.9, §20). A schedule row is a
// store fact; the one thing a web write needs beyond the store is the
// board's repository check (the same one the card form runs, before
// anything is stored) and, for a forced fire, the engine — the board's,
// since this process is the board. Reads are projections off the loop;
// writes run off it and report back as the notice they would have
// toasted.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/schedule"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/webapi"
)

// Schedules is GET /api/schedules: every row, oldest first.
func (b *Bridge) Schedules(ctx context.Context) (webapi.Schedules, error) {
	return webLoad(ctx, b, func(m *Shell) (func(context.Context) (webapi.Schedules, error), error) {
		store := m.store
		if store == nil {
			return nil, webErr(WebUnavailable, "this board has no store to keep schedules in")
		}
		return func(ctx context.Context) (webapi.Schedules, error) {
			rows, err := store.ListSchedules(ctx)
			if err != nil {
				return webapi.Schedules{}, err
			}
			out := webapi.Schedules{Schedules: make([]webapi.Schedule, 0, len(rows))}
			for _, sc := range rows {
				out.Schedules = append(out.Schedules, webSchedule(sc))
			}
			return out, nil
		}, nil
	})
}

// WebSchedule projects one row for the page — and for the CLI's
// schedule --json output, which shows the same shape rather than a
// second projection of it.
func WebSchedule(sc domain.Schedule) webapi.Schedule {
	return webSchedule(sc)
}

func webSchedule(sc domain.Schedule) webapi.Schedule {
	return webapi.Schedule{
		ID: string(sc.ID), Name: sc.Name, Kind: string(sc.Kind),
		Target: string(sc.Target), Repo: sc.Repo,
		Cron: sc.Cron, Timezone: sc.Timezone, Prompt: sc.Prompt,
		Backend: sc.Backend, Model: sc.Model, Envelope: sc.Envelope,
		Enabled: sc.Enabled, RunRequested: sc.RunRequested,
		LastRun: sc.LastRun, NextRun: sc.NextRun,
		LastStatus: string(sc.LastStatus), LastDetail: sc.LastDetail,
		LastCard: string(sc.LastCard), OrphanCard: string(sc.OrphanCard),
		CreatedAt: sc.CreatedAt,
	}
}

// webScheduleFire projects one fire for the page.
func webScheduleFire(f engine.ScheduleFire) webapi.ScheduleFire {
	return webapi.ScheduleFire{
		ID: string(f.ID), Name: f.Name, Kind: string(f.Kind), Forced: f.Forced,
		Status: string(f.Outcome.Status), Detail: f.Outcome.Detail,
		Card: string(f.Outcome.Card), Orphan: string(f.Outcome.Orphan), At: f.Outcome.At,
	}
}

// scheduleFromRequest builds a definition the store will accept. The
// cadence is a preset or an expression — one of the two; a preset
// compiles here, so the stored form is always the compiled cron. A
// request that names no kind reads as a heartbeat when it names a target
// and nothing otherwise.
func scheduleFromRequest(req webapi.ScheduleRequest) (*domain.Schedule, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, webErr(WebBadRequest, "name the schedule")
	}
	id, err := domain.NewScheduleID(name)
	if err != nil {
		return nil, webErr(WebBadRequest, "%s", err)
	}
	kind := domain.ScheduleKind(strings.TrimSpace(req.Kind))
	if kind == "" && strings.TrimSpace(req.Target) != "" {
		kind = domain.ScheduleHeartbeat
	}
	cron := strings.TrimSpace(req.Cron)
	if cron == "" {
		every := strings.TrimSpace(req.Every)
		if every == "" {
			return nil, webErr(WebBadRequest, "give the cadence: cron \"0 * * * *\" or every 5m")
		}
		cron, err = schedule.Compile(every)
		if err != nil {
			return nil, webErr(WebBadRequest, "%s", err)
		}
	}
	return &domain.Schedule{
		ID: id, Name: name, Kind: kind,
		Target: webID(req.Target), Repo: strings.TrimSpace(req.Repo),
		Cron: cron, Timezone: strings.TrimSpace(req.Timezone),
		Prompt:  strings.TrimSpace(req.Prompt),
		Backend: strings.TrimSpace(req.Backend), Model: strings.TrimSpace(req.Model),
		Envelope: func() int {
			if req.Envelope != nil {
				return *req.Envelope
			}
			return 0
		}(),
	}, nil
}

// scheduleHandles grabs, on the loop, what a schedule write needs off it:
// the store, the repository check, and the board's now.
func (m *Shell) scheduleHandles() (*state.Store, func(string) error, time.Time) {
	return m.store, m.requireRepo, m.now()
}

// CreateSchedule is POST /api/schedules: a definition, stored disabled.
// A mint body's repo goes through the board's repository check before
// the store write — the same check the card form runs.
func (b *Bridge) CreateSchedule(ctx context.Context, req webapi.ScheduleRequest) (webapi.Schedule, error) {
	sc, err := scheduleFromRequest(req)
	if err != nil {
		return webapi.Schedule{}, err
	}
	if sc.Kind == domain.ScheduleHeartbeat {
		// A heartbeat has no repository of its own; the target's is the
		// one that matters, and the store's validation refuses the rest.
		sc.Repo = ""
		sc.Envelope = 0
		sc.Backend, sc.Model = "", ""
	}
	var store *state.Store
	var requireRepo func(string) error
	var now time.Time
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		store, requireRepo, now = m.scheduleHandles()
		return nil
	}); err != nil {
		return webapi.Schedule{}, err
	}
	if store == nil {
		return webapi.Schedule{}, webErr(WebUnavailable, "this board has no store to keep schedules in")
	}
	if sc.Kind == domain.ScheduleMint {
		if err := requireRepo(sc.Repo); err != nil {
			return webapi.Schedule{}, webErr(WebConflict, "%s", sanitize(err.Error()))
		}
	}
	sc.CreatedAt = now
	if err := store.CreateSchedule(ctx, sc); err != nil {
		return webapi.Schedule{}, webScheduleErr(err)
	}
	_, err = b.Await(ctx, func(m *Shell) (tea.Cmd, error) {
		return m.scheduleNotice(fmt.Sprintf("schedule %s added — off until you enable it", sc.ID)), nil
	})
	if err != nil {
		return webapi.Schedule{}, err
	}
	return webSchedule(*sc), nil
}

// UpdateSchedule is PATCH /api/schedules/{id}: a definition edit. The
// store forces the row off — a cadence the person has not re-approved is
// a cadence that must not fire — so the answer says to re-enable it.
func (b *Bridge) UpdateSchedule(ctx context.Context, id string, req webapi.ScheduleRequest) (webapi.Schedule, error) {
	sc, err := scheduleFromRequest(req)
	if err != nil {
		return webapi.Schedule{}, err
	}
	var store *state.Store
	var requireRepo func(string) error
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		store, requireRepo, _ = m.scheduleHandles()
		return nil
	}); err != nil {
		return webapi.Schedule{}, err
	}
	if store == nil {
		return webapi.Schedule{}, webErr(WebUnavailable, "this board has no store to keep schedules in")
	}
	current, err := store.Schedule(ctx, domain.ScheduleID(id))
	if err != nil {
		return webapi.Schedule{}, webScheduleErr(err)
	}
	// The identity is not what an edit changes: the row's id and kind
	// stay, and a heartbeat keeps its target unless the request names
	// another freeform card.
	sc.ID = current.ID
	sc.Kind = current.Kind
	sc.CreatedAt = current.CreatedAt
	if sc.Kind == domain.ScheduleHeartbeat {
		sc.Repo, sc.Envelope, sc.Backend, sc.Model = "", 0, "", ""
		if sc.Target == "" {
			sc.Target = current.Target
		}
	} else {
		sc.Target = ""
		if err := requireRepo(sc.Repo); err != nil {
			return webapi.Schedule{}, webErr(WebConflict, "%s", sanitize(err.Error()))
		}
	}
	if err := store.UpdateScheduleDefinition(ctx, sc); err != nil {
		return webapi.Schedule{}, webScheduleErr(err)
	}
	_, err = b.Await(ctx, func(m *Shell) (tea.Cmd, error) {
		return m.scheduleNotice(fmt.Sprintf("schedule %s edited — off until you enable it again", sc.ID)), nil
	})
	if err != nil {
		return webapi.Schedule{}, err
	}
	return webSchedule(*sc), nil
}

// EnableSchedule is POST /api/schedules/{id}/enable: on goes with the
// first fire the cadence computes from now.
func (b *Bridge) EnableSchedule(ctx context.Context, id string) (webapi.Schedule, error) {
	var store *state.Store
	var now time.Time
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		store, _, now = m.scheduleHandles()
		return nil
	}); err != nil {
		return webapi.Schedule{}, err
	}
	if store == nil {
		return webapi.Schedule{}, webErr(WebUnavailable, "this board has no store to keep schedules in")
	}
	sc, err := store.Schedule(ctx, domain.ScheduleID(id))
	if err != nil {
		return webapi.Schedule{}, webScheduleErr(err)
	}
	next, err := schedule.NextRun(sc.Cron, sc.Timezone, now)
	if err != nil {
		return webapi.Schedule{}, webScheduleErr(err)
	}
	if err := store.SetScheduleEnabled(ctx, sc.ID, true, next); err != nil {
		return webapi.Schedule{}, webScheduleErr(err)
	}
	sc, _ = store.Schedule(ctx, sc.ID)
	_, err = b.Await(ctx, func(m *Shell) (tea.Cmd, error) {
		return m.scheduleNotice(fmt.Sprintf("schedule %s enabled — first fire %s", sc.ID, next.Format(time.DateTime))), nil
	})
	if err != nil {
		return webapi.Schedule{}, err
	}
	return webSchedule(sc), nil
}

// DisableSchedule is POST /api/schedules/{id}/disable.
func (b *Bridge) DisableSchedule(ctx context.Context, id string) (webapi.Schedule, error) {
	var store *state.Store
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		store, _, _ = m.scheduleHandles()
		return nil
	}); err != nil {
		return webapi.Schedule{}, err
	}
	if store == nil {
		return webapi.Schedule{}, webErr(WebUnavailable, "this board has no store to keep schedules in")
	}
	sc, err := store.Schedule(ctx, domain.ScheduleID(id))
	if err != nil {
		return webapi.Schedule{}, webScheduleErr(err)
	}
	if err := store.SetScheduleEnabled(ctx, sc.ID, false, time.Time{}); err != nil {
		return webapi.Schedule{}, webScheduleErr(err)
	}
	sc, _ = store.Schedule(ctx, sc.ID)
	_, err = b.Await(ctx, func(m *Shell) (tea.Cmd, error) {
		return m.scheduleNotice(fmt.Sprintf("schedule %s disabled", sc.ID)), nil
	})
	if err != nil {
		return webapi.Schedule{}, err
	}
	return webSchedule(sc), nil
}

// RunScheduleNow is POST /api/schedules/{id}/run: the forced fire, the
// board's engine running it — the same path the TUI's run-now key takes.
// The fire's outcome is the answer, and the board toasts it like any
// other fire (a busy skip says nothing: it is recorded, not announced).
func (b *Bridge) RunScheduleNow(ctx context.Context, id string) (webapi.ScheduleFire, error) {
	return webLoad(ctx, b, func(m *Shell) (func(context.Context) (webapi.ScheduleFire, error), error) {
		eng := m.engine
		if eng == nil {
			return nil, webErr(WebUnavailable, "this board has no agent to fire")
		}
		return func(ctx context.Context) (webapi.ScheduleFire, error) {
			fire, err := eng.RunSchedule(ctx, domain.ScheduleID(id), time.Now())
			if err != nil {
				return webapi.ScheduleFire{}, webScheduleErr(err)
			}
			out := webScheduleFire(fire)
			if out.Status != string(domain.ScheduleSkippedBusy) {
				b.deliver(noticeMsg{
					text:   sanitize(scheduleFireNotice(fire)),
					isErr:  fire.Outcome.Status == domain.ScheduleFailed,
					reload: true,
				})
			}
			return out, nil
		}, nil
	})
}

// DeleteSchedule is DELETE /api/schedules/{id}: the row goes; the cards
// it minted stay on the board like any freeform card.
func (b *Bridge) DeleteSchedule(ctx context.Context, id string) (WebOutcome, error) {
	var store *state.Store
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { store = m.store; return nil }); err != nil {
		return WebOutcome{}, err
	}
	if store == nil {
		return WebOutcome{}, webErr(WebUnavailable, "this board has no store to keep schedules in")
	}
	if err := store.DeleteSchedule(ctx, domain.ScheduleID(id)); err != nil {
		return WebOutcome{}, webScheduleErr(err)
	}
	out, err := b.Await(ctx, func(m *Shell) (tea.Cmd, error) {
		return m.scheduleNotice(fmt.Sprintf("schedule %s deleted", id)), nil
	})
	out.ID = id
	return out, err
}

// scheduleNotice is a schedule verb's toast: a notice on the loop and a
// reload, since a row changed. A schedule verb's refusals answer the
// request directly; what reaches here as a notice is a success.
func (m *Shell) scheduleNotice(text string) tea.Cmd {
	return func() tea.Msg { return noticeMsg{text: sanitize(text), reload: true} }
}

// webScheduleErr classes a schedule refusal: a missing row is not found,
// anything else is the board or the store saying no.
func webScheduleErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := IsWebError(err); ok {
		return err
	}
	if errors.Is(err, state.ErrNotFound) {
		return webErr(WebNotFound, "%s", sanitize(err.Error()))
	}
	return webErr(WebConflict, "%s", sanitize(err.Error()))
}
