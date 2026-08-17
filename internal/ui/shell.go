package ui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/notify"
	"github.com/morphis/gummi/internal/planround"
	"github.com/morphis/gummi/internal/reviewround"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/layout"
	"github.com/morphis/gummi/internal/ui/logo"
	"github.com/morphis/gummi/internal/ui/overlay"
	"github.com/morphis/gummi/internal/ui/statusbar"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/verify"
	"github.com/morphis/gummi/internal/workflow"
	"github.com/morphis/gummi/internal/worktree"
)

// SortMode selects how the board's todo column is ordered. It is an
// ephemeral, in-memory toggle (never persisted): SortCreation shows
// cards in creation order, SortSeverity ranks bugs by severity.
type SortMode int

const (
	SortCreation SortMode = iota
	SortSeverity
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

	rows      []featureRow
	sel       int
	sortMode  SortMode // todo-column ordering toggle (ephemeral, not persisted)
	notice    noticeMsg
	spec      *specView      // non-nil while the spec surface is open
	diff      *diffView      // non-nil while the diff surface is open
	ingest    *ingestView    // non-nil while the ingest review surface is open
	ingestRun *ingestRunView // non-nil while an ingest pass is decomposing (one at a time)

	bugIngest    *bugIngestView // non-nil while the bug-import review surface is open
	bugIngesting bool           // a bug import is fetching (one at a time)

	mergePrep bool // a squash merge's preconditions are being checked (one at a time)

	// agent orchestration (nil engine means no agent wired)
	engine       *engine.Engine
	chat         *chatPane // non-nil while attached to an interactive session
	inbox        *inbox    // needs-attention queue
	checks       map[domain.FeatureID][]verify.Result
	baselining   map[domain.FeatureID]bool // a baseline check run is in flight
	reviewRounds map[domain.FeatureID]int  // automatic review→fix→review counter
	planRounds   map[domain.FeatureID]int  // automatic plan→critique→replan counter
	planStore    planround.Store           // persistence seam for planRounds (defaults to store)
	reviewStore  reviewround.Store         // persistence seam for reviewRounds (defaults to store)
	profileNames []string                  // profile names for the new-feature form
	envelope     int                       // default spend-plan envelope for new features (0 = none)
	notifier     *notify.Notifier          // bell/desktop hook for needs-attention events

	// Copilot quota hint (copilotquota.go): the latest reading shown as
	// a status-bar pill, its enable flag, and the gh seam for tests.
	copilot       copilotQuota
	copilotHint   bool
	ghCopilotUser func(context.Context) ([]byte, error)

	// shared activity spinner (spinner.go): frame is the current cycle
	// position; spinning guards the single live tick loop.
	frame    int
	spinning bool

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
		baselining:   map[domain.FeatureID]bool{},
		reviewRounds: map[domain.FeatureID]int{},
		planRounds:   map[domain.FeatureID]int{},
		copilotHint:  true,
	}
}

