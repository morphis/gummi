package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

func newTestCommitMsgDialog(t *testing.T) *commitMsgDialog {
	t.Helper()
	return newCommitMsgDialog(
		domain.Feature{ID: "FD-001", Slug: "dark-mode"},
		func(_ string, _ domain.LandMethod) tea.Cmd { return nil },
		nil,
	)
}

func TestCommitMsgDialogNavigationKeyDoesNotMarkModified(t *testing.T) {
	const draft = "feat(ui): add dark mode"

	navKeys := []tea.Key{
		{Code: tea.KeyUp},
		{Code: tea.KeyDown},
		{Code: tea.KeyLeft},
		{Code: tea.KeyRight},
		{Code: tea.KeyHome},
		{Code: tea.KeyEnd},
		{Code: tea.KeyPgUp},
		{Code: tea.KeyPgDown},
	}

	for _, key := range navKeys {
		t.Run(key.String(), func(t *testing.T) {
			d := newTestCommitMsgDialog(t)

			// A pure cursor-movement key, before the draft arrives.
			if _, _ = d.HandleKey(tea.KeyPressMsg{Code: key.Code}); d.modified {
				t.Fatalf("HandleKey(%s) set modified on empty dialog; want false", key)
			}

			// A draft arriving afterwards must still fill the box.
			d.gen = 1
			d.apply(commitDraftMsg{f: d.feature, gen: 1, draft: draft})
			if got := d.input.Value(); got != draft {
				t.Fatalf("apply after %s: value = %q, want draft %q", key, got, draft)
			}
		})
	}
}

func TestCommitMsgDialogModifyingKeyMarksModifiedAndSkipsDraft(t *testing.T) {
	const draft = "feat(ui): replace the modified flag"

	d := newTestCommitMsgDialog(t)

	// A printable character changes the value, so it must mark modified.
	if _, _ = d.HandleKey(tea.KeyPressMsg{Code: 'x', Text: "x"}); !d.modified {
		t.Fatal("HandleKey(printable char) did not set modified; want true")
	}
	typed := d.input.Value()
	if typed == "" {
		t.Fatal("typing a character left the textarea empty")
	}

	// A draft arriving afterwards must not clobber what was typed.
	d.gen = 1
	d.apply(commitDraftMsg{f: d.feature, gen: 1, draft: draft})
	if got := d.input.Value(); got != typed {
		t.Fatalf("apply clobbered typed text: got %q, want %q", got, typed)
	}
}

func TestCommitMsgDialogApplyHonorsGenerationAndEmpty(t *testing.T) {
	d := newTestCommitMsgDialog(t)

	// A stale-generation draft must be dropped.
	d.gen = 2
	d.apply(commitDraftMsg{f: d.feature, gen: 1, draft: "stale"})
	if got := d.input.Value(); got != "" {
		t.Fatalf("stale-gen apply filled box with %q; want empty", got)
	}

	// An empty draft leaves the box empty even for the current gen.
	d.apply(commitDraftMsg{f: d.feature, gen: 2, draft: ""})
	if got := d.input.Value(); got != "" {
		t.Fatalf("empty draft filled box with %q; want empty", got)
	}
}

func TestCommitMsgDialogPasteAlwaysMarksModified(t *testing.T) {
	d := newTestCommitMsgDialog(t)

	d.HandlePaste(tea.PasteMsg{Content: "feat: pasted note"})
	if !d.modified {
		t.Fatal("HandlePaste did not set modified; want true")
	}
	if got := d.input.Value(); got != "feat: pasted note" {
		t.Fatalf("HandlePaste inserted %q, want pasted content", got)
	}
}

