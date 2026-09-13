package spec

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/morphis/gummi/internal/domain"
)

// The goal doc (domain.KindGoal) is the context carrier for a goal: the
// objective and done-when list agreed in the plan conversation, the card
// list the goal runs, your notes, the try-it guide, and the generated
// hand-over report. Three fenced blocks carry the parts gummi acts on:
//
//	```gummi-done-when   under ## Done when — the checkable items
//	```gummi-goal        under ## Budget    — the lane count
//	```gummi-cards       under ## Cards     — one row per card
//
// Like gummi-checks they are strict-YAML islands inside prose several roles
// rewrite, so they parse forgivingly: %% marker lines that land inside the
// fence are dropped before decoding.

// Goal doc section titles, in contract order.
const (
	GoalSectionObjective = "Objective"
	GoalSectionDoneWhen  = "Done when"
	GoalSectionLimits    = "Limits"
	GoalSectionBudget    = "Budget"
	GoalSectionCards     = "Cards"
	GoalSectionNotes     = "Notes"
	GoalSectionTryIt     = "Try it"
	GoalSectionReview    = "Review"
	GoalSectionVerify    = "Verification plan"
	GoalSectionReport    = "Report"
)

const (
	promptGoalObjective = "%% @gummi: the outcome, in the requester's own words — what will be true when this goal is met"
	promptGoalDoneWhen  = "%% @gummi: the checkable statements that say the goal is met — one gummi-done-when row each, with a `check:` command or `judge: true`"
	promptGoalLimits    = "%% @gummi: out of scope, constraints, and things not to touch"
	promptGoalBudget    = "%% @gummi: a rough cost range per done-when item, and a plain warning when the budget looks too small; the lanes line says how many cards may run at once"
	promptGoalCards     = "%% @gummi: the cards this goal runs — one gummi-cards row each, every row serving at least one done-when item"
	promptGoalNotes     = "%% @gummi: notes you type into the goal land here; the lead reads them on its next turn"
	promptGoalTryIt     = "%% @gummi: short steps to see the result working — commands to run and what you should see; if nothing is visible, say so and point at the done-when checks"
	promptGoalReview    = "%% @gummi: the goal review's findings on the combined change land here"
	promptGoalVerify    = "%% @gummi: the repo's build/test/lint commands and each done-when item's check land here as a gummi-checks block at approval" + checksShape + "; add live checks for the judged done-when items and run the try-it guide"
	promptGoalReport    = "%% @gummi: generated when the goal is ready for you — done-when report, decisions for review, declined findings, found along the way, spend"
)

// doneWhenScaffold and cardsScaffold ship with the blank goal doc so the
// architect never improvises the block shapes.
const (
	doneWhenScaffold = "```gummi-done-when\n" +
		"# id / says / check (a command) or judge: true\n" +
		"- id: DW-1\n" +
		"  says: \"\"\n" +
		"  check: \"\"\n" +
		"```"
	goalBudgetScaffold = "```gummi-goal\n" +
		"lanes: 2\n" +
		"```"
	cardsScaffold = "```gummi-cards\n" +
		"# title / one_liner / kind (feature, bug, research) / serves / depends_on / envelope / id (set when minted or attached)\n" +
		"- title: \"\"\n" +
		"  one_liner: \"\"\n" +
		"  serves: []\n" +
		"```"
)

// GoalSeed is what a creation surface can seed into a goal doc: the
// objective, verbatim. Everything else is the plan conversation's work.
type GoalSeed struct {
	Objective string
}

// GoalTemplate renders a blank goal doc.
func GoalTemplate(f *domain.Feature) string { return renderGoal(f, GoalSeed{}) }

// SeededGoalTemplate renders a goal doc with the objective filled in.
func SeededGoalTemplate(f *domain.Feature, seed GoalSeed) string { return renderGoal(f, seed) }

