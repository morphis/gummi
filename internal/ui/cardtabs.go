package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
)

// Thread, artifact and diff are VIEWS, not actions.
//
// They used to be rows in the card's option list, ranked among "approve"
// and "land on main" as though reading the thing were one of the answers
// to what should happen to it. It never is: reading is what you do
// *before* answering, and a reading surface offered as an option is a
// row the reader has to rule out every visit. As tabs they are always
// one keystroke away and never compete with the decision.
//
// The tab bar switches which full surface is MOUNTED rather than
// embedding panes in the card page. specview.go and diffview.go are
// already whole surfaces with their own keymaps, annotate modes and
// scroll state, mounted over the board tab by mainView's own precedence
// (shell.go); re-homing them as panes would mean rebuilding both and
// re-deriving the 36x9 layout contract, for a result a reader could not
// tell apart. So the bar is chrome drawn above whichever of the three is
// mounted, and switching is mount/unmount.
//
// The keys are alt chords because the card page's composer owns every
// printable key — the same reason alt+o toggles tool output and alt+j/k
// step cards. alt+s was already the artifact's chord (it opened the
// document the pinned spec line names); it keeps that job and gains a
// bar that says so.
type cardTab string

const (
	cardTabThread   cardTab = "thread"
	cardTabArtifact cardTab = "artifact"
	cardTabDiff     cardTab = "diff"
)

// cardTabKeys maps each tab to the chord that selects it.
var cardTabKeys = map[cardTab]string{
	cardTabThread:   "alt+t",
	cardTabArtifact: "alt+s",
	cardTabDiff:     "alt+d",
}

// activeCardTab reports which of the three is on screen, read from the
// surfaces themselves rather than from a stored mode — mainView paints
// by that same precedence, so a remembered tab could disagree with what
// the reader is looking at.
func (m *Shell) activeCardTab() cardTab {
	switch {
	case m.spec != nil:
		return cardTabArtifact
	case m.diff != nil:
		return cardTabDiff
	}
	return cardTabThread
}

// cardHasDiff reports whether the card has a diff to show at all.
// Research cards carry no branch and never get a worktree (AGENTS.md's
// worktree note), and a card still in todo has nothing on one — the same
// test cardActionsFor makes for its own diff row, so the tab bar and the
// action inventory cannot disagree about whether the surface exists.
func cardHasDiff(r featureRow) bool {
	return r.F.Kind != domain.KindResearch && r.F.Stage != domain.StageTodo
}

// cardTabBar renders the bar. The active tab is titled, the others are
// muted and wear their chord, so the row teaches the keys instead of
// requiring them known — the same label-first, key-demoted shape the
// action list uses.
func (m *Shell) cardTabBar(active cardTab, w int) string {
	r, ok := m.selected()
	if !ok {
		return ""
	}
	s := m.styles
	tabs := []cardTab{cardTabThread, cardTabArtifact}
	if cardHasDiff(r) {
		tabs = append(tabs, cardTabDiff)
	}
	out := " "
	for i, t := range tabs {
		if i > 0 {
			out += s.Faint.Render("  ·  ")
		}
		name := string(t)
		if t == cardTabArtifact {
			// the label is the interface, so it takes the card's own noun
			// for its document — a bug card's page calling it a "spec"
			// names the document by a word appearing nowhere else on it.
			name = artifactNoun(r.F.Kind)
		}
		if t == active {
			out += s.PaneTitleActive.Render(name)
			continue
		}
		out += s.Muted.Render(name) + " " + s.KeyHint.Render(cardTabKeys[t])
	}
	return ansi.Truncate(out, w, "…")
}

// cardSurface draws the bar above whichever surface is mounted, giving
// the surface the rows the bar did not take.
func (m *Shell) cardSurface(active cardTab, w, h int, render func(int, int) string) string {
	bar := m.cardTabBar(active, w)
	if bar == "" || h <= 1 {
		return render(w, h)
	}
	return bar + "\n" + render(w, h-1)
}

