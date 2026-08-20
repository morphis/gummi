package driver

import (
	"encoding/json"
	"io"
	"strings"
	"sync"

	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/verifydoc"
)

// emitter serializes the run's event stream as NDJSON — one compact JSON
// object per line — to w. The default stream is milestones (created,
// stage, gate, done) plus decision boundaries (question, blocked,
// escalation, exhausted, timeout, error); verbose additionally emits
// per-tool-call activity lines. Writes are serialized so a concurrent
// activity line can't interleave with a milestone.
type emitter struct {
	mu      sync.Mutex
	w       io.Writer
	verbose bool
}

func newEmitter(w io.Writer, verbose bool) *emitter { return &emitter{w: w, verbose: verbose} }

// emit writes one event object as a line. A marshal error is dropped
// rather than aborting the run — the stream is advisory, the exit code is
// authoritative.
func (e *emitter) emit(v any) {
	line, err := json.Marshal(v)
	if err != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, _ = e.w.Write(append(line, '\n'))
}

// The event payloads. Every object carries an "event" discriminator and,
// where an item exists, its id. omitempty keeps optional fields off the
// wire so the default stream stays terse.

type createdEvent struct {
	Event    string `json:"event"`
	ID       string `json:"id"`
	Ref      string `json:"ref,omitempty"`
	Branch   string `json:"branch"`
	Route    string `json:"route"`
	Envelope int    `json:"envelope"`
}

type resumedEvent struct {
	Event string `json:"event"`
	ID    string `json:"id"`
	Ref   string `json:"ref,omitempty"`
	Stage string `json:"stage"`
}

// envelopeRaisedEvent reports a `resume --envelope N` top-up: the feature's
// credit budget went From→To before the parked stage re-ran. Emitted only
// when the new envelope actually exceeds the old one (a no-op raise is silent).
type envelopeRaisedEvent struct {
	Event string `json:"event"`
	ID    string `json:"id"`
	From  int    `json:"from"`
	To    int    `json:"to"`
}

type stageEvent struct {
	Event  string `json:"event"`
	ID     string `json:"id"`
	Stage  string `json:"stage"`
	Round  int    `json:"round,omitempty"`
	Result string `json:"result,omitempty"`
}

type gateEvent struct {
	Event    string `json:"event"`
	ID       string `json:"id"`
	From     string `json:"from"`
	To       string `json:"to"`
	Decision string `json:"decision"`
}

type questionEvent struct {
	Event       string   `json:"event"`
	ID          string   `json:"id"`
	Q           string   `json:"q"`
	Options     []string `json:"options,omitempty"`
	Recommended string   `json:"recommended,omitempty"`
	FreeForm    bool     `json:"free_form"`
	Resume      string   `json:"resume"`
	// Next is the copy-pasteable command that advances this stop — the exact
	// `gummi resume <id> …` verb for this event, so a caller driving the
	// stream never has to recall which flag a given stop takes. Free-form
	// values (an answer, a change note) appear as a <placeholder> to fill in.
	Next string `json:"next,omitempty"`
}

