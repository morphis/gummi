package ui

import (
	"context"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/engine"
	"github.com/morphia/gummi/internal/notify"
	"github.com/morphia/gummi/internal/spec"
	"github.com/morphia/gummi/internal/state"
	"github.com/morphia/gummi/internal/ui/layout"
	"github.com/morphia/gummi/internal/ui/logo"
	"github.com/morphia/gummi/internal/ui/overlay"
	"github.com/morphia/gummi/internal/ui/statusbar"
	"github.com/morphia/gummi/internal/ui/theme"
	"github.com/morphia/gummi/internal/verify"
	"github.com/morphia/gummi/internal/worktree"
)

// Shell is gummi's top-level Bubble Tea model. It owns the screen
// buffer, the rectangle layout, the style set, the dialog stack, and —
// once attached to a workspace — the kanban board state. Panes render
// to strings and are painted into their rects (the Crush hybrid
// pattern); all IO runs in commands, never in Update or View.
type Shell struct {
	styles  *theme.Styles
	version string

	width, height int
	layout        layout.Layout

	// Dialogs (gate prompts, forms) live on this stack.
	Overlay overlay.Stack

	// workspace wiring (nil store means detached: splash only)
	store *state.Store
	wt    *worktree.Manager
	ws    state.Workspace

	rows   []featureRow
	sel    int
	notice noticeMsg
	spec   *specView // non-nil while the spec surface is open
	diff   *diffView // non-nil while the diff surface is open

	// agent orchestration (nil engine means no agent wired)
	engine       *engine.Engine
	chat         *chatPane // non-nil while attached to an interactive session
	inbox        *inbox    // needs-attention queue
	checks       map[domain.FeatureID][]verify.Result
	reviewRounds map[domain.FeatureID]int // automatic review→fix→review counter
	profileNames []string                 // profile names for the new-feature form
	envelope     int                      // default spend-plan envelope for new features (0 = none)
	notifier     *notify.Notifier         // bell/desktop hook for needs-attention events

	// now is injectable for deterministic tests.
	now func() time.Time
}

// NewShell builds a detached shell (splash + empty board).
func NewShell(t theme.Theme, version string) *Shell {
	return &Shell{
		styles:       theme.New(t),
		version:      version,
		now:          time.Now,
		inbox:        newInbox(),
		checks:       map[domain.FeatureID][]verify.Result{},
		reviewRounds: map[domain.FeatureID]int{},
	}
}

// Attach wires the shell to a workspace: its store, worktree manager,
// and paths. Must be called before Run for board functionality.
func (m *Shell) Attach(store *state.Store, wt *worktree.Manager, ws state.Workspace) {
	m.store, m.wt, m.ws = store, wt, ws
}

// AttachEngine wires the agent orchestrator, enabling interactive chat
// and autonomous stages. Optional: without it the board is static.
func (m *Shell) AttachEngine(e *engine.Engine) { m.engine = e }

// SetProfileNames sets the profile names offered by the new-feature
// form (from profiles.yaml). Empty leaves the built-in presets.
func (m *Shell) SetProfileNames(names []string) { m.profileNames = names }

// SetEnvelope sets the default spend-plan envelope (credits) stamped on
// new features, enabling layer-3 per-stage budgets. 0 leaves features
// unbudgeted (or governed by a flat per-stage budget).
func (m *Shell) SetEnvelope(credits int) { m.envelope = credits }

// SetNotifier wires the needs-attention notification hook (bell/desktop).
func (m *Shell) SetNotifier(n *notify.Notifier) { m.notifier = n }

// raiseAttention adds a needs-attention item and, when it is a new alert
// (not an update of an existing one), fires the notification hook.
func (m *Shell) raiseAttention(id domain.FeatureID, kind attnKind, text string) {
	if m.inbox.add(id, kind, text) {
		m.notifier.Alert(string(id) + ": " + text)
	}
}

// Styles exposes the derived style set to panes.
func (m *Shell) Styles() *theme.Styles { return m.styles }

// attached reports whether a workspace is wired in.
func (m *Shell) attached() bool { return m.store != nil }

// Init implements tea.Model.
func (m *Shell) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.attached() {
		cmds = append(cmds, m.loadRows)
	}
	if m.engine != nil {
		cmds = append(cmds, m.listenEngine)
	}
	return tea.Batch(cmds...)
}

// listenEngine bridges the engine's event channel into Bubble Tea: it
// blocks for one event and returns it as a message, and is re-issued
// after each one so the stream stays live.
func (m *Shell) listenEngine() tea.Msg {
	ev, ok := <-m.engine.Events()
	if !ok {
		return engineClosedMsg{}
	}
	return engineEventMsg{ev}
}

