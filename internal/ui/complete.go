package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/ui/theme"
)

// Inline slash completion for a thread composer: the popup that opens
// above the line the moment a "/" is typed into an empty one, narrows as
// the word is typed, and hands the composer back a completed word on tab.
//
// It is deliberately a separate model from commandMenu (commandmenu.go),
// which is the same inventory shown as a modal dialog. The two answer
// different questions: the menu is for browsing ("what can I do here?")
// and owns the keyboard while it is up; this is for recall ("I know the
// word, finish it for me") and never takes the keyboard at all — every
// key still goes to the composer, and the popup is a hint painted over
// the rows above it. That is why this type has no Dialog methods, is
// never pushed onto the Overlay, and cannot be "in" a mode: it exists
// exactly as long as the line under it still looks like a command.
//
// It is also pure. Nothing here reads a Shell, an engine or a board
// session — the caller supplies the rows and acts on the chosen id — so
// the matching, the cursor clamping and the tab-completion arithmetic
// are all table-tested (complete_test.go) with no TUI anywhere in sight.

// completeMaxRows caps how many rows the popup draws before it stops
// listing and starts counting. Six is the same budget the card page's
// action list settles on for the same reason (cardactions.go's maxRows):
// the popup grows UP from the composer, straight over the conversation
// it is being typed into, and a list that can cover the last thing the
// board said is a list that has taken the screen for itself. Past six,
// "…N more — keep typing" is both shorter and better advice than a
// scrollbar.
const completeMaxRows = 6

// completeMinNameWidth is the narrowest the name column is ever squeezed
// to. Below this a row stops being a completion at all — you cannot tell
// which word you are about to accept — so on a pane too narrow even for
// that, the block is allowed to be the one thing that overflows and gets
// clipped, rather than every row rendering as an indistinguishable stub.
const completeMinNameWidth = 4

// completionRow is one offered completion.
//
// name is the bare word — "inbox", "claude" — without the leading "/",
// which belongs to the line's syntax rather than to any row: a value
// row ("/profile thrifty") has no slash of its own, and rendering one
// with a slash the composer would not accept is how a popup teaches the
// wrong thing.
type completionRow struct {
	name string
	desc string // what it does, or what it resolves to; the dim right-hand text
	key  string // the accelerator that already runs it, right-aligned; "" when none
	id   string // what the caller switches on to run it — never shown
	// available false renders the row dimmed and refuses to run it,
	// exactly as commandMenu does: a backend that is not on PATH is worth
	// SEEING (it explains why your `agent:` line does nothing) and worth
	// refusing (spawning it fails later and less clearly).
	available bool
	// needsValue marks a COMMAND-tier row whose command does nothing
	// without an argument (board-profile, board-model — see
	// boardCommandRows, the only place that sets it). This file stays
	// ignorant of which command that is; it only has to know that enter
	// on such a row (handleBoardCompletionKey) has nothing to run and so
	// completes the word and opens the value tier instead, the same as
	// tab already does — running the command anyway would mean offering
	// a key and then refusing what it does.
	needsValue bool
}

// completion is the popup's whole state: the rows currently offered, the
// cursor, and the two halves of the line that produced them.
//
// head is the text before the token being completed — "" while the
// command word itself is being typed, "/profile " once it is complete
// and its value is. prefix is the token typed so far, matched against
// every row's name. head+"/"+prefix (command tier) or head+prefix (value
// tier) always reconstructs exactly what is in the composer, which is
// what lets accept() rewrite the line without the caller re-deriving it.
type completion struct {
	all    []completionRow // the unfiltered source, in its own order
	rows   []completionRow // all, filtered by prefix, in the same order
	cursor int             // index into rows
	head   string
	prefix string
	// value is false for the command tier (the word after "/") and true
	// for the argument tier (the word after a complete command). Only
	// the renderer and accept() care: a command completes to "/name ",
	// a value to "name".
	value bool
}

