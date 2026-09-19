package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// Goals are the fourth kind of work item (KindGoal): a card whose work is
// other cards. The design stage agrees an objective and a done-when list;
// the build stage is conducted — the goal's cards run on a shared goal
// branch and land on it one commit each — and verify checks the combined
// branch. The types here are the structured parts of a goal doc, parsed
// out of its fenced blocks (internal/spec) and acted on by the conductor
// (internal/engine, internal/goalpolicy).

// DoneWhen is one item of a goal's done-when list: a statement that must
// be true for the goal to be met, and how it is checked. Exactly one of
// Check (a command run on the combined branch) or Judge (a statement the
// goal's verify reads against the combined diff) says how.
type DoneWhen struct {
	ID    string `yaml:"id"`              // DW-1, DW-2, …; stable once the plan is approved
	Says  string `yaml:"says"`            // the statement, in the reader's words
	Check string `yaml:"check,omitempty"` // a shell command that exits 0 when met
	Judge bool   `yaml:"judge,omitempty"` // judged by verify against the combined diff
	// Repo is the configured repository whose goal tree the check runs in.
	// Empty is the goal's home repo, which is the only repo a single-repo
	// goal has. A command needs a directory, and a goal that spans
	// repositories has several — so the item says which. Whether the name
	// is configured at all is the plan gate's question (it needs the
	// workspace's repo set); this is only where the answer is written.
	Repo string `yaml:"repo,omitempty"`
	// Experiment names a configured experiment as the item's means of
	// proof: an orchestrated run on a substrate, for a statement no command
	// in a checkout can show. The third means beside Check and Judge, and
	// exclusive with both.
	Experiment string `yaml:"experiment,omitempty"`
	// Assertions narrows an experiment item to some of what the run
	// asserts, by id. It is what lets "ingress from the first router works"
	// be met weeks before "a migration keeps its connections" can be, by
	// the same experiment. Empty means the whole run.
	Assertions []string `yaml:"assertions,omitempty"`
	// Substrate names the external environment this item's CHECK needs —
	// a cluster, a device farm, a staging account (§17.7). A command in a
	// checkout that drives one is scarce, slow, stateful and shared like
	// any other job on it, and two of them at once are not slow but wrong:
	// so gummi takes the substrate's lease for the length of the check,
	// the way it does for an experiment run, and reports the check not-run
	// rather than failed when somebody else has it.
	//
	// It is a property of the means, not of the statement: an item proved
	// by an `experiment:` gets its substrate from the experiment's own
	// definition and must not name one here.
	Substrate string `yaml:"substrate,omitempty"`
}

// Means names how the item is shown to hold: "check", "judge" or
// "experiment".
func (d DoneWhen) Means() string {
	switch {
	case strings.TrimSpace(d.Experiment) != "":
		return "experiment"
	case strings.TrimSpace(d.Check) != "":
		return "check"
	case d.Judge:
		return "judge"
	}
	return ""
}

var doneWhenIDRe = regexp.MustCompile(`^DW-[0-9]+$`)

// Validate checks an item is complete enough to gate on: an id of the
// DW-N form, a statement, and a way to check it. An item nobody can check
// keeps the goal's plan gate shut — "done" that cannot be shown is not a
// done-when item.
func (d DoneWhen) Validate() error {
	if !doneWhenIDRe.MatchString(d.ID) {
		return fmt.Errorf("done-when id %q is not of the form DW-N", d.ID)
	}
	if strings.TrimSpace(d.Says) == "" {
		return fmt.Errorf("done-when %s says nothing", d.ID)
	}
	means := 0
	for _, has := range []bool{strings.TrimSpace(d.Check) != "", d.Judge, strings.TrimSpace(d.Experiment) != ""} {
		if has {
			means++
		}
	}
	if means == 0 {
		return fmt.Errorf("done-when %s has no way to check it: give it a check command, an experiment, or judge: true", d.ID)
	}
	if strings.TrimSpace(d.Check) != "" && d.Judge {
		return fmt.Errorf("done-when %s has both a check and judge: true; pick one", d.ID)
	}
	if means > 1 {
		return fmt.Errorf("done-when %s names more than one of check, experiment and judge; pick one", d.ID)
	}
	if len(d.Assertions) > 0 && strings.TrimSpace(d.Experiment) == "" {
		return fmt.Errorf("done-when %s lists assertions but names no experiment to report them", d.ID)
	}
	if strings.TrimSpace(d.Substrate) != "" && strings.TrimSpace(d.Check) == "" {
		if strings.TrimSpace(d.Experiment) != "" {
			return fmt.Errorf("done-when %s names a substrate and an experiment; an experiment brings its own, and naming a second one here would be two answers to one question", d.ID)
		}
		return fmt.Errorf("done-when %s names a substrate but has no check to run on it: a substrate is what a COMMAND needs, and a judged item runs none", d.ID)
	}
	return nil
}

