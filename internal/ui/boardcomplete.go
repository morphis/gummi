package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/engine"
)

// The board thread's half of slash completion: where the rows come from,
// when the popup exists, and what running one does. complete.go owns the
// popup itself and knows none of this.
//
// The board composer had no "/" vocabulary at all before this
// (handleBoardInputKey's own doc comment: every turn is a message, full
// stop). That reasoning was about the CARD verbs — approve, verify, land
// — and it still holds: those act on a selected card, and a board
// conversation has none. What it never covered is the board-level
// inventory the space menu has carried all along (globalCommands): "new
// feature", "the inbox", "import bugs from GitHub" are workspace
// actions, they belong to this tab as much as to the dashboard, and the
// only way to reach them from here was to leave the tab. So the command
// tier here has two sources: the space menu's named entries — one
// inventory, two presentations, the same rule submitThreadInput settled
// on for the card thread after the two-vocabularies bug — plus the one
// conversation-scoped command this tab answers itself: /clear acts on
// the conversation the composer stands in, not on the workspace, so the
// menu (openable from every tab) never learns it (boardCommandRows).
//
// Anything that is not a command line stays a message, unchanged. A "/"
// mid-sentence, a pasted path, a word that matches no command: all prose,
// all sent, because that is what this tab promised and completion is not
// a reason to start refusing lines it used to accept.

// boardCommandRows is the command tier's source, in two parts: the
// board-level entries of the space menu that have a slash name, plus the
// tab's own conversation-scoped /clear appended after them.
//
// The appended row is a deliberate exception to "the popup's rows are
// the menu's rows", made here where the rows are built so it reads as a
// decision rather than an oversight: /clear acts on the conversation the
// composer is standing in — the same category as esc's interrupt — while
// everything above it acts on the workspace. The space menu is openable
// from every tab, so a row there would offer clearing a conversation it
// cannot show; it never learns this one, exactly as the split below
// keeps the card-scoped verbs out of here.
//
// The name filter is what keeps the card-scoped entries out.
// globalCommands appends the selected card's whole action inventory when
// a card page is open (cardCommands), and a card page can perfectly well
// be open on the BOARD tab while the user is typing here — so without
// this, "/park" typed at the board would offer to park whichever card
// happens to be selected on a tab that isn't even visible. Those entries
// carry no name (boardactions.go), so they are simply not in this
// vocabulary, and there is no second list to keep in step with the first.
func (m *Shell) boardCommandRows() []completionRow {
	var out []completionRow
	for _, c := range m.globalCommands() {
		if c.name == "" {
			continue
		}
		out = append(out, completionRow{
			name:       c.name,
			desc:       c.label,
			key:        c.key,
			id:         c.id,
			available:  c.available,
			needsValue: boardCommandNeedsValue(c.id),
		})
	}
	// The conversation-scoped command, last: the workspace rows keep
	// their order and their places, and this one reads after them — the
	// same tier the buried profile and model rows already occupy, with
	// prefix narrowing ("c", "cl") doing the surfacing. Clearing is
	// always safe to offer: it answers a missing session and an
	// opening-in-flight itself (clearBoardConversation), so available is
	// unconditional, and it takes no argument, so needsValue stays false
	// and no value tier ever opens behind it.
	out = append(out, completionRow{
		name:      "clear",
		desc:      "start a fresh conversation",
		id:        "board-clear",
		available: true,
	})
	return out
}

// boardCommandNeedsValue reports whether id names a command that has
// nothing to do without an argument. It is the one place that mapping
// lives — complete.go's completionRow carries the resulting bool but
// stays ignorant of which commands earn it, so a third command that
// needs a value later is one line here, not a new hardcoded name check
// spread across a file that otherwise knows nothing about gummi's own
// vocabulary.
func boardCommandNeedsValue(id string) bool {
	switch id {
	case "board-profile", "board-model":
		return true
	default:
		return false
	}
}

