package ui

import (
	"sort"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/engine"
)

// slashKey is the "/" keypress, spelled once so every test below opens
// the popup exactly the way a user does.
var slashKey = tea.KeyPressMsg{Code: '/', Text: "/"}

// boardTabWithSlash opens the agent tab and types "/" into its composer.
func boardTabWithSlash(t *testing.T) *Shell {
	t.Helper()
	m, _ := agentWorkspace(t, agent.NewFake("ok"))
	m = openBoardTab(t, m)
	return press(t, m, slashKey)
}

// TestBoardSlashOpensTheCompletionPopup: "/" on an empty line offers the
// board-level command inventory, and the rows are actually drawn into the
// tab — not merely held in a field.
func TestBoardSlashOpensTheCompletionPopup(t *testing.T) {
	m := boardTabWithSlash(t)
	if m.boardComplete == nil {
		t.Fatal(`"/" on an empty board line did not open the completion popup`)
	}
	if m.boardInput.Value() != "/" {
		t.Errorf("composer = %q, want the typed %q", m.boardInput.Value(), "/")
	}
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"/new", "/inbox", "more — keep typing"} {
		if !strings.Contains(view, want) {
			t.Errorf("agent tab view missing %q:\n%s", want, view)
		}
	}
}

// TestBoardSlashPopupNarrowsAsYouType: the rows follow the prefix, and
// the ones that stop matching stop being drawn.
func TestBoardSlashPopupNarrowsAsYouType(t *testing.T) {
	m := boardTabWithSlash(t)
	m = typeString(t, m, "in")

	var got []string
	for _, r := range m.boardComplete.rows {
		got = append(got, r.name)
	}
	if strings.Join(got, ",") != "ingest,inbox" {
		t.Fatalf("rows for %q = %v, want [ingest inbox]", m.boardInput.Value(), got)
	}
	view := ansi.Strip(m.View().Content)
	if strings.Contains(view, "/new") {
		t.Errorf("a row that no longer matches is still drawn:\n%s", view)
	}
}

// TestBoardSlashTabCompletesWithoutRunning is tab's whole contract here:
// it rewrites the line and stops. Nothing is sent, nothing is run, and
// the popup is re-derived from the line tab just wrote — which for a
// command that takes no values means it closes.
func TestBoardSlashTabCompletesWithoutRunning(t *testing.T) {
	m := boardTabWithSlash(t)
	m = typeString(t, m, "inb")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})

	if got := m.boardInput.Value(); got != "/inbox " {
		t.Fatalf("composer after tab = %q, want %q", got, "/inbox ")
	}
	if m.tab != TabAgent {
		t.Errorf("tab = %v: tab completed AND switched tabs", m.tab)
	}
	if len(m.board.Snapshot().Transcript) != 0 {
		t.Error("tab sent the line as a message")
	}
}

// TestBoardSlashTabStopsAtTheSharedPrefix: with several rows still in
// play tab extends as far as they agree and no further, leaving the
// popup open on the narrowed set rather than picking one for you.
func TestBoardSlashTabStopsAtTheSharedPrefix(t *testing.T) {
	m := boardTabWithSlash(t)
	m = typeString(t, m, "i")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})

	if got := m.boardInput.Value(); got != "/i" {
		t.Fatalf("composer after tab = %q, want %q — ingest/import/inbox share only \"i\"", got, "/i")
	}
	if m.boardComplete == nil {
		t.Fatal("the popup closed on a line that still matches three commands")
	}
}

// TestBoardSlashEnterRunsTheCommand: enter runs the highlighted row
// rather than sending the line, and the composer is left empty — the
// command is gone, not sitting there to be sent again.
func TestBoardSlashEnterRunsTheCommand(t *testing.T) {
	m := boardTabWithSlash(t)
	m = typeString(t, m, "inb")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.tab != TabInbox {
		t.Errorf("tab = %v, want TabInbox — enter did not run /inbox", m.tab)
	}
	if m.boardInput.Value() != "" {
		t.Errorf("composer = %q, want it cleared", m.boardInput.Value())
	}
	if len(m.board.Snapshot().Transcript) != 0 {
		t.Error("enter ran the command AND sent it as a message")
	}
}