// CheckName is the name a done-when item's command runs under in the goal
// doc's gummi-checks block, so a verify result reads back to its item.
func (d DoneWhen) CheckName() string { return "done-when " + d.ID }

// DoneWhenIDForCheck reverses CheckName: the item id a check result
// belongs to, and whether it belongs to one at all.
func DoneWhenIDForCheck(name string) (string, bool) {
	id, ok := strings.CutPrefix(name, "done-when ")
	if !ok || !doneWhenIDRe.MatchString(id) {
		return "", false
	}
	return id, true
}

// GoalCardRow is one row of a goal doc's card list: a card the goal will
// run. A row with an ID names a card that already exists — minted from an
// earlier row, or an existing board card handed to the goal (attached).
type GoalCardRow struct {
	Title     string    `yaml:"title"`
	OneLiner  string    `yaml:"one_liner,omitempty"`
	Kind      string    `yaml:"kind,omitempty"`       // a CardType name: feature (default), bug, research or diagnosis
	Serves    []string  `yaml:"serves,omitempty"`     // done-when ids this card is for
	DependsOn []string  `yaml:"depends_on,omitempty"` // titles (or ids) of other rows
	Envelope  int       `yaml:"envelope,omitempty"`   // credits; 0 = the conductor splits
	ID        FeatureID `yaml:"id,omitempty"`         // set once minted or when attaching
	// Repo is the configured repository the row's card is minted into.
	// Empty is the workspace default, exactly as for a card created any
	// other way. A row that names an existing card (ID) ignores it: that
	// card already has a repository and it is not the row's to change.
	Repo string `yaml:"repo,omitempty"`
	// Live marks a card that proves itself on the substrate before it
	// lands: the goal makes a run of the experiment its items name, on the
	// goal's heads with this card's branch in place of the goal's. For the
	// few cards whose whole point is live behaviour — every other card is
	// proven with the rest, by the goal's integration runs.
	Live bool `yaml:"live,omitempty"`
}

// EffectiveType resolves the row's `kind:` to a card type, defaulting the
// blank to a feature. It is a CardType rather than a Kind because
// "diagnosis" is a name a row may write and is not a Kind — the row spells
// what the person would say, and the pair it resolves to is what the mint
// needs. ok is false for a name that is not offered at all.
func (r GoalCardRow) EffectiveType() (CardType, bool) {
	if strings.TrimSpace(r.Kind) == "" {
		return CardType{Kind: KindFeature}, true
	}
	return ParseCardType(r.Kind)
}

// EffectiveKind returns the kind the row mints, with the feature default
// resolved and an unrecognized name left as-is so Validate can name it.
func (r GoalCardRow) EffectiveKind() Kind {
	if ct, ok := r.EffectiveType(); ok {
		return ct.Kind
	}
	return Kind(r.Kind)
}

// Validate checks a row can become (or name) a goal card. Goals do not
// nest, so a row can never be a goal; every row must serve at least one
// done-when item, because a card that serves none is not the goal's work.
func (r GoalCardRow) Validate(items map[string]bool) error {
	name := r.Title
	if name == "" {
		name = string(r.ID)
	}
	if strings.TrimSpace(r.Title) == "" && r.ID == "" {
		return fmt.Errorf("a card row has neither a title nor an id")
	}
	if r.IsTBD() {
		return r.validateTBD(name, items)
	}
	ct, ok := r.EffectiveType()
	if !ok || ct.Kind == KindGoal {
		return fmt.Errorf("card %q: kind %q cannot run inside a goal (feature, bug, research or diagnosis)", name, r.Kind)
	}
	if r.ID != "" {
		if _, err := ParseFeatureID(string(r.ID)); err != nil {
			return fmt.Errorf("card %q: %w", name, err)
		}
		if r.ID.Kind() == KindGoal {
			return fmt.Errorf("card %q: goals do not nest", name)
		}
	}
	if len(r.Serves) == 0 {
		return fmt.Errorf("card %q serves no done-when item; a card that serves none is not the goal's work", name)
	}
	for _, s := range r.Serves {
		if !items[s] {
			return fmt.Errorf("card %q serves %s, which is not on the done-when list", name, s)
		}
	}
	if r.Envelope < 0 {
		return fmt.Errorf("card %q: negative envelope", name)
	}
	return nil
}

