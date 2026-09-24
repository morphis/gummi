// Package hooks runs user-configured scripts when the board changes —
// the script surface beside the bell/desktop notifier (DESIGN §4.2's
// needs-attention hooks), driven by the same events from the inside out.
// A hook is a shell command line (config.yaml's `hooks:` list) run on
// one event, with the event in the environment and a JSON payload on
// stdin:
//
//	hooks:
//	  - run: ~/bin/gummi-notify "$1"       # every event
//	  - run: page-oncall.sh "$1"
//	    events: [gate.waiting, budget.exhausted]
//
// The `run:` line is a shell command line, not a program name: it is fed
// to `sh -c`, where the event name is "$1". A line that names a script
// and nothing else therefore hands that script no arguments — forward
// "$1" (as above) if the script wants it as argv[1]. GUMMI_EVENT carries
// the same name and needs no forwarding.
//
// The event vocabulary is closed (see the Event* constants). Two facts
// shape the contract:
//
//   - Hooks are advisory. A hook's exit status and output are none of
//     gummi's business: scripts run detached from the caller's path,
//     never block it (the dispatcher is a bounded queue with one worker;
//     a full queue drops, counted), and never fail the run that raised
//     the event. Stdout is discarded — scripts that need a trail write
//     their own. Advisory is not silent, though: a run that fails, and
//     an event that was never delivered, are counted and reported in one
//     line at Close, because a hook nobody can see failing is a hook
//     nobody can fix.
//   - Hooks fire where the event is committed. The store reports
//     committed card events (Transitions, parks, decisions, creations)
//     and the worktree layer reports squash-merges; a hook therefore
//     fires from whichever process performed the write, exactly once per
//     committed row (the store's own dedupe keys are honored — a
//     decision re-raise that deduped to a no-op raises no hook).
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// The event vocabulary: the closed set of names a hook's `events:` filter
// may name and that arrive as argv[1]. Attention events (waiting, parked,
// failed, exhausted) are the notification surface; the rest are
// milestones. Nothing session-shaped (messages, tool calls, critiques)
// is hooked — that volume belongs to `run --verbose`'s NDJSON stream.
const (
	// EventCardCreated: a card was minted (creation, ingest, bugs import).
	EventCardCreated = "card.created"
	// EventStageEnter: a card crossed a stage edge (forward or rerun);
	// from/to/actor name the edge.
	EventStageEnter = "stage.enter"
	// EventCardVerified: the card reached done with a verified branch —
	// the landing moment, merge or hand-off alike.
	EventCardVerified = "card.verified"
	// EventCardParked: the card stopped and waits (reason/detail say why:
	// needs-you, gave-up, blocked, quit).
	EventCardParked = "card.parked"
	// EventCardMerged: the card's branch was squash-merged onto its base;
	// commit is the landed sha.
	EventCardMerged = "card.merged"
	// EventGateWaiting: the card blocked on a design-gate approval.
	EventGateWaiting = "gate.waiting"
	// EventAskWaiting: the card's agent asked a question and awaits an
	// answer.
	EventAskWaiting = "question.waiting"
	// EventBudgetGone: the card's envelope is spent.
	EventBudgetGone = "budget.exhausted"
	// EventCardFailed: the card hit a failing verify, a rebase conflict,
	// or an idle stop; decision_kind distinguishes (verify|conflict|idle).
	EventCardFailed = "card.failed"
)

// AllEvents is the whole vocabulary, for validation and doctor's render.
var AllEvents = []string{
	EventCardCreated, EventStageEnter, EventCardVerified, EventCardParked,
	EventCardMerged, EventGateWaiting, EventAskWaiting, EventBudgetGone,
	EventCardFailed,
}

// ValidEvent reports whether name is in the closed vocabulary, so a
// config typo fails at load rather than silencing a hook forever.
func ValidEvent(name string) bool {
	for _, ev := range AllEvents {
		if name == ev {
			return true
		}
	}
	return false
}

// Hook is one configured script: a shell command run per matching event.
// An empty events filter matches every event.
type Hook struct {
	// Run is the command, executed via `sh -c` in the workspace root.
	// Operator config, not agent or repo input — it may use quoting,
	// pipes, whatever the shell allows.
	Run string `yaml:"run"`
	// Events filters which events fire this hook; empty means all.
	Events []string `yaml:"events,omitempty"`
}

// matches reports whether the hook wants the event.
func (h Hook) matches(ev string) bool {
	if len(h.Events) == 0 {
		return true
	}
	for _, e := range h.Events {
		if e == ev {
			return true
		}
	}
	return false
}