type (
	engineEventMsg  struct{ ev engine.Event }
	engineClosedMsg struct{}
)

// handleEngineEvent folds an engine event into the notice line, the
// needs-attention queue, and the automatic review loop. It returns a
// command for any automatic follow-up (review→fix→review), or nil.
func (m *Shell) handleEngineEvent(ev engine.Event) tea.Cmd {
	switch ev.Kind {
	case engine.EventError:
		if ev.Err != nil {
			// engine/provider errors may embed model-controlled bytes
			text := sanitize(ev.Err.Error())
			m.notice = noticeMsg{text: text, isErr: true}
			m.raiseAttention(ev.Feature, attnFailure, text)
		}
	case engine.EventExhausted:
		// budget exhausted mid-stage: raise a gate, don't auto-continue.
		m.reviewRounds[ev.Feature] = 0
		m.raiseAttention(ev.Feature, attnBudget, string(ev.Stage)+" hit its budget — u top up (release reserve) or x park")
		m.notice = noticeMsg{text: string(ev.Feature) + " budget exhausted at " + string(ev.Stage), isErr: true}
	case engine.EventIdle:
		s := m.engine.Get(ev.Feature)
		if s == nil || s.Interactive || s.State() != engine.StateDone {
			return nil
		}
		// review/implement completions may drive the automatic loop;
		// anything the loop doesn't consume becomes a generic gate item.
		if handled, cmd := m.onAutonomousDone(ev.Feature, ev.Stage); handled {
			return cmd
		}
		m.raiseAttention(ev.Feature, attnGate, string(ev.Stage)+" finished — review & advance")
	}
	return nil
}

// Update implements tea.Model.
func (m *Shell) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout = layout.Compute(m.width, m.height)
		return m, nil

	case rowsMsg:
		if msg.err != nil {
			m.notice = noticeMsg{text: msg.err.Error(), isErr: true}
			return m, nil
		}
		m.rows = msg.rows
		m.clampSel()
		return m, nil

	case noticeMsg:
		m.notice = msg
		if m.attached() {
			return m, m.loadRows
		}
		return m, nil

	case specLoadedMsg:
		if msg.err != nil {
			m.notice = noticeMsg{text: msg.err.Error(), isErr: true}
			return m, nil
		}
		sv := &specView{f: msg.f, path: msg.path, content: msg.content, doc: spec.Parse(msg.content), cursor: 1}
		if m.spec != nil && m.spec.path == msg.path {
			// reload in place: keep mode, cursor, and scroll
			sv.annotate, sv.offset = m.spec.annotate, m.spec.offset
			sv.cursor = min(m.spec.cursor, len(sv.doc.Lines))
		}
		m.spec = sv
		return m, nil

	case diffLoadedMsg:
		if msg.err != nil {
			m.notice = noticeMsg{text: sanitize(msg.err.Error()), isErr: true}
			return m, nil
		}
		if msg.empty {
			m.notice = noticeMsg{text: string(msg.f.ID) + ": no changes in the worktree yet"}
			return m, nil
		}
		dv := newDiffView(msg.f, msg.diff, msg.anns)
		if m.diff != nil && m.diff.f.ID == msg.f.ID {
			// reload in place: keep mode, cursor, and scroll, clamped in
			// case the diff shrank (e.g. after a fix-up run).
			dv.annotate = m.diff.annotate
			dv.offset = min(m.diff.offset, max(len(dv.lines)-1, 0))
			dv.cursor = min(max(m.diff.cursor, 1), len(dv.lines))
		}
		m.diff = dv
		return m, nil

	case verifyResultMsg:
		if msg.err != nil {
			m.notice = noticeMsg{text: sanitize(msg.err.Error()), isErr: true}
			return m, nil
		}
		m.checks[msg.feature] = msg.results
		passed := 0
		for _, r := range msg.results {
			if r.OK {
				passed++
			}
		}
		m.notice = noticeMsg{
			text:  string(msg.feature) + " verify: " + strconv.Itoa(passed) + "/" + strconv.Itoa(len(msg.results)) + " passed",
			isErr: passed != len(msg.results),
		}
		return m, nil

	case engineEventMsg:
		cmd := m.handleEngineEvent(msg.ev)
		// engine events otherwise carry no payload the view needs — they
		// just signal "re-render from Snapshot" — so keep listening, plus
		// any automatic review-loop follow-up.
		return m, tea.Batch(m.listenEngine, cmd)

	case engineClosedMsg:
		// the agent backend shut down unexpectedly; drop the pane so the
		// user isn't left on a frozen chat, and say why.
		if m.chat != nil {
			m.chat = nil
			m.notice = noticeMsg{text: "agent backend stopped", isErr: true}
		}
		return m, nil

	case tea.KeyPressMsg:
		if consumed, cmd := m.Overlay.HandleKey(msg); consumed {
			return m, cmd
		}
		return m, m.handleKey(msg)
	}
	return m, nil
}