func TestCommitMsgDialogSurfacesDraftFailureReason(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		prefix string
		guard  bool
	}{
		{
			name:   "fault",
			err:    errors.New(`scribe session could not open: model "mab/qwen3.6-35b-a3b-q5xl" not found`),
			prefix: "no draft: the scribe could not draft one",
		},
		{
			name:   "guard-rejection",
			err:    engine.NewCommitDraftGuardError("the scribe pasted a diff instead of composing a message"),
			prefix: "the scribe pasted a diff",
			guard:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newCommitMsgDialog(
				domain.Feature{ID: "FD-001", Slug: "dark-mode"},
				func(_ string, _ domain.LandMethod) tea.Cmd { return nil },
				func(ctx context.Context, f domain.Feature, fresh bool) (string, error) { return "", tc.err },
			)
			msg, ok := d.startDraft(false)().(commitDraftMsg)
			if !ok {
				t.Fatal("startDraft did not emit commitDraftMsg")
			}
			d.apply(msg)
			if d.reason == "" {
				t.Fatal("no reason recorded for the failed draft")
			}
			if !strings.Contains(d.reason, tc.prefix) {
				t.Fatalf("reason %q does not carry %q", d.reason, tc.prefix)
			}
			if d.guard != tc.guard {
				t.Fatalf("guard = %v, want %v (reason %q)", d.guard, tc.guard, d.reason)
			}
			if v := d.View(theme.New(theme.GummiDark()), 80, 24); !strings.Contains(v, d.reason) {
				t.Fatalf("dialog view missing the reason %q:\n%s", d.reason, v)
			}
		})
	}
}

func TestCommitMsgDialogClearsReasonOnRedraft(t *testing.T) {
	d := newCommitMsgDialog(
		domain.Feature{ID: "FD-001", Slug: "dark-mode"},
		func(_ string, _ domain.LandMethod) tea.Cmd { return nil },
		func(ctx context.Context, f domain.Feature, fresh bool) (string, error) { return "", errors.New("boom") },
	)
	d.apply(d.startDraft(false)().(commitDraftMsg))
	if d.reason == "" {
		t.Fatal("expected a reason from the failing pass")
	}
	// a fresh pass starts drafting with the reason cleared, so the
	// "drafting…" affordance replaces the stale explanation.
	d.startDraft(false)
	if d.reason != "" {
		t.Fatalf("redraft left reason %q, want cleared", d.reason)
	}
	if !d.drafting {
		t.Fatal("redraft should mark the pass as in-flight")
	}
}