// cardTabKey answers the three chords, from whichever of the three
// surfaces is mounted — one handler rather than the same switch repeated
// in handleSpecKey, handleDiffKey and handleThreadInputKey, so a chord
// can never work on two of them and not the third.
//
// It is only live while a card page is open. Opening the artifact from
// the backlog list mounts the same surface with no page underneath it to
// go back to, so there is no bar there and no tab to switch.
func (m *Shell) cardTabKey(key string) (tea.Cmd, bool) {
	r, ok := m.selected()
	if !ok {
		return nil, false
	}
	tab, ok := cardTabFor(key)
	if !ok {
		return nil, false
	}
	if tab == m.activeCardTab() {
		// already here. Answered rather than passed on, so the chord never
		// falls through to the mounted surface and types (or scrolls)
		// instead of being the no-op it looks like.
		return nil, true
	}
	switch tab {
	case cardTabThread:
		m.spec, m.diff = nil, nil
		return nil, true
	case cardTabArtifact:
		m.diff = nil
		return m.openSpec(r.F), true
	case cardTabDiff:
		if !cardHasDiff(r) {
			// the bar does not draw the tab for such a card, so the chord
			// says why rather than doing nothing — a key the help table
			// lists owes an answer.
			m.notice = noticeMsg{text: string(r.F.ID) + ": no diff — " + noDiffReason(r)}
			return nil, true
		}
		m.spec = nil
		return m.openDiff(r.F), true
	}
	return nil, false
}

// cardCitationKey answers alt+1..alt+9 — the citation chords.
//
// It lives beside cardTabKey and is called from the same places for the
// same reason: the chords must work from all three surfaces, or a
// citation opened from the thread would be unopenable from the artifact
// it just landed on. The digit is the number printed in the narration,
// so a card with fewer citations than that simply does not answer —
// silently, because an unmarked number is not a key the reader was
// offered.
func (m *Shell) cardCitationKey(key string) (tea.Cmd, bool) {
	n, ok := citationChord(key)
	if !ok {
		return nil, false
	}
	r, ok := m.selected()
	if !ok {
		return nil, false
	}
	// Events is populated for the selected card only, lazily, and on a
	// COPY of the row — threadView does it at thread.go's render and
	// nothing writes it back to m.rows. So a row straight out of
	// m.selected() carries an empty Events slice, and every event citation
	// resolved against nothing: scrollThreadToEvent walked an empty list,
	// found no stretch opening at the cited seq and answered "nothing on
	// this page opens at that citation" — including when the status bar
	// itself was advertising "alt+a open cited" (round 3 §2.2).
	//
	// The mark is generated from m.cardEvents (narration.go's
	// unattendedClaim reads the cache directly), so resolving against the
	// same cache is what makes the printed mark and the key agree —
	// narration.go's invariant 3, which was true of the generator and
	// false of the opener.
	r.Events = m.cardEvents[r.F.ID]
	cmd := m.openCitation(r, n)
	// Answered either way: the chord belongs to this tier, and letting an
	// unmatched one fall through would type an alt-digit into the
	// composer instead of doing nothing.
	return cmd, true
}

// reservedChords are the card page's alt letters that mean something
// else: the three tabs, the card step, and the tool-output toggle. A
// citation must not be able to name one of them.
//
// The tab tier is answered before the citation tier, so an overlap would
// today be harmless — the tab would simply win. That is exactly why it
// is excluded here instead: a collision saved only by the ORDER of two
// handlers is a bug waiting for someone to reorder them, and it would
// present as a mark the reader can see and cannot open.
const reservedChords = "tsdjko"

// citationLetters are the marks, in order: the first nine letters that
// are not reserved.
//
// LETTERS, not digits, and not alt+digits either — alt+1/2/3 are the
// shell's own board/inbox/agent tabs and are answered above this tier,
// so a numbered chord would have switched tab instead of opening a
// citation. Footnote letters are the convention anyway, and this leaves
// the whole digit row to the picker (F14).
var citationLetters = firstFreeLetters(9)

// firstFreeLetters takes n letters from the alphabet, skipping the
// reserved chords.
func firstFreeLetters(n int) string {
	var out []byte
	for c := byte('a'); c <= 'z' && len(out) < n; c++ {
		if strings.IndexByte(reservedChords, c) < 0 {
			out = append(out, c)
		}
	}
	return string(out)
}

// citationMark is the mark printed beside claim n (1-based), including
// the key that opens it.
func citationMark(n int) string {
	if n < 1 || n > len(citationLetters) {
		return ""
	}
	return "alt+" + string(citationLetters[n-1])
}