func renderGoal(f *domain.Feature, seed GoalSeed) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: %s\n\n", f.ID, f.Title)
	if f.OneLiner != "" {
		fmt.Fprintf(&b, "> %s\n\n", f.OneLiner)
	}
	section(&b, GoalSectionObjective, neutralizeMarkers(seed.Objective), promptGoalObjective)
	section(&b, GoalSectionDoneWhen, promptGoalDoneWhen+"\n\n"+doneWhenScaffold, "")
	section(&b, GoalSectionLimits, "", promptGoalLimits)
	section(&b, GoalSectionBudget, promptGoalBudget+"\n\n"+goalBudgetScaffold, "")
	section(&b, GoalSectionCards, promptGoalCards+"\n\n"+cardsScaffold, "")
	section(&b, GoalSectionNotes, "", promptGoalNotes)
	section(&b, GoalSectionTryIt, "", promptGoalTryIt)
	section(&b, GoalSectionReview, "", promptGoalReview)
	section(&b, GoalSectionVerify, "", promptGoalVerify)
	section(&b, GoalSectionReport, "", promptGoalReport)
	return strings.TrimRight(b.String(), "\n") + "\n"
}

var (
	doneWhenFenceRe = regexp.MustCompile("(?s)```gummi-done-when[ \\t]*\\n(.*?)```")
	cardsFenceRe    = regexp.MustCompile("(?s)```gummi-cards[ \\t]*\\n(.*?)```")
	goalFenceRe     = regexp.MustCompile("(?s)```gummi-goal[ \\t]*\\n(.*?)```")
)

// fenceBody finds the first block matching re in the named section,
// falling back to anywhere in the document so a block that drifted out of
// its heading still counts. It returns the body with %% lines dropped.
func fenceBody(content, sectionName string, re *regexp.Regexp) (string, bool) {
	hay := content
	if body, ok := ViewSection(content, sectionName); ok && re.MatchString(body) {
		hay = body
	}
	m := re.FindStringSubmatch(hay)
	if m == nil {
		return "", false
	}
	var keep []string
	for _, line := range strings.Split(m[1], "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "%%") {
			continue
		}
		keep = append(keep, strings.ReplaceAll(line, "\t", "  "))
	}
	return strings.Join(keep, "\n"), true
}

// isBlankDoneWhen is the untouched scaffold row (or a row left empty).
func isBlankDoneWhen(d domain.DoneWhen) bool {
	return strings.TrimSpace(d.Says) == "" && strings.TrimSpace(d.Check) == "" && !d.Judge
}

