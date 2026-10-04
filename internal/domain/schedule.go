package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Schedules and heartbeats (DESIGN §19.9): two time-driven primitives
// that put freeform sessions on a clock. A schedule mints a NEW freeform
// card on a cron cadence and kicks it off with a stored prompt; a
// heartbeat sends a recurring turn into ONE existing freeform session so
// the same conversation reassesses and continues. Both are rows the board
// fires from its update loop — nothing fires while no board is running,
// and missed fires coalesce into one catch-up fire.
//
// A schedule is an operator surface, and a narrow one by design: it only
// ever mints freeform cards, never crosses a gate, and never acts
// board-wide. Its envelope is the brake — a scheduled card mints with
// one, and an exhausted heartbeat target pauses the schedule rather than
// raising anything.

// ScheduleID identifies a schedule. Like a stack's id it is not minted
// from the shared counter: a schedule is not a unit of work, carries no
// branch, and never appears in a commit message, so it takes no place in
// the FD/BG/RS/GL numbering. It is the slug of its name.
type ScheduleID string

// ScheduleKind is which of the two primitives a row is.
type ScheduleKind string

const (
	// ScheduleMint mints a new freeform card each fire and kicks it off
	// with the stored prompt.
	ScheduleMint ScheduleKind = "mint"
	// ScheduleHeartbeat sends the stored prompt as a turn into one
	// existing freeform session, whose id is Target.
	ScheduleHeartbeat ScheduleKind = "heartbeat"
)

// Valid reports whether the kind is one of the two.
func (k ScheduleKind) Valid() bool {
	return k == ScheduleMint || k == ScheduleHeartbeat
}

// ScheduleStatus is the last fire's outcome, as the store's one row of
// history keeps it. skipped-busy is recorded but never notified: a
// five-minute heartbeat against a working session would otherwise notify
// every five minutes.
type ScheduleStatus string

const (
	ScheduleOK                   ScheduleStatus = "ok"
	ScheduleSkippedBusy          ScheduleStatus = "skipped-busy"
	SchedulePausedExhausted      ScheduleStatus = "paused-exhausted"
	ScheduleDisabledTargetClosed ScheduleStatus = "disabled-target-closed"
	ScheduleFailed               ScheduleStatus = "failed"
)

// Disables reports whether an outcome with this status turns the schedule
// off. A plain failure never does — it fires again on its next cadence.
func (s ScheduleStatus) Disables() bool {
	return s == SchedulePausedExhausted || s == ScheduleDisabledTargetClosed
}

// ScheduleOutcome is one fire's result, recorded on the row in a single
// write. Orphan names a card this schedule minted whose kickoff then
// failed — the next fire retries that card instead of minting another
// beside it — and is always overwritten, so every outcome that is not
// "minted, then kickoff failed" clears it.
type ScheduleOutcome struct {
	At     time.Time
	Status ScheduleStatus
	Detail string
	// Card is the card this fire minted or retried, for display; empty
	// leaves the stored last_card unchanged.
	Card FeatureID
	// Orphan overwrites orphan_card unconditionally.
	Orphan FeatureID
	// Disable turns the schedule off in the same write — the
	// paused-exhausted and disabled-target-closed outcomes, plus the
	// defensive zero-next-run failure. It is engine-internal instruction,
	// not part of any rendered shape.
	Disable bool
}

// Schedule is one stored definition: what fires, on what cadence, into
// what, and what happened last. The cadence is a compiled cron string —
// presets compile at the face, and cron is canonical.
type Schedule struct {
	ID   ScheduleID
	Name string
	Kind ScheduleKind
	// Target is the heartbeat's one freeform card. Empty on a mint.
	Target FeatureID
	// Repo is the mint's configured repository, empty for the workspace
	// default — the same convention as Feature.Repo.
	Repo string
	// Cron is the canonical 5-field expression. Its validity is enforced
	// at the store's write boundary (which can call the cron package);
	// this type cannot, because the cron package imports this one.
	Cron     string
	Timezone string // IANA name; empty means local
	// Prompt is the mint's opening turn or the heartbeat's recurring turn.
	Prompt string
	// Backend and Model are the mint's session backend and model (the
	// same pair a hand-minted freeform card can name); empty is the
	// profile's implementer. A heartbeat spends its target's session and
	// carries neither.
	Backend string
	Model   string
	// Envelope is the minted card's brake, in credits. A mint must carry
	// one > 0; a heartbeat has none of its own — its target's envelope
	// is the brake, and exhaustion pauses the schedule.
	Envelope     int
	Enabled      bool
	RunRequested bool // a CLI run-now waiting on the board's next tick
	LastRun      time.Time
	NextRun      time.Time
	LastStatus   ScheduleStatus
	LastDetail   string
	// LastCard is the last fire's card, for display. It is never retried:
	// the orphan pointer below is the only card a fire reuses.
	LastCard FeatureID
	// OrphanCard names a card this schedule minted whose kickoff then
	// failed, and nothing else. The next fire retries it while it exists
	// and is not closed; a card from an earlier good fire never receives
	// a second opening turn.
	OrphanCard FeatureID
	CreatedAt  time.Time
}

