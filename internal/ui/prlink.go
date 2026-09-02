package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// prLinkProbeMsg carries the pre-fill probe's outcome: prefill is the
// single matching PR number (exactly one `gh pr list --head branch` match),
// hint is a faint, non-blocking note (no match — most branches simply have
// no PR yet), and err is a genuine gh failure (not authenticated, no
// network, gh missing), surfaced as a blocking notice before the dialog
// still opens empty.
type prLinkProbeMsg struct {
	f       domain.Feature
	prefill string
	hint    string
	err     error
}

// noPRFoundMarker is the substring internal/pr.resolveAuto's zero-match
// error carries — the one auto-resolution failure that reads as "no PR
// yet" rather than a gh problem, so the probe can tell it apart from every
// other error resolvePR can return (auth, network, gh missing, or an
// ambiguous multi-match).
const noPRFoundMarker = "found no open PR"

// openPRLinkDialog runs the pre-fill probe off the render loop (the shape
// prepareMerge/prepareSquash use) before the dialog opens, so a branch with
// exactly one open PR needs only an enter to confirm it.
func (m *Shell) openPRLinkDialog(f domain.Feature) tea.Cmd {
	if !f.PullRequest.Empty() {
		m.notice = noticeMsg{text: fmt.Sprintf("%s is already linked to %s#%d (%s); unlink it first",
			f.ID, f.PullRequest.Repo, f.PullRequest.Number, f.PullRequest.URL), isErr: true}
		return nil
	}
	return func() tea.Msg {
		if m.resolvePR == nil {
			return prLinkProbeMsg{f: f}
		}
		ctx := context.Background()
		mgr, err := m.wt.ManagerFor(ctx, &f)
		if err != nil {
			return prLinkProbeMsg{f: f, err: err}
		}
		ref, err := m.resolvePR(ctx, "", mgr.RepoRoot(), f.BranchName())
		if err != nil {
			if strings.Contains(err.Error(), noPRFoundMarker) {
				return prLinkProbeMsg{f: f, hint: "no PR found for this branch"}
			}
			return prLinkProbeMsg{f: f, err: err}
		}
		return prLinkProbeMsg{f: f, prefill: strconv.Itoa(ref.Number)}
	}
}

// handlePRLinkProbe folds a completed probe into a notice (only on a real
// gh failure) and opens the dialog either way — manual URL/number entry
// remains the fallback rather than a dead end.
func (m *Shell) handlePRLinkProbe(msg prLinkProbeMsg) {
	if msg.err != nil {
		m.notice = noticeMsg{text: sanitize(string(msg.f.ID) + ": checking for an open PR: " + msg.err.Error()), isErr: true}
	} else {
		m.notice = noticeMsg{}
	}
	m.Overlay.Push(newPRLinkDialog(msg.f, msg.prefill, msg.hint, func(spec string) tea.Cmd {
		return m.submitPRLink(msg.f, spec)
	}))
}

// prLinkDialog fields, in tab order — mirrors envelopeDialog.
const (
	prLinkFieldInput = iota
	prLinkFieldButtons
)

// prLinkDialog is the one-field popover linking a card to a PR — modelled
// on envelopeDialog, the smallest existing popover with a text field and a
// submit. It accepts a full PR URL, a bare number, or an empty submit
// meaning auto (pr.Resolve's own trichotomy, applied unchanged).
type prLinkDialog struct {
	feature  domain.Feature
	input    textinput.Model
	buttons  *buttonRow
	focus    int
	hint     string // faint "no PR found for this branch" note; "" once the field carries a prefill or the user has typed
	problem  string
	onSubmit func(spec string) tea.Cmd
}

func newPRLinkDialog(f domain.Feature, prefill, hint string, onSubmit func(string) tea.Cmd) *prLinkDialog {
	in := textinput.New()
	in.Placeholder = "PR URL or number (blank = auto)"
	in.CharLimit = 200
	in.SetWidth(40)
	in.SetValue(prefill)
	in.Focus()
	if prefill != "" {
		hint = ""
	}
	return &prLinkDialog{
		feature: f, input: in, hint: hint, onSubmit: onSubmit,
		buttons: newButtonRow(button{label: "Cancel"}, button{label: "Link"}),
	}
}

// ID implements overlay.Dialog.
func (d *prLinkDialog) ID() string { return "pr-link" }

