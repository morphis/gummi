package spec

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

func goalCard() *domain.Feature {
	return &domain.Feature{ID: "GL-004", Num: 4, Kind: domain.KindGoal, Title: "Export works offline", Slug: "export-works-offline", Stage: domain.StageTodo}
}

func TestGoalTemplateSections(t *testing.T) {
	doc := SeededGoalTemplate(goalCard(), GoalSeed{Objective: "Exports must work with no network.\n%% sneaky"})
	want := []string{"Objective", "Done when", "Limits", "Budget", "Cards", "Notes", "Try it", "Review", "Verification plan", "Report"}
	if got := Headings(doc); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("headings = %v", got)
	}
	obj, _ := ViewSection(doc, GoalSectionObjective)
	if !strings.Contains(obj, "Exports must work with no network.") || strings.Contains(obj, "\n%% sneaky") {
		t.Fatalf("objective not seeded or marker not neutralized: %q", obj)
	}
	// the blank scaffolds parse as empty lists, not errors
	items, found, err := ParseDoneWhen(doc)
	if err != nil || !found || len(items) != 0 {
		t.Fatalf("scaffold done-when: %v %v %v", items, found, err)
	}
	rows, found, err := ParseGoalCards(doc, nil)
	if err != nil || !found || len(rows) != 0 {
		t.Fatalf("scaffold cards: %v %v %v", rows, found, err)
	}
	lanes, err := ParseGoalLanes(doc)
	if err != nil || lanes != 2 {
		t.Fatalf("scaffold lanes = %d, %v", lanes, err)
	}
	if blankTemplate(goalCard()) != GoalTemplate(goalCard()) {
		t.Fatalf("a goal's blank artifact is the goal template")
	}
}

const filledGoal = "# GL-004: Export works offline\n\n" +
	"## Objective\n\nExport with no network.\n\n" +
	"## Done when\n\n```gummi-done-when\n" +
	"- id: DW-1\n  says: export runs with no network\n  check: make offline-test\n" +
	"%% @reviewer: is this check real?\n" +
	"- id: DW-2\n  says: the docs explain the cache\n  judge: true\n```\n\n" +
	"## Limits\n\nNo new dependencies.\n\n" +
	"## Budget\n\nAbout 1200 credits.\n\n```gummi-goal\nlanes: 3\n```\n\n" +
	"## Cards\n\n```gummi-cards\n# field guide\n" +
	"- title: local cache for export\n  serves: [DW-1]\n  envelope: 600\n" +
	"- title: document the cache\n  serves: [DW-2]\n  depends_on: [local cache for export]\n" +
	"- title: old flaky test\n  id: BG-012\n  kind: bug\n  serves: [DW-1]\n```\n\n" +
	"## Notes\n\n" + promptGoalNotes + "\n\n" +
	"## Verification plan\n\n\n"

