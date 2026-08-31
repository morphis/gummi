package ui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/notify"
	"github.com/morphis/gummi/internal/rounds"
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
// once attached to a workspace — the board state. Panes render to
// strings and are painted into their rects (the Crush hybrid pattern);
// all IO runs in commands, never in Update or View.
type Shell struct {
	styles  *theme.Styles
	version string

	width, height int
	layout        layout.Layout

	// Dialogs (gate prompts, forms) live on this stack.
	Overlay overlay.Stack

	// workspace wiring (nil store means detached: splash only)
	store *state.Store
	wt    *worktree.Pool
	ws    state.Workspace

	rows []featureRow
	sel  int
	// tab picks which of gummi's top-level tabs owns the main pane
	// (tabs.go): board, inbox, or agent. cardOpen is the board tab's own
	// page-within-a-tab — the selected card opened full width — and
	// belongs to no other tab.
	tab Tab
	// agent is the hosted CLI on TabAgent, spawned lazily on first visit
	// (agenttab.go). nil means it has not been opened yet, or could not
	// start — agentErr says which.
	agent     *agentView
	agentErr  agentSpawnErr
	agentSock string // workspace MCP socket handed to the hosted CLI
	// agentSpawnedAt is when the current m.agent was started (ensureAgent,
	// agenttab.go). The agentExitedMsg handler compares it against m.now()
	// to tell a real, useful session from a CLI that fails at startup and
	// would otherwise spin (see agentCrashLoopWindow).
	agentSpawnedAt time.Time
	// locked is the input lock over a foreign tab (tabs.go's
	// tabDef.foreign), modelled on zellij's ctrl+g: locked, gummi keeps
	// nothing at all but ctrl+g itself, so tab, alt+1/2/3 and ? all reach
	// the hosted CLI, and the mouse goes to it too. Unlocked — the
	// default — gummi keeps only the tab switches and passes every other
	// key through, which is enough to type at the agent but not to use
	// its tab completion, and leaves the mouse to the terminal's own
	// selection.
	//
	// It is one flag rather than per-tab state because it is a mode of
	// the keyboard, not a property of a pane: what it answers is "who am
	// I typing at", and there is only ever one answer at a time.
	locked bool
	// lockUsed records that the user has worked the lock at least once.
	// Until then gummi says what ctrl+g is for on every arrival at a
	// hosted tab, and again if tab is what moved them off one — a lock
	// nobody knows about is the same as no lock, and the two moments it
	// matters are just before you reach for the CLI's tab and just after
	// it did something else. One press retires the lesson: it is an
	// offer, not a nag, and having taken it once is proof it landed.
	lockUsed bool
	// agentConfigName is the workspace's persisted `agent:` choice
	// (config.Config.Agent, loaded once at startup via SetAgentConfig) —
	// the third rung of resolveAgentAttach's precedence, below
	// GUMMI_ATTACH_CMD/GUMMI_AGENT and above the picker. agentConfigPath
	// is where a picker choice gets written back (config.SetAgent);
	// empty disables persistence (a detached shell in tests has nowhere
	// to write), and the choice still applies for the rest of this run.
	agentConfigName string
	agentConfigPath string
	cardOpen        bool
	// threadInput is the card page's persistent message/verb box
	// (thread.go, threadinput.go): a Shell field rather than one rebuilt
	// per render so an unsent draft survives leaving and returning to the
	// tab, same as the chat pane's own m.chat. threadChip is the inline
	// confirm chip pending in its place, or nil. threadSkipParse makes
	// exactly the next submit send as a message unconditionally — the
	// "esc no, send as a message" half of the chip contract
	// (threadinput.go's doc comments own the full story).
	threadInput     textarea.Model
	threadChip      *pendingChip
	threadSkipParse bool

	// bounceNotes holds the line the composer aimed at a decision's
	// bounce answer: the card is rewound now, but its reborn work stage
	// only runs when someone starts it, so the note waits in memory and
	// rides that run's kickoff (runStage) — the same delivery and the
	// same lifetime the headless driver's --bounce note takes. Lost when
	// the process exits, exactly as the driver's is.
	bounceNotes map[domain.FeatureID]string
	inboxSel    int      // cursor into m.inbox.list(), the inbox tab's own selection
	sortMode    SortMode // todo-column ordering toggle (ephemeral, not persisted)
	notice      noticeMsg
	spec        *specView      // non-nil while the spec surface is open
	diff        *diffView      // non-nil while the diff surface is open
	ingest      *ingestView    // non-nil while the ingest review surface is open
	ingestRun   *ingestRunView // non-nil while an ingest pass is decomposing (one at a time)
	deps        *depPicker     // non-nil while the dependency picker is open

	bugIngest    *bugIngestView // non-nil while the bug-import review surface is open
	bugIngesting bool           // a bug import is fetching (one at a time)

	mergePrep  bool // a squash merge's preconditions are being checked (one at a time)
	squashPrep bool // a squash-in-place's preconditions are being checked (one at a time)

	// The dashboard's action list is the second focus region on the board:
	// → moves into it, ← back to the cards. Only the cursor and the focus
	// flag live here — the list itself is rebuilt from cardActionsFor on
	// each use, so it can never go stale against the selected card.
	actionFocused bool
	actionCursor  int
	actionCard    domain.FeatureID // whose list actionCursor belongs to
	// the list folds everything that is legal here but not the advice
	// (cardactions.go); this is whether the fold is currently open. It
	// lives here, not on the list, because the list is rebuilt per frame.
	actionsExpanded bool
	// decision selection belongs to the pinned control in the card thread.
	// The decision itself is regenerated from live ask/next-step state on
	// every render; only ephemeral picker position lives on Shell.
	decisionKey    string
	decisionCursor int
	decisionPicked map[int]bool

	// agent orchestration (nil engine means no agent wired)
	engine *engine.Engine
	// follow is the live tail of a card another process is driving, opened
	// by watchForeign and rendered read-only by the card thread's live
	// stage block. Non-nil only while the card page is open on that card;
	// openCard/closeCard/stepCard stop it (follow.go).
	follow *followSource
	// threadOutputs expands every captured tool output in the thread
	// (alt+o); failures always show their tail either way. Sticky, like
	// the chat pane's toggle was.
	threadOutputs bool
	// threadTranscript is the transcript view (t): every stage segment
	// renders its events instead of one collapsed line, so the whole run
	// reads in the body. Scoped to the visit (openCard/stepCard reset it).
	threadTranscript bool
	// threadFreeForm arms the composer as the open ask's free-form answer
	// channel — the chat pane's 'o' channel, inherited now that the pane
	// is gone. While armed, the decision's picker keys stand down so a
	// line that starts with a digit types as prose; enter delivers the
	// line verbatim as the answer. Disarmed by blur, by an answer, or by
	// the decision changing.
	threadFreeForm bool
	// autopilotAnswering names the cards whose open decision autopilot has
	// already taken and is in the middle of delivering — the interval
	// between dispatching the answer and the answer event landing. It is
	// what the pinned decision marks itself with (decision.go).
	//
	// Deliberately not "this card's mode would answer a decision of this
	// kind": a card sitting idle on gates is not being taken by anyone —
	// autopilot only ever starts a stage it crossed into itself — so
	// marking it from the rule table alone would be a standing lie about
	// a card nothing is going to move.
	autopilotAnswering map[domain.FeatureID]bool
	// foreignTicks counts live-drive probes, pacing the slower full row
	// reload that picks up what another process wrote to the store
	// (follow.go).
	foreignTicks int
	// locks is the board's per-card lock registry, shared with the engine
	// (AttachCardLocks). Nil leaves the board's git verbs unlocked.
	locks      *state.CardLocks
	inbox      *inbox // needs-attention queue
	checks     map[domain.FeatureID][]verify.Result
	baselining map[domain.FeatureID]bool // a baseline check run is in flight
	rounds     map[roundKey]int          // automatic loop round counters, keyed by (id, round_kind)
	// cardEvents caches the card-event log (state.CardEvent, card_events
	// table) per feature, loaded lazily by loadCardEvents and applied to
	// the selected row's featureRow.Events at render time (msgs.go). It is
	// never populated for a row that has not been the selected card on an
	// open card page — loading every card's log on each board refresh
	// would be unbounded IO.
	cardEvents map[domain.FeatureID][]state.CardEvent
	// expandedStages is the thread's fold state: which stage segments (see
	// thread.go's stageSegments) render their events instead of one
	// collapsed line. Keyed by a per-card, per-segment string so two
	// cards' fold state never collides. Nothing sets a key yet — this is
	// the seam a key binds to, not a live toggle yet.
	expandedStages map[string]bool
	// threadScroll is how many lines back from the newest the card
	// thread's body is scrolled. Zero is the bottom, which is where a
	// card opens and where it stays as a live stage streams — counting
	// back from the end rather than forward from the start is what keeps
	// arriving output from shoving the view out from under a reader.
	threadScroll int
	roundStore   rounds.Store     // persistence seam for rounds (defaults to store)
	profileNames []string         // profile names for the new-feature form
	repoNames    []string         // configured managed-repo names for the new-card forms
	envelope     int              // default spend-plan envelope for new features (0 = none)
	notifier     *notify.Notifier // bell/desktop hook for needs-attention events

	// Copilot quota hint (copilotquota.go): the latest reading shown as
	// a status-bar pill, its enable flag, and the gh seam for tests.
	copilot       copilotQuota
	copilotHint   bool
	ghCopilotUser func(context.Context) ([]byte, error)

	// openReviewThreads probes a linked PR for open review threads.
	// Tests stub it; nil uses the engine path.
	openReviewThreads func(context.Context, domain.Feature) (int, string, error)

	// shared activity spinner (spinner.go): frame is the current cycle
	// position; spinning guards the single live tick loop.
	frame    int
	spinning bool

	// now is injectable for deterministic tests.
	now func() time.Time
}

// roundKey is the fast-path round-counter map's key: one entry per
// (feature, round kind), matching the keyed store row.
type roundKey struct {
	id   domain.FeatureID
	kind domain.RoundKind
}

// round reads the fast-path count for (id, kind), defaulting to 0.
func (m *Shell) round(id domain.FeatureID, kind domain.RoundKind) int {
	return m.rounds[roundKey{id, kind}]
}

// setRound writes the fast-path count for (id, kind).
func (m *Shell) setRound(id domain.FeatureID, kind domain.RoundKind, n int) {
	m.rounds[roundKey{id, kind}] = n
}

