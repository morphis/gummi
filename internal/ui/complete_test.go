package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/morphis/gummi/internal/ui/theme"
)

// completeRows is the fixed inventory the pure tests match against —
// deliberately not m.globalCommands(), so a command added to the menu
// later cannot silently change what these assert about the MATCHING.
func completeRows() []completionRow {
	return []completionRow{
		{name: "new", desc: "New feature", key: "n", id: "n", available: true},
		{name: "bug", desc: "New bug", key: "B", id: "B", available: true},
		{name: "research", desc: "New research card", key: "R", id: "R", available: true},
		{name: "ingest", desc: "Ingest a spec into features", key: "I", id: "I", available: true},
		{name: "import", desc: "Import bugs from GitHub", key: "G", id: "G", available: false},
		{name: "inbox", desc: "Open the needs-you inbox", key: "i", id: "i", available: true},
		{name: "sort", desc: "Sort todo by severity", key: "S", id: "S", available: true},
		{name: "quit", desc: "Quit gummi", key: "q", id: "q", available: true},
	}
}

// TestCompleteSlashSplitsTheLine pins which lines are command lines at
// all, and where the token being completed starts. The negative rows
// matter more than the positive ones: every one of them is a line the
// composer has always accepted as prose, and completion must not start
// eating any of them.
func TestCompleteSlashSplitsTheLine(t *testing.T) {
	tests := []struct {
		line   string
		head   string
		prefix string
		value  bool
		ok     bool
	}{
		{line: "/", prefix: "", ok: true},
		{line: "/in", prefix: "in", ok: true},
		{line: "/INBOX", prefix: "INBOX", ok: true},
		{line: "/agent ", head: "/agent ", prefix: "", value: true, ok: true},
		{line: "/agent cla", head: "/agent ", prefix: "cla", value: true, ok: true},
		// everything after the first space is one token: there is no
		// argument grammar here, so a value with spaces stays whole.
		{line: "/new dark mode", head: "/new ", prefix: "dark mode", value: true, ok: true},

		// not command lines
		{line: ""},
		{line: "what's stuck?"},
		{line: " /inbox"},                       // not column one
		{line: "look in internal/ui/complete."}, // a path
		{line: "ship it and/or park it"},
		{line: "see https://example.com/x"},
		{line: "/inbox\nand more"}, // a pasted block, not a typed command
	}
	for _, tc := range tests {
		head, prefix, value, ok := completeSlash(tc.line)
		if ok != tc.ok || head != tc.head || prefix != tc.prefix || value != tc.value {
			t.Errorf("completeSlash(%q) = (%q, %q, %v, %v), want (%q, %q, %v, %v)",
				tc.line, head, prefix, value, ok, tc.head, tc.prefix, tc.value, tc.ok)
		}
	}
}

// TestCompletionMatchesByPrefixOnly: the popup offers what the typed
// characters literally begin, and nothing else. "box" must not reach
// "inbox" (substring) and "ibx" must not reach it either (fuzzy) — the
// two looser rules that would let tab replace the line instead of
// extending it.
func TestCompletionMatchesByPrefixOnly(t *testing.T) {
	tests := []struct {
		prefix string
		want   []string
	}{
		{prefix: "", want: []string{"new", "bug", "research", "ingest", "import", "inbox", "sort", "quit"}},
		{prefix: "i", want: []string{"ingest", "import", "inbox"}},
		{prefix: "in", want: []string{"ingest", "inbox"}},
		{prefix: "IN", want: []string{"ingest", "inbox"}}, // case-insensitive
		{prefix: "inb", want: []string{"inbox"}},
		{prefix: "box", want: nil}, // substring, refused
		{prefix: "ibx", want: nil}, // fuzzy, refused
		{prefix: "zzz", want: nil},
	}
	for _, tc := range tests {
		c := newCompletion("", tc.prefix, false, completeRows())
		if len(tc.want) == 0 {
			if c != nil {
				t.Errorf("prefix %q: want no popup, got %d rows", tc.prefix, len(c.rows))
			}
			continue
		}
		if c == nil {
			t.Errorf("prefix %q: want %v, got no popup", tc.prefix, tc.want)
			continue
		}
		var got []string
		for _, r := range c.rows {
			got = append(got, r.name)
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("prefix %q: rows = %v, want %v", tc.prefix, got, tc.want)
		}
	}
}