// Features is the enrichment source the dispatcher reads to fill a
// payload's card fields (kind, title, branch). *state.Store satisfies it.
type Features interface {
	GetFeature(ctx context.Context, id domain.FeatureID) (domain.Feature, error)
}

// Payload is the JSON object a hook receives on stdin (and the shape of
// the fields exported as GUMMI_* env). Flat on purpose — jq-friendlier
// than nested envelopes — and every field but `event` is omitempty, so
// each event carries only what it knows.
type Payload struct {
	Event string `json:"event"`
	// Workspace is the absolute workspace root (where .gummi lives); also
	// exported as GUMMI_WORKSPACE.
	Workspace string `json:"workspace,omitempty"`
	// ID is the card ("FD-004"); empty on workspace-wide events. Also
	// GUMMI_CARD.
	ID string `json:"id,omitempty"`
	// CardKind is the card's kind: feature, bug, research or goal.
	CardKind string `json:"card_kind,omitempty"`
	// Title is the card's title, at enrichment time.
	Title string `json:"title,omitempty"`
	// Stage is the stage the event happened in (for stage.enter: the
	// stage entered).
	Stage string `json:"stage,omitempty"`
	// Repo is the name of the managed repository the card belongs to, on
	// a workspace that manages several ("" for the default one). The
	// script's own cwd is always the workspace root, so this is what
	// tells a multi-repo hook which tree the card's branch is in.
	Repo string `json:"repo,omitempty"`
	// Branch is the card's branch name, when it has one.
	Branch string `json:"branch,omitempty"`
	// At is the event's commit time (RFC3339).
	At string `json:"at,omitempty"`
	// From/To/Actor are stage.enter's edge and who crossed it.
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	Actor string `json:"actor,omitempty"`
	// Decision is the decision id of a waiting event, for correlating
	// with `gummi status`'s decisions.
	Decision string `json:"decision,omitempty"`
	// DecisionKind is the waiting decision's kind (gate, ask, verify,
	// conflict, budget, idle) — how the card is stopped.
	DecisionKind string `json:"decision_kind,omitempty"`
	// Question is what a waiting decision asked, verbatim.
	Question string `json:"question,omitempty"`
	// Reason/Detail are card.parked's why: the park reason and the
	// sentence the user was shown.
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Commit is card.merged's landed squash-commit sha.
	Commit string `json:"commit,omitempty"`
}

// hookTimeout bounds one hook script's run. A hung script must never hold
// the worker longer than this; the kill takes the script's whole process
// group, the same treatment agent children get.
const hookTimeout = 15 * time.Second

// queueCap bounds the dispatcher's queue. Overflow drops (counted): the
// alternative — blocking the store write that raised the event — trades
// a notification for a stall, backwards for an advisory surface.
const queueCap = 64

// drainBudget bounds Close's drain as a whole, however deep the queue is.
// Per-script timeouts alone do not bound it: queueCap hung scripts at
// hookTimeout each would hold an exiting process for minutes after its
// own output ended — a stall traded for notifications nobody is waiting
// on any more. Whatever the budget does not cover is abandoned, counted,
// and reported.
const drainBudget = hookTimeout

// stderrTail bounds how much of a failing script's stderr is kept for the
// summary line. Enough to carry "not found" or a stack's first line;
// short enough that a chatty script cannot grow the dispatcher.
const stderrTail = 200

// Dispatcher runs a config's hooks against the board's events. The zero
// use is fine: all methods are nil-safe no-ops, so a workspace with no
// hooks configured wires up without conditionals.
type Dispatcher struct {
	ws    string
	hooks []Hook
	feats Features

	// hookTimeout bounds one script's run and drainBudget bounds Close's
	// whole drain; the consts of the same names are the production
	// defaults. Fields so tests can shrink them.
	hookTimeout time.Duration
	drainBudget time.Duration

	// warn receives the one-line summary Close prints when something was
	// lost or failed; nil means os.Stderr.
	warn io.Writer

	queue chan Payload
	done  chan struct{}
	wg    sync.WaitGroup

	// exiting is cancelled once Close's budget is spent, killing whatever
	// script is still running; deadline is that same instant, shared so
	// the worker's drain and the running script measure from one clock.
	exiting    context.Context
	stopExit   context.CancelFunc
	deadlineNS atomic.Int64

	closeOnce sync.Once
	dropped   atomic.Int64
	abandoned atomic.Int64
	failed    atomic.Int64

	// lastFail holds the most recent failure's one-line description, for
	// the summary. Written by the worker, read by Close and Failures.
	mu       sync.Mutex
	lastFail string
}