// boardValueRows is the argument tier's source: the values a completed
// command takes, and whether it takes any at all.
//
// Three commands have one. "/agent" and "/profile" both enumerate a
// CLOSED set — agentcli.Detect probes PATH for the five known CLIs,
// engine.BoardProfiles reads profiles.yaml — so the popup can offer the
// real names instead of asking for a string. "/model" is different: a
// model is an opaque per-role string (config.RoleConfig.Model, forwarded
// verbatim to whichever backend runs) with no registry behind it, so its
// rows are only ever the models this workspace has already asked a
// backend to run (engine.KnownModels) — a memory aid, not the closed set
// the other two offer, which is why runBoardModelCommand accepts any
// non-empty string rather than checking it against these rows the way
// runBoardProfileCommand does.
//
// Every other command is left out on purpose rather than by omission:
// none of them read an argument at all (runBoardCommand's own doc
// comment), so a value list for one would be a picker with nothing
// behind it.
func (m *Shell) boardValueRows(cmd string) ([]completionRow, bool) {
	switch cmd {
	case "profile":
		return m.boardProfileValueRows(), true
	case "model":
		return m.boardModelValueRows(), true
	}
	return nil, false
}

// labelBackendModel words an empty Backend or Model coming back from the
// engine as the thing it falls back to, never left blank — a blank field
// reads as missing data, not as "the default". Shared by the board's own
// /profile picker (boardProfileValueRows) and the card-scoped one
// (openCardProfilePicker, cardprofile.go) so their wording can't drift.
func labelBackendModel(backend, model string) (string, string) {
	if backend == "" {
		backend = "engine default"
	}
	if model == "" {
		model = "backend default"
	}
	return backend, model
}

// boardProfileValueRows lists every declared profile for the /profile
// picker. Each row's description names what the profile actually
// resolves to rather than the raw yaml (engine.BoardProfile's own doc
// comment on why: a profile that never declared a board role at all
// borrows the architect's, so the picker has to show what would really
// run, not what the "board:" key literally says). An empty Backend or
// Model coming back from the engine is worded as the thing it falls
// back to, never left blank — a blank field reads as missing data, not
// as "the default", and this is the one layer allowed to make that a UI
// judgment (BoardProfiles' own doc comment declines to).
//
// Nil-safe: no engine (or no profiles.yaml, folded into an empty
// BoardProfiles) means nothing to offer, but /profile still exists as a
// command — it just has no values right now, the same distinction
// boardValueRows' bool return exists to make.
func (m *Shell) boardProfileValueRows() []completionRow {
	if m.engine == nil {
		return nil
	}
	var out []completionRow
	for _, p := range m.engine.BoardProfiles() {
		backend, model := labelBackendModel(p.Backend, p.Model)
		desc := backend + " · " + model
		if m.board != nil && p.Name == m.board.Profile() {
			desc += " · current"
		}
		out = append(out, completionRow{
			name:      p.Name,
			desc:      desc,
			id:        "board-profile:" + p.Name,
			available: true,
		})
	}
	return out
}

// boardModelValueRows lists every model harvested from profiles.yaml for
// the /model picker (engine.KnownModels' own doc comment on why there is
// no wider registry behind it), each row's description naming which
// profile·role pairings asked for it. The one actually driving the live
// board session — read the same way boardHeader reads it, via runModel
// over the session's own snapshot, never re-derived — is marked current.
//
// Nil-safe for the identical reason boardProfileValueRows is.
func (m *Shell) boardModelValueRows() []completionRow {
	if m.engine == nil {
		return nil
	}
	var running string
	if m.board != nil {
		running = runModel(m.board.Snapshot())
	}
	var out []completionRow
	for _, mm := range m.engine.KnownModels() {
		desc := strings.Join(mm.Uses, ", ")
		if mm.Model == running {
			if desc != "" {
				desc += " · current"
			} else {
				desc = "current"
			}
		}
		out = append(out, completionRow{
			name:      mm.Model,
			desc:      desc,
			id:        "board-model:" + mm.Model,
			available: true,
		})
	}
	return out
}