// TestCommitDraftFailurePersistsDurably pins that a failed draft pass
// records its reason on the feature (surviving the dialog) and a
// successful draft clears it — the durable "later inspection" surface,
// not just an in-memory dialog line.
func TestCommitDraftFailurePersistsDurably(t *testing.T) {
	m, _ := newWorkspace(t)
	m = pump(t, m, m.Init())
	f := mkFeature(t, m.store, 1, "dark mode", domain.StageVerify)
	// give the card a committed worktree so a board reload would walk the
	// expensive Landed/merge-tree path — the cost the old handler paid for
	// a metadata-only note by returning m.loadRows.
	wtDir, err := m.wt.Create(context.Background(), &f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, "work.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, wtDir, "add", ".")
	git(t, wtDir, "commit", "-qm", "committed work")
	// count git spawns: persisting a draft outcome must not trigger a
	// board reload (the pre-restructure handler returned m.loadRows,
	// re-walking git state for a metadata-only note).
	logPath := gitShim(t)
	m = pump(t, m, m.loadRows)
	before := len(gitLogLines(t, logPath))

	reason := "draft unavailable: scribe session could not open: boom"
	m = pump(t, m, func() tea.Msg { return commitDraftMsg{f: f.ID, gen: 1, draft: "", reason: reason} })
	// the durable write landed on the feature...
	got, err := m.store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitDraftFail != reason {
		t.Fatalf("durable reason = %q, want %q", got.CommitDraftFail, reason)
	}
	// ...and is reflected on the already-open dashboard row in place,
	// with no board reload (no new git spawns).
	if m.rows[0].F.CommitDraftFail != reason {
		t.Fatalf("row did not reflect durable reason %q, want it shown in place", reason)
	}
	if after := len(gitLogLines(t, logPath)); after != before {
		t.Fatalf("git spawns grew from %d to %d reflecting a draft outcome (want no board reload)", before, after)
	}

	// a successful draft clears the durable note
	m = pump(t, m, func() tea.Msg { return commitDraftMsg{f: f.ID, gen: 2, draft: "feat(ui): dark mode", reason: ""} })
	got, err = m.store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CommitDraftFail != "" {
		t.Fatalf("successful draft left durable reason %q, want cleared", got.CommitDraftFail)
	}
	if m.rows[0].F.CommitDraftFail != "" {
		t.Fatalf("row still shows durable reason %q after a successful draft", m.rows[0].F.CommitDraftFail)
	}
}

// TestCommitMsgDialogArmsBeforeSubmittingUnmodifiedDraft pins BG-054:
// ctrl+s against a scribe draft the operator never reviewed must not land
// on the first press — merge() arms instead of submitting. Only a
// second press, with the text still unmodified, actually lands it.
func TestCommitMsgDialogArmsBeforeSubmittingUnmodifiedDraft(t *testing.T) {
	d := newTestCommitMsgDialog(t)
	d.gen = 1
	d.apply(commitDraftMsg{f: d.feature, gen: 1, draft: "feat(ui): add dark mode"})
	if d.drafting {
		t.Fatal("apply left drafting true; probe assumption invalid")
	}
	if d.modified {
		t.Fatal("apply marked modified; probe assumption invalid")
	}

	if ok, _ := d.merge(); ok {
		t.Fatal("BG-054: first ctrl+s against an unreviewed draft submitted immediately, want arm")
	}
	if !d.armed {
		t.Fatal("first ctrl+s against an unreviewed draft did not arm")
	}
	if v := d.View(theme.New(theme.GummiDark()), 80, 24); !strings.Contains(v, "ctrl+s again") {
		t.Fatalf("armed dialog missing the confirm hint:\n%s", v)
	}

	if ok, _ := d.merge(); !ok {
		t.Fatal("second ctrl+s against the still-unmodified draft did not submit")
	}
}

// TestCommitMsgDialogRedraftClearsArm pins the "can't be primed once and
// fired later against different, unseen text" guarantee: a fresh draft
// pass invalidates an arm taken against the text it is about to replace.
func TestCommitMsgDialogRedraftClearsArm(t *testing.T) {
	d := newTestCommitMsgDialog(t)
	d.gen = 1
	d.apply(commitDraftMsg{f: d.feature, gen: 1, draft: "feat(ui): add dark mode"})
	if ok, _ := d.merge(); ok {
		t.Fatal("first ctrl+s should arm, not submit")
	}
	if !d.armed {
		t.Fatal("merge did not arm")
	}

	d.startDraft(false)
	if d.armed {
		t.Fatal("a fresh draft pass left a stale arm from the previous draft")
	}
}

// TestCommitMsgDialogTypingAfterArmingSubmitsWithoutFurtherConfirm pins
// that reviewing (typing into) an armed, unreviewed draft is itself the
// confirmation — the next ctrl+s submits normally, it doesn't stay armed.
func TestCommitMsgDialogTypingAfterArmingSubmitsWithoutFurtherConfirm(t *testing.T) {
	d := newTestCommitMsgDialog(t)
	d.gen = 1
	d.apply(commitDraftMsg{f: d.feature, gen: 1, draft: "feat(ui): add dark mode"})
	if ok, _ := d.merge(); ok {
		t.Fatal("first ctrl+s should arm, not submit")
	}

	d.HandleKey(tea.KeyPressMsg{Code: '!', Text: "!"})
	if !d.modified {
		t.Fatal("typing a character should mark the box modified")
	}

	if ok, _ := d.merge(); !ok {
		t.Fatal("ctrl+s after the operator edited the draft should submit immediately")
	}
}

// TestCommitMsgDialogHidesDraftingHintOnceModified pins the drafting
// affordance to an unmodified box: once the operator types, the hint
// must not claim it won't be overwritten while the draft it's warning
// about has already been overwritten by hand.
func TestCommitMsgDialogHidesDraftingHintOnceModified(t *testing.T) {
	d := newTestCommitMsgDialog(t)
	d.startDraft(false)
	if v := d.View(theme.New(theme.GummiDark()), 80, 24); !strings.Contains(v, "drafting a suggested message") {
		t.Fatalf("drafting affordance missing while drafting:\n%s", v)
	}
	d.HandleKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if !d.modified {
		t.Fatal("typing a character should mark the box modified")
	}
	if v := d.View(theme.New(theme.GummiDark()), 80, 24); strings.Contains(v, "drafting a suggested message") {
		t.Fatalf("drafting hint shown while the operator is editing:\n%s", v)
	}
}

// TestCommitDialogTogglesMethod: ctrl+t flips the landing between squash and
// merge commit, the title and verb follow, and the submit carries the method
// in force.
// TestCommitDialogMergeHidesMessageBox: picking merge in the method field
// hides the message box, its hints and Redraft, and says the merge takes
// git's own message; picking squash again brings the box back.
func TestCommitDialogMergeHidesMessageBox(t *testing.T) {
	d := newCommitMsgDialog(domain.Feature{ID: "FD-001", Slug: "x"}, func(_ string, _ domain.LandMethod) tea.Cmd { return nil }, nil)
	d.baseBranch = "main"
	d.input.SetValue("land the thing")
	d.modified = true
	view := func() string { return ansi.Strip(d.View(theme.New(theme.GummiDark()), 100, 30)) }

	if got := view(); !strings.Contains(got, "Method: ‹ Squash — one commit ›") || !strings.Contains(got, "Redraft") {
		t.Fatalf("squash view lacks the method field or Redraft:\n%s", got)
	}
	d.HandleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	got := view()
	if !strings.Contains(got, "Method: ‹ Merge commit — keeps the branch's commits ›") {
		t.Fatalf("merge view lacks the merge method:\n%s", got)
	}
	if strings.Contains(got, "land the thing") || strings.Contains(got, "Redraft") {
		t.Errorf("merge view still shows the message box or Redraft:\n%s", got)
	}
	if !strings.Contains(got, "git's merge message") {
		t.Errorf("merge view does not say the merge takes git's message:\n%s", got)
	}
	if !strings.Contains(got, "merge FD-001") || !strings.Contains(got, "ctrl+s merge") {
		t.Errorf("merge view title or footer does not name the merge:\n%s", got)
	}
	d.HandleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if got := view(); !strings.Contains(got, "land the thing") || !strings.Contains(got, "Redraft") {
		t.Errorf("back to squash, the box did not return with its text:\n%s", got)
	}
}

// TestCommitDialogMergeLandsWithoutDraft: a merge submits at once with an
// empty message, without arming on unmodified text and without a draft.
func TestCommitDialogMergeLandsWithoutDraft(t *testing.T) {
	var gotMsg string
	var gotMethod domain.LandMethod
	submitted := 0
	drafted := false
	d := newCommitMsgDialog(domain.Feature{ID: "FD-001", Slug: "x"}, func(msg string, method domain.LandMethod) tea.Cmd {
		submitted++
		gotMsg, gotMethod = msg, method
		return nil
	}, func(context.Context, domain.Feature, bool) (string, error) {
		drafted = true
		return "", nil
	})
	d.baseBranch = "main"
	d.gen = 1
	d.apply(commitDraftMsg{f: d.feature, gen: 1, draft: "feat(ui): a scribe draft"})
	d.HandleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})

	if done, _ := d.HandleKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}); !done {
		t.Fatal("ctrl+s on a merge did not land at once")
	}
	if submitted != 1 || gotMsg != "" || gotMethod != domain.LandMerge {
		t.Fatalf("submit = %d×, %q, %q; want one empty-message merge", submitted, gotMsg, gotMethod)
	}
	if drafted {
		t.Error("a merge asked for a draft")
	}
}