// completionPrefix reports whether row name n is completed by the typed
// prefix p. Prefix, case-insensitively, and nothing else.
//
// Not substring, and not fuzzy, on purpose. The closed verb vocabulary
// this sits beside is matched by exact first word and is "never
// fuzzy-matched" (verbs.go), and a completion popup that accepted a
// looser rule than the parser behind it would offer rows that typing one
// more character makes vanish. Prefix also keeps one promise the other
// two cannot: what you typed is always a literal head of what you get,
// so tab can extend the line rather than replace it.
func completionPrefix(n, p string) bool {
	return strings.HasPrefix(strings.ToLower(n), strings.ToLower(p))
}

// newCompletion builds a popup over all, filtered by prefix, cursor on
// the first match. It returns nil when nothing matches — "no popup" and
// "an empty popup" are the same state, and representing it as nil means
// every caller's `if c != nil` is also the "is anything offered?" test.
func newCompletion(head, prefix string, value bool, all []completionRow) *completion {
	c := &completion{all: all, head: head, prefix: prefix, value: value}
	c.refilter(prefix)
	if len(c.rows) == 0 {
		return nil
	}
	return c
}

// refilter re-matches the rows against a new prefix and reclamps the
// cursor. The cursor goes back to the top rather than trying to follow
// the row it was on: typing narrows toward what you want, so the best
// match after a keystroke is the first one, not wherever the previous
// set left the highlight.
func (c *completion) refilter(prefix string) {
	c.prefix = prefix
	c.rows = c.rows[:0]
	for _, r := range c.all {
		if completionPrefix(r.name, prefix) {
			c.rows = append(c.rows, r)
		}
	}
	c.cursor = 0
}

// move steps the cursor by delta and stops at both ends. It does not
// wrap: the popup is short, anchored to the composer, and a cursor that
// silently jumps from the last row to the first is a cursor you have to
// watch instead of a list you can feel your way down.
func (c *completion) move(delta int) {
	c.cursor = min(max(c.cursor+delta, 0), max(len(c.rows)-1, 0))
}

// selected returns the row under the cursor.
func (c *completion) selected() (completionRow, bool) {
	if c.cursor < 0 || c.cursor >= len(c.rows) {
		return completionRow{}, false
	}
	return c.rows[c.cursor], true
}

// commonPrefix is the longest prefix every matching row shares — what
// tab extends the line to when several rows are still in play.
//
// Case is taken from the first row rather than from what was typed, so
// completing "/IN" yields "/inbox", the name as the inventory spells it:
// the vocabulary owns its own spelling, and a line that has to survive
// being re-parsed later should carry the canonical form.
func (c *completion) commonPrefix() string {
	if len(c.rows) == 0 {
		return c.prefix
	}
	out := c.rows[0].name
	for _, r := range c.rows[1:] {
		for !completionPrefix(r.name, out) {
			out = out[:len(out)-1]
		}
	}
	return out
}

// accept returns the composer line tab should leave behind: the head, the
// selected word (or the common prefix when several rows remain), and —
// for a completed command — a trailing space, which is both the thing
// that lets the value tier open behind it and the honest signal that
// enter has not run anything yet.
//
// exact reports whether the line now names one whole row, which is what
// the caller uses to decide whether the popup has more to say.
func (c *completion) accept() (line string, exact bool) {
	word := c.commonPrefix()
	if len(c.rows) == 1 {
		word = c.rows[0].name
	}
	if c.value {
		return c.head + word, len(c.rows) == 1
	}
	if len(c.rows) == 1 {
		return c.head + "/" + word + " ", true
	}
	return c.head + "/" + word, false
}