// Attach wires the shell to a workspace: its store, worktree manager,
// and paths. Must be called before Run for board functionality.
func (m *Shell) Attach(store *state.Store, wt *worktree.Manager, ws state.Workspace) {
	m.store, m.wt, m.ws = store, wt, ws
	// the plan-rounds persistence seam defaults to the real store; tests
	// may swap in a failing store to prove the fail-closed path.
	m.planStore = store
	m.reviewStore = store
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

// SetCopilotHint toggles the status-bar Copilot quota pill (on by
// default; it hides itself anyway when gh or a quota is absent).
func (m *Shell) SetCopilotHint(on bool) { m.copilotHint = on }

// reconstructInbox rebuilds the needs-attention queue from the engine's
// restored sessions at startup — the queue is otherwise in-memory and a
// restart drops parked gates, budget stops, and failures (a parked budget
// item's only top-up path is the inbox, so losing it stranded the card).
// Derived from durable session state: a failed run (paused with a stored
// error) → failure; a settled autonomous stage → a budget park if its
// activity recorded an exhaustion, else a review-&-advance gate. Live
// events refine these as they arrive; this only seeds what a restart lost.
func (m *Shell) reconstructInbox() {
	if m.engine == nil {
		return
	}
	for id, s := range m.engine.Sessions() {
		snap := s.Snapshot()
		switch {
		case snap.Err != nil:
			m.inbox.add(id, attnFailure, sanitize(snap.Err.Error()))
		case snap.State != engine.StateDone || !autonomousStage(snap.Feature.Stage):
			// running/queued/interactive sessions raise their own items live
			continue
		case exhaustedActivity(snap.Activity):
			m.inbox.add(id, attnBudget, string(snap.Feature.Stage)+" hit its budget — u top up or x park")
		default:
			m.inbox.add(id, attnGate, string(snap.Feature.Stage)+" finished — review & advance")
		}
	}
}

// exhaustedActivity reports whether a restored session's activity feed
// records a budget stop (the marker exhaust writes), so the reconstructed
// item is a budget park rather than a plain gate.
func exhaustedActivity(activity []string) bool {
	for _, a := range activity {
		if strings.Contains(a, "budget exhausted") || strings.Contains(a, "budget reached") {
			return true
		}
	}
	return false
}

// raiseAttention adds a needs-attention item and, when it is a new alert
// (not an update of an existing one), fires the notification hook.
func (m *Shell) raiseAttention(id domain.FeatureID, kind attnKind, text string) {
	if m.inbox.add(id, kind, text) {
		m.notifier.Alert(string(id) + ": " + text)
	}
}

// raiseEscalation is raiseAttention for gates an automatic loop gave up
// on (round cap, unclear verdict): the item carries the escalation flag
// so surfaces tint it as needs-you rather than finished-clean. Every
// escalation is a gate — the loop stopped at a decision only the human
// can take.
func (m *Shell) raiseEscalation(id domain.FeatureID, text string) {
	if m.inbox.addEscalated(id, attnGate, text) {
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
		// rebuild the needs-attention queue from the sessions the engine
		// restored, so a parked budget gate (and its top-up path) survives a
		// restart instead of vanishing until re-triggered.
		m.reconstructInbox()
		cmds = append(cmds, m.listenEngineCmd())
	}
	if m.copilotHint {
		cmds = append(cmds, m.fetchCopilotQuota())
	}
	return tea.Batch(cmds...)
}

// listenEngineCmd wraps the blocking engine-listener as a subscription so
// synchronous test scaffolds know to skip it rather than wait on the
// never-returning channel read.
func (m *Shell) listenEngineCmd() tea.Cmd {
	return subscription(m.listenEngine)
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
			// a one-shot pass not bound to a feature (ingest) has no card
			// to queue behind; the notice alone carries it
			if ev.Feature != "" {
				m.raiseAttention(ev.Feature, attnFailure, text)
			}
		}
	case engine.EventExhausted:
		// budget exhausted mid-stage: raise a gate, don't auto-continue.
		// Clear the persisted plan-rounds count first, and only zero the
		// in-memory counter once the write succeeds — a failed write must
		// not re-grant budget on resume (the next entry rehydrates the
		// persisted, nonzero value).
		if err := planround.Reset(context.Background(), m.planStore, ev.Feature); err != nil {
			m.notice = noticeMsg{text: sanitize(err.Error()), isErr: true}
			m.raiseAttention(ev.Feature, attnFailure, sanitize(err.Error()))
		} else {
			m.planRounds[ev.Feature] = 0
		}
		// same write-through for the review-loop counter: a failed write
		// must not lose the burned rounds recorded in the store.
		if err := reviewround.Reset(context.Background(), m.reviewStore, ev.Feature); err != nil {
			m.notice = noticeMsg{text: sanitize(err.Error()), isErr: true}
			m.raiseAttention(ev.Feature, attnFailure, sanitize(err.Error()))
		} else {
			m.reviewRounds[ev.Feature] = 0
		}
		if ev.Committed {
			// wrap-up exhaustion: the stage's work is committed, so this
			// reads as ready-to-advance with top-up as the alternative —
			// not lost work.
			m.raiseAttention(ev.Feature, attnBudget, string(ev.Stage)+" reached its budget with work committed — g advance, or u top up for more")
			m.notice = noticeMsg{text: string(ev.Feature) + ": " + string(ev.Stage) + " reached its budget (work committed)"}
		} else {
			m.raiseAttention(ev.Feature, attnBudget, string(ev.Stage)+" hit its budget — u top up or x park")
			m.notice = noticeMsg{text: string(ev.Feature) + " budget exhausted at " + string(ev.Stage), isErr: true}
		}
	case engine.EventQuestion:
		// the agent asked something. When you're attached to this feature
		// the inline picker already shows it; otherwise queue it so you can
		// jump to the picker from the needs-attention inbox.
		if m.chat != nil && m.chat.feature == ev.Feature {
			return nil
		}
		q := "the agent has a question — attach to answer"
		if s := m.engine.Get(ev.Feature); s != nil {
			if a := s.Snapshot().PendingAsk; a != nil {
				q = "asks: " + a.Question
			}
		}
		m.raiseAttention(ev.Feature, attnQuestion, q)
	case engine.EventAnnotations:
		// the agent resolved a diff comment — refresh an open diff surface
		// so its open-count and gutter markers burn down live
		if m.diff != nil && m.diff.f.ID == ev.Feature {
			return m.reloadDiff()
		}
	case engine.EventTripwire:
		// the agent wrote into the main checkout — a hard stop with no
		// top-up path, so it shares the failure attention lane.
		text := "wrote to main checkout — " + strings.Join(ev.DirtyPaths, ", ") + " (resolve then re-run)"
		m.raiseAttention(ev.Feature, attnFailure, text)
		m.notice = noticeMsg{text: text, isErr: true}
	case engine.EventIdle:
		s := m.engine.Get(ev.Feature)
		if s == nil || s.Interactive || s.State() != engine.StateDone {
			return nil
		}
		// a finished rebase-resolve session is judged by the git state it
		// left, never by the verdict loop of the stage it borrowed.
		if s.Snapshot().Rebase {
			return m.judgeRebase(ev.Feature)
		}
		// review/implement completions may drive the automatic loop;
		// anything the loop doesn't consume becomes a generic gate item.
		if handled, cmd := m.onAutonomousDone(ev.Feature, ev.Stage); handled {
			return cmd
		}
		m.raiseAttention(ev.Feature, attnGate, string(ev.Stage)+" finished — review & advance")
		// the session may have edited the artifact or committed; reload so
		// the gate's row state (landed, open-comment counts) is fresh
		return m.loadRows
	}
	return nil
}

