package ui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/firstrun"
	"github.com/morphis/gummi/internal/notify"
	"github.com/morphis/gummi/internal/pr"
	"github.com/morphis/gummi/internal/publish"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/layout"
	"github.com/morphis/gummi/internal/ui/logo"
	"github.com/morphis/gummi/internal/ui/overlay"
	"github.com/morphis/gummi/internal/ui/statusbar"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/webapi"
	"github.com/morphis/gummi/internal/worktree"
)

// Shell is gummi's top-level Bubble Tea model. It owns the screen
// buffer, the rectangle layout, the style set, the dialog stack, and —
// once attached to a workspace — the board state. Panes render to
// strings and are painted into their rects (the Crush hybrid pattern);
// all IO runs in commands, never in Update or View.
type Shell struct {
	// curatorErr is the curator's last tick error, said once until it
	// changes: a curator that cannot run is not a reason to repeat it on
	// every poll, nor to hide what the schedules did.
	curatorErr string
	styles     *theme.Styles
	version    string

	width, height int
	layout        layout.Layout

	// Dialogs (gate prompts, forms) live on this stack.
	Overlay overlay.Stack

	// workspace wiring (nil store means detached: splash only)
	store *state.Store
	wt    *worktree.Pool
	// baseBranches names, per repo (the empty key is the workspace
	// default), the branch that repo's main checkout has out — the branch
	// a card lands on. Read in Attach and again by each repository read
	// (installRepos): it is copy, it is wanted in render paths that must not
	// shell out to git, and a trunk rarely changes under a running board.
	// Read it through baseBranch, never directly — a missing entry has to
	// answer with a name.
	baseBranches map[string]string
	ws           state.Workspace
	// name is what this instance is called (config.yaml's name), "" when
	// unnamed.
	name string
	// repoSeq issues the sequence numbers of repository reads (nextRepoSeq);
	// repoInstalled is the newest one installed (installRepos). Both are
	// what keep an older read from overwriting a newer one.
	repoSeq atomic.Uint64
	// repoMu guards baseBranches and repoBranches: installRepos writes them
	// on the loop while loadRows, an off-loop read, resolves each card's
	// trunk (repoBaseBranch).
	repoMu        sync.RWMutex
	repoInstalled uint64

	rows []featureRow
	// children are the hidden child cards of the sessions on the board (a
	// workflow step's), by id: never rows, but the web face reads a step's
	// thread by its card (webrun.go).
	children map[domain.FeatureID]domain.Feature
	// webStep is what the web last saw of each step card's session
	// (emitStepChange), so a delta that moves nothing in its run's block
	// does not send its session's page to read it again.
	webStep map[domain.FeatureID]string
	sel     int
	// tab picks which of gummi's top-level tabs owns the main pane
	// (tabs.go): board, stats, or inbox. cardOpen is the board tab's own
	// page-within-a-tab — the selected card opened full width — and
	// belongs to no other tab.
	tab      Tab
	cardOpen bool
	// draft is a new session before its first message (sessiondraft.go),
	// drawn over the board tab's pane while it is open; draftKept is one
	// left with words in it, which the next n brings back.
	draft, draftKept *sessionDraft
	// threadInput is the card page's persistent message/verb box
	// (thread.go, threadinput.go): a Shell field rather than one rebuilt
	// per render so an unsent draft survives leaving and returning to the
	// tab, same as the chat pane's own m.chat.
	threadInput textarea.Model
	// threadDrafts holds every card's unsent line except the one currently
	// live in threadInput, keyed by feature. openCard/stepCard
	// swap the composer's buffer to the newly selected card's entry (empty
	// if it has none) after stashing the outgoing card's own text here;
	// closeCard stashes on the way out. Without this a line typed on one
	// card was still sitting in the box on the next one (F5).
	threadDrafts map[domain.FeatureID]string
	// openOnLoad is a card to land on once the next row load arrives,
	// empty when the cursor simply stays where it was. A card minted by a
	// command does not reach m.rows until that reload lands, so the act
	// that created it cannot select it itself — it names it here instead
	// and the rowsMsg arm performs the jump (followup.go's mint is the
	// one writer).
	openOnLoad domain.FeatureID

	// landConflicts are the files a card's last landing conflicted in
	// (merge.go's landFeature), so its decision offers the rebase
	// that resolves them rather than the landing that just failed. A
	// rebase clears it (rebaseFeature).
	landConflicts map[domain.FeatureID][]string

	inboxSel int // cursor into m.inbox.list(), the inbox tab's own selection
	notice   noticeMsg
	spec     *specView // non-nil while the spec surface is open
	diff     *diffView // non-nil while the diff surface is open

	mergePrep map[domain.FeatureID]bool // cards whose landing preconditions are being checked (one landing per card at a time)
	// ghWork is what the board is waiting on GitHub for, per card
	// (markGHWork): the status bar spins on it until its ghDoneMsg
	ghWork     map[domain.FeatureID]string
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
	// decisionAimed and decisionAimBase track wordAim's hold on decisionCursor
	// separately from an explicit pick, so an emptied composer can withdraw
	// the aim instead of leaving the cursor stuck where it pointed last. See
	// syncDecision.
	decisionAimed   bool
	decisionAimBase int
	// decisionDrawn is whether the open decision's block actually made it
	// onto the last real render (openDecisionBlock, via threadRender) or
	// was dropped for lack of room (windowDecisionBlock's F21 case). It is
	// written only by threadRender, so it is only ever current immediately
	// after one — nothing else may read it directly. visibleDecision
	// (decision.go) is the single place that does: it forces a fresh
	// render before reading this field, which is what keeps the bar and
	// every key handler that acts on a decision from acting as though a
	// picker were on screen when the render actually dropped it — a
	// decision can exist in domain state while its block is off screen on
	// a short terminal (BG-058).
	decisionDrawn bool

	// agent orchestration (nil engine means no agent wired)
	engine *engine.Engine
	// engineWhy is why the host came up with no engine, in the words the
	// failure gave; empty when it is not known. See noAgent.
	engineWhy string
	// follow is the live tail of a card another process is driving, opened
	// by watchForeign and rendered read-only by the card thread's live
	// stage block. Non-nil only while the card page is open on that card;
	// openCard/closeCard/stepCard stop it (follow.go).
	follow *followSource
	// threadOutputs expands every captured tool output in the thread
	// (alt+o); failures always show their tail either way. Sticky, like
	// the chat pane's toggle was.
	threadOutputs bool
	// threadFreeForm arms the composer as the open ask's free-form answer
	// channel — the chat pane's 'o' channel, inherited now that the pane
	// is gone. While armed, the decision's picker keys stand down so a
	// line that starts with a digit types as prose; enter delivers the
	// line verbatim as the answer. Disarmed by blur, by an answer, or by
	// the decision changing.
	threadFreeForm bool
	// threadAsk arms the composer against the card's consult session
	// instead of whatever stage session is (or isn't) live — the `ask`
	// verb's doing (verbs.go, threadinput.go's routeVerb). Sticky, like
	// threadFreeForm: it stays armed across multiple turns until esc, so
	// a follow-up question doesn't need retyping `ask`. Reset by blur,
	// the same as threadFreeForm.
	threadAsk bool
	// stackTickQueue is the same seam for stacks (stackboard.go): a card
	// whose branch moved wakes its stack, and the engine replays the
	// cards above it. Coalesced per stack, drained by Update.
	stackTickQueue map[domain.StackID]bool
	// repoBranches is each repo's local branch list, read beside
	// baseBranches (installRepos) and for the same reason: the creation
	// dialog's base row is rendered, and rendering may not run git.
	repoBranches map[string][]string
	// stackRows is each stacked card's position and staleness, derived in
	// loadRows (it asks git) and read by cardLine (which may not).
	stackRows map[domain.FeatureID]stackRow
	// workflowTicking and workflowTickAgain coalesce workflow ticks
	// (workflowloop.go).
	workflowTicking   bool
	workflowTickAgain bool
	// wf is what the terminal holds of the open session's workflow runs
	// (runblock.go), and stepv a step opened over its thread
	// (stepview.go), nil when none is.
	wf runState
	// dispatches are the open agent's dispatched sessions, as last folded
	// (dispatchblock.go).
	dispatches map[domain.FeatureID][]foldedDispatch
	// nestedRows are the sessions the last board frame drew under their
	// agent (boardnest.go).
	nestedRows map[domain.FeatureID]bool
	stepv      *stepView
	// sizedFor is how many landed rows worktreeSizeText was measured
	// against, so a reload that did not change the set does not re-walk
	// every worktree on disk. -1 until the first measurement.
	sizedFor int
	// worktreeSizeText is the disk the un-cleaned landed worktrees hold,
	// formatted, as the archive header and the close-out sweep print it.
	// Measured on demand (a filesystem walk per frame is the wrong price
	// for a line of text) and empty until something has measured it.
	worktreeSizeText string
	// archiveOpen is whether the board's folded archive — every card that
	// settled longer ago than archiveWindow — is showing its rows. It is
	// ephemeral, like sortMode: the board opens with history folded away,
	// which is the state that makes the list about today.
	archiveOpen bool
	// stats is the mounted stats tab (statsview.go), nil when closed:
	// where the selected card's credits and hours went.
	stats *statsView
	// logv is the mounted log tab (logview.go), nil when closed: the
	// selected card's own commits, and the draft of a rewrite of them.
	logv *logView
	// wsstats is the workspace stats tab (wsstats.go), nil until its
	// first visit mounts it: where the whole board's credits and hours
	// went, and when — the timeline. The card's own tab is stats; this
	// is the same question at the scale of the board.
	wsstats *wsStatsView
	// foreignTicks counts live-drive probes, pacing the slower full row
	// reload that picks up what another process wrote to the store
	// (follow.go).
	foreignTicks int
	// webActor is who a web request that runs outside an intent (a goal's
	// verb, run by Bridge.Await) acts as, for the length of its call on
	// the loop: humanActor reads it, so the record names the person.
	webActor string
	// storeVersion is the store's data_version as last seen (foreignMsg):
	// a change is another process's commit, and the board re-reads.
	storeVersion int64
	// fresh keeps the rows, and what a page was told about them, current
	// with the store and the repository (freshness.go).
	fresh *freshness
	// locks is the board's per-card lock registry, shared with the engine
	// (AttachCardLocks). Nil leaves the board's git verbs unlocked.
	locks *state.CardLocks
	inbox *inbox // needs-attention queue
	// cardEvents caches the card-event log (state.CardEvent, card_events
	// table) per feature, loaded lazily by loadCardEvents and applied to
	// the selected row's featureRow.Events at render time (msgs.go). It is
	// never populated for a row that has not been the selected card on an
	// open card page — loading every card's log on each board refresh
	// would be unbounded IO.
	cardEvents map[domain.FeatureID][]state.CardEvent
	// threadScroll is how many lines back from the newest the card
	// thread's body is scrolled. Zero is the bottom, which is where a
	// card opens and where it stays as a live stage streams — counting
	// back from the end rather than forward from the start is what keeps
	// arriving output from shoving the view out from under a reader.
	threadScroll int
	// threadBodyCard and threadBodyLen are the previous frame's body
	// length for the card threadScroll is scrolled on. threadScroll is a
	// distance from the end, so when the body grows out from under a
	// scrolled-back reader, threadRender advances threadScroll by the same
	// amount the body grew — otherwise the fixed distance keeps the window
	// itself sliding forward with every appended line (BG-042). Keyed to
	// the card so switching cards (which resets threadScroll to 0 anyway)
	// never applies one card's growth to another's offset.
	threadBodyCard domain.FeatureID
	threadBodyLen  int
	// threadTopEvent is the event index (featureRow.Events) rendered at
	// the top of the current scrolled window, refreshed by threadBody on
	// every non-measure render for the card named by threadBodyCard. A
	// resize reads it before applying the new width, because threadScroll
	// on its own is a raw row count with no notion of which content it
	// points at — a width change rewraps the body underneath it and the
	// same number lands on a different row (BG-057). -1 means no event
	// owns the top row (it fell in a folded stage, a rule, or the window
	// is not scrolled).
	threadTopEvent int
	// pendingScrollAnchor{Card,Event} carry a resize's request across to
	// the next render: land threadTopEvent as it was captured just before
	// the resize back at the top of the window again, however the reflow
	// moved it. Set by the WindowSizeMsg handler, consumed and cleared by
	// threadBody the same frame it is set, so it never survives a card
	// switch or outlives the render it was meant for.
	pendingScrollAnchor      bool
	pendingScrollAnchorCard  domain.FeatureID
	pendingScrollAnchorEvent int
	// lastSeen is how far through each card's event log this machine has
	// already read (state's card_last_seen).
	lastSeen map[domain.FeatureID]int64
	// pendingSeen holds marks written by markSeen before fetchLastSeen's
	// reply lands, so that mark can't flip lastSeen non-nil for every
	// *other* card too (BG-056 review). Kept separate until lastSeenMsg
	// merges it in.
	pendingSeen map[domain.FeatureID]int64

	// consultSending holds, per card, a line handed to the consult session
	// and not yet delivered — opening a session is a model call, and the
	// thread says so for as long as it runs (consultBlock). Cleared by
	// consultSentMsg on both outcomes.
	consultSending map[domain.FeatureID]string
	// rewound is the line alt+z last put back in a freeform card's composer:
	// pressed again over it, rewind goes one message further back.
	rewound      map[domain.FeatureID]string
	profileNames []string // profile names for the new-card dialog
	repoNames    []string // configured managed-repo names for the new-card dialog
	// lastRepo is the repository the last card was created in: the
	// new-card dialog's preselect.
	lastRepo string
	// ghIssue and remoteOrigin are the new-card dialog's GitHub and git
	// seams; nil uses gh and git. Tests stub both.
	ghIssue      func(ctx context.Context, dir, ownerRepo string, n int) (domain.BugProposal, error)
	remoteOrigin func(dir string) string
	envelope     int              // default spend-plan envelope for new features (0 = none)
	notifier     *notify.Notifier // bell/desktop hook for needs-attention events
	// attention hears the same transitions the bell does, with the card
	// and its question apart: the web face's push notifications
	// (AddAttentionNotifier).
	attention []AttentionNotifier
	// headless is a Shell hosted by a Bridge (bridge.go): no screen, so a
	// dialog nobody is answering is never seen, and what the TUI would
	// have asked in one is asked of the web face instead (webintent.go).
	headless bool
	// firstRun is the first-run setup still pending (SetFirstRun);
	// firstRunWiz is what the web page was last shown of it, so its answer
	// is checked against what was offered.
	firstRun      bool
	firstRunProbe FirstRunProbe
	firstRunWiz   *firstrun.Wizard
	// today is the board's spend since local midnight, measured off the
	// loop after a row load (webboard.go) for the web face's header.
	today todaySpend
	// intent is the web intent whose commands are being handled right now
	// (webintent.go), nil between them.
	intent *webIntent
	// liveIntents are the web intents still being followed.
	liveIntents map[*webIntent]bool

	// Copilot quota hint (copilotquota.go): the latest reading shown as
	// a status-bar pill, its enable flag, and the gh seam for tests.
	copilot       copilotQuota
	copilotHint   bool
	ghCopilotUser func(context.Context) ([]byte, error)

	// openReviewThreads probes a linked PR for open review threads.
	// Tests stub it; nil uses the engine path.
	openReviewThreads func(context.Context, domain.Feature) (int, string, error)

	// resolvePR and fetchPRReviewThreads back prlink and prpull: a real gh
	// call, unlike openReviewThreads' local-approximation default above —
	// prlink/prpull need a real answer from GitHub or the feature does
	// nothing new. Both are nil in a test scaffold and stubbed directly
	// (squash_test.go's pattern for openReviewThreads); cmd/gummi/main.go
	// wires them to pr.Resolve/pr.FetchReviewThreads alongside pr.GHBinary(),
	// next to the other shell.Set* calls. resolvePR takes repoDir so gh can
	// auto-detect owner/repo from the card's own configured repo, matching
	// pr.Resolve's own contract.
	resolvePR            func(ctx context.Context, spec, repoDir, branch string) (domain.PullRequestRef, error)
	fetchPRReviewThreads func(ctx context.Context, ref domain.PullRequestRef) ([]pr.ReviewThread, []pr.TopLevelComment, string, error)
	// fetchPRChecks backs prchecks: the PR's checks with the end of each
	// failed job's log. nil leaves the action reporting it is unavailable.
	fetchPRChecks func(ctx context.Context, ref domain.PullRequestRef) (pr.Checks, error)
	// prSquashMergeAllowed backs prlink's non-blocking squash-method
	// caution (the same one `gummi pr link` prints). Best-effort like
	// prepareMerge's own provenance warn: nil or a failing lookup just
	// skips the caution, never blocks the link.
	prSquashMergeAllowed func(ctx context.Context, repo string) (bool, error)
	// publishEnabled, publishGH: EnablePublishing's switch and gh binary;
	// publishChecked and publishWhy are detection's answer (publish.go),
	// nil Why meaning gh is there and signed in.
	publishEnabled bool
	publishGH      string
	publishChecked bool
	publishWhy     *publish.Error

	// shared activity spinner (spinner.go): frame is the current cycle
	// position; spinning guards the single live tick loop; motionEnabled
	// gates whether the clock is allowed to run at all.
	frame         int
	spinning      bool
	motionEnabled bool

	// now is injectable for deterministic tests.
	now func() time.Time

	// changeHook is told what a message may have changed, for a host
	// without a screen (bridge.go's SetChangeHook). Nil under the TUI.
	changeHook func(webapi.Change)
	// restart marks the process for a restart when the host supports one
	// (SetRestartHook); the "restart" command then quits. Nil hides it.
	restart func()
	// webBusy is whether each card was working the last time an engine
	// update was mapped for the web (bridge.go's emitEngineChange): a
	// streaming update moves only the live block, until the one that ends
	// or starts a turn — that one moves the card's head and decision too.
	webBusy map[domain.FeatureID]bool
	// webWatching is the same for a freeform card's open watch: the turn
	// that starts or settles one moves the board row's "watching" word too.
	webWatching map[domain.FeatureID]bool
}

