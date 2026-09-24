package hooks

// The dispatcher's contract, pinned here: a run line is a shell command
// line (the event is the shell's "$1", forwarded or not) fed the JSON
// payload on stdin, in the workspace root, with the identity in the
// environment; filters narrow; overflow drops and the exit budget
// abandons, both counted and reported; timeouts kill the process group;
// Close drains under one clock; nil is inert.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// waitFor polls until read returns non-empty or the deadline passes.
func waitFor(t *testing.T, read func() []byte, what string) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b := read(); len(b) > 0 {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
	return nil
}

// recordingHook returns a hook script that appends one line per run —
// argv[1], GUMMI_EVENT, GUMMI_CARD, the cwd — to out, plus the raw stdin
// body beside it.
func recordingHook(out string) Hook {
	return Hook{Run: `printf '%s|%s|%s|%s\n' "$1" "$GUMMI_EVENT" "$GUMMI_CARD" "$(pwd)" >> ` + out +
		`; cat > ` + out + `.json`}
}

// mustJSON marshals v or fails the test — payload JSON in a test input is
// never allowed to be silently empty.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func readLine(t *testing.T, path string) string {
	t.Helper()
	b := waitFor(t, func() []byte {
		b, _ := os.ReadFile(path)
		return b
	}, path)
	return strings.TrimSpace(string(b))
}

func testDispatcher(t *testing.T, hooks []Hook) *Dispatcher {
	t.Helper()
	return New(hooks, t.TempDir(), nil)
}

func TestDispatcherRunsScript(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "runs.log")
	d := New([]Hook{recordingHook(out)}, dir, nil)
	defer d.Close()

	d.Observe(state.CardEvent{
		Feature: "FD-004", Stage: "verify", Kind: state.EventPark,
		Payload: mustJSON(t, state.ParkPayload{Reason: state.ParkReasonNeedsYou, Detail: "verify failed"}),
		At:      time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC),
	})

	line := readLine(t, out)
	parts := strings.Split(line, "|")
	if len(parts) != 4 {
		t.Fatalf("recording line = %q, want event|event|card|cwd", line)
	}
	if parts[0] != EventCardParked || parts[1] != EventCardParked {
		t.Errorf("argv[1]=%q GUMMI_EVENT=%q, want both %q", parts[0], parts[1], EventCardParked)
	}
	if parts[2] != "FD-004" {
		t.Errorf("GUMMI_CARD = %q, want FD-004", parts[2])
	}
	if !strings.HasSuffix(parts[3], dir) {
		t.Errorf("cwd = %q, want the workspace root %q", parts[3], dir)
	}

	var p Payload
	body, err := os.ReadFile(out + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Event != EventCardParked || p.ID != "FD-004" || p.Reason != state.ParkReasonNeedsYou || p.Stage != "verify" {
		t.Errorf("payload = %+v", p)
	}
	if p.Workspace != dir {
		t.Errorf("workspace = %q, want %q", p.Workspace, dir)
	}
}

func TestDispatcherFilter(t *testing.T) {
	dir := t.TempDir()
	all := filepath.Join(dir, "all.log")
	narrow := filepath.Join(dir, "narrow.log")
	d := New([]Hook{
		{Run: `echo "$1" >> ` + all},
		{Run: `echo "$1" >> ` + narrow, Events: []string{EventGateWaiting}},
	}, dir, nil)
	defer d.Close()

	d.Observe(state.CardEvent{Feature: "FD-001", Stage: "todo", Kind: state.EventCreated, At: time.Now()})
	d.Observe(state.CardEvent{
		Feature: "FD-001", Stage: "todo", Kind: state.EventDecisionOpen, At: time.Now(),
		Payload: mustJSON(t, state.DecisionPayload{ID: "d-1", Kind: state.DecisionKindGate, Question: "approve?"}),
	})

	if got := readLine(t, narrow); got != EventGateWaiting {
		t.Errorf("narrow hook saw %q, want only %q", got, EventGateWaiting)
	}
	b, _ := os.ReadFile(all)
	if lines := strings.Count(strings.TrimSpace(string(b)), "\n") + 1; lines != 2 {
		t.Errorf("unfiltered hook ran %d times, want 2 (body %q)", lines, b)
	}
}

