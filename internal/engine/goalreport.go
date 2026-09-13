package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/goalpolicy"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
)

// The hand-over: what a goal gives you when it is ready. Every surface —
// the goal page, `gummi status --json`, the done event, and the goal doc's
// own Report section — renders this one structure, so they cannot
// disagree about which done-when items were met.

// Done-when statuses.
const (
	DoneWhenMet     = "met"
	DoneWhenNotMet  = "not met"
	DoneWhenUnknown = "not checked"
)

// DoneWhenStatus is one done-when item at the hand-over.
type DoneWhenStatus struct {
	ID       string `json:"id"`
	Says     string `json:"says"`
	How      string `json:"how"` // "check: <cmd>" or "judged"
	Status   string `json:"status"`
	Evidence string `json:"evidence,omitempty"`
}

// GoalReportCard is one card at the hand-over.
type GoalReportCard struct {
	ID       domain.FeatureID `json:"id"`
	Kind     domain.Kind      `json:"kind"`
	Title    string           `json:"title"`
	State    string           `json:"state"`
	Stage    domain.Stage     `json:"stage"`
	Envelope int              `json:"envelope"`
	Spent    float64          `json:"spent"`
	Serves   []string         `json:"serves,omitempty"`
	Commit   string           `json:"commit,omitempty"` // the landed commit on the goal branch
	Subject  string           `json:"subject,omitempty"`
	Reason   string           `json:"reason,omitempty"`
	Attached bool             `json:"attached,omitempty"`
}

// GoalLogLine is a log entry as the report lists it.
type GoalLogLine struct {
	Ref         string           `json:"ref,omitempty"` // D-N for decisions
	Card        domain.FeatureID `json:"card,omitempty"`
	Item        string           `json:"item,omitempty"`
	Detail      string           `json:"detail"`
	Alternative string           `json:"alternative,omitempty"`
	Finding     string           `json:"finding,omitempty"`
	By          string           `json:"by,omitempty"`
}

// GoalReport is the hand-over of one goal.
type GoalReport struct {
	ID         domain.FeatureID `json:"id"`
	Title      string           `json:"title"`
	Stage      domain.Stage     `json:"stage"`
	Ready      bool             `json:"ready"` // verified: waiting for you
	Partial    string           `json:"partial,omitempty"`
	WrappingUp bool             `json:"wrapping_up,omitempty"`
	Budget     GoalReportBudget `json:"budget"`
	Lanes      int              `json:"lanes"`
	DoneWhen   []DoneWhenStatus `json:"done_when"`
	Cards      []GoalReportCard `json:"cards"`
	Decisions  []GoalLogLine    `json:"decisions,omitempty"`
	Declined   []GoalLogLine    `json:"declined_findings,omitempty"`
	Found      []GoalLogLine    `json:"found_along_the_way,omitempty"`
	TryIt      string           `json:"try_it,omitempty"`
}

// GoalReportBudget is the budget tree at the hand-over.
type GoalReportBudget struct {
	Envelope  int     `json:"envelope"`
	Own       float64 `json:"goal_spend"`
	Given     float64 `json:"held_by_cards"`
	CardSpent float64 `json:"card_spend"`
	Reserve   int     `json:"reserve"`
	Available float64 `json:"left_to_give"`
	Total     float64 `json:"total_spend"`
}

// Met counts the met done-when items.
func (r GoalReport) Met() (met, total int) {
	for _, d := range r.DoneWhen {
		if d.Status == DoneWhenMet {
			met++
		}
	}
	return met, len(r.DoneWhen)
}

var judgedLineRe = regexp.MustCompile(`(?mi)^\s*[-*]?\s*(DW-[0-9]+)\s*[:—-]+\s*(met|not met)\b[\s:—-]*(.*)$`)

// GoalReport builds a goal's hand-over from its view, its log and its doc.
func (e *Engine) GoalReport(ctx context.Context, goalID domain.FeatureID) (GoalReport, error) {
	view, err := e.GoalView(ctx, goalID)
	if err != nil {
		return GoalReport{}, err
	}
	return buildGoalReport(view), nil
}