// maxScheduleNameLen caps a schedule's display name at the same length a
// card title gets: it has to fit on one board row.
const maxScheduleNameLen = 100

// Validate reports whether the definition is well formed. It is
// structural only — per-kind field rules and non-empty text. Cron
// validity is deliberately NOT here: the schedule package that owns
// parsing imports this one, so Validate has no Parse to call, and the
// store's write boundary is where cron is checked instead.
func (s *Schedule) Validate() error {
	if !scheduleIDRe.MatchString(string(s.ID)) {
		return fmt.Errorf("invalid schedule id %q: want lowercase words joined by dashes", s.ID)
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("schedule %s has no name", s.ID)
	}
	if len(s.Name) > maxScheduleNameLen {
		return fmt.Errorf("schedule %s name is %d chars, max %d", s.ID, len(s.Name), maxScheduleNameLen)
	}
	if !s.Kind.Valid() {
		return fmt.Errorf("schedule %s has kind %q: want %q or %q", s.ID, s.Kind, ScheduleMint, ScheduleHeartbeat)
	}
	if strings.TrimSpace(s.Cron) == "" {
		return fmt.Errorf("schedule %s has no cron", s.ID)
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return fmt.Errorf("schedule %s has no prompt", s.ID)
	}
	if strings.ContainsAny(s.Repo, "/\\") {
		return fmt.Errorf("schedule %s repo %q must be a configured repository name, not a path", s.ID, s.Repo)
	}
	if s.LastStatus != "" && !lastStatusKnown(s.LastStatus) {
		return fmt.Errorf("schedule %s has unknown last status %q", s.ID, s.LastStatus)
	}
	switch s.Kind {
	case ScheduleHeartbeat:
		if s.Target == "" {
			return fmt.Errorf("schedule %s: a heartbeat names the freeform card it sends turns to", s.ID)
		}
		if !isFreeformID(s.Target) {
			return fmt.Errorf("schedule %s: heartbeat target %s is not a freeform card", s.ID, s.Target)
		}
		if s.Repo != "" {
			return fmt.Errorf("schedule %s: a heartbeat has no repository of its own; %s's is the one that matters", s.ID, s.Target)
		}
		if s.Backend != "" || s.Model != "" {
			return fmt.Errorf("schedule %s: a heartbeat spends its target's session and names no backend or model", s.ID)
		}
		if s.Envelope != 0 {
			return fmt.Errorf("schedule %s: a heartbeat has no envelope; %s's is the brake", s.ID, s.Target)
		}
	case ScheduleMint:
		if s.Target != "" {
			return fmt.Errorf("schedule %s: a mint mints fresh cards and names no target", s.ID)
		}
		if s.Envelope <= 0 {
			return fmt.Errorf("schedule %s: a scheduled card always mints with a brake — give it an envelope above zero", s.ID)
		}
	}
	return nil
}

// scheduleIDRe is the allowlist for a schedule id: the slug rules, so an
// id is safe to print in a notice and carries no path separators.
var scheduleIDRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// lastStatusKnown reports whether v is one of the five recorded statuses.
func lastStatusKnown(v ScheduleStatus) bool {
	switch v {
	case ScheduleOK, ScheduleSkippedBusy, SchedulePausedExhausted,
		ScheduleDisabledTargetClosed, ScheduleFailed:
		return true
	}
	return false
}

// isFreeformID reports whether id parses as a freeform card's id.
func isFreeformID(id FeatureID) bool {
	return id != "" && id.Kind() == KindFreeform
}

// NewScheduleID derives a schedule's id from the name it will be shown
// under. There is no fallback here: a schedule is named on purpose by
// whoever adds it, and a name that yields nothing usable is a name to
// retype, not to guess around.
func NewScheduleID(name string) (ScheduleID, error) {
	slug, err := Slugify(name)
	if err != nil {
		return "", fmt.Errorf("cannot derive a schedule id from %q: %w", name, err)
	}
	return ScheduleID(slug), nil
}