// syncBoardCompletion rebuilds the popup from whatever is in the composer
// right now, and is called after every key that can change that text.
//
// Rebuilding beats mutating. The composer is a real textarea: ctrl+u,
// ctrl+w, a paste, a cursor move and a backspace can all change the line
// without the popup being told which of them happened, and a popup kept
// in step by hand would eventually stand over a line it no longer
// describes. Deriving it fresh means there is one rule — "the line
// starts with /, and something matches" — and the popup's existence is
// that rule's answer rather than a piece of state that can disagree with
// it.
func (m *Shell) syncBoardCompletion() {
	m.boardComplete = nil
	head, prefix, value, ok := completeSlash(m.boardInput.Value())
	if !ok {
		return
	}
	if !value {
		m.boardComplete = newCompletion(head, prefix, false, m.boardCommandRows())
		return
	}
	// The value tier only opens behind a command that actually takes
	// values, so "/new some title" keeps typing as ordinary text with no
	// picker over it — and, per runTypedBoardCommand, stays a message on
	// enter too, since /new has nowhere to put those words.
	cmd := head[1 : len(head)-1]
	rows, takesValues := m.boardValueRows(cmd)
	if !takesValues {
		return
	}
	m.boardComplete = newCompletion(head, prefix, true, rows)
}

// handleBoardCompletionKey answers the keys the popup claims while it is
// open, and reports whether it took the key. Everything it does not claim
// falls through to the composer untouched — the popup never holds the
// keyboard, so typing through it is just typing.
//
// The two keys that change meaning here, and only here, are tab and
// enter, and both go back to their usual jobs the instant the popup is
// gone. esc is a third, softer case: it dismisses the popup rather than
// interrupting the board's turn, which is the same "esc backs out of the
// nearest thing first" rule every other surface follows.
func (m *Shell) handleBoardCompletionKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	c := m.boardComplete
	if c == nil {
		return nil, false
	}
	switch msg.String() {
	case "up":
		c.move(-1)
		return nil, true
	case "down":
		c.move(1)
		return nil, true
	case "esc":
		// Dismiss only. The text stays exactly as typed — esc here means
		// "stop offering", not "undo what I wrote" — and a second esc,
		// with no popup left to close, interrupts the turn as it always
		// has.
		m.boardComplete = nil
		return nil, true
	case "tab":
		m.completeBoardWord()
		return nil, true
	case "enter":
		row, ok := c.selected()
		if !ok {
			return nil, false
		}
		if !row.available {
			// Visible but not offered, exactly as the command menu treats
			// the same case: say why rather than swallowing the key or
			// running something that will fail further in.
			m.notice = noticeMsg{text: row.name + " — not available here", isErr: true}
			return nil, true
		}
		if !c.value && row.needsValue {
			// A command tier row that needsValue has nothing to run yet —
			// board-profile and board-model both require an argument
			// (runBoardCommand), and firing them with "" would only mean
			// raising the very "name one" notice a second later. So enter
			// finishes the word and opens the value tier instead of
			// running the command and immediately refusing the key that
			// offered it.
			//
			// acceptRow, not accept(): enter is a choice of THIS row, and
			// accept()'s shared-prefix answer would rewrite "/" to "/"
			// whenever several commands still match — the highlighted row
			// doing visibly nothing. acceptRow's own comment has the case.
			m.setBoardLine(c.acceptRow(row))
			return nil, true
		}
		m.boardComplete = nil
		m.boardInput.Reset()
		return m.runBoardCompletion(row), true
	}
	return nil, false
}

