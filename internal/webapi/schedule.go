package webapi

import "time"

// Schedule is one row of GET /api/schedules: a timed trigger for
// freeform sessions (DESIGN §19.9) — a mint that starts a new card on a
// cron cadence, or a heartbeat that sends a recurring turn into one
// existing session. Enabled is off until it is switched on; a definition
// edit forces it off again.
type Schedule struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // "mint" or "heartbeat"
	// Target is the heartbeat's one freeform card; Repo the mint's
	// configured repository, empty for the workspace default.
	Target string `json:"target,omitempty"`
	Repo   string `json:"repo,omitempty"`
	// Cron is the canonical 5-field expression — presets compile before
	// they are stored, so what a page shows and what fires agree.
	Cron     string `json:"cron"`
	Timezone string `json:"timezone,omitempty"`
	Prompt   string `json:"prompt"`
	// Backend and Model are the mint's session choice; a heartbeat spends
	// its target's session and carries neither.
	Backend string `json:"backend,omitempty"`
	Model   string `json:"model,omitempty"`
	// Envelope is the minted card's brake in credits; a heartbeat's
	// target's envelope is the brake there, so this stays zero.
	Envelope int `json:"envelope,omitempty"`

	Enabled bool `json:"enabled"`
	// RunRequested is a pending run-now: the CLI asked the running board
	// for one off-cadence fire.
	RunRequested bool `json:"runRequested,omitempty"`

	LastRun time.Time `json:"lastRun,omitzero"`
	NextRun time.Time `json:"nextRun,omitzero"`

	LastStatus string `json:"lastStatus,omitempty"`
	LastDetail string `json:"lastDetail,omitempty"`
	// LastCard is the last fire's card, for display; OrphanCard names a
	// card this schedule minted whose kickoff failed and whose next fire
	// retries it.
	LastCard   string `json:"lastCard,omitempty"`
	OrphanCard string `json:"orphanCard,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
}

// Schedules is GET /api/schedules.
type Schedules struct {
	Schedules []Schedule `json:"schedules"`
}

// ScheduleRequest is the body of POST /api/schedules (a definition) and
// of PATCH /api/schedules/{id} (an edit, which disables the row until it
// is re-enabled). Kind selects which fields mean what: a mint names
// repo/backend/model/envelope, a heartbeat names target. Every is a
// cadence preset (`5m`, `1h`, `@daily`) compiled to cron at the boundary;
// Cron is the expression itself, and one of the two must be set.
type ScheduleRequest struct {
	Name     string `json:"name"`
	Kind     string `json:"kind,omitempty"`
	Target   string `json:"target,omitempty"`
	Repo     string `json:"repo,omitempty"`
	Cron     string `json:"cron,omitempty"`
	Every    string `json:"every,omitempty"`
	Timezone string `json:"timezone,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
	Backend  string `json:"backend,omitempty"`
	Model    string `json:"model,omitempty"`
	// Envelope is a mint's brake; a heartbeat ignores it.
	Envelope *int `json:"envelope,omitempty"`
}

// SchedulePreviewRequest is POST /api/schedules/preview's body: the
// cadence inputs a form holds so far, answered before anything is
// stored. Every is a preset (`5m`, `@daily`), Cron the expression;
// either may be empty when the other is present, and with both set the
// cron wins — the same precedence the store write applies. Timezone is
// a zone name, empty for the host's zone. Backend and Model, when the
// form has already picked a pair, shape the envelope hint.
type SchedulePreviewRequest struct {
	Every    string `json:"every,omitempty"`
	Cron     string `json:"cron,omitempty"`
	Timezone string `json:"timezone,omitempty"`
	Backend  string `json:"backend,omitempty"`
	Model    string `json:"model,omitempty"`
}

// SchedulePreview is POST /api/schedules/preview's answer. A valid
// cadence answers with the canonical cron the store would hold and its
// coming fires; a refused one answers 200 with Error saying why, so the
// form shows the refusal where the person is typing rather than as a
// failed request.
type SchedulePreview struct {
	// Cron is the canonical 5-field expression the store would hold:
	// the preset compiled, or the expression trimmed. Empty when no
	// cadence was given at all.
	Cron string `json:"cron,omitempty"`
	// Fires are the next fire times, soonest first, in the asked zone;
	// empty whenever Error is set.
	Fires []time.Time `json:"fires,omitempty"`
	// Error says why the cadence is refused; empty when it is valid.
	Error string `json:"error,omitempty"`
	// EnvelopeHint is the mint form's guidance about the picked pair's
	// credit rate: what a credit buys where the workspace can price the
	// pair, otherwise what the brake is for.
	EnvelopeHint string `json:"envelopeHint,omitempty"`
}

// ScheduleFire is one fire's recorded result, as POST
// /api/schedules/{id}/run and the board's poll report it.
type ScheduleFire struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Forced bool   `json:"forced,omitempty"`
	// Status is the outcome: ok, skipped-busy, paused-exhausted,
	// disabled-target-closed or failed.
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	// Card is the card the fire minted or retried; Orphan the one a
	// failed kickoff left for the next fire to retry.
	Card   string    `json:"card,omitempty"`
	Orphan string    `json:"orphan,omitempty"`
	At     time.Time `json:"at,omitzero"`
}