// New builds a dispatcher for cfg's hooks, running scripts in ws (the
// workspace root) and enriching card payloads from feats (the store; nil
// skips enrichment). No hooks configured → a nil dispatcher, whose
// methods are all no-ops.
func New(cfg []Hook, ws string, feats Features) *Dispatcher {
	if len(cfg) == 0 {
		return nil
	}
	d := &Dispatcher{
		ws:          ws,
		hooks:       append([]Hook(nil), cfg...),
		feats:       feats,
		hookTimeout: hookTimeout,
		drainBudget: drainBudget,
		queue:       make(chan Payload, queueCap),
		done:        make(chan struct{}),
	}
	d.exiting, d.stopExit = context.WithCancel(context.Background())
	d.wg.Add(1)
	go d.run()
	return d
}

// Observe implements the store's Observer: translate one committed card
// event to its hook event and queue it. Non-blocking by contract — a full
// queue drops the event (counted) rather than stalling the store write.
func (d *Dispatcher) Observe(ev state.CardEvent) {
	p, ok := d.translate(ev)
	if !ok {
		return
	}
	d.enqueue(p)
}

// Merged reports a squash-merge landing (the worktree layer's hook). f is
// already enriched — the manager has it in hand — so no store read is
// needed.
func (d *Dispatcher) Merged(f *domain.Feature, commit string) {
	if d == nil || f == nil {
		return
	}
	d.enqueue(Payload{
		Event:     EventCardMerged,
		Workspace: d.ws,
		ID:        string(f.ID),
		CardKind:  string(f.Kind),
		Title:     f.Title,
		Branch:    f.BranchName(),
		Stage:     string(f.Stage),
		Repo:      f.Repo,
		Commit:    commit,
		At:        time.Now().UTC().Format(time.RFC3339),
	})
}

// Close stops the dispatcher: it stops accepting work and drains what is
// already queued, bounded as a whole by drainBudget, then reports in one
// line whatever was lost or failed. A script already running when Close
// is called still gets its own timeout — it cannot be un-started — so the
// worst case is one hookTimeout plus the budget, not the queue's depth
// times either. Safe to call more than once and on a nil dispatcher.
func (d *Dispatcher) Close() {
	if d == nil {
		return
	}
	d.closeOnce.Do(func() {
		// One deadline for the whole exit, fixed here rather than where
		// the worker notices: a script already running would otherwise
		// push the drain's own clock out by its full timeout, and the
		// budget would buy twice what it says.
		deadline := time.Now().Add(d.budget())
		d.deadlineNS.Store(deadline.UnixNano())
		var timer *time.Timer
		if d.stopExit != nil {
			timer = time.AfterFunc(time.Until(deadline), d.stopExit)
		}
		close(d.done)
		d.wg.Wait()
		if timer != nil {
			timer.Stop()
			d.stopExit() // release the context, timer fired or not
		}
	})
	d.wg.Wait()
	d.report()
}

// Dropped reports how many events were dropped because the queue was
// full — one of the two ways an advisory surface can lose an event.
func (d *Dispatcher) Dropped() int64 {
	if d == nil {
		return 0
	}
	return d.dropped.Load()
}

// Abandoned reports how many queued events Close's drain budget ran out
// on — the other way one is lost, and the price of not stalling an
// exiting process behind a hung script.
func (d *Dispatcher) Abandoned() int64 {
	if d == nil {
		return 0
	}
	return d.abandoned.Load()
}

// Failed reports how many hook script runs ended badly — a non-zero exit,
// a missing interpreter, a kill at the timeout.
func (d *Dispatcher) Failed() int64 {
	if d == nil {
		return 0
	}
	return d.failed.Load()
}

// SetWarn redirects the summary Close prints (default os.Stderr). Call it
// before any event is queued; tests use it to read the summary back.
func (d *Dispatcher) SetWarn(w io.Writer) {
	if d == nil {
		return
	}
	d.warn = w
}