// Update implements tea.Model. It delegates to update, then keeps the
// shared spinner clock alive: while anything on screen animates exactly
// one tick loop runs, and it winds down on the first tick after the
// last activity stops.
func (m *Shell) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(spinnerTickMsg); ok {
		if !m.spinnerActive() {
			m.spinning = false
			return m, nil
		}
		m.frame++
		return m, spinnerTick()
	}
	model, cmd := m.update(msg)
	if !m.spinning && m.spinnerActive() {
		m.spinning = true
		cmd = tea.Batch(cmd, spinnerTick())
	}
	return model, cmd
}

func (m *Shell) update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		// an outcome-driven clear: the action that produced this notice
		// succeeded, so drop the attention item it resolved (see
		// noticeMsg.clearInbox). It runs on the Update goroutine, never
		// inside a command.
		if msg.clearInbox != "" {
			m.inbox.remove(msg.clearInbox)
		}
		m.notice = msg
		// a reload is opt-in: only a notice emitted by a command that
		// mutated row-rendered state carries the flag (see noticeMsg).
		// A routine status notice ("queued", "paused", a non-mutating
		// error) never triggers a board reload.
		if msg.reload && m.attached() {
			return m, m.loadRows
		}
		return m, nil

	case copilotQuotaMsg:
		m.copilot = msg.quota
		if msg.retry {
			return m, copilotQuotaTick()
		}
		return m, nil

	case copilotQuotaTickMsg:
		return m, m.fetchCopilotQuota()

	case mergeReadyMsg:
		m.mergePrep = false
		if msg.err != nil {
			m.notice = noticeMsg{text: sanitize(msg.err.Error()), isErr: true}
			return m, nil
		}
		m.notice = noticeMsg{}
		if msg.warn != "" {
			// non-blocking caution (provenance in branch commits): shown
			// while the commit-message dialog collects the landing message
			m.notice = noticeMsg{text: sanitize(msg.warn), isErr: true}
		}
		f, thenDone := msg.f, msg.thenDone
		d := newCommitMsgDialog(f, func(message string) tea.Cmd {
			return m.squashMergeFeature(f, message, thenDone)
		}, func(dctx context.Context, feature domain.Feature) (string, error) {
			// best-effort: a nil engine or any drafting failure yields an
			// empty draft, never a hard error or a delayed dialog; dctx
			// lets esc cancel an in-flight pass.
			if m.engine == nil {
				return "", nil
			}
			return m.engine.DraftCommitMessage(dctx, feature)
		})
		m.Overlay.Push(d)
		// start the draft pass off the render loop; the dialog is already
		// open and editable, and the draft fills only while unmodified.
		return m, d.startDraft()

	case commitDraftMsg:
		// a late reply from a closed dialog (esc) or a stale pass (ctrl+r
		// regenerated) is dropped; apply only while the dialog is live.
		if d, ok := m.Overlay.Top().(*commitMsgDialog); ok && d.feature == msg.f {
			d.apply(msg)
		}
		// Record the outcome durably on the feature so a failed draft
		// survives the dialog and later inspection still sees it: the
		// reason on a failed pass, cleared on a successful draft. The write
		// runs in a command — never in Update (see the no-IO-in-Update
		// contract above).
		reason := ""
		if msg.draft == "" && msg.reason != "" {
			reason = msg.reason
		}
		return m, m.recordCommitDraftFail(msg.f, reason)

	case commitDraftPersistedMsg:
		// the durable note is written; reflect it on the feature's own
		// dashboard row in place. It is row metadata only — no git state
		// changed, so the board list has nothing to re-walk (no reload).
		for i := range m.rows {
			if m.rows[i].F.ID == msg.id {
				m.rows[i].F.CommitDraftFail = msg.reason
				break
			}
		}
		return m, nil

	case mergeThenDoneMsg:
		// the verify→done gate routes through the merge flow: collect the
		// user's commit message, then land + transition on ctrl+s.
		if m.mergePrep {
			m.notice = noticeMsg{text: "already preparing a merge — wait for it", isErr: true}
			return m, nil
		}
		m.mergePrep = true
		m.inbox.remove(msg.f.ID)
		m.notice = noticeMsg{text: string(msg.f.ID) + ": landing on main…"}
		return m, m.prepareMerge(msg.f, true)

	case rebaseConflictMsg:
		m.offerAgentRebase(msg)
		return m, nil

	case rebaseSettledMsg:
		return m, m.rebaseSettled(msg)

	case worktreeEnteredMsg:
		// show the transition notice, reload, and run the background
		// one-shot passes: check discovery and/or the envelope estimate.
		// The approval succeeded, so its attention item is resolved here.
		m.inbox.remove(msg.id)
		m.notice = noticeMsg{text: msg.note}
		cmds := []tea.Cmd{m.loadRows}
		if msg.discover {
			cmds = append(cmds, m.discoverChecks(msg.id))
		}
		if msg.estimate {
			cmds = append(cmds, m.scribeEstimate(msg.id))
		}
		return m, tea.Batch(cmds...)

	case checksDiscoveredMsg:
		// discovery settled (wrote a block, found one already there, or
		// failed): baseline whatever block the artifact now carries.
		if msg.n > 0 {
			m.notice = noticeMsg{text: fmt.Sprintf("%s: discovered %d repo check(s) into the %s",
				msg.id, msg.n, artifactNoun(msg.id.Kind()))}
		}
		m.baselining[msg.id] = true
		return m, tea.Batch(m.baselineChecks(msg.id), spinnerTick())

	case baselineDoneMsg:
		delete(m.baselining, msg.id)
		switch {
		case msg.err != nil:
			m.notice = noticeMsg{text: string(msg.id) + ": gummi-checks baseline failed — " + sanitize(msg.err.Error()), isErr: true}
		case len(msg.results) > 0:
			// the results live in the store (BaselineFails via loadRows), not
			// in m.checks — that map is manual verify runs, and a baseline
			// bleeding into it would mislabel the dashboard and the
			// failed-check guidance at verify.
			m.notice = baselineNotice(msg.id, msg.results)
		}
		return m, m.loadRows

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
			dv.setCursor(m.diff.cursor)
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

	case ingestStepMsg:
		// live progress from the running pass; keep listening on the same
		// stream (a finished/discarded run just drains silently).
		if m.ingestRun != nil {
			m.ingestRun.apply(msg.step)
		}
		return m, listenIngestSteps(msg.ch)

	case ingestStreamClosedMsg:
		return m, nil

	case ingestLoadedMsg:
		m.ingestRun = nil
		if msg.err != nil {
			m.notice = noticeMsg{text: "ingest: " + sanitize(msg.err.Error()), isErr: true}
			return m, nil
		}
		// the user explicitly asked for this decomposition and has been
		// waiting on it, so it takes the foreground: clear any pane opened
		// meanwhile (chat detaches, its session keeps running) so the review
		// surface is never installed hidden behind another view.
		m.chat, m.spec, m.diff = nil, nil, nil
		m.ingest = newIngestView(msg.res, msg.profile, msg.envelope)
		m.notice = noticeMsg{text: "proposed " + strconv.Itoa(len(msg.res.Proposals)) + " feature(s) — review & approve"}
		return m, nil

	case bugIngestLoadedMsg:
		m.bugIngesting = false
		if msg.err != nil {
			m.notice = noticeMsg{text: "import: " + sanitize(msg.err.Error()), isErr: true}
			return m, nil
		}
		if len(msg.res.Proposals) == 0 {
			extra := ""
			if n := len(msg.res.Skipped); n > 0 {
				extra = fmt.Sprintf(" (%d already on the board)", n)
			}
			m.notice = noticeMsg{text: "no new bugs to import" + extra}
			return m, nil
		}
		m.chat, m.spec, m.diff, m.ingest = nil, nil, nil, nil
		m.bugIngest = newBugIngestView(msg.res, msg.profile, msg.envelope)
		m.notice = noticeMsg{text: "fetched " + strconv.Itoa(len(msg.res.Proposals)) + " bug(s) — review & approve"}
		return m, nil

	case engineEventMsg:
		cmd := m.handleEngineEvent(msg.ev)
		// engine events otherwise carry no payload the view needs — they
		// just signal "re-render from Snapshot" — so keep listening, plus
		// any automatic review-loop follow-up.
		return m, tea.Batch(m.listenEngineCmd(), cmd)

	case engineClosedMsg:
		// the agent backend shut down unexpectedly; drop the pane so the
		// user isn't left on a frozen chat, and say why.
		if m.chat != nil {
			m.chat = nil
			m.notice = noticeMsg{text: "agent backend stopped", isErr: true}
		}
		return m, nil

	case chatAttachedMsg:
		// the attach ran in a command (spawning the backend can take
		// seconds); open the pane now, on the Update goroutine.
		if msg.err != nil {
			m.notice = noticeMsg{text: sanitize(msg.err.Error()), isErr: true}
			return m, nil
		}
		m.chat = newChatPane(msg.feature.ID, msg.session)
		m.inbox.remove(msg.feature.ID)
		return m, nil

	case tea.KeyPressMsg:
		if consumed, cmd := m.Overlay.HandleKey(msg); consumed {
			return m, cmd
		}
		return m, m.handleKey(msg)

	case tea.PasteMsg:
		if consumed, cmd := m.Overlay.HandlePaste(msg); consumed {
			return m, cmd
		}
		return m, m.handlePaste(msg)
	}
	return m, nil
}