func (m *Shell) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if key == "ctrl+c" {
		return tea.Quit
	}
	// The chat pane captures all keys except the global quit.
	if m.chat != nil {
		return m.handleChatKey(msg)
	}
	if m.spec != nil {
		return m.handleSpecKey(key)
	}
	if m.diff != nil {
		return m.handleDiffKey(key)
	}
	switch key {
	case "q":
		return tea.Quit
	case "?":
		m.Overlay.Push(helpDialog{})
		return nil
	}
	if !m.attached() {
		return nil
	}
	switch key {
	case "tab":
		m.cycleAttention()
		return nil
	case "i":
		m.openInbox()
		return nil
	case "enter":
		if r, ok := m.selected(); ok {
			m.inbox.remove(r.F.ID)
			return m.attachOrRun(r.F)
		}
	case "p":
		if r, ok := m.selected(); ok {
			m.inbox.remove(r.F.ID)
			return m.pauseRun(r.F)
		}
	case "v":
		if r, ok := m.selected(); ok {
			return m.runChecks(r.F)
		}
	case "s":
		if r, ok := m.selected(); ok {
			return m.openSpec(r.F)
		}
	case "d":
		if r, ok := m.selected(); ok {
			return m.openDiff(r.F)
		}
	case "j", "down":
		m.moveSel(1)
	case "k", "up":
		m.moveSel(-1)
	case "n":
		m.Overlay.Push(newFeatureForm(m.profileNames, m.createFeature))
	case "g":
		if r, ok := m.selected(); ok {
			m.inbox.remove(r.F.ID)
			return m.advanceStage(r.F.ID)
		}
	case "b":
		if r, ok := m.selected(); ok {
			m.inbox.remove(r.F.ID)
			return m.bounceStage(r.F.ID)
		}
	case "r":
		if r, ok := m.selected(); ok {
			return m.rebaseFeature(r.F)
		}
	case "c":
		if r, ok := m.selected(); ok {
			if !r.Landed {
				m.notice = noticeMsg{text: string(r.F.ID) + " hasn't landed on main yet", isErr: true}
				return nil
			}
			f := r.F
			m.Overlay.Push(&confirmDialog{
				id:        "confirm-cleanup",
				question:  "clean up " + string(f.ID) + "?",
				detail:    "removes the worktree (incl. untracked files) and merged branch — keeps the record",
				onConfirm: func() tea.Cmd { return m.cleanupLanded(f) },
			})
		}
	case "x":
		if r, ok := m.selected(); ok {
			f := r.F
			m.Overlay.Push(&confirmDialog{
				id:        "confirm-delete",
				question:  "delete " + string(f.ID) + "?",
				detail:    f.Title + " — removes worktree, branch, and record",
				onConfirm: func() tea.Cmd { return m.deleteFeature(f.ID) },
			})
		}
	default:
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			m.jumpSel(int(key[0] - '0'))
		}
	}
	return nil
}

// selected returns the selected row, if any.
func (m *Shell) selected() (featureRow, bool) {
	if m.sel < 0 || m.sel >= len(m.rows) {
		return featureRow{}, false
	}
	return m.rows[m.sel], true
}

// moveSel moves the selection through the board's display order.
func (m *Shell) moveSel(delta int) {
	order := m.displayOrder()
	if len(order) == 0 {
		return
	}
	pos := 0
	for i, idx := range order {
		if idx == m.sel {
			pos = i
			break
		}
	}
	pos = (pos + delta + len(order)) % len(order)
	m.sel = order[pos]
}

// jumpSel selects the nth visible card (1-based), matching the numbers
// shown on the board.
func (m *Shell) jumpSel(n int) {
	order := m.displayOrder()
	if n >= 1 && n <= len(order) {
		m.sel = order[n-1]
	}
}

// displayOrder lists row indices in board display order (grouped by
// super-state).
func (m *Shell) displayOrder() []int {
	var order []int
	for _, super := range domain.SuperStates {
		for i, r := range m.rows {
			if r.F.Stage.SuperState() == super {
				order = append(order, i)
			}
		}
	}
	return order
}

