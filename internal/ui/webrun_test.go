package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/webapi"
)

const webRunSpec = "# Out file\n\n## Problem\n\nNo file.\n\n## Done when\n\n```gummi-done-when\n" +
	"- id: DW-1\n  says: out.txt exists\n  check: test -f out.txt\n```\n"

const webRunRecipe = "name: one\nsteps:\n  - id: write\n    role: implementer\n    prompt: Write out.txt.\n"

// A run on the web face (DESIGN §25.5, §25.9, §25.10, §25.3, §25.6): the
// proposal is its decision's card; the run is one item in the session's
// thread, delivered on every page; a step opens with its head, brief and
// own diff, and its card's thread is read like any card's; the specs list
// names the spec met; Revert run lists the paths and puts them back.
func TestARunOnTheWebFace(t *testing.T) {
	now := time.Now()
	f := domain.Feature{
		ID: "FF-001", Num: 1, Kind: domain.KindFreeform, Title: "Out file", Slug: "out-file",
		Stage: domain.StageOpen, BranchScheme: domain.BranchSchemeKind, CreatedAt: now, UpdatedAt: now,
		Budget: domain.Budget{Envelope: 5000},
	}
	fake := agent.NewFake("ok")
	fake.Caps = agent.Capabilities{ClientTools: true, UsageEvents: true, Interrupt: true}
	fake.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		if strings.Contains(msg, "of workflow run") {
			_ = os.WriteFile(filepath.Join(opts.WorkDir, "out.txt"), []byte("hi\n"), 0o600)
		}
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 2, Model: "m"}},
			{Kind: agent.EventMessage, Text: "wrote out.txt"},
			{Kind: agent.EventIdle},
		}
	}
	b, _, eng, _ := headlessBoardWith(t, fake, []domain.Feature{f}, nil)
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	ctx := context.Background()
	ff, err := eng.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "hi"); err != nil {
		t.Fatal(err)
	}
	waitUntilTrue(t, "the session's first turn", func() bool { return !ff.Busy() && len(ff.Snapshot().Transcript) > 1 })
	if _, _, err := eng.StartSessionSpec(ctx, f, "Out file", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.WriteSessionSpec(ctx, f, 1, webRunSpec, "user"); err != nil {
		t.Fatal(err)
	}
	run, err := eng.WorkflowPropose(ctx, f.ID, engine.WorkflowRequest{RecipeYAML: webRunRecipe})
	if err != nil {
		t.Fatal(err)
	}
	var c webapi.Card
	waitUntilTrue(t, "the card to pin the proposal", func() bool {
		reloadRows(t, b)
		c, err = b.Card(ctx, string(f.ID))
		return err == nil && c.Decision != nil && c.Decision.Proposal != nil
	})
	p := c.Decision.Proposal
	if p == nil || p.Run != run.N || len(p.Steps) != 1 || p.Steps[0].Role != "implementer" ||
		len(p.Checks) != 1 || p.Checks[0].Cmd != "test -f out.txt" || !p.Bounded {
		t.Fatalf("the proposal's card: %+v", c.Decision)
	}
	if err := eng.WorkflowAnswer(ctx, f.ID, run.N, run.Pending.ID, engine.WorkflowAllow); err != nil {
		t.Fatal(err)
	}
	waitUntilTrue(t, "the run to end", func() bool {
		_, _ = eng.WorkflowTick(ctx)
		var r *domain.Run
		_ = b.Do(ctx, func(m *Shell) tea.Cmd { r, _ = m.store.Run(ctx, f.ID, run.N); return nil })
		// the store marks a run ended before gummi lets go of its end
		// phase, and nothing is offered a revert while it is ending
		return r != nil && r.State.Ended() && eng.RunPhase(f.ID, run.N) == ""
	})
	reloadRows(t, b)

	d := webDocsThread(t, b, string(f.ID))
	th, err := d.Thread(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	var block *webapi.RunBlock
	for _, it := range th.Items {
		if it.T == webapi.ItemRun {
			block = it.Run
		}
	}
	if block == nil || block.State != "done" || len(block.Rows) != 1 || block.Rows[0].Card == "" ||
		len(block.Files) != 1 || block.Files[0] != "out.txt" || !block.CanRevert {
		t.Fatalf("the run's block: %+v", block)
	}
	// the run that ended last is the latest news: whole, its line said;
	// it stands among the live conversation's turns where it was proposed
	if block.Folded || !strings.HasPrefix(block.Line, "one · run 1 · spec 1") || block.Mark != "✓" {
		t.Fatalf("the ended run's fold: folded %t, %q %q", block.Folded, block.Mark, block.Line)
	}
	if th.Live == nil || th.Live.Freeform == nil || len(th.Live.Freeform.Runs) != 1 || th.Live.Freeform.Runs[0].N != 1 {
		t.Fatalf("the live conversation does not place the run: %+v", th.Live)
	}
	if at := th.Live.Freeform.Runs[0].Before; at < 1 || at > len(th.Live.Freeform.Turns) {
		t.Fatalf("the run stands before turn %d of %d", at, len(th.Live.Freeform.Turns))
	}
	later, err := d.Thread(ctx, th.LastSeq)
	if err != nil || len(later.Items) == 0 || later.Items[0].T != webapi.ItemRun {
		t.Fatalf("a page past every event is not handed the run: %+v %v", later.Items, err)
	}

	card := block.Rows[0].Card
	face, err := d.Step(ctx, card)
	if err != nil || !face.CanRevert {
		t.Fatalf("an ended run's step offers no revert to it: %+v %v", face, err)
	}
	if pre, err := d.RevertPreviewTo(ctx, run.N, "write"); err != nil || pre.Step != "write" || len(pre.Paths) != 1 {
		t.Fatalf("the step's revert preview %+v %v", pre, err)
	}
	if err != nil || face.Path != "write" || face.Role != "implementer" || !strings.Contains(face.Brief, "Write out.txt.") ||
		strings.Join(face.Crumb, " › ") != "Session › one run 1 › write" {
		t.Fatalf("the step's face %+v %v", face, err)
	}
	diff, err := d.StepDiff(ctx, card)
	if err != nil || len(diff.Files) != 1 || diff.Files[0].Path != "out.txt" {
		t.Fatalf("the step's diff %+v %v", diff, err)
	}
	if _, err := d.Step(ctx, "FF-999"); err == nil {
		t.Fatal("a card that is no step of this session opened as one")
	}
	stepThread := webDocsThread(t, b, card)
	if st, err := stepThread.Thread(ctx, 0); err != nil || st.Live == nil || st.Live.Freeform == nil {
		t.Fatalf("the step card's own thread: %+v %v", st, err)
	}

	specs, err := d.Specs(ctx)
	if err != nil || len(specs.Specs) != 1 || specs.Specs[0].State != "met" || specs.Specs[0].DoneWhen != 1 ||
		len(specs.Specs[0].Runs) != 1 || !specs.CanStart {
		t.Fatalf("specs %+v %v", specs, err)
	}

	if sr := specs.Specs[0].Runs[0]; sr.Mark != "✓" || sr.Met != 1 || sr.Checked != 1 || sr.Ended.IsZero() {
		t.Fatalf("the spec's run: %+v", sr)
	}
	d.UseSpec(1)
	if sp, err := d.Spec(ctx); err != nil || len(sp.Runs) != 1 || sp.Runs[0].N != run.N {
		t.Fatalf("the spec does not list its runs: %+v %v", sp.Runs, err)
	}

	// once the person says something after it, the run folds to its line
	if err := ff.Send(ctx, "thanks"); err != nil {
		t.Fatal(err)
	}
	waitUntilTrue(t, "the run to fold", func() bool {
		th, err := webDocsThread(t, b, string(f.ID)).Thread(ctx, 0)
		if err != nil {
			return false
		}
		for _, it := range th.Items {
			if it.T == webapi.ItemRun && it.Run.Folded && it.Run.Rev != block.Rev {
				return true
			}
		}
		return false
	})

	// the run's whole diff, beside its steps' own
	if rd, err := d.RunDiff(ctx, run.N); err != nil || len(rd.Files) != 1 || rd.Files[0].Path != "out.txt" {
		t.Fatalf("the run's diff %+v %v", rd, err)
	}
	if _, err := d.RunDiff(ctx, 9); err == nil {
		t.Fatal("a run the session never had has a diff")
	}
	// an artifact its recipe never declared is no artifact of the run's
	if _, err := d.RunArtifact(ctx, run.N, "report"); err == nil || !strings.Contains(err.Error(), "declares no artifact") {
		t.Fatalf("an undeclared artifact: %v", err)
	}
	// only a queued run is withdrawn; ask again takes back nothing given
	if err := d.RunAct(ctx, run.N, "withdraw", ""); err == nil || !strings.Contains(err.Error(), "not waiting to start") {
		t.Fatalf("withdrawing an ended run: %v", err)
	}
	if err := d.RunAct(ctx, run.N, "askagain", ""); err != nil {
		t.Fatalf("ask again: %v", err)
	}

	pre, err := d.RevertPreview(ctx, run.N)
	if err != nil || len(pre.Paths) != 1 || pre.Paths[0] != "out.txt" || pre.Note == "" {
		t.Fatalf("revert preview %+v %v", pre, err)
	}
	if pre.Tree == "" {
		t.Fatalf("the preview names no tree: %+v", pre)
	}
	// a tree that moved after the preview is not reverted unseen
	if err := os.WriteFile(filepath.Join(ff.WorkDir(), "later.txt"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Revert(ctx, run.N, pre.Tree); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("a revert against a stale preview: %v", err)
	}
	if err := os.Remove(filepath.Join(ff.WorkDir(), "later.txt")); err != nil {
		t.Fatal(err)
	}
	if done, err := d.Revert(ctx, run.N, pre.Tree); err != nil || len(done.Paths) != 1 {
		t.Fatalf("revert %+v %v", done, err)
	}
	if _, err := os.Stat(filepath.Join(ff.WorkDir(), "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("reverting left the run's file: %v", err)
	}
}

func webDocsThread(t *testing.T, b *Bridge, id string) *WebDocs {
	t.Helper()
	var (
		d   *WebDocs
		err error
	)
	if derr := b.Do(context.Background(), func(m *Shell) tea.Cmd { d, err = m.WebThreadDocs(id); return nil }); derr != nil {
		t.Fatal(derr)
	}
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// reloadRows has the board read its rows again, as an engine event would.
func reloadRows(t *testing.T, b *Bridge) {
	t.Helper()
	if err := b.Do(context.Background(), func(m *Shell) tea.Cmd { return m.loadRows }); err != nil {
		t.Fatal(err)
	}
	if err := b.Do(context.Background(), func(*Shell) tea.Cmd { return nil }); err != nil {
		t.Fatal(err)
	}
}

func waitUntilTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The Tree tab is the session's tree, which a run's steps share: while a
// step works in it the tab says so and refuses a write, as it does while
// the session's own agent is mid-turn; once the run ends the write goes
// through.
func TestTheTreeTabWaitsWhileAStepWorksInTheTree(t *testing.T) {
	now := time.Now()
	f := domain.Feature{
		ID: "FF-001", Num: 1, Kind: domain.KindFreeform, Title: "Out file", Slug: "out-file",
		Stage: domain.StageOpen, BranchScheme: domain.BranchSchemeKind, CreatedAt: now, UpdatedAt: now,
		Budget: domain.Budget{Envelope: 5000},
	}
	gate := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(open)
	fake := agent.NewFake("ok")
	fake.Caps = agent.Capabilities{ClientTools: true, UsageEvents: true, Interrupt: true}
	fake.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		if strings.Contains(msg, "of workflow run") {
			_ = os.WriteFile(filepath.Join(opts.WorkDir, "out.txt"), []byte("hi\n"), 0o600)
			<-gate
		}
		return []agent.Event{
			{Kind: agent.EventUsage, Usage: agent.Usage{Credits: 2, Model: "m"}},
			{Kind: agent.EventMessage, Text: "wrote out.txt"},
			{Kind: agent.EventIdle},
		}
	}
	b, _, eng, _ := headlessBoardWith(t, fake, []domain.Feature{f}, nil)
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	ctx := context.Background()
	ff, err := eng.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "hi"); err != nil {
		t.Fatal(err)
	}
	waitUntilTrue(t, "the session's first turn", func() bool { return !ff.Busy() && len(ff.Snapshot().Transcript) > 1 })
	if _, _, err := eng.StartSessionSpec(ctx, f, "Out file", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.WriteSessionSpec(ctx, f, 1, webRunSpec, "user"); err != nil {
		t.Fatal(err)
	}
	run, err := eng.WorkflowPropose(ctx, f.ID, engine.WorkflowRequest{RecipeYAML: webRunRecipe})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.WorkflowAnswer(ctx, f.ID, run.N, run.Pending.ID, engine.WorkflowAllow); err != nil {
		t.Fatal(err)
	}
	waitUntilTrue(t, "the step is writing", func() bool {
		_, _ = eng.WorkflowTick(ctx)
		_, err := os.Stat(filepath.Join(ff.WorkDir(), "out.txt"))
		return err == nil
	})
	reloadRows(t, b)
	d := webDocsThread(t, b, string(f.ID))
	tree, err := d.Tree(ctx)
	if err != nil || !strings.Contains(tree.Why, "workflow run 1") {
		t.Fatalf("the Tree tab while a step works: why %q, %v", tree.Why, err)
	}
	stage := webapi.TreeRequest{Op: webapi.TreeStage, Paths: []string{"out.txt"}}
	if _, err := d.TreeWrite(ctx, stage, "pat"); err == nil || !strings.Contains(err.Error(), "workflow run 1") {
		t.Fatalf("a Tree tab write while a step works: %v", err)
	}
	open()
	waitUntilTrue(t, "the run to end", func() bool {
		_, _ = eng.WorkflowTick(ctx)
		var r *domain.Run
		_ = b.Do(ctx, func(m *Shell) tea.Cmd { r, _ = m.store.Run(ctx, f.ID, run.N); return nil })
		return r != nil && r.State.Ended()
	})
	// the report reaches the session as a turn of its own, sent after the
	// run ends: wait for that turn to arrive and be answered, or a write
	// can land while the session is busy with it and be refused, rightly.
	// Answered is the agent's reply after the report, not the report
	// alone: the report is in the transcript a moment before the turn
	// starts, and an idle session then is one about to be busy.
	waitUntilTrue(t, "the session answered the report", func() bool {
		if ff.Busy() {
			return false
		}
		reported := false
		for _, msg := range ff.Snapshot().Transcript {
			switch {
			case strings.Contains(msg.Content, "Run 1 ("):
				reported = true
			case reported && msg.Author == engine.AuthorAssistant:
				return true
			}
		}
		return false
	})
	d = webDocsThread(t, b, string(f.ID))
	if _, err := d.TreeWrite(ctx, stage, "pat"); err != nil {
		t.Fatalf("a Tree tab write once the run ended: %v", err)
	}
}