// acceptRow is accept() for a caller that has already CHOSEN one row,
// rather than one asking how far every match agrees.
//
// The two are not the same question, and answering enter with accept()
// was a real bug: accept() stops at commonPrefix unless exactly one row
// is left, because that is what tab means — extend the line as far as
// nothing is being decided for you. enter means "this row, the one under
// the cursor". Type "/", arrow down to a row and press enter with a
// dozen commands still matching, and accept() rewrites the line to the
// shared prefix of all twelve — "/" — so the highlighted row appears to
// do nothing at all.
//
// The trailing space on the command tier is the same one accept() adds
// for the same reason: it is what opens the value tier behind a command
// that takes one.
func (c *completion) acceptRow(r completionRow) string {
	if c.value {
		return c.head + r.name
	}
	return c.head + "/" + r.name + " "
}

// completeSlash splits a composer line into the head, the token being
// completed, and which tier that token belongs to. ok is false for every
// line that is not a command line at all.
//
// The rules, and why:
//
//   - the line must start with "/" — in column one, no leading space. A
//     slash anywhere else is a slash: file paths, dates, "and/or" and
//     "http://…" all have to keep typing as themselves, and the only
//     unambiguous place to spend the character is the position where a
//     message would not begin with it.
//   - "/word" (no space yet) is the COMMAND tier: head "", prefix "word".
//   - "/word " and beyond is the VALUE tier: head "/word ", prefix
//     whatever follows. Everything after the first space is one opaque
//     token — a value with spaces in it completes against its whole
//     remainder, because gummi has no argument grammar here and inventing
//     one would mean the popup and the command disagreeing about where an
//     argument ended.
//   - a line with a newline in it is not a command line. The composer
//     wraps but never holds a newline (enter sends), so this only fires
//     on a paste — and a pasted block whose first character happens to be
//     "/" is text, not a command someone is typing.
func completeSlash(line string) (head, prefix string, value, ok bool) {
	if !strings.HasPrefix(line, "/") || strings.ContainsRune(line, '\n') {
		return "", "", false, false
	}
	rest := line[1:]
	sp := strings.IndexRune(rest, ' ')
	if sp < 0 {
		return "", rest, false, true
	}
	return line[:sp+2], rest[sp+1:], true, true
}

// view renders the popup's rows as lines, to be laid above the composer
// by the caller. It returns nothing when there is nothing to offer, so
// the caller never has to special-case an empty block.
//
// The selected row takes the bright accent band, the same cursor
// commandMenu paints for the same inventory (commandmenu.go's renderRow):
// one selection idiom for one list, wherever you meet it. The typed
// prefix is NOT separately highlighted inside the name — the band already
// says which row, the line above already shows what was typed, and a
// third mark competing with both is noise.
func (c *completion) view(s *theme.Styles, w int) []string {
	if c == nil || len(c.rows) == 0 {
		return nil
	}
	nameW, width := c.columns(w)

	first, last := completeWindow(len(c.rows), c.cursor)
	out := make([]string, 0, last-first+1)
	for i := first; i < last; i++ {
		out = append(out, c.renderRow(s, c.rows[i], i == c.cursor, nameW, width))
	}
	if more := len(c.rows) - (last - first); more > 0 {
		// Clipped like every other row: this line is the longest thing the
		// popup draws and, unlike the rows, has no column arithmetic
		// holding it in — left alone it was the one thing still hanging
		// off the end of a narrow pane.
		out = append(out, s.Faint.Render(ansi.Truncate(fmt.Sprintf("  …%d more — keep typing", more), width, "")))
	}
	return out
}

// completeWindow is windowLines' arithmetic (ingestview.go) over indices
// rather than over rendered strings, so the band can be painted onto the
// row the cursor is really on instead of being matched back by value —
// two rows may legitimately carry the same text (a profile and a model
// of the same name), and identity is what the cursor means.
func completeWindow(n, cursor int) (first, last int) {
	if n <= completeMaxRows {
		return 0, n
	}
	first = cursor - (completeMaxRows-1)/2
	first = min(max(first, 0), n-completeMaxRows)
	return first, first + completeMaxRows
}

// rowLabel is the row's left-hand text: the command tier spells its own
// slash back at you (what you would type), the value tier does not.
func (c *completion) rowLabel(r completionRow) string {
	if c.value {
		return r.name
	}
	return "/" + r.name
}