func (m *Shell) clampSel() {
	if len(m.rows) == 0 {
		m.sel = 0
		return
	}
	if m.sel >= len(m.rows) {
		m.sel = len(m.rows) - 1
	}
	if m.sel < 0 {
		m.sel = 0
	}
}

// View implements tea.Model: compute the buffer, paint the panes, the
// status bar, then the dialog stack.
func (m *Shell) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.BackgroundColor = m.styles.Theme.BgBase
	v.WindowTitle = "gummi"

	if m.width <= 0 || m.height <= 0 {
		return v
	}
	canvas := uv.NewScreenBuffer(m.width, m.height)
	m.draw(&canvas)

	content := strings.ReplaceAll(canvas.Render(), "\r\n", "\n")
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	v.Content = strings.Join(lines, "\n")
	return v
}

func (m *Shell) draw(scr uv.Screen) {
	s := m.styles
	l := m.layout

	if l.KanbanVisible {
		uv.NewStyledString(m.boardView(l.Kanban.Dx())).Draw(scr, l.Kanban)
		sep := strings.TrimSuffix(strings.Repeat(s.Separator.Render("│")+"\n", l.Main.Dy()), "\n")
		uv.NewStyledString(sep).Draw(scr, uv.Rect(l.Main.Min.X, 0, 1, l.Main.Dy()))
	}
	main := m.mainView(max(l.Main.Dx()-3, 0), l.Main.Dy())
	mainArea := uv.Rect(l.Main.Min.X+2, l.Main.Min.Y, max(l.Main.Dx()-2, 0), l.Main.Dy())
	uv.NewStyledString(main).Draw(scr, mainArea)

	uv.NewStyledString(m.statusView(l.Status.Dx())).Draw(scr, l.Status)

	m.Overlay.Draw(scr, l.Area, s)
}

// attachOrRun handles `enter`: interactive stages open the chat pane;
// autonomous stages start (or observe) an autonomous run.
func (m *Shell) attachOrRun(f domain.Feature) tea.Cmd {
	if m.engine == nil {
		m.notice = noticeMsg{text: "no agent configured (set a model/provider to enable agents)", isErr: true}
		return nil
	}
	switch {
	case f.Stage == domain.StageBrainstorm || f.Stage == domain.StageSpec:
		return m.attachChat(f)
	case autonomousStage(f.Stage):
		return m.runStage(f)
	default:
		m.notice = noticeMsg{text: string(f.ID) + " is in " + string(f.Stage) + " — nothing to run", isErr: true}
		return nil
	}
}

// attachChat opens the interactive chat pane for a feature, starting
// (or reusing) its engine session.
func (m *Shell) attachChat(f domain.Feature) tea.Cmd {
	s, err := m.engine.Attach(context.Background(), f)
	if err != nil {
		m.notice = noticeMsg{text: err.Error(), isErr: true}
		return nil
	}
	m.chat = newChatPane(f.ID, s)
	return nil
}

// runStage enqueues an autonomous run for a feature's stage; the engine
// schedules and kicks it off. Activity streams into the dashboard;
// `p` pauses it.
func (m *Shell) runStage(f domain.Feature) tea.Cmd {
	if s := m.engine.Get(f.ID); s != nil {
		switch s.State() {
		case engine.StateRunning:
			m.notice = noticeMsg{text: string(f.ID) + " is already running"}
			return nil
		case engine.StateQueued:
			m.notice = noticeMsg{text: string(f.ID) + " is queued"}
			return nil
		}
	}
	if err := m.engine.Run(f); err != nil {
		m.notice = noticeMsg{text: err.Error(), isErr: true}
		return nil
	}
	m.notice = noticeMsg{text: string(f.ID) + " queued"}
	return nil
}

// pauseRun stops a feature's autonomous session, freeing its slot.
func (m *Shell) pauseRun(f domain.Feature) tea.Cmd {
	s := m.engine.Get(f.ID)
	if s == nil || s.Interactive {
		return nil
	}
	if err := m.engine.Pause(context.Background(), f.ID); err != nil {
		m.notice = noticeMsg{text: err.Error(), isErr: true}
		return nil
	}
	m.notice = noticeMsg{text: string(f.ID) + " paused"}
	return nil
}

// sessionFor returns the engine session bound to a feature, or nil.
func (m *Shell) sessionFor(id domain.FeatureID) *engine.Session {
	if m.engine == nil {
		return nil
	}
	return m.engine.Get(id)
}

// cycleAttention moves the selection to the next feature in the
// needs-attention queue (DESIGN §6: `tab` cycles the queue).
func (m *Shell) cycleAttention() {
	var cur domain.FeatureID
	if r, ok := m.selected(); ok {
		cur = r.F.ID
	}
	next := m.inbox.next(cur)
	if next == "" {
		return
	}
	for i, r := range m.rows {
		if r.F.ID == next {
			m.sel = i
			return
		}
	}
}

