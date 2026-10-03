package webapi

import "time"

// CardStats is GET /api/cards/{id}/stats: the card's run as cardrun
// reports it — the same fold the TUI's run tab and `status --stats` read,
// so the three cannot disagree about what a card cost.
type CardStats struct {
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	Kind     string        `json:"kind"`
	Stage    string        `json:"stage"`
	Sessions []StatSession `json:"sessions"`
	Money    StatMoney     `json:"money"`
	Clock    StatClock     `json:"clock"`
	Hands    StatHands     `json:"hands"`
	Judgment StatJudgment  `json:"judgment"`
	// Envelope is the budget and how much of it is left.
	Envelope StatEnvelope `json:"envelope"`
}

// StatSession is one pass: a stage run by one role.
type StatSession struct {
	Stage   string    `json:"stage"`
	Role    string    `json:"role"`
	Flavor  string    `json:"flavor,omitempty"`
	Model   string    `json:"model,omitempty"`
	Started time.Time `json:"started"`
	// Ended is absent while the pass is still open.
	Ended       time.Time `json:"ended,omitzero"`
	EndInferred bool      `json:"endInferred,omitempty"`
	Turns       int       `json:"turns"`
	Tools       int       `json:"tools"`
	ToolFails   int       `json:"toolFails"`
	Credits     float64   `json:"credits"`
	Estimated   float64   `json:"estimated,omitempty"`
	Verdict     string    `json:"verdict,omitempty"`
	// Tokens is what the pass spent in tokens — the grain Credits is the
	// sum of. Absent when the backend reported no token figures.
	Tokens Tokens `json:"tokens,omitzero"`
	// ContextPeak and ContextLimit are how close the pass came to its
	// context window; absent when the backend never reported occupancy.
	ContextPeak  int64 `json:"contextPeak,omitempty"`
	ContextLimit int64 `json:"contextLimit,omitempty"`
	// Redo marks work the card had already done; RedoReason is
	// "corrected" or "reproved".
	Redo          bool   `json:"redo,omitempty"`
	RedoReason    string `json:"redoReason,omitempty"`
	Reconstructed bool   `json:"reconstructed,omitempty"`
}

// StatMoney is where the credits went.
type StatMoney struct {
	Credits   float64 `json:"credits"`
	Estimated float64 `json:"estimated"`
	FirstPass float64 `json:"firstPass"`
	Rework    float64 `json:"rework"`
	Corrected float64 `json:"corrected"`
	Reproved  float64 `json:"reproved"`
	// Elsewhere is what no pass accounts for (a goal's lead, a one-shot,
	// a decomposition), and ElsewhereBy names it by role — so the pass
	// rows plus this come to Credits, the figure the board prints.
	Elsewhere   float64  `json:"elsewhere"`
	ElsewhereBy []Bucket `json:"elsewhereBy,omitempty"`
	ByStage     []Bucket `json:"byStage"`
	ByRole      []Bucket `json:"byRole"`
	ByModel     []Bucket `json:"byModel"`
}

// Bucket is one named share of a total.
type Bucket struct {
	Name    string  `json:"name"`
	Credits float64 `json:"credits"`
}

// StatClock is where the hours went, in milliseconds.
type StatClock struct {
	AgentMs   int64 `json:"agentMs"`
	OnYouMs   int64 `json:"onYouMs"`
	IdleMs    int64 `json:"idleMs"`
	ElapsedMs int64 `json:"elapsedMs"`
	// ToFirstGateMs is how long until the card first crossed a design
	// gate, and ToVerifiedMs until its verify stage first passed; absent
	// until it has happened.
	ToFirstGateMs int64 `json:"toFirstGateMs,omitempty"`
	ToVerifiedMs  int64 `json:"toVerifiedMs,omitempty"`
}

// StatHands is what the card did, as against what it decided.
type StatHands struct {
	Turns     int `json:"turns"`
	ToolCalls int `json:"toolCalls"`
	ToolFails int `json:"toolFails"`
	// Tools is every recorded agent tool call, by name and descending
	// count. It is omitted — never empty — when the card's backend
	// recorded none, so "this backend reports no tool calls" stays a
	// different fact on the wire from "this card made none". The tag is
	// omitzero, not the repo's usual omitempty: omitempty collapses nil
	// and empty, and only the nil side may read as absent.
	Tools     []StatToolUse  `json:"tools,omitzero"`
	Skills    []StatToolUse  `json:"skills,omitempty"`
	Subagents []StatToolUse  `json:"subagents,omitempty"`
	Checks    []StatCheckRun `json:"checks,omitempty"`
}