func buildGoalReport(v GoalView) GoalReport {
	g := v.Goal
	r := GoalReport{
		ID: g.ID, Title: g.Title, Stage: g.Stage, Partial: g.Goal.Partial,
		Ready:      g.Stage == domain.StageVerify && !g.VerifiedAt.IsZero(),
		WrappingUp: g.Goal.WrappingUp(), Lanes: g.Goal.LaneCount(),
	}
	r.Budget = GoalReportBudget{
		Envelope: v.Ledger.Envelope, Own: v.Ledger.Own, Given: v.Ledger.Given,
		Reserve: v.Ledger.Reserve, Available: max(0, v.Ledger.Available),
	}

	landedSubject := map[domain.FeatureID]string{}
	var lastChecks []goalCheckResult
	notMet := map[string]string{}
	for _, en := range v.Log {
		switch en.Action {
		case state.GoalLanded:
			landedSubject[en.Card] = en.Detail
		case state.GoalChecks:
			var rs []goalCheckResult
			if json.Unmarshal([]byte(en.Detail), &rs) == nil {
				lastChecks = rs
			}
		case state.GoalNotMet:
			notMet[en.Item] = en.Detail
		case state.GoalDecision:
			r.Decisions = append(r.Decisions, GoalLogLine{Ref: en.DecisionRef(), Card: en.Card, Item: en.Item, Detail: en.Detail, Alternative: en.Alternative, By: en.By})
		case state.GoalDeclined:
			r.Declined = append(r.Declined, GoalLogLine{Card: en.Card, Finding: en.Ref, Detail: en.Detail, By: en.By})
		case state.GoalFound:
			r.Found = append(r.Found, GoalLogLine{Card: en.Card, Detail: en.Detail, By: en.By})
		}
	}

	servedBy := map[string][]GoalCard{}
	for _, c := range v.Cards {
		rc := GoalReportCard{
			ID: c.Feature.ID, Kind: c.Feature.Kind, Title: c.Feature.Title, State: c.State.String(),
			Stage: c.Feature.Stage, Envelope: c.Envelope, Spent: c.Spent, Serves: c.Serves,
			Reason: c.Reason, Attached: c.Feature.GoalAttached,
		}
		if c.State == goalpolicy.Landed {
			rc.Commit = c.Feature.LandedSHA
			if s := landedSubject[c.Feature.ID]; s != "" {
				_, subj, _ := strings.Cut(s, " ")
				rc.Subject = subj
			}
		}
		r.Budget.CardSpent += c.Spent
		r.Cards = append(r.Cards, rc)
		for _, s := range c.Serves {
			servedBy[s] = append(servedBy[s], c)
		}
	}
	r.Budget.Total = r.Budget.Own + r.Budget.CardSpent

	checkByName := map[string]goalCheckResult{}
	for _, c := range lastChecks {
		checkByName[c.Name] = c
	}
	verification, tryIt := "", ""
	if v.DocPath != "" {
		if raw, err := os.ReadFile(v.DocPath); err == nil {
			verification, _ = spec.ViewSection(string(raw), spec.GoalSectionVerify)
			tryIt, _ = spec.ViewSection(string(raw), spec.GoalSectionTryIt)
		}
	}
	judged := map[string][2]string{}
	for _, m := range judgedLineRe.FindAllStringSubmatch(verification, -1) {
		judged[strings.ToUpper(m[1])] = [2]string{strings.ToLower(m[2]), strings.TrimSpace(m[3])}
	}
	r.TryIt = strings.TrimSpace(stripPrompt(tryIt))

	for _, d := range v.DoneWhen {
		st := DoneWhenStatus{ID: d.ID, Says: d.Says, How: "judged", Status: DoneWhenUnknown}
		if d.Check != "" {
			st.How = "check: " + d.Check
			if res, ok := checkByName[d.CheckName()]; ok {
				if res.OK {
					st.Status, st.Evidence = DoneWhenMet, "check passed"
				} else {
					st.Status, st.Evidence = DoneWhenNotMet, "check "+res.Status
				}
			}
		} else if j, ok := judged[d.ID]; ok {
			st.Status, st.Evidence = j[0], j[1]
		}
		if cards := servedBy[d.ID]; len(cards) > 0 && st.Status != DoneWhenMet {
			allDropped := true
			for _, c := range cards {
				if c.State != goalpolicy.Dropped {
					allDropped = false
				}
			}
			if allDropped {
				st.Status, st.Evidence = DoneWhenNotMet, "every card serving it was dropped"
			}
		}
		if why, ok := notMet[d.ID]; ok {
			st.Status, st.Evidence = DoneWhenNotMet, why
		}
		r.DoneWhen = append(r.DoneWhen, st)
	}
	return r
}