// openInbox shows the needs-attention overlay.
func (m *Shell) openInbox() {
	m.Overlay.Push(newInboxDialog(m.inbox.list(),
		func(id domain.FeatureID) tea.Cmd {
			m.inbox.remove(id)
			for i, r := range m.rows {
				if r.F.ID == id {
					m.sel = i
					break
				}
			}
			return nil
		},
		m.inbox.remove,
		m.topUpBudget,
	))
}

// topUpBudget releases a feature's reserve and resumes its exhausted
// stage (the "top up" action of a budget gate).
func (m *Shell) topUpBudget(id domain.FeatureID) tea.Cmd {
	m.inbox.remove(id)
	if m.engine == nil {
		return nil
	}
	return func() tea.Msg {
		if err := m.engine.TopUp(context.Background(), id); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: string(id) + " topped up — reserve released, resuming"}
	}
}

// autonomousStage reports whether a stage runs an autonomous agent
// (as opposed to interactive chat or no agent).
func autonomousStage(s domain.Stage) bool {
	switch s {
	case domain.StagePlan, domain.StageImplement, domain.StageReview, domain.StageVerify:
		return true
	default:
		return false
	}
}

// handleChatKey routes keys while the chat pane is open.
func (m *Shell) handleChatKey(msg tea.KeyPressMsg) tea.Cmd {
	detach, send, cmd := m.chat.handleKey(msg)
	if detach {
		// esc detaches; the engine session keeps running (DESIGN §6).
		m.chat = nil
		return nil
	}
	if send != "" {
		return m.sendChat(send)
	}
	return cmd
}

// sendChat delivers a user turn to the engine in a command. It captures
// the pane's session and engine at call time (not inside the goroutine,
// which would race the main loop) and refuses to send if that session
// is no longer the active one — the turn would otherwise land in the
// wrong feature's session.
func (m *Shell) sendChat(text string) tea.Cmd {
	eng, sess := m.engine, m.chat.session
	id := sess.Feature.ID
	return func() tea.Msg {
		if eng.Get(id) != sess {
			return noticeMsg{text: "chat session is no longer active", isErr: true}
		}
		if err := eng.Send(context.Background(), id, text); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return nil
	}
}

func (m *Shell) mainView(w, h int) string {
	if m.chat != nil {
		return m.chat.view(m.styles, w, h)
	}
	if m.spec != nil {
		return m.specViewRender(w, h)
	}
	if m.diff != nil {
		return m.diffViewRender(w, h)
	}
	if len(m.rows) > 0 {
		return m.dashboardView(w, h)
	}
	return logo.Splash(m.styles, m.version, w, h)
}

func (m *Shell) statusView(w int) string {
	pills := []statusbar.Pill{
		{Text: "gummi", Kind: statusbar.KindMode},
		{Text: m.boardCounts(), Kind: statusbar.KindNeutral},
	}
	if run := m.runCounts(); run != "" {
		pills = append(pills, statusbar.Pill{Text: run, Kind: statusbar.KindNeutral})
	}
	if n := m.inbox.len(); n > 0 {
		pills = append(pills, statusbar.Pill{Text: "✉ " + strconv.Itoa(n) + " need you", Kind: statusbar.KindAlert})
	}
	if m.notice.text != "" {
		kind := statusbar.KindNeutral
		if m.notice.isErr {
			kind = statusbar.KindAlert
		}
		pills = append(pills, statusbar.Pill{Text: m.notice.text, Kind: kind})
	}
	return statusbar.Render(m.styles, w, pills,
		[]statusbar.Hint{
			{Key: "n", Label: "new"},
			{Key: "g", Label: "advance"},
			{Key: "?", Label: "help"},
			{Key: "q", Label: "quit"},
		},
	)
}

// runCounts summarizes live agent sessions for the status bar
// (⬤ running · ◔ queued), empty when nothing is running.
func (m *Shell) runCounts() string {
	if m.engine == nil {
		return ""
	}
	var running, queued int
	for _, s := range m.engine.Sessions() {
		switch s.State() {
		case engine.StateRunning:
			running++
		case engine.StateQueued:
			queued++
		}
	}
	var parts []string
	if running > 0 {
		parts = append(parts, "⬤ "+strconv.Itoa(running)+" running")
	}
	if queued > 0 {
		parts = append(parts, "◔ "+strconv.Itoa(queued)+" queued")
	}
	return strings.Join(parts, " · ")
}