// TestCommitDialogMergeIgnoresRedraft: a merge has no draft to redraft, so
// ctrl+r starts nothing while merge is selected.
func TestCommitDialogMergeIgnoresRedraft(t *testing.T) {
	d := newCommitMsgDialog(domain.Feature{ID: "FD-001", Slug: "x"}, func(_ string, _ domain.LandMethod) tea.Cmd { return nil },
		func(context.Context, domain.Feature, bool) (string, error) { return "", nil })
	d.HandleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if _, cmd := d.HandleKey(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}); cmd != nil || d.drafting {
		t.Fatal("ctrl+r started a draft while merge is selected")
	}
}

// TestCommitDialogMergeCancelsInFlightDraft: a draft still running when
// merge is picked is cancelled, and its late reply is dropped.
func TestCommitDialogMergeCancelsInFlightDraft(t *testing.T) {
	d := newCommitMsgDialog(domain.Feature{ID: "FD-001", Slug: "x"}, func(_ string, _ domain.LandMethod) tea.Cmd { return nil },
		func(context.Context, domain.Feature, bool) (string, error) { return "", nil })
	d.startDraft(false)
	if !d.drafting {
		t.Fatal("no draft in flight to start the test from")
	}
	gen := d.gen
	d.HandleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if d.drafting || d.gen == gen {
		t.Fatalf("picking merge left the draft running (drafting=%v)", d.drafting)
	}
	d.apply(commitDraftMsg{f: d.feature, gen: gen, draft: "late"})
	if d.input.Value() != "" {
		t.Errorf("a late reply from the cancelled draft filled the box: %q", d.input.Value())
	}
}

