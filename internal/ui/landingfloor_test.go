package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

// While a freeform turn runs, the menu offers only "stop this turn" among
// its endings: no merge, squash or hand-off over whatever the agent has
// half-written.
func TestABusyFreeformCardsMenuOffersNoLanding(t *testing.T) {
	busy := nextInput{stage: domain.StageOpen, kind: domain.KindFreeform, hasWorktree: true, freeformBusy: true}
	r := freeformRow(12, "drop the leaked pty fd", true)
	for _, a := range cardActionsFor(busy, r) {
		switch a.id {
		case "merge", "squash", "handoff":
			t.Errorf("mid-turn the menu offers %q (%s)", a.id, a.label)
		}
	}
}

// A freeform card lands on a person's read of its diff (DESIGN §19.1), so
// an open diff comment — the person's own unfinished read — holds it.
func TestAnOpenDiffCommentHoldsAFreeformLanding(t *testing.T) {
	m, root := newWorkspace(t)
	m.SetCopilotHint(false)
	m = pump(t, m, m.Init())
	m.Overlay.Push(m.openCardForm(domain.CardType{Kind: domain.KindFreeform}))
	m = typeString(t, m, "Drop the leaked pty fd")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	ctx := context.Background()
	fs, _ := m.store.ListFeatures(ctx)
	f := fs[0]
	if _, err := m.wt.Ensure(ctx, &f); err != nil {
		t.Fatal(err)
	}
	commitWork(t, root, string(f.ID))
	if _, err := m.store.AddDiffAnnotation(ctx, domain.DiffAnnotation{Feature: f.ID, File: "a.go", Anchor: "+x", Excerpt: "+x", Comment: "this leaks too"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)
	before := headSHA(t, root)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m.sel = 0
	m = press(t, m, tea.KeyPressMsg{Code: 'm', Text: "m"})
	if _, ok := m.Overlay.Top().(*commitMsgDialog); !ok {
		t.Logf("m refused: %q", m.notice.text)
		return
	}
	typeMessage(t, m, "fix: land over the open comment")
	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if headSHA(t, root) != before {
		t.Errorf("a freeform card landed over an open diff comment (notice %q)", m.notice.text)
	}
}

// A workflow card that never reached verify does not land from the board
// either (merge.go's landingRefusal; domain.Feature.MayLand).
func TestAnUnverifiedCardDoesNotLandFromTheBoard(t *testing.T) {
	m, root := newWorkspace(t)
	m.SetCopilotHint(false)
	m = pump(t, m, m.Init())
	ctx := context.Background()
	f := domain.Feature{ID: "FD-001", Num: 1, Title: "x", Slug: "x", Stage: domain.StageImplement}
	if err := m.store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	if _, err := m.wt.Ensure(ctx, &f); err != nil {
		t.Fatal(err)
	}
	commitWork(t, root, string(f.ID))
	m = pump(t, m, m.loadRows)
	before := headSHA(t, root)
	m.sel = 0
	m = press(t, m, tea.KeyPressMsg{Code: 'm', Text: "m"})
	if _, ok := m.Overlay.Top().(*commitMsgDialog); !ok {
		t.Logf("m refused: %q", m.notice.text)
		return
	}
	typeMessage(t, m, "feat: unverified")
	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if headSHA(t, root) != before {
		t.Errorf("a card at implement landed on main (MayLand: %v; notice %q)", f.MayLand(), m.notice.text)
	}
}

// A freeform card may land as a merge commit, which keeps every commit on
// its branch in main's history, so loose work is never swept into a canned
// checkpoint: the landing is refused until the person commits it.
func TestALooseFreeformCardDoesNotLandUnderACheckpointCommit(t *testing.T) {
	m, root := newWorkspace(t)
	m.SetCopilotHint(false)
	m = pump(t, m, m.Init())
	m.Overlay.Push(m.openCardForm(domain.CardType{Kind: domain.KindFreeform}))
	m = typeString(t, m, "Drop the leaked pty fd")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	ctx := context.Background()
	fs, _ := m.store.ListFeatures(ctx)
	f := fs[0]
	wt, err := m.wt.Ensure(ctx, &f)
	if err != nil {
		t.Fatal(err)
	}
	commitWork(t, root, string(f.ID))
	if err := os.WriteFile(filepath.Join(wt, "loose.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)
	tip := gitOut(t, wt, "rev-parse", "HEAD")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m.sel = 0
	m = press(t, m, tea.KeyPressMsg{Code: 'm', Text: "m"})
	if _, ok := m.Overlay.Top().(*commitMsgDialog); ok {
		t.Fatal("loose work on a freeform card went to the landing dialog")
	}
	if !m.notice.isErr || !strings.Contains(m.notice.text, "uncommitted work") {
		t.Errorf("no refusal naming the loose work (notice %q)", m.notice.text)
	}
	if got := gitOut(t, wt, "rev-parse", "HEAD"); got != tip {
		t.Errorf("a checkpoint commit was made on the branch: %s -> %s", tip, got)
	}
}