// stripPrompt drops the template's %% prompt lines from a section body.
func stripPrompt(body string) string {
	var keep []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "%%") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}

// RenderGoalReport renders a hand-over as the goal doc's Report section.
func RenderGoalReport(r GoalReport) string {
	var b strings.Builder
	met, total := r.Met()
	state := "ready for you"
	if r.Partial != "" {
		state = "ready for you — partial: " + r.Partial
	}
	fmt.Fprintf(&b, "%s. %d of %d done-when items met.\n\n", capitalize(state), met, total)

	b.WriteString("### Done when\n\n")
	for _, d := range r.DoneWhen {
		mark := "✗"
		switch d.Status {
		case DoneWhenMet:
			mark = "✓"
		case DoneWhenUnknown:
			mark = "?"
		}
		fmt.Fprintf(&b, "- %s %s — %s (%s)", mark, d.ID, d.Says, d.Status)
		if d.Evidence != "" {
			b.WriteString(": " + d.Evidence)
		}
		b.WriteString("\n")
	}

	b.WriteString("\n### Cards\n\n")
	for _, c := range r.Cards {
		fmt.Fprintf(&b, "- %s %s — %s", c.ID, c.Title, c.State)
		if c.Commit != "" {
			fmt.Fprintf(&b, " as %s", shortSHA(c.Commit))
		}
		if c.Reason != "" && c.State != "landed" {
			b.WriteString(": " + c.Reason)
		}
		fmt.Fprintf(&b, " · %.0f of %d credits\n", c.Spent, c.Envelope)
	}

	if len(r.Decisions) > 0 {
		b.WriteString("\n### Decisions for review\n\n")
		for _, d := range r.Decisions {
			fmt.Fprintf(&b, "- %s %s", d.Ref, d.Detail)
			if d.Alternative != "" {
				b.WriteString(" (not: " + d.Alternative + ")")
			}
			if d.Card != "" {
				b.WriteString(" — " + string(d.Card))
			}
			b.WriteString("\n")
		}
	}
	if len(r.Declined) > 0 {
		b.WriteString("\n### Declined findings\n\n")
		for _, d := range r.Declined {
			fmt.Fprintf(&b, "- %s: %s — declined: %s\n", d.Card, d.Finding, d.Detail)
		}
	}
	if len(r.Found) > 0 {
		b.WriteString("\n### Found along the way\n\n")
		for _, d := range r.Found {
			fmt.Fprintf(&b, "- %s %s\n", d.Card, d.Detail)
		}
	}
	fmt.Fprintf(&b, "\n### Spend\n\n- budget %d · goal %.0f · cards %.0f · total %.0f · reserve %d\n",
		r.Budget.Envelope, r.Budget.Own, r.Budget.CardSpent, r.Budget.Total, r.Budget.Reserve)
	return b.String()
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// WriteGoalReport writes a goal's hand-over into its doc's Report section.
// Best-effort: the report is a rendering of facts the store already holds.
func (e *Engine) WriteGoalReport(ctx context.Context, goalID domain.FeatureID) (GoalReport, error) {
	r, err := e.GoalReport(ctx, goalID)
	if err != nil {
		return r, err
	}
	goal, err := e.cfg.Store.GetFeature(ctx, goalID)
	if err != nil {
		return r, err
	}
	path := e.artifactFile(&goal)
	if path == "" {
		return r, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	doc, _, err := spec.ReplaceSection(string(raw), spec.GoalSectionReport, RenderGoalReport(r))
	if err != nil {
		return r, err
	}
	return r, atomicfile.Write(path, []byte(doc), 0o600)
}