// NewShell builds a detached shell (splash + empty board).
func NewShell(t theme.Theme, version string) *Shell {
	styles := theme.New(t)
	m := &Shell{
		styles:         styles,
		version:        version,
		now:            time.Now,
		consultSending: map[domain.FeatureID]string{},
		cardEvents:     map[domain.FeatureID][]state.CardEvent{},
		threadDrafts:   map[domain.FeatureID]string{},
		copilotHint:    true,
		motionEnabled:  true,
		// -1, so the first row load always measures: a zero would read as
		// "already measured, and the answer was nothing landed".
		sizedFor: -1,
		// the composer is themed from the same styles as everything else
		// on the page; left on the widget's own defaults it renders in raw
		// ANSI and reads as a foreign box (threadinput.go).
		threadInput: newThreadInput(styles),
		fresh:       newFreshness(),
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
	m.loadName()
	m.resolveBaseBranches()
}

// repoSnapshot is one read of the workspace's repositories: the names a
// discovered set holds, each repository's checked-out branch, and the
// branches it has. seq orders reads: installRepos keeps only a snapshot
// newer than the one it installed last, so a slow read cannot overwrite a
// fresher one.
type repoSnapshot struct {
	seq        uint64
	discovered bool // names are the pool's rescanned set; false keeps the board's own
	names      []string
	base       map[string]string
	branches   map[string][]string
}

// readRepos rescans a discovered named set (Pool.Refresh, a no-op for a
// fixed one; a failed rescan keeps the set the pool had), then reads every
// repository's branches. The rescan comes first, so a clone it finds gets
// its branches in the same snapshot. It runs git and walks the disk, so the
// board's loop calls it only through an off-loop read (rescanRepos,
// Bridge.RefreshRepos) — and once at Attach, before the loop runs.
func readRepos(ctx context.Context, wt *worktree.Pool, seq uint64) repoSnapshot {
	if err := wt.Refresh(); err != nil {
		fmt.Fprintln(os.Stderr, "gummi: rescanning repositories:", err)
	}
	base, branches := readBranches(ctx, wt)
	snap := repoSnapshot{seq: seq, base: base, branches: branches}
	if wt.Discovering() {
		snap.discovered, snap.names = true, wt.Names()
	}
	return snap
}

// nextRepoSeq issues the sequence number for a repository read. It is taken
// when the read is dispatched, never when it lands.
func (m *Shell) nextRepoSeq() uint64 { return m.repoSeq.Add(1) }

// installRepos installs snap unless a newer snapshot is already in place,
// and reports whether it did. Only then do the dialogs re-offer their
// choices (setRepoChoices).
func (m *Shell) installRepos(snap repoSnapshot) bool {
	if snap.seq <= m.repoInstalled {
		return false
	}
	m.repoInstalled = snap.seq
	if snap.discovered {
		m.repoNames = snap.names
	}
	m.repoMu.Lock()
	m.baseBranches, m.repoBranches = snap.base, snap.branches
	m.repoMu.Unlock()
	return true
}

// resolveBaseBranches reads each configured repository's current branch
// name and its branches, at attach. Every name a card can carry is
// resolved here so baseBranch below is a map lookup: it is called from
// render paths, and a render path may not run git. Later reads go through
// rescanRepos, off the loop, so a board served for days still offers a
// branch cut after launch to adopt or fork from. Attach's read rescans a
// discovered set once more; that is harmless, since Attach has just built
// the set, and a fixed set's rescan is a no-op.
func (m *Shell) resolveBaseBranches() {
	if m.wt == nil {
		return
	}
	m.installRepos(readRepos(context.Background(), m.wt, m.nextRepoSeq()))
}

// rescanRepos returns the off-loop read of the repositories, answered as a
// reposReadMsg, when the set is discovered; nil otherwise, so a fixed
// workspace runs nothing. reopen, when set, is the card whose repository
// picker should open once a clone is found (the set-repo verb with no
// repositories known yet).
func (m *Shell) rescanRepos(reopen domain.FeatureID) tea.Cmd {
	if m.wt == nil || !m.wt.Discovering() {
		return nil
	}
	wt, seq := m.wt, m.nextRepoSeq()
	return func() tea.Msg {
		return reposReadMsg{snap: readRepos(context.Background(), wt, seq), reopenPicker: reopen}
	}
}

// reposReadMsg answers a rescanRepos read. reopenPicker names a card whose
// repository picker to open when the set is now non-empty.
type reposReadMsg struct {
	snap         repoSnapshot
	reopenPicker domain.FeatureID
}

// repoChoices is a dialog that offers repositories and their branches. A
// rescan that installs a newer snapshot re-offers them through it.
type repoChoices interface {
	setRepoChoices(names []string, base map[string]string, branches map[string][]string)
}

// landRepos installs a rescan's snapshot and re-offers the choices of every
// open dialog that offers repositories. A snapshot that is not newer than the
// installed one changes nothing.
func (m *Shell) landRepos(msg reposReadMsg) {
	if !m.installRepos(msg.snap) {
		return
	}
	for i := 0; i < m.Overlay.Len(); i++ {
		if d, ok := m.Overlay.At(i).(repoChoices); ok {
			d.setRepoChoices(m.repoNames, m.baseBranches, m.repoBranches)
		}
	}
	for _, d := range []*sessionDraft{m.draft, m.draftKept} {
		if d != nil {
			d.form.setRepoChoices(m.repoNames, m.baseBranches, m.repoBranches)
		}
	}
	if msg.reopenPicker != "" && !m.overlayHasRepoPicker(msg.reopenPicker) {
		m.openRepoPicker(msg.reopenPicker)
	}
}

// overlayHasRepoPicker reports whether the repository picker for card id is
// already open.
func (m *Shell) overlayHasRepoPicker(id domain.FeatureID) bool {
	for i := 0; i < m.Overlay.Len(); i++ {
		if d, ok := m.Overlay.At(i).(*repoPickerDialog); ok && d.feature.ID == id {
			return true
		}
	}
	return false
}

// openRepoPicker opens the set-repo picker for card id on the names the board
// now has, or shows the notice when there are none.
func (m *Shell) openRepoPicker(id domain.FeatureID) {
	i := m.rowIndex(id)
	if i < 0 {
		return
	}
	if r := m.rows[i]; r.F.Home || r.HasWorktree {
		return
	}
	if len(m.repoNames) == 0 {
		m.notice = noticeMsg{text: "no other repositories configured"}
		return
	}
	m.Overlay.Push(newRepoPickerDialog(m.rows[i].F, m.repoNames, func(repo string) tea.Cmd {
		return m.setRepo(id, repo)
	}))
}

// readBranches is what installRepos installs: each repository's
// checked-out branch, and the branches it has. It runs git.
func readBranches(ctx context.Context, wt *worktree.Pool) (map[string]string, map[string][]string) {
	base := map[string]string{"": wt.BaseBranch(ctx, "")}
	repo := map[string][]string{}
	if mgr, err := wt.ManagerForName(ctx, ""); err == nil {
		if branches, berr := mgr.ListBranches(ctx); berr == nil {
			repo[""] = branches
		}
	}
	for _, name := range wt.Names() {
		base[name] = wt.BaseBranch(ctx, name)
		if mgr, err := wt.ManagerForName(ctx, name); err == nil {
			if branches, berr := mgr.ListBranches(ctx); berr == nil {
				repo[name] = branches
			}
		}
	}
	return base, repo
}

// baseBranch names the branch f lands on, for prose. Every caller that
// used to write the literal "main" goes through here.
//
// It answers with worktree.DefaultBaseBranchName for a card whose repo
// resolved to nothing and for a Shell that was never attached (the test
// scaffolds), so a sentence built from it is never missing a word.
func (m *Shell) baseBranch(f domain.Feature) string {
	if name := m.parentBranchOf(f); name != "" {
		return name
	}
	if f.Base != "" && (f.StackID == "" || f.StackPos == 0) {
		return f.Base
	}
	return m.repoBaseBranch(f)
}

// repoBaseBranch is baseBranch's trunk half: the branch f's repository has
// out, with no goal considered. Only the two callers that have already
// answered the goal question for themselves reach it directly.
func (m *Shell) repoBaseBranch(f domain.Feature) string {
	m.repoMu.RLock()
	defer m.repoMu.RUnlock()
	if name, ok := m.baseBranches[f.Repo]; ok && name != "" {
		return name
	}
	if name, ok := m.baseBranches[""]; ok && name != "" {
		return name
	}
	return worktree.DefaultBaseBranchName
}

// rowIndex is the index of card id in m.rows, -1 when it is not loaded.
func (m *Shell) rowIndex(id domain.FeatureID) int {
	for i, r := range m.rows {
		if r.F.ID == id {
			return i
		}
	}
	return -1
}

// parentBranchOf names the branch f lands on when that is not its
// repository's trunk: a card a freeform session created forks from and
// lands on that card's branch while it is open (worktree.Pool.ManagerFor).
// "" for every other card.
func (m *Shell) parentBranchOf(f domain.Feature) string {
	if f.ParentID == "" {
		return ""
	}
	if i := m.rowIndex(f.ParentID); i >= 0 && m.rows[i].F.Stage != domain.StageDone {
		return m.rows[i].F.BranchName()
	}
	return ""
}

// baseBranchOf is baseBranch keyed by card id, for the callers that hold
// an id rather than a Feature. It reads the card's repo off the loaded
// row when there is one and otherwise answers for the default repository,
// which is the right answer in the single-repo workspace that is the
// common case and a safe one elsewhere: the alternative these callers had
// was the literal "main".
func (m *Shell) baseBranchOf(id domain.FeatureID) string {
	for _, r := range m.rows {
		if r.F.ID == id {
			return m.baseBranch(r.F)
		}
	}
	return m.baseBranch(domain.Feature{})
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
		return fmt.Sprintf("%s is being driven by another gummi process — press enter to watch it, or wait for it to finish", id.Ref())
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

// SetEngineUnavailable records why the host has no engine to attach, so
// a refusal can say it. The reason is otherwise one line on the host's
// stderr at startup — which a web host's reader never sees, and which a
// host started by a service manager writes to a journal nobody is
// reading when a card refuses to run.
func (m *Shell) SetEngineUnavailable(why string) { m.engineWhy = strings.TrimSpace(why) }

// noAgent words a refusal for want of an engine. With no reason on
// record it is the plain "no agent configured" followed by tail, the
// caller's own clause about what needed one. With a reason it says that
// instead: the agent is configured and did not start, and sending the
// reader to their configuration would send them the wrong way.
func (m *Shell) noAgent(tail string) string {
	if m.engineWhy == "" {
		return "no agent configured" + tail
	}
	return "no agent started: " + sanitize(m.engineWhy) +
		" — fix that in the environment gummi runs in, then restart it"
}

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

// DefaultEnvelopeCredits is what the creation dialogs prefill when the
// workspace has not set GUMMI_ENVELOPE. A card needs *some* budget to be
// worth creating, and an empty-handed board that opened on 0 (uncapped)
// made the unbudgeted answer the one nobody had to type. It is a prefill
// and nothing more: it is editable in the dialog, GUMMI_ENVELOPE
// overrides it, and the headless verbs still refuse to start without an
// explicit envelope — no unattended run spends against a number the
// operator never chose.
const DefaultEnvelopeCredits = 2000

// SetEnvelope sets the default spend-plan envelope (credits) stamped on
// new features, enabling layer-3 per-stage budgets. 0 leaves features
// unbudgeted (or governed by a flat per-stage budget).
func (m *Shell) SetEnvelope(credits int) { m.envelope = credits }

// envelopePrefill is the number the creation dialogs open on. It is
// deliberately not m.envelope itself: m.envelope stays the *operator's*
// envelope (0 when GUMMI_ENVELOPE is unset). Folding the prefill into
// that field would read as an explicit choice nobody made.
func (m *Shell) envelopePrefill() int {
	if m.envelope > 0 {
		return m.envelope
	}
	return DefaultEnvelopeCredits
}

// SetNotifier wires the needs-attention notification hook (bell/desktop).
func (m *Shell) SetNotifier(n *notify.Notifier) { m.notifier = n }

// AttentionNotifier hears every needs-you transition: a card that just
// started waiting on a person, and the one line the inbox shows for it.
// It is called on the Update goroutine, so it must not block — hand the
// message to a goroutine and return.
type AttentionNotifier interface {
	NeedsYou(id domain.FeatureID, text string)
}

// AddAttentionNotifier attaches a notifier beside the bell (or instead of
// it, when the bell is off). Attach before the program runs.
func (m *Shell) AddAttentionNotifier(n AttentionNotifier) {
	if n != nil {
		m.attention = append(m.attention, n)
	}
}

// alert signals a needs-you transition to the bell and to every attached
// notifier.
func (m *Shell) alert(id domain.FeatureID, text string) {
	m.notifier.Alert(m.cardName(id) + ": " + text)
	for _, n := range m.attention {
		n.NeedsYou(id, text)
	}
}

// SetPRResolver wires prlink's head-branch-resolution and submit path to a
// real gh lookup. Without it (a test scaffold, or a shell nobody has wired
// yet) prlink's probe and submit both report that linking is unavailable
// rather than silently doing nothing.
func (m *Shell) SetPRResolver(fn func(ctx context.Context, spec, repoDir, branch string) (domain.PullRequestRef, error)) {
	m.resolvePR = fn
}

// SetPRThreadFetcher wires prpull's review-thread fetch to a real gh call.
// Without it prpull reports that pulling is unavailable rather than
// silently ingesting nothing.
func (m *Shell) SetPRThreadFetcher(fn func(ctx context.Context, ref domain.PullRequestRef) ([]pr.ReviewThread, []pr.TopLevelComment, string, error)) {
	m.fetchPRReviewThreads = fn
}

// SetPRChecksFetcher wires prchecks' read of a PR's checks and failed-job
// logs to a real gh call. Without it prchecks reports that it is
// unavailable rather than telling a session nothing is failing.
func (m *Shell) SetPRChecksFetcher(fn func(ctx context.Context, ref domain.PullRequestRef) (pr.Checks, error)) {
	m.fetchPRChecks = fn
}

// SetPRSquashMergeChecker wires prlink's non-blocking squash-method
// caution to a real gh lookup. Optional: nil leaves linking silent about
// the repo's merge-method setting.
func (m *Shell) SetPRSquashMergeChecker(fn func(ctx context.Context, repo string) (bool, error)) {
	m.prSquashMergeAllowed = fn
}

// SetCopilotHint toggles the status-bar Copilot quota pill (on by
// default; it hides itself anyway when gh or a quota is absent).
func (m *Shell) SetCopilotHint(on bool) { m.copilotHint = on }

// SetMotion toggles the shared activity spinner (on by default). Off
// freezes every glyph in the UI to its static first frame and stops the
// clock's tick loop from ever starting.
func (m *Shell) SetMotion(enabled bool) { m.motionEnabled = enabled }

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
	diffOpen, err := m.engine.OpenDiffComments(ctx, f.ID)
	if err != nil {
		return 0, "", err
	}
	return diffOpen, f.PullRequest.URL, nil
}

// openSquashDialog opens the reused commit-message dialog for a squash
// in place, pre-filled with the same best-effort draft the merge path uses.
func (m *Shell) openSquashDialog(f domain.Feature) tea.Cmd {
	d := newCommitMsgDialog(f, func(message string, _ domain.LandMethod) tea.Cmd {
		return m.collapseFeature(f, message)
	}, func(dctx context.Context, feature domain.Feature, fresh bool) (string, error) {
		if m.engine == nil {
			return "", nil
		}
		return m.engine.LandingMessage(dctx, feature, fresh)
	}).squashInPlace()
	// The dialog names the branch this lands on. It is a field rather than
	// a constructor argument (commitMsgDialog.baseBranch has the why), and
	// this is the wiring: without it the dialog falls back to saying "main"
	// at a repo whose trunk is master.
	d.baseBranch = m.baseBranch(f)
	m.Overlay.Push(d)
	return d.startDraft(false)
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
	// the same stop raised again with nothing run in between (a retry
	// that failed the same way) is the one stop, already on record
	repeat := m.repeatsLastPark(id, text)
	if !m.parkAttentionItem(id, kind, text) || repeat {
		return
	}
	switch kind {
	case attnGate:
		m.logDecision(id, state.DecisionKindGate, text)
	case attnFailure:
		// a stage that could not run blocks the card on a person just as
		// a gate does, and without a row it was forgotten on a restart:
		// the card came back reading idle, the failure nowhere
		m.logDecision(id, state.DecisionKindFailure, text)
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
	m.alert(id, text)
	m.logPark(id, state.ParkReasonNeedsYou, text)
	return true
}

// logDecision records a card's open decision in its own history
// (best-effort and silent, like logPark beside it): a card blocked on a
// person leaves a row, whoever drove it here. The id is minted per
// raise, generation-scoped so a re-raised stop after a bounce never
// collides with its predecessor's row.
func (m *Shell) logDecision(id domain.FeatureID, kind, question string) {
	m.logDecisionAs(id, kind, question, false)
}

// logDecisionAs is logDecision for a stop that may be an escalation: the
// record keeps the flag, so the stop reads the same after a restart.
func (m *Shell) logDecisionAs(id domain.FeatureID, kind, question string, escalated bool) {
	if m.store == nil {
		return
	}
	stage := m.recordStage(id)
	_ = m.store.OpenDecision(context.Background(), id, stage, state.DecisionPayload{
		ID:        kind + ":" + string(id) + ":" + string(stage) + ":" + strconv.FormatInt(time.Now().UnixNano(), 10),
		Kind:      kind,
		Question:  question,
		Escalated: escalated,
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
	if m.store == nil || m.repeatsLastPark(id, detail) {
		return
	}
	_ = m.store.AppendPark(context.Background(), id, m.recordStage(id), reason, detail, "", time.Now())
}

// markSeen records that this card's log has been read up to its newest
// event.
func (m *Shell) markSeen(id domain.FeatureID, events []state.CardEvent) tea.Cmd {
	seq := newestSeq(events)
	// Marks made before lastSeen has loaded go into pendingSeen instead of
	// lazily initialising lastSeen itself — m.lastSeen is nil until
	// fetchLastSeen's reply lands, which is "not loaded yet", distinct
	// from "loaded, nothing seen" (BG-056).
	if m.lastSeen != nil {
		if seq <= m.lastSeen[id] {
			return nil
		}
		m.lastSeen[id] = seq
	} else {
		if seq <= m.pendingSeen[id] {
			return nil
		}
		if m.pendingSeen == nil {
			m.pendingSeen = map[domain.FeatureID]int64{}
		}
		m.pendingSeen[id] = seq
	}
	if m.store == nil {
		return nil
	}
	// Best-effort and silent, like the history writes above: failing to
	// remember where the reader got to costs them one redundant jump next
	// time, which is not worth a message.
	return func() tea.Msg {
		_ = m.store.SetLastSeen(context.Background(), id, seq)
		return nil
	}
}

// lastSeenMsg carries the per-card read marks loaded at startup.
type lastSeenMsg struct {
	seqs map[domain.FeatureID]int64
	err  error
}

// fetchLastSeen reads every card's read mark in one query, at startup —
// the same shape and the same reason as fetchOpenDecisions (inboxseed.go).
// A failure is silent: the marks are a convenience, and a board that
// cannot read them simply opens every card at its newest line, which is
// what it did before they existed.
func (m *Shell) fetchLastSeen() tea.Msg {
	seqs, err := m.store.LastSeenSeqs(context.Background())
	return lastSeenMsg{seqs: seqs, err: err}
}

// logRebase records a card's branch rebased onto its base
// (state.EventRebase). Best-effort and silent, like logPark.
func (m *Shell) logRebase(id domain.FeatureID, p state.RebasePayload) {
	if m.store == nil {
		return
	}
	_ = m.store.AppendRebase(context.Background(), id, m.recordStage(id), p, time.Now())
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

// cardName names a card in a sentence a person reads, from its id alone:
// the title the board holds for it (domain.Feature.Name), or its number
// when the board has no such row — a card deleted or not yet loaded.
func (m *Shell) cardName(id domain.FeatureID) string {
	for i := range m.rows {
		if m.rows[i].F.ID == id {
			return m.rows[i].F.Name()
		}
	}
	return id.Ref()
}

// cardTitle is the card's bare title for a column that names it — no
// quotes, uncut — or its number when the board has no such row.
func (m *Shell) cardTitle(id domain.FeatureID) string {
	for i := range m.rows {
		if t := strings.Join(strings.Fields(m.rows[i].F.Title), " "); m.rows[i].F.ID == id && t != "" {
			return sanitize(t)
		}
	}
	return id.Ref()
}

// storedFeature reads the card as the store holds it now. The board's
// rows are a snapshot refreshed by an asynchronous reload, and the
// commands that move a card (a hand-over, a crossing, a rewind) start its
// next session before that reload can land: with a fast agent the stage
// finishes first, and every decision made off the row then acts on the
// card as it was — the stage it left, the mode it had before it was
// handed over. Reading the store is what the row would say once it
// caught up, so the decisions an event triggers read it instead.
func (m *Shell) storedFeature(id domain.FeatureID) (domain.Feature, bool) {
	if m.store == nil {
		return domain.Feature{}, false
	}
	f, err := m.store.GetFeature(context.Background(), id)
	if err != nil {
		return domain.Feature{}, false
	}
	return f, true
}

// recordStage is the stage a row written to the card's history is filed
// under: the stored one (storedFeature), falling back to the board's row.
// Filing under the row's stage put a design gate autopilot raised right
// after a hand-over under "todo", because the card had left todo before
// the board reloaded — and the thread then headed it "TODO GATE".
func (m *Shell) recordStage(id domain.FeatureID) domain.Stage {
	if f, ok := m.storedFeature(id); ok {
		return f.Stage
	}
	return m.stageOf(id)
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
		// records (openDecisionsMsg's handler in update). Store.OpenDecisions hits the
		// database, and Init runs on the Update goroutine — see
		// attachChat's no-IO-in-Update contract — so the query has to be a
		// dispatched command, not a direct call.
		//
		// A store is all it needs: the record outliving the process that
		// raised it is the whole point of it, so a board with no agent
		// wired still learns what a headless run left waiting.
		cmds = append(cmds, m.fetchOpenDecisions)
		if m.publishEnabled {
			cmds = append(cmds, m.detectPublish)
		}
		// How far through each card the reader already got. Loaded once,
		// in bulk, for the same reason the decision records are: the board
		// renders every card and a per-card query here would be one round
		// trip per row on screen. Without it the map starts empty on every
		// launch and the first card opened after a restart would look
		// entirely unread — which is precisely the jump this mark exists to
		// stop happening twice.
		cmds = append(cmds, m.fetchLastSeen)
	}
	if m.engine != nil {
		cmds = append(cmds, m.listenEngineCmd())
	}
	if cmd := m.firstRunInit(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if m.copilotHint {
		cmds = append(cmds, m.fetchCopilotQuota())
	}
	if m.attached() {
		// probe for cards other gummi processes are driving, so the board
		// badges them (and withholds the actions that would fight them)
		// instead of presenting a card it cannot touch as idle.
		cmds = append(cmds, foreignTick())
		// tick the runs at once, not a poll later: a run left running by a
		// gummi that is gone is parked, and asks its person, from the start
		cmds = append(cmds, workflowPollTick(), m.workflowTickCmd())
		// The stats tab's own slow refresh. It reads the record rather
		// than the event stream, so nothing wakes it when a running lane
		// grows or a wait is answered — a tick does, only while the tab
		// is on screen (updateWsStats).
		cmds = append(cmds, wsStatsTick())
		// The stack loop's backstop poll. Stacks mostly wake on engine
		// events (a session settled, a branch moved); this catches the
		// changes gummi cannot hear — a hand-run git command, or a PR
		// merging and main being pulled.
		cmds = append(cmds, stackPoll())
		// The schedules' clock. Nothing announces a cadence coming due;
		// this is the only thing that fires one.
		cmds = append(cmds, schedulePoll())
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

// threadShowsFailure reports whether the card page is open on id with its
// thread already rendering that card's session error inline (thread.go's
// snap.Err branch), so a failure the thread already prints does not also
// get a second, multi-row copy in the notice band (F9). A failure on a card
// the thread is NOT showing still needs the notice.
func (m *Shell) threadShowsFailure(id domain.FeatureID) bool {
	if m.engine == nil || !m.cardOpen || m.sel < 0 || m.sel >= len(m.rows) || m.rows[m.sel].F.ID != id {
		return false
	}
	return !m.rows[m.sel].DrivenAbroad && m.engine.Freeform(id) != nil
}

// crossCardOpen reports whether a card page is open on a card other than
// id — the case where an engine-raised failure or budget stop about id
// must not write the transient notice. The surface the reader is on
// belongs to the card they opened, and a notice about a different card
// buries the feedback for what they are doing; the event still reaches
// the channels that own it (the inbox item, the bell/desktop hook, the
// board row's glyph, the card's own thread), so nothing is lost — only
// not echoed over a page it does not belong to. False when no card page
// is open (the board tab keeps the notice) and for the empty id: a notice
// not bound to a card (an ingest or other one-shot pass) has no card it
// could be cross to, so it always writes.
func (m *Shell) crossCardOpen(id domain.FeatureID) bool {
	if id == "" {
		return false
	}
	return m.cardOpen && m.selectedID() != id
}

// handleEngineEvent folds an engine event into the notice line, the
// needs-attention queue, and the automatic review loop. It returns a
// command for any automatic follow-up (review→fix→review), or nil.
func (m *Shell) handleEngineEvent(ev engine.Event) tea.Cmd {
	switch ev.Kind {
	case engine.EventObjective:
		// a session's objective settled: the one notification it gives
		// (DESIGN §19.11), with the auditor's last word on it
		if ff := m.engine.Freeform(ev.Feature); ff != nil {
			if o := ff.Snapshot().Objective; o != nil {
				m.alert(ev.Feature, "objective "+string(o.State)+" — "+sanitize(o.Note))
			}
		}
		return m.loadRows
	case engine.EventWorkflow:
		// something a workflow run's conductor reads changed: tick the
		// runs, and reload so the session's decisions are current
		return tea.Batch(m.workflowTickCmd(), m.loadRows)
	case engine.EventCardCreated:
		// a card was minted or filed onto the open board by a caller that
		// touches no session machinery — a goal's lead — the only
		// announcement it gets, since cardmint writes straight to the
		// store with no event of its own. Reload rows the same way
		// EventIdle does for a finished stage.
		return m.loadRows
	case engine.EventError:
		if ev.Err != nil {
			// engine/provider errors may embed model-controlled bytes
			text := sanitize(ev.Err.Error())
			// the thread's own live-stage block already prints this
			// error inline when its card page is open (thread.go's
			// snap.Err branch) — the notice band would be the same
			// message twice, once under the status bar and once inside
			// the conversation it is about (F9). The notice is dropped
			// outright too when a card page is open on a different card:
			// the channels that own the failure (the inbox item below,
			// the hook it rings) still carry it, and the page being read
			// is not this card's. raiseAttention still runs
			// unconditionally: the inbox item is what turns
			// openDecision's pinned question into the failure kind, and
			// nothing else feeds that.
			if !m.threadShowsFailure(ev.Feature) && !m.crossCardOpen(ev.Feature) {
				m.notice = noticeMsg{text: text, isErr: true, id: ev.Feature}
			}
			// a one-shot pass not bound to a feature (ingest) has no card
			// to queue behind; the notice alone carries it
			if ev.Feature != "" {
				// a drifted card is only probed for its drift once it is
				// stopped on a person (stoppedDrift). The error already
				// says so: mark the row before the stop is raised, so the
				// failure is never shown — and answered — with the
				// retries the drift refuses, only to change under the
				// reader when the rows reload.
				var drift *worktree.ForkDriftError
				if errors.As(ev.Err, &drift) {
					for i := range m.rows {
						if m.rows[i].F.ID == ev.Feature {
							m.rows[i].Drift = drift
						}
					}
				}
				m.raiseAttention(ev.Feature, attnFailure, text)
			}
		}
	case engine.EventExhausted:
		// budget exhausted mid-stage: raise a gate, don't auto-continue.
		// With a card page open on another card, the notices below do not
		// land: each names this event's card, and the page being read is
		// not its. Every raiseAttention and the row reload stay
		// unconditional.
		crossCard := m.crossCardOpen(ev.Feature)
		if ev.Committed {
			// wrap-up exhaustion: the stage's work is committed, so this
			// reads as ready-to-advance with top-up as the alternative —
			// not lost work.
			m.raiseAttention(ev.Feature, attnBudget, budgetAttentionText(ev.Stage, true))
			if !crossCard {
				m.notice = noticeMsg{text: m.cardName(ev.Feature) + ": " + string(ev.Stage) + " reached its budget (work committed)"}
			}
		} else {
			m.raiseAttention(ev.Feature, attnBudget, budgetAttentionText(ev.Stage, false))
			if !crossCard {
				m.notice = noticeMsg{text: m.cardName(ev.Feature) + " budget exhausted at " + string(ev.Stage), isErr: true, id: ev.Feature}
			}
		}
		// The park's own numbers: the engine suppresses the trailing idle
		// of an exhausted turn (engine.handle), so the EventIdle branch
		// below — the one that reloads rows when a stage finishes — never
		// fires here, and the row would keep the spend it had before the
		// run. That is the reading that made a budget stop look like a
		// mistake: "budget exhausted" beside a masthead still offering
		// hundreds of credits. The session is gone by now, so the live
		// figure goes with it and only a reload can tell the truth.
		return m.loadRows
	case engine.EventBudget:
		// a threshold crossing (50/80/95%) is the one usage-driven event
		// worth a row reload: the card is deep enough into its envelope
		// that the board's other surfaces — the cost tick, the run chip's
		// "N are left in the envelope" — are about to matter, and they
		// read the row rather than the live session.
		return m.loadRows
	case engine.EventQuestion:
		// The agent asked something: queue it, always. This used to skip
		// the queue whenever the card page happened to be open on the
		// asking card, on the grounds that its pinned decision already
		// shows the question inline — but the test ran once, at event
		// time, and nothing revisited it. Open the card, start the stage,
		// step back to the board while it thinks, and the question arrives
		// with the card open, is never queued, and then exists nowhere a
		// person can find it: the board says "in progress", the status bar
		// says "running", and the inbox says "nothing needs you", while
		// the agent sits blocked on a human. The item is cleared when the
		// answer lands, so the redundancy while you are looking straight
		// at the question costs one row and is at least true.
		q := "the agent has a question"
		if ff := m.engine.Freeform(ev.Feature); ff != nil {
			if s := ff.Session(); s != nil {
				if a := s.Snapshot().PendingAsk; a != nil {
					q = "asks: " + a.Question
				}
			}
		}
		if _, d := m.engine.WorkflowDecision(context.Background(), ev.Feature); d != nil {
			q = "the workflow run asks: " + d.Question
		}
		m.raiseAttention(ev.Feature, attnQuestion, q)
		if _, step := m.children[ev.Feature]; step {
			// a run step's card is hidden: its question is its session's to
			// show, which only a reload of the rows reads (stepAsks)
			return m.loadRows
		}
	case engine.EventAnnotations:
		// the agent resolved a diff comment — refresh an open diff surface
		// so its open-count and gutter markers burn down live. The row's
		// own count has to move too, and it must move whether or not the
		// diff happens to be on screen: the gate reads the row.
		if m.diff != nil && m.diff.f.ID == ev.Feature {
			return m.reloadDiff()
		}
		return m.refreshBlockers(ev.Feature)
	}
	return nil
}

// refreshOpenCardEvents re-reads the open card's event log when the event
// just handled means the log grew. The thread's history above the live
// stage — the folded receipt for every finished stage session, the
// autopilot period rules drawn around them — is composed from
// featureRow.Events (thread.go), and that snapshot is otherwise taken
// once, when the card page opens or the selection moves on it
// (loadCardEvents). A stage that starts and finishes while the page is
// already open therefore left nothing behind: the live block moved on to
// the next stage and the one before it simply vanished from the history,
// with no receipt, no crossing line and a live header still stamped with
// the previous stage's time — until the reader left the card and came
// back, which reloaded the log and restored all of it.
//
// The three kinds below are the ones the engine sends after it has
// written to the log, so a reload here can never race ahead of the
// write: Idle follows handle's once-per-turn persist, Stopped follows
// the setState(StateDone)/persist pair that records stage_exit, and
// Started follows the fresh session's own persist on the interactive
// path (the autonomous one persists just after, and its first Idle picks
// that up). Only the card actually on screen is reloaded, so this stays
// one small read per turn of one card, not the per-row IO the row
// snapshot exists to avoid.
func (m *Shell) refreshOpenCardEvents(ev engine.Event) tea.Cmd {
	if !m.cardOpen || ev.Feature == "" || m.selectedID() != ev.Feature {
		return nil
	}
	switch ev.Kind {
	case engine.EventStarted, engine.EventIdle, engine.EventStopped:
		return m.loadCardEvents(ev.Feature)
	}
	return nil
}

// reloadOpenCardEvents re-reads id's event log when id is the card whose
// page is open, and does nothing otherwise. It is the crossing-shaped
// counterpart to refreshOpenCardEvents above: a gate records its
// transition in the same log the thread's history is composed from, but
// a crossing's outcome only ever asked for a row reload, and rows are
// where the masthead and the stage rail come from — never the history.
//
// That gap only shows when nothing is running at the gate. A crossing
// made from a live stage drops that stage's session, and the Stopped
// event which follows reloads the log through refreshOpenCardEvents, so
// its receipt lands; a crossing made at an idle stage emits no engine
// event at all, and used to move the rail while leaving the thread
// showing the previous transition as its newest line. One card could
// therefore show a receipt for one crossing and not the next.
func (m *Shell) reloadOpenCardEvents(id domain.FeatureID) tea.Cmd {
	if !m.cardOpen || id == "" || m.selectedID() != id {
		return nil
	}
	return m.loadCardEvents(id)
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
	var (
		model tea.Model = m
		cmd   tea.Cmd
	)
	if tm, ok := msg.(webTrackedMsg); ok {
		// a message a web intent's command produced (webintent.go): it is
		// handled like any other, on the intent's behalf.
		return m.updateTracked(tm)
	}
	if req, ok := msg.(*bridgeMsg); ok {
		// a web request's read or intent (bridge.go), run between two
		// messages like any other; it reports its own changes.
		cmd = req.run(m)
	} else if rm, ok := msg.(reposReadMsg); ok {
		m.landRepos(rm)
	} else {
		before := m.Overlay.Len()
		model, cmd = m.update(msg)
		m.emitChanges(msg)
		// a dialog that offers repositories opened by this message asks for
		// a rescan; it runs off the loop and re-offers the choices when it
		// lands (rescanRepos), so the dialog is not behind a clone made
		// since launch.
		if m.Overlay.Len() > before {
			if _, ok := m.Overlay.Top().(repoChoices); ok {
				cmd = tea.Batch(cmd, m.rescanRepos(""))
			}
		}
	}
	if tick := m.drainStackTicks(); tick != nil {
		cmd = tea.Batch(cmd, tick)
	}
	if !m.spinning && m.spinnerActive() {
		m.spinning = true
		cmd = tea.Batch(cmd, spinnerTick())
	}
	m.sweepOrphanDialogs()
	return model, cmd
}

func (m *Shell) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(statsLoadedMsg); ok {
		return m, m.statsLoaded(msg)
	}
	if cmd, ok := m.updateWorkflow(msg); ok {
		return m, cmd
	}
	if cmd, ok := m.updateRuns(msg); ok {
		return m, cmd
	}
	if m.updateDispatches(msg) {
		return m, nil
	}
	if cmd, ok := m.updateStack(msg); ok {
		return m, cmd
	}
	if cmd, ok := m.updateSchedule(msg); ok {
		return m, cmd
	}
	if cmd, ok := m.updateProfiles(msg); ok {
		return m, cmd
	}
	if cmd, ok := m.updateFirstRun(msg); ok {
		return m, cmd
	}
	if cmd, ok := m.updateCurator(msg); ok {
		return m, cmd
	}
	if cmd, ok := m.updateWsStats(msg); ok {
		return m, cmd
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// BG-057: a width change rewraps the thread body, so the row
		// threadScroll counts back from no longer names the same content.
		// Captured before the resize is applied, using threadTopEvent as
		// threadBody left it on the last render at the old width — the
		// WindowSizeMsg handler has no rendering of its own to derive it
		// from. threadBody consumes this on its next render, at the new
		// width, and re-derives threadScroll from where that same event
		// landed after the reflow instead of leaving the old row count in
		// place.
		if m.cardOpen && msg.Width != m.width && m.threadScroll > 0 && m.threadTopEvent >= 0 &&
			m.sel >= 0 && m.sel < len(m.rows) && m.threadBodyCard == m.rows[m.sel].F.ID {
			m.pendingScrollAnchor = true
			m.pendingScrollAnchorCard = m.threadBodyCard
			m.pendingScrollAnchorEvent = m.threadTopEvent
		}
		m.width, m.height = msg.Width, msg.Height
		m.layout = m.computeLayout()
		return m, nil

	case rowsMsg:
		if msg.err != nil {
			m.notice = noticeMsg{text: msg.err.Error(), isErr: true}
			return m, nil
		}
		if m.staleRows(msg) {
			// a load older than the one already applied (freshness.go)
			return m, nil
		}
		// the cursor is kept on the card it was on, by id: the rows about
		// to replace these can be a different length and a different order,
		// and an index alone would quietly point somewhere else.
		was := m.selectedID()
		m.rows = msg.rows
		m.children = msg.children
		m.stackRows = msg.stacks
		m.restoreSel(was)
		var jump tea.Cmd
		if id := m.openOnLoad; id != "" {
			// A card minted since the last load: this is the first moment
			// it is on the board, so it is the first moment it can be
			// landed on. Cleared whether or not it is there — a card that
			// failed to appear must not have every later reload yank the
			// cursor off whatever the reader has since selected.
			m.openOnLoad = ""
			if !m.headless {
				// a headless board has no page to land on: the web face's
				// pages open their own cards
				jump, _ = m.jumpToCard(id)
			}
		}
		// the action cursor belongs to whichever card is selected, so it
		// resyncs whether or not the selection survived.
		m.syncActionFocus()
		// A row reload can move the gate's blocking counts without
		// touching the log — a comment resolved, a section drafted —
		// and those are half the narration's cache key, so the pass is
		// re-checked here too. The other half, and the moment a card
		// page first has a log to key on at all, is cardEventsMsg.
		// The archive header's disk figure is measured only when the set
		// it describes has actually changed — a walk of every landed
		// worktree is far too expensive to repeat on every reload, and a
		// figure that is one landing stale is still the right order of
		// magnitude for the decision it informs.
		cmds := []tea.Cmd{jump}
		if m.cardOpen {
			// a run's decision, its state and its spend all move with the
			// rows: its blocks are read again beside them
			cmds = append(cmds, m.loadRuns(m.selectedID()), m.loadDispatches(m.selectedID()))
		}
		if n := landedRows(m.rows); n != m.sizedFor {
			m.sizedFor = n
			cmds = append(cmds, m.refreshWorktreeSize())
		}
		cmds = append(cmds, m.measureToday())
		return m, tea.Batch(cmds...)

	case openDecisionsMsg:
		// The record is the source: a failed query has nothing to seed
		// from, and the notice says the queue may be short a few items
		// rather than silently pretending it is complete.
		if msg.err != nil {
			m.notice = noticeMsg{text: "needs-you queue: " + sanitize(msg.err.Error()), isErr: true}
			return m, nil
		}
		before := m.inboxIDs()
		switch {
		case msg.refresh:
			m.refreshInboxFromDecisions(msg.decisions)
		case msg.reseed:
			m.seedInboxFromDecisions(msg.decisions)
		default:
			m.seedInboxFromDecisions(msg.decisions)
		}
		// A card is probed for fork drift only once it is stopped on a
		// person (stoppedDrift), and the rows may have loaded before this
		// seeding put its stop back — at startup they always do. Reload
		// when a card newly stopped here, so its answers know.
		for _, it := range m.inbox.list() {
			if !before[it.Feature] {
				return m, m.loadRows
			}
		}
		return m, nil

	case sessionSwitchedMsg:
		m.notice = noticeMsg{text: msg.text, id: msg.id}
		return m, m.loadRows

	case noticeMsg:
		// an outcome-driven clear: the action that produced this notice
		// succeeded, so drop the attention item it resolved (see
		// noticeMsg.clearInbox). It runs on the Update goroutine, never
		// inside a command.
		if msg.restore != "" && strings.TrimSpace(m.threadInput.Value()) == "" {
			// back into the line it was typed on, and only while nothing
			// has been typed since — a sentence the user has moved on
			// from must not reappear under their cursor.
			m.threadInput.SetValue(msg.restore)
		}
		var reseed tea.Cmd
		if msg.clearInbox != "" {
			m.inbox.remove(msg.clearInbox)
			if msg.reseedInbox && m.store != nil {
				reseed = m.reseedCardDecisions(msg.clearInbox)
			}
		}
		m.notice = msg
		// a reload is opt-in: only a notice emitted by a command that
		// mutated row-rendered state carries the flag (see noticeMsg).
		// A routine status notice ("queued", "paused", a non-mutating
		// error) never triggers a board reload.
		if msg.reload && m.attached() {
			// the same mutation that moved a row can have written to the
			// open card's event log — crossing a gate records its
			// transition there — and the thread's history is not composed
			// from rows, so reloading them alone leaves the page saying two
			// different things about the same card.
			return m, tea.Batch(m.loadRows, m.reloadOpenCardEvents(m.selectedID()), reseed)
		}
		return m, reseed

	case copilotQuotaMsg:
		m.copilot = msg.quota
		if msg.retry {
			return m, copilotQuotaTick()
		}
		return m, nil

	case copilotQuotaTickMsg:
		return m, m.fetchCopilotQuota()

	case mergeReadyMsg:
		delete(m.mergePrep, msg.f.ID)
		if msg.err != nil {
			m.notice = noticeMsg{text: sanitize(msg.err.Error()), isErr: true}
			if msg.webErr != "" {
				m.notice.web = sanitize(msg.webErr)
			}
			return m, nil
		}
		m.notice = noticeMsg{}
		if msg.warn != "" {
			// non-blocking caution (provenance in branch commits): shown
			// while the commit-message dialog collects the landing message
			m.notice = noticeMsg{text: sanitize(msg.warn), isErr: true}
		}
		f := msg.f
		var d *commitMsgDialog
		d = newCommitMsgDialog(f, func(message string, method domain.LandMethod) tea.Cmd {
			return m.landFeature(f, message, method, d.keepBranch)
		}, func(dctx context.Context, feature domain.Feature, fresh bool) (string, error) {
			// best-effort: a nil engine or any drafting failure yields an
			// empty draft, never a hard error or a delayed dialog; dctx
			// lets esc cancel an in-flight pass.
			if m.engine == nil {
				return "", nil
			}
			// fresh=false takes the message the verify gate pre-drafted for
			// this branch, so the common landing opens on a filled box
			// instead of on a ~60s pass; Redraft passes true.
			return m.engine.LandingMessage(dctx, feature, fresh)
		})
		d.baseBranch = m.baseBranch(f) // see openSquashDialog's own wiring
		// the cards a closing takes the worktree and branch of: every card
		// a landing closes, outside a stack
		d.closes = !f.Stacked() && !f.MainCheckout
		m.Overlay.Push(d)
		// start the draft pass off the render loop; the dialog is already
		// open and editable, and the draft fills only while unmodified.
		return m, d.startDraft(false)

	case squashReadyMsg:
		m.squashPrep = false
		if msg.err != nil {
			text := msg.f.Name() + " squash failed: " + msg.err.Error()
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
				card:     f.ID,
				id:       "confirm-squash",
				question: "squash " + f.Name() + "?",
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

	case logLoadedMsg:
		return m, m.logLoaded(msg)

	case logPatchMsg:
		return m, m.logPatchLoaded(msg)

	case logPreparedMsg:
		return m, m.logPrepared(msg)

	case logRewrittenMsg:
		return m, m.logRewritten(msg)

	case prLinkProbeMsg:
		m.handlePRLinkProbe(msg)
		return m, nil

	case publishDetectedMsg:
		m.publishChecked, m.publishWhy = true, msg.err
		return m, nil

	case publishFactsMsg:
		m.handlePublishFacts(msg)
		return m, nil

	case publishSpanFactsMsg:
		m.handlePublishSpanFacts(msg)
		return m, nil

	case publishStepMsg:
		return m, m.handlePublishStep(msg)

	case ghDoneMsg:
		m.endGHWork(msg.id)
		return m.update(msg.inner)

	case sessionModelsMsg:
		m.handleSessionModelsMsg(msg)
		return m, nil

	case prChecksReadyMsg:
		return m, m.deliverPRChecks(msg)

	case prPullDoneMsg:
		m.notice = msg.notice
		cmds := []tea.Cmd{m.loadRows}
		if msg.newlyWritten && !m.headless {
			// the comments just pulled land on screen, not merely counted.
			// A headless board has no screen: the web face's diff tab is
			// its own, and a surface mounted here would stay mounted for
			// the life of the process (openOnLoad's rule, the same reason).
			cmds = append(cmds, m.openDiff(msg.f))
		}
		return m, tea.Batch(cmds...)

	case commitDraftMsg:
		// a late reply from a closed dialog (esc) or a stale pass (ctrl+r
		// regenerated) is dropped; apply only while the dialog is live —
		// wherever it stands: on a web board two landings can be drafting
		// at once, and the one opened second is on top.
		for i := m.Overlay.Len() - 1; i >= 0; i-- {
			if d, ok := m.Overlay.At(i).(*commitMsgDialog); ok && d.feature == msg.f {
				d.apply(msg)
				break
			}
		}
		// Record the outcome durably on the feature so a failed draft
		// survives the dialog and later inspection still sees it: the
		// reason on a failed pass, cleared on a successful draft. The write
		// runs in a command — never in Update (see the no-IO-in-Update
		// contract above).
		if msg.cancelled {
			return m, nil
		}
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

	case landConflictMsg:
		// the landing hit conflicts: the decision leads with the rebase
		// that resolves them (merge.go's landConflictMsg)
		if m.landConflicts == nil {
			m.landConflicts = map[domain.FeatureID][]string{}
		}
		m.landConflicts[msg.id] = msg.files
		return m.update(msg.notice)

	case handOffReadyMsg:
		// the preflight passed (or did not): open the confirm that names
		// what a hand-off keeps, changes and unblocks. Nothing has been
		// committed, stamped or transitioned yet — all three wait on the
		// confirm, so esc here leaves the card exactly as it was.
		if msg.err != nil {
			m.notice = noticeMsg{text: sanitize(msg.err.Error()), isErr: true}
			return m, nil
		}
		f := msg.f
		d := &confirmDialog{
			card:         f.ID,
			id:           "confirm-handoff",
			cancelLabel:  "Cancel",
			confirmLabel: "Hand off",
			question:     "hand off " + f.Name() + "?",
			detail:       handOffDetail(f, m.baseBranch(f), msg.drift, msg.goes),
		}
		if msg.goes {
			d.option = keepBranchOption
		}
		d.onConfirm = func() tea.Cmd { return m.handOffFeature(f, d.optionOn) }
		m.Overlay.Push(d)
		return m, nil

	case mergeThenDoneMsg:
		// the verify→done gate routes through the merge flow: collect the
		// user's commit message, then land + transition on ctrl+s.
		//
		// The inbox entry is NOT dropped here. It used to be, on dispatch,
		// which meant the gate's "review & land on main" vanished the moment
		// the dialog opened and stayed gone if the user pressed esc — and it
		// was also the reason the `m` key had no removal at all, since the
		// removal lived on this path rather than on the landing. It is
		// cleared on the merge's own success now (landFeature).
		if m.mergePrep[msg.f.ID] {
			m.notice = noticeMsg{text: "already preparing " + msg.f.Name() + "'s merge — wait for it", isErr: true}
			return m, nil
		}
		m.markMergePrep(msg.f.ID)
		m.notice = noticeMsg{text: msg.f.Name() + ": landing on main…"}
		return m, m.prepareMerge(msg.f)

	case rebaseConflictMsg:
		m.offerAgentRebase(msg)
		return m, nil

	case rebasedMsg:
		if msg.cleared {
			m.driftCleared(msg.id)
		}
		return m.Update(msg.notice)

	case consultSentMsg:
		delete(m.consultSending, msg.id)
		if msg.err != nil {
			m.notice = noticeMsg{text: sanitize(msg.err.Error()), isErr: true, id: msg.id}
		}
		return m, nil

	case blockersMsg:
		// fold the recomputed counts into the row the page reads. Nothing
		// else about the row is touched: this arrives while a session may
		// be streaming into the same row, and a wholesale replacement
		// would roll back whatever landed in between.
		for i := range m.rows {
			if m.rows[i].F.ID == msg.id {
				m.rows[i].OpenDiffComments = msg.openDiffComments
				break
			}
		}
		return m, nil

	case specStartedMsg:
		return m, m.loadSessionSpec(msg.f, msg.n)

	case specLoadedMsg:
		if msg.err != nil {
			m.notice = noticeMsg{text: msg.err.Error(), isErr: true}
			return m, nil
		}
		sv := &specView{f: msg.f, path: msg.path, content: msg.content, doc: spec.Parse(msg.content), cursor: 1, n: msg.n, specs: msg.specs, workflowsOff: !m.workflowsOffered(msg.f)}
		// Open where the card is. The thread pins the artifact with the
		// section this stage is about — "⌄ spec · Implementation notes"
		// and the key that opens it — and the decision's own rows name it
		// too ("read the plan — it lives in the spec's Implementation
		// notes"). Landing on line 1 after either of those made the
		// reader hunt for the thing they were just pointed at, in a
		// document long enough that the hunt is the work. A document
		// without that heading (a fresh draft) keeps the top.
		want := currentSpecSection(msg.f.Kind, msg.f.Stage)
		if line, ok := spec.HeadingLine(msg.content, want); ok {
			sv.cursor = line
		}
		// A SHUT GATE OUTRANKS THE STAGE'S OWN SECTION. When the artifact
		// carries open @user comments, those comments are the reason the
		// card cannot move and the reason the reader was sent here — the
		// decision block's only row says so ("resolve open comments — N
		// open … x resolves one"). Landing on the stage's heading instead
		// dropped the reader mid-document with the blockers off-screen at
		// L76 and L105, reachable only by walking n through every
		// resolution and template placeholder between (round 3 §3.5: seven
		// presses to reach the first one).
		if line, ok := firstBlockingComment(sv); ok {
			sv.cursor = line
		}
		if m.spec != nil && m.spec.path == msg.path {
			// reload in place: keep the cursor, clamped in case the doc
			// shrank. The window follows the cursor, so that is the whole
			// of the position to carry over.
			sv.cursor = min(m.spec.cursor, len(sv.doc.Lines))
		}
		m.spec = sv
		// the artifact just changed under the gate — a comment added or
		// resolved, or a section the stage wrote. The counts the card page
		// reads live on the row, not here, so refresh them or the page
		// goes on describing the document as it was (see refreshBlockers).
		return m, m.refreshBlockers(msg.f.ID)

	case diffLoadedMsg:
		if msg.err != nil {
			m.notice = noticeMsg{text: sanitize(msg.err.Error()), isErr: true}
			return m, nil
		}
		if msg.empty {
			text := msg.f.Name() + ": no changes in the worktree yet"
			if msg.drift != "" {
				text += " — " + msg.drift + " (r on the board, /rebase on the card)"
			}
			m.notice = noticeMsg{text: text}
			return m, nil
		}
		dv := newDiffView(msg.f, msg.diff, msg.anns)
		dv.drift = msg.drift
		if m.diff != nil && m.diff.f.ID == msg.f.ID {
			// reload in place: keep the cursor, clamped in case the diff
			// shrank (e.g. after a fix-up run). The window follows the
			// cursor, so that is the whole of the position to carry over.
			dv.setCursor(m.diff.cursor)
		}
		m.diff = dv
		// same reason as specLoadedMsg: adding or resolving a diff comment
		// arrives here as a reload, and the gate's count lives on the row.
		return m, m.refreshBlockers(msg.f.ID)

	case weekReportMsg:
		if d, ok := m.Overlay.Top().(*weekDialog); ok {
			rep := msg.report
			d.report = &rep
		}
		return m, nil

	case sweepPlannedMsg:
		// the measurement landed: hand it to the pass if it is still open.
		// A pass the reader has already left simply drops it — nothing was
		// changed by measuring.
		if d, ok := m.Overlay.Top().(*closeOutDialog); ok {
			plan := msg.plan
			d.sweep = &plan
		}
		return m, nil

	case sweptMsg:
		m.notice = noticeMsg{
			text:   sweptText(msg),
			reload: true,
		}
		return m, m.refreshWorktreeSize()

	case worktreeSizeMsg:
		m.worktreeSizeText = msg.text
		return m, nil

	case cardIssueMsg:
		if msg.err != nil {
			msg.form.failImport(msg.err)
		} else {
			msg.form.applyIssue(msg.prop, msg.ref)
		}
		return m, nil

	case cardCreatedMsg:
		return m, m.cardCreated(msg)

	case draftFailedMsg:
		return m, m.draftFailed(msg)

	case foreignTickMsg:
		// keep the probe running whether or not anything is driven
		// elsewhere: a run can start in another terminal at any moment.
		return m, tea.Batch(foreignTick(), m.probeForeign())

	case foreignMsg:
		return m, m.applyForeignMsg(msg)

	case followRecordMsg:
		return m, m.applyFollow(msg)

	case followClosedMsg:
		// the tail ended (the pane closed, or its context was canceled).
		// Nothing to do: the pane, if still open, keeps its last view.
		return m, nil

	case todaySpentMsg:
		m.todaySpent(msg)
		return m, nil

	case todayDueMsg:
		m.today.due = false
		return m, m.measureToday()

	case engineEventMsg:
		cmd := m.handleEngineEvent(msg.ev)
		// engine events otherwise carry no payload the view needs — they
		// just signal "re-render from Snapshot" — so keep listening, plus
		// any automatic review-loop follow-up and, when the event means
		// the open card's event log grew, a reload of it.
		return m, tea.Batch(m.listenEngineCmd(), cmd, m.refreshOpenCardEvents(msg.ev), m.openRunsChanged(msg.ev), m.openDispatchesChanged(msg.ev), m.measureToday())

	case engineClosedMsg:
		// the agent backend shut down unexpectedly. There is no pane left
		// to freeze — a card's own thread renders its session's error —
		// but anyone with a live session was watching something that just
		// stopped, so say why.
		m.notice = noticeMsg{text: "agent backend stopped", isErr: true}
		return m, nil

	case lastSeenMsg:
		// Merge rather than replace: a card can be opened and marked read
		// (markSeen, into pendingSeen while lastSeen is still nil) before
		// this startup reply lands, and that mark must survive the reply
		// rather than be overwritten by the store's pre-mark value
		// (BG-056). Take the max seq per card so neither side's knowledge
		// is lost.
		if msg.err == nil {
			seqs := msg.seqs
			if seqs == nil {
				seqs = map[domain.FeatureID]int64{}
			}
			for id, seq := range m.pendingSeen {
				if seq > seqs[id] {
					seqs[id] = seq
				}
			}
			m.pendingSeen = nil
			m.lastSeen = seqs
		}
		return m, nil

	case cardEventsMsg:
		// a late reply for a card the page has since moved off (esc, or a
		// second J/K before the first load landed) is dropped: the cache
		// keys on the feature it belongs to and nothing renders a stale
		// key by mistake, but there's nothing to gain by keeping a fetch
		// racing behind the current selection.
		if msg.err == nil {
			m.cardEvents[msg.id] = msg.events
			return m, m.markSeen(msg.id, msg.events)
		}
		return m, nil

	case tea.KeyPressMsg:
		// ctrl+c is hoisted above the overlay: it is the one key every
		// terminal program is expected to answer, and routing it into an
		// open dialog's text input (which is what happened) left no way
		// out of a modal but esc. Quit is what it does everywhere.
		if msg.String() == "ctrl+c" {
			return m, m.quitCmd()
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
	}
	return m, nil
}

// handlePaste routes bracketed-paste text to whichever pane input is
// editing; a paste with no input focused is dropped.
func (m *Shell) handlePaste(msg tea.PasteMsg) tea.Cmd {
	// Scoped to the board tab, exactly as handleKey scopes the same
	// surface (its boardSurfacesLive gate). Both composers can report
	// Focused() at once — a card page stays open across a tab switch and
	// nothing blurs its input — so an ungated test here answered for
	// whichever branch came first, and this one did.
	if m.boardSurfacesLive() && m.draft != nil {
		return m.draftPaste(msg)
	}
	if m.boardSurfacesLive() && m.cardOpen && m.stepv != nil {
		if m.stepv.composing {
			var cmd tea.Cmd
			m.stepv.input, cmd = m.stepv.input.Update(msg)
			return cmd
		}
		return nil
	}
	if m.boardSurfacesLive() && m.cardOpen && m.threadInput.Focused() {
		return m.handleThreadPaste(msg)
	}
	return nil
}

// quitCmd is the shared exit path for q and ctrl+c. Nothing runs when no
// one is at the keyboard — a session's turn is the person's — so quitting
// asks nothing: a session's conversation outlives the process, and its
// next turn respawns a backend carrying it.
func (m *Shell) quitCmd() tea.Cmd {
	return tea.Quit
}

// handleKey routes one key press. Its shape is the keymap's tiers made
// literal, read top to bottom:
//
//	tier 1  alt+1/2/3/4 — answered here, above every surface, so a tab is
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
func (m *Shell) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "alt+1":
		return m.gotoTab(TabBoard)
	case "alt+2":
		return m.gotoTab(TabStats)
	case "alt+3":
		return m.gotoTab(TabInbox)
	case "alt+/":
		// the help key that is always gummi's. ? is the convenient one,
		// but it is also ordinary punctuation, so it has to yield wherever
		// the user is typing prose — the chat box, the bug-import filter,
		// the board's own composer. Those are exactly the surfaces whose
		// key rules are least guessable, which left the help unreachable
		// in the places it was most wanted. alt is the prefix for keys a
		// terminal multiplexer won't have claimed (DESIGN).
		m.Overlay.Push(m.helpOverlay())
		return nil
	}
	if key == "tab" {
		return m.nextTab()
	}
	if key == "?" && !m.textEntry() {
		m.Overlay.Push(m.helpOverlay())
		return nil
	}
	// tier 3: whichever surface owns the main pane, in the same order
	// mainView paints them, and only on the tab they belong to.
	if m.boardSurfacesLive() {
		// a new session's draft is over everything on the board tab
		if m.draft != nil {
			m.clearTransientNotice()
			return m.handleDraftKey(msg)
		}
		// the card page's tabs, answered before any of the three surfaces
		// they switch between gets the key (cardtabs.go)
		if m.cardOpen && m.stepv != nil {
			// a step opened over the thread answers first: its own tabs,
			// its line to the agent, and the way back (stepview.go)
			m.clearTransientNotice()
			return m.handleStepKey(msg)
		}
		if m.cardOpen {
			if cmd, ok := m.cardTabKey(key); ok {
				return cmd
			}
		}
		// The review surfaces clear a standing notice before acting on the
		// key. Elsewhere a notice is dropped on a view change, which is
		// enough — but these two are surfaces a reader stays on for many
		// keystrokes, resolving comments one after another, and a refusal
		// raised by one of those keys used to sit under all the rest of
		// them. "stage todo has no agent action" was still on the bar
		// several successful resolves later, describing nothing that had
		// happened since. A key that acts here replaces the answer to the
		// last key; one that fails raises its own notice again anyway.
		if m.spec != nil {
			m.clearTransientNotice()
			return m.handleSpecKey(key)
		}
		if m.diff != nil {
			m.clearTransientNotice()
			return m.handleDiffKey(key)
		}
		if m.stats != nil {
			m.clearTransientNotice()
			return m.handleStatsKey(key)
		}
		if m.logv != nil {
			m.clearTransientNotice()
			return m.handleLogKey(key)
		}
		if m.cardOpen && m.wf.focus {
			m.clearTransientNotice()
			return m.handleRunKey(key)
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

// cleanUpNudge is the tail of every notice telling the reader a landed
// branch is waiting to be cleaned up (merge.go, squash.go, and the
// board's own m/z guards below). It used to read "press c to clean up",
// which is only true on the board: c is the cleanup key there, but these
// same notices land on the card page too — right where a user stands the
// moment they merge — and on that surface c types the letter into the
// thread composer, with enter ready to send it to an agent (§2.3). "clean
// up" is also the action's own name (nextsteps.go's landed-card action
// row, and the board's own key label in keymap.go), so naming the action
// instead of a key is truthful on both surfaces without rebinding
// anything — the composer keeping every printable key on the card page
// is a deliberate design decision, not an oversight this notice should
// paper over.
// cleanUpDetail is what the clean-up confirm says it will remove.
func cleanUpDetail(r featureRow) string {
	if r.Landed {
		return "removes the worktree (incl. untracked files) and merged branch — keeps the record"
	}
	return "removes the worktree, and the branch unless it is the only copy of its work — keeps the record"
}

const cleanUpNudge = "clean up removes the worktree and branch"

// keepBranchOption names the choice a closing offers beside its yes: a
// closed card's worktree and branch go with it unless the person says to
// keep them.
const keepBranchOption = "keep the branch and worktree"

// gotoTab switches to t and does the arrival work every route into a tab
// shares, so alt+N and the tab cycle cannot drift apart on it.
func (m *Shell) gotoTab(t Tab) tea.Cmd {
	m.setTab(t)
	if m.tab == TabStats {
		// The stats tab measures on first arrival so it never opens on a
		// page nobody has read yet, and re-arms nothing afterwards — its
		// tick handles the refresh, and r handles the impatient.
		return m.ensureWsStats()
	}
	return nil
}

// textEntry reports whether the surface holding the keyboard is taking
// free-form text right now. Only ? consults it: every other global is a
// modifier chord (alt+N) or a key no text field wants (tab), but a
// question mark is ordinary punctuation, and a user typing "should we
// retry?" into a chat must get the character rather than the help
// overlay.
func (m *Shell) textEntry() bool {
	if !m.boardSurfacesLive() {
		return false
	}
	// The thread composer stays focused for as long as the card page is
	// open, including while a spec or diff review surface is drawn over
	// it and answering the keyboard itself (handleKey's tier 3: m.spec
	// and m.diff are checked before the cardOpen-and-focused thread-input
	// fallback). Reporting textEntry here regardless used to make "?"
	// (both surfaces list it in their own key table, footer and all) type
	// a literal "?" into a draft nobody was looking at instead of opening
	// help — the one key their tables advertised that did nothing (§2.1).
	// The reason this function exists is "don't steal a printable key
	// from someone typing prose", and that reason does not hold when a
	// review surface, not the composer, is what the user is actually
	// looking at and typing into.
	if m.draft != nil {
		return m.draft.typing()
	}
	if m.cardOpen && m.stepv != nil {
		return m.stepv.composing
	}
	if m.cardOpen && m.wf.focus {
		return false
	}
	return m.cardOpen && m.threadInput.Focused() && m.spec == nil && m.diff == nil
}

// boardKey answers the board's keys. It is split out from handleKey so
// the card action list and the command menu can invoke an action by name
// without a second copy of the guards each case carries (a research card
// refusing a merge, a card with no worktree refusing a diff). Both paths
// funnel through here, so what a surface offers and what the handler
// does cannot drift apart.
func (m *Shell) boardKey(key string) tea.Cmd {
	// tab, alt+1/2/3 and ? never arrive here: handleKey answers them
	// above every surface, which is what makes them global rather than
	// "global as long as nothing is open".
	if m.tab == TabStats {
		return m.wsStatsKey(key)
	}
	if m.tab == TabInbox {
		return m.inboxKey(key)
	}
	if m.tab != TabBoard {
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
	// a card something else is already driving is read-only here: refuse
	// the verbs that would write to it and name the driver, rather than
	// racing it or failing deeper in with a confusing error. The action
	// list withholds exactly this set, so what the board offers and what
	// it answers stay in lockstep. Two drivers: another gummi process,
	// and the lead of the goal a card belongs to (featureRow.watchOnly).
	if r, ok := m.selected(); ok && r.watchOnly() && foreignBlockedKeys[key] {
		m.notice = noticeMsg{
			text:  fmt.Sprintf("%s is driven by %s — read-only here (enter watches it)", r.F.Name(), r.watchDriver()),
			isErr: true,
			id:    r.F.ID,
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
			// a turn in flight is exactly what a reader who can see it
			// going the wrong way wants to stop
			if cmd, handled := m.interruptFreeform(r.F); handled {
				return cmd
			}
			m.notice = noticeMsg{text: r.F.Name() + ": nothing running to stop", id: r.F.ID}
			return nil
		}
	case "t":
		if r, ok := m.selected(); ok {
			m.clearTransientNotice()
			return m.openThread(r.F)
		}
	case "s":
		if r, ok := m.selected(); ok {
			m.clearTransientNotice()
			if !cardHasArtifact(r) {
				// A card with no document (or no spec) to open (DESIGN §19).
				// The key is filtered out of this card's bindings, so nothing
				// advertises it here — but the handler still answers, the way
				// every other withheld key on this board does.
				m.notice = noticeMsg{text: r.F.Name() + ": " + noArtifactReason(r), id: r.F.ID}
				return nil
			}
			return m.openSpec(r.F)
		}
	case "d":
		if r, ok := m.selected(); ok {
			if n := branchVerbRefusal(r, "diff"); n != nil {
				m.notice = *n
				return nil
			}
			m.clearTransientNotice()
			return m.openDiff(r.F)
		}
	case "a":
		if r, ok := m.selected(); ok {
			// attach needs a worktree to attach into, so it takes the same
			// refusal as the branch verbs rather than reaching resolveAttach
			// and coming back with a worse one.
			if n := branchVerbRefusal(r, "attach"); n != nil {
				m.notice = *n
				return nil
			}
			return m.attachRaw(r.F)
		}
	case "j", "down":
		m.moveSel(1)
	case "k", "up":
		m.moveSel(-1)
	case "pgup":
		if order := m.displayOrder(); len(order) > 0 {
			m.sel = order[0]
			m.syncActionFocus()
		}
	case "pgdown":
		if order := m.displayOrder(); len(order) > 0 {
			m.sel = order[len(order)-1]
			m.syncActionFocus()
		}
	case "n":
		return m.openSessionDraft(nil)
	case "T":
		// Stack a new card on TOP of the selected one. This is the whole
		// of setting a stack up: the stack is created by the act of
		// stacking a second card onto a first, so there is no "new
		// stack" dialog to find and the common case costs one key.
		if cmd := m.openStackForm(); cmd != nil {
			return cmd
		}
	case "W":
		// The one screen that answers "was running this worth it". It
		// reports and never acts: every key that would change something
		// belongs on the card it would change.
		return m.openWeek()
	case "L":
		// Schedules and heartbeats: what comes back on a clock without a
		// person typing. The list is the only place the board shows them,
		// so the key lives beside W's — another whole-board surface.
		return m.openSchedules()
	case "P":
		// The profiles: which agent and model each role runs on, and a
		// person's edits of them — the same file the web page's Profiles
		// view writes.
		return m.openProfiles()
	case "E":
		// The skill pack and its curator (DESIGN §23): the one place a
		// person checks, pins and reverts what reaches every session
		// without them. K would read better, but the card page steps
		// cards with J/K and lists the board's keys beside its own.
		return m.openCurator()
	case "C":
		// The board-wide counterpart of c: c tidies one landed card, C
		// closes out the session — walk what is ready to land, then sweep
		// what landing left behind. Uppercase for the bigger act, and the
		// pair is the mnemonic.
		return m.openCloseOut()
	case "u":
		if r, ok := m.selected(); ok {
			if n := envelopeRefusal(r); n != nil {
				m.notice = *n
				return nil
			}
			m.Overlay.Push(newEnvelopeDialog(r.F, func(to int) tea.Cmd {
				return m.setEnvelope(r.F.ID, to)
			}))
		}
	case "o":
		if r, ok := m.selected(); ok {
			if r.HasWorktree {
				m.notice = noticeMsg{text: r.F.Name() + ": repo is fixed once a worktree exists", isErr: true}
				return nil
			}
			if len(m.repoNames) == 0 {
				// nothing to offer yet: a discovered set may have one now, and
				// the picker opens when it lands (landRepos)
				if rescan := m.rescanRepos(r.F.ID); rescan != nil {
					return rescan
				}
				m.notice = noticeMsg{text: "no other repositories configured"}
				return nil
			}
			// the picker opens on the set the board has; a rescan on this
			// press (Update) re-offers a clone made since launch
			m.Overlay.Push(newRepoPickerDialog(r.F, m.repoNames, func(repo string) tea.Cmd {
				return m.setRepo(r.F.ID, repo)
			}))
		}
	case "r":
		if r, ok := m.selected(); ok {
			if n := branchVerbRefusal(r, "rebase"); n != nil {
				m.notice = *n
				return nil
			}
			if n := rebaseRefusal(r); n != nil {
				m.notice = *n
				return nil
			}
			return m.rebaseFeature(r.F)
		}
	case "h":
		if r, ok := m.selected(); ok {
			if n := branchVerbRefusal(r, "hand-off"); n != nil {
				m.notice = *n
				return nil
			}
			// only the person can say when a card's work is finished, and
			// saying so IS the ending (DESIGN §19)
			return m.prepareHandOff(r.F)
		}
	case "m":
		if r, ok := m.selected(); ok {
			if n := branchVerbRefusal(r, "merge"); n != nil {
				m.notice = *n
				return nil
			}
			if r.Landed {
				m.notice = noticeMsg{text: r.F.Name() + " already landed on main — " + cleanUpNudge, isErr: true}
				return nil
			}
			if m.mergePrep[r.F.ID] {
				m.notice = noticeMsg{text: "already preparing " + r.F.Name() + "'s merge — wait for it", isErr: true}
				return nil
			}
			m.markMergePrep(r.F.ID)
			m.notice = noticeMsg{text: r.F.Name() + ": preparing merge…"}
			// a card goes to done with its landing: landing IS its
			// ending (domain.Feature.MayLand)
			return m.prepareMerge(r.F)
		}
	case "z":
		if r, ok := m.selected(); ok {
			if n := branchVerbRefusal(r, "squash"); n != nil {
				m.notice = *n
				return nil
			}
			if r.Landed {
				m.notice = noticeMsg{text: r.F.Name() + " already landed on main — " + cleanUpNudge, isErr: true}
				return nil
			}
			if m.squashPrep {
				m.notice = noticeMsg{text: r.F.Name() + " already preparing a squash — wait for it", isErr: true}
				return nil
			}
			m.squashPrep = true
			m.notice = noticeMsg{text: r.F.Name() + ": preparing squash…"}
			return m.prepareSquash(r.F)
		}
	case "c":
		if r, ok := m.selected(); ok {
			if n := branchVerbRefusal(r, "cleanup"); n != nil {
				m.notice = *n
				return nil
			}
			if !r.cleanable() {
				m.notice = noticeMsg{text: r.F.Name() + " hasn't landed on " + r.baseBranch() + " yet", isErr: true}
				return nil
			}
			f := r.F
			m.Overlay.Push(&confirmDialog{
				card:         f.ID,
				id:           "confirm-cleanup",
				cancelLabel:  "Keep",
				confirmLabel: "Clean up",
				question:     "clean up " + f.Name() + "?",
				detail:       cleanUpDetail(r),
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
			detail := f.Title + " — removes worktree, branch, and record"
			m.Overlay.Push(&confirmDialog{
				card:         f.ID,
				id:           "confirm-delete",
				cancelLabel:  "Keep",
				confirmLabel: "Delete",
				question:     "delete " + f.Name() + "?",
				detail:       detail,
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
// user into an unrelated surface. An error notice is kept only while the
// selection still names the same feature it was raised about — it carries
// something the user still needs to read on that surface, but has no
// standing once the user has moved to an unrelated card. An error with no
// feature stamped on it (id == "") is not scoped to any surface and is
// cleared like any other transient notice.
func (m *Shell) clearTransientNotice() {
	if m.notice.isErr && m.notice.id != "" {
		if r, ok := m.selected(); ok && r.F.ID == m.notice.id {
			return
		}
	}
	m.notice = noticeMsg{}
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
	m.syncActionFocus()
	// A notice names the card it is about, so moving to a different card
	// leaves it describing something the reader is no longer looking at.
	// Cleared AFTER the selection moves, not before: the one notice
	// clearTransientNotice keeps is an error about the selected card, and
	// that question has to be asked of the card being moved TO.
	m.clearTransientNotice()
}

// jumpSel selects the nth visible card (1-based), matching the numbers
// shown on the board.
func (m *Shell) jumpSel(n int) {
	order := m.displayOrder()
	if n >= 1 && n <= len(order) {
		m.sel = order[n-1]
		m.syncActionFocus()
	}
}

// displayOrder lists row indices in board display order: grouped by
// super-state, chronological within each.
func (m *Shell) displayOrder() []int {
	order, _ := m.displayOrderNested()
	return order
}

// displayOrderNested is displayOrder with the open sessions an agent
// dispatched moved under it (nestDispatched), and which rows those are.
func (m *Shell) displayOrderNested() ([]int, map[int]int) {
	var order []int
	for _, super := range domain.SuperStates {
		var idxs []int
		for i, r := range m.rows {
			// A folded archive row is not in the order at all, which is
			// what keeps the jump numbers contiguous and stops alt+j/alt+k
			// from walking through the graveyard — half of what made the
			// unbounded DONE group cost something rather than merely look
			// untidy.
			if m.archived(m.rows[i]) {
				continue
			}
			if r.F.Stage.SuperState() == super {
				idxs = append(idxs, i)
			}
		}
		order = append(order, idxs...)
	}
	return m.nestDispatched(order)
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
	order := m.displayOrder()
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
	l := layout.Compute(m.width, m.height)
	if m.railWanted() {
		l = layout.WithRail(l, railWidth, railMinMain)
	}
	return l
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
	return m.spec == nil && m.diff == nil
}

// View implements tea.Model: compute the buffer, paint the panes, the
// status bar, then the dialog stack.
func (m *Shell) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.BackgroundColor = m.styles.Theme.BgBase
	v.WindowTitle = "gummi"
	// Mouse reporting is never requested: asking for it would suppress
	// the terminal's own click-drag selection across the whole program,
	// which no surface here has a use for.

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
// background (bands, pills) keep it.
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
	// the rail comes and goes with the card page, so the layout is
	// carved per frame rather than only on a resize
	l := m.computeLayout()
	if !l.Rail.Empty() {
		uv.NewStyledString(m.railView(l.Rail.Dx(), l.Rail.Dy())).Draw(scr, l.Rail)
	}

	uv.NewStyledString(m.tabBarView(l.Tabs.Dx())).Draw(scr, l.Tabs)
	// a long error/remedy, or any notice of several lines, is wrapped
	// into a band above the status bar rather than truncated into a
	// one-line pill ("set permiss…"); it borrows the bottom rows of the
	// main pane. Short notices stay pills.
	// Every surface goes through mainView below.
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

// noticeBand renders a notice that does not fit a status pill as wrapped
// lines for the band above the status bar, or nil when the notice is
// short enough to ride as one. Two kinds go there: a long error/remedy,
// in the error colour, and any notice of more than one line — a pill is
// one row, so a notice that says what happened and then the commands to
// run about it (an automatic stack replay's pushes) would lose every line
// but its first. Routine one-line status stays a quiet pill.
func (m *Shell) noticeBand(w int) []string {
	if !m.noticeInBand() || w < 8 {
		return nil
	}
	style := m.styles.Base
	if m.notice.isErr {
		style = m.styles.Error
	}
	var out []string
	for _, para := range strings.Split(sanitize(m.notice.text), "\n") {
		// wrapText folds runs of spaces, which would flatten the indent
		// that sets a command apart from the sentence introducing it: keep
		// each line's own indent and wrap what follows it.
		body := strings.TrimLeft(para, " \t")
		indent := strings.Repeat(" ", min(len(para)-len(body), w/2))
		for _, l := range strings.Split(wrapText(body, max(w-len(indent), 1)), "\n") {
			out = append(out, style.Render(indent+l))
		}
	}
	return out
}

// noticeInBand reports whether the current notice is being shown in the
// band (so statusView omits its pill and doesn't double it).
func (m *Shell) noticeInBand() bool {
	if m.notice.text == "" {
		return false
	}
	return strings.Contains(m.notice.text, "\n") || (m.notice.isErr && len(m.notice.text) > noticeThreshold)
}

// attachOrRun handles `enter` on the board: a card another process owns
// can only be watched. Nothing else runs on a keypress — a session's turn
// starts with a line typed on its card, and a retired workflow card never
// runs again.
func (m *Shell) attachOrRun(f domain.Feature) tea.Cmd {
	if _, ok := m.foreignFor(f.ID); ok {
		return m.watchForeign(f)
	}
	if m.engine == nil {
		m.notice = noticeMsg{text: m.noAgent(" (set a model/provider to enable agents)"), isErr: true}
		return nil
	}
	m.notice = noticeMsg{text: f.Name() + " is in " + string(f.Stage) + " — nothing to run", isErr: true}
	return nil
}

// openThread opens the card's page. It used to be openTranscript, which
// toggled a separate transcript view on top of the page — the product
// call was that the thread already is the conversation, so a second mode
// showing the same events a different way was redundant, and it is gone.
// What is left, and what still earns t its own key rather than folding
// into enter, is the routing: a card another process is driving has no
// session this board can attach, so it opens the read-only watch instead
// of trying (and failing) to run it the way enter would.
func (m *Shell) openThread(f domain.Feature) tea.Cmd {
	if _, ok := m.foreignFor(f.ID); ok {
		return m.watchForeign(f)
	}
	return m.openCard()
}

// parkVerb is fireVerb's landing spot for the composer's "park" verb
// (threadinput.go): stopping the session's turn in flight, the act
// boardVerb's "p" takes. On a card with nothing to stop, it says so.
func (m *Shell) parkVerb() tea.Cmd {
	r, ok := m.selected()
	if !ok {
		return nil
	}
	// On a freeform card the turn in flight is what there is to park, and
	// it is the one a reader typing the word is looking at: the card page
	// owns every printable key, so "/park" is how the stop is reached from
	// the surface the turn is streaming into.
	if cmd, handled := m.interruptFreeform(r.F); handled {
		return cmd
	}
	m.notice = noticeMsg{text: r.F.Name() + ": nothing running to park"}
	return nil
}

// consultFor returns the feature's consult session if one has ever been
// opened, or nil — a lookup, never a spawn (engine.Engine.Consult's own
// contract), so rendering a card's thread never opens a consult backend
// just by being drawn.
func (m *Shell) consultFor(id domain.FeatureID) *engine.ConsultSession {
	if m.engine == nil {
		return nil
	}
	return m.engine.Consult(id)
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
	name := m.cardName(id)
	return func() tea.Msg {
		ctx := context.Background()
		if err := m.engine.TopUp(ctx, id); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: name + " topped up — resuming", reload: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s topped up — budget raised to %s, resuming",
			id, domain.FormatDollars(float64(f.Budget.Envelope))), reload: true}
	}
}

// setRepo durably changes a feature's managed repository. It is the
// command backing the o repo picker; the board reloads on success so the
// repo badge re-renders immediately.
func (m *Shell) setRepo(id domain.FeatureID, repo string) tea.Cmd {
	name := m.cardName(id)
	return func() tea.Msg {
		updated, err := m.engine.SetRepo(context.Background(), id, repo)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		label := updated.Repo
		if label == "" {
			label = "default"
		}
		return noticeMsg{text: fmt.Sprintf("%s: repo set to %s", name, label), reload: true}
	}
}

// setEnvelope durably sets a feature's envelope to an explicit credit
// figure (the u envelope dialog). Unlike topUpBudget it resumes
// nothing: a budget-gated feature stays in the inbox, where enter or
// its own u picks the work back up.
func (m *Shell) setEnvelope(id domain.FeatureID, to int) tea.Cmd {
	if m.engine == nil {
		m.notice = noticeMsg{text: m.noAgent(" — budgets meter agent spend"), isErr: true}
		return nil
	}
	name := m.cardName(id)
	return func() tea.Msg {
		if err := m.engine.RaiseEnvelope(context.Background(), id, to); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if to == 0 {
			return noticeMsg{text: name + ": budget removed — spend is uncapped", reload: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s: budget set to %s (applies from the next agent session)", name, domain.FormatDollars(float64(to))), reload: true}
	}
}

// budgetAttentionText is the sentence a stage records when it runs out
// of credits, in its two flavours: work committed and ready to advance,
// or stopped with nothing banked.
//
// It names the surface to act on rather than the keys to press, because
// it is written once and read twice — on the inbox row, where u and x
// really do top up and park, and as the reason the card's own history
// gives for the run stopping. The card page composer owns every
// printable key (BG-078), so a bare letter offered there types itself
// into the message box one line under the sentence offering it. The
// card page's own next step already points at the inbox; this is the
// line that used to disagree with it.
func budgetAttentionText(stage domain.Stage, committed bool) string {
	// A freeform card has no stage to name and nothing to advance to: its
	// turns are committed as they happen, so the honest sentence is that
	// the conversation has stopped and what would restart it.
	if stage == domain.StageOpen {
		return "spent its budget — its work is committed; top it up to carry on"
	}
	if committed {
		return string(stage) + " reached its budget with work committed — advance it, or top it up from the inbox"
	}
	return string(stage) + " hit its budget — top it up or park it from the inbox"
}

func (m *Shell) mainView(w, h int) string {
	if m.boardSurfacesLive() {
		// With a card page open, the artifact and the diff are that page's
		// tabs rather than surfaces that replaced it, so each draws the
		// bar naming where it is and how to get back (cardtabs.go).
		// Reached from the backlog list there is no page underneath and no
		// bar — the same surface, mounted without a card page around it.
		if m.draft != nil {
			return m.draftView(w, h)
		}
		if m.stepv != nil && m.cardOpen {
			// a step opened over the thread, with its own crumb back
			return m.stepViewRender(w, h)
		}
		if m.spec != nil {
			if m.cardOpen {
				return m.cardSurface(cardTabArtifact, w, h, m.specViewRender)
			}
			return m.specViewRender(w, h)
		}
		if m.diff != nil {
			if m.cardOpen {
				return m.cardSurface(cardTabDiff, w, h, m.diffViewRender)
			}
			return m.diffViewRender(w, h)
		}
		if m.stats != nil {
			if m.cardOpen {
				return m.cardSurface(cardTabStats, w, h, m.statsViewRender)
			}
			return m.statsViewRender(w, h)
		}
		if m.logv != nil {
			if m.cardOpen {
				return m.cardSurface(cardTabLog, w, h, m.logViewRender)
			}
			return m.logViewRender(w, h)
		}
	}
	switch m.tab {
	case TabStats:
		return m.wsStatsRender(w, h)
	case TabInbox:
		return m.inboxView(w, h)
	}
	if len(m.rows) > 0 {
		// the board tab owns the whole pane: the backlog list, or one
		// card's page opened out of it.
		if m.cardOpen {
			// no cardSurface here: the thread tab's bar rides the card
			// page's own chrome line rather than taking a second row of
			// its own (cardPageView). The artifact and diff have no such
			// line, so those two get the standalone bar above.
			return m.cardPageView(w, h)
		}
		return m.backlogView(w, h)
	}
	return logo.Splash(m.styles, m.version, w, h)
}

func (m *Shell) statusView(w int) string {
	pills := []statusbar.Pill{
		{Text: m.brand(), Kind: statusbar.KindMode},
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
	if len(m.mergePrep) > 0 {
		pills = append(pills, statusbar.Pill{Text: m.spinner() + " merging", Kind: statusbar.KindNeutral})
	}
	if len(m.ghWork) > 0 {
		pills = append(pills, statusbar.Pill{Text: m.ghWorkPill(), Kind: statusbar.KindNeutral})
	}
	if m.squashPrep {
		pills = append(pills, statusbar.Pill{Text: m.spinner() + " squashing", Kind: statusbar.KindNeutral})
	}
	if n := m.inbox.len(); n > 0 {
		// agrees with its own count: "✉ 1 need you" was on the most-read
		// line on screen (round 3 §5.5).
		need := " need you"
		if n == 1 {
			need = " needs you"
		}
		pills = append(pills, statusbar.Pill{Text: "✉ " + strconv.Itoa(n) + need, Kind: statusbar.KindAlert})
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
	hints := barHints(bindings)
	// …except while a modal has the keyboard. Then the surface's keys are
	// not what the next keystroke does — the bar would read "enter land on
	// main" over a merge dialog where enter activates a button — and every
	// dialog already draws its own hint row inside its frame (buttons.go's
	// "enter activates the focused control" convention, and the same rule
	// agentBindings follows when the completion popup is open). All that
	// is left to say from out here is the one key every dialog answers to.
	if m.Overlay.HasDialogs() {
		hints = []statusbar.Hint{{Key: "esc", Label: "close"}}
	}
	return statusbar.Render(m.styles, w, pills, hints)
}

// runCounts summarizes live agent sessions for the status bar
// (⬤ running), empty when nothing is running.
func (m *Shell) runCounts() string {
	if m.engine == nil {
		return ""
	}
	// counted the way the board row and the web header count a running
	// card (webRow): needs-you outranks busy, and anything else at work — a
	// freeform turn, a check, a scribe pass — is running, so the status bar
	// and the stats tab say one number
	var running int
	for _, r := range m.rows {
		if _, needs := m.inbox.get(r.F.ID); needs {
			continue
		}
		if m.cardBusy(r) {
			running++
		}
	}
	if running == 0 {
		return ""
	}
	return "⬤ " + strconv.Itoa(running) + " running"
}

// markMergePrep notes that a card's landing preconditions are being
// checked, so a second press on the same card waits for the first.
func (m *Shell) markMergePrep(id domain.FeatureID) {
	if m.mergePrep == nil {
		m.mergePrep = map[domain.FeatureID]bool{}
	}
	m.mergePrep[id] = true
}

// brand is the status bar's first pill: gummi, and the instance's name
// when it has one.
func (m *Shell) brand() string {
	if m.name == "" {
		return "gummi"
	}
	return "gummi · " + m.name
}

// rowByID is the board's row for id as it is now, or false for a card
// that is gone.
func (m *Shell) rowByID(id domain.FeatureID) (featureRow, bool) {
	for i := range m.rows {
		if m.rows[i].F.ID == id {
			return m.rows[i], true
		}
	}
	return featureRow{}, false
}
