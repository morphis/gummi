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
	if strings.TrimSpace(d.Check) == "" && !d.Judge {
		return fmt.Errorf("done-when %s has no way to check it: give it a check command or judge: true", d.ID)
	}
	if strings.TrimSpace(d.Check) != "" && d.Judge {
		return fmt.Errorf("done-when %s has both a check and judge: true; pick one", d.ID)
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
	Kind      Kind      `yaml:"kind,omitempty"`       // feature (default), bug or research
	Serves    []string  `yaml:"serves,omitempty"`     // done-when ids this card is for
	DependsOn []string  `yaml:"depends_on,omitempty"` // titles (or ids) of other rows
	Envelope  int       `yaml:"envelope,omitempty"`   // credits; 0 = the conductor splits
	ID        FeatureID `yaml:"id,omitempty"`         // set once minted or when attaching
}

// EffectiveKind returns the row's kind with the feature default resolved.
func (r GoalCardRow) EffectiveKind() Kind {
	if r.Kind == "" {
		return KindFeature
	}
	return r.Kind
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
	k := r.EffectiveKind()
	if !k.Valid() || k == KindGoal {
		return fmt.Errorf("card %q: kind %q cannot run inside a goal (feature, bug or research)", name, r.Kind)
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
