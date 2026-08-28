package ui

import (
	"context"
	"errors"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
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
	// reason is a non-empty failure explanation from the last draft pass
	// (empty on success); guard marks a deliberate guard rejection (diff
	// dump / attribution) so it reads differently from a config fault.
	reason string
	guard  bool

	// focus is the tab position (text ⇄ buttons); buttons carries the
	// Cancel/Redraft/Merge row that makes the merge reachable without a
	// Ctrl key a multiplexer might claim.
	focus   int
	buttons *buttonRow
}

func newCommitMsgDialog(f domain.Feature, onSubmit func(string) tea.Cmd, draft func(ctx context.Context, f domain.Feature) (string, error)) *commitMsgDialog {
	in := textarea.New()
	in.Placeholder = "commit message"
	in.CharLimit = 4000
	in.ShowLineNumbers = false
	in.SetWidth(64)
	in.SetHeight(8)
	in.Focus()
	return &commitMsgDialog{
		feature: f.ID, f: f, branch: f.BranchName(), input: in, onSubmit: onSubmit, draft: draft,
		buttons: newButtonRow(
			button{label: "Cancel"},
			button{label: "Redraft"},
			button{label: "Merge", danger: true},
		),
	}
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
	d.reason = ""
	f := d.f
	return func() tea.Msg {
		draft, err := d.draft(ctx, f)
		cancel() // release the bound even on the fast path
		msg := commitDraftMsg{f: d.feature, gen: gen, draft: draft}
		if err != nil {
			var guard *engine.CommitDraftGuardError
			if errors.As(err, &guard) {
				msg.guard = true
				msg.reason = guard.Error()
			} else {
				msg.reason = "draft unavailable: " + err.Error()
			}
		}
		return msg
	}
}

// apply fills the textarea with a completed draft, honoring the "only
// while the user hasn't modified it" and "only the latest pass" rules.
func (d *commitMsgDialog) apply(msg commitDraftMsg) {
	if msg.gen != d.gen {
		return // stale pass (a re-draft or a closed dialog) is dropped
	}
	d.drafting = false
	d.reason = msg.reason
	d.guard = msg.guard
	if d.modified || msg.draft == "" {
		return // keep the user's keystrokes, or keep the box empty on failure
	}
	d.input.SetValue(msg.draft)
}

// ID implements overlay.Dialog.
func (d *commitMsgDialog) ID() string { return "commit-message" }

// commit message fields, in tab order.
const (
	commitFieldText = iota
	commitFieldButtons
)

// merge validates and fires onSubmit — the same path ctrl+s and the
// Merge button both reach.
func (d *commitMsgDialog) merge() (bool, tea.Cmd) {
	text := strings.TrimSpace(d.input.Value())
	if text == "" {
		return false, nil // nothing to commit with — keep editing
	}
	if d.cancel != nil {
		d.cancel()
	}
	return true, d.onSubmit(text)
}

// HandleKey implements overlay.Dialog. The textarea owns enter (a commit
// message is multi-line), so the merge lives on a button row one tab
// away. ctrl+s stays as an accelerator, but it can no longer be the only
// way through: zellij binds ctrl+s to search mode, which made the most
// consequential action in gummi unreachable inside a multiplexer.
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
		return d.merge()
	case "ctrl+r":
		// regenerate the draft; applies only while the user hasn't typed.
		return false, d.startDraft()
	case "tab", "shift+tab":
		d.focus = (d.focus + 1) % 2
		if d.focus == commitFieldText {
			d.input.Focus()
		} else {
			d.input.Blur()
		}
		return false, nil
	}
	if d.focus == commitFieldButtons {
		switch key.String() {
		case "left", "h":
			d.buttons.Move(-1)
		case "right", "l":
			d.buttons.Move(1)
		case "enter":
			switch d.buttons.Cursor() {
			case 0: // Cancel
				if d.cancel != nil {
					d.cancel()
				}
				return true, nil
			case 1: // Redraft
				return false, d.startDraft()
			default: // Merge
				return d.merge()
			}
		}
		return false, nil
	}
	before := d.input.Value()
	d.input, _ = d.input.Update(key)
	if d.input.Value() != before {
		d.modified = true
	}
	return false, nil
}

// HandlePaste implements overlay.Paster.
func (d *commitMsgDialog) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	if d.focus != commitFieldText {
		return nil
	}
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
	switch {
	case d.drafting && !d.modified:
		b.WriteString("\n" + s.Faint.Render("drafting a suggested message… (edit below to keep yours)"))
	case d.reason != "":
		// a deliberate guard rejection is a correctness guard firing, not a
		// fault — warn rather than alarm, and never offer to fix a profile.
		if d.guard {
			b.WriteString("\n" + s.Warning.Render(d.reason))
		} else {
			b.WriteString("\n" + s.Error.Render(d.reason))
		}
	}
	b.WriteString("\n\n" + d.buttons.View(s, d.focus == commitFieldButtons) + "\n")
	b.WriteString("\n" + s.Faint.Render("tab buttons · enter activates · ctrl+s merge · esc cancel"))
	return s.DialogFrame.Render(b.String())
}
