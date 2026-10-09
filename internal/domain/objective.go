package domain

import (
	"errors"
	"regexp"
	"strings"
	"time"
)

// Objective is what a person has asked a freeform card's session to keep
// working toward (DESIGN §19.11): after each turn an auditor judges the
// work against it, and gummi sends the next turn itself until it is met,
// stuck or out of money. A card has at most one, and none by default.
//
// The rules live here as pure functions — Next is the whole loop's
// decision — so they are tested without a session; the engine runs the
// audit, the check and the turns, and the store keeps the row.
type Objective struct {
	// Text is the objective as the person wrote it.
	Text string
	// Check is an optional shell command that must exit 0 in the worktree
	// for an auditor's MET to count.
	Check string
	State ObjectiveState
	// Turns is how many continuation turns gummi has sent for it.
	Turns int
	// StuckStreak is how many STUCK verdicts came in a row.
	StuckStreak int
	// Note is the last thing the auditor (or the check) said.
	Note string
	// BaseRev is the branch tip when it was set, "" when the session had
	// no worktree yet; the audit's commits and diffstat are measured from
	// it.
	BaseRev string
	// SetAt is when it was set; SetBy is who set it (state.PersonActor).
	SetAt time.Time
	SetBy string
}

// ObjectiveState is where an objective stands.
type ObjectiveState string

const (
	// ObjectiveActive is running: each turn's end is audited.
	ObjectiveActive ObjectiveState = "active"
	// ObjectivePaused is the person's own state, from pause or stop.
	ObjectivePaused ObjectiveState = "paused"
	// ObjectiveMet: the auditor said MET and the check, if any, passed.
	ObjectiveMet ObjectiveState = "met"
	// ObjectiveStuck: ObjectiveStuckLimit STUCK verdicts in a row.
	ObjectiveStuck ObjectiveState = "stuck"
	// ObjectiveExhausted: the card's envelope ran out.
	ObjectiveExhausted ObjectiveState = "exhausted"
	// ObjectiveCapped: it reached ObjectiveTurnCap continuations.
	ObjectiveCapped ObjectiveState = "capped"
	// ObjectiveFailed: the backend errored or the session was closed.
	ObjectiveFailed ObjectiveState = "failed"
)

// ObjectiveTurnCap is the continuation limit: it catches a loop that is
// cheap and going nowhere, which the envelope would let run a long time.
const ObjectiveTurnCap = 20

// ObjectiveStuckLimit is how many STUCK verdicts in a row settle it.
const ObjectiveStuckLimit = 3

// Settled reports whether the objective has ended on its own: it no
// longer runs, and resume does not restart it.
func (s ObjectiveState) Settled() bool {
	switch s {
	case ObjectiveMet, ObjectiveStuck, ObjectiveExhausted, ObjectiveCapped, ObjectiveFailed:
		return true
	}
	return false
}

// Valid reports whether s is one of the states above.
func (s ObjectiveState) Valid() bool {
	return s == ObjectiveActive || s == ObjectivePaused || s.Settled()
}

// Validate refuses an objective with no text or an unknown state.
func (o Objective) Validate() error {
	if strings.TrimSpace(o.Text) == "" {
		return errors.New("an objective needs some text")
	}
	if !o.State.Valid() {
		return errors.New("unknown objective state " + string(o.State))
	}
	if o.Turns < 0 || o.StuckStreak < 0 {
		return errors.New("an objective's counters cannot be negative")
	}
	return nil
}

// AuditVerdict is an auditor's answer about the turn that just ended.
type AuditVerdict string

const (
	AuditContinue AuditVerdict = "CONTINUE"
	AuditMet      AuditVerdict = "MET"
	AuditStuck    AuditVerdict = "STUCK"
)

// auditLine is a verdict line as a model writes it: markdown decoration
// allowed around it, the note after a dash or colon on the same line.
var auditLine = regexp.MustCompile(`(?i)^[\s>#*_\-` + "`" + `]*VERDICT[*_\s]*:[*_\s]*(CONTINUE|MET|STUCK)\b[*_` + "`" + `]*\s*(?:[-—–:]\s*)?(.*)$`)

// ParseAudit reads an auditor's reply: the last VERDICT line decides, and
// its note is the rest of that line, or else the first other non-empty
// line. A reply with no verdict line is STUCK — an auditor that cannot
// say is not a reason to keep spending.
func ParseAudit(reply string) (AuditVerdict, string) {
	lines := strings.Split(strings.TrimSpace(reply), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		m := auditLine.FindStringSubmatch(strings.TrimSpace(lines[i]))
		if m == nil {
			continue
		}
		note := strings.Trim(strings.TrimSpace(m[2]), "*_`")
		if note == "" {
			note = firstOtherLine(lines, i)
		}
		return AuditVerdict(strings.ToUpper(m[1])), note
	}
	return AuditStuck, "the auditor gave no verdict: " + clip(firstOtherLine(lines, -1), 200)
}

func firstOtherLine(lines []string, skip int) string {
	for i, l := range lines {
		if l = strings.TrimSpace(l); i != skip && l != "" {
			return l
		}
	}
	return ""
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// CheckResult is what an objective's check command did, when the auditor
// said MET and there was one to run.
type CheckResult struct {
	Ran    bool
	Passed bool
	// Tail is the end of its output, the note a failing check leaves.
	Tail string
}

// Next applies one audit to an active objective: the objective as it now
// stands, and whether gummi sends another turn. A turn sent counts toward
// ObjectiveTurnCap; reaching the cap settles it as capped instead.
//
// A failing check is a CONTINUE whose note is the check's tail: the work
// has a test that proves it, and the test is the judge.
func (o Objective) Next(v AuditVerdict, note string, check CheckResult) (Objective, bool) {
	o.Note = note
	switch v {
	case AuditMet:
		if !check.Ran || check.Passed {
			o.State = ObjectiveMet
			o.StuckStreak = 0
			return o, false
		}
		o.StuckStreak = 0
		o.Note = "the auditor said met, but the check failed: " + check.Tail
	case AuditStuck:
		o.StuckStreak++
		if o.StuckStreak >= ObjectiveStuckLimit {
			o.State = ObjectiveStuck
			return o, false
		}
	default:
		o.StuckStreak = 0
	}
	if o.Turns >= ObjectiveTurnCap {
		o.State = ObjectiveCapped
		return o, false
	}
	o.Turns++
	return o, true
}
