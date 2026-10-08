package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/schedule"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/webapi"
)

// The `gummi schedule` verbs: define, read, enable, disable, request a
// run-now, and remove — the operator surface for schedules and
// heartbeats (DESIGN §19.9). These verbs are store writes that run with
// or without a board; none of them fires a session, because a freeform
// session is a board's thing. `run-now` sets a request the running
// board's next tick serves; when no board is running it says so, and the
// request sits in the store until one comes up.

// scheduleEnv is the store wiring the verbs share; a schedule never
// drives a card, so no engine is built. The pool is bound anyway for the
// repository check a mint's --repo gets for free at the store, and for
// the lock probe run-now reports with.
type scheduleEnv struct {
	store   *state.Store
	ws      state.Workspace
	cleanup func()
}

func openScheduleEnv() (*scheduleEnv, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	wsRoot, defaultRoot, _, err := resolveAllRoots(cwd)
	if err != nil {
		return nil, err
	}
	ws, err := ensureWorkspace(wsRoot, defaultRoot)
	if err != nil {
		return nil, err
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		return nil, err
	}
	return &scheduleEnv{store: store, ws: ws, cleanup: func() { _ = store.Close() }}, nil
}

// resolveScheduleArg names a schedule by id or by the name it was added
// under: the id is the slug of the name, so both usually agree; a name
// that slugifies differently is tried as its slug.
func resolveScheduleArg(ctx context.Context, store *state.Store, arg string) (domain.ScheduleID, error) {
	if strings.TrimSpace(arg) == "" {
		return "", fmt.Errorf("schedule: name the schedule")
	}
	rows, err := store.ListSchedules(ctx)
	if err != nil {
		return "", err
	}
	for _, sc := range rows {
		if string(sc.ID) == arg || sc.Name == arg {
			return sc.ID, nil
		}
	}
	id, err := domain.NewScheduleID(arg)
	if err == nil {
		for _, sc := range rows {
			if sc.ID == id {
				return sc.ID, nil
			}
		}
	}
	return "", fmt.Errorf("no schedule %q (gummi schedule list shows the ones there are)", arg)
}

// runScheduleList implements `gummi schedule list [--json]`.
func runScheduleList(fl cliFlags, _ []string) error {
	env, err := openScheduleEnv()
	if err != nil {
		return err
	}
	defer env.cleanup()
	rows, err := env.store.ListSchedules(context.Background())
	if err != nil {
		return err
	}
	if fl.Bool("json") {
		out := webapi.Schedules{Schedules: make([]webapi.Schedule, 0, len(rows))}
		for _, sc := range rows {
			out.Schedules = append(out.Schedules, ui.WebSchedule(sc))
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, string(b))
		return err
	}
	if len(rows) == 0 {
		fmt.Println("  no schedules. `gummi schedule add` defines one.")
		return nil
	}
	now := time.Now()
	for _, sc := range rows {
		state := "off"
		if sc.Enabled {
			state = "on "
		}
		target := ""
		switch {
		case sc.Kind == domain.ScheduleHeartbeat:
			target = " → " + string(sc.Target)
		case sc.Repo != "":
			target = " · " + sc.Repo
		}
		fmt.Printf("  %s  %s  %s%s  %s  %s\n",
			string(sc.ID), state, scheduleKindWordCLI(sc.Kind), target, sc.Cron, scheduleFireWords(sc, now))
	}
	return nil
}

// scheduleKindWordCLI is a row's kind as the list prints it.
func scheduleKindWordCLI(k domain.ScheduleKind) string {
	if k == domain.ScheduleHeartbeat {
		return "heartbeat"
	}
	return "mint"
}

// scheduleFireWords is a row's cadence state as the list prints it: when
// it fires next, and what happened last.
func scheduleFireWords(sc domain.Schedule, now time.Time) string {
	parts := []string{}
	switch {
	case !sc.Enabled:
		parts = append(parts, "off")
	case sc.NextRun.IsZero():
		parts = append(parts, "no next run")
	default:
		d := sc.NextRun.Sub(now)
		parts = append(parts, fmt.Sprintf("next in %s", d.Round(time.Second)))
	}
	if sc.LastStatus != "" {
		parts = append(parts, "last "+string(sc.LastStatus))
	}
	return strings.Join(parts, " · ")
}