// completeBoardWord rewrites the composer to the popup's best completion
// (commonPrefix across every still-matching row, or the sole row's own
// name once only one remains — completion.accept's own rule) and
// re-derives the popup from the result. It is tab's whole job, factored
// out so enter's needsValue case (above) can perform the identical
// completion instead of duplicating these three lines beside it.
func (m *Shell) completeBoardWord() {
	line, _ := m.boardComplete.accept()
	m.setBoardLine(line)
}

// setBoardLine replaces the composer's contents and lets the popup
// re-derive itself from the result.
//
// Re-derived rather than assumed: completing a command to "/agent " or
// "/profile " is exactly the line that opens the value tier behind it,
// and letting syncBoardCompletion notice that is what makes the second
// popup appear without either caller knowing anything about tiers. It is
// shared by tab (which completes as far as every match agrees) and by
// enter on a command that needs a value (which completes to the one row
// under the cursor) precisely so those two can differ in WHICH line they
// choose without also differing in what happens to the popup afterwards.
func (m *Shell) setBoardLine(line string) {
	m.boardInput.SetValue(line)
	m.boardInput.CursorEnd()
	m.syncBoardCompletion()
}

// runBoardCompletion performs one chosen row.
//
// A command row's id is the space menu's id, so a bare command lands on
// runBoardCommand — the identical dispatch a fully typed line runs
// through (runTypedBoardCommand), not a second copy of it. A value row
// carries "<command id>:<value>" (boardProfileValueRows,
// boardModelValueRows), which this splits back into the pair
// runBoardCommand expects — the one mapping this file needs of its own,
// and one line per command that takes values.
func (m *Shell) runBoardCompletion(row completionRow) tea.Cmd {
	if id, val, ok := strings.Cut(row.id, ":"); ok {
		return m.runBoardCommand(id, val)
	}
	return m.runBoardCommand(row.id, "")
}

// runBoardCommand is the one place a board command actually runs, whether
// it was reached by choosing a row from the popup (runBoardCompletion) or
// by typing the whole line and pressing enter with no popup open
// (runTypedBoardCommand). Every command in globalCommands still ignores
// arg and falls through to runCommand(id) except the two that were the
// reason arg was wired through in the first place — and board-clear,
// which belongs to no menu (boardCommandRows) and routes to the tab's
// own clear action.
func (m *Shell) runBoardCommand(id, arg string) tea.Cmd {
	switch id {
	case "board-profile":
		return m.runBoardProfileCommand(arg)
	case "board-model":
		return m.runBoardModelCommand(arg)
	case "board-clear":
		return m.clearBoardConversation()
	}
	return m.runCommand(id)
}

// openBoardValuePicker answers a command-menu pick of "/profile" or
// "/model".
//
// Both need an argument the menu has no way to supply: its onRun is
// runCommand(id string) — one id, no room for a value — so picking either
// one there used to do nothing whatsoever. boardVerb's default case
// swallowed the id, the menu closed, and the user got no dialog, no
// notice and no action.
//
// Rather than teach the menu an argument field it has no other use for,
// the entry means "take me somewhere I can say which one": the agent tab,
// with the word already typed and its value picker open. That is the
// identical line tab-completing "/profile" leaves behind, so the two
// routes into this choice cannot present it differently.
func (m *Shell) openBoardValuePicker(id string) tea.Cmd {
	name := "profile"
	if id == "board-model" {
		name = "model"
	}
	// gotoTab focuses the board composer and starts the session if this
	// is the tab's first visit, so the line below always lands in a
	// focused widget on a tab that is on its way to having a session.
	cmd := m.gotoTab(TabAgent)
	m.setBoardLine("/" + name + " ")
	return cmd
}

// boardSnapshot is the live board session's snapshot, or the zero value
// when no session is open — so callers testing "is there anything going
// on" can ask one question instead of guarding the nil first.
func (m *Shell) boardSnapshot() engine.Snapshot {
	if m.board == nil {
		return engine.Snapshot{}
	}
	return m.board.Snapshot()
}

