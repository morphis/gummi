package ui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// commitMsgDialog collects the squash-merge commit message before the
// merge runs: it opens empty — the landing commit's message is the
// user's to write, never generated — and merges on ctrl+s, so a plain
// enter stays free for editing multi-line messages.
type commitMsgDialog struct {
	feature  domain.FeatureID
	branch   string
	input    textarea.Model
	onSubmit func(message string) tea.Cmd
}

func newCommitMsgDialog(f domain.Feature, onSubmit func(string) tea.Cmd) *commitMsgDialog {
	in := textarea.New()
	in.Placeholder = "commit message"
	in.CharLimit = 4000
	in.ShowLineNumbers = false
	in.SetWidth(64)
	in.SetHeight(8)
	in.Focus()
	return &commitMsgDialog{feature: f.ID, branch: f.BranchName(), input: in, onSubmit: onSubmit}
}

// ID implements overlay.Dialog.
func (d *commitMsgDialog) ID() string { return "commit-message" }

// HandleKey implements overlay.Dialog.
func (d *commitMsgDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "ctrl+s":
		text := strings.TrimSpace(d.input.Value())
		if text == "" {
			return false, nil // nothing to commit with — keep editing
		}
		return true, d.onSubmit(text)
	}
	d.input, _ = d.input.Update(key)
	return false, nil
}

// HandlePaste implements overlay.Paster.
func (d *commitMsgDialog) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	d.input, _ = d.input.Update(msg)
	return nil
}

// View implements overlay.Dialog.
func (d *commitMsgDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("squash-merge "+string(d.feature)) + "\n")
	b.WriteString(s.Subtle.Render(d.branch+" → main") + "\n\n")
	b.WriteString(d.input.View() + "\n\n")
	b.WriteString(s.Faint.Render("ctrl+s merge · esc cancel"))
	return s.DialogFrame.Render(b.String())
}
