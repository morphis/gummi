package driver

import (
	"encoding/json"
	"io"
	"strings"
	"sync"

	"github.com/morphis/gummi/internal/engine"
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
}

type gatePendingEvent struct {
	Event  string `json:"event"`
	ID     string `json:"id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Resume string `json:"resume"`
}

type blockedEvent struct {
	Event    string `json:"event"`
	ID       string `json:"id"`
	Gate     string `json:"gate"`
	OpenSpec int    `json:"open_questions,omitempty"`
	OpenDiff int    `json:"open_diff,omitempty"`
	Resume   string `json:"resume"`
}

type escalationEvent struct {
	Event  string `json:"event"`
	ID     string `json:"id"`
	Stage  string `json:"stage"`
	Reason string `json:"reason"`
	Resume string `json:"resume"`
}

type exhaustedEvent struct {
	Event     string  `json:"event"`
	ID        string  `json:"id"`
	Stage     string  `json:"stage"`
	Spent     float64 `json:"spent_credits"`
	Envelope  int     `json:"envelope"`
	Committed bool    `json:"committed"`
	Resume    string  `json:"resume"`
}

type timeoutEvent struct {
	Event  string `json:"event"`
	ID     string `json:"id"`
	Stage  string `json:"stage"`
	Resume string `json:"resume"`
}

// stoppedEvent is the --until terminal milestone: a clean early stop at the
// named design stage, resumable. Exit 0 (a caller tells it apart from done
// by the event, not the code).
type stoppedEvent struct {
	Event  string `json:"event"`
	ID     string `json:"id"`
	Stage  string `json:"stage"`
	Resume string `json:"resume"`
}

type doneEvent struct {
	Event        string  `json:"event"`
	ID           string  `json:"id"`
	Branch       string  `json:"branch"`
	Spec         string  `json:"spec,omitempty"`
	Spent        float64 `json:"spent_credits"`
	ReviewRounds int     `json:"review_rounds"`
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