func TestTranslateMapsDecisionKinds(t *testing.T) {
	d := &Dispatcher{}
	cases := []struct {
		kind string
		want string
	}{
		{state.DecisionKindGate, EventGateWaiting},
		{state.DecisionKindAsk, EventAskWaiting},
		{state.DecisionKindBudget, EventBudgetGone},
		{state.DecisionKindVerify, EventCardFailed},
		{state.DecisionKindConflict, EventCardFailed},
		{state.DecisionKindIdle, EventCardFailed},
	}
	for _, c := range cases {
		payload, _ := json.Marshal(state.DecisionPayload{ID: "d-9", Kind: c.kind, Question: "why"})
		p, ok := d.translate(state.CardEvent{
			Feature: "FD-002", Stage: "implement", Kind: state.EventDecisionOpen,
			Payload: string(payload), At: time.Now(),
		})
		if !ok {
			t.Fatalf("kind %q: translate said no", c.kind)
		}
		if p.Event != c.want {
			t.Errorf("decision kind %q → event %q, want %q", c.kind, p.Event, c.want)
		}
		if p.Decision != "d-9" || p.Question != "why" {
			t.Errorf("decision kind %q: payload = %+v", c.kind, p)
		}
	}
}

func TestTranslateGateAndPark(t *testing.T) {
	d := &Dispatcher{}
	gp, _ := json.Marshal(state.GatePayload{From: "verify", To: "done", Actor: "auto"})
	p, ok := d.translate(state.CardEvent{
		Feature: "FD-003", Kind: state.EventGate, Payload: string(gp), At: time.Now(),
	})
	if !ok || p.Event != EventStageEnter || p.From != "verify" || p.To != "done" || p.Actor != "auto" {
		t.Errorf("gate → %+v ok=%v", p, ok)
	}

	pp, _ := json.Marshal(state.ParkPayload{Reason: "gave-up", Detail: "rounds exhausted"})
	p, ok = d.translate(state.CardEvent{
		Feature: "FD-003", Kind: state.EventPark, Payload: string(pp), At: time.Now(),
	})
	if !ok || p.Event != EventCardParked || p.Reason != "gave-up" || p.Detail != "rounds exhausted" {
		t.Errorf("park → %+v ok=%v", p, ok)
	}
}

func TestTranslateSyntheticsAndSkipsNarration(t *testing.T) {
	d := &Dispatcher{}
	p, ok := d.translate(state.CardEvent{Feature: "FD-004", Kind: state.EventCreated, At: time.Now()})
	if !ok || p.Event != EventCardCreated {
		t.Errorf("created → %+v ok=%v", p, ok)
	}
	p, ok = d.translate(state.CardEvent{Feature: "FD-004", Kind: state.EventVerified, At: time.Now()})
	if !ok || p.Event != EventCardVerified {
		t.Errorf("verified → %+v ok=%v", p, ok)
	}
	for _, kind := range []string{state.EventMessage, state.EventTool, state.EventToolResult, state.EventStageEnter, state.EventAsk, state.EventAutopilot} {
		if _, ok := d.translate(state.CardEvent{Kind: kind, At: time.Now()}); ok {
			t.Errorf("kind %q reported as hookable", kind)
		}
	}
}

