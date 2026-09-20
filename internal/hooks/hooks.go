// Package hooks runs user-configured scripts when the board changes —
// the script surface beside the bell/desktop notifier (DESIGN §4.2's
// needs-attention hooks), driven by the same events from the inside out.
// A hook is a shell command (config.yaml's `hooks:` list) run on one
// event with the event name as argv[1] and a JSON payload on stdin:
//
//	hooks:
//	  - run: ~/bin/gummi-notify            # every event
//	  - run: page-oncall.sh
//	    events: [gate.waiting, budget.exhausted]
//
// The event vocabulary is closed (see the Event* constants). Two facts
// shape the contract:
//
//   - Hooks are advisory. A hook's exit status, output and very life are
//     none of gummi's business: scripts run detached from the caller's
//     path, never block it (the dispatcher is a bounded queue with one
//     worker; a full queue drops, counted), and never fail the run that
//     raised the event. A hook's output is discarded — scripts that need
//     a trail write their own.
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

// Dispatcher runs a config's hooks against the board's events. The zero
// use is fine: all methods are nil-safe no-ops, so a workspace with no
// hooks configured wires up without conditionals.
type Dispatcher struct {
	ws    string
	hooks []Hook
	feats Features

	// hookTimeout bounds one script's run; the hookTimeout const is the
	// production default. A field so tests can shrink it.
	hookTimeout time.Duration

	queue chan Payload
	done  chan struct{}
	wg    sync.WaitGroup

	closeOnce sync.Once
	dropped   atomic.Int64
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
		queue:       make(chan Payload, queueCap),
		done:        make(chan struct{}),
	}
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
		Commit:    commit,
		At:        time.Now().UTC().Format(time.RFC3339),
	})
}

// Close stops the dispatcher: it stops accepting work and drains what is
// already queued, bounding each remaining script by hookTimeout. Safe to
// call more than once and on a nil dispatcher.
func (d *Dispatcher) Close() {
	if d == nil {
		return
	}
	d.closeOnce.Do(func() { close(d.done) })
	d.wg.Wait()
}

// Dropped reports how many events were dropped because the queue was
// full — the one way an advisory surface can lose an event.
func (d *Dispatcher) Dropped() int64 {
	if d == nil {
		return 0
	}
	return d.dropped.Load()
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

// run is the single worker: it drains the queue in order, and after
// Close keeps draining what is already queued before exiting.
func (d *Dispatcher) run() {
	defer d.wg.Done()
	for {
		select {
		case p := <-d.queue:
			d.fire(p)
		default:
			select {
			case p := <-d.queue:
				d.fire(p)
			case <-d.done:
				for {
					select {
					case p := <-d.queue:
						d.fire(p)
					default:
						return
					}
				}
			}
		}
	}
}

// fire runs every hook matching p, in config order. Enrichment happens
// here — the worker goroutine, never the store's write path.
func (d *Dispatcher) fire(p Payload) {
	d.enrich(&p)
	body, err := json.Marshal(p)
	if err != nil {
		return
	}
	for _, h := range d.hooks {
		if !h.matches(p.Event) {
			continue
		}
		d.exec(h, p.Event, body)
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
	}
	p.Workspace = d.ws
}

// exec runs one hook script for one event: `sh -c <run> gummi-hook
// <event>` in the workspace root, JSON on stdin, the identity in the
// environment. Every failure is swallowed here — advisory end to end.
func (d *Dispatcher) exec(h Hook, ev string, body []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), d.hookTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", h.Run, "gummi-hook", ev) //nolint:gosec // run is operator config (hooks: in config.yaml), not agent/repo input
	cmd.Dir = d.ws
	cmd.Env = append(os.Environ(),
		"GUMMI_EVENT="+ev,
		"GUMMI_WORKSPACE="+d.ws,
		"GUMMI_CARD="+gummiCardOf(body),
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
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Run() // advisory: exit status and output are the script's business
}

// gummiCardOf re-reads the payload's id for the GUMMI_CARD env var. The
// body is the same struct just marshaled; a decode failure (cannot
// happen for our own marshal) yields an empty value.
func gummiCardOf(body []byte) string {
	var p Payload
	if err := json.Unmarshal(body, &p); err != nil {
		return ""
	}
	return p.ID
}

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

// EventsFor renders a hook's filter for doctor ("" = all events).
func (h Hook) Filter() string {
	return strings.Join(h.Events, ",")
}

// Describe renders one hook line for doctor's config report.
func (h Hook) Describe() string {
	if len(h.Events) == 0 {
		return h.Run + " (all events)"
	}
	return fmt.Sprintf("%s (%s)", h.Run, strings.Join(h.Events, ", "))
}
