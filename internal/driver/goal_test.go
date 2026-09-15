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
	if out.Status != StatusDone {
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

	// the stream: the cards verified (not `done`), the goal ticked, and
	// the single `done` is the goal's, carrying its hand-over
	var dones, verifieds, ticks int
	var goalDone map[string]any
	for _, ev := range h.events() {
		switch ev["event"] {
		case "done":
			dones++
			goalDone = ev
		case "verified":
			verifieds++
		case "goal":
			ticks++
		}
	}
	if dones != 1 || verifieds != 2 || ticks == 0 {
		t.Fatalf("done %d verified %d goal ticks %d\n%s", dones, verifieds, ticks, h.buf.String())
	}
	if goalDone["id"] != string(g.ID) {
		t.Fatalf("the done event is the goal's: %v", goalDone)
	}
	gd, _ := goalDone["goal"].(map[string]any)
	if gd == nil || gd["done_when_met"].(float64) != 2 || gd["done_when_total"].(float64) != 2 {
		t.Fatalf("goal hand-over = %v", goalDone["goal"])
	}

	// landing: one merge commit on main over both card commits
	h.buf.Reset()
	mout, err := h.driver(Options{}).Merge(ctx, g.ID, "")
	if err != nil || mout.Status != StatusDone {
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
	if err != nil || out.Status != StatusDone {
		t.Fatalf("status %s err %v\n%s", out.Status, err, h.buf.String())
	}
	got, _ := h.store.GetFeature(ctx, g.ID)
	if got.Goal.Partial == "" || got.VerifiedAt.IsZero() {
		t.Fatalf("the goal should stop ready for you, partial: %+v", got.Goal)
	}
	var goalDone map[string]any
	for _, ev := range h.events() {
		if ev["event"] == "done" {
			goalDone = ev
		}
	}
	gd, _ := goalDone["goal"].(map[string]any)
	if gd == nil || gd["done_when_met"].(float64) != 1 || gd["partial"] == "" {
		t.Fatalf("hand-over = %v", goalDone)
	}
}