func TestDispatcherEnrichesFromFeatures(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "body.json")
	feats := stubFeatures{{
		f: domain.Feature{ID: "BG-007", Num: 7, Kind: domain.KindBug, Title: "Crash on empty input",
			Slug: "crash-on-empty-input", Stage: domain.StageImplement, BranchScheme: domain.BranchSchemeKind},
	}}
	d := New([]Hook{{Run: `cat > ` + out}}, dir, feats)
	defer d.Close()

	d.Observe(state.CardEvent{Feature: "BG-007", Stage: "implement", Kind: state.EventDecisionOpen, At: time.Now()})

	var p Payload
	body := waitFor(t, func() []byte {
		b, _ := os.ReadFile(out)
		return b
	}, "body.json")
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Title != "Crash on empty input" || p.CardKind != string(domain.KindBug) || p.Branch != "bug/crash-on-empty-input" {
		t.Errorf("enrichment missing: %+v", p)
	}
}

type stubFeatures []struct {
	f domain.Feature
}

func (s stubFeatures) GetFeature(_ context.Context, id domain.FeatureID) (domain.Feature, error) {
	for _, e := range s {
		if e.f.ID == id {
			return e.f, nil
		}
	}
	return domain.Feature{}, errNotFoundStub
}

var errNotFoundStub = jsonStubError("not found")

type jsonStubError string

func (e jsonStubError) Error() string { return string(e) }

func TestDispatcherDropsOnOverflow(t *testing.T) {
	dir := t.TempDir()
	d := &Dispatcher{
		ws:    dir,
		hooks: []Hook{{Run: "sleep 0.05"}},
		queue: make(chan Payload, 1),
		done:  make(chan struct{}),
	}
	d.SetWarn(io.Discard) // the drop summary is this test's expected outcome
	d.wg.Add(1)
	go d.run()

	for range 5 {
		d.enqueue(Payload{Event: EventStageEnter})
	}
	if got := d.Dropped(); got < 1 {
		t.Errorf("dropped = %d, want ≥1 (queue holds 1, worker busy)", got)
	}
	d.Close()
}

func TestDispatcherTimeoutKillsScript(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "late.log")
	d := &Dispatcher{
		ws:    dir,
		hooks: []Hook{{Run: `sleep 30; echo ran >> ` + out}},
		queue: make(chan Payload, 4),
		done:  make(chan struct{}),
	}
	d.hookTimeout = 150 * time.Millisecond
	d.SetWarn(io.Discard) // the kill is counted as a failure; that is the point
	d.wg.Add(1)
	go d.run()

	start := time.Now()
	d.enqueue(Payload{Event: EventStageEnter})
	d.Close() // waits for the worker; the killed script bounds it
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Close took %v; the hung script was not killed by the timeout", elapsed)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("the killed script still ran to completion")
	}
}

func TestDispatcherCloseDrainsQueue(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "drain.log")
	d := New([]Hook{{Run: `echo "$1" >> ` + out}}, dir, nil)
	for range 10 {
		d.enqueue(Payload{Event: EventCardCreated})
	}
	d.Close() // must drain what is queued, not abandon it
	b, _ := os.ReadFile(out)
	if lines := strings.Count(strings.TrimSpace(string(b)), "\n") + 1; lines != 10 {
		t.Errorf("drained %d of 10 events", lines)
	}
	// after Close, late events are neither panics nor runs
	d.enqueue(Payload{Event: EventStageEnter})
}

func TestNilDispatcherInert(t *testing.T) {
	var d *Dispatcher
	d.Observe(state.CardEvent{Kind: state.EventCreated, At: time.Now()})
	d.Merged(&domain.Feature{ID: "FD-001"}, "abc")
	d.Close()
	if d.Dropped() != 0 {
		t.Error("nil dispatcher dropped something impossible")
	}
}

func TestMergedPayload(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "body.json")
	d := New([]Hook{{Run: `cat > ` + out}}, dir, nil)
	defer d.Close()

	d.Merged(&domain.Feature{
		ID: "FD-009", Num: 9, Kind: domain.KindFeature, Title: "Dark mode",
		Slug: "dark-mode", Stage: domain.StageDone, BranchScheme: domain.BranchSchemeKind,
	}, "abc123")

	var p Payload
	body := waitFor(t, func() []byte {
		b, _ := os.ReadFile(out)
		return b
	}, "body.json")
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	if p.Event != EventCardMerged || p.ID != "FD-009" || p.Branch != "feat/dark-mode" || p.Commit != "abc123" {
		t.Errorf("merged payload = %+v", p)
	}
}

