package ui

import (
	"context"
	"errors"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/worktree"
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
	feature domain.FeatureID
	f       domain.Feature
	branch  string
	// baseBranch is the branch this merge actually lands on. It is not a
	// constructor parameter: both call sites (shell.go's openSquashDialog
	// and its mergeReadyMsg handler) are *Shell methods with m in scope,
	// so the intended wiring is a one-line `d.baseBranch = m.baseBranch(f)`
	// right after construction — a field, not a signature change, so nothing
	// outside this file has to change to add it. Left unset it falls back
	// to worktree.DefaultBaseBranchName (base()), same as everywhere else
	// this fallback appears, so a merge dialog with nobody wiring it yet
	// still says something rather than nothing (REVIEW-ux-drive-2026-09-10-
	// round2.md §3.4).
	baseBranch string
	input      textarea.Model
	onSubmit   func(message string) tea.Cmd
	// draft runs a read-only, best-effort scribe pass for the landing
	// message under a caller-provided context (so esc cancels it); a nil
	// backend or any failure returns an empty draft.
	draft func(ctx context.Context, f domain.Feature) (string, error)
	// gen tags each draft pass so a stale reply can't clobber a re-draft
	// or a closed dialog.
	gen      int
	cancel   context.CancelFunc
	drafting bool // a draft pass is in flight — show the "drafting…" affordance
	// startedAt is when the current draft pass began (startDraft), so the
	// "drafting…" line can show elapsed time and an animated glyph instead
	// of static text a 90-second wait is indistinguishable from a hang
	// behind. This dialog has no *Shell to read the package's shared frame
	// clock (m.frame) off, so the glyph is derived from wall-clock time
	// instead of that shared cadence — see spinnerGlyph.
	startedAt time.Time
	modified  bool // the user has typed; never overwrite their keystrokes
	// armed is set by the first merge() attempt against non-empty,
	// unmodified text (a scribe draft the operator hasn't reviewed) and
	// requires a second attempt to actually land it. Editing the box
	// (modified flips true) or a fresh startDraft() pass clears it, so an
	// arm from one draft can never fire against a later, unseen one.
	armed bool
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

// base is the branch this merge lands on, falling back to
// worktree.DefaultBaseBranchName the way every other reader of an
// unset/unattached baseBranch does (see the field's own doc comment).
func (d *commitMsgDialog) base() string {
	if d.baseBranch != "" {
		return d.baseBranch
	}
	return worktree.DefaultBaseBranchName
}

// spinnerGlyph is the "drafting…" line's activity marker. The package's
// shared spinner (spinner.go) advances off m.frame, which only ticks
// while Shell.spinnerActive() is true — this dialog is not one of the
// states that function checks, and wiring it in is a spinner.go/shell.go
// change outside this file's scope for this pass. Deriving the frame
// from wall-clock time instead means the glyph is correct whenever View
// happens to render (any keypress, the eventual reply), even without a
// dedicated tick loop keeping it live between them — a real improvement
// over the static text this replaces, short of the continuous animation
// a shared-loop wiring would give it.
func (d *commitMsgDialog) spinnerGlyph() string {
	if d.startedAt.IsZero() {
		return spinnerFrames[0]
	}
	n := int(time.Since(d.startedAt) / spinnerInterval)
	return spinnerFrames[n%len(spinnerFrames)]
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
	d.startedAt = time.Now()
	d.reason = ""
	d.armed = false // a fresh pass invalidates any earlier arm
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
	// an arm taken against whatever text was showing before this reply
	// must not carry over to the text this reply lands.
	d.armed = false
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
// Merge button both reach. Non-empty text the operator never modified is
// an unreviewed scribe draft, not something they authored or explicitly
// accepted; the first attempt against it only arms — the view shows a
// confirm hint — and a second attempt, with the text still unmodified,
// is what actually lands it.
func (d *commitMsgDialog) merge() (bool, tea.Cmd) {
	text := strings.TrimSpace(d.input.Value())
	if text == "" {
		return false, nil // nothing to commit with — keep editing
	}
	if !d.modified && !d.armed {
		d.armed = true
		return false, nil // arm: require a second attempt to land unreviewed text
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
// commitBodyCols is the widest the message box gets. A commit body is
// wrapped at ~72 columns by convention (and gummi's own drafts are), so
// anything narrower re-wraps an already-wrapped paragraph and produces
// the ragged mess this box used to show:
//
//	┃ - scripts had to scrape the padded text table;
//	┃ machine-readable JSON
//	┃   lets consumers parse structured data instead of
//	┃ screen-scraping
//
// The box was a fixed 64×8 regardless of the terminal, so on a 120-column
// screen the reader was asked to approve, here, a message they could not
// read here — eight lines of eighteen, with nothing saying there was
// more. A few columns of slack past 72 keeps the longest conventional
// line off the edge.
const commitBodyCols = 78

func (d *commitMsgDialog) View(s *theme.Styles, w, h int) string {
	// Size to the frame rather than to a constant. The dialog is given
	// its space here and nowhere else, and the textarea has to be told
	// before it renders.
	d.input.SetWidth(clamp(w-12, 40, commitBodyCols))
	// chrome: title, branch, blank, the status line and its blank, the
	// button row and its blanks, the hint, and the frame's own border.
	d.input.SetHeight(clamp(h-12, 6, 24))

	var b strings.Builder
	b.WriteString(s.DialogTitle.Render("squash-merge "+string(d.feature)) + "\n")
	b.WriteString(s.Subtle.Render(d.branch+" → "+d.base()) + "\n\n")
	b.WriteString(d.input.View() + "\n")
	// Say when the message continues past the box. Approving something
	// you cannot see all of is the failure this guards, and a reader with
	// no scrollbar has no other way to know there is more.
	if hidden := d.input.LineCount() - d.input.Height(); hidden > 0 {
		b.WriteString(s.Faint.Render("  ↓ "+itoa(hidden)+" more line"+plural(hidden)+" — ↑↓ scrolls the message") + "\n")
	}
	switch {
	case d.armed && !d.modified:
		// "unreviewed" used to accuse the reader of not reading a draft
		// that was, in the observed drive, sitting fully visible on
		// screen — what this guard actually checks is that the text is
		// untouched, so it says that instead.
		b.WriteString("\n" + s.Warning.Render("this is the scribe's draft, untouched — ctrl+s again to land it as written"))
	case d.drafting && !d.modified:
		// A live spinner and elapsed clock, not static text: this pass
		// took ~90s the first time and ~2min the second in the round 2
		// drive, with nothing on screen to say it was still working
		// rather than hung. d.spinnerGlyph derives its frame from
		// wall-clock time rather than the package's shared m.frame clock
		// (spinner.go's spinnerActive doesn't know about this dialog —
		// wiring that in is outside this file), so it is correct whenever
		// this renders even without a dedicated tick loop keeping it
		// live between renders.
		//
		// The old parenthetical — "(edit below to keep yours)" — read
		// backwards: there is nothing "below" yet while this is still
		// running, and "keep yours" described a race the reader can't
		// see. This says what typing actually does: it stops the draft
		// from overwriting what's been typed.
		line := d.spinnerGlyph() + " " + withElapsed("drafting a suggested message", time.Now(), d.startedAt)
		b.WriteString("\n" + s.Faint.Render(line+" — type your own and it won't be overwritten"))
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
