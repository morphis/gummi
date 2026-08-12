package ui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// commitMsgDialog collects the squash-merge commit message before the
// merge runs. It opens immediately with an empty editable textarea, and a
// best-effort scribe pass drafts a suggested landing message while the
// user reads and edits. The draft fills the textarea only if the user has
// not typed into it — it never clobbers keystrokes — and merges on
// ctrl+s, so a plain enter stays free for editing multi-line messages.
// The human gate is unchanged: nothing lands except on an explicit
// ctrl+s.
type commitMsgDialog struct {
	feature  domain.FeatureID
	f        domain.Feature
	branch   string
	input    textarea.Model
	onSubmit func(message string) tea.Cmd
	// draft runs a read-only, best-effort scribe pass for the landing
	// message under a caller-provided context (so esc cancels it); a nil
	// backend or any failure returns an empty draft.
	draft func(ctx context.Context, f domain.Feature) (string, error)
	// gen tags each draft pass so a stale reply can't clobber a re-draft
	// or a closed dialog.
	gen      int
	cancel   context.CancelFunc
	drafting bool // a draft pass is in flight — show the "drafting…" affordance
	modified bool // the user has typed; never overwrite their keystrokes
}

func newCommitMsgDialog(f domain.Feature, onSubmit func(string) tea.Cmd, draft func(ctx context.Context, f domain.Feature) (string, error)) *commitMsgDialog {
	in := textarea.New()
	in.Placeholder = "commit message"
	in.CharLimit = 4000
	in.ShowLineNumbers = false
	in.SetWidth(64)
	in.SetHeight(8)
	in.Focus()
	return &commitMsgDialog{feature: f.ID, f: f, branch: f.BranchName(), input: in, onSubmit: onSubmit, draft: draft}
}

// startDraft launches a fresh best-effort draft pass for this dialog and
// returns the command to run it. A stale in-flight pass is cancelled
// first. The arriving commitDraftMsg carries the generation, so apply
// only honors the latest.
func (d *commitMsgDialog) startDraft() tea.Cmd {
	d.gen++
	gen := d.gen
	if d.cancel != nil {
		d.cancel()
	}
	// the pass runs under this context so esc (or a re-draft) cancels it:
	// a wedged backend can then never leave work hanging past the dialog.
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.drafting = true
	f := d.f
	return func() tea.Msg {
		draft, _ := d.draft(ctx, f)
		cancel() // release the bound even on the fast path
		return commitDraftMsg{f: d.feature, gen: gen, draft: draft}
	}
}

// apply fills the textarea with a completed draft, honoring the "only
// while the user hasn't modified it" and "only the latest pass" rules.
func (d *commitMsgDialog) apply(msg commitDraftMsg) {
	if msg.gen != d.gen || d.modified {
		return
	}
	d.drafting = false
	if msg.draft == "" {
		return // nothing usable — leave the dialog empty
	}
	d.input.SetValue(msg.draft)
}

// ID implements overlay.Dialog.
func (d *commitMsgDialog) ID() string { return "commit-message" }

// HandleKey implements overlay.Dialog.
func (d *commitMsgDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		// cancel the whole merge, including an in-flight draft; a late
		// reply after this sees a closed dialog and is dropped.
		if d.cancel != nil {
			d.cancel()
		}
		return true, nil
	case "ctrl+s":
		text := strings.TrimSpace(d.input.Value())
		if text == "" {
			return false, nil // nothing to commit with — keep editing
		}
		if d.cancel != nil {
			d.cancel()
		}
		return true, d.onSubmit(text)
	case "ctrl+r":
		// regenerate the draft; applies only while the user hasn't typed.
		return false, d.startDraft()
	}
	d.modified = true
	d.input, _ = d.input.Update(key)
	return false, nil
}

// HandlePaste implements overlay.Paster.
func (d *commitMsgDialog) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	d.modified = true
	d.input, _ = d.input.Update(msg)
	return nil
}

// View implements overlay.Dialog.
func (d *commitMsgDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("squash-merge "+string(d.feature)) + "\n")
	b.WriteString(s.Subtle.Render(d.branch+" → main") + "\n\n")
	b.WriteString(d.input.View() + "\n")
	if d.drafting && !d.modified {
		b.WriteString("\n" + s.Faint.Render("drafting a suggested message… (edit below to keep yours)"))
	}
	b.WriteString("\n" + s.Faint.Render("ctrl+s merge · ctrl+r regenerate · esc cancel"))
	return s.DialogFrame.Render(b.String())
}