func TestHookDescribe(t *testing.T) {
	if got := (Hook{Run: "notify.sh"}).Describe(); got != "notify.sh (all events)" {
		t.Errorf("Describe() = %q", got)
	}
	if got := (Hook{Run: "page.sh", Events: []string{EventGateWaiting, EventBudgetGone}}).Describe(); got != "page.sh (gate.waiting, budget.exhausted)" {
		t.Errorf("Describe() = %q", got)
	}
}

func TestValidEvent(t *testing.T) {
	for _, ev := range AllEvents {
		if !ValidEvent(ev) {
			t.Errorf("ValidEvent(%q) = false, want true", ev)
		}
	}
	for _, bad := range []string{"", "card", "stage", "gate-waiting", "Card.Created"} {
		if ValidEvent(bad) {
			t.Errorf("ValidEvent(%q) = true, want false", bad)
		}
	}
}

// TestRunLineIsACommandLineNotAProgram pins the contract the docs make:
// `run:` is fed to `sh -c`, so the event is the *shell's* "$1" and a line
// naming a script and nothing else hands that script no arguments. The
// environment carries the event either way. This is the shape every
// documented example uses, and the one the older tests never covered.
func TestRunLineIsACommandLineNotAProgram(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s|%s\\n' \"$1\" \"$GUMMI_EVENT\" >> \"$OUT\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OUT", filepath.Join(dir, "bare.log"))

	bare := New([]Hook{{Run: script}}, dir, nil)
	bare.Observe(state.CardEvent{Feature: "FD-001", Kind: state.EventCreated, At: time.Now()})
	bare.Close()
	if got := readLine(t, filepath.Join(dir, "bare.log")); got != "|"+EventCardCreated {
		t.Errorf("a bare script line gave argv[1]=%q; want none, with the event only in the environment", got)
	}

	t.Setenv("OUT", filepath.Join(dir, "fwd.log"))
	fwd := New([]Hook{{Run: script + ` "$1"`}}, dir, nil)
	fwd.Observe(state.CardEvent{Feature: "FD-001", Kind: state.EventCreated, At: time.Now()})
	fwd.Close()
	if got := readLine(t, filepath.Join(dir, "fwd.log")); got != EventCardCreated+"|"+EventCardCreated {
		t.Errorf("a forwarding line gave %q, want the event in both argv[1] and the environment", got)
	}
}

// TestCloseDrainIsBounded pins the exit contract: a queue of hung scripts
// is bounded as a whole, not per script. Without it, queueCap hung hooks
// would hold an exiting process for queueCap × hookTimeout — minutes of
// stall after the run's own output ended.
func TestCloseDrainIsBounded(t *testing.T) {
	dir := t.TempDir()
	d := &Dispatcher{
		ws:          dir,
		hooks:       []Hook{{Run: "sleep 30"}},
		queue:       make(chan Payload, 16),
		done:        make(chan struct{}),
		hookTimeout: 5 * time.Second,
		drainBudget: 300 * time.Millisecond,
	}
	d.SetWarn(io.Discard)
	d.wg.Add(1)
	go d.run()

	for range 8 {
		d.enqueue(Payload{Event: EventStageEnter})
	}
	start := time.Now()
	d.Close()
	// 8 hung scripts at a 5s timeout each is 40s unbounded; the budget
	// caps it near 300ms (plus the kill's WaitDelay).
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Close took %v; the drain budget did not bound it", elapsed)
	}
	if d.Abandoned() == 0 {
		t.Error("nothing counted as abandoned; the budget ran out silently")
	}
}

