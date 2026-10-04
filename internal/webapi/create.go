package webapi

// CreateCardRequest is POST /api/cards: the new-card form, every kind.
// Fields that do not apply to Kind are ignored. Answers Card.
type CreateCardRequest struct {
	// Kind is "feature", "bug", "research", "goal" or "freeform".
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Description is the card's brief: a feature's what-and-why, a
	// research card's question, a goal's done-when.
	Description string `json:"description,omitempty"`
	OneLiner    string `json:"oneLiner,omitempty"`
	Profile     string `json:"profile,omitempty"`
	Repo        string `json:"repo,omitempty"`
	// Base is the branch the card forks from and lands on.
	Base string `json:"base,omitempty"`
	// Envelope is the credit budget; nil takes the board's default.
	Envelope *int `json:"envelope,omitempty"`
	// Autopilot starts the card with its gates crossing unattended.
	Autopilot bool `json:"autopilot,omitempty"`
	// Adopt names an existing branch to put the card on (§10 D22); PR
	// names a pull request whose head branch to adopt.
	Adopt string `json:"adopt,omitempty"`
	PR    string `json:"pr,omitempty"`
	// DependsOn lists the cards this one waits for.
	DependsOn []string `json:"dependsOn,omitempty"`
	// StackOn places the card on top of another card's stack.
	StackOn string `json:"stackOn,omitempty"`
	// Bug fields.
	Severity string `json:"severity,omitempty"`
	Repro    string `json:"repro,omitempty"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Env      string `json:"env,omitempty"`
	// Issue is a GitHub issue number to seed a bug from.
	Issue int `json:"issue,omitempty"`
	// Diagnosis makes a research card a diagnosis rather than a survey.
	Diagnosis bool `json:"diagnosis,omitempty"`
	// Start runs the card's first stage once it is created.
	Start bool `json:"start,omitempty"`
	// Backend and Model are the agent and model a session (a freeform
	// card) runs on, picked in its draft's composer (DESIGN §19.8). Both
	// empty runs it on its profile's implementer. Refused on every other
	// kind: a stage takes its agent from the card's profile.
	Backend string `json:"backend,omitempty"`
	Model   string `json:"model,omitempty"`
	// MainCheckout is a freeform kind's "runs in" choice: a session that
	// works in the repository's main checkout instead of a worktree of
	// its own (DESIGN §19) — no branch cut, no worktree created. Refused
	// on every other kind, and with Base or StackOn set: a main-checkout
	// session has no branch to fork from or stack onto.
	MainCheckout bool `json:"mainCheckout,omitempty"`
	// Attachments are the ids of images (already uploaded via POST
	// /api/attachments) to link into the card's seeded description —
	// stored by reference, so every later stage that reads the spec sees
	// them too.
	Attachments []string `json:"attachments,omitempty"`
	// Skills are library skills (agent-plugin item ids, as Form.Skills
	// offers them) to hand this card's sessions instead of the whole
	// library; empty forwards the whole library.
	Skills []string `json:"skills,omitempty"`
}

// Form is GET /api/form: the choices the new-card form offers.
type Form struct {
	Kinds      []Choice `json:"kinds"`
	Profiles   []string `json:"profiles"`
	Repos      []string `json:"repos"`
	Severities []string `json:"severities"`
	// Branches lists the chosen repo's branches (?repo=), for Base and
	// Adopt.
	Branches []string `json:"branches,omitempty"`
	// Adoptable says, for each of Branches in the same order, whether a
	// new card may adopt it and, when it may not, why: another card holds
	// it (a landed one too — one branch, one card), it is the branch the
	// card would land on, or it carries no commits of its own past that
	// branch. Measured against the repo's default base; a card given
	// another base is checked again when it is created.
	Adoptable []AdoptChoice `json:"adoptable,omitempty"`
	// Stackable are the cards a new card can be stacked on; Dependable the
	// cards it can depend on.
	Stackable  []CardRef `json:"stackable"`
	Dependable []CardRef `json:"dependable"`
	// Envelope is the board's default envelope.
	Envelope int `json:"envelope"`
	// Sessions is what a session's model picker offers.
	Sessions SessionModels `json:"sessions"`
	// Skills are the library skills a card can be created with: Value is
	// the item id, Label its name, Detail its description.
	Skills []Choice `json:"skills"`
}

// AdoptChoice is one branch the new-card form's adopt list shows. Why is
// empty when the branch may be adopted, and otherwise the reason it will
// be refused, short enough to sit beside its name ("FD-001 has it").
// Held names the card holding it, the one reason that does not depend
// on which base the card is given.
type AdoptChoice struct {
	Branch string `json:"branch"`
	Why    string `json:"why,omitempty"`
	Held   string `json:"held,omitempty"`
}

// SessionModels is what a session's model picker offers (DESIGN §19.8).
// There is deliberately no list of every model an agent can run kept by
// gummi: model ids are opaque strings the adapters forward verbatim, so
// the picker offers what each agent says it provides (asked live, where
// it can say so), plus the ones this workspace already uses, and takes
// any other typed in.
type SessionModels struct {
	// Default is what a new session runs on when nothing is picked: the
	// default profile's implementer.
	Default SessionModel `json:"default"`
	// Agents is every agent a session can be pointed at, in the order the
	// picker lists them, with whether this host can run it.
	Agents []SessionAgent `json:"agents"`
	// Recent are the pairs sessions on this board run on, most recently
	// changed first.
	Recent []SessionModel `json:"recent"`
}

// SessionModel is one agent-and-model pair a session runs on.
type SessionModel struct {
	Backend string `json:"backend"`
	Model   string `json:"model"`
}

// SessionAgent is one agent in the picker.
type SessionAgent struct {
	Name string `json:"name"`
	// Installed is false for an agent this host cannot start: its CLI is
	// not on PATH (or, for headless, no command line is configured).
	Installed bool `json:"installed"`
	// Models are the ids this agent offers by itself (its own catalog,
	// asked live) merged with the ids the workspace's profiles run on it;
	// any other id may be typed. Empty for an agent that cannot
	// enumerate — the typed entry and the profile ids are then all a
	// picker has.
	Models []string `json:"models"`
	// NeedsModel marks an agent that refuses to start without a model id.
	NeedsModel bool `json:"needsModel,omitempty"`
	// Hint says how this agent spells a model id, when it has a rule.
	Hint string `json:"hint,omitempty"`
	// Pattern is what a typed id must match for this agent to run it: an
	// ECMAScript regular expression, case-insensitive; empty takes any id.
	// The picker hides a typed id that does not match; the server still
	// checks every pick.
	Pattern string `json:"pattern,omitempty"`
	// Images is whether the agent can take images natively with a turn
	// (agent.Capabilities.Images) — the backend-level half of the live
	// answer Composer.Images reports for a card whose session is open. A
	// session draft's composer reads it for the agent its pair names.
	Images bool `json:"images,omitempty"`
}

// Choice is one option in a select: a value and the words for it.
type Choice struct {
	Value  string `json:"value"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

// CardRef names a card in a picker.
type CardRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Stage string `json:"stage,omitempty"`
}