// GoalRowTBD is the kind of a row that is not a card yet: work the plan
// knows it will need and cannot describe until other rows have found
// something out.
const GoalRowTBD = "tbd"

// IsTBD reports whether the row is a deliberate unknown.
//
// A plan gate that demands every card be named demands, of work that
// begins with discovery, a list nobody can honestly write — and gets an
// invented one, which the budget is then committed to. A tbd row says the
// true thing instead: these items need more cards, which cards depends on
// what these rows find, and this much of the budget is held for them. It
// serves its items, so "every item is served" stays true without anyone
// pretending; its envelope is a tranche the ledger holds, as it holds a
// waiting card's; and the lead turns it into real cards, within that
// tranche, once what it waits for has landed.
func (r GoalCardRow) IsTBD() bool { return strings.EqualFold(strings.TrimSpace(r.Kind), GoalRowTBD) }

func (r GoalCardRow) validateTBD(name string, items map[string]bool) error {
	if r.ID != "" {
		return fmt.Errorf("card %q: a tbd row is not a card yet and cannot name one", name)
	}
	if len(r.Serves) == 0 {
		return fmt.Errorf("card %q serves no done-when item; an unknown that serves none is not the goal's work", name)
	}
	for _, s := range r.Serves {
		if !items[s] {
			return fmt.Errorf("card %q serves %s, which is not on the done-when list", name, s)
		}
	}
	if r.Envelope <= 0 {
		return fmt.Errorf("card %q: a tbd row holds part of the budget for cards nobody can name yet — give it an envelope, or the unknown is unbounded", name)
	}
	if len(r.DependsOn) == 0 {
		return fmt.Errorf("card %q: a tbd row waits for something to be found out — name the rows (depends_on) whose findings will say what its cards are", name)
	}
	if r.Live {
		return fmt.Errorf("card %q: a tbd row is not a card; mark the cards it becomes live when they are created", name)
	}
	return nil
}

// GoalReserveFloor is the smallest reserve a goal holds back: enough for
// its own critique and verify turns even on a small budget.
const GoalReserveFloor = 100

// DefaultGoalReserve is the reserve a goal holds before its lead has
// estimated one: 15% of the budget, never below GoalReserveFloor and never
// above the budget itself.
func DefaultGoalReserve(envelope int) int {
	r := envelope * 15 / 100
	if r < GoalReserveFloor {
		r = GoalReserveFloor
	}
	if envelope > 0 && r > envelope {
		r = envelope
	}
	return r
}

// GoalHeadroomPercent is the least share of a goal's budget (past its
// reserve) that minting its cards leaves ungiven: the pool its lead's turns
// and its later raises come out of. A goal that handed every credit to its
// cards up front would have nothing left to decide with.
const GoalHeadroomPercent = 10

// GoalLeadTurnsPerCard is how many lead turns the headroom plans for each
// card: its questions, its plan check, and a look when it lands or sticks.
// Held envelopes are not spent envelopes, so a headroom that ignores how
// many cards the lead tends starves the lead half-way through — and a lead
// with no room cannot answer, check a plan or rescue a stuck card.
const GoalLeadTurnsPerCard = 4

// GoalHeadroomMaxPercent caps the headroom, so a plan of many cards still
// gives most of the budget to the cards doing the work.
const GoalHeadroomMaxPercent = 30

// GoalMintPool is what a goal's plan may hand to its cards at the start:
// the budget less the reserve, what the goal has already spent, and the
// headroom kept for its lead and later raises — the larger of
// GoalHeadroomPercent and GoalLeadTurnsPerCard turns per card, never more
// than GoalHeadroomMaxPercent.
func (f *Feature) GoalMintPool(spent float64, cards int) float64 {
	pool := float64(f.Budget.Envelope-f.ReserveCredits()) - spent
	if pool <= 0 {
		return pool
	}
	headroom := max(pool*GoalHeadroomPercent/100, float64(cards*GoalLeadTurnsPerCard*TurnReserveCredits))
	headroom = min(headroom, pool*GoalHeadroomMaxPercent/100)
	return pool - headroom
}

// ReserveCredits returns the goal's reserve: the lead's estimate when it
// has made one, the default formula otherwise.
func (f *Feature) ReserveCredits() int {
	if f.Goal.Reserve > 0 {
		return f.Goal.Reserve
	}
	return DefaultGoalReserve(f.Budget.Envelope)
}

// GoalBranchName is the goal branch every card of the goal forks from and
// lands on — the goal card's own branch.
func (f *Feature) GoalBranchName() string { return f.BranchName() }