// citationRange spells the chord range for n citations — "a" for one,
// "a/b" for two, "a-d" beyond that — so the bar names keys that exist
// rather than a range with dead letters in it.
func citationRange(n int) string {
	n = min(n, len(citationLetters))
	switch {
	case n <= 1:
		return "a"
	case n == 2:
		return "a/b"
	default:
		return "a-" + string(citationLetters[n-1])
	}
}

// citationChord reads alt+<letter> into the 1-based citation number.
func citationChord(key string) (int, bool) {
	rest, ok := strings.CutPrefix(key, "alt+")
	if !ok || len(rest) != 1 {
		return 0, false
	}
	if i := strings.IndexByte(citationLetters, rest[0]); i >= 0 {
		return i + 1, true
	}
	return 0, false
}

// cardTabFor resolves a chord to its tab.
func cardTabFor(key string) (cardTab, bool) {
	for t, k := range cardTabKeys {
		if k == key {
			return t, true
		}
	}
	return "", false
}

// noDiffReason names which of the two reasons a card has no diff, so the
// refusal is a fact about this card rather than a generic apology.
func noDiffReason(r featureRow) string {
	if r.F.Kind == domain.KindResearch {
		return "a research card carries no branch"
	}
	return "nothing has run on it yet"
}

// cardTabBindings are the rows every card surface's key table carries,
// so the ? overlay names the way between the three from all three. The
// diff row is dropped on a card that has no diff, the same filter the
// bar itself applies — a help table that lists a key which only ever
// refuses is the drift keymap.go's research-card filter already exists
// to prevent.
func (m *Shell) cardTabBindings() []binding {
	var bs []binding
	r, haveCard := m.selected()
	// The citation chords lead the tier, and the order is the point: the
	// bar sheds hints from the second-to-last backwards, so whatever is
	// earliest here outlives the rest of the tier. alt+t/s/d are already
	// printed on screen by the tab bar itself, and this chord is printed
	// nowhere but the marks it opens — so it is the one of the four
	// worth keeping when the bar runs out of room.
	//
	// Listed only when the narration actually prints marks: a key table
	// naming alt+1 above a paragraph with no [alt+1] in it is the same
	// drift the diff row's own filter exists to prevent.
	if haveCard {
		if n := len(citedClaims(m.cardNarration(m.nextInputFor(r), r))); n > 0 {
			bs = append(bs, binding{
				key: "alt+" + citationRange(n), label: "open cited",
				help: "open what a numbered claim above cites — the check, the hunk, the section, or the moment in the thread",
				bar:  true,
			})
		}
	}
	bs = append(bs,
		binding{key: "alt+t", label: "thread", help: "the card's conversation — where you answer the decision"},
		binding{key: "alt+s", label: "artifact", help: "the document the stage wrote — comment, resolve and approve in place"},
	)
	if haveCard {
		bs[len(bs)-1].help = "the " + artifactNoun(r.F.Kind) + " — comment, resolve and approve in place"
		if !cardHasDiff(r) {
			return bs
		}
	}
	return append(bs, binding{key: "alt+d", label: "diff", help: "the card's diff — comment, resolve and approve in place"})
}

// withCardTabs splices the tab rows into a card surface's key table just
// above its last row.
//
// Last, by the convention every one of those tables already follows, is
// the way out — and it has to stay there: the status bar sheds hints
// from the second-to-last backwards precisely so a surface's escape
// hatch outlives every other row (statusbar.Render). Appending the tabs
// would bury it; prepending them would spend the bar's first, widest
// slots on navigation instead of on what enter does.
func (m *Shell) withCardTabs(bs []binding) []binding {
	tabs := m.cardTabBindings()
	if len(bs) == 0 {
		return tabs
	}
	out := make([]binding, 0, len(bs)+len(tabs))
	out = append(out, bs[:len(bs)-1]...)
	out = append(out, tabs...)
	return append(out, bs[len(bs)-1])
}

// withCardTabsIf is withCardTabs applied conditionally, for the surfaces
// that are a card tab only when a card page is open underneath them.
func (m *Shell) withCardTabsIf(when bool, bs []binding) []binding {
	if !when {
		return bs
	}
	return m.withCardTabs(bs)
}