// runScheduleAdd implements `gummi schedule add`:
//
//	schedule add --name nightly --cron '0 5 * * *' | --every 1h
//	             [--tz Zone/City] --prompt "…" --envelope N [--repo r]
//	             [--agent b --model m]
//	schedule add --name tidy --every 15m --prompt "…" --heartbeat FF-001
//
// A mint always carries an envelope (a scheduled card mints with a
// brake); a heartbeat never has one (its target's is the brake). The
// stored definition is the compiled cron, and it is stored off.
func runScheduleAdd(fl cliFlags, _ []string) error {
	name := strings.TrimSpace(fl.String("name"))
	cron := strings.TrimSpace(fl.String("cron"))
	every := strings.TrimSpace(fl.String("every"))
	prompt := strings.TrimSpace(fl.String("prompt"))
	tz := strings.TrimSpace(fl.String("tz"))
	heartbeat := strings.TrimSpace(fl.String("heartbeat"))
	repo := strings.TrimSpace(fl.String("repo"))
	backend := strings.TrimSpace(fl.String("agent"))
	model := strings.TrimSpace(fl.String("model"))
	envelope := fl.Budget("envelope")

	if name == "" {
		return fmt.Errorf("schedule add needs --name")
	}
	if prompt == "" {
		return fmt.Errorf("schedule add needs --prompt: the opening turn a mint sends, or the recurring turn a heartbeat sends")
	}
	if cron == "" && every == "" {
		return fmt.Errorf("schedule add needs a cadence: --cron \"0 * * * *\" or --every 5m")
	}
	if every != "" {
		compiled, err := schedule.Compile(every)
		if err != nil {
			return err
		}
		if cron != "" {
			return fmt.Errorf("give one cadence, not both: --cron %q or --every %q", cron, every)
		}
		cron = compiled
	}
	id, err := domain.NewScheduleID(name)
	if err != nil {
		return err
	}
	sc := &domain.Schedule{
		ID: id, Name: name, Cron: cron, Timezone: tz, Prompt: prompt,
		Backend: backend, Model: model,
	}
	if heartbeat != "" {
		sc.Kind = domain.ScheduleHeartbeat
		sc.Target = domain.FeatureID(strings.ToUpper(heartbeat))
		if envelope != 0 {
			return fmt.Errorf("a heartbeat has no envelope: %s's own envelope is the brake; leave --envelope off", heartbeat)
		}
	} else {
		sc.Kind = domain.ScheduleMint
		sc.Repo = repo
		sc.Envelope = envelope
		if envelope <= 0 {
			return fmt.Errorf("schedule add needs --envelope <dollars> for a mint: a scheduled card always mints with a brake")
		}
	}
	env, err := openScheduleEnv()
	if err != nil {
		return err
	}
	defer env.cleanup()
	sc.CreatedAt = time.Now()
	if err := env.store.CreateSchedule(context.Background(), sc); err != nil {
		return err
	}
	if fl.Bool("json") {
		b, err := json.MarshalIndent(ui.WebSchedule(*sc), "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, string(b))
		return err
	}
	fmt.Printf("  schedule %s added — off until `gummi schedule enable %s`\n", sc.ID, sc.ID)
	return nil
}

// runScheduleEnable implements `gummi schedule enable <id|name>`: the
// switch that starts spending, so it is its own verb. The first fire is
// computed here, from now, in the row's zone.
func runScheduleEnable(fl cliFlags, args []string) error {
	env, err := openScheduleEnv()
	if err != nil {
		return err
	}
	defer env.cleanup()
	ctx := context.Background()
	arg, aerr := needOneArg("enable", args)
	if aerr != nil {
		return aerr
	}
	id, err := resolveScheduleArg(ctx, env.store, arg)
	if err != nil {
		return err
	}
	sc, err := env.store.Schedule(ctx, id)
	if err != nil {
		return err
	}
	next, err := schedule.NextRun(sc.Cron, sc.Timezone, time.Now())
	if err != nil {
		return err
	}
	if err := env.store.SetScheduleEnabled(ctx, id, true, next); err != nil {
		return err
	}
	if fl.Bool("json") {
		b, err := json.MarshalIndent(ui.WebSchedule(mustSchedule(env, ctx, id)), "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, string(b))
		return err
	}
	fmt.Printf("  schedule %s enabled — first fire %s\n", id, next.Format(time.DateTime))
	return nil
}

// runScheduleDisable implements `gummi schedule disable <id|name>`.
func runScheduleDisable(fl cliFlags, args []string) error {
	env, err := openScheduleEnv()
	if err != nil {
		return err
	}
	defer env.cleanup()
	ctx := context.Background()
	arg, aerr := needOneArg("disable", args)
	if aerr != nil {
		return aerr
	}
	id, err := resolveScheduleArg(ctx, env.store, arg)
	if err != nil {
		return err
	}
	if err := env.store.SetScheduleEnabled(ctx, id, false, time.Time{}); err != nil {
		return err
	}
	if fl.Bool("json") {
		b, err := json.MarshalIndent(ui.WebSchedule(mustSchedule(env, ctx, id)), "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, string(b))
		return err
	}
	fmt.Printf("  schedule %s disabled\n", id)
	return nil
}

// runScheduleRunNow implements `gummi schedule run-now <id|name>`: a
// fire off-cadence, served by the running board's next tick. A disabled
// schedule is refused — enable it first; the one forced fire of a
// disabled row is a person at the board (the TUI's or the web page's
// run-now), which can see the row it is starting.
func runScheduleRunNow(fl cliFlags, args []string) error {
	env, err := openScheduleEnv()
	if err != nil {
		return err
	}
	defer env.cleanup()
	ctx := context.Background()
	arg, aerr := needOneArg("run-now", args)
	if aerr != nil {
		return aerr
	}
	id, err := resolveScheduleArg(ctx, env.store, arg)
	if err != nil {
		return err
	}
	if err := env.store.RequestScheduleRun(ctx, id); err != nil {
		return err
	}
	if fl.Bool("json") {
		sc, _ := env.store.Schedule(ctx, id)
		b, err := json.MarshalIndent(ui.WebSchedule(sc), "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, string(b))
		return err
	}
	if release, lerr := state.AcquireLock(env.ws.LockFile()); lerr == nil {
		// The lock was free: no board is up, so nothing will serve the
		// request until one is.
		release()
		fmt.Printf("  schedule %s will fire on the next tick of a running board — none is running now\n", id)
		return nil
	}
	fmt.Printf("  schedule %s fires on the running board's next tick\n", id)
	return nil
}

// runScheduleRm implements `gummi schedule rm <id|name>`: the row goes;
// the cards it minted stay on the board like any freeform card.
func runScheduleRm(args []string) error {
	env, err := openScheduleEnv()
	if err != nil {
		return err
	}
	defer env.cleanup()
	arg, aerr := needOneArg("rm", args)
	if aerr != nil {
		return aerr
	}
	id, rerr := resolveScheduleArg(context.Background(), env.store, arg)
	if rerr != nil {
		return rerr
	}
	if err := env.store.DeleteSchedule(context.Background(), id); err != nil {
		return err
	}
	fmt.Printf("  schedule %s deleted — the cards it minted stay\n", id)
	return nil
}

func mustSchedule(env *scheduleEnv, ctx context.Context, id domain.ScheduleID) domain.Schedule {
	sc, err := env.store.Schedule(ctx, id)
	if err != nil {
		// unreachable: the enable/disable that just succeeded read the row
		return domain.Schedule{}
	}
	return sc
}

// needOneArg is the one-argument check every schedule verb shares.
func needOneArg(verb string, args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("schedule %s needs exactly one argument: the schedule's id or name", verb)
	}
	return args[0], nil
}