// runBoardProfileCommand answers /profile <arg>. An empty arg — the bare
// command, typed and sent without ever opening the popup — has nothing
// to switch to, so it names what's missing rather than running anything.
// An arg that names no declared profile is refused with a notice rather
// than handed to the engine: resolveBoardRole (internal/engine/
// profiles.go) falls back to the workspace's default profile for an
// unknown name, which would make a typo look like it took effect while
// silently switching to something else. Only a name BoardProfiles
// actually declares reaches confirmBoardReopen.
func (m *Shell) runBoardProfileCommand(arg string) tea.Cmd {
	if arg == "" {
		m.notice = noticeMsg{text: "/profile — name a profile (tab-complete to see the list)"}
		return nil
	}
	if m.engine == nil {
		return nil
	}
	// Case-insensitively, like every other match in this feature (the
	// popup's prefix filter, the command word itself) — and the declared
	// spelling is what gets used, never what was typed, so the name that
	// reaches the engine is one resolveBoardRole can actually find.
	name := ""
	for _, p := range m.engine.BoardProfiles() {
		if strings.EqualFold(p.Name, arg) {
			name = p.Name
			break
		}
	}
	if name == "" {
		m.notice = noticeMsg{text: "no profile named " + arg, isErr: true}
		return nil
	}
	return m.confirmBoardReopen(
		"switch the board to the "+name+" profile?",
		"the current conversation ends; a fresh one starts under "+name,
		engine.BoardOpts{Profile: name},
	)
}

// runBoardModelCommand answers /model <arg>. Unlike /profile, any
// non-empty string is accepted — KnownModels' own doc comment is
// explicit that gummi keeps no registry of valid model names, so the
// picker is a memory aid, never a gate, and the free-text rows
// boardModelValueRows offers are exactly as authoritative as whatever
// the user chooses to type instead.
//
// The board's CURRENT profile travels with the switch: Profile is read
// from the live session (m.board.Profile()), not left empty. Leaving it
// empty would hand ReopenBoard the workspace's default profile instead
// of the one already running — switching the model would then silently
// reset the profile too, which is exactly the bug this field exists to
// avoid (engine.BoardOpts.Model's own doc comment).
func (m *Shell) runBoardModelCommand(arg string) tea.Cmd {
	if arg == "" {
		m.notice = noticeMsg{text: "/model — name a model (tab-complete to see known ones)"}
		return nil
	}
	var profile string
	if m.board != nil {
		profile = m.board.Profile()
	}
	return m.confirmBoardReopen(
		"switch the board to model "+arg+"?",
		"the current conversation ends; a fresh one starts under it",
		engine.BoardOpts{Profile: profile, Model: arg},
	)
}

// confirmBoardReopen gates a board respawn behind a confirm — reopening
// always ends the running conversation, and /profile and /model are
// exactly the moment a user might not have meant to throw it away. But
// only when there is something the confirm could protect: a session
// with no transcript yet (or no session at all) has nothing to lose, and
// asking "discard this empty conversation?" is a dialog that only ever
// gets in the way — the same judgment confirmDuplicate (boardactions.go)
// already makes for a different destructive action.
func (m *Shell) confirmBoardReopen(question, detail string, opts engine.BoardOpts) tea.Cmd {
	// Busy counts as "something to lose" alongside a non-empty transcript.
	// sendBoardMessage clears the composer synchronously but appends the
	// turn from inside its command, so for the moment between the two the
	// transcript is still empty while a turn is very much in flight —
	// skipping the confirm there would discard a message the user watched
	// leave the composer.
	if snap := m.boardSnapshot(); m.board == nil || (len(snap.Transcript) == 0 && !snap.Busy) {
		return m.reopenBoard(opts)
	}
	m.Overlay.Push(&confirmDialog{
		id:           "confirm-board-reopen",
		cancelLabel:  "Cancel",
		confirmLabel: "Switch",
		question:     question,
		detail:       detail,
		onConfirm:    func() tea.Cmd { return m.reopenBoard(opts) },
	})
	return nil
}

