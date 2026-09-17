package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestCommitDialogTakesThePredraftAndRedraftsFresh pins the one bit the
// dialog carries about pre-drafting: opening accepts the message the
// verify gate already composed, and Redraft refuses it.
//
// The distinction is the whole reason the seam takes a flag. Opening on
// the stored draft is what removes the ~60s wait from every landing; a
// Redraft answered from that same store would hand the reader back the
// text they just asked to be rid of, instantly, and read as a dead
// button.
func TestCommitDialogTakesThePredraftAndRedraftsFresh(t *testing.T) {
	var asked []bool
	d := newCommitMsgDialog(
		domain.Feature{ID: "FD-001", Slug: "dark-mode"},
		func(string) tea.Cmd { return nil },
		func(_ context.Context, _ domain.Feature, fresh bool) (string, error) {
			asked = append(asked, fresh)
			return "feat(ui): a message\n\n- a bullet", nil
		},
	)
	d.apply(d.startDraft(false)().(commitDraftMsg))
	if _, cmd := d.HandleKey(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}); cmd != nil {
		cmd()
	}
	want := []bool{false, true}
	if len(asked) != len(want) {
		t.Fatalf("seam called %d times %v, want %v", len(asked), asked, want)
	}
	for i := range want {
		if asked[i] != want[i] {
			t.Errorf("call %d asked fresh=%v, want %v", i, asked[i], want[i])
		}
	}
}

// TestVerifyGatePredraftsTheLandingMessage is the mechanism end to end at
// the seam that matters: a card whose verify pass just passed has a
// landing message on disk before anyone presses a key, composed against
// the branch it describes.
//
// It drives predraftLandingMessage rather than the whole gate because
// that command IS what the gate batches, beside the verified stamp — and
// what it must do is durable, so a test can read it back rather than
// watch for it.
func TestVerifyGatePredraftsTheLandingMessage(t *testing.T) {
	ctx := context.Background()
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)

	const subject = "feat(ui): open the dialog on a message"
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, _ string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "```gummi-commit\n" + subject + "\n\n- a rationale bullet\n```"},
			{Kind: agent.EventIdle},
		}
	}}
	eng := engine.New(engine.Config{
		Agents: singleAgent(ag), Store: store,
		Pool: wt, Workspace: ws, Model: "fake-model",
	})
	t.Cleanup(func() { eng.Close() })
	m.AttachEngine(eng)

	f := mkFeature(t, store, 1, "dark mode", domain.StageVerify)
	wtDir, err := wt.Create(ctx, &f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, "work.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, wtDir, "add", ".")
	git(t, wtDir, "commit", "-qm", "committed work")

	cmd := m.predraftLandingMessage(f.ID, "the checks all held")
	if cmd == nil {
		t.Fatal("the verify gate produced no pre-draft command")
	}
	msg, ok := cmd().(commitDraftPersistedMsg)
	if !ok {
		t.Fatalf("pre-draft emitted %T, want commitDraftPersistedMsg", cmd())
	}
	if msg.reason != "" {
		t.Fatalf("pre-draft failed: %s", msg.reason)
	}
	cur, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cur.CommitDraft, subject) {
		t.Errorf("stored draft = %q, want the scribe's message", cur.CommitDraft)
	}
	if cur.CommitDraftSHA == "" {
		t.Error("the stored draft carries no branch tip, so nothing can tell it from a stale one")
	}
	// and the dialog's own seam now answers from it, without a pass
	got, err := eng.LandingMessage(ctx, cur, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != cur.CommitDraft {
		t.Errorf("LandingMessage = %q, want the pre-drafted %q", got, cur.CommitDraft)
	}
}

// TestPredraftNeedsNoEngine: the command is batched unconditionally at the
// verify gate, so it has to be inert on a board with no engine attached
// (every test shell, and a board still starting up) rather than panic
// there.
func TestPredraftNeedsNoEngine(t *testing.T) {
	m, _ := newWorkspace(t)
	if cmd := m.predraftLandingMessage("FD-001", "note"); cmd != nil {
		t.Error("a board with no engine returned a pre-draft command")
	}
}