// columns sizes the popup: nameW is the column every description starts
// after, width is the whole block. Both come from the popup's own
// content rather than from the pane it sits in — keyColumn's reasoning
// (cardactions.go), which this deliberately does not reuse: that helper
// sizes a two-column label/key list, and a row here has three parts, so
// borrowing it left every description truncated to "New…" against a
// 14-column block in a 100-column pane.
//
// The description column is what the popup exists for on a first read
// ("/ingest" alone does not say what ingesting is), so it gets whatever
// the name and key columns leave and is the only part that gives ground
// when the pane is narrow — a name that truncates is a name you cannot
// finish typing, and a key that truncates is a lie.
func (c *completion) columns(w int) (nameW, width int) {
	descW, keyW := 0, 0
	for _, r := range c.rows {
		nameW = max(nameW, ansi.StringWidth(c.rowLabel(r)))
		descW = max(descW, ansi.StringWidth(r.desc))
		keyW = max(keyW, ansi.StringWidth(r.key))
	}
	// The name column gives way before the block does. A popup wider than
	// the pane it sits in is not a popup with a wide name column, it is a
	// popup the pane truncates mid-row, so on a terminal too narrow for
	// even the names the names are what shrink — the alternative, a floor
	// the width could never fall below, returned a block wider than w and
	// left the caller's clip to hack the ends off whole rows.
	nameW = min(nameW, max(w-markerWidth-keyGap-keyW, completeMinNameWidth))
	full := markerWidth + nameW + keyGap + descW + keyGap + keyW
	return nameW, max(min(full, w), 0)
}

// renderRow paints one row: marker, name, dim description aligned into
// its own column, right-aligned key. Its shape is commandMenu.renderRow's,
// minus the dialog frame that one sits in and plus the description.
func (c *completion) renderRow(s *theme.Styles, r completionRow, sel bool, nameW, width int) string {
	marker := "  "
	name, desc, key := s.Base, s.Faint, s.Faint
	if !r.available {
		name = s.Faint
	}
	if sel {
		marker = s.BandMarker(true)
		name, desc, key = s.BandText, s.BandTextDim, s.BandTextDim
		if !r.available {
			// the band swallows Faint; an unavailable row still has to read
			// one tier down from an available one (commandMenu's own note).
			name = s.Muted
		}
	}
	// Truncated to the name column, which columns() may have squeezed
	// below the widest name on a narrow pane — rendering the full label
	// against a clamped column is how the block ended up wider than the
	// width that column was derived from.
	label := ansi.Truncate(c.rowLabel(r), nameW, "…")
	// The name column is padded before it is styled, so the padding is
	// plain spaces rather than a run of styled background that would show
	// as a stripe on the banded row.
	left := marker + name.Render(label) + strings.Repeat(" ", max(nameW-ansi.StringWidth(label), 0))

	right := ""
	if r.key != "" {
		right = key.Render(r.key)
	}
	// What is left after the name column and the key is the description's,
	// truncated to fit rather than wrapped: a popup row is one line.
	room := width - ansi.StringWidth(marker) - nameW - keyGap - ansi.StringWidth(right)
	mid := ""
	if r.desc != "" && room > 0 {
		mid = desc.Render(ansi.Truncate(r.desc, room, "…"))
	}
	row := left + strings.Repeat(" ", keyGap) + mid
	pad := width - ansi.StringWidth(row) - ansi.StringWidth(right)
	row += strings.Repeat(" ", max(pad, 0)) + right
	// The last word on the width, after every column has had its say. The
	// marker, the minimum name column and the key have their own floors
	// and can still add up past width on a pane narrower than all of them
	// put together; on a terminal that small something has to give, and
	// losing the end of a row is better than drawing past the edge of the
	// pane the caller measured.
	row = ansi.Truncate(row, width, "")
	if sel {
		return s.Band(row, width, true)
	}
	return row
}