// TestBoardSlashEscDismissesThenInterrupts: esc backs out of the nearest
// thing first. The line survives the dismissal — esc means "stop
// offering", not "undo what I typed" — and with no popup left, esc is
// the interrupt it has always been.
func TestBoardSlashEscDismissesThenInterrupts(t *testing.T) {
	m := boardTabWithSlash(t)
	m = typeString(t, m, "inb")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})

	if m.boardComplete != nil {
		t.Fatal("esc did not dismiss the popup")
	}
	if got := m.boardInput.Value(); got != "/inb" {
		t.Errorf("composer after esc = %q, want the line kept as %q", got, "/inb")
	}
	// second esc: nothing to dismiss, so it reaches the interrupt path
	// without the popup having permanently stolen the key.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.boardComplete != nil {
		t.Error("a dismissed popup came back")
	}
}

// TestBoardSlashMidLineIsJustText: the popup costs the "/" character
// only in column one. Everything the composer accepted as prose before —
// paths, "and/or", URLs — still types as itself and still sends.
func TestBoardSlashMidLineIsJustText(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("ok"))
	m = openBoardTab(t, m)
	m = typeString(t, m, "look at internal/ui/complete.go")

	if m.boardComplete != nil {
		t.Fatalf("a mid-line slash opened the popup: %q", m.boardInput.Value())
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleBoard(t, m.board)
	if !strings.Contains(ansi.Strip(m.View().Content), "internal/ui/complete.go") {
		t.Error("the line was not sent as a message")
	}
}