// NewShell builds a detached shell (splash + empty board).
func NewShell(t theme.Theme, version string) *Shell {
	styles := theme.New(t)
	m := &Shell{
		styles:         styles,
		version:        version,
		now:            time.Now,
		checks:         map[domain.FeatureID][]verify.Result{},
		baselining:     map[domain.FeatureID]bool{},
		rounds:         map[roundKey]int{},
		cardEvents:     map[domain.FeatureID][]state.CardEvent{},
		expandedStages: map[string]bool{},
		copilotHint:    true,
		// the composer is themed from the same styles as everything else
		// on the page; left on the widget's own defaults it renders in raw
		// ANSI and reads as a foreign box (threadinput.go).
		threadInput: newThreadInput(styles),
	}
	// indirected through m rather than passing m.now's current value: a
	// test fixes m.now after this constructor returns (agentWorkspace,
	// populatedShell), and a closure over the field's value at this point
	// would keep calling the real clock regardless.
	m.inbox = newInbox(func() time.Time { return m.now() })
	return m
}

// Attach wires the shell to a workspace: its store, worktree pool (one
// manager per managed repository), and paths. Must be called before Run for
// board functionality.
func (m *Shell) Attach(store *state.Store, wt *worktree.Pool, ws state.Workspace) {
	m.store, m.wt, m.ws = store, wt, ws
	// the rounds persistence seam defaults to the real store; tests may
	// swap in a failing store to prove the fail-closed path.
	m.roundStore = store
}

// cardLocked runs fn holding the card's lock for its whole duration, so a
// git verb this board runs can't interleave with a headless
// run/resume/merge/clean of the same card (each of which takes the very
// same lock). Holds inside this process are refcounted, so a verb on a
// card the board's own engine is already driving joins that hold instead
// of refusing itself.
//
// With no registry wired (a test scaffold) it is a plain pass-through,
// which is the behavior these verbs had before locking existed.
func (m *Shell) cardLocked(id domain.FeatureID, fn func() tea.Msg) tea.Cmd {
	return func() tea.Msg {
		release, err := m.locks.Acquire(id)
		if err != nil {
			return noticeMsg{text: cardLockedNotice(id, err), isErr: true}
		}
		defer release()
		return fn()
	}
}

// cardLockedNotice renders a dispatch failure, adding what the board can
// offer when the card turned out to be locked by another gummi process:
// watching the run it may not drive. Any other error passes through as
// itself.
func cardLockedNotice(id domain.FeatureID, err error) string {
	if errors.Is(err, state.ErrLocked) {
		return fmt.Sprintf("%s is being driven by another gummi process — press enter to watch it, or wait for it to finish", id)
	}
	return sanitize(err.Error())
}

// AttachCardLocks wires the board's per-card lock registry — the same one
// its engine holds cards with — so the board's own git verbs (merge,
// rebase, squash, clean, verify, delete) take the card's lock for their
// duration too. Without it those verbs run unlocked, which is the old
// behavior and all a test scaffold needs.
func (m *Shell) AttachCardLocks(l *state.CardLocks) { m.locks = l }

// AttachEngine wires the agent orchestrator, enabling interactive chat
// and autonomous stages. Optional: without it the board is static.
func (m *Shell) AttachEngine(e *engine.Engine) { m.engine = e }

// SetProfileNames sets the profile names offered by the new-feature
// form (from profiles.yaml). Empty leaves the built-in presets.
func (m *Shell) SetProfileNames(names []string) { m.profileNames = names }

// SetRepoNames sets the configured managed-repository names offered by the
// new-feature and new-bug forms. Empty leaves only the workspace default.
func (m *Shell) SetRepoNames(names []string) { m.repoNames = names }

// repoHasDefault reports whether the workspace default repository
// actually resolves (worktree.Pool.Known(""))  — false in a repos:-only
// workspace with no `repo:` root. The creation dialogs' repo field uses
// this so it never offers a "default" choice that would only fail later
// at worktree creation (worktree/pool.go ManagerForName).
func (m *Shell) repoHasDefault() bool {
	if m.wt == nil {
		return false
	}
	return m.wt.Known(m.wt.DefaultName())
}

// SetEnvelope sets the default spend-plan envelope (credits) stamped on
// new features, enabling layer-3 per-stage budgets. 0 leaves features
// unbudgeted (or governed by a flat per-stage budget).
func (m *Shell) SetEnvelope(credits int) { m.envelope = credits }

// SetNotifier wires the needs-attention notification hook (bell/desktop).
func (m *Shell) SetNotifier(n *notify.Notifier) { m.notifier = n }

// SetCopilotHint toggles the status-bar Copilot quota pill (on by
// default; it hides itself anyway when gh or a quota is absent).
func (m *Shell) SetCopilotHint(on bool) { m.copilotHint = on }

// probeOpenReviewThreads returns the count of open review threads and the
// PR URL for a linked PR, or (0, "", nil) when there is no linked PR. A
// nil seam falls back to the engine path so tests can stub failures.
func (m *Shell) probeOpenReviewThreads(ctx context.Context, f domain.Feature) (int, string, error) {
	if m.openReviewThreads != nil {
		return m.openReviewThreads(ctx, f)
	}
	if f.PullRequest.Empty() {
		return 0, "", nil
	}
	if m.engine == nil {
		return 0, f.PullRequest.URL, nil
	}
	_, diffOpen, _, err := m.engine.GateBlockers(ctx, f.ID)
	if err != nil {
		return 0, "", err
	}
	return diffOpen, f.PullRequest.URL, nil
}

// openSquashDialog opens the reused commit-message dialog for a squash
// in place, pre-filled with the same best-effort draft the merge path uses.
func (m *Shell) openSquashDialog(f domain.Feature) tea.Cmd {
	d := newCommitMsgDialog(f, func(message string) tea.Cmd {
		return m.collapseFeature(f, message)
	}, func(dctx context.Context, feature domain.Feature) (string, error) {
		if m.engine == nil {
			return "", nil
		}
		return m.engine.DraftCommitMessage(dctx, feature)
	})
	m.Overlay.Push(d)
	return d.startDraft()
}

