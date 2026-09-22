package ui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/cardmint"
	"github.com/morphis/gummi/internal/domain"
)

// The follow-up is what happens after a card is done and the work turns
// out to be wrong.
//
// Done is terminal and should stay terminal: a landed card's follow-up is
// new work with its own spec, not a rewind, and the workflow has no edge
// out of done for good reasons. What was missing was not an edge — it was
// a pen. The card holding the spec, the branch, the plan and the whole
// history offered nothing at all, so the honest path was `esc`, `n`, and
// typing out again the context this card is already keeping.
//
// So: the line you type at a finished card becomes a bug card, and the
// new card carries where it came from. One keystroke instead of a
// re-transcription, and the provenance is recorded rather than
// remembered.

// bugFromCardDesc builds the description the new card is minted from.
// Its FIRST LINE is the title (domain.SplitFreeform's rule, which every
// mint obeys), so the typed line leads and the provenance follows it into
// the draft's body — where a reader of the new card meets it as context
// rather than as a title nobody wrote.
func bugFromCardDesc(parent domain.Feature, line string, landed bool) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(line))
	b.WriteString("\n\nFound in " + string(parent.ID) + " — " + parent.Title + ".")
	switch {
	case parent.LandedSHA != "":
		b.WriteString(" That card landed as " + shortSHA(parent.LandedSHA) + ".")
	case landed:
		b.WriteString(" That card's branch is on the base branch.")
	case parent.HandedOff():
		b.WriteString(" That card was handed off; its branch " + parent.BranchName() + " was kept.")
	}
	b.WriteString("\nIts " + artifactNoun(parent.Kind) + " is " + parent.ArtifactPath() +
		" and its branch is " + parent.BranchName() + ".")
	return b.String()
}

// openBugFromCard raises the confirm for a follow-up bug minted from a
// finished card.
//
// It reads the composer rather than opening the new-card form: the line
// is already typed — that is what someone does when they find the problem
// — and routing through the form would ask again for what is on screen.
// With nothing typed it says what it wants, the same answer "changes" and
// the keyless "run" give one row away, rather than answering enter with
// silence.
func (m *Shell) openBugFromCard(r featureRow) tea.Cmd {
	return m.bugFromLine(r, strings.TrimSpace(m.threadInput.Value()))
}

// bugFromLine is openBugFromCard with the line named rather than read off
// the composer, for the re-entry's own route to this row
// (reentry.go's fixedSendBack). The two cannot be one function reading
// the composer: a line that reached the router through a conversation had
// its leading vocabulary word stripped on the way (chat.go's chatExit),
// so the composer holds a word the bug card must not be titled with.
func (m *Shell) bugFromLine(r featureRow, line string) tea.Cmd {
	if line == "" {
		m.notice = noticeMsg{text: "type what is wrong — your line becomes the bug card"}
		m.focusThreadInput()
		return nil
	}
	if parseInput(line).Kind != verbNone {
		// a "/verb" line belongs to the parser, not to this row; taking it
		// as prose would mint a card titled with a command.
		m.notice = noticeMsg{text: "that line is a command — type what is wrong instead", isErr: true}
		return nil
	}
	f, landed := r.F, r.Landed
	title, _, _ := domain.SplitFreeform(line)
	detail := "\na new bug card, carrying where it came from.\n\n" +
		"  " + pad("title") + title + "\n" +
		"  " + pad("found in") + string(f.ID) + " — " + endingWord(f.Ending(landed)) + "\n" +
		"  " + pad("carries") + f.ArtifactPath() + " · " + f.BranchName()
	m.Overlay.Push(&confirmDialog{
		id:           "confirm-bug-from-card",
		cancelLabel:  "Cancel",
		confirmLabel: "Open it",
		question:     "open a bug from " + string(f.ID) + "?",
		detail:       detail,
		onConfirm:    func() tea.Cmd { return m.mintBugFromCard(f, line, landed) },
	})
	return nil
}

// mintBugFromCard mints the follow-up through cardmint.Mint — the same
// recipe every other card is minted by — and clears the composer only
// once the mint has been asked for, so a line that fails to become a card
// is still on screen to try again with.
//
// The new card inherits the parent's profile, repository and envelope: a
// follow-up to a card lives in the same repository by construction, and
// the profile and budget that suited the work suit the fix. It inherits
// nothing else — no external ref (which must stay unambiguous for
// re-ingest dedupe, duplicateFeature's own rule) and no gate mode, since
// unattended is not a property to inherit into work nobody has looked at.
func (m *Shell) mintBugFromCard(parent domain.Feature, line string, landed bool) tea.Cmd {
	env := parent.Budget.Envelope
	if env <= 0 {
		env = m.envelope
	}
	desc := bugFromCardDesc(parent, line, landed)
	m.threadInput.Reset()
	m.saveThreadDraft(parent.ID)
	return func() tea.Msg {
		f, err := cardmint.Mint(context.Background(), m.store, m.ws, cardmint.Input{
			Kind:        domain.KindBug,
			Description: desc,
			Profile:     parent.Profile,
			Envelope:    env,
			Repo:        parent.Repo,
			RequireRepo: m.requireRepo,
			// FoundBy has always meant "the card that filed this one" —
			// it was written only by a goal until now, because a goal was
			// the only thing that ever filed one.
			FoundBy: parent.ID,
		})
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		// open: the follow-up is the card the reader is now working on —
		// the sentence they just typed is its whole content, and the card
		// they typed it at has ended. Leaving the cursor on the finished
		// card would answer a mint with a page that cannot act on it.
		return cardCreatedMsg{f: f, open: true}
	}
}