// TestBoardSlashNonsenseStillSends: a command line matching nothing has
// no popup, and enter sends it as the message this tab has always
// promised every line is. Completion is not a reason to start refusing
// lines that used to work.
func TestBoardSlashNonsenseStillSends(t *testing.T) {
	m := boardTabWithSlash(t)
	m = typeString(t, m, "rebase the theme branch")

	if m.boardComplete != nil {
		t.Fatal("a line matching no command still shows a popup")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleBoard(t, m.board)
	if !strings.Contains(ansi.Strip(m.View().Content), "rebase the theme branch") {
		t.Error("a non-matching command line was not sent as a message")
	}
}

// TestBoardTabStillCyclesWithoutAPopup: the one key the popup borrows
// from a global is given straight back. With nothing open, tab is the
// tab cycle it always was.
func TestBoardTabStillCyclesWithoutAPopup(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("ok"))
	m = openBoardTab(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.tab == TabAgent {
		t.Error("tab did not cycle away from the agent tab with no popup open")
	}
}

// TestBoardAgentValuesCompleteInline is the argument tier: once "/agent"
// is a whole word, the popup offers the CLI names instead of asking for
// a string. The names are agentcli.Known's fixed set; which of them are
// installed depends on the machine, so only the names are asserted.
func TestBoardAgentValuesCompleteInline(t *testing.T) {
	m := boardTabWithSlash(t)
	m = typeString(t, m, "agent ")

	if m.boardComplete == nil {
		t.Fatal(`"/agent " did not open the value picker`)
	}
	if !m.boardComplete.value {
		t.Error("the popup opened on the command tier, not the value tier")
	}
	var got []string
	for _, r := range m.boardComplete.rows {
		got = append(got, r.name)
	}
	if strings.Join(got, ",") != "copilot,claude,codex,opencode,zz" {
		t.Errorf("value rows = %v, want the five known CLIs", got)
	}

	m = typeString(t, m, "cop")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.boardInput.Value(); got != "/agent copilot" {
		t.Errorf("composer after completing a value = %q, want %q", got, "/agent copilot")
	}
}

// TestBoardCommandWithoutValuesTakesFreeText: only a command that
// actually has a value list opens the second tier, AND — the half this
// test used to leave unchecked — enter on such a line sends it rather
// than running the command and dropping the words.
//
// runCommand takes an id and nothing else, so there is nowhere for "dark
// mode" to go. Claiming the line anyway is how "/new dark mode" created
// an untitled card and "/quit for today, ship tomorrow" opened the quit
// dialog, both swallowing the rest of the line silently. Pressing enter
// is the whole point of the test; without it this passed against exactly
// the bug it is named for.
func TestBoardCommandWithoutValuesTakesFreeText(t *testing.T) {
	m := boardTabWithSlash(t)
	m = typeString(t, m, "new dark mode")
	if m.boardComplete != nil {
		t.Errorf("a command with no value list opened one: %q", m.boardInput.Value())
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleBoard(t, m.board)
	if !strings.Contains(ansi.Strip(m.View().Content), "dark mode") {
		t.Error("the words were dropped instead of sent as a message")
	}
}

// TestBoardCommandWordWithArgumentItCannotTakeStaysAMessage is the same
// rule at its most damaging. "q" quits gummi; a line beginning "/quit"
// that carries anything else is prose, and must never reach the quit
// dialog.
func TestBoardCommandWordWithArgumentItCannotTakeStaysAMessage(t *testing.T) {
	m := boardTabWithSlash(t)
	m = typeString(t, m, "quit for today, ship tomorrow")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.Overlay.Len() != 0 {
		t.Fatal("a message beginning /quit opened a dialog")
	}
	settleBoard(t, m.board)
	if !strings.Contains(ansi.Strip(m.View().Content), "ship tomorrow") {
		t.Error("the line was not sent as a message")
	}
}

// TestBoardBareCommandWordStillRuns guards the other side of that rule:
// narrowing what counts as a command must not stop a bare command word
// from running.
func TestBoardBareCommandWordStillRuns(t *testing.T) {
	m := boardTabWithSlash(t)
	m = typeString(t, m, "inbox ")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.tab != TabInbox {
		t.Errorf("tab = %v, want TabInbox — a bare command word stopped running", m.tab)
	}
}

// TestBoardProfileFromTheCommandMenuOpensThePicker: /profile and /model
// are in globalCommands, which feeds the space-key menu as well as the
// slash popup — and that menu's onRun carries an id with no room for an
// argument, so picking either one there did nothing at all. It has to
// land somewhere the value can be given.
func TestBoardProfileFromTheCommandMenuOpensThePicker(t *testing.T) {
	m, _ := agentWorkspaceProfiles(t, agent.NewFake("ok"), testBoardProfiles())
	m = pump(t, m, m.runCommand("board-profile"))

	if m.tab != TabAgent {
		t.Fatalf("tab = %v, want TabAgent", m.tab)
	}
	if got := m.boardInput.Value(); got != "/profile " {
		t.Fatalf("composer = %q, want %q", got, "/profile ")
	}
	if m.boardComplete == nil || !m.boardComplete.value {
		t.Error("the value picker did not open")
	}
}

// TestBoardProfileArgIsCaseInsensitive: every other match in this feature
// ignores case, and the declared spelling is what must reach the engine —
// resolveBoardRole looks the name up literally.
func TestBoardProfileArgIsCaseInsensitive(t *testing.T) {
	m := press(t, boardTabWithProfiles(t), slashKey)
	m = typeString(t, m, "profile THRIFTY")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.notice.isErr {
		t.Fatalf("a differently-cased but declared profile was refused: %q", m.notice.text)
	}
}

// TestBoardEnterRunsAValueTierLineWithNoPopup: "/inbox " is already past
// the command word, into the value tier, and inbox has no values — so
// the popup that opened while "/inbox" was still being typed is gone by
// the time the trailing space lands. Enter still has to run it rather
// than send the literal text.
func TestBoardEnterRunsAValueTierLineWithNoPopup(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("ok"))
	m = openBoardTab(t, m)
	m = typeString(t, m, "/inbox ")

	if m.boardComplete != nil {
		t.Fatal("inbox has no values — the popup should already be closed")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.tab != TabInbox {
		t.Errorf("tab = %v, want TabInbox — enter did not run /inbox", m.tab)
	}
	if m.boardInput.Value() != "" {
		t.Errorf("composer = %q, want it cleared", m.boardInput.Value())
	}
	if len(m.board.Snapshot().Transcript) != 0 {
		t.Error("enter sent the line as a message instead of running it")
	}
}

// TestBoardTabThenEnterRunsTheCommand is the whole round trip: tab
// completes "/inb" to "/inbox " (closing the popup, per
// TestBoardSlashTabCompletesWithoutRunning), and enter on that finished
// line still has to run it.
func TestBoardTabThenEnterRunsTheCommand(t *testing.T) {
	m := boardTabWithSlash(t)
	m = typeString(t, m, "inb")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.boardInput.Value(); got != "/inbox " {
		t.Fatalf("composer after tab = %q, want %q", got, "/inbox ")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.tab != TabInbox {
		t.Errorf("tab = %v, want TabInbox — enter did not run the tab-completed line", m.tab)
	}
	if len(m.board.Snapshot().Transcript) != 0 {
		t.Error("enter sent the tab-completed line as a message instead of running it")
	}
}

// TestBoardEscThenEnterSendsAsMessage: esc dismisses the popup but keeps
// the text (TestBoardSlashEscDismissesThenInterrupts), and "/inb" matches
// no command word exactly — so a second esc would interrupt, but the
// first enter after the dismissal has to send it as prose, not run
// "/inbox" as if the popup had chosen it.
func TestBoardEscThenEnterSendsAsMessage(t *testing.T) {
	m := boardTabWithSlash(t)
	m = typeString(t, m, "inb")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.boardComplete != nil {
		t.Fatal("esc did not dismiss the popup")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleBoard(t, m.board)

	if m.tab != TabAgent {
		t.Errorf("tab = %v: a non-matching word ran a command instead of sending", m.tab)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "/inb") {
		t.Error("the dismissed line was not sent as a message")
	}
}

// TestBoardRebaseLineSendsAsMessage: a slash line whose word matches no
// command still sends, even with an argument that reads like a real
// instruction — the command word is what enter judges, never the rest of
// the line.
func TestBoardRebaseLineSendsAsMessage(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("ok"))
	m = openBoardTab(t, m)
	m = typeString(t, m, "/rebase the theme branch")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleBoard(t, m.board)

	if m.tab != TabAgent {
		t.Errorf("tab = %v: /rebase ran as a command instead of sending", m.tab)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "/rebase the theme branch") {
		t.Error("the non-matching command line was not sent as a message")
	}
}

// TestBoardMidSentenceSlashSendsAsMessage: a "/" that never reached
// column one never opens the popup (TestBoardSlashMidLineIsJustText), and
// enter on it must not be reinterpreted as a command line either.
func TestBoardMidSentenceSlashSendsAsMessage(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("ok"))
	m = openBoardTab(t, m)
	m = typeString(t, m, "look at internal/ui/x.go")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleBoard(t, m.board)

	if m.tab != TabAgent {
		t.Errorf("tab = %v: a mid-sentence slash ran as a command instead of sending", m.tab)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "internal/ui/x.go") {
		t.Error("the mid-sentence line was not sent as a message")
	}
}

// TestBoardEnterIsCaseInsensitive: the command word match is
// case-insensitive, same as completionPrefix's own rule for the popup.
func TestBoardEnterIsCaseInsensitive(t *testing.T) {
	m, _ := agentWorkspace(t, agent.NewFake("ok"))
	m = openBoardTab(t, m)
	m = typeString(t, m, "/INBOX")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.tab != TabInbox {
		t.Errorf("tab = %v, want TabInbox — \"/INBOX\" did not run case-insensitively", m.tab)
	}
	if len(m.board.Snapshot().Transcript) != 0 {
		t.Error("enter sent \"/INBOX\" as a message instead of running it")
	}
}

// TestBoardCardActionsStayOutOfTheSlashVocabulary: globalCommands
// appends the selected card's whole inventory when a card page is open
// on the BOARD tab, and none of it belongs to a board conversation —
// "/park" here would park a card on a tab that is not even visible. The
// name filter (boardCommandRows) is what keeps it out.
func TestBoardCardActionsStayOutOfTheSlashVocabulary(t *testing.T) {
	m := boardTabWithSlash(t)
	m.cardOpen = true // a card page left open on the hidden board tab

	for _, r := range m.boardCommandRows() {
		if r.name == "" {
			t.Fatal("an unnamed command reached the slash vocabulary")
		}
	}
	names := map[string]bool{}
	for _, r := range m.boardCommandRows() {
		names[r.name] = true
	}
	for _, forbidden := range []string{"park", "diff", "envelope", "duplicate"} {
		if names[forbidden] {
			t.Errorf("card-scoped action %q is offered on the board thread", forbidden)
		}
	}
}

// testBoardProfiles is the profiles.yaml fixture behind every /profile
// and /model test below. "thrifty" and "careful" each declare a role
// resolveBoardRole actually reaches (board, then architect — see its own
// doc comment), so BoardProfiles resolves each to a distinct
// backend+model; "bare" declares neither, exercising the "nothing at
// all" fallback whose empty Backend/Model boardProfileValueRows has to
// word as a default rather than leave blank.
func testBoardProfiles() config.Profiles {
	return config.Profiles{
		Default: "thrifty",
		Profiles: map[string]config.Profile{
			"thrifty": {"board": {Backend: "claude", Model: "haiku"}},
			"careful": {"architect": {Backend: "codex", Model: "gpt-5"}},
			"bare":    {"implementer": {Backend: "codex", Model: "gpt-5"}},
		},
	}
}

// boardTabWithProfiles opens the agent tab on a workspace whose engine
// was built with testBoardProfiles wired in — the fixture the /profile
// and /model tests need that boardTabWithSlash's plain agentWorkspace
// doesn't carry.
func boardTabWithProfiles(t *testing.T) *Shell {
	t.Helper()
	m, _ := agentWorkspaceProfiles(t, agent.NewFake("ok"), testBoardProfiles())
	return openBoardTab(t, m)
}

// TestBoardProfileValueRowsListDeclaredProfilesAndMarkCurrent: "/profile "
// offers every declared profile, described by what it actually resolves
// to (not the raw yaml — engine.BoardProfile's own doc comment), and the
// one the live session is running under reads as current.
func TestBoardProfileValueRowsListDeclaredProfilesAndMarkCurrent(t *testing.T) {
	m := boardTabWithProfiles(t)
	m = pump(t, m, m.reopenBoard(engine.BoardOpts{Profile: "careful"}))
	if m.board == nil || m.board.Profile() != "careful" {
		t.Fatalf(`setup: board did not reopen under "careful" (profile=%q)`, m.board.Profile())
	}

	m = press(t, m, slashKey)
	m = typeString(t, m, "profile ")

	if m.boardComplete == nil || !m.boardComplete.value {
		t.Fatal(`"/profile " did not open the value tier`)
	}
	got := map[string]string{}
	for _, r := range m.boardComplete.rows {
		got[r.name] = r.desc
	}
	for _, name := range []string{"thrifty", "careful", "bare"} {
		if _, ok := got[name]; !ok {
			t.Errorf("profile rows = %v, missing %q", got, name)
		}
	}
	if !strings.Contains(got["careful"], "codex") || !strings.Contains(got["careful"], "gpt-5") {
		t.Errorf(`"careful" desc = %q, want it to name its resolved backend and model`, got["careful"])
	}
	if !strings.Contains(got["careful"], "current") {
		t.Errorf(`"careful" desc = %q, want it marked current — the board is running under it`, got["careful"])
	}
	if strings.Contains(got["thrifty"], "current") {
		t.Errorf(`"thrifty" desc = %q, wrongly marked current`, got["thrifty"])
	}
	if !strings.Contains(got["bare"], "default") {
		t.Errorf(`"bare" desc = %q, want its empty backend/model worded as a default rather than left blank`, got["bare"])
	}
}

// TestBoardModelValueRowsListKnownModels: "/model " offers every model
// harvested from profiles.yaml (engine.KnownModels), not a fixed
// registry — the fixture declares exactly "haiku" and "gpt-5".
func TestBoardModelValueRowsListKnownModels(t *testing.T) {
	m := boardTabWithProfiles(t)
	m = press(t, m, slashKey)
	m = typeString(t, m, "model ")

	if m.boardComplete == nil || !m.boardComplete.value {
		t.Fatal(`"/model " did not open the value tier`)
	}
	var names []string
	for _, r := range m.boardComplete.rows {
		names = append(names, r.name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "gpt-5,haiku" {
		t.Errorf("model rows = %v, want the models harvested from profiles.yaml", names)
	}
}

// TestBoardProfileCommandTierEnterOpensValueTier: enter on the COMMAND
// tier's "/profile" row has nothing to run — the command needsValue — so
// it completes the word and opens the value tier instead, exactly what
// tab already does, rather than running board-profile with no argument.
func TestBoardProfileCommandTierEnterOpensValueTier(t *testing.T) {
	m := boardTabWithProfiles(t)
	m = press(t, m, slashKey)
	m = typeString(t, m, "profile")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if got := m.boardInput.Value(); got != "/profile " {
		t.Fatalf("composer after enter on a needsValue row = %q, want %q", got, "/profile ")
	}
	if m.boardComplete == nil || !m.boardComplete.value {
		t.Fatal(`enter on "/profile" did not open the value tier`)
	}
	if len(m.board.Snapshot().Transcript) != 0 {
		t.Error("enter ran the command instead of completing the word")
	}
}

// TestBoardProfileUnknownArgRefuses: an argument naming no declared
// profile is refused with a notice — never silently handed to the
// engine, which would fall back to the workspace default and leave a
// typo looking like it took effect.
func TestBoardProfileUnknownArgRefuses(t *testing.T) {
	m := boardTabWithProfiles(t)
	before := m.board

	m = typeString(t, m, "/profile nope")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if !m.notice.isErr || !strings.Contains(m.notice.text, "nope") {
		t.Errorf("notice = %+v, want an error naming the unknown profile", m.notice)
	}
	if m.board != before {
		t.Error("an unknown profile still reopened the board")
	}
	if m.Overlay.Contains("confirm-board-reopen") {
		t.Error("an unknown profile raised a confirm it should never reach")
	}
}

// TestBoardModelArbitraryStringAccepted: unlike /profile, /model has no
// registry behind it — any non-empty string is accepted, since gummi
// treats a model as opaque everywhere else too (KnownModels' own doc
// comment).
func TestBoardModelArbitraryStringAccepted(t *testing.T) {
	m := boardTabWithProfiles(t)

	m = typeString(t, m, "/model something-nobody-declared")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.notice.isErr {
		t.Errorf("an arbitrary model string was refused: %+v", m.notice)
	}
	if m.board == nil || m.board.Snapshot().Model != "something-nobody-declared" {
		t.Fatalf("board model = %q, want the arbitrary string accepted verbatim", m.board.Snapshot().Model)
	}
}

// TestBoardModelSwitchPreservesCurrentProfile: the important case
// runBoardModelCommand's doc comment calls out — switching the model
// must not silently reset the profile back to the workspace default.
func TestBoardModelSwitchPreservesCurrentProfile(t *testing.T) {
	m := boardTabWithProfiles(t)
	m = pump(t, m, m.reopenBoard(engine.BoardOpts{Profile: "careful"}))
	if m.board == nil || m.board.Profile() != "careful" {
		t.Fatalf(`setup: board did not reopen under "careful"`)
	}

	m = typeString(t, m, "/model swapped-model")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.board == nil || m.board.Profile() != "careful" {
		t.Errorf(`board profile after /model = %q, want "careful" preserved`, m.board.Profile())
	}
	if m.board.Snapshot().Model != "swapped-model" {
		t.Errorf("board model = %q, want %q", m.board.Snapshot().Model, "swapped-model")
	}
}

// TestBoardReopenConfirmGate: reopening the board discards its
// conversation, so switching a second time — once there is a real
// conversation to lose — has to confirm first, but the very first switch
// (an empty, freshly-opened session) has nothing to lose and must not
// stop to ask.
func TestBoardReopenConfirmGate(t *testing.T) {
	m := boardTabWithProfiles(t)

	m = typeString(t, m, "/profile careful")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Overlay.Contains("confirm-board-reopen") {
		t.Fatal("an empty conversation raised a confirm before reopening")
	}
	if m.board == nil || m.board.Profile() != "careful" {
		t.Fatalf(`an empty conversation did not reopen straight away (profile=%q)`, m.board.Profile())
	}

	m = typeString(t, m, "hello board")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleBoard(t, m.board)
	if len(m.board.Snapshot().Transcript) == 0 {
		t.Fatal("setup: board has no transcript to protect")
	}

	m = typeString(t, m, "/profile bare")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.Overlay.Contains("confirm-board-reopen") {
		t.Fatal("a non-empty conversation reopened without confirming first")
	}
	if m.board.Profile() != "careful" {
		t.Error("the board reopened before the confirm was answered")
	}

	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if m.board == nil || m.board.Profile() != "bare" {
		t.Errorf(`confirming did not reopen under "bare" (profile=%q)`, m.board.Profile())
	}
}

// TestBoardEnterOnHighlightedNeedsValueRowCompletesThatRow is the
// regression for enter answering with the wrong word. The popup's
// accept() stops at the prefix every match shares, which is right for
// tab and wrong for enter: enter names the row under the cursor. With a
// dozen commands still matching, completing to the shared prefix
// rewrote "/" as "/" — the highlighted row visibly doing nothing at all.
func TestBoardEnterOnHighlightedNeedsValueRowCompletesThatRow(t *testing.T) {
	// The profiles fixture, so the value tier this is meant to land in
	// actually has rows to show — on a workspace declaring no profiles at
	// all there is nothing to offer and no popup is the honest answer.
	m := press(t, boardTabWithProfiles(t), slashKey)
	// Arrow onto /profile WITHOUT narrowing the list, so the prefix every
	// remaining row shares is still empty.
	steps := -1
	for i, r := range m.boardComplete.rows {
		if r.name == "profile" {
			steps = i
			break
		}
	}
	if steps <= 0 {
		t.Fatalf("no /profile row to walk to in the command tier (index %d)", steps)
	}
	if len(m.boardComplete.rows) < 3 {
		t.Fatalf("only %d rows match; this test needs the shared prefix to be empty", len(m.boardComplete.rows))
	}
	for range steps {
		m = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if got, _ := m.boardComplete.selected(); got.name != "profile" {
		t.Fatalf("cursor landed on %q, want profile", got.name)
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if got := m.boardInput.Value(); got != "/profile " {
		t.Fatalf("composer = %q, want %q — enter completed the shared prefix instead of the highlighted row", got, "/profile ")
	}
	if m.boardComplete == nil || !m.boardComplete.value {
		t.Error("enter completed the word but did not open the value tier behind it")
	}
}