// TestCommitDialogMethodFieldCyclesWithArrows: the method field is the
// first tab stop; ←/→ cycle it, and tab from the box reaches the buttons.
func TestCommitDialogMethodFieldCyclesWithArrows(t *testing.T) {
	d := newCommitMsgDialog(domain.Feature{ID: "FD-001", Slug: "x"}, func(_ string, _ domain.LandMethod) tea.Cmd { return nil }, nil)
	if d.method != domain.LandSquash {
		t.Fatalf("default method = %q, want squash", d.method)
	}
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: 0})
	if d.focus != commitFieldButtons {
		t.Fatalf("tab from the box went to focus %d, want the buttons", d.focus)
	}
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}) // back to the box
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}) // and on to the first stop: the method field
	if d.focus != commitFieldMethod {
		t.Fatalf("shift+tab did not reach the method field: focus %d", d.focus)
	}
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if d.method != domain.LandMerge {
		t.Fatalf("→ on the method field left method %q, want merge", d.method)
	}
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	if d.method != domain.LandSquash {
		t.Fatalf("← on the method field left method %q, want squash", d.method)
	}
}

// TestCommitDialogMergeBackToSquashKeepsText: switching to merge and back
// keeps the box's text, and the squash it returns to still needs its
// ordinary second confirmation for untouched text.
func TestCommitDialogMergeBackToSquashKeepsText(t *testing.T) {
	var gotMsg string
	d := newCommitMsgDialog(domain.Feature{ID: "FD-001", Slug: "x"}, func(msg string, _ domain.LandMethod) tea.Cmd {
		gotMsg = msg
		return nil
	}, nil)
	d.input.SetValue("land the thing")
	d.modified = true
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight}) // merge
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyLeft})  // squash again
	if d.input.Value() != "land the thing" {
		t.Fatalf("box text after merge and back = %q, want it kept", d.input.Value())
	}
	if done, _ := d.HandleKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}); !done || gotMsg != "land the thing" {
		t.Fatalf("squash back on its text did not land it: done=%v msg=%q", done, gotMsg)
	}
}

// TestCommitDialogDefaultSubmitSquashes: a dialog never toggled submits squash.
func TestCommitDialogDefaultSubmitSquashes(t *testing.T) {
	var gotMethod domain.LandMethod
	d := newCommitMsgDialog(domain.Feature{ID: "FD-001", Slug: "x"}, func(_ string, method domain.LandMethod) tea.Cmd {
		gotMethod = method
		return nil
	}, nil)
	d.input.SetValue("land the thing")
	d.modified = true
	d.HandleKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if gotMethod != domain.LandSquash {
		t.Fatalf("default submit method = %q, want squash", gotMethod)
	}
}

// TestCommitDialogNoMethodToggleForGoalCard: a goal's card lands as a squash
// only, so the dialog offers no toggle and ctrl+t does nothing.
func TestCommitDialogNoMethodToggleForGoalCard(t *testing.T) {
	d := newCommitMsgDialog(domain.Feature{ID: "FD-002", Slug: "x", GoalID: "GL-001"}, func(_ string, _ domain.LandMethod) tea.Cmd { return nil }, nil)
	if d.canToggle() {
		t.Fatal("a card in a goal offers a method toggle")
	}
	d.HandleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if d.method != domain.LandSquash {
		t.Fatalf("ctrl+t changed a goal card's method to %q", d.method)
	}
}

// TestCommitDialogSquashInPlaceHasNoMethod: a squash in place is one commit
// on the branch; it offers no landing method at all.
func TestCommitDialogSquashInPlaceHasNoMethod(t *testing.T) {
	d := newCommitMsgDialog(domain.Feature{ID: "FD-001", Slug: "x"}, func(_ string, _ domain.LandMethod) tea.Cmd { return nil }, nil).squashInPlace()
	if d.canToggle() {
		t.Fatal("squash in place offers a landing method")
	}
	d.HandleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if d.method != domain.LandSquash {
		t.Fatalf("ctrl+t changed a squash in place to %q", d.method)
	}
}
