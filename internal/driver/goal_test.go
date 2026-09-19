package driver

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/experiment"
	"github.com/morphis/gummi/internal/state"
)

const driverGoalDoc = "# goal\n\n" +
	"## Objective\n\nExport works offline.\n\n" +
	"## Done when\n\n```gummi-done-when\n" +
	"- id: DW-1\n  says: the cache file exists\n  check: test -f cache.txt\n" +
	"- id: DW-2\n  says: the flag file exists\n  check: test -f flag.txt\n```\n\n" +
	"## Limits\n\nNone.\n\n" +
	"## Budget\n\nFine.\n\n```gummi-goal\nlanes: 2\n```\n\n" +
	"## Cards\n\n```gummi-cards\n" +
	"- title: local cache\n  serves: [DW-1]\n" +
	"- title: offline flag\n  serves: [DW-2]\n  depends_on: [local cache]\n```\n\n" +
	"## Notes\n\n\n## Try it\n\nRun it.\n\n## Review\n\n\n## Verification plan\n\nThe checks.\n\n## Report\n\n\n"

// cardOfSession names the card a fake session works for: its worktree's
// directory is the card id.
func cardOfSession(opts agent.SessionOpts) domain.FeatureID {
	if id, err := domain.ParseFeatureID(filepath.Base(opts.WorkDir)); err == nil {
		return id
	}
	return domain.FeatureID(opts.FeatureID)
}

// goalHarness scripts a whole goal: every writer drafts what its gate
// needs, each card's implementer writes the file its done-when check looks
// for, and every critique and verify passes.
func goalHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, true, nil)
	files := map[string]string{"local cache": "cache.txt", "offline flag": "flag.txt"}
	h.fake.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		ctx := context.Background()
		id := cardOfSession(opts)
		f, err := h.store.GetFeature(ctx, id)
		if err != nil {
			return []agent.Event{{Kind: agent.EventIdle}}
		}
		switch opts.Role {
		case agent.RoleReviewer:
			h.draftRequiredSections(f)
			return toolVerdict(opts.Model, "pass")
		case agent.RoleLead, agent.RoleScribe:
			return msgIdle(opts.Model, "nothing to do")
		}
		h.draftRequiredSections(f)
		if f.Stage == domain.StageImplement && !f.IsGoal() {
			if name := files[f.Title]; name != "" {
				_ = os.WriteFile(filepath.Join(opts.WorkDir, name), []byte("x\n"), 0o600)
			}
		}
		if f.Stage == domain.StageVerify {
			h.draftRequiredSections(f)
		}
		return msgIdle(opts.Model, "done")
	}
	return h
}