// reconstructInbox is the needs-attention queue's fallback source at
// startup, covering whatever the durable decision_open records
// (seedInboxFromDecisions, dispatched ahead of this from the
// openDecisionsMsg handler) did not: a card driven by a pre-decision
// binary, or one whose stop predates this change. Derived from durable
// session state: a failed run (paused with a stored error) → failure; a
// settled autonomous stage → a budget park if its activity recorded an
// exhaustion, else a review-&-advance gate.
//
// Every add here goes through seed, not add/put: it must not clobber a
// feature the decision seeding (or a live engine event racing it) already
// gave an item, since both of those are fresher than this inference.
func (m *Shell) reconstructInbox() {
	if m.engine == nil {
		return
	}
	for id, s := range m.engine.Sessions() {
		snap := s.Snapshot()
		switch {
		case snap.Err != nil:
			m.inbox.seed(attnItem{Feature: id, Kind: attnFailure, Text: sanitize(snap.Err.Error())})
		case snap.State != engine.StateDone || !autonomousStage(snap.Feature.Stage):
			// running/queued/interactive sessions raise their own items live
			continue
		case exhaustedActivity(snap.Activity):
			m.inbox.seed(attnItem{Feature: id, Kind: attnBudget, Text: string(snap.Feature.Stage) + " hit its budget — u top up or x park"})
		default:
			m.inbox.seed(attnItem{Feature: id, Kind: attnGate, Text: string(snap.Feature.Stage) + " finished — review & advance"})
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

// raiseEscalation is raiseAttention for gates an automatic loop gave up
// on (round cap, unclear verdict): the item carries the escalation flag
// so surfaces tint it as needs-you rather than finished-clean. Every
// escalation is a gate — the loop stopped at a decision only the human
// can take — and it is recorded as one, the same seam the driver's
// escalations raise through.
func (m *Shell) raiseEscalation(id domain.FeatureID, text string) {
	if m.inbox.addEscalated(id, attnGate, text) {
		m.notifier.Alert(string(id) + ": " + text)
		m.logPark(id, state.ParkReasonGaveUp, text)
		m.logDecision(id, decisionKindForStage(m.stageOf(id)), text)
	}
}

// raiseAttention adds a needs-attention item and, when it is a new alert
// (not an update of an existing one), fires the notification hook and
// records the stop's decision — the one row §10.18 requires. Only gate
// items record here: asks and budget stops open their decisions where
// they happen (the engine's ask path and exhaust), so the TUI writing
// them too would mint a second row for the same decision. The inbox's own
// already-present check keeps one stop from being recorded twice, exactly
// as the park beside it does.
func (m *Shell) raiseAttention(id domain.FeatureID, kind attnKind, text string) {
	if m.parkAttentionItem(id, kind, text) && kind == attnGate {
		m.logDecision(id, state.DecisionKindGate, text)
	}
}

// parkAttentionItem is raiseAttention without the decision write: the
// inbox add, the notification, and the park-history row, reporting
// whether it was a new alert (the same "is this new" gate raiseAttention
// itself checks before logging a decision). It exists for
// autopilotCrossGate's own park fallback (autopilot.go): that caller
// already opened the gate's decision row before attempting the crossing,
// so parking after a blocked Advance must add the inbox item without
// minting a second decision row for the same stop.
func (m *Shell) parkAttentionItem(id domain.FeatureID, kind attnKind, text string) bool {
	if !m.inbox.add(id, kind, text) {
		return false
	}
	m.notifier.Alert(string(id) + ": " + text)
	m.logPark(id, state.ParkReasonNeedsYou, text)
	return true
}

// logDecision records a card's open decision in its own history
// (best-effort and silent, like logPark beside it): a card blocked on a
// person leaves a row, whoever drove it here. The id is minted per
// raise, generation-scoped so a re-raised stop after a bounce never
// collides with its predecessor's row.
func (m *Shell) logDecision(id domain.FeatureID, kind, question string) {
	if m.store == nil {
		return
	}
	stage := m.stageOf(id)
	_ = m.store.OpenDecision(context.Background(), id, stage, state.DecisionPayload{
		ID:       kind + ":" + string(id) + ":" + string(stage) + ":" + strconv.FormatInt(time.Now().UnixNano(), 10),
		Kind:     kind,
		Question: question,
	}, time.Now())
}

// logPark records a card stopping in its own history. It hangs off the
// two raise paths rather than their callers because those are where a
// card actually comes to rest, and because the inbox's own
// already-present check is what keeps one stop from being logged twice.
//
// Best-effort and deliberately silent: the user has already been told
// the card stopped, and failing to write the history entry is not worth
// a second, more confusing message about it.
func (m *Shell) logPark(id domain.FeatureID, reason, detail string) {
	if m.store == nil {
		return
	}
	_ = m.store.AppendPark(context.Background(), id, m.stageOf(id), reason, detail, "", time.Now())
}

// stageOf reads the card's current stage from the loaded rows. A card
// that is not on the board (deleted mid-flight) reports an empty stage,
// which the log stores as-is rather than guessing.
func (m *Shell) stageOf(id domain.FeatureID) domain.Stage {
	for _, r := range m.rows {
		if r.F.ID == id {
			return r.F.Stage
		}
	}
	return ""
}

// autopilotModeFor reads a card's stored gate-approval mode: the board's
// own row when the card is loaded there (the same source stageOf reads,
// kept fresh by every reload) wins, because it is what every other
// autopilot-mode read in this package (autopilotCursorFor, planAutopilot)
// already treats as authoritative. A card whose row hasn't loaded yet —
// an event arriving before the first loadRows lands — falls back to the
// engine session's own copy of the feature, which Attach/dispatch loaded
// fresh no longer ago than the row would have.
func (m *Shell) autopilotModeFor(id domain.FeatureID) string {
	for _, r := range m.rows {
		if r.F.ID == id {
			return r.F.GateApproval
		}
	}
	if m.engine != nil {
		if s := m.engine.Get(id); s != nil {
			return s.Snapshot().Feature.GateApproval
		}
	}
	return ""
}

// markAutopilotAnswering / clearAutopilotAnswering bracket the interval
// the pinned decision reports as autopilot's. Both run on the Update
// goroutine (the dispatch, and the message the dispatched command sends
// back), so the map needs no lock of its own.
func (m *Shell) markAutopilotAnswering(id domain.FeatureID) {
	if m.autopilotAnswering == nil {
		m.autopilotAnswering = map[domain.FeatureID]bool{}
	}
	m.autopilotAnswering[id] = true
}

func (m *Shell) clearAutopilotAnswering(id domain.FeatureID) {
	delete(m.autopilotAnswering, id)
}

// autopilotAnsweredMsg closes the interval markAutopilotAnswering opened,
// whatever the answer's outcome was: the decision is no longer autopilot's
// to take, either because it took it or because the attempt failed and
// the card is the human's again.
type autopilotAnsweredMsg struct {
	id     domain.FeatureID
	notice noticeMsg
	// park is the needs-you text to queue when the answer did not land:
	// the agent is still blocked on its question, so something has to
	// point the user at it. Empty when the answer went through.
	park string
}

// autopilotAnswerAsk answers id's live pending ask with rec as autopilot.
// AnswerAs talks to the agent backend (it delivers the answer as a fresh
// turn), which is exactly the IO the no-IO-in-Update contract keeps out
// of Update itself, so it runs inside the returned command rather than
// where handleEngineEvent decided to call it. It opens no decision row of
// its own: the engine's ask path already opened one when it raised the
// question (DESIGN §6.3), and the answer event AnswerAs records carries
// that same id, closing it exactly as a human's answer would.
func (m *Shell) autopilotAnswerAsk(id domain.FeatureID, rec, question string) tea.Cmd {
	m.markAutopilotAnswering(id)
	return func() tea.Msg {
		if err := m.engine.AnswerAs(context.Background(), id, rec, state.ActorAutopilot); err != nil {
			// the agent is still blocked on the question, so the card has
			// to reach the queue after all — park is what says so.
			return autopilotAnsweredMsg{
				id:     id,
				notice: noticeMsg{text: sanitize(err.Error()), isErr: true},
				park:   question,
			}
		}
		return autopilotAnsweredMsg{id: id, notice: noticeMsg{text: string(id) + ": auto-answered: " + rec}}
	}
}

// decisionKindForStage maps a stopped card's stage to the decision kind
// its stop records: a failed verify escalates as the verify decision it
// is; every other give-up is a gate the human judges.
func decisionKindForStage(stage domain.Stage) string {
	if stage == domain.StageVerify {
		return state.DecisionKindVerify
	}
	return state.DecisionKindGate
}

// Styles exposes the derived style set to panes.

// attached reports whether a workspace is wired in.
func (m *Shell) attached() bool { return m.store != nil }

// Init implements tea.Model.
func (m *Shell) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.attached() {
		cmds = append(cmds, m.loadRows)
		// Seed the needs-attention queue from the durable decision_open
		// records (openDecisionsMsg's handler in update, which also runs
		// reconstructInbox as the session-inference fallback) rather than
		// rebuilding it by inference alone. Store.OpenDecisions hits the
		// database, and Init runs on the Update goroutine — see
		// attachChat's no-IO-in-Update contract — so the query has to be a
		// dispatched command, not a direct call.
		//
		// A store is all it needs: the record outliving the process that
		// raised it is the whole point of it, so a board with no agent
		// wired still learns what a headless run left waiting. The session
		// inference behind it is the part that needs an engine, and it
		// no-ops without one.
		cmds = append(cmds, m.fetchOpenDecisions)
	}
	if m.engine != nil {
		if !m.attached() {
			// no store to query, so the inference is the only source there
			// is — a scaffold-only shape, but it must still seed something.
			m.reconstructInbox()
		}
		// offer to pick up any card the board stopped by quitting
		// (quitresume.go) — once, here, and nowhere else: nothing may
		// restart a card without this dialog's own confirm.
		m.maybeOfferQuitResume()
		cmds = append(cmds, m.listenEngineCmd())
	}
	if m.copilotHint {
		cmds = append(cmds, m.fetchCopilotQuota())
	}
	if m.attached() {
		// probe for cards other gummi processes are driving, so the board
		// badges them (and withholds the actions that would fight them)
		// instead of presenting a card it cannot touch as idle.
		cmds = append(cmds, foreignTick())
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
		if err := rounds.Reset(context.Background(), m.roundStore, ev.Feature, domain.RoundKindPlan); err != nil {
			m.notice = noticeMsg{text: sanitize(err.Error()), isErr: true}
			m.raiseAttention(ev.Feature, attnFailure, sanitize(err.Error()))
		} else {
			m.setRound(ev.Feature, domain.RoundKindPlan, 0)
		}
		// same write-through for the review-loop counter: a failed write
		// must not lose the burned rounds recorded in the store.
		if err := rounds.Reset(context.Background(), m.roundStore, ev.Feature, domain.RoundKindReview); err != nil {
			m.notice = noticeMsg{text: sanitize(err.Error()), isErr: true}
			m.raiseAttention(ev.Feature, attnFailure, sanitize(err.Error()))
		} else {
			m.setRound(ev.Feature, domain.RoundKindReview, 0)
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
		// A card whose stored mode answers its own questions (§10.17: full
		// only — gates still stops for a question) takes the recommended
		// option itself, whether or not anyone is looking at the card page
		// right now: a decision must not resolve differently because a
		// human happened to be on screen (the same reason the design
		// forbids a countdown). This has to run before the cardOpen check
		// below, which is about where a *parked* question is shown, not
		// whether one gets parked at all.
		if autopilotAnswers(m.autopilotModeFor(ev.Feature), decisionAsk) {
			if s := m.engine.Get(ev.Feature); s != nil {
				if ask := s.Snapshot().PendingAsk; ask != nil {
					if rec := engine.RecommendedOption(ask); rec != "" {
						// the question travels with the command: if the answer
						// fails to land the agent is still blocked on it, and
						// the queue is the only thing that would say so.
						return m.autopilotAnswerAsk(ev.Feature, rec, "asks: "+ask.Question)
					}
				}
			}
			// no live ask, or RecommendedOption came back empty: nothing
			// safe to answer with, so fall through and park like today.
		}
		// the agent asked something. When the card page is open on the
		// asking card, its pinned decision already shows the question
		// inline; otherwise queue it so you can jump to it from the
		// needs-attention inbox.
		if m.cardOpen && m.sel >= 0 && m.sel < len(m.rows) && m.rows[m.sel].F.ID == ev.Feature {
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
		text := string(ev.Stage) + " finished — review & advance"
		if cmd, attempted := m.autopilotCrossGate(s.Snapshot().Feature, text); attempted {
			return cmd
		}
		m.raiseAttention(ev.Feature, attnGate, text)
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
		m.layout = m.computeLayout()
		// the hosted CLI has to learn the new pane size from both halves
		// of the pty pair, or it keeps drawing at the old width.
		if m.agent != nil {
			w, h := m.agentPaneSize()
			if err := m.agent.Resize(w, h); err != nil {
				m.notice = noticeMsg{text: sanitize(err.Error()), isErr: true}
			}
		}
		return m, nil

	case agentOutputMsg:
		// a repaint happens for free on any message; re-arm the listener
		// or the tab goes deaf after its first chunk.
		if m.agent == nil || msg.view != m.agent {
			return m, nil
		}
		return m, m.agent.Wait()

	case agentExitedMsg:
		if m.agent == nil || msg.view != m.agent {
			return m, nil
		}
		// A hosted CLI ending its own session (the user typed /exit, or an
		// autonomous run finished) shouldn't leave a dead pane sitting on
		// the tab — respawn it right away, same as the first visit.
		//
		// Guard against a crash loop first, though: a CLI that fails at
		// startup (bad auth, a missing config file, an incompatible flag)
		// exits almost immediately, and respawning that unconditionally
		// would spin forever, each attempt burning a process start and
		// scrolling the same failure past the user with no chance to read
		// it. agentCrashLoopWindow draws the line — an exit within it reads
		// as "never really started"; past it, as a session that ran and
		// ended, worth restarting.
		if elapsed := m.now().Sub(m.agentSpawnedAt); elapsed < agentCrashLoopWindow {
			text := fmt.Sprintf("agent exited %s after starting — not restarting (looks like a crash loop)",
				elapsed.Round(time.Millisecond))
			if msg.err != nil {
				text += ": " + sanitize(msg.err.Error())
			}
			m.agent = nil
			m.agentErr = agentSpawnErr(text)
			return m, nil
		}
		text := "agent exited"
		if msg.err != nil {
			text += ": " + sanitize(msg.err.Error())
		}
		m.notice = noticeMsg{text: text, isErr: msg.err != nil}
		m.agent = nil
		return m, m.ensureAgent()

	case agentPickerLoadedMsg:
		m.Overlay.Push(newAgentPickerDialog(msg.agents, m.agentConfigName, m.chooseAgentCLI))
		return m, nil

	case agentChosenMsg:
		if msg.err != nil {
			m.notice = noticeMsg{text: "saving agent choice: " + sanitize(msg.err.Error()), isErr: true}
			return m, nil
		}
		m.agentConfigName = msg.name
		// The hosted CLI may already be running under the old choice (or
		// sitting on a spawn error from having none) — close it so the
		// next visit to the agent tab respawns under the new selection
		// instead of keeping a stale process or a stale error message on
		// screen forever.
		m.closeAgent()
		m.agentErr = ""
		m.notice = noticeMsg{text: "agent tab: " + msg.name + " chosen"}
		return m, nil

	case rowsMsg:
		if msg.err != nil {
			m.notice = noticeMsg{text: msg.err.Error(), isErr: true}
			return m, nil
		}
		// the cursor is kept on the card it was on, by id: the rows about
		// to replace these can be a different length and a different order,
		// and an index alone would quietly point somewhere else.
		was := m.selectedID()
		m.rows = msg.rows
		m.restoreSel(was)
		// the action cursor belongs to whichever card is selected, so it
		// resyncs whether or not the selection survived.
		m.syncActionFocus()
		return m, nil

	case openDecisionsMsg:
		// The record is the primary source: seed from it first, then let
		// reconstructInbox's session inference fill whatever it didn't
		// cover (a pre-decision card, or a query that came back empty). A
		// failed query has nothing to seed from, so reconstructInbox runs
		// alone and the notice says the queue may be short a few items
		// rather than silently pretending it is complete.
		if msg.err != nil {
			m.notice = noticeMsg{text: "needs-you queue: " + sanitize(msg.err.Error()), isErr: true}
			m.reconstructInbox()
			return m, nil
		}
		m.seedInboxFromDecisions(msg.decisions)
		m.reconstructInbox()
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

	case squashReadyMsg:
		m.squashPrep = false
		if msg.err != nil {
			text := string(msg.f.ID) + " squash failed: " + msg.err.Error()
			var ne squashNoticeErr
			if errors.As(msg.err, &ne) {
				// landed guard carries its own ID-prefixed notice; emit it
				// verbatim without the generic "squash failed:" wrapper.
				text = ne.text
			}
			m.notice = noticeMsg{text: text, isErr: true}
			return m, nil
		}
		m.notice = noticeMsg{}
		f := msg.f
		if msg.openThreads > 0 {
			detail := strconv.Itoa(msg.openThreads) + " open review thread(s) will be detached from their lines"
			if msg.prURL != "" {
				detail = detail + " — " + msg.prURL
			}
			m.Overlay.Push(&confirmDialog{
				id:       "confirm-squash",
				question: "squash " + string(f.ID) + "?",
				detail:   detail,
				onConfirm: func() tea.Cmd {
					return func() tea.Msg { return squashOpenDialogMsg{f: f} }
				},
			})
			return m, nil
		}
		return m, m.openSquashDialog(f)

	case squashOpenDialogMsg:
		return m, m.openSquashDialog(msg.f)

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

	case autopilotSettledMsg:
		// whatever the crossing came back as, autopilot is done holding
		// this decision (autopilot.go's autopilotSettled) — then the inner
		// message is handled exactly as if it had arrived on its own.
		m.clearAutopilotAnswering(msg.id)
		if msg.inner == nil {
			return m, nil
		}
		return m.update(msg.inner)

	case autopilotAnsweredMsg:
		m.clearAutopilotAnswering(msg.id)
		if msg.park != "" {
			// the answer never reached the agent — a session swapped out
			// from under the command, an empty recommendation — and the
			// agent is still blocked on the question. Park it the way the
			// non-autopilot path would have, or the card waits on a
			// question with nothing on screen pointing at it.
			m.parkAttentionItem(msg.id, attnQuestion, msg.park)
		}
		m.notice = msg.notice
		return m, nil

	case autopilotContinueMsg:
		// the crossing landed; the stage behind it is autopilot's to start
		// (msgs.go's autopilotContinueMsg says why this is the idle
		// decision being answered rather than a second gate crossing).
		m.clearAutopilotAnswering(msg.id)
		m.notice = noticeMsg{text: msg.note}
		m.inbox.remove(msg.id)
		return m, tea.Batch(m.loadRows, m.autopilotRun(msg.id, msg.to))

	case autopilotGateBlockedMsg:
		// autopilotCrossGate (autopilot.go) already opened the gate's
		// decision row before attempting the crossing, so parking here
		// uses parkAttentionItem, not raiseAttention — logging the
		// decision a second time for the one stop would leave a
		// duplicate open row for what is a single park.
		m.clearAutopilotAnswering(msg.id)
		m.parkAttentionItem(msg.id, attnGate, msg.text)
		return m, m.loadRows

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
		if msg.continueTo != "" {
			// autopilot's own crossing: the stage behind the gate is its to
			// start, alongside the one-shot passes rather than after them —
			// discovery and the estimate read the artifact the crossing
			// just promoted, and neither is a precondition of running.
			m.clearAutopilotAnswering(msg.id)
			cmds = append(cmds, m.autopilotRun(msg.id, msg.continueTo))
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
			// reload in place: keep the cursor, clamped in case the doc
			// shrank. The window follows the cursor, so that is the whole
			// of the position to carry over.
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
			// reload in place: keep the cursor, clamped in case the diff
			// shrank (e.g. after a fix-up run). The window follows the
			// cursor, so that is the whole of the position to carry over.
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
		// waiting on it, so it takes the foreground: the review surface
		// installs over whatever the board tab held.
		m.spec, m.diff = nil, nil
		if msg.decomposeFor != "" && len(msg.res.Proposals) == 0 {
			// every `## Slices` row is already settled (or there were none) —
			// mirrors the headless auto-trigger's zero-slice no-op instead of
			// opening a review surface with nothing to approve.
			m.notice = noticeMsg{text: string(msg.decomposeFor) + ": nothing unsettled to decompose"}
			return m, nil
		}
		if msg.decomposeFor != "" {
			m.ingest = newDecomposeReviewView(msg.res, msg.decomposeFor)
		} else {
			m.ingest = newIngestView(msg.res, msg.profile, msg.envelope, msg.repo)
		}
		m.notice = noticeMsg{text: "proposed " + strconv.Itoa(len(msg.res.Proposals)) + " feature(s) — review & approve"}
		return m, nil

	case depsLoadedMsg:
		if msg.err != nil {
			m.notice = noticeMsg{text: sanitize(msg.err.Error()), isErr: true}
			return m, nil
		}
		// a reload from a closed picker (esc'd while the edge write was in
		// flight) still refreshes the board; only apply the candidate set
		// while the picker is open on the same card.
		if msg.reload {
			if m.deps != nil && m.deps.f.ID == msg.f.ID {
				m.deps.cands = msg.cands
				m.deps.removeOnly = msg.removeOnly
				m.deps.setCursor(m.deps.cursor)
			}
			return m, m.loadRows
		}
		// initial open: install the surface for the selected card, snapped
		// onto the first navigable row (buildCands sets the cursor while
		// reading, which the open message doesn't carry).
		m.deps = &depPicker{f: msg.f, cands: msg.cands, removeOnly: msg.removeOnly}
		m.deps.setCursor(0)
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
		m.spec, m.diff, m.ingest = nil, nil, nil
		m.bugIngest = newBugIngestView(msg.res, msg.profile, msg.envelope)
		m.notice = noticeMsg{text: "fetched " + strconv.Itoa(len(msg.res.Proposals)) + " bug(s) — review & approve"}
		return m, nil

	case foreignTickMsg:
		// keep the probe running whether or not anything is driven
		// elsewhere: a run can start in another terminal at any moment.
		return m, tea.Batch(foreignTick(), m.probeForeign)

	case foreignMsg:
		changed := m.applyForeign(msg.drives)
		if changed || msg.reload {
			// a drive that just started or ended moved the store too, and a
			// long-running one keeps moving it; reload so the badges are not
			// the only thing on the board that is current.
			return m, m.loadRows
		}
		return m, nil

	case followRecordMsg:
		return m, m.applyFollow(msg)

	case followClosedMsg:
		// the tail ended (the pane closed, or its context was canceled).
		// Nothing to do: the pane, if still open, keeps its last view.
		return m, nil

	case engineEventMsg:
		cmd := m.handleEngineEvent(msg.ev)
		// engine events otherwise carry no payload the view needs — they
		// just signal "re-render from Snapshot" — so keep listening, plus
		// any automatic review-loop follow-up.
		return m, tea.Batch(m.listenEngineCmd(), cmd)

	case engineClosedMsg:
		// the agent backend shut down unexpectedly. There is no pane left
		// to freeze — a card's own thread renders its session's error —
		// but anyone with a live session was watching something that just
		// stopped, so say why.
		if m.engine != nil && len(m.engine.Sessions()) > 0 {
			m.notice = noticeMsg{text: "agent backend stopped", isErr: true}
		}
		return m, nil

	case chatAttachedMsg:
		// the attach ran in a command (spawning the backend can take
		// seconds). The card page is the conversation's surface now, so
		// arrival means landing on it with the composer ready — the pane
		// this message used to open is gone (DESIGN §10.5: retired into
		// the thread).
		if msg.err != nil {
			m.notice = noticeMsg{text: cardLockedNotice(msg.feature.ID, msg.err), isErr: true}
			return m, nil
		}
		m.inbox.remove(msg.feature.ID)
		if r, ok := m.selected(); !ok || r.F.ID != msg.feature.ID {
			// the board moved on while the backend spawned. The page is
			// the selected card's, so there is nowhere honest to land the
			// arrival: the session is live and the card shows it the next
			// time it is opened.
			return m, nil
		}
		var open tea.Cmd
		if !m.cardOpen {
			// openCard also kicks the event-log load the thread's folded
			// receipts read — dropping its command would leave the page
			// showing a live session over an empty history.
			open = m.openCard()
		}
		m.focusThreadInput()
		return m, open

	case cardEventsMsg:
		// a late reply for a card the page has since moved off (esc, or a
		// second J/K before the first load landed) is dropped: the cache
		// keys on the feature it belongs to and nothing renders a stale
		// key by mistake, but there's nothing to gain by keeping a fetch
		// racing behind the current selection.
		if msg.err == nil {
			m.cardEvents[msg.id] = msg.events
		}
		return m, nil

	case tea.KeyPressMsg:
		// ctrl+c is hoisted above the overlay: it is the one key every
		// terminal program is expected to answer, and routing it into an
		// open dialog's text input (which is what happened) left no way
		// out of a modal but esc.
		if msg.String() == "ctrl+c" {
			// On a foreign tab ctrl+c belongs to the hosted CLI — it is
			// the key its users reach for most, and gummi taking it would
			// break interrupting a run. This is not a special case for
			// ctrl+c but the general pass-through rule (handleKey): gummi
			// keeps the tab switches and hands over the rest. alt+1 then q
			// still quits from inside the tab, and ctrl+g first if locked.
			if m.hostedKeyboard() {
				return m, m.agentKey(msg)
			}
			return m, m.quitCmd()
		}
		// ctrl+g is hoisted for the opposite reason to ctrl+c: it is the
		// one key gummi never yields, in either state. A lock you can
		// enter but not leave is the trap this whole mechanism exists to
		// remove, so nothing — not an overlay, not the hosted CLI — is
		// allowed between the user and the way out.
		if msg.String() == "ctrl+g" {
			m.toggleLock()
			return m, nil
		}
		if consumed, cmd := m.Overlay.HandleKey(msg); consumed {
			return m, cmd
		}
		return m, m.handleKey(msg)

	case tea.PasteMsg:
		if consumed, cmd := m.Overlay.HandlePaste(msg); consumed {
			return m, cmd
		}
		return m, m.handlePaste(msg)

	case tea.MouseMsg:
		// gummi's own surfaces are keyboard-only by design, so the mouse
		// has exactly one destination: a hosted CLI that has the input
		// lock. Everywhere else the event is dropped and the terminal's
		// native selection is left alone — see forwardMouse.
		m.forwardMouse(msg)
		return m, nil
	}
	return m, nil
}

// handlePaste routes bracketed-paste text to whichever pane input is
// editing; a paste with no input focused is dropped.
func (m *Shell) handlePaste(msg tea.PasteMsg) tea.Cmd {
	// the hosted CLI brackets pastes itself (x/vt honours the child's own
	// bracketed-paste mode), so hand it the text rather than any of
	// gummi's inputs while its tab is up.
	if m.hostedKeyboard() {
		m.agent.Paste(msg.Content)
		return nil
	}
	if m.cardOpen && m.threadInput.Focused() {
		return m.handleThreadPaste(msg)
	}
	if bv := m.bugIngest; bv != nil && bv.filtering {
		bv.filter, _ = bv.filter.Update(msg)
		bv.setCursor(bv.cursor) // reclamp: the visible set may have shrunk
	}
	return nil
}

// quitCmd is the shared exit path for q and ctrl+c. Quitting with
// autonomous work live stops sessions mid-turn; ask first so the user
// who means it can still get out. A live session on an autopilot card
// (GateApproval anything but GateOff) is not lost work in the same way:
// StopForQuit records where it stopped, and it picks back up on reopen
// (quitresume.go) — so it gets its own wording, naming the cards and
// saying so, never implying they keep going once the terminal closes
// (they don't — there is no background execution). A card driven by
// hand still loses its in-flight turn and its spend, uncommitted on
// disk; that warning is unchanged. Idle quit stays a single keypress,
// and a second press while the confirm is already up means it —
// otherwise ctrl+c, hoisted above the overlay, could only ever re-raise
// the dialog it just opened.
func (m *Shell) quitCmd() tea.Cmd {
	if m.Overlay.Contains("confirm-quit") {
		return m.quitNow()
	}
	question, detail := "", ""
	confirmLabel, cancelLabel := "Quit", "Stay"
	autopilotLive, plainLive := m.liveAutopilotSplit()
	switch {
	case len(autopilotLive) > 0:
		question = autopilotQuitQuestion(autopilotLive)
		detail = "they stop where they are and pick up when you reopen."
		if len(plainLive) > 0 {
			detail += " quitting also stops " + strings.Join(plainLive, ", ") +
				" mid-turn — the in-flight turn and its spend are discarded and the work is left uncommitted on disk (recoverable next run)."
		}
		confirmLabel, cancelLabel = "Stop them and quit", "Cancel"
	case len(plainLive) > 0:
		question = "quit with live sessions " + strings.Join(plainLive, ", ") + "?"
		detail = "quitting stops them mid-turn — the in-flight turn and its spend are discarded and the work is left uncommitted on disk (recoverable next run)"
	// an ingest or bug-import pass is not an engine session, so
	// liveAutopilotSplit never saw it. Both cost a paid architect pass,
	// and esc already confirms before discarding one — quitting past
	// that silently would make the confirm theatre.
	case m.ingestRun != nil:
		question = "quit while a decompose is running?"
		detail = "the architect pass is paid for and its proposals are not written anywhere yet — quitting loses them"
	case m.ingest != nil:
		question = fmt.Sprintf("quit with %d unsaved proposal(s)?", len(m.ingest.props))
		detail = "they came from a paid architect pass over " + m.ingest.source + " and nothing has been created yet"
	case m.bugIngesting:
		question = "quit while a bug import is fetching?"
		detail = "the fetch is in flight — quitting drops it"
	case m.bugIngest != nil && m.bugIngest.edited:
		question = "quit with unsaved import edits?"
		detail = "your renamed titles and one-liners are not kept — re-importing fetches the issues as they are on GitHub"
	default:
		return m.quitNow()
	}
	m.Overlay.Push(&confirmDialog{
		id:           "confirm-quit",
		cancelLabel:  cancelLabel,
		confirmLabel: confirmLabel,
		question:     question,
		detail:       detail,
		onConfirm:    m.quitNow,
	})
	return nil
}

// quitNow is quitCmd's actual exit: it stops every live autopilot
// session (best-effort, and a no-op when there is nothing to stop — see
// engine.Engine.StopForQuit), closes the hosted CLI, and quits.
func (m *Shell) quitNow() tea.Cmd {
	if m.engine != nil {
		m.engine.StopForQuit(context.Background())
	}
	m.closeAgent()
	return tea.Quit
}

// liveAutopilotSplit splits the board's live (StateRunning/StateQueued)
// sessions by whether their card is on autopilot — GateApproval
// anything but domain.GateOff, same as everywhere else the field is
// interpreted (domain.Feature.GateApproval's own doc: empty reads as
// GateGates). autopilot holds bare ids, sorted — all the quit dialog
// needs to name them; plain mirrors the old liveSessions' "<id>
// (<stage>)" labels, so a hand-driven session's wording stays exactly
// what it was.
func (m *Shell) liveAutopilotSplit() (autopilot, plain []string) {
	if m.engine == nil {
		return nil, nil
	}
	for id, s := range m.engine.Sessions() {
		switch s.State() {
		case engine.StateRunning, engine.StateQueued:
		default:
			continue
		}
		if s.Feature.GateApproval == domain.GateOff {
			plain = append(plain, fmt.Sprintf("%s (%s)", id, s.Feature.Stage))
			continue
		}
		autopilot = append(autopilot, string(id))
	}
	sort.Strings(autopilot)
	sort.Strings(plain)
	return autopilot, plain
}

// autopilotQuitQuestion words the quit dialog's title line for one or
// more autopilot cards, e.g. "2 cards are running on autopilot — FD-047,
// FD-044."
func autopilotQuitQuestion(ids []string) string {
	verb := "cards are"
	if len(ids) == 1 {
		verb = "card is"
	}
	return fmt.Sprintf("%d %s running on autopilot — %s.", len(ids), verb, strings.Join(ids, ", "))
}

// resumeAfterTopUp restarts a card that had stopped for want of budget,
// once its envelope has been raised and the user has said yes to the
// separate resume question.
//
// It re-reads the card rather than reusing the row the dialog was built
// from: that snapshot still carries the old, exhausted envelope, and
// resuming against it would put the run straight back into the wall it
// just stopped at.
func (m *Shell) resumeAfterTopUp(id domain.FeatureID) tea.Cmd {
	if m.store == nil {
		return nil
	}
	f, err := m.store.GetFeature(context.Background(), id)
	if err != nil {
		return func() tea.Msg { return noticeMsg{text: sanitize(err.Error()), isErr: true} }
	}
	return tea.Batch(
		m.resumeCard(f),
		func() tea.Msg { return noticeMsg{text: string(id) + ": envelope raised — resuming", reload: true} },
	)
}

// handleKey routes one key press. Its shape is the keymap's tiers made
// literal, read top to bottom:
//
//	tier 1  alt+1/2/3 — answered here, above every surface, so a tab is
//	        always one keystroke away no matter what holds the keyboard.
//	        (ctrl+c is hoisted higher still, above the overlay stack, in
//	        update.) These used to be answered in boardKey, below the
//	        early returns for chat/spec/diff/…, which meant they did
//	        nothing at all from inside a view — you had to esc out first.
//	tier 2  tab and ? — the two grammar keys this level owns. The rest of
//	        the grammar (j/k, enter, esc) means the same thing everywhere
//	        because each surface binds it the same way, not because it is
//	        intercepted; these two are the ones no surface may redefine.
//	tier 3  everything below — the active surface's own verbs.
//
// The hosted CLI on the agent tab is the one deliberate exception: it
// owns its whole keymap, so past the tier-1 switch every key goes to the
// child. Taking tab or ? from it would break keys its users need.
func (m *Shell) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	// A locked keyboard is answered before anything: locked means gummi
	// keeps nothing but ctrl+g, which update already took above the
	// overlay. Even the tier-1 tab switches go to the hosted CLI, which
	// is the point — it is how its own tab completion, ? and esc reach it.
	if m.keyboardLocked() {
		return m.agentKey(msg)
	}
	switch key {
	case "alt+1":
		return m.gotoTab(TabBoard)
	case "alt+2":
		return m.gotoTab(TabInbox)
	case "alt+3":
		return m.gotoTab(TabAgent)
	case "alt+/":
		// the help key that is always gummi's. ? is the convenient one,
		// but it is also ordinary punctuation, so it has to yield wherever
		// the user is typing prose — the chat box, the bug-import filter,
		// the hosted CLI. Those are exactly the surfaces whose key rules
		// are least guessable, which left the help unreachable in the
		// three places it was most wanted. alt is the prefix for keys a
		// multiplexer or a hosted pty won't have claimed (DESIGN).
		m.Overlay.Push(m.helpOverlay())
		return nil
	}
	if key == "tab" {
		return m.nextTab()
	}
	// An unlocked foreign tab keeps everything gummi did not just claim.
	// That is a short list on purpose — the tab switches and nothing else
	// — so typing at the agent works without a mode, and ?, esc, enter and
	// ctrl+c all land where the user is looking. Only tab is gummi's, and
	// ctrl+g is how you hand that one over too.
	if m.hostedKeyboard() {
		return m.agentKey(msg)
	}
	if key == "?" && !m.textEntry() {
		m.Overlay.Push(m.helpOverlay())
		return nil
	}
	// tier 3: whichever surface owns the main pane, in the same order
	// mainView paints them, and only on the tab they belong to.
	if m.boardSurfacesLive() {
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
		if m.deps != nil {
			return m.handleDepsKey(key)
		}
		if m.cardOpen && m.threadInput.Focused() {
			return m.handleThreadInputKey(msg)
		}
	}
	// q quits only from the board root: every surface above answers it as
	// an alias for esc, and a q that quit gummi from inside a spec would
	// be far worse than one that doesn't.
	if key == "q" {
		return m.quitCmd()
	}
	if !m.attached() {
		return nil
	}
	return m.boardKey(key)
}

// boardSurfacesLive reports whether the board tab's own overlaying
// surfaces — a chat, a spec, a diff, an ingest review, a bug import, the
// dependency picker, the live ingest feed — should be drawn and fed the
// keyboard.
//
// They are scoped to the board tab on purpose. Each one belongs to a
// card, and a card belongs to the board. mainView, draw and handleKey
// used to test them *before* m.tab, so with a chat open, switching to
// the inbox still rendered the chat and still handed it every key. That
// was unreachable while switching tabs from a chat was impossible; the
// tab keys going global is exactly what exposes it.
//
// Hidden while you are elsewhere, restored when you come back — never
// discarded. A chat holds an unsent input buffer, and throwing that away
// on a tab switch would be its own, worse bug.
func (m *Shell) boardSurfacesLive() bool { return m.tab == TabBoard }

// hostedKeyboard reports whether a hosted program is on screen and able
// to take keys — a foreign tab (tabs.go) with a live child.
func (m *Shell) hostedKeyboard() bool {
	return m.foreignTab(m.tab) && m.agent != nil
}

// keyboardLocked reports whether the keyboard is handed wholesale to the
// hosted program. The lock is only real where there is something to hand
// it to: a lock left set over a dead child would swallow every key with
// nothing to receive them, which is unrecoverable rather than modal.
func (m *Shell) keyboardLocked() bool { return m.locked && m.hostedKeyboard() }

// toggleLock flips the keyboard lock, refusing where there is nothing to
// lock. The refusal says what the key is for rather than doing nothing:
// ctrl+g is reserved everywhere, so a user who presses it on the board
// has already decided it means something and deserves to learn what.
func (m *Shell) toggleLock() {
	if !m.hostedKeyboard() {
		m.notice = noticeMsg{text: "ctrl+g locks the agent tab — nothing to lock here"}
		return
	}
	m.locked = !m.locked
	m.lockUsed = true
	m.clearTransientNotice()
}

// Notices that name the lock. Both stay under noticeThreshold so they
// ride as a quiet status pill rather than taking rows off the pane —
// this is an offer, and an offer that reformats the screen is a nag.
const (
	// lockOfferNotice greets an arrival at a hosted tab: said before the
	// user reaches for a key gummi is holding, which is the only time
	// saying it is any use.
	lockOfferNotice = "ctrl+g hands tab, alt+N + mouse to the agent"
	// lockLeftNotice explains the surprise in the other direction — tab
	// was pressed at a CLI prompt and moved the user instead of
	// completing. It names the key that would have done what they meant.
	lockLeftNotice = "tab left the agent — ctrl+g keeps it there"
)

// offerLock names the lock on arrival at a hosted tab, while the user
// has yet to use it. Silent once locked (the indicator says it far
// better) and silent with no child, where there is nothing to offer.
func (m *Shell) offerLock() {
	if m.lockUsed || m.locked || !m.hostedKeyboard() {
		return
	}
	m.notice = noticeMsg{text: lockOfferNotice}
}

// gotoTab switches to t and does the arrival work every route into a tab
// shares, so alt+N and the tab cycle cannot drift apart on it.
func (m *Shell) gotoTab(t Tab) tea.Cmd {
	m.setTab(t)
	if !m.foreignTab(m.tab) {
		return nil
	}
	// spawning is deferred to the first visit so a board that never
	// opens the tab never pays for a pty or a CLI process.
	cmd := m.ensureAgent()
	m.offerLock()
	return cmd
}

// textEntry reports whether the surface holding the keyboard is taking
// free-form text right now. Only ? consults it: every other global is a
// modifier chord (alt+N) or a key no text field wants (tab), but a
// question mark is ordinary punctuation, and a user typing "should we
// retry?" into a chat must get the character rather than the help
// overlay. The agent tab is not listed because it never reaches here —
// hostedKeyboard hands the hosted CLI every key gummi has not claimed,
// ? among them.
func (m *Shell) textEntry() bool {
	if !m.boardSurfacesLive() {
		return false
	}
	if m.cardOpen && m.threadInput.Focused() {
		return true
	}
	return m.bugIngest != nil && m.bugIngest.filtering
}

// boardKey answers the board's keys. It is split out from handleKey so
// the card action list and the command menu can invoke an action by name
// without a second copy of the guards each case carries (a research card
// refusing a merge, a card with no worktree refusing a diff). Both paths
// funnel through here, so what a surface offers and what the handler
// does cannot drift apart.
func (m *Shell) boardKey(key string) tea.Cmd {
	// tab, alt+1/2/3 and ? never arrive here: handleKey answers them above
	// every surface, which is what makes them global rather than "global
	// as long as nothing is open".
	if m.tab == TabInbox {
		return m.inboxKey(key)
	}
	if m.tab != TabBoard {
		// A foreign tab that reaches this far has no live child — the CLI
		// failed to start, or none is chosen yet (a live one is answered
		// in handleKey, which still has the real KeyPressMsg it needs).
		// gummi holds the keyboard here but has nothing to spend it on,
		// and "nothing" is the answer: this used to fall through to the
		// inbox's keymap, so on the agent tab x silently dismissed an
		// inbox item, enter jumped to a card and switched tabs, and u
		// topped up a budget — all from a tab showing none of it.
		return nil
	}
	// reconcile before anything can act: m.sel is written from half a
	// dozen places (the attention cycle, the inbox jump, pgup/pgdn, a
	// reload) and a cursor left over from another card would otherwise
	// run its action against this one. Idempotent, so the per-site calls
	// stay for rendering and this is the backstop for correctness.
	m.syncActionFocus()
	// the board tab answers movement, enter and esc itself (there is one
	// list on screen at a time — the backlog, or the card page it opens);
	// everything it doesn't claim is a board verb, unchanged.
	if cmd, handled := m.backlogKey(key); handled {
		return cmd
	}
	if key == " " || key == "space" {
		m.Overlay.Push(newCommandMenu(m.globalCommands(), m.runCommand))
		return nil
	}
	// On the card page pgup/pgdn scroll the conversation. On the board
	// they still jump to the first and last card — but the card page
	// already steps cards with J/K, so the pair is free here, and a long
	// thread has nothing else to reach its history with.
	if m.cardOpen && (key == "pgup" || key == "pgdown") {
		m.scrollThread(key == "pgup")
		return nil
	}
	if key == "/" && m.cardOpen {
		// The card page's own route into the thread's input
		// (threadinput.go's doc comment): consumed here, not inserted, the
		// same convention bugIngestView's own "/" uses to focus its filter.
		m.focusThreadInput()
		return nil
	}
	return m.boardVerb(key)
}

// threadSize is the width and height the card thread is rendered into:
// the main pane less the card page's own breadcrumb row (backlog.go's
// cardPageView). The key handler needs it to page by a screenful, and
// getting it from the layout rather than a remembered number keeps a
// resize from leaving the scroll step stale.
func (m *Shell) threadSize() (int, int) {
	main := m.computeLayout().Main
	crumb, blank := cardPageChrome(main.Dy())
	return main.Dx(), max(main.Dy()-crumb-blank, 1)
}

// scrollThread pages the card thread's body. The step is the visible body
// height rather than a fixed number of lines, so a page means what it
// says on a phone-sized terminal and on a tall one alike.
//
// Scrolling is clamped at both ends: at the newest it stays at zero, so
// the view keeps tracking a live stage, and at the oldest it stops on
// the first line instead of paging into blank space.
func (m *Shell) scrollThread(up bool) {
	w, h := m.threadSize()
	step := max(h-1, 1)
	if up {
		m.threadScroll = min(m.threadScroll+step, m.maxThreadScroll(w, h))
		return
	}
	m.threadScroll = max(m.threadScroll-step, 0)
}

// boardVerb performs a board action. It is the layer below boardKey's
// focus handling, so an action invoked by name (from the card list or the
// command menu) reaches the same guarded case body as its key without
// passing back through the focus interception.
func (m *Shell) boardVerb(key string) tea.Cmd {
	// a card another gummi process is driving is read-only here: refuse
	// the verbs that would write to it and name the owner, rather than
	// racing the other process or failing deeper in with a confusing
	// error. The action list withholds exactly this set, so what the
	// board offers and what it answers stay in lockstep.
	if r, ok := m.selected(); ok && r.DrivenAbroad && foreignBlockedKeys[key] {
		m.notice = noticeMsg{
			text:  fmt.Sprintf("%s is being driven by pid %d — read-only here (enter watches it)", r.F.ID, r.Foreign.PID),
			isErr: true,
		}
		return nil
	}
	switch key {
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
			// p pauses the card's own autonomous session (running, queued,
			// or a finished one p can park) — the existing pause binding —
			// and otherwise opens the dependency picker for the selected card.
			if s := m.sessionFor(r.F.ID); s != nil && !s.Interactive {
				return m.pauseRun(r.F)
			}
			m.clearTransientNotice()
			return m.openDeps(r.F)
		}
	case "v":
		if r, ok := m.selected(); ok {
			return m.runChecks(r.F)
		}
	case "t":
		if r, ok := m.selected(); ok {
			m.clearTransientNotice()
			return m.openTranscript(r.F)
		}
	case "s":
		if r, ok := m.selected(); ok {
			m.clearTransientNotice()
			return m.openSpec(r.F)
		}
	case "d":
		if r, ok := m.selected(); ok {
			if !workflow.NeedsWorktree(r.F.Kind, r.F.Stage) {
				m.notice = noticeMsg{text: string(r.F.ID) + ": no diff — research cards carry no branch"}
				return nil
			}
			m.clearTransientNotice()
			return m.openDiff(r.F)
		}
	case "a":
		if r, ok := m.selected(); ok {
			return m.attachRaw(r.F)
		}
	case "A":
		if r, ok := m.selected(); ok {
			if r.DrivenAbroad {
				m.notice = noticeMsg{
					text:  fmt.Sprintf("%s is being driven by pid %d — read-only here (enter watches it)", r.F.ID, r.Foreign.PID),
					isErr: true,
				}
				return nil
			}
			return m.openAutopilot(r.F)
		}
	case "agent-cli":
		// Menu-only, and dispatched on an id rather than a letter on
		// purpose: A already means "approve the gate" in the spec, diff
		// and ingest views, and choosing a CLI is a rare action that does
		// not deserve a board key which reads as approve everywhere else.
		// Unlike every other case here it also belongs to no card, so it
		// works with nothing selected (an empty board's splash answers
		// space too).
		return m.openAgentPickerCmd()
	case "j", "down":
		m.moveSel(1)
	case "k", "up":
		m.moveSel(-1)
	case "pgup":
		if order := m.displayOrder(m.sortMode); len(order) > 0 {
			m.sel = order[0]
			m.syncActionFocus()
		}
	case "pgdown":
		if order := m.displayOrder(m.sortMode); len(order) > 0 {
			m.sel = order[len(order)-1]
			m.syncActionFocus()
		}
	case "n":
		m.Overlay.Push(newFeatureForm(m.profileNames, m.repoNames, m.repoHasDefault(), m.envelope, m.createFeature))
	case "B":
		m.Overlay.Push(newBugForm(m.profileNames, m.repoNames, m.repoHasDefault(), m.envelope, m.createBug))
	case "R":
		m.Overlay.Push(newRSForm(m.profileNames, m.repoNames, m.repoHasDefault(), m.envelope, m.createResearch))
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
		m.Overlay.Push(newIngestForm(m.profileNames, m.repoNames, m.repoHasDefault(), m.startIngest))
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
			if r.F.Kind == domain.KindResearch && r.F.Stage == domain.StageDone {
				// FD-081: a done RS card has nothing left to advance — g
				// re-runs decompose instead, the board-key counterpart to
				// the headless --request-changes re-run.
				return m.startDecomposeReRun(r.F)
			}
			return m.advanceStage(r.F.ID)
		}
	case "b":
		if r, ok := m.selected(); ok {
			return m.bounceStage(r.F.ID, "")
		}
	case "u":
		if r, ok := m.selected(); ok {
			m.Overlay.Push(newEnvelopeDialog(r.F, func(to int) tea.Cmd {
				return m.setEnvelope(r.F.ID, to)
			}, func() tea.Cmd {
				return m.resumeAfterTopUp(r.F.ID)
			}))
		}
	case "o":
		if r, ok := m.selected(); ok {
			if workflow.NeedsWorktree(r.F.Kind, r.F.Stage) || r.HasWorktree {
				m.notice = noticeMsg{text: string(r.F.ID) + ": repo is fixed once a worktree exists", isErr: true}
				return nil
			}
			if len(m.repoNames) == 0 {
				m.notice = noticeMsg{text: "no other repositories configured"}
				return nil
			}
			m.Overlay.Push(newRepoPickerDialog(r.F, m.repoNames, func(repo string) tea.Cmd {
				return m.setRepo(r.F.ID, repo)
			}))
		}
	case "P":
		if r, ok := m.selected(); ok {
			return m.routeViaPlan(r.F.ID)
		}
	case "r":
		if r, ok := m.selected(); ok {
			if !workflow.NeedsWorktree(r.F.Kind, r.F.Stage) {
				m.notice = noticeMsg{text: string(r.F.ID) + ": no rebase — research cards carry no branch"}
				return nil
			}
			return m.rebaseFeature(r.F)
		}
	case "m":
		if r, ok := m.selected(); ok {
			if !workflow.NeedsWorktree(r.F.Kind, r.F.Stage) {
				m.notice = noticeMsg{text: string(r.F.ID) + ": no merge — research cards carry no branch"}
				return nil
			}
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
	case "z":
		if r, ok := m.selected(); ok {
			if !workflow.NeedsWorktree(r.F.Kind, r.F.Stage) {
				m.notice = noticeMsg{text: string(r.F.ID) + ": no squash — research cards carry no branch"}
				return nil
			}
			if !r.HasWorktree {
				m.notice = noticeMsg{text: string(r.F.ID) + " has no worktree yet (created at spec approval)", isErr: true}
				return nil
			}
			if r.Landed {
				m.notice = noticeMsg{text: string(r.F.ID) + " already landed on main — press c to clean up", isErr: true}
				return nil
			}
			if m.squashPrep {
				m.notice = noticeMsg{text: string(r.F.ID) + " already preparing a squash — wait for it", isErr: true}
				return nil
			}
			m.squashPrep = true
			m.notice = noticeMsg{text: string(r.F.ID) + ": preparing squash…"}
			return m.prepareSquash(r.F)
		}
	case "c":
		if r, ok := m.selected(); ok {
			if !workflow.NeedsWorktree(r.F.Kind, r.F.Stage) {
				m.notice = noticeMsg{text: string(r.F.ID) + ": no cleanup — research cards carry no branch"}
				return nil
			}
			if !r.Landed {
				m.notice = noticeMsg{text: string(r.F.ID) + " hasn't landed on main yet", isErr: true}
				return nil
			}
			f := r.F
			m.Overlay.Push(&confirmDialog{
				id:           "confirm-cleanup",
				cancelLabel:  "Keep",
				confirmLabel: "Clean up",
				question:     "clean up " + string(f.ID) + "?",
				detail:       "removes the worktree (incl. untracked files) and merged branch — keeps the record",
				onConfirm:    func() tea.Cmd { return m.cleanupLanded(f) },
			})
		}
	// D, not x: x is the reversible one on every other surface (resolve a
	// comment, dismiss an inbox item, drop a proposal, remove a
	// dependency), and the board was the single place it destroyed work.
	// Uppercase for what cannot be undone, matching the diff view's
	// existing x-resolves / D-deletes pair.
	case "D":
		if r, ok := m.selected(); ok {
			f := r.F
			m.Overlay.Push(&confirmDialog{
				id:           "confirm-delete",
				cancelLabel:  "Keep",
				confirmLabel: "Delete",
				question:     "delete " + string(f.ID) + "?",
				detail:       f.Title + " — removes worktree, branch, and record",
				onConfirm:    func() tea.Cmd { return m.deleteFeature(f.ID) },
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
	m.syncActionFocus()
}

// jumpSel selects the nth visible card (1-based), matching the numbers
// shown on the board.
func (m *Shell) jumpSel(n int) {
	order := m.displayOrder(m.sortMode)
	if n >= 1 && n <= len(order) {
		m.sel = order[n-1]
		m.syncActionFocus()
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

// selectedID names the card the cursor is on, or "" when the board has
// no rows yet.
func (m *Shell) selectedID() domain.FeatureID {
	if m.sel >= 0 && m.sel < len(m.rows) {
		return m.rows[m.sel].F.ID
	}
	return ""
}

// restoreSel puts the cursor back on the card it was on before a reload,
// by identity rather than by position.
//
// m.sel indexes m.rows, and a reload can insert, remove or reorder rows
// underneath it — a card created, one deleted, a headless run moving one
// between groups — which slid the cursor onto a different card with no
// keypress. Clamping the index to the new length, which is all this used
// to do, keeps it in range and says nothing about whether it still points
// at what the user was looking at.
//
// When the card really is gone the cursor falls to the top of the list.
func (m *Shell) restoreSel(id domain.FeatureID) {
	if id != "" {
		for i, r := range m.rows {
			if r.F.ID == id {
				m.sel = i
				return
			}
		}
	}
	m.selectFirstDisplayed()
}

// selectFirstDisplayed puts the cursor on the first card in the order the
// board paints (displayOrder: todo, then in progress, research, review
// and verify, with done last) rather than on m.rows[0].
//
// Those are not the same row and were never meant to be. m.rows arrives
// ORDER BY num, so row zero is the lowest-numbered card — the oldest one
// — and on any board with a bit of history the oldest card is finished.
// So the board opened with the cursor parked on done work, at the bottom
// of a list it had scrolled past everything to reach.
func (m *Shell) selectFirstDisplayed() {
	order := m.displayOrder(m.sortMode)
	if len(order) == 0 {
		m.sel = 0
		return
	}
	m.sel = order[0]
}

// computeLayout carves the terminal into the tab bar, the main pane, and
// the status bar (layout.Compute) — the same three rows regardless of
// which tab is active; only the main pane's content changes (mainView).
func (m *Shell) computeLayout() layout.Layout {
	return layout.Compute(m.width, m.height)
}

// boardPaneFocused reports whether the board pane owns the arrow keys —
// nothing has taken over the main pane, and focus has not moved right
// into the selected card's action list. It mirrors activeSurface's
// precedence (keymap.go): whatever surface answers the keys is the one
// that gets to look focused.
func (m *Shell) boardPaneFocused() bool {
	if m.actionFocused {
		return false
	}
	return m.spec == nil && m.diff == nil && m.ingest == nil &&
		m.bugIngest == nil && m.deps == nil && (m.ingestRun == nil || m.ingestRun.hidden)
}

// View implements tea.Model: compute the buffer, paint the panes, the
// status bar, then the dialog stack.
func (m *Shell) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.BackgroundColor = m.styles.Theme.BgBase
	v.WindowTitle = "gummi"
	// Mouse reporting is requested per-frame, and only while the input
	// lock is on. Asking for it unconditionally would suppress the
	// terminal's own click-drag selection across the whole program — a
	// steep price on surfaces that have no use for a mouse at all, and
	// on an agent tab whose CLI may not want one either.
	if m.keyboardLocked() {
		v.MouseMode = tea.MouseModeCellMotion
	}

	if m.width <= 0 || m.height <= 0 {
		return v
	}
	canvas := uv.NewScreenBuffer(m.width, m.height)
	m.draw(&canvas)
	paintBase(canvas.Buffer, m.styles.Theme.BgBase)

	content := strings.ReplaceAll(canvas.Render(), "\r\n", "\n")
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	v.Content = strings.Join(lines, "\n")
	v.Cursor = m.agentCursor()
	return v
}

// paintBase gives every cell of a finished frame the theme's base
// background.
//
// The frame asks for its background once, as tea.View.BackgroundColor —
// an OSC 11 that requests BgBase as the terminal's *default* background.
// A terminal that ignores that request (tmux swallows OSC 11, and the
// board runs inside tmux) is then left to fill, in its own default
// background, every cell we handed it without an explicit one: the pad
// past the end of a line, and every span the renderer clears with EL/ECH
// after a style reset. That is the black bar trailing a transcript line
// — the text there carries a foreground and no fill, so the erase behind
// it runs on the terminal's black rather than on ours, and the bar stops
// exactly where the line's last glyph does.
//
// Carrying the fill on the cells themselves makes the background ours in
// every terminal, whether or not OSC 11 lands. Cells that already chose a
// background (bands, pills, the hosted agent's own paint) keep it.
func paintBase(b *uv.Buffer, bg color.Color) {
	if b == nil || bg == nil {
		return
	}
	for y := range b.Height() {
		for x := range b.Width() {
			c := b.CellAt(x, y)
			// a zero cell is a wide glyph's placeholder, not a cell of
			// its own: styling it would render it as a second glyph.
			if c == nil || c.IsZero() || c.Style.Bg != nil {
				continue
			}
			c.Style.Bg = bg
		}
	}
}

func (m *Shell) draw(scr uv.Screen) {
	s := m.styles
	l := m.layout

	uv.NewStyledString(m.tabBarView(l.Tabs.Dx())).Draw(scr, l.Tabs)
	// a long error/remedy is wrapped into a band above the status bar
	// rather than truncated into a one-line pill ("set permiss…"); it
	// borrows the bottom rows of the main pane. Short notices stay pills.
	// the agent tab paints cells, not a string: its emulator composites
	// straight into scr so the hosted CLI's own truecolor survives. Every
	// other surface goes through mainView below.
	if m.hostedKeyboard() {
		m.drawAgentTab(scr)
		uv.NewStyledString(m.statusView(l.Status.Dx())).Draw(scr, l.Status)
		uv.NewStyledString(m.tabBarView(l.Tabs.Dx())).Draw(scr, l.Tabs)
		m.Overlay.Draw(scr, l.Area, s)
		return
	}
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

// attachOrRun handles `enter`: interactive stages attach the agent into
// the card's thread (the conversation's surface); autonomous stages
// start (or watch) an autonomous run.
func (m *Shell) attachOrRun(f domain.Feature) tea.Cmd {
	// another process owns this card: the only thing this board can
	// honestly do with enter is watch it.
	if _, ok := m.foreignFor(f.ID); ok {
		return m.watchForeign(f)
	}
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

// attachChat attaches a feature's engine session, starting (or reusing)
// it. Attach spawns the agent backend, which can take seconds, so it
// runs in a command (never in Update — see the no-IO-in-Update contract
// above). The card page is the conversation's surface — chatAttachedMsg
// lands on it with the composer ready; the pane this used to open is
// retired into the thread (DESIGN §10.5).
func (m *Shell) attachChat(f domain.Feature) tea.Cmd {
	return func() tea.Msg {
		s, err := m.engine.Attach(context.Background(), f)
		return chatAttachedMsg{feature: f, session: s, err: err}
	}
}

// attachChatWith attaches the interactive stage's session and delivers
// the composer's line as the conversation's first turn — the prose aimed
// at the decision's run answer. Attach runs in a command (the backend
// can take seconds to spawn), so the line rides the same closure, the
// way the review-comments path attaches-then-sends (annotate.go); the
// card page shows the conversation when chatAttachedMsg lands, exactly
// as a plain attach's does.
func (m *Shell) attachChatWith(f domain.Feature, opening string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		s, err := m.engine.Attach(ctx, f)
		if err != nil {
			return chatAttachedMsg{feature: f, session: s, err: err}
		}
		if err := m.engine.Send(ctx, f.ID, opening); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return chatAttachedMsg{feature: f, session: s}
	}
}

// seedRounds hydrates the in-memory round counter for kind from the store
// on loop entry, so a resumed (or relaunched) loop resumes with the rounds
// already burned instead of a fresh budget. A failed read returns the
// error and leaves the fast-path map untouched; the caller aborts dispatch
// rather than proceeding on a guessed-zero count.
func (m *Shell) seedRounds(f domain.Feature, kind domain.RoundKind) error {
	n, err := rounds.Load(context.Background(), m.roundStore, f.ID, kind)
	if err != nil {
		return err
	}
	m.setRound(f.ID, kind, n)
	return nil
}

// runStage enqueues an autonomous run for a feature's stage; the engine
// schedules and kicks it off. Activity streams into the thread's live
// stage block; `p` pauses it. On an already-running session, enter opens
// the card page as the observer: the full scrollable transcript is the
// thread's body now, with steering via the composer.
func (m *Shell) runStage(f domain.Feature) tea.Cmd {
	return m.runStageWithNote(f, "")
}

// runStageWithNote is runStage with a note appended to the stage kickoff:
// the composer's prose aimed at the run (a decision's run answer) or a
// bounce's stashed note. engine.RunWith is Run's note-carrying path —
// the kickoff the fresh session opens with says what it starts from.
func (m *Shell) runStageWithNote(f domain.Feature, note string) tea.Cmd {
	// entering the plan stage hydrates the loop's round counter from the
	// store, so a resumed plan resumes with the rounds already burned. A
	// failed read aborts dispatch rather than guessing at a fresh budget.
	if f.Stage == domain.StagePlan {
		if err := m.seedRounds(f, domain.RoundKindPlan); err != nil {
			m.notice = noticeMsg{text: sanitize(err.Error()), isErr: true}
			m.raiseAttention(f.ID, attnFailure, sanitize(err.Error()))
			return nil
		}
	}
	// the review loop spans review → work(fix/investigate) → review, and
	// any of those can be the resume landing point, so seed the counter on
	// each of them. Investigate is research's work leg — a resume landing
	// on the RS work leg must not re-grant the review budget either.
	if f.Stage == domain.StageReview || f.Stage == domain.StageImplement ||
		f.Stage == domain.StageFix || f.Stage == domain.StageInvestigate {
		if err := m.seedRounds(f, domain.RoundKindReview); err != nil {
			m.notice = noticeMsg{text: sanitize(err.Error()), isErr: true}
			m.raiseAttention(f.ID, attnFailure, sanitize(err.Error()))
			return nil
		}
	}
	if s := m.engine.Get(f.ID); s != nil {
		switch s.State() {
		case engine.StateRunning:
			// already running: the thread is the watch surface. Opening
			// the card page (when it somehow isn't) is all there is to do
			// — the pane enter used to attach as an observer is gone. It
			// opens through openCard, not by setting the flag, so the
			// history the live block sits on top of is loaded too.
			if !m.cardOpen {
				return m.openCard()
			}
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
						return noticeMsg{text: cardLockedNotice(f.ID, err), isErr: true}
					}
					return noticeMsg{text: string(f.ID) + " resuming plan critique (plan already written)", clearInbox: f.ID}
				}
			}
		}
	}
	// A stashed bounce note rides the reborn work stage's kickoff — the
	// only delivery the rewind's note has (shell.go's bounceNotes), the
	// driver's --bounce note lifetime in miniature. A note the decision's
	// own answer carries wins this kickoff; the stash then still rides
	// the next one rather than being silently dropped.
	if note == "" {
		if stashed, ok := m.bounceNotes[f.ID]; ok &&
			(f.Stage == domain.StageImplement || f.Stage == domain.StageFix) {
			delete(m.bounceNotes, f.ID)
			note = stashed
		}
	}
	// Run schedules and spawns the backend synchronously; do it in a command
	// so a slow agent launch can't freeze the TUI.
	return func() tea.Msg {
		if err := m.engine.RunWith(f, note); err != nil {
			return noticeMsg{text: cardLockedNotice(f.ID, err), isErr: true}
		}
		text := string(f.ID) + " queued"
		if note != "" {
			text += " — your line rides the kickoff"
		}
		return noticeMsg{text: text, clearInbox: f.ID}
	}
}

// openTranscript opens the card thread's transcript view: every stage
// segment renders its events instead of one folded line, and the whole
// run reads in the body — tool calls with their captured outputs
// included. It never starts or re-runs anything; on a card page it
// toggles back to the folded view. A card another process is driving
// opens the read-only live view instead — the store's transcript for
// such a card is a turn behind, when it exists at all.
func (m *Shell) openTranscript(f domain.Feature) tea.Cmd {
	if _, ok := m.foreignFor(f.ID); ok {
		// the live file is the only transcript of a run this process does
		// not own; the store's copy lags a whole turn behind.
		return m.watchForeign(f)
	}
	if m.cardOpen {
		if r, ok := m.selected(); ok && r.F.ID == f.ID {
			// already on the page: the key toggles the transcript view
			m.threadTranscript = !m.threadTranscript
			return nil
		}
	}
	// from the list (or from another card's page) t opens the page with
	// the view already on — openCard resets it to folded, so the flag is
	// set after, and its event-log load is the command that carries.
	cmd := m.openCard()
	m.threadTranscript = true
	return cmd
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

// cardEventsMsg delivers one card's event log (state.CardEvent), loaded
// by loadCardEvents.
type cardEventsMsg struct {
	id     domain.FeatureID
	events []state.CardEvent
	err    error
}

// loadCardEvents reads one card's event log from the store — the
// thread's folded stage receipts and live-stage fallback (thread.go).
// Fired only for the selected card, when the card page opens and when
// J/K moves the selection on it, never for the whole board: an unbounded
// per-card read on every row would be exactly the IO-per-frame the row
// snapshot in msgs.go exists to avoid. A detached shell (no store, as in
// several UI tests) has nothing to read, so it returns nil rather than a
// command that would panic on m.store.
func (m *Shell) loadCardEvents(id domain.FeatureID) tea.Cmd {
	if m.store == nil {
		return nil
	}
	return func() tea.Msg {
		evs, err := m.store.Events(context.Background(), id)
		return cardEventsMsg{id: id, events: evs, err: err}
	}
}

// openInbox switches to the needs-attention tab. It used to push a modal
// dialog (inbox_dialog.go); that dialog is gone now that the queue is a
// first-class tab, so this is just the tab switch — kept as its own
// function because `i` (boardVerb) still names it, not setTab directly.
func (m *Shell) openInbox() {
	m.setTab(TabInbox)
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

// setRepo durably changes a feature's managed repository. It is the
// command backing the o repo picker; the board reloads on success so the
// repo badge re-renders immediately.
func (m *Shell) setRepo(id domain.FeatureID, repo string) tea.Cmd {
	return func() tea.Msg {
		updated, err := m.engine.SetRepo(context.Background(), id, repo)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		label := updated.Repo
		if label == "" {
			label = "default"
		}
		return noticeMsg{text: fmt.Sprintf("%s: repo set to %s", id, label), reload: true}
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

// setGateApproval persists a card's gate-approval mode — the write half
// of the autopilot overlay's confirm (autopilot.go's startAutopilot).
// Like SetVerifiedAt/SetGateApproval on the store side
// this is a side-channel write: it touches neither a session nor the
// stage, so — unlike deleteFeature/cleanupLanded — it carries no card
// lock, the same call shape as setRepo and setEnvelope above.
func (m *Shell) setGateApproval(id domain.FeatureID, mode string) tea.Cmd {
	return func() tea.Msg {
		if err := m.store.SetGateApproval(context.Background(), id, mode); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		label := "attended — caller approves each design gate"
		if mode == domain.GateGates {
			label = "unattended — design gates auto-approve"
		}
		return noticeMsg{text: fmt.Sprintf("%s: gate approval now %s", id, label), reload: true}
	}
}

// autonomousStage reports whether a stage runs an autonomous agent
// (as opposed to interactive chat or no agent).
func autonomousStage(s domain.Stage) bool {
	switch s {
	case domain.StagePlan, domain.StageImplement, domain.StageFix, domain.StageInvestigate, domain.StageReview, domain.StageVerify:
		return true
	default:
		return false
	}
}

func (m *Shell) mainView(w, h int) string {
	if m.boardSurfacesLive() {
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
		if m.deps != nil {
			return m.depPickerView(w, h)
		}
		if m.ingestRun != nil && !m.ingestRun.hidden {
			return m.ingestRunRender(w, h)
		}
	}
	switch m.tab {
	case TabAgent:
		// only ever the placeholder: a live child is composited by
		// drawAgentTab, which bypasses this string path entirely.
		return m.agentTabPlaceholder(w, h)
	case TabInbox:
		return m.inboxView(w, h)
	}
	if len(m.rows) > 0 {
		// the board tab owns the whole pane: the backlog list, or one
		// card's page opened out of it.
		if m.cardOpen {
			return m.cardPageView(w, h)
		}
		return m.backlogView(w, h)
	}
	return logo.Splash(m.styles, m.version, w, h)
}

func (m *Shell) statusView(w int) string {
	// the leading pill normally just names the program. While the
	// keyboard is locked it says so instead, in the alert weight: the
	// lock changes what every other key does, and the one place a user
	// already looks to find out what a key will do is this row.
	mode := statusbar.Pill{Text: "gummi", Kind: statusbar.KindMode}
	if m.keyboardLocked() {
		mode = statusbar.Pill{Text: "⬤ locked · ctrl+g", Kind: statusbar.KindAlert}
	}
	pills := []statusbar.Pill{
		mode,
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
	if m.squashPrep {
		pills = append(pills, statusbar.Pill{Text: m.spinner() + " squashing", Kind: statusbar.KindNeutral})
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