// report prints the one line that makes an advisory surface accountable:
// what failed, and what never ran. Silence means every hook ran and every
// event reached it.
func (d *Dispatcher) report() {
	var parts []string
	if n := d.Failed(); n > 0 {
		d.mu.Lock()
		last := d.lastFail
		d.mu.Unlock()
		parts = append(parts, fmt.Sprintf("%d script %s failed (last: %s)", n, plural(n, "run"), last))
	}
	if n := d.Dropped(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s dropped (queue full)", n, plural(n, "event")))
	}
	if n := d.Abandoned(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d queued %s abandoned at exit (%s drain budget)",
			n, plural(n, "event"), d.budget()))
	}
	if len(parts) == 0 {
		return
	}
	w := d.warn
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintln(w, "gummi: hooks: "+strings.Join(parts, "; "))
}

// plural renders "1 run" / "2 runs" for the summary.
func plural(n int64, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// noteFailure records one bad run for the summary.
func (d *Dispatcher) noteFailure(h Hook, ev string, err error, stderr []byte) {
	d.failed.Add(1)
	msg := h.Run + " on " + ev + ": " + err.Error()
	if tail := strings.TrimSpace(string(stderr)); tail != "" {
		msg += ": " + strings.ReplaceAll(tail, "\n", " ")
	}
	d.mu.Lock()
	d.lastFail = msg
	d.mu.Unlock()
}

// enqueue offers one payload; a full queue drops it, counted. It never
// blocks and never panics after Close (the send just fills the abandoned
// buffer, which nobody reads).
func (d *Dispatcher) enqueue(p Payload) {
	if d == nil {
		return
	}
	select {
	case d.queue <- p:
	default:
		d.dropped.Add(1)
	}
}

// run is the single worker: it fires queued events in order until Close,
// then drains what is left under one budget. Closing is checked before
// each event, not only when the queue runs dry: a deep queue would
// otherwise keep the worker in its normal path, and a process trying to
// exit would wait out a full timeout per hung script rather than one
// budget for all of them.
func (d *Dispatcher) run() {
	defer d.wg.Done()
	for {
		select {
		case <-d.done:
			d.drain(d.exitDeadline())
			return
		default:
		}
		select {
		case p := <-d.queue:
			d.fire(p, time.Time{})
		case <-d.done:
			d.drain(d.exitDeadline())
			return
		}
	}
}

// exitDeadline is the instant Close's budget runs out — the one clock the
// drain and any still-running script both measure against.
func (d *Dispatcher) exitDeadline() time.Time {
	if ns := d.deadlineNS.Load(); ns > 0 {
		return time.Unix(0, ns)
	}
	return time.Now().Add(d.budget())
}

// budget is the drain allowance. A hand-built dispatcher (tests) leaves
// the field zero; the const is what production means by "the budget".
func (d *Dispatcher) budget() time.Duration {
	if d.drainBudget <= 0 {
		return drainBudget
	}
	return d.drainBudget
}

// drain empties the queue after Close, firing what fits before deadline
// and counting the rest abandoned.
func (d *Dispatcher) drain(deadline time.Time) {
	for {
		select {
		case p := <-d.queue:
			d.fire(p, deadline)
		default:
			return
		}
	}
}

// fire runs every hook matching p, in config order. Enrichment happens
// here — the worker goroutine, never the store's write path. A non-zero
// deadline is Close's drain budget: an event the budget no longer covers
// is abandoned whole, so the count stays in events rather than scripts.
func (d *Dispatcher) fire(p Payload, deadline time.Time) {
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		d.abandoned.Add(1)
		return
	}
	d.enrich(&p)
	body, err := json.Marshal(p)
	if err != nil {
		return
	}
	for _, h := range d.hooks {
		if !h.matches(p.Event) {
			continue
		}
		d.exec(h, p.Event, p.ID, body, deadline)
	}
}