// TestCompletionAcceptExtendsNeverReplaces is tab's contract: what it
// leaves in the composer always still begins with what was typed. With
// several rows left it stops at the shared prefix (and adds no space,
// because the word is not finished); with one row it finishes the word
// and adds the space that lets the value tier open behind it.
func TestCompletionAcceptExtendsNeverReplaces(t *testing.T) {
	tests := []struct {
		prefix string
		want   string
		exact  bool
	}{
		{prefix: "", want: "/", exact: false},     // nothing shared across all eight
		{prefix: "i", want: "/i", exact: false},   // ingest/import/inbox share only "i"
		{prefix: "in", want: "/in", exact: false}, // already at the shared prefix
		{prefix: "inb", want: "/inbox ", exact: true},
		{prefix: "s", want: "/sort ", exact: true},
		// the vocabulary owns its own spelling: a shouted prefix
		// completes to the canonical lower-case word.
		{prefix: "INB", want: "/inbox ", exact: true},
	}
	for _, tc := range tests {
		c := newCompletion("", tc.prefix, false, completeRows())
		if c == nil {
			t.Fatalf("prefix %q: no popup", tc.prefix)
		}
		got, exact := c.accept()
		if got != tc.want || exact != tc.exact {
			t.Errorf("prefix %q: accept = (%q, %v), want (%q, %v)", tc.prefix, got, exact, tc.want, tc.exact)
		}
		if !strings.HasPrefix(strings.ToLower(got), "/"+strings.ToLower(tc.prefix)) {
			t.Errorf("prefix %q: accept produced %q, which does not extend what was typed", tc.prefix, got)
		}
	}
}

// TestCompletionValueAcceptKeepsTheHead: completing an argument rewrites
// only the argument. The command word in front of it is head, and head
// is carried through untouched.
func TestCompletionValueAcceptKeepsTheHead(t *testing.T) {
	rows := []completionRow{
		{name: "copilot", available: true},
		{name: "claude", available: true},
		{name: "codex", available: true},
	}
	c := newCompletion("/agent ", "cl", true, rows)
	if c == nil {
		t.Fatal("no popup for /agent cl")
	}
	got, exact := c.accept()
	if got != "/agent claude" || !exact {
		t.Errorf("accept = (%q, %v), want (\"/agent claude\", true)", got, exact)
	}
	// several matches: the shared prefix only, and no trailing space —
	// a half-typed value is not a value.
	c = newCompletion("/agent ", "c", true, rows)
	if got, exact := c.accept(); got != "/agent c" || exact {
		t.Errorf("accept = (%q, %v), want (\"/agent c\", false)", got, exact)
	}
}

// TestCompletionCursorClampsAndDoesNotWrap: ↑ at the top and ↓ at the
// bottom stay put. A popup anchored to the composer with six visible
// rows is a list you feel your way down, and one that silently teleports
// to the other end is one you have to watch.
func TestCompletionCursorClampsAndDoesNotWrap(t *testing.T) {
	c := newCompletion("", "in", false, completeRows()) // ingest, inbox
	if c == nil {
		t.Fatal("no popup")
	}
	c.move(-1)
	if c.cursor != 0 {
		t.Errorf("up at the top: cursor = %d, want 0", c.cursor)
	}
	c.move(1)
	c.move(1)
	c.move(1)
	if c.cursor != 1 {
		t.Errorf("down past the bottom: cursor = %d, want 1", c.cursor)
	}
	if got, _ := c.selected(); got.name != "inbox" {
		t.Errorf("selected = %q, want inbox", got.name)
	}
}