// ParseDoneWhen reads the goal doc's done-when list. found is false when
// the doc has no gummi-done-when block at all. Blank rows (the scaffold)
// are skipped; every other row is validated, and duplicate ids refused.
func ParseDoneWhen(content string) (items []domain.DoneWhen, found bool, err error) {
	body, ok := fenceBody(content, GoalSectionDoneWhen, doneWhenFenceRe)
	if !ok {
		return nil, false, nil
	}
	var rows []domain.DoneWhen
	if err := yaml.Unmarshal([]byte(body), &rows); err != nil {
		return nil, true, fmt.Errorf("the gummi-done-when block does not parse: %w", err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if isBlankDoneWhen(r) {
			continue
		}
		r.Says = strings.TrimSpace(r.Says)
		r.Check = strings.TrimSpace(r.Check)
		if err := r.Validate(); err != nil {
			return nil, true, err
		}
		if seen[r.ID] {
			return nil, true, fmt.Errorf("done-when %s appears twice", r.ID)
		}
		seen[r.ID] = true
		items = append(items, r)
	}
	return items, true, nil
}

// goalBlock is the gummi-goal block's shape.
type goalBlock struct {
	Lanes int `yaml:"lanes"`
}

// ParseGoalLanes reads the lane count from the goal doc's gummi-goal
// block, 0 when there is none (the default applies).
func ParseGoalLanes(content string) (int, error) {
	body, ok := fenceBody(content, GoalSectionBudget, goalFenceRe)
	if !ok {
		return 0, nil
	}
	var g goalBlock
	if err := yaml.Unmarshal([]byte(body), &g); err != nil {
		return 0, fmt.Errorf("the gummi-goal block does not parse: %w", err)
	}
	if g.Lanes < 0 {
		return 0, fmt.Errorf("the gummi-goal block names %d lanes; use a positive number", g.Lanes)
	}
	return g.Lanes, nil
}

func isBlankCardRow(r domain.GoalCardRow) bool {
	return strings.TrimSpace(r.Title) == "" && r.ID == "" && len(r.Serves) == 0
}

// ParseGoalCards reads the goal doc's card list. found is false when the
// doc has no gummi-cards block. Blank rows are skipped; the rest are
// validated against the done-when ids in items, titles must be unique, and
// every depends_on must name another row by title or id.
func ParseGoalCards(content string, items []domain.DoneWhen) (rows []domain.GoalCardRow, found bool, err error) {
	body, ok := fenceBody(content, GoalSectionCards, cardsFenceRe)
	if !ok {
		return nil, false, nil
	}
	var raw []domain.GoalCardRow
	if err := yaml.Unmarshal([]byte(body), &raw); err != nil {
		return nil, true, fmt.Errorf("the gummi-cards block does not parse: %w", err)
	}
	ids := map[string]bool{}
	for _, it := range items {
		ids[it.ID] = true
	}
	names := map[string]bool{}
	for _, r := range raw {
		if isBlankCardRow(r) {
			continue
		}
		r.Title = strings.TrimSpace(r.Title)
		if err := r.Validate(ids); err != nil {
			return nil, true, err
		}
		key := strings.ToLower(r.Title)
		if key != "" {
			if names[key] {
				return nil, true, fmt.Errorf("two cards are titled %q", r.Title)
			}
			names[key] = true
		}
		if r.ID != "" {
			names[strings.ToLower(string(r.ID))] = true
		}
		rows = append(rows, r)
	}
	for _, r := range rows {
		for _, d := range r.DependsOn {
			if !names[strings.ToLower(strings.TrimSpace(d))] {
				return nil, true, fmt.Errorf("card %q depends on %q, which is not on the card list", r.Title, d)
			}
		}
	}
	return rows, true, nil
}

// SetGoalCards rewrites the goal doc's gummi-cards block with rows,
// keeping any leading comment lines of the existing block. The minted and
// attached ids are written back this way, so the doc and the store agree
// on which card a row became.
func SetGoalCards(content string, rows []domain.GoalCardRow) (string, error) {
	return rewriteFence(content, GoalSectionCards, cardsFenceRe, "gummi-cards", rows)
}

// SetDoneWhen rewrites the goal doc's gummi-done-when block with items.
func SetDoneWhen(content string, items []domain.DoneWhen) (string, error) {
	return rewriteFence(content, GoalSectionDoneWhen, doneWhenFenceRe, "gummi-done-when", items)
}

// rewriteFence replaces the block matched by re (in the named section, or
// anywhere) with v rendered as YAML; when the doc has no such block it is
// appended to the section.
func rewriteFence(content, sectionName string, re *regexp.Regexp, lang string, v any) (string, error) {
	out, err := yaml.Marshal(v)
	if err != nil {
		return "", err
	}
	body, ok := ViewSection(content, sectionName)
	if !ok {
		return "", fmt.Errorf("the goal doc has no ## %s section", sectionName)
	}
	loc := re.FindStringSubmatchIndex(body)
	if loc == nil {
		nb := strings.TrimRight(body, "\n") + "\n\n```" + lang + "\n" + string(out) + "```\n"
		newContent, _, err := ReplaceSection(content, sectionName, nb)
		return newContent, err
	}
	fence := body[loc[2]:loc[3]]
	var preamble strings.Builder
	for _, line := range strings.Split(fence, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			preamble.WriteString(line + "\n")
			continue
		}
		break
	}
	nb := body[:loc[2]] + preamble.String() + string(out) + body[loc[3]:]
	newContent, _, err := ReplaceSection(content, sectionName, nb)
	return newContent, err
}

// AppendGoalNote adds one of your notes to the goal doc's Notes section,
// replacing the section's prompt the first time. The note is flattened to
// one bullet and marker-neutralized, so a note can never open or close a
// %% thread.
func AppendGoalNote(content, note, stamp string) (string, error) {
	note = oneLine(neutralizeMarkers(note))
	if note == "" {
		return content, nil
	}
	body, ok := ViewSection(content, GoalSectionNotes)
	if !ok {
		return "", fmt.Errorf("the goal doc has no ## %s section", GoalSectionNotes)
	}
	var keep []string
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == promptGoalNotes {
			continue
		}
		keep = append(keep, line)
	}
	nb := strings.TrimSpace(strings.Join(keep, "\n"))
	line := "- " + note
	if stamp != "" {
		line = "- (" + stamp + ") " + note
	}
	if nb != "" {
		nb += "\n"
	}
	newContent, _, err := ReplaceSection(content, GoalSectionNotes, nb+line+"\n")
	return newContent, err
}
