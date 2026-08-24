package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// repoPickerDialog is the inline popover that retargets a card's managed
// repository before it cuts a worktree. It cycles through the workspace's
// selectable repos: the empty default name plus every configured named repo.
type repoPickerDialog struct {
	feature    domain.Feature
	candidates []string // candidates[0] == "" (default); rest are sorted names
	idx        int
	onSubmit   func(repo string) tea.Cmd
}

func newRepoPickerDialog(f domain.Feature, names []string, onSubmit func(string) tea.Cmd) *repoPickerDialog {
	candidates := make([]string, 0, len(names)+1)
	candidates = append(candidates, "")
	candidates = append(candidates, names...)
	idx := 0
	for i, n := range candidates {
		if n == f.Repo {
			idx = i
			break
		}
	}
	return &repoPickerDialog{feature: f, candidates: candidates, idx: idx, onSubmit: onSubmit}
}

// ID implements overlay.Dialog.
func (d *repoPickerDialog) ID() string { return "repo" }

// HandleKey implements overlay.Dialog.
func (d *repoPickerDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "enter":
		return true, d.onSubmit(d.candidates[d.idx])
	case "left", "h":
		d.idx--
		if d.idx < 0 {
			d.idx = len(d.candidates) - 1
		}
		return false, nil
	case "right", "l", "r":
		d.idx++
		if d.idx >= len(d.candidates) {
			d.idx = 0
		}
		return false, nil
	}
	return false, nil
}

// View implements overlay.Dialog.
func (d *repoPickerDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("repo · "+string(d.feature.ID)) + "\n\n")
	now := d.feature.Repo
	if now == "" {
		now = "default"
	}
	b.WriteString(s.Faint.Render("now "+now) + "\n\n")
	b.WriteString(repoPickerOptions(s, d.candidates, d.idx) + "\n")
	b.WriteString("\n" + s.Faint.Render("←/→ · h/l · r cycle · enter set · esc cancel"))
	return s.DialogFrame.Render(b.String())
}

func repoPickerOptions(s *theme.Styles, candidates []string, idx int) string {
	parts := make([]string, len(candidates))
	for i, c := range candidates {
		label := c
		if label == "" {
			label = "default"
		}
		if i == idx {
			parts[i] = s.Selection.Render("▸ " + label)
		} else {
			parts[i] = s.Faint.Render("  " + label)
		}
	}
	return strings.Join(parts, "   ")
}