// TestCompletionRefilterResetsTheCursor: narrowing moves the highlight
// back to the best match rather than leaving it wherever the wider set
// had put it — the cursor must never end up on a row the new prefix
// only accidentally still contains.
func TestCompletionRefilterResetsTheCursor(t *testing.T) {
	c := newCompletion("", "i", false, completeRows())
	c.move(2) // inbox
	c.refilter("in")
	if c.cursor != 0 {
		t.Errorf("cursor after refilter = %d, want 0", c.cursor)
	}
	if got, _ := c.selected(); got.name != "ingest" {
		t.Errorf("selected after refilter = %q, want ingest", got.name)
	}
}

// TestCompletionWindowFollowsTheCursor: past completeMaxRows the popup
// scrolls rather than growing, and the row under the cursor is always
// one of the rows actually drawn — acting on a selection you cannot see
// is the bug windowing exists to prevent.
func TestCompletionWindowFollowsTheCursor(t *testing.T) {
	rows := completeRows() // eight, two more than the cap
	c := newCompletion("", "", false, rows)
	for cursor := 0; cursor < len(rows); cursor++ {
		c.cursor = cursor
		first, last := completeWindow(len(c.rows), c.cursor)
		if last-first != completeMaxRows {
			t.Fatalf("cursor %d: window drew %d rows, want %d", cursor, last-first, completeMaxRows)
		}
		if cursor < first || cursor >= last {
			t.Errorf("cursor %d fell outside the drawn window [%d,%d)", cursor, first, last)
		}
	}
}

// TestCompletionViewCountsWhatItCannotDraw: the tail says how many rows
// are left instead of silently dropping them.
func TestCompletionViewCountsWhatItCannotDraw(t *testing.T) {
	c := newCompletion("", "", false, completeRows())
	lines := c.view(theme.New(theme.GummiDark()), 60)
	if len(lines) != completeMaxRows+1 {
		t.Fatalf("view drew %d lines, want %d rows plus a count", len(lines), completeMaxRows)
	}
	if tail := ansi.Strip(lines[len(lines)-1]); !strings.Contains(tail, "…2 more") {
		t.Errorf("tail = %q, want it to count the 2 undrawn rows", tail)
	}
}

// TestCompletionPopupGolden pins the popup's whole look: the accent band
// on the selected row, the dim descriptions, the right-aligned
// accelerators, and the dimmed row for a command that is visible but not
// available.
func TestCompletionPopupGolden(t *testing.T) {
	c := newCompletion("", "", false, completeRows())
	c.move(2) // land the band away from the first row
	out := strings.Join(c.view(theme.New(theme.GummiDark()), 64), "\n")
	golden.RequireEqual(t, []byte(out))
}

// TestCompletionViewNeverExceedsItsPane: the popup is clipped by the
// caller to the pane width, so a block wider than the pane does not
// overflow — it loses the ends of whole rows. columns() used to return a
// floor width (marker + name + key) that w could not push below, so on a
// narrow terminal it handed back rows wider than the space they had.
func TestCompletionViewNeverExceedsItsPane(t *testing.T) {
	s := theme.New(theme.GummiDark())
	for _, w := range []int{80, 40, 20, 14, 12, 8, 1} {
		c := newCompletion("", "", false, completeRows())
		for _, line := range c.view(s, w) {
			if got := ansi.StringWidth(line); got > w {
				t.Errorf("w=%d: row %q is %d wide", w, ansi.Strip(line), got)
			}
		}
	}
}

// TestCompletionViewSurvivesZeroWidth: a pane can momentarily measure
// zero during a resize, and a popup is rendered on that frame like
// anything else. It must not panic (strings.Repeat with a negative count
// does) whatever the arithmetic works out to.
func TestCompletionViewSurvivesZeroWidth(t *testing.T) {
	c := newCompletion("", "", false, completeRows())
	c.view(theme.New(theme.GummiDark()), 0)
}