// boardCommandWord splits a composer line into the command word and its
// argument by reusing completeSlash's own head/prefix/value split rather
// than re-deriving the same "/" and first-space rule a second time here.
// ok is false for anything completeSlash does not recognize as command
// syntax at all — no leading "/", a pasted block with a newline in it —
// which is exactly when the caller has nothing to match and keeps the
// line as prose.
//
// It answers "what word, and what's left over", nothing about whether
// that word means anything: completeSlash's value flag already tells us
// which half of the line the word lives in (bare, or before the first
// space), so recombining it here is arithmetic, not a vocabulary lookup.
// The vocabulary lookup is runTypedBoardCommand's job, once, against
// exactly this pair.
func boardCommandWord(line string) (word, arg string, ok bool) {
	head, prefix, value, ok := completeSlash(line)
	if !ok {
		return "", "", false
	}
	if !value {
		return prefix, "", true
	}
	// head is "/" + word + " " by completeSlash's own construction (the
	// space it split on is folded into head, never left in prefix), so
	// trimming exactly one leading and one trailing character recovers
	// the word without needing strings.TrimSpace to guess how much
	// whitespace is really there.
	return head[1 : len(head)-1], strings.TrimSpace(prefix), true
}

// runTypedBoardCommand is enter's answer when no popup is open: does the
// line's command word — and only the word, never the rest of the line —
// exactly name an entry in the board's slash vocabulary?
//
// Exact and case-insensitive, never a prefix, on purpose: this sits
// beside the same closed-vocabulary reasoning verbs.go's parseInput
// documents for the card thread. A popup narrows on a prefix because it
// is showing you what typing more would still match; enter has no such
// hedge to offer. If "/inb" ran "/inbox" here, a vocabulary that later
// grows a command actually starting with "inb" would silently steal a
// line someone has already sent a hundred times as a message.
//
// ok is true exactly when this function has already decided the line's
// fate — run it, or refuse it with the same notice
// handleBoardCompletionKey raises for an unavailable row — so the caller
// (handleBoardInputKey) never falls through to sendBoardMessage for a
// line this claimed. ok is false for everything else, including a "/"
// word that matches nothing, which is the hole this function exists to
// close without touching what enter does for prose.
func (m *Shell) runTypedBoardCommand(line string) (tea.Cmd, bool) {
	word, arg, ok := boardCommandWord(line)
	if !ok || word == "" {
		return nil, false
	}
	for _, row := range m.boardCommandRows() {
		if !strings.EqualFold(row.name, word) {
			continue
		}
		if arg != "" && !m.boardCommandTakesValue(row.name) {
			// The word matches, but the command has nowhere to put the
			// rest of the line — runCommand takes an id and nothing else.
			// Claiming the line anyway is how "/quit for today, ship
			// tomorrow" opened the quit dialog and "/new dark mode"
			// created an untitled card, in both cases swallowing the
			// words without a word about it. A command that cannot use
			// what follows it has not been typed as a command, so this
			// stays what the tab promises every unclaimed line is: a
			// message, delivered whole. The board has tools of its own
			// for acting on it.
			return nil, false
		}
		if !row.available {
			m.notice = noticeMsg{text: row.name + " — not available here", isErr: true}
			return nil, true
		}
		m.boardInput.Reset()
		return m.runBoardCommand(row.id, arg), true
	}
	return nil, false
}

// boardCommandTakesValue reports whether a command word can carry an
// argument at all, which is exactly "does it offer a value tier" —
// boardValueRows' own ok result. Deriving it from there rather than
// keeping a second list is what stops the two from disagreeing about a
// command the popup completes values for but the typed path drops them
// from.
func (m *Shell) boardCommandTakesValue(name string) bool {
	_, ok := m.boardValueRows(strings.ToLower(name))
	return ok
}