// handlePaste routes bracketed-paste text to whichever pane input is
// editing; a paste with no input focused is dropped.
func (m *Shell) handlePaste(msg tea.PasteMsg) tea.Cmd {
	if m.chat != nil {
		return m.chat.handlePaste(msg)
	}
	if bv := m.bugIngest; bv != nil && bv.filtering {
		bv.filter, _ = bv.filter.Update(msg)
		bv.setCursor(bv.cursor) // reclamp: the visible set may have shrunk
	}
	return nil
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
	if m.ingest != nil {
		return m.handleIngestKey(key)
	}
	if m.bugIngest != nil {
		return m.handleBugIngestKey(msg)
	}
	switch key {
	case "q":
		// quitting with autonomous work live stops sessions mid-turn,
		// discarding the in-flight turn and its spend and leaving the
		// stage uncommitted on disk; ask first so the user who means it
		// can still get out (the confirm dialog's way-through). Idle quit
		// stays a single keypress.
		if live := m.liveSessions(); len(live) > 0 {
			m.Overlay.Push(&confirmDialog{
				id:       "confirm-quit",
				question: "quit with live sessions " + strings.Join(live, ", ") + "?",
				detail:   "quitting stops them mid-turn — the in-flight turn and its spend are discarded and the work is left uncommitted on disk (recoverable next run)",
				onConfirm: func() tea.Cmd {
					return tea.Quit
				},
			})
			return nil
		}
		return tea.Quit
	case "?":
		m.Overlay.Push(m.helpOverlay())
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
			m.clearTransientNotice()
			return m.attachOrRun(r.F)
		}
	case "p":
		if r, ok := m.selected(); ok {
			return m.pauseRun(r.F)
		}
	case "v":
		if r, ok := m.selected(); ok {
			return m.runChecks(r.F)
		}
	case "t":
		if r, ok := m.selected(); ok {
			m.clearTransientNotice()
			m.openTranscript(r.F)
		}
	case "s":
		if r, ok := m.selected(); ok {
			m.clearTransientNotice()
			return m.openSpec(r.F)
		}
	case "d":
		if r, ok := m.selected(); ok {
			m.clearTransientNotice()
			return m.openDiff(r.F)
		}
	case "a":
		if r, ok := m.selected(); ok {
			return m.attachRaw(r.F)
		}
	case "j", "down":
		m.moveSel(1)
	case "k", "up":
		m.moveSel(-1)
	case "pgup":
		if order := m.displayOrder(m.sortMode); len(order) > 0 {
			m.sel = order[0]
		}
	case "pgdown":
		if order := m.displayOrder(m.sortMode); len(order) > 0 {
			m.sel = order[len(order)-1]
		}
	case "n":
		m.Overlay.Push(newFeatureForm(m.profileNames, m.envelope, m.createFeature))
	case "B":
		m.Overlay.Push(newBugForm(m.profileNames, m.envelope, m.createBug))
	case "S":
		if m.sortMode == SortSeverity {
			m.sortMode = SortCreation
			m.notice = noticeMsg{text: "todo: creation order"}
		} else {
			m.sortMode = SortSeverity
			m.notice = noticeMsg{text: "todo: by severity"}
		}
	case "I":
		if m.engine == nil {
			m.notice = noticeMsg{text: "no agent configured — ingestion needs one", isErr: true}
			return nil
		}
		if m.ingestRun != nil {
			// one pass at a time; I brings a backgrounded feed forward
			m.ingestRun.hidden = false
			m.notice = noticeMsg{text: "an ingest is already decomposing — showing its progress"}
			return nil
		}
		m.Overlay.Push(newIngestForm(m.profileNames, m.startIngest))
	case "esc":
		if m.ingestRun != nil && !m.ingestRun.hidden {
			// background the feed; the pass keeps running and the review
			// surface still takes the foreground when it lands.
			m.ingestRun.hidden = true
		}
	case "G":
		if m.engine == nil {
			m.notice = noticeMsg{text: "no agent configured — bug import needs the engine", isErr: true}
			return nil
		}
		if m.bugIngesting {
			m.notice = noticeMsg{text: "an import is already running — wait for it", isErr: true}
			return nil
		}
		m.Overlay.Push(newBugIngestForm(m.profileNames, m.startBugIngest))
	case "g":
		if r, ok := m.selected(); ok {
			return m.advanceStage(r.F.ID)
		}
	case "b":
		if r, ok := m.selected(); ok {
			return m.bounceStage(r.F.ID)
		}
	case "u":
		if r, ok := m.selected(); ok {
			m.Overlay.Push(newEnvelopeDialog(r.F, func(to int) tea.Cmd {
				return m.setEnvelope(r.F.ID, to)
			}))
		}
	case "P":
		if r, ok := m.selected(); ok {
			return m.routeViaPlan(r.F.ID)
		}
	case "r":
		if r, ok := m.selected(); ok {
			return m.rebaseFeature(r.F)
		}
	case "m":
		if r, ok := m.selected(); ok {
			if !r.HasWorktree {
				m.notice = noticeMsg{text: string(r.F.ID) + " has no worktree yet (created at spec approval)", isErr: true}
				return nil
			}
			if r.Landed {
				m.notice = noticeMsg{text: string(r.F.ID) + " already landed on main — press c to clean up", isErr: true}
				return nil
			}
			if m.mergePrep {
				m.notice = noticeMsg{text: "already preparing a merge — wait for it", isErr: true}
				return nil
			}
			m.mergePrep = true
			m.notice = noticeMsg{text: string(r.F.ID) + ": preparing merge…"}
			return m.prepareMerge(r.F, false)
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
	case "y":
		if r, ok := m.selected(); ok {
			f := r.F
			m.Overlay.Push(&confirmDialog{
				id:        "confirm-duplicate",
				question:  "duplicate " + string(f.ID) + "?",
				detail:    f.Title + " — fresh copy in todo (same skips, profile, envelope); this card stays",
				onConfirm: func() tea.Cmd { return m.duplicateFeature(f.ID) },
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

// clearTransientNotice drops a routine status notice on a view change so
// stale text (a lingering "critiquing", a "queued") doesn't follow the
// user into an unrelated surface. Error notices are kept — they carry
// something the user still needs to read (and long ones show in the band).
func (m *Shell) clearTransientNotice() {
	if !m.notice.isErr {
		m.notice = noticeMsg{}
	}
}

// selected returns the selected row, if any.
func (m *Shell) selected() (featureRow, bool) {
	if m.sel < 0 || m.sel >= len(m.rows) {
		return featureRow{}, false
	}
	return m.rows[m.sel], true
}

// mainPage is the page-key scroll step for main-pane surfaces: most of
// the body (the pane minus its header rows), with a line of overlap.
func (m *Shell) mainPage() int {
	return max(m.layout.Main.Dy()-5, 5)
}

// moveSel moves the selection through the board's display order.
func (m *Shell) moveSel(delta int) {
	order := m.displayOrder(m.sortMode)
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
	order := m.displayOrder(m.sortMode)
	if n >= 1 && n <= len(order) {
		m.sel = order[n-1]
	}
}

// displayOrder lists row indices in board display order (grouped by
// super-state). With sort == SortSeverity the todo column is ordered by
// severity (critical first) with creation time as a stable tiebreaker;
// every other column keeps chronological order regardless.
func (m *Shell) displayOrder(mode SortMode) []int {
	var order []int
	for _, super := range domain.SuperStates {
		var idxs []int
		for i, r := range m.rows {
			if r.F.Stage.SuperState() == super {
				idxs = append(idxs, i)
			}
		}
		if super == domain.SuperTodo && mode == SortSeverity {
			sort.SliceStable(idxs, func(a, b int) bool {
				ra, rb := severityRank(m.rows[idxs[a]].F.Severity), severityRank(m.rows[idxs[b]].F.Severity)
				if ra != rb {
					return ra > rb
				}
				return m.rows[idxs[a]].F.CreatedAt.Before(m.rows[idxs[b]].F.CreatedAt)
			})
		}
		order = append(order, idxs...)
	}
	return order
}

// severityRank maps a severity to its sort rank: critical ranks highest
// (4), an unclassified (empty) severity ranks lowest (0).
func severityRank(sev domain.Severity) int {
	switch sev {
	case domain.SeverityCritical:
		return 4
	case domain.SeverityHigh:
		return 3
	case domain.SeverityMedium:
		return 2
	case domain.SeverityLow:
		return 1
	default:
		return 0
	}
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
	// a long error/remedy is wrapped into a band above the status bar
	// rather than truncated into a one-line pill ("set permiss…"); it
	// borrows the bottom rows of the main pane. Short notices stay pills.
	band := m.noticeBand(max(l.Main.Dx()-3, 0))
	mainH := l.Main.Dy()
	if len(band) > 0 {
		mainH = max(mainH-len(band)-1, 0)
	}
	main := m.mainView(max(l.Main.Dx()-3, 0), mainH)
	mainArea := uv.Rect(l.Main.Min.X+2, l.Main.Min.Y, max(l.Main.Dx()-2, 0), mainH)
	uv.NewStyledString(main).Draw(scr, mainArea)

	if len(band) > 0 {
		y := l.Status.Min.Y - len(band)
		uv.NewStyledString(strings.Join(band, "\n")).
			Draw(scr, uv.Rect(l.Main.Min.X+2, y, max(l.Main.Dx()-2, 0), len(band)))
	}

	uv.NewStyledString(m.statusView(l.Status.Dx())).Draw(scr, l.Status)

	m.Overlay.Draw(scr, l.Area, s)
}

// noticeThreshold is the notice length above which it moves from a
// one-line status pill to the wrappable band (a truncated pill drops the
// tail of a multi-step remedy, e.g. "set permissions: allow-all in …").
const noticeThreshold = 48

// noticeBand renders a long error notice as wrapped lines for the band
// above the status bar, or nil when the notice is short enough to ride as
// a status pill. Only error/remedy notices get the band — routine status
// stays a quiet pill.
func (m *Shell) noticeBand(w int) []string {
	if m.notice.text == "" || !m.notice.isErr || len(m.notice.text) <= noticeThreshold || w < 8 {
		return nil
	}
	wrapped := wrapText(sanitize(m.notice.text), w)
	var out []string
	for _, l := range strings.Split(wrapped, "\n") {
		out = append(out, m.styles.Error.Render(l))
	}
	return out
}

// noticeInBand reports whether the current notice is being shown in the
// band (so statusView omits its pill and doesn't double it).
func (m *Shell) noticeInBand() bool {
	return m.notice.text != "" && m.notice.isErr && len(m.notice.text) > noticeThreshold
}

// attachOrRun handles `enter`: interactive stages open the chat pane;
// autonomous stages start (or observe) an autonomous run.
func (m *Shell) attachOrRun(f domain.Feature) tea.Cmd {
	if m.engine == nil {
		m.notice = noticeMsg{text: "no agent configured (set a model/provider to enable agents)", isErr: true}
		return nil
	}
	switch {
	case workflow.Interactive(f.Stage):
		// brainstorm/spec for features, triage/diagnose for bugs
		return m.attachChat(f)
	case autonomousStage(f.Stage):
		return m.runStage(f)
	default:
		m.notice = noticeMsg{text: string(f.ID) + " is in " + string(f.Stage) + " — nothing to run", isErr: true}
		return nil
	}
}

// attachChat opens the interactive chat pane for a feature, starting
// (or reusing) its engine session. Attach spawns the agent backend, which
// can take seconds, so it runs in a command (never in Update — see the
// no-IO-in-Update contract above); the pane opens when chatAttachedMsg
// lands.
func (m *Shell) attachChat(f domain.Feature) tea.Cmd {
	return func() tea.Msg {
		s, err := m.engine.Attach(context.Background(), f)
		return chatAttachedMsg{feature: f, session: s, err: err}
	}
}

// seedPlanRounds hydrates the in-memory plan-critique round counter from
// the store on plan-stage entry, so a resumed plan resumes with the rounds
// already burned instead of a fresh two-round budget. A failed read returns
// the error and leaves the fast-path map untouched; the caller aborts
// dispatch rather than proceeding on a guessed-zero count.
func (m *Shell) seedPlanRounds(f domain.Feature) error {
	n, err := planround.Load(context.Background(), m.planStore, f.ID)
	if err != nil {
		return err
	}
	m.planRounds[f.ID] = n
	return nil
}

// seedReviewRounds hydrates the in-memory review→fix round counter from
// the store on review-loop entry, so a resumed (or relaunched) loop
// resumes with the rounds already burned instead of a fresh review budget.
// A failed read returns the error and leaves the fast-path map untouched;
// the caller aborts dispatch rather than proceeding on a guessed-zero count.
func (m *Shell) seedReviewRounds(f domain.Feature) error {
	n, err := reviewround.Load(context.Background(), m.reviewStore, f.ID)
	if err != nil {
		return err
	}
	m.reviewRounds[f.ID] = n
	return nil
}

// runStage enqueues an autonomous run for a feature's stage; the engine
// schedules and kicks it off. Activity streams into the dashboard;
// `p` pauses it. On an already-running session, enter attaches the chat
// pane as an observer: the full scrollable transcript, with steering
// via the input (esc detaches, the run keeps going).
func (m *Shell) runStage(f domain.Feature) tea.Cmd {
	// entering the plan stage hydrates the loop's round counter from the
	// store, so a resumed plan resumes with the rounds already burned. A
	// failed read aborts dispatch rather than guessing at a fresh budget.
	if f.Stage == domain.StagePlan {
		if err := m.seedPlanRounds(f); err != nil {
			m.notice = noticeMsg{text: sanitize(err.Error()), isErr: true}
			m.raiseAttention(f.ID, attnFailure, sanitize(err.Error()))
			return nil
		}
	}
	// the review loop spans review → work(fix) → review, and any of those
	// can be the resume landing point, so seed the counter on each of them.
	if f.Stage == domain.StageReview || f.Stage == domain.StageImplement || f.Stage == domain.StageFix {
		if err := m.seedReviewRounds(f); err != nil {
			m.notice = noticeMsg{text: sanitize(err.Error()), isErr: true}
			m.raiseAttention(f.ID, attnFailure, sanitize(err.Error()))
			return nil
		}
	}
	if s := m.engine.Get(f.ID); s != nil {
		switch s.State() {
		case engine.StateRunning:
			m.chat = newChatPane(f.ID, s)
			return nil
		case engine.StateQueued:
			m.notice = noticeMsg{text: string(f.ID) + " is queued"}
			return nil
		case engine.StateDone:
			// A finished plan session resumes the loop at its position
			// instead of re-running the plan writer: a finished writer
			// means the (possibly revised) plan is already on disk, so the
			// next leg is the critique; a finished critique means the loop
			// is awaiting the judge's replan-or-approve decision. Other
			// stages just re-run (status quo).
			if f.Stage == domain.StagePlan {
				if s.Snapshot().Critique {
					return m.onPlanDone(f.ID)
				}
				return m.planStep(f.ID, true, "resuming plan critique (plan already written)")
			}
		case engine.StatePaused:
			// An interrupted plan critique resumes as a critique: the plan
			// is already written, so restarting the plan writer would burn
			// a full plan pass to redo finished work. A mid-flight writer
			// still falls through to engine.Run (status-quo restart).
			if f.Stage == domain.StagePlan && s.Snapshot().Critique {
				return func() tea.Msg {
					if err := m.engine.RunCritique(f, ""); err != nil {
						return noticeMsg{text: sanitize(err.Error()), isErr: true}
					}
					return noticeMsg{text: string(f.ID) + " resuming plan critique (plan already written)", clearInbox: f.ID}
				}
			}
		}
	}
	// Run schedules and spawns the backend synchronously; do it in a command
	// so a slow agent launch can't freeze the TUI.
	return func() tea.Msg {
		if err := m.engine.Run(f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: string(f.ID) + " queued", clearInbox: f.ID}
	}
}

// openTranscript attaches the chat pane to a feature's existing session
// in whatever state it is — running, done, paused — so the full
// scrollable transcript (tool calls with captured outputs, messages) can
// be read after the fact, e.g. to see why a verify run failed. Unlike
// enter it never starts or re-runs anything.
func (m *Shell) openTranscript(f domain.Feature) {
	s := m.sessionFor(f.ID)
	if s == nil {
		m.notice = noticeMsg{text: string(f.ID) + " has no session transcript", isErr: true}
		return
	}
	m.chat = newChatPane(f.ID, s)
}

// pauseRun stops a feature's autonomous session, freeing its slot.
func (m *Shell) pauseRun(f domain.Feature) tea.Cmd {
	s := m.engine.Get(f.ID)
	if s == nil || s.Interactive {
		return nil
	}
	// Pause interrupts the agent (IPC/network); run it in a command.
	return func() tea.Msg {
		if err := m.engine.Pause(context.Background(), f.ID); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{text: string(f.ID) + " paused", clearInbox: f.ID}
	}
}

// sessionFor returns the engine session bound to a feature, or nil.
func (m *Shell) sessionFor(id domain.FeatureID) *engine.Session {
	if m.engine == nil {
		return nil
	}
	return m.engine.Get(id)
}

// liveSessions names every autonomous session that holds or is waiting
// for a slot — StateRunning or StateQueued — as sorted "<id> (<stage>)"
// display strings. Interactive chat holds no slot and has no budget, so
// it is not live here. Empty when nothing is running or queued.
func (m *Shell) liveSessions() []string {
	if m.engine == nil {
		return nil
	}
	var names []string
	for id, s := range m.engine.Sessions() {
		st := s.State()
		if st != engine.StateRunning && st != engine.StateQueued {
			continue
		}
		names = append(names, fmt.Sprintf("%s (%s)", id, s.Feature.Stage))
	}
	sort.Strings(names)
	return names
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
		m.suggestFor,
	))
}

// suggestFor derives a feature's ranked next actions for the inbox
// overlay (the dashboard's next block does the same via nextInputFor).
func (m *Shell) suggestFor(id domain.FeatureID) []nextAction {
	for _, r := range m.rows {
		if r.F.ID == id {
			return nextActions(m.nextInputFor(r))
		}
	}
	return nil
}

// topUpBudget durably raises a feature's envelope and resumes its
// exhausted stage (the "top up" action of a budget gate).
func (m *Shell) topUpBudget(id domain.FeatureID) tea.Cmd {
	m.inbox.remove(id)
	if m.engine == nil {
		return nil
	}
	return func() tea.Msg {
		ctx := context.Background()
		if err := m.engine.TopUp(ctx, id); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: string(id) + " topped up — resuming", reload: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s topped up — envelope raised to %d credits, resuming",
			id, f.Budget.Envelope), reload: true}
	}
}

// setEnvelope durably sets a feature's envelope to an explicit credit
// figure (the u envelope dialog). Unlike topUpBudget it resumes
// nothing: a budget-gated feature stays in the inbox, where enter or
// its own u picks the work back up.
func (m *Shell) setEnvelope(id domain.FeatureID, to int) tea.Cmd {
	if m.engine == nil {
		m.notice = noticeMsg{text: "no agent configured — budgets meter agent spend", isErr: true}
		return nil
	}
	return func() tea.Msg {
		if err := m.engine.RaiseEnvelope(context.Background(), id, to); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if to == 0 {
			return noticeMsg{text: string(id) + ": envelope removed — spend is uncapped", reload: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s: envelope set to %d credits (applies from the next agent session)", id, to), reload: true}
	}
}

// autonomousStage reports whether a stage runs an autonomous agent
// (as opposed to interactive chat or no agent).
func autonomousStage(s domain.Stage) bool {
	switch s {
	case domain.StagePlan, domain.StageImplement, domain.StageFix, domain.StageReview, domain.StageVerify:
		return true
	default:
		return false
	}
}

// handleChatKey routes keys while the chat pane is open.
func (m *Shell) handleChatKey(msg tea.KeyPressMsg) tea.Cmd {
	detach, send, answer, cmd := m.chat.handleKey(msg)
	if detach {
		// esc detaches; the engine session keeps running (DESIGN §6).
		m.chat = nil
		return nil
	}
	if answer != "" {
		return m.answerChat(answer)
	}
	if send != "" {
		return m.sendChat(send)
	}
	return cmd
}

// answerChat delivers the user's reply to an open ask_user question,
// resolving the agent's blocked tool call. Like sendChat it captures the
// session at call time and refuses to answer a since-swapped session.
func (m *Shell) answerChat(text string) tea.Cmd {
	eng, sess := m.engine, m.chat.session
	id := sess.Feature.ID
	return func() tea.Msg {
		if eng.Get(id) != sess {
			return noticeMsg{text: "chat session is no longer active", isErr: true}
		}
		if err := eng.Answer(context.Background(), id, text); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return nil
	}
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
		return m.chat.view(m.styles, w, h, m.spinner())
	}
	if m.spec != nil {
		return m.specViewRender(w, h)
	}
	if m.diff != nil {
		return m.diffViewRender(w, h)
	}
	if m.ingest != nil {
		return m.ingestViewRender(w, h)
	}
	if m.bugIngest != nil {
		return m.bugIngestViewRender(w, h)
	}
	if m.ingestRun != nil && !m.ingestRun.hidden {
		return m.ingestRunRender(w, h)
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
	if m.copilot.ok {
		kind := statusbar.KindNeutral
		if m.copilot.low() {
			kind = statusbar.KindAlert
		}
		pills = append(pills, statusbar.Pill{Text: m.copilot.pill(), Kind: kind})
	}
	if m.ingestRun != nil {
		pills = append(pills, statusbar.Pill{Text: m.spinner() + " ingest", Kind: statusbar.KindNeutral})
	}
	if m.mergePrep {
		pills = append(pills, statusbar.Pill{Text: m.spinner() + " merging", Kind: statusbar.KindNeutral})
	}
	if n := m.inbox.len(); n > 0 {
		pills = append(pills, statusbar.Pill{Text: "✉ " + strconv.Itoa(n) + " need you", Kind: statusbar.KindAlert})
	}
	if m.notice.text != "" && !m.noticeInBand() {
		kind := statusbar.KindNeutral
		if m.notice.isErr {
			kind = statusbar.KindAlert
		}
		pills = append(pills, statusbar.Pill{Text: m.notice.text, Kind: kind})
	}
	// the hint row tracks whichever surface owns the main pane, from the
	// same tables the ? overlay renders (keymap.go)
	_, bindings := m.activeSurface()
	return statusbar.Render(m.styles, w, pills, barHints(bindings))
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
