// Package driver is gummi's headless run loop: it drives one feature
// through the same engine gate floor the TUI uses (engine.Advance,
// engine.Run/RunWith/RunCritique, engine.Attach), but with no human at
// the keyboard. Each invocation runs the engine forward until the caller
// must decide, then returns a typed Outcome the CLI maps to an exit code
// and a final NDJSON line. Any point a human would resolve — a design
// question, a rerun/critique cap, budget exhaustion, a stalled stage —
// becomes a durable, resumable escalation with a non-zero exit, never a
// silent auto-proceed (DESIGN §8.2, deterministic failure over silent
// degradation). The driver changes *who* approves a gate, not *whether*
// the quality floor runs.
package driver

// Status is the terminal classification of a run/resume invocation. Each
// maps to a stable process exit code (ExitCode) so a calling agent can
// branch on the result without parsing stdout.
type Status string

const (
	// StatusDone: a verified branch is ready. gummi never merges it.
	StatusDone Status = "done"
	// StatusError: setup or agent failure; nothing partial landed.
	StatusError Status = "error"
	// StatusQuestion: a delegated ask_user question, or a caller design
	// gate awaiting --approve/--request-changes. Resume to continue.
	StatusQuestion Status = "question"
	// StatusBlocked: open %%/diff threads block a gate. Resolve them (or
	// resume --request-changes) before the gate can cross.
	StatusBlocked Status = "blocked"
	// StatusEscalation: a rerun/critique cap was hit, or a stage returned
	// no clear verdict. The card stays durable + resumable.
	StatusEscalation Status = "escalation"
	// StatusExhausted: the credit envelope ran dry. Raise it, then resume.
	StatusExhausted Status = "exhausted"
	// StatusTimeout: a stage went quiet past the inactivity budget — a
	// likely hang. Durable + resumable.
	StatusTimeout Status = "timeout"
)

// ExitCode is the process exit status for a terminal Status. done is 0;
// error keeps the conventional 1; the decision boundaries take distinct
// codes so a caller can branch on them.
func (s Status) ExitCode() int {
	switch s {
	case StatusDone:
		return 0
	case StatusError:
		return 1
	case StatusQuestion:
		return 2
	case StatusBlocked:
		return 3
	case StatusEscalation:
		return 4
	case StatusExhausted:
		return 5
	case StatusTimeout:
		return 6
	default:
		return 1
	}
}

// Outcome is the terminal result of drive. ID is the feature the run
// governed (empty only when creation itself failed before an ID existed).
type Outcome struct {
	Status Status
	ID     string
}
