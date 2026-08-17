package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

func newTestCommitMsgDialog(t *testing.T) *commitMsgDialog {
	t.Helper()
	return newCommitMsgDialog(
		domain.Feature{ID: "FD-001", Slug: "dark-mode"},
		func(string) tea.Cmd { return nil },
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
			prefix: "draft unavailable",
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
				func(string) tea.Cmd { return nil },
				func(ctx context.Context, f domain.Feature) (string, error) { return "", tc.err },
			)
			msg, ok := d.startDraft()().(commitDraftMsg)
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
		func(string) tea.Cmd { return nil },
		func(ctx context.Context, f domain.Feature) (string, error) { return "", errors.New("boom") },
	)
	d.apply(d.startDraft()().(commitDraftMsg))
	if d.reason == "" {
		t.Fatal("expected a reason from the failing pass")
	}
	// a fresh pass starts drafting with the reason cleared, so the
	// "drafting…" affordance replaces the stale explanation.
	d.startDraft()
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

// TestCommitMsgDialogHidesDraftingHintOnceModified pins the drafting
// affordance to an unmodified box: once the operator types, the hint
// must not claim "edit below to keep yours" over their own keystrokes.
func TestCommitMsgDialogHidesDraftingHintOnceModified(t *testing.T) {
	d := newTestCommitMsgDialog(t)
	d.startDraft()
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