func TestDriveGoalEndToEnd(t *testing.T) {
	h := goalHarness(t)
	ctx := context.Background()
	d := h.driver(Options{Envelope: 4000, Autonomous: true, GoalDoc: driverGoalDoc})
	g, err := d.Create(ctx, domain.CardType{Kind: domain.KindGoal}, "Export works offline")
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Drive(ctx, g)
	if err != nil {
		t.Fatalf("drive: %v\n%s", err, h.buf.String())
	}
	if out.Status != StatusVerified {
		t.Fatalf("status = %s\n%s", out.Status, h.buf.String())
	}

	got, _ := h.store.GetFeature(ctx, g.ID)
	if got.Stage != domain.StageVerify || got.VerifiedAt.IsZero() {
		t.Fatalf("the goal stops ready for you: %+v", got)
	}
	cards, _ := h.store.ListGoalCards(ctx, g.ID)
	if len(cards) != 2 {
		t.Fatalf("cards = %+v", cards)
	}
	for _, c := range cards {
		if c.Stage != domain.StageDone || c.LandedSHA == "" {
			t.Fatalf("%s should have landed on the goal branch: %+v", c.ID, c)
		}
	}
	for _, name := range []string{"cache.txt", "flag.txt"} {
		if _, err := os.Stat(filepath.Join(h.root, g.WorktreePath(), name)); err != nil {
			t.Fatalf("%s is on the goal branch: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(h.root, name)); err == nil {
			t.Fatalf("%s must not reach main before the goal lands", name)
		}
	}

	// the stream: the cards emit `card_verified` (a notification, not a
	// terminal event), the goal ticked, and the single terminal `verified`
	// is the goal's own, carrying its hand-over
	var dones, verifieds, ticks int
	var goalDone map[string]any
	for _, ev := range h.events() {
		switch ev["event"] {
		case "verified":
			dones++
			goalDone = ev
		case "card_verified":
			verifieds++
		case "goal":
			ticks++
		}
	}
	if dones != 1 || verifieds != 2 || ticks == 0 {
		t.Fatalf("verified %d card_verified %d goal ticks %d\n%s", dones, verifieds, ticks, h.buf.String())
	}
	if goalDone["id"] != string(g.ID) {
		t.Fatalf("the terminal verified event is the goal's: %v", goalDone)
	}
	gd, _ := goalDone["goal"].(map[string]any)
	if gd == nil || gd["done_when_met"].(float64) != 2 || gd["done_when_total"].(float64) != 2 {
		t.Fatalf("goal hand-over = %v", goalDone["goal"])
	}

	// landing: one merge commit on main over both card commits
	h.buf.Reset()
	mout, err := h.driver(Options{}).Merge(ctx, g.ID, "")
	if err != nil || mout.Status != StatusVerified {
		t.Fatalf("merge: %v %v\n%s", mout.Status, err, h.buf.String())
	}
	for _, name := range []string{"cache.txt", "flag.txt"} {
		if _, err := os.Stat(filepath.Join(h.root, name)); err != nil {
			t.Fatalf("%s is on main after landing: %v", name, err)
		}
	}
	logOut, err := exec.CommandContext(ctx, "git", "-C", h.root, "log", "--first-parent", "-1", "--format=%s").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(logOut), "Merge "+string(g.ID)) {
		t.Fatalf("main's first-parent tip = %q %v", logOut, err)
	}
	if got, _ := h.store.GetFeature(ctx, g.ID); got.Stage != domain.StageDone {
		t.Fatalf("goal stage after landing = %s", got.Stage)
	}
}

func TestResumeGoalFlagsRefuseOtherCards(t *testing.T) {
	h := newHarness(t, false, nil)
	ctx := context.Background()
	d := h.driver(Options{Until: domain.StagePlan})
	f, err := d.Create(ctx, domain.CardType{Kind: domain.KindFeature}, "plain card")
	if err != nil {
		t.Fatal(err)
	}
	note := "hi"
	out, _ := h.driver(Options{}).Resume(ctx, f.ID, ResumeInput{Note: &note})
	if out.Status != StatusError {
		t.Fatalf("--note on a plain card must fail, got %s", out.Status)
	}
	if !strings.Contains(h.buf.String(), "not a goal") {
		t.Fatalf("stream = %s", h.buf.String())
	}
}

// A goal whose done-when item can never be met still ends: its verify
// fails, it goes back to its cards for its rework rounds, and then it stops
// ready for you, partial, with the unmet item on the report.
func TestDriveGoalThatCannotMeetAnItemEndsPartial(t *testing.T) {
	h := newHarness(t, true, nil)
	h.fake.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		f, err := h.store.GetFeature(context.Background(), cardOfSession(opts))
		if err != nil {
			return []agent.Event{{Kind: agent.EventIdle}}
		}
		h.draftRequiredSections(f)
		switch opts.Role {
		case agent.RoleReviewer:
			return toolVerdict(opts.Model, "pass")
		}
		if f.Stage == domain.StageImplement && !f.IsGoal() && f.Title == "local cache" {
			_ = os.WriteFile(filepath.Join(opts.WorkDir, "cache.txt"), []byte("x\n"), 0o600)
		}
		return msgIdle(opts.Model, "done")
	}
	ctx := context.Background()
	doc := strings.Replace(driverGoalDoc, "  depends_on: [local cache]\n", "", 1)
	d := h.driver(Options{Envelope: 6000, Autonomous: true, GoalDoc: doc})
	g, err := d.Create(ctx, domain.CardType{Kind: domain.KindGoal}, "Export works offline")
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Drive(ctx, g)
	if err != nil || out.Status != StatusVerified {
		t.Fatalf("status %s err %v\n%s", out.Status, err, h.buf.String())
	}
	got, _ := h.store.GetFeature(ctx, g.ID)
	if got.Goal.Partial == "" || got.VerifiedAt.IsZero() {
		t.Fatalf("the goal should stop ready for you, partial: %+v", got.Goal)
	}
	var goalDone map[string]any
	for _, ev := range h.events() {
		if ev["event"] == "verified" {
			goalDone = ev
		}
	}
	gd, _ := goalDone["goal"].(map[string]any)
	if gd == nil || gd["done_when_met"].(float64) != 1 || gd["partial"] == "" {
		t.Fatalf("hand-over = %v", goalDone)
	}
}