func (d *prLinkDialog) submit() (bool, tea.Cmd) {
	return true, d.onSubmit(strings.TrimSpace(d.input.Value()))
}

// HandleKey implements overlay.Dialog.
func (d *prLinkDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "tab", "shift+tab":
		d.setFocus((d.focus + 1) % 2)
		return false, nil
	}
	if d.focus == prLinkFieldButtons {
		switch key.String() {
		case "left", "h":
			d.buttons.Move(-1)
			return false, nil
		case "right", "l":
			d.buttons.Move(1)
			return false, nil
		case "enter":
			if d.buttons.Cursor() == 0 {
				return true, nil
			}
			return d.submit()
		}
		return false, nil
	}
	if key.String() == "enter" {
		return d.submit()
	}
	d.problem, d.hint = "", ""
	d.input, _ = d.input.Update(key)
	return false, nil
}

func (d *prLinkDialog) setFocus(f int) {
	d.focus = f
	if f == prLinkFieldInput {
		d.input.Focus()
	} else {
		d.input.Blur()
	}
}

// HandlePaste implements overlay.Paster.
func (d *prLinkDialog) HandlePaste(msg tea.PasteMsg) tea.Cmd {
	if d.focus == prLinkFieldInput {
		d.input, _ = d.input.Update(msg)
	}
	return nil
}

// View implements overlay.Dialog.
func (d *prLinkDialog) View(s *theme.Styles, w, h int) string {
	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("link PR · "+string(d.feature.ID)) + "\n\n")
	b.WriteString(d.input.View() + "\n")
	if d.hint != "" {
		b.WriteString(s.Faint.Render(d.hint) + "\n")
	}
	if d.problem != "" {
		b.WriteString(s.Error.Render(d.problem) + "\n")
	}
	b.WriteString("\n" + d.buttons.View(s, d.focus == prLinkFieldButtons) + "\n")
	b.WriteString("\n" + s.Faint.Render("enter link · tab buttons · esc cancel"))
	return s.DialogFrame.Render(b.String())
}

// submitPRLink resolves spec (a URL, a bare number, or "" for auto) via
// pr.Resolve and links f. It re-reads the feature from the store and
// re-checks the already-linked guard at run time, not from the snapshot the
// dialog was built from — the board row may be stale against a concurrent
// `gummi pr link` elsewhere.
func (m *Shell) submitPRLink(f domain.Feature, spec string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if m.resolvePR == nil {
			return noticeMsg{text: "link PR is unavailable — no PR backend wired", isErr: true}
		}
		cur, err := m.store.GetFeature(ctx, f.ID)
		if err != nil {
			return noticeMsg{text: sanitize(string(f.ID) + ": " + err.Error()), isErr: true}
		}
		if !cur.PullRequest.Empty() {
			return noticeMsg{text: fmt.Sprintf("%s is already linked to %s#%d (%s); unlink it first",
				f.ID, cur.PullRequest.Repo, cur.PullRequest.Number, cur.PullRequest.URL), isErr: true}
		}
		mgr, err := m.wt.ManagerFor(ctx, &f)
		if err != nil {
			return noticeMsg{text: sanitize(string(f.ID) + ": " + err.Error()), isErr: true}
		}
		ref, err := m.resolvePR(ctx, spec, mgr.RepoRoot(), f.BranchName())
		if err != nil {
			return noticeMsg{text: sanitize(string(f.ID) + ": resolving PR: " + err.Error()), isErr: true}
		}
		if err := m.store.SetPullRequest(ctx, f.ID, ref); err != nil {
			return noticeMsg{text: sanitize(string(f.ID) + ": " + err.Error()), isErr: true}
		}
		text := fmt.Sprintf("%s linked to %s#%d", f.ID, ref.Repo, ref.Number)
		if m.prSquashMergeAllowed != nil {
			// non-blocking caution, the same repo-can't-squash-merge warning
			// `gummi pr link` prints (efd73cd) — best-effort like
			// prepareMerge's own provenance warn: a lookup failure here just
			// means the caution is skipped, not that linking failed.
			if allowed, err := m.prSquashMergeAllowed(ctx, ref.Repo); err == nil && !allowed {
				text += "\nwarning: " + ref.Repo + " does not allow squash-merge on GitHub; run `z` to collapse the branch to one commit before merging so main stays clean"
			}
		}
		return noticeMsg{text: text, reload: true}
	}
}