func TestParseFilledGoal(t *testing.T) {
	items, found, err := ParseDoneWhen(filledGoal)
	if err != nil || !found {
		t.Fatalf("done-when: %v %v", found, err)
	}
	if len(items) != 2 || items[0].Check != "make offline-test" || !items[1].Judge {
		t.Fatalf("items = %+v", items)
	}
	rows, _, err := ParseGoalCards(filledGoal, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Envelope != 600 || rows[1].DependsOn[0] != "local cache for export" || rows[2].ID != "BG-012" || rows[2].EffectiveKind() != domain.KindBug {
		t.Fatalf("rows = %+v", rows)
	}
	if lanes, _ := ParseGoalLanes(filledGoal); lanes != 3 {
		t.Fatalf("lanes = %d", lanes)
	}

	rows[0].ID = "FD-005"
	rows[1].ID = "FD-006"
	doc, err := SetGoalCards(filledGoal, rows)
	if err != nil {
		t.Fatal(err)
	}
	back, _, err := ParseGoalCards(doc, items)
	if err != nil {
		t.Fatal(err)
	}
	if back[0].ID != "FD-005" || back[1].ID != "FD-006" || back[2].ID != "BG-012" {
		t.Fatalf("ids not written back: %+v", back)
	}
	body, _ := ViewSection(doc, GoalSectionCards)
	if !strings.Contains(body, "# field guide") {
		t.Fatalf("the block's comment preamble must survive a rewrite: %q", body)
	}
	if !strings.Contains(doc, "## Limits\n\nNo new dependencies.") {
		t.Fatalf("other sections must be untouched")
	}

	items = append(items, domain.DoneWhen{ID: "DW-3", Says: "works on Windows paths", Check: "go test ./winpath"})
	doc, err = SetDoneWhen(doc, items)
	if err != nil {
		t.Fatal(err)
	}
	if again, _, err := ParseDoneWhen(doc); err != nil || len(again) != 3 {
		t.Fatalf("done-when rewrite: %+v %v", again, err)
	}
}

func TestParseGoalRefusals(t *testing.T) {
	cases := map[string]string{
		"duplicate id": "## Done when\n\n```gummi-done-when\n- id: DW-1\n  says: a\n  check: 'true'\n- id: DW-1\n  says: b\n  check: 'true'\n```\n",
		"no check":     "## Done when\n\n```gummi-done-when\n- id: DW-1\n  says: a\n```\n",
		"bad yaml":     "## Done when\n\n```gummi-done-when\n- id: [\n```\n",
	}
	for name, doc := range cases {
		if _, _, err := ParseDoneWhen(doc); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	items := []domain.DoneWhen{{ID: "DW-1", Says: "a", Check: "true"}}
	cardCases := map[string]string{
		"unknown dep":   "## Cards\n\n```gummi-cards\n- title: a\n  serves: [DW-1]\n  depends_on: [nope]\n```\n",
		"dup title":     "## Cards\n\n```gummi-cards\n- title: a\n  serves: [DW-1]\n- title: A\n  serves: [DW-1]\n```\n",
		"serves none":   "## Cards\n\n```gummi-cards\n- title: a\n```\n",
		"nested goal":   "## Cards\n\n```gummi-cards\n- title: a\n  kind: goal\n  serves: [DW-1]\n```\n",
		"unknown serve": "## Cards\n\n```gummi-cards\n- title: a\n  serves: [DW-7]\n```\n",
	}
	for name, doc := range cardCases {
		if _, _, err := ParseGoalCards(doc, items); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, found, _ := ParseGoalCards("## Cards\n\nnothing\n", items); found {
		t.Fatalf("no block is not found")
	}
}

func TestAppendGoalNote(t *testing.T) {
	doc, err := AppendGoalNote(filledGoal, "also make it\nwork on Windows %% trick", "10:40")
	if err != nil {
		t.Fatal(err)
	}
	doc, err = AppendGoalNote(doc, "keep the flag name", "")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := ViewSection(doc, GoalSectionNotes)
	if strings.Contains(body, promptGoalNotes) {
		t.Fatalf("the prompt must go once a note exists: %q", body)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 2 || lines[0] != "- (10:40) also make it work on Windows %% trick" || lines[1] != "- keep the flag name" {
		t.Fatalf("notes = %q", lines)
	}
	if len(Parse(doc).UserOpenThreads()) != 0 {
		t.Fatalf("a note must never open a thread")
	}
}

func TestParseGoalProgramme(t *testing.T) {
	doc := "## Budget\n\n```gummi-goal\nlanes: 2\nafter: gl-003\nland_order: [ovn, microovn, home]\n```\n"
	after, order, err := ParseGoalProgramme(doc)
	if err != nil || after != "gl-003" || len(order) != 3 || order[0] != "ovn" || order[2] != "" {
		t.Fatalf("%q %v %v", after, order, err)
	}
	if _, _, err := ParseGoalProgramme("## Budget\n\n```gummi-goal\nland_order: [ovn, ovn]\n```\n"); err == nil {
		t.Fatal("a repository lands once")
	}
	if after, order, err := ParseGoalProgramme("no block at all"); err != nil || after != "" || order != nil {
		t.Fatal("a goal that says nothing of the kind stands alone")
	}
	b, every, err := ParseGoalSubstrate("## Budget\n\n```gummi-goal\nruns: 12\nminutes: 600\nintegrate_every: 3\n```\n")
	if err != nil || !b.Agreed() || b.Runs != 12 || b.Minutes != 600 || every != 3 {
		t.Fatalf("%+v %d %v", b, every, err)
	}
	if _, _, err := ParseGoalSubstrate("## Budget\n\n```gummi-goal\nruns: -1\n```\n"); err == nil {
		t.Fatal("negative budgets are refused")
	}
}