// TestFailuresAreCountedAndReported pins that advisory is not silent: a
// hook that cannot run is counted, named with its stderr tail, and
// reported in one line at Close. A hook nobody can see failing is a hook
// nobody can fix.
func TestFailuresAreCountedAndReported(t *testing.T) {
	dir := t.TempDir()
	var summary bytes.Buffer
	d := New([]Hook{{Run: filepath.Join(dir, "not-there.sh")}}, dir, nil)
	d.SetWarn(&summary)
	d.Observe(state.CardEvent{Feature: "FD-001", Kind: state.EventCreated, At: time.Now()})
	d.Close()

	if d.Failed() != 1 {
		t.Fatalf("Failed() = %d, want 1", d.Failed())
	}
	got := summary.String()
	if !strings.Contains(got, "1 script run failed") || !strings.Contains(got, EventCardCreated) {
		t.Errorf("summary = %q, want it to name the failed run and its event", got)
	}
	if !strings.Contains(got, "not found") && !strings.Contains(got, "not-there.sh") {
		t.Errorf("summary = %q, want the shell's own complaint in it", got)
	}
}

// TestCleanRunReportsNothing is the other half: silence means every hook
// ran and every event reached it.
func TestCleanRunReportsNothing(t *testing.T) {
	dir := t.TempDir()
	var summary bytes.Buffer
	d := New([]Hook{{Run: "true"}}, dir, nil)
	d.SetWarn(&summary)
	d.Observe(state.CardEvent{Feature: "FD-001", Kind: state.EventCreated, At: time.Now()})
	d.Close()
	if got := summary.String(); got != "" {
		t.Errorf("a clean run printed %q, want silence", got)
	}
}

// TestPayloadCarriesRepo pins the multi-repo field: the script's cwd is
// always the workspace root, so the payload is what says which managed
// repository the card's branch lives in.
func TestPayloadCarriesRepo(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "body.json")
	feats := stubFeatures{{
		f: domain.Feature{ID: "FD-011", Num: 11, Kind: domain.KindFeature, Title: "Multi",
			Slug: "multi", Stage: domain.StageVerify, Repo: "lxd", BranchScheme: domain.BranchSchemeKind},
	}}
	d := New([]Hook{{Run: `cat > ` + out}}, dir, feats)
	defer d.Close()

	d.Observe(state.CardEvent{Feature: "FD-011", Stage: "verify", Kind: state.EventVerified, At: time.Now()})

	var p Payload
	if err := json.Unmarshal(waitFor(t, func() []byte {
		b, _ := os.ReadFile(out)
		return b
	}, "body.json"), &p); err != nil {
		t.Fatal(err)
	}
	if p.Repo != "lxd" || p.Event != EventCardVerified {
		t.Errorf("payload = %+v, want card.verified carrying repo lxd", p)
	}
}

// TestCloseBoundsARunningScript pins the other half of the exit bound: a
// script already running when Close arrives cannot be un-started, but it
// is still measured against the same budget, so exiting costs the budget
// once rather than a full per-script timeout on top of it.
func TestCloseBoundsARunningScript(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	d := &Dispatcher{
		ws:          dir,
		hooks:       []Hook{{Run: "touch " + started + "; sleep 30"}},
		queue:       make(chan Payload, 4),
		done:        make(chan struct{}),
		hookTimeout: 30 * time.Second,
		drainBudget: 300 * time.Millisecond,
	}
	d.exiting, d.stopExit = context.WithCancel(context.Background())
	d.SetWarn(io.Discard)
	d.wg.Add(1)
	go d.run()

	d.enqueue(Payload{Event: EventStageEnter})
	waitFor(t, func() []byte {
		b, _ := os.ReadFile(started)
		if _, err := os.Stat(started); err == nil {
			return []byte("x")
		}
		return b
	}, "the script to start")

	start := time.Now()
	d.Close()
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Close took %v; the running script outlived the budget", elapsed)
	}
}