type gatePendingEvent struct {
	Event  string `json:"event"`
	ID     string `json:"id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Resume string `json:"resume"`
	Next   string `json:"next,omitempty"`
}

type blockedEvent struct {
	Event    string `json:"event"`
	ID       string `json:"id"`
	Gate     string `json:"gate"`
	OpenSpec int    `json:"open_questions,omitempty"`
	OpenDiff int    `json:"open_diff,omitempty"`
	// BlockingDeps names each outstanding dependency (its ID and current
	// stage) when a coding gate is held by an unmet dependency.
	BlockingDeps []engine.BlockingDep `json:"blocking_deps,omitempty"`
	// Document summarizes a research card's failing citation/coverage
	// floor when a StatusBlockedDocument gate holds the card at verify.
	Document *documentSummary `json:"document,omitempty"`
	Resume   string           `json:"resume"`
}

// documentSummary is the NDJSON-facing shape of a verifydoc.Report: counts
// only, so the stream stays compact — the full report is available in the
// TUI's spec view for anyone who needs the detail.
type documentSummary struct {
	OpenThreads int `json:"open_threads,omitempty"`
	Citations   int `json:"citations,omitempty"`
	Coverage    int `json:"coverage,omitempty"`
}

// newDocumentSummary converts an engine document report to its NDJSON
// summary.
func newDocumentSummary(r verifydoc.Report) *documentSummary {
	return &documentSummary{
		OpenThreads: r.OpenThreads,
		Citations:   len(r.Citations),
		Coverage:    len(r.Coverage),
	}
}

type escalationEvent struct {
	Event  string `json:"event"`
	ID     string `json:"id"`
	Stage  string `json:"stage"`
	Reason string `json:"reason"`
	Resume string `json:"resume"`
	Next   string `json:"next,omitempty"`
}

type exhaustedEvent struct {
	Event     string  `json:"event"`
	ID        string  `json:"id"`
	Stage     string  `json:"stage"`
	Spent     float64 `json:"spent_credits"`
	Envelope  int     `json:"envelope"`
	Committed bool    `json:"committed"`
	Resume    string  `json:"resume"`
	Next      string  `json:"next,omitempty"`
	// Preconditions carry probes a caller should run BEFORE acting on `next`
	// (see resumePreconditions). Their point is to catch the "orphan gummi
	// still running" race: a bare retry there hits ErrLocked and looks like a
	// fresh failure. Empty when nothing is worth checking.
	Preconditions *resumePreconditions `json:"preconditions,omitempty"`
}

type timeoutEvent struct {
	Event string `json:"event"`
	ID    string `json:"id"`
	Stage string `json:"stage"`
	// Hint names the most likely cause (a backend stall/disconnect/auth
	// loss rather than a gummi hang), so a caller reading the stream can act
	// without re-diagnosing an opaque timeout.
	Hint string `json:"hint,omitempty"`
	// StageTimeoutUsed is the --stage-timeout value that fired, so a caller
	// tuning it after a timeout has the misconfigured number in hand rather
	// than guessing the default. Formatted as Go's Duration string (e.g.
	// "20m0s"). Absent when the timeout was disabled (--stage-timeout 0).
	StageTimeoutUsed string               `json:"stage_timeout_used,omitempty"`
	Resume           string               `json:"resume"`
	Next             string               `json:"next,omitempty"`
	Preconditions    *resumePreconditions `json:"preconditions,omitempty"`
}

// resumePreconditions is the small set of probes a caller should run before
// following a terminal event's `next` command. Right now it carries just
// check_running — the shell one-liner that detects an orphan gummi process
// still driving a card. Structured (not a free-form hint) so a programmatic
// caller can enumerate the checks rather than parse prose.
type resumePreconditions struct {
	// CheckRunning is a shell one-liner that prints a warning when the
	// workspace's recorded pid is still alive — a caller should wait rather
	// than immediately retrying `next`, which would hit ErrLocked. The pid
	// file is a best-effort hint across concurrent drives, not a unique
	// governor of the workspace.
	CheckRunning string `json:"check_running,omitempty"`
}

// stoppedEvent is the --until terminal milestone: a clean early stop at the
// named design stage, resumable. Exit 0 (a caller tells it apart from done
// by the event, not the code).
type stoppedEvent struct {
	Event  string `json:"event"`
	ID     string `json:"id"`
	Stage  string `json:"stage"`
	Resume string `json:"resume"`
	Next   string `json:"next,omitempty"`
}

type doneEvent struct {
	Event        string  `json:"event"`
	ID           string  `json:"id"`
	Branch       string  `json:"branch"`
	Spec         string  `json:"spec,omitempty"`
	Spent        float64 `json:"spent_credits"`
	ReviewRounds int     `json:"review_rounds"`
}

// mergedEvent reports a successful headless landing: the feature branch was
// squash-merged onto main as one commit. Commit is the landed commit's sha —
// exactly what SquashMerge created — so a driving script can record which
// commit reached main without re-reading git.
type mergedEvent struct {
	Event  string `json:"event"`
	ID     string `json:"id"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
}

// cleanedEvent reports a successful headless cleanup: a landed card's
// worktree and branch were removed (the card record stays as a done entry).
type cleanedEvent struct {
	Event  string `json:"event"`
	ID     string `json:"id"`
	Branch string `json:"branch"`
}

type errorEvent struct {
	Event string `json:"event"`
	ID    string `json:"id,omitempty"`
	Error string `json:"error"`
	// Resumable is true when the failure left a durable, non-terminal
	// feature card behind (earlier stages committed real progress) — the
	// error's exit code is still 1, but a `resume <id>` may finish it. Stage
	// is that card's parked stage. Both are absent when the failure happened
	// before an id existed (creation/validation) or the card is terminal.
	Resumable bool   `json:"resumable"`
	Stage     string `json:"stage,omitempty"`
	// Hint is a short remediation note when the error looks like a backend
	// disconnect, stall, or auth failure — absent otherwise.
	Hint string `json:"hint,omitempty"`
	// Next is the resume command to retry, present only when Resumable (a
	// durable non-terminal card exists); absent for pre-id setup failures.
	Next string `json:"next,omitempty"`
}

type activityEvent struct {
	Event string `json:"event"`
	ID    string `json:"id"`
	Stage string `json:"stage"`
	Line  string `json:"line"`
}

// activity emits a per-tool-call line, only under verbose.
func (e *emitter) activity(id, stage, line string) {
	if !e.verbose {
		return
	}
	e.emit(activityEvent{Event: "activity", ID: id, Stage: stage, Line: line})
}

// askLabels flattens an Ask's options to their labels, for the question
// event's options array.
func askLabels(a *engine.Ask) []string {
	if a == nil {
		return nil
	}
	out := make([]string, 0, len(a.Options))
	for _, o := range a.Options {
		out = append(out, o.Label)
	}
	return out
}

// recommendedOption picks the option the agent flagged as recommended —
// by convention it marks that option's label ("… (recommended)") — and
// falls back to the first option when none is marked. It is both the
// value surfaced in the question event and the answer --autonomous
// auto-takes (D5).
func recommendedOption(a *engine.Ask) string {
	if a == nil || len(a.Options) == 0 {
		return ""
	}
	for _, o := range a.Options {
		if strings.Contains(strings.ToLower(o.Label), "recommend") {
			return o.Label
		}
	}
	return a.Options[0].Label
}