// StatToolUse is one tool the card called, and how that went.
type StatToolUse struct {
	Name  string `json:"name"`
	Calls int    `json:"calls"`
	Fails int    `json:"fails,omitempty"`
	// Detail is the argument of the most recent call — on a skill or a
	// subagent, the only thing that says what was delegated.
	Detail string `json:"detail,omitempty"`
	// TotalMs is the summed duration of the calls that reported one.
	TotalMs int64 `json:"totalMs,omitempty"`
}

// StatCheckRun is one named gummi check and its tally across the card.
type StatCheckRun struct {
	Name  string `json:"name"`
	Runs  int    `json:"runs"`
	Fails int    `json:"fails,omitempty"`
	// Excused marks a failure written off as pre-existing by the card's
	// baseline.
	Excused bool `json:"excused,omitempty"`
}

// StatJudgment is what the card decided, and who decided it.
type StatJudgment struct {
	Gates StatAnswered `json:"gates"`
	Asks  StatAnswered `json:"asks"`
	// Parks is every time the card stopped and waited for someone. The
	// ones the board process quit made are already out — the fold leaves
	// them off, and the projection does not re-filter.
	Parks []StatPark `json:"parks,omitempty"`
}

// StatAnswered is a count of decisions split by who took them.
type StatAnswered struct {
	Total     int `json:"total"`
	ByYou     int `json:"byYou"`
	ByMachine int `json:"byMachine"`
}

// StatPark is one time the card stopped and waited for someone.
type StatPark struct {
	Reason string    `json:"reason"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// StatEnvelope is the budget a card runs against.
type StatEnvelope struct {
	Credits int     `json:"credits"`
	Left    float64 `json:"left"`
}

// Fleet is GET /api/fleet?from=&to=: the whole board's run over a window,
// fleetrun's fold.
type Fleet struct {
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Credits   float64   `json:"credits"`
	Estimated float64   `json:"estimated"`
	Rework    float64   `json:"rework"`
	Corrected float64   `json:"corrected"`
	Reproved  float64   `json:"reproved"`
	ByStage   []Bucket  `json:"byStage"`
	ByModel   []Bucket  `json:"byModel"`
	AgentMs   int64     `json:"agentMs"`
	OnYouMs   int64     `json:"onYouMs"`
	IdleMs    int64     `json:"idleMs"`
	ElapsedMs int64     `json:"elapsedMs"`
	Running   int       `json:"running"`
	PeakLanes int       `json:"peakLanes"`
	// Tokens is what the window's passes spent in tokens — the passes
	// Credits covers and no others.
	Tokens Tokens `json:"tokens"`
	// Busiest is the stretch of the window with the most agent time;
	// absent when nothing ran.
	Busiest *Busiest `json:"busiest,omitempty"`
	Lanes   []Lane   `json:"lanes"`
	// AllTimeCredits and AllTimeCards are the ledger beyond the window.
	AllTimeCredits float64 `json:"allTimeCredits"`
	AllTimeCards   int     `json:"allTimeCards"`
}

// Tokens is a token count split the way the adapters report it.
type Tokens struct {
	Input  int64 `json:"input"`
	Cached int64 `json:"cached"`
	Output int64 `json:"output"`
}

// Busiest is the window's busiest stretch: where it starts, how long it
// is (an hour, or a day on a long window), and the agent time inside it.
type Busiest struct {
	From    time.Time `json:"from"`
	LenMs   int64     `json:"lenMs"`
	AgentMs int64     `json:"agentMs"`
}

// Lane is one card's row on the fleet timeline.
type Lane struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	Kind    string  `json:"kind"`
	Ending  string  `json:"ending,omitempty"`
	Credits float64 `json:"credits"`
	Redo    float64 `json:"redo,omitempty"`
	// Tokens is what the lane's window passes spent, charged on the same
	// rule as Credits.
	Tokens  Tokens `json:"tokens"`
	Running bool   `json:"running,omitempty"`
	// Note is the one-line diagnosis of why the lane cost what it did
	// ("sent back once after a verdict"); empty on a lane merely costly.
	Note string `json:"note,omitempty"`
	// OpenWaitFrom is when the decision the card still waits on was
	// raised, when one is open.
	OpenWaitFrom time.Time   `json:"openWaitFrom,omitzero"`
	Blocks       []Span      `json:"blocks"`
	Waits        []Span      `json:"waits,omitempty"`
	Gates        []time.Time `json:"gates,omitempty"`
	LandedAt     time.Time   `json:"landedAt,omitzero"`
}

// Span is one stretch on a lane: a stage session (Stage set) or a wait.
type Span struct {
	From  time.Time `json:"from"`
	To    time.Time `json:"to,omitzero"`
	Stage string    `json:"stage,omitempty"`
	Role  string    `json:"role,omitempty"`
}