// enrich fills the card fields from the store. Best-effort: a card that
// cannot be read (deleted, concurrent churn) keeps the event's own
// fields and ships without enrichment.
func (d *Dispatcher) enrich(p *Payload) {
	if d.feats == nil || p.ID == "" {
		p.Workspace = d.ws
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if f, err := d.feats.GetFeature(ctx, domain.FeatureID(p.ID)); err == nil {
		if p.CardKind == "" {
			p.CardKind = string(f.Kind)
		}
		if p.Title == "" {
			p.Title = f.Title
		}
		if p.Branch == "" {
			p.Branch = f.BranchName()
		}
		if p.Stage == "" {
			p.Stage = string(f.Stage)
		}
		if p.Repo == "" {
			p.Repo = f.Repo
		}
	}
	p.Workspace = d.ws
}

// exec runs one hook script for one event: `sh -c <run> gummi-hook
// <event>` in the workspace root, JSON on stdin, the identity in the
// environment. The event is the shell's "$1" (and "gummi-hook" its $0),
// so a run line reaches its own argv only by forwarding it — see the
// package doc. No failure here reaches the caller; it is counted and
// named in Close's summary instead.
//
// deadline, when non-zero, is Close's drain budget: the script's own
// timeout is clamped to what is left of it, so the last event in a queue
// cannot add a full hookTimeout to an exiting process.
func (d *Dispatcher) exec(h Hook, ev, card string, body []byte, deadline time.Time) {
	timeout := d.hookTimeout
	if !deadline.IsZero() {
		if left := time.Until(deadline); left < timeout {
			timeout = left
		}
	}
	if timeout <= 0 {
		return
	}
	// Parented on the exit context so a script that was already running
	// when Close came is killed with the rest when the budget is spent —
	// it keeps its own timeout until then, so a slow pager fired by the
	// last event still gets those seconds to reach anyone.
	parent := d.exiting
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", h.Run, "gummi-hook", ev) //nolint:gosec // run is operator config (hooks: in config.yaml), not agent/repo input
	cmd.Dir = d.ws
	cmd.Env = append(os.Environ(),
		"GUMMI_EVENT="+ev,
		"GUMMI_WORKSPACE="+d.ws,
		"GUMMI_CARD="+card,
	)
	// Run in its own process group and kill the group on cancel: a script
	// that spawned children (a notifier daemon, a curl) must not orphan
	// them behind the timeout. Mirrors the headless agent's teardown.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = bytes.NewReader(body)
	// Stdout is the script's business and goes nowhere — a hook must never
	// interleave with the NDJSON stream or the TUI's render surface. A
	// bounded tail of stderr is kept, and only to name the failure in
	// Close's summary.
	cmd.Stdout = nil
	errTail := &tailBuffer{max: stderrTail}
	cmd.Stderr = errTail
	if err := cmd.Run(); err != nil {
		d.noteFailure(h, ev, err, errTail.bytes())
	}
}

// tailBuffer keeps the last max bytes written to it: a failing script's
// stderr tail, bounded so a chatty one cannot grow the dispatcher.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

// bytes returns the kept tail. Safe to call once cmd.Run has returned —
// exec.Cmd has joined its copying goroutine by then.
func (t *tailBuffer) bytes() []byte { return t.buf }

// translate maps one committed card event to its hook payload. The event
// kinds are the store's log vocabulary plus its two observer-only
// synthetics (created, verified); anything else (messages, tool calls,
// stage_enter runs, ask answers) is not hookable and returns false.
// Payload decode failures leave the specific fields empty — the event
// still fires with its identity, because a notification that knows the
// card beats one that knows nothing.
func (d *Dispatcher) translate(ev state.CardEvent) (Payload, bool) {
	p := Payload{
		ID:    string(ev.Feature),
		Stage: string(ev.Stage),
		At:    ev.At.UTC().Format(time.RFC3339),
	}
	switch ev.Kind {
	case state.EventCreated:
		p.Event = EventCardCreated
	case state.EventVerified:
		p.Event = EventCardVerified
	case state.EventGate:
		var gp state.GatePayload
		_ = json.Unmarshal([]byte(ev.Payload), &gp)
		p.Event = EventStageEnter
		p.From, p.To, p.Actor = gp.From, gp.To, gp.Actor
		p.Stage = gp.To
	case state.EventPark:
		var pp state.ParkPayload
		_ = json.Unmarshal([]byte(ev.Payload), &pp)
		p.Event = EventCardParked
		p.Reason, p.Detail = pp.Reason, pp.Detail
	case state.EventDecisionOpen:
		var dp state.DecisionPayload
		_ = json.Unmarshal([]byte(ev.Payload), &dp)
		p.Decision, p.DecisionKind, p.Question = dp.ID, dp.Kind, dp.Question
		switch dp.Kind {
		case state.DecisionKindGate:
			p.Event = EventGateWaiting
		case state.DecisionKindAsk:
			p.Event = EventAskWaiting
		case state.DecisionKindBudget:
			p.Event = EventBudgetGone
		default:
			// verify, conflict, idle — and anything a later kind adds —
			// are all "the card stopped on a failure-shaped decision".
			p.Event = EventCardFailed
		}
	default:
		return Payload{}, false
	}
	return p, true
}

// Describe renders one hook line for doctor's config report.
func (h Hook) Describe() string {
	if len(h.Events) == 0 {
		return h.Run + " (all events)"
	}
	return fmt.Sprintf("%s (%s)", h.Run, strings.Join(h.Events, ", "))
}