// --goal-note, --wrap-up and --reverse exist to reach a goal WHILE it
// runs, and a running goal is exactly the card whose lock another process
// holds — so the CLI hands the decision over instead of refusing on the
// only card it applies to, and this invocation drives nothing.
func TestAGoalDecisionReachesAGoalAnotherProcessIsDriving(t *testing.T) {
	h := goalHarness(t)
	ctx := context.Background()
	d := h.driver(Options{Envelope: 4000, Autonomous: true, GoalDoc: driverGoalDoc, Until: domain.StagePlan})
	g, err := d.Create(ctx, domain.CardType{Kind: domain.KindGoal}, "Export works offline")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Drive(ctx, g); err != nil {
		t.Fatalf("drive to the plan stop: %v\n%s", err, h.buf.String())
	}
	before, _ := h.store.GetFeature(ctx, g.ID)

	note := "the hex path is broken too"
	out, err := h.driver(Options{}).Resume(ctx, g.ID, ResumeInput{Note: &note, Deliver: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != StatusNoted {
		t.Fatalf("status = %s, want noted\n%s", out.Status, h.buf.String())
	}
	if out.Status.ExitCode() != 0 {
		t.Errorf("a delivered decision exits %d; finding the goal running is its success case", out.Status.ExitCode())
	}
	if !strings.Contains(h.buf.String(), `"event":"noted"`) {
		t.Errorf("no noted event on the stream: %s", h.buf.String())
	}
	after, _ := h.store.GetFeature(ctx, g.ID)
	if after.Stage != before.Stage {
		t.Errorf("delivering a note drove the goal from %s to %s", before.Stage, after.Stage)
	}
	log, _ := h.store.GoalLog(ctx, g.ID)
	var sawNote bool
	for _, en := range log {
		if en.Action == state.GoalNote && en.Detail == note {
			sawNote = true
		}
	}
	if !sawNote {
		t.Errorf("the conductor has no note to read: %+v", log)
	}
}

// A goal's own verify that says the environment cannot run the checks
// judged nothing. It used to take the rework path — the f.IsGoal() arm
// came first — which spent a corrective round and sent the goal back to
// cards nobody had found fault with; two of them ended a goal partial. It
// stops where it is instead, and the same resume runs the verify again.
func TestAGoalVerifyTheEnvironmentCouldNotRunJudgesNothing(t *testing.T) {
	h := goalHarness(t)
	inner := h.fake.Responder
	blocked := true
	h.fake.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		f, err := h.store.GetFeature(context.Background(), cardOfSession(opts))
		if err == nil && f.IsGoal() && f.Stage == domain.StageVerify && opts.Role == agent.RoleReviewer && blocked {
			h.draftRequiredSections(f)
			return toolVerdict(opts.Model, "blocked")
		}
		return inner(opts, msg)
	}
	ctx := context.Background()
	d := h.driver(Options{Envelope: 6000, Autonomous: true, GoalDoc: driverGoalDoc})
	g, err := d.Create(ctx, domain.CardType{Kind: domain.KindGoal}, "Export works offline")
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Drive(ctx, g)
	if err != nil || out.Status != StatusStalled {
		t.Fatalf("status %s err %v\n%s", out.Status, err, h.buf.String())
	}
	got, _ := h.store.GetFeature(ctx, g.ID)
	if got.Stage != domain.StageVerify || got.Goal.Partial != "" || !got.VerifiedAt.IsZero() {
		t.Fatalf("the goal stays at verify, whole and unverified: stage %s partial %q", got.Stage, got.Goal.Partial)
	}
	for _, ev := range h.events() {
		if ev["result"] == "reworking" {
			t.Fatalf("nothing was judged, so nothing goes back to the cards:\n%s", h.buf.String())
		}
	}

	blocked = false
	d2 := h.driver(Options{Envelope: 6000, Autonomous: true})
	out, err = d2.Resume(ctx, g.ID, ResumeInput{})
	if err != nil || out.Status != StatusVerified {
		t.Fatalf("resume: status %s err %v\n%s", out.Status, err, h.buf.String())
	}
	got, _ = h.store.GetFeature(ctx, g.ID)
	if got.Goal.Partial != "" {
		t.Fatalf("a verify that could run found nothing wrong: partial %q", got.Goal.Partial)
	}
}

// A goal whose item is proved by an experiment, end to end: the conductor
// makes the run once the work has settled, the goal's verify reads the
// evidence beside its commands' results, and the hand-over points at it.
func TestDriveGoalProvedByAnExperiment(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h := goalHarness(t)
	rig := t.TempDir()
	cfg := "substrates:\n  rig:\n    probe: test -f " + rig + "/up\n    provision: touch " + rig + "/up\n" +
		"experiments:\n  live:\n    substrate: rig\n    run: |\n" +
		"      printf '{\"id\":\"cache\",\"ok\":true}\\n{\"id\":\"flag\",\"ok\":true}\\n' > \"$GUMMI_EVIDENCE/results.ndjson\"\n" +
		"      test -f \"$GUMMI_TREE_HOME/cache.txt\" && test -f \"$GUMMI_TREE_HOME/flag.txt\"\n" +
		"    collect: echo dump > \"$GUMMI_EVIDENCE/state.txt\"\n"
	if err := os.WriteFile(h.ws.ConfigFile(), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	h.eng.SetExperimentSpawner(func(dir string) error {
		job, err := experiment.LoadJob(dir)
		if err != nil {
			return err
		}
		experiment.Execute(context.Background(), job)
		return nil
	})
	doc := strings.Replace(driverGoalDoc, "  check: test -f flag.txt\n", "  experiment: live\n", 1)
	doc = strings.Replace(doc, "lanes: 2\n", "lanes: 2\nruns: 6\n", 1)
	ctx := context.Background()
	d := h.driver(Options{Envelope: 6000, Autonomous: true, GoalDoc: doc})
	g, err := d.Create(ctx, domain.CardType{Kind: domain.KindGoal}, "Export works offline")
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Drive(ctx, g)
	if err != nil || out.Status != StatusVerified {
		t.Fatalf("status %s err %v\n%s", out.Status, err, h.buf.String())
	}
	got, _ := h.store.GetFeature(ctx, g.ID)
	if got.Goal.Partial != "" {
		t.Fatalf("whole: %q", got.Goal.Partial)
	}
	// Two runs. The first card's landing is proven early, while the second
	// card is still to come, and fails — the flag is not there yet, which is
	// something that never held failing, not a regression, and costs the
	// goal nothing. The second is the proof of what it hands over.
	// The third is the negative control: the same experiment on the trunk,
	// where it must be able to fail for the pass to mean anything.
	runs := h.eng.ExperimentRuns(g.ID)
	if len(runs) != 3 || runs[0].Outcome != experiment.Fail || runs[1].Outcome != experiment.Pass ||
		!runs[2].ExpectFail || runs[2].Outcome != experiment.Fail {
		t.Fatalf("an early run, the final one, and the trunk: %+v", runs)
	}
	for _, ev := range h.events() {
		if ev["result"] == "reworking" {
			t.Fatalf("a run that fails on work still in flight sends nothing back:\n%s", h.buf.String())
		}
	}
	runs = runs[1:2]
	if _, err := os.Stat(filepath.Join(runs[0].Dir, "evidence", "state.txt")); err != nil {
		t.Fatal("the bundle a reviewer reads is there")
	}
	rep, err := h.eng.GoalReport(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if met, total := rep.Met(); met != 2 || total != 2 {
		t.Fatalf("met %d of %d: %+v", met, total, rep.DoneWhen)
	}
	// the run, and where its bundle is — the path from the workspace down,
	// which is what a reader can hold in their head
	rel := runs[0].Dir
	if i := strings.Index(rel, "/.gummi/evidence/"); i >= 0 {
		rel = rel[i+1:]
	}
	if ev := rep.DoneWhen[1].Evidence; !strings.Contains(ev, runs[0].ID) || !strings.Contains(ev, rel) {
		t.Fatalf("the item's evidence is the run and where its bundle is: %q", ev)
	}
}

// A goal plan the gate refuses says what is wrong with it. The gate
// computes that sentence — an item nothing can check, a card in a
// repository the workspace does not manage, an envelope that is not a
// number — and the driver used to throw it away: StatusBlockedGoalPlan
// was the one blocked status with no case of its own, so it fell to the
// default and reported "unexpected gate status". A real goal whose
// architect wrote `envelope: ""` rather than guess a number stopped the
// whole run with nothing to act on.
func TestARefusedGoalPlanSaysWhatIsWrongWithIt(t *testing.T) {
	h := goalHarness(t)
	ctx := context.Background()
	// a card list naming a done-when item that does not exist: the gate
	// refuses it and names it
	doc := strings.Replace(driverGoalDoc,
		"- title: offline flag\n  serves: [DW-2]\n  depends_on: [local cache]\n",
		"- title: offline flag\n  serves: [DW-9]\n", 1)
	d := h.driver(Options{Envelope: 4000, Autonomous: true, GoalDoc: doc})
	g, err := d.Create(ctx, domain.CardType{Kind: domain.KindGoal}, "Export works offline")
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Drive(ctx, g)
	if err != nil {
		t.Fatalf("drive: %v\n%s", err, h.buf.String())
	}
	if out.Status != StatusBlocked {
		t.Fatalf("status = %s, want blocked\n%s", out.Status, h.buf.String())
	}
	var blocked map[string]any
	for _, ev := range h.events() {
		if ev["event"] == "blocked" {
			blocked = ev
		}
		if ev["event"] == "escalation" {
			t.Errorf("escalated instead of reporting the refusal: %v", ev)
		}
	}
	if blocked == nil {
		t.Fatalf("no blocked event\n%s", h.buf.String())
	}
	reason, _ := blocked["reason"].(string)
	if reason == "" {
		t.Fatalf("blocked event carries no reason: %v", blocked)
	}
	if !strings.Contains(reason, "DW-9") {
		t.Errorf("reason = %q, want it to name the item the plan got wrong", reason)
	}
}

// The architect is the one who can fix a refused goal plan, and nothing
// was telling the architect. A goal whose plan the gate refused parked,
// and every resume ran a whole plan pass, returned "pass", and left the
// doc exactly as it was — three passes for no change, with no way out
// but a person editing the file. An unattended run now sends the
// refusal back as a replan round.
func TestARefusedGoalPlanGoesBackToTheArchitect(t *testing.T) {
	h := goalHarness(t)
	ctx := context.Background()
	bad := strings.Replace(driverGoalDoc,
		"- title: offline flag\n  serves: [DW-2]\n  depends_on: [local cache]\n",
		"- title: offline flag\n  serves: [DW-9]\n", 1)
	inner := h.fake.Responder
	var toArchitect []string
	h.fake.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleArchitect {
			toArchitect = append(toArchitect, msg)
		}
		return inner(opts, msg)
	}
	d := h.driver(Options{Envelope: 4000, Autonomous: true, GateApproval: GateAutopilot, GoalDoc: bad})
	g, err := d.Create(ctx, domain.CardType{Kind: domain.KindGoal}, "Export works offline")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Drive(ctx, g); err != nil {
		t.Fatalf("drive: %v\n%s", err, h.buf.String())
	}
	var told bool
	for _, m := range toArchitect {
		if strings.Contains(m, "DW-9") {
			told = true
		}
	}
	if !told {
		t.Errorf("the architect was never told what the gate refused; it got %d messages\n%s",
			len(toArchitect), h.buf.String())
	}
	// and it is one round, not a loop: the same refusal twice parks
	var replans int
	for _, ev := range h.events() {
		if ev["event"] == "stage" && ev["result"] == "replanning" {
			replans++
		}
	}
	if replans != 1 {
		t.Errorf("replans = %d, want exactly one — the same refusal twice is a person's problem", replans)
	}
}
