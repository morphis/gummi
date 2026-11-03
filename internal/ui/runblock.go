package ui

// A session's workflow runs in the terminal's thread (DESIGN §25.9): each
// run is one block among the session's turns, where it was proposed,
// drawn as text from threadfold's fold of it — the same fold, fed the
// same inputs (WebDocs.runInputs), the web page draws its block from, so
// every word the block says is threadfold's. The runs are read off the
// render path (loadRuns) and a block's lines are drawn again only when
// its Rev, or what the terminal shows of it, moved.
//
// While a run is open a live strip stands above the composer
// (liveStripRows): the run, where it stands, its step at work and that
// step's action. alt+w jumps to the open run's block with the cursor on
// its active step, and steps into the blocks: ↑↓ walk a run's head and
// its step rows, enter opens a step (stepview.go), unfolds an ended run
// folded to its one line (or folds it again) and unfolds its steps, p pauses or resumes the run, x stops it and v reverts it,
// behind a confirm that lists every path it puts back, and s saves a
// recipe the session wrote inline as a workspace recipe.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/threadfold"
	"github.com/morphis/gummi/internal/ui/theme"
)

// runState is what the terminal holds of the open session's runs.
type runState struct {
	// byCard is the open session's runs as last folded, oldest first
	// (loadRuns).
	byCard map[domain.FeatureID][]foldedRun
	// loading and again coalesce reads: one asked for while another is in
	// flight is made once more after it, for the card asked about last.
	loading bool
	again   domain.FeatureID
	// cache is each block's lines by what they were drawn from (runKey).
	cache map[string][]string
	// focus says the keyboard is on the blocks (alt+w), at cursor.
	focus  bool
	cursor runTarget
	// jump asks the next frame to bring the cursor's block into view
	// from its head (alt+w).
	jump bool
	// stepsOpen holds the ended runs whose steps a person unfolded, by
	// card and run (runFoldKey).
	stepsOpen map[string]bool
	// folds is a person's own fold of an ended run, by card and run: true
	// folded to its line, false whole. A run they never touched folds as
	// threadfold says (Run.Folded). It is kept until they leave the card
	// (leaveRuns).
	folds map[string]bool
	// seen is what each step card's session last said (stepNowKey), so a
	// streaming delta that moves nothing in its block reads nothing.
	seen map[domain.FeatureID]string
}

// runTarget is a place the cursor can stand in the blocks: a run's head
// (row "") or one of its rows, by the row's key.
type runTarget struct {
	n   int
	row string
	// dispatch names the dispatched session's block an agent's cursor
	// stands on (dispatchblock.go); n and row are then unset.
	dispatch domain.FeatureID
}

// foldedRun is a run's block and the moment its run was proposed, where
// the block stands among the session's turns (threadfold places a run's
// item the same way).
type foldedRun struct {
	threadfold.Run
	at time.Time
}

type runsLoadedMsg struct {
	id   domain.FeatureID
	runs []foldedRun
}

// loadRuns reads the session's runs and folds them, off the render path:
// RunFiles compares snapshots in git.
func (m *Shell) loadRuns(id domain.FeatureID) tea.Cmd {
	r, ok := m.rowByID(id)
	if !ok || !isRunSession(r.F) {
		return nil
	}
	if m.wf.loading {
		m.wf.again = id
		return nil
	}
	d, err := m.webDocsFor(r)
	if err != nil {
		return nil
	}
	m.wf.loading = true
	return func() tea.Msg {
		ins, said := d.runFold(context.Background())
		runs := make([]foldedRun, 0, len(ins))
		for i, r := range threadfold.FoldRuns(ins, said) {
			runs = append(runs, foldedRun{Run: r, at: ins[i].Run.CreatedAt})
		}
		return runsLoadedMsg{id: id, runs: runs}
	}
}

// openRunsChanged reads the open card's runs again when ev moved anything
// its blocks draw: an event of the session's own, or one of a step's that
// changed what the step's row says.
func (m *Shell) openRunsChanged(ev engine.Event) tea.Cmd {
	if !m.cardOpen || ev.Feature == "" {
		return nil
	}
	open := m.selectedID()
	if ev.Feature == open {
		// the session's own stream moves nothing a block draws; what a run
		// does to it comes as one of these
		switch ev.Kind {
		case engine.EventWorkflow, engine.EventIdle, engine.EventStopped, engine.EventQuestion:
			// a step's state is settled by its run, after its own turn
			// ended: an open step is read again with its blocks
			var step tea.Cmd
			if m.stepv != nil {
				step = m.reloadStep(m.stepv.card)
			}
			return tea.Batch(m.loadRuns(open), step)
		}
		return nil
	}
	f, ok := m.children[ev.Feature]
	if !ok || f.Child.Of != open || m.engine == nil {
		return nil
	}
	now, _ := m.engine.StepNow(ev.Feature)
	key := fmt.Sprintf("%t|%s|%.1f|%t", now.Busy, now.Action, now.Spent, now.Ask != nil)
	if m.wf.seen[ev.Feature] == key {
		return nil
	}
	if m.wf.seen == nil {
		m.wf.seen = map[domain.FeatureID]string{}
	}
	m.wf.seen[ev.Feature] = key
	return tea.Batch(m.loadRuns(open), m.reloadStep(ev.Feature))
}

// updateRuns handles the blocks' own messages.
func (m *Shell) updateRuns(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case runsLoadedMsg:
		m.wf.loading = false
		// only the open card's runs are kept: they are read for it alone
		m.wf.byCard = map[domain.FeatureID][]foldedRun{msg.id: msg.runs}
		if m.wf.focus && m.selectedID() == msg.id {
			m.clampRunCursor()
		}
		var cmd tea.Cmd
		if again := m.wf.again; again != "" {
			m.wf.again = ""
			cmd = m.loadRuns(again)
		}
		return cmd, true
	case runsReloadMsg:
		return m.loadRuns(msg.id), true
	case revertPreviewMsg:
		if msg.err != nil {
			return errNoticeCmd(msg.err.Error()), true
		}
		m.Overlay.Push(revertConfirm(msg, m.revertRun))
		return nil, true
	case stepLoadedMsg:
		m.stepLoaded(msg)
		return nil, true
	case stepDiffMsg:
		m.stepDiffLoaded(msg)
		return nil, true
	case runArtifactsMsg:
		m.Overlay.Push(&confirmDialog{
			id: "run-artifacts", card: msg.card,
			question:     fmt.Sprintf("Run %d's artifacts", msg.run),
			detail:       msg.detail,
			confirmLabel: "Close",
			onConfirm:    func() tea.Cmd { return nil },
		})
		return nil, true
	}
	return nil, false
}

// errNoticeCmd raises text as an error notice.
func errNoticeCmd(text string) tea.Cmd {
	text = sanitize(text)
	return func() tea.Msg { return noticeMsg{text: text, isErr: true} }
}

// openRuns is the open card's runs, as last read.
func (m *Shell) openRuns() []foldedRun {
	if !m.cardOpen {
		return nil
	}
	return m.currentRuns(m.selectedID())
}

// currentRuns is a session's runs as last read, any that ended before its
// conversation as it stands began folded to its line
// (threadfold.FoldSince): a /clear on a session at rest moves no event the
// runs are read again on.
func (m *Shell) currentRuns(id domain.FeatureID) []foldedRun {
	runs := m.wf.byCard[id]
	if len(runs) == 0 || m.engine == nil {
		return runs
	}
	ff := m.engine.Freeform(id)
	if ff == nil {
		return runs
	}
	start := threadfold.ConversationStart(ff.Snapshot().Transcript)
	if start.IsZero() {
		return runs
	}
	out := make([]foldedRun, 0, len(runs))
	for _, run := range runs {
		if threadfold.RunLeftBehind(domain.RunState(run.State), run.Ended, start) {
			run.Folded = true
		}
		out = append(out, run)
	}
	return out
}

// runBlocks draws the session's runs for its thread, each at the moment it
// was proposed.
func (m *Shell) runBlocks(s *theme.Styles, r featureRow, w int) []timedBlock {
	runs := m.currentRuns(r.F.ID)
	if len(runs) == 0 {
		return nil
	}
	if m.wf.cache == nil {
		m.wf.cache = map[string][]string{}
	}
	now := m.now()
	out := make([]timedBlock, 0, len(runs))
	live := map[string]bool{}
	for _, run := range runs {
		sel := ""
		if m.wf.focus && m.wf.cursor.n == run.N {
			sel = "head"
			if m.wf.cursor.row != "" {
				sel = "row:" + m.wf.cursor.row
			}
		}
		k := runFoldKey(r.F.ID, run.N)
		open := m.wf.stepsOpen[k]
		folded := m.runFolded(r.F.ID, run.Run)
		el := runElapsed(run.Run, now)
		// a block with a step at work turns its spinner, as a working line does
		spin := ""
		if runWorking(run.Run) {
			spin = m.spinnerGlyph(m.motionEnabled)
		}
		key := strings.Join([]string{string(r.F.ID), strconv.Itoa(run.N), run.Rev, strconv.Itoa(w), sel, strconv.FormatBool(open), strconv.FormatBool(folded), el, strconv.FormatBool(m.wf.focus), spin}, "|")
		lines, ok := m.wf.cache[key]
		if !ok {
			if folded {
				lines = runFoldedLines(s, run.Run, sel == "head", w)
			} else {
				lines = runBlockLines(s, run.Run, sel, open, !m.wf.focus, el, spin, w)
			}
			m.wf.cache[key] = lines
		}
		live[key] = true
		out = append(out, timedBlock{at: run.at, lines: append(append([]string{}, lines...), "")})
	}
	// a block that moved leaves its old lines behind: keep only what this
	// frame drew
	for k := range m.wf.cache {
		if !live[k] {
			delete(m.wf.cache, k)
		}
	}
	return out
}

func runFoldKey(id domain.FeatureID, n int) string { return string(id) + "|" + strconv.Itoa(n) }

// runStepsRow is the cursor's target on an ended run's folded steps, the
// line enter unfolds them from.
const runStepsRow = "\x00steps"

// runEnded says a run ran and will run no more.
func runEnded(r threadfold.Run) bool { return domain.RunState(r.State).Ended() }

// runFolded says an ended run is drawn as its one line: as the person
// last left it on this card, else as threadfold folds it (§25.9).
func (m *Shell) runFolded(id domain.FeatureID, r threadfold.Run) bool {
	if !runEnded(r) {
		return false
	}
	if f, ok := m.wf.folds[runFoldKey(id, r.N)]; ok {
		return f
	}
	return r.Folded
}

// toggleRunFold folds an ended run to its line, or unfolds it whole: the
// person's own, kept until they leave the card. A run folded again keeps
// its steps folded too.
func (m *Shell) toggleRunFold(id domain.FeatureID, r threadfold.Run) {
	if m.wf.folds == nil {
		m.wf.folds = map[string]bool{}
	}
	k := runFoldKey(id, r.N)
	fold := !m.runFolded(id, r)
	m.wf.folds[k] = fold
	if fold {
		delete(m.wf.stepsOpen, k)
	}
}

// runFoldedLines is an ended run folded to its one line (§25.9): its
// state's glyph and its line, and ▸, which enter unfolds; selected marks
// the cursor on it. It names no key unless the cursor is on it: a thread
// of many folded runs is no column of hints.
func runFoldedLines(s *theme.Styles, r threadfold.Run, selected bool, w int) []string {
	w = max(w, 24)
	lead := s.Faint.Render("─ ")
	if selected {
		lead = s.Cursor.Render("▸ ")
	}
	left := lead + runStateStyle(s, r).Render(r.Mark) + " " + s.Subtle.Render(r.Line) + s.Faint.Render(" ▸")
	right := ""
	if selected {
		right = s.KeyHint.Render("enter") + s.Faint.Render(" unfolds it")
	}
	return []string{spread(left, right, w)}
}

// runWorking says one of a run's rows is a step at work.
func runWorking(r threadfold.Run) bool {
	for _, row := range r.Rows {
		if row.State == string(domain.StepRunning) {
			return true
		}
	}
	return false
}

// runsWorking says the open card's blocks have a step at work, whose
// spinner turns.
func (m *Shell) runsWorking() bool {
	for _, run := range m.openRuns() {
		if runWorking(run.Run) {
			return true
		}
	}
	return false
}

// runElapsed is how long a run has worked, to the minute, its pauses left
// out; "" before it starts.
func runElapsed(r threadfold.Run, now time.Time) string {
	if r.Started.IsZero() {
		return ""
	}
	return runDur(threadfold.RunWorked(r, now))
}

// runDur is a duration to the minute: "<1m", "14m", "1h12m".
func runDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// runBlockLines draws one run's block at width w. sel is what the cursor
// is on in it — "head", "row:<key>" or "" — unfolded whether an ended
// run's steps are drawn (else they are one line, the cursor's target
// "row:"+runStepsRow), hint whether its foot names the key that steps
// into the blocks, el its elapsed time and spin the spinner's frame a step
// at work is marked with.
func runBlockLines(s *theme.Styles, r threadfold.Run, sel string, stepsOpen, hint bool, el, spin string, w int) []string {
	w = max(w, 24)
	var lines []string
	bar := s.Faint.Render("│ ")
	add := func(l string) { lines = append(lines, ansi.Truncate(bar+l, w, "…")) }

	// the head: the recipe and its run, its spec, then where it stands
	lead := s.Faint.Render("┌ ")
	if sel == "head" {
		lead = s.Cursor.Render("▸ ")
	}
	left := s.Faint.Render("workflow ") + s.Title.Render(r.Recipe+" · run "+strconv.Itoa(r.N))
	if r.SpecTitle != "" {
		left += s.Faint.Render("  spec ") + s.Subtle.Render(r.SpecTitle)
	}
	right := []string{runStateStyle(s, r).Render(threadfold.RunStateWord(r))}
	if r.State != string(domain.RunProposed) && r.State != string(domain.RunDenied) {
		right = append(right, s.Muted.Render(domain.FormatDollars(r.Spent)))
	}
	if el != "" {
		right = append(right, s.Faint.Render(el))
	}
	// a narrow block gives up the elapsed time and the money before the
	// state
	for len(right) > 1 && ansi.StringWidth(strings.Join(right, " · ")) > w/2 {
		right = right[:len(right)-1]
	}
	lines = append(lines, spread(lead+left, strings.Join(right, s.Faint.Render(" · ")), w))
	if r.Reason != "" && r.State != string(domain.RunDone) {
		add(s.Subtle.Render(r.Reason))
	}
	if words := threadfold.PhaseWords(r.Phase); words != "" {
		add(s.Info.Render("gummi is " + words))
	}
	if r.AlwaysAllowed {
		l := s.Warning.Render("every run of this session starts without asking")
		if sel == "head" {
			l += s.Faint.Render(" · ") + s.KeyHint.Render("a") + s.Faint.Render(" asks again")
		}
		add(l)
	}
	ended := r.State == string(domain.RunDone) || r.State == string(domain.RunPartial) || r.State == string(domain.RunStopped)
	baseline := func() {
		b := r.Baseline
		switch {
		case b == nil:
		case b.Running:
			add(s.Faint.Render("baseline · " + threadfold.BaselineWords))
		case b.Err != "":
			add(s.Faint.Render("baseline not run · " + b.Err))
		default:
			var parts []string
			for _, it := range b.Items {
				if it.Problem {
					parts = append(parts, s.Warning.Render("⚠ "+it.Line))
					continue
				}
				parts = append(parts, s.Faint.Render(it.Line))
			}
			add(s.Subtle.Render("baseline · ") + strings.Join(parts, s.Faint.Render(" · ")))
		}
	}
	switch r.State {
	case string(domain.RunQueued):
		l := s.Subtle.Render("allowed · starts when the open run ends")
		if r.CanWithdraw && sel == "head" {
			l += s.Faint.Render(" · ") + s.KeyHint.Render("w") + s.Faint.Render(" withdraws it")
		}
		add(l)
	case string(domain.RunProposed):
		add(s.Warning.Render("waits on your answer below"))
		baseline()
		if r.SavedAs != "" {
			add(s.Faint.Render("its recipe saved as ") + s.Subtle.Render(r.SavedAs))
		}
		return lines
	case string(domain.RunDenied):
		lines[len(lines)-1] = strings.Replace(lines[len(lines)-1], "│", "└", 1)
		return lines
	}
	if len(r.Strip) > 0 {
		add(runStrip(s, r.Strip, spin))
	}
	baseline()
	if p := r.Pending; p != nil {
		add(s.Warning.Render("waits on you · ") + s.Base.Render(firstLine(p.Question)) + s.Faint.Render(" — answer it below the thread"))
	}
	rows := func() {
		for _, row := range r.Rows {
			lines = append(lines, stepRowLines(s, row, sel == "row:"+row.Key, spin, w)...)
		}
	}
	if ended {
		if len(r.Results) > 0 {
			// one line for the lot; a line each once they say before → after
			var res []string
			changes := false
			for _, x := range r.Results {
				l := verdictStyle(s, x.Verdict).Render(verdictMark(x.Verdict) + " " + x.Item)
				if x.Change != "" {
					l += s.Faint.Render(" " + x.Change)
					changes = true
				}
				res = append(res, l)
			}
			if changes {
				for _, l := range res {
					add(l)
				}
			} else {
				add(strings.Join(res, "  "))
			}
		}
		if stepsOpen {
			rows()
		} else if len(r.Rows) > 0 {
			var ids []string
			for _, x := range r.Rows {
				if x.Folded {
					ids = append(ids, fmt.Sprintf("%s ↺ %d", x.ID, x.Iter))
					continue
				}
				ids = append(ids, x.ID)
			}
			steps := s.Muted.Render(fmt.Sprintf("%d step%s", len(r.Rows), plural(len(r.Rows)))) + s.Faint.Render(" · "+strings.Join(ids, " · "))
			if sel == "row:"+runStepsRow {
				more := s.Faint.Render(" · ") + s.KeyHint.Render("enter") + s.Faint.Render(" unfolds them")
				lines = append(lines, ansi.Truncate(s.Cursor.Render("▸ ")+steps+more, w, "…"))
			} else {
				add(steps)
			}
		}
		if len(r.Commits) > 0 {
			add(s.Faint.Render("commits made while it ran: ") + s.Subtle.Render(strings.Join(r.Commits, " · ")))
		}
	} else {
		rows()
	}
	if len(r.Artifacts) > 0 {
		var arts []string
		for _, a := range r.Artifacts {
			if a.Exists {
				arts = append(arts, s.Base.Render(a.File))
			} else {
				arts = append(arts, s.Faint.Render(a.File+" (not yet)"))
			}
		}
		l := s.Faint.Render("artifacts: ") + strings.Join(arts, s.Faint.Render(" · "))
		if sel == "head" {
			l += s.Faint.Render(" · ") + s.KeyHint.Render("r") + s.Faint.Render(" reads them")
		}
		add(l)
	}
	// the foot: what the run has changed in the tree
	foot := s.Faint.Render("└ ")
	var said string
	switch {
	case len(r.Files) > 0 && ended:
		said = s.Base.Render(fmt.Sprintf("%d file%s", len(r.Files), plural(len(r.Files)))) + s.Subtle.Render(" left changed in the tree")
	case len(r.Files) > 0:
		said = s.Base.Render(fmt.Sprintf("%d file%s", len(r.Files), plural(len(r.Files)))) + s.Subtle.Render(" changed in the tree · uncommitted")
	case ended:
		said = s.Subtle.Render("the run left the tree as it found it")
	default:
		said = s.Subtle.Render("nothing changed yet")
	}
	if len(r.Files) > 0 {
		said += s.Faint.Render(": " + strings.Join(r.Files, ", "))
	}
	tail := []string{}
	if r.Report {
		tail = append(tail, s.Faint.Render(fmt.Sprintf("report in spec %d", r.Spec)))
	}
	switch {
	case r.SavedAs != "":
		tail = append(tail, s.Faint.Render("saved as ")+s.Subtle.Render(r.SavedAs))
	case r.CanSave && sel == "head":
		tail = append(tail, s.KeyHint.Render("s")+s.Faint.Render(" saves its recipe"))
	}
	if sel == "head" && len(r.Files) > 0 {
		tail = append(tail, s.KeyHint.Render("f")+s.Faint.Render(" its diff"))
	}
	if sel == "head" && r.CanRetry {
		tail = append(tail, s.KeyHint.Render("t")+s.Faint.Render(" retries its failed steps"))
	}
	if sel == "head" && r.CanForward {
		tail = append(tail, s.KeyHint.Render("F")+s.Faint.Render(" forwards what the filter kept out"))
	}
	if ended && sel == "head" {
		tail = append(tail, s.KeyHint.Render("enter")+s.Faint.Render(" folds it"))
	}
	if hint {
		tail = append(tail, s.KeyHint.Render("alt+w")+s.Faint.Render(" steps in"))
	}
	lines = append(lines, spread(foot+said, strings.Join(tail, s.Faint.Render(" · ")), w))
	return lines
}

// spread lays left and right on one line w wide, right flush to the edge
// when both fit and cut from the end when they do not.
func spread(left, right string, w int) string {
	if right == "" {
		return ansi.Truncate(left, w, "…")
	}
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap >= 2 {
		return left + strings.Repeat(" ", gap) + right
	}
	if rw := ansi.StringWidth(right); rw <= w/2 {
		// the right side is the facts; the left gives up its tail first
		left = ansi.Truncate(left, w-rw-2, "…")
		return left + strings.Repeat(" ", max(w-ansi.StringWidth(left)-rw, 2)) + right
	}
	return ansi.Truncate(left+"  "+right, w, "…")
}

// runStrip is the recipe's top-level entries, each in its state; one at
// work wears the spinner.
func runStrip(s *theme.Styles, strip []threadfold.StripEntry, spin string) string {
	return stripEntries(s, strip, spin, " → ")
}

// stripEntries is runStrip with its entries joined by sep.
func stripEntries(s *theme.Styles, strip []threadfold.StripEntry, spin, sep string) string {
	var parts []string
	for _, e := range strip {
		word := e.ID
		switch e.Kind {
		case "repeat":
			word = fmt.Sprintf("%s ↺ %d/%d", e.ID, e.Iter, e.Max)
		case "parallel":
			var ids []string
			for _, b := range e.Branches {
				ids = append(ids, b.ID)
			}
			word = strings.Join(ids, " ∥ ")
		}
		if mark := stepMark(e.State); mark != "" && e.State != "running" && e.State != "waiting" {
			word += " " + mark
		}
		style := stepStateStyle(s, e.State)
		if e.State == "running" {
			style = style.Bold(true)
			if spin != "" {
				word += " " + spin
			}
		}
		parts = append(parts, style.Render(word))
	}
	return strings.Join(parts, s.Faint.Render(sep))
}

// liveRun is the card's open run, the one its live strip is about.
func (m *Shell) liveRun(id domain.FeatureID) (threadfold.Run, bool) {
	runs := m.wf.byCard[id]
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Live != nil {
			return runs[i].Run, true
		}
	}
	return threadfold.Run{}, false
}

// liveStripRows is the thread's live strip (§25.9): the open run's one
// line above the composer, none while no run is open.
func (m *Shell) liveStripRows(s *theme.Styles, r featureRow, w int) []string {
	run, ok := m.liveRun(r.F.ID)
	if !ok {
		return nil
	}
	spin := ""
	if run.Live.Mood == threadfold.StripRunning && len(run.Live.Steps) > 0 {
		spin = m.spinnerGlyph(m.motionEnabled)
	}
	return []string{runStripLine(s, run, spin, runElapsed(run, m.now()), w)}
}

// runStripLine draws an open run's live strip as one line w wide: the run
// and where it stands, its step at work and that step's action, its money
// and time, and the key that jumps to its block. A narrow line gives up
// the recipe's entries first, then the money and the time; the run's name,
// the step's action and the key stay.
func runStripLine(s *theme.Styles, r threadfold.Run, spin, el string, w int) string {
	l := r.Live
	lead, name := s.Info.Render("●"), s.Info.Bold(true).Render(l.Name)
	if spin != "" {
		lead = s.Info.Render(spin)
	}
	var now string
	switch l.Mood {
	case threadfold.StripRunning:
		now = s.Base.Bold(true).Render(l.Who)
		if l.Who != "" {
			now += " "
		}
		now += s.Subtle.Render(l.Action)
	case threadfold.StripAsking:
		lead, name = s.Warning.Render("?"), s.Warning.Bold(true).Render(l.Name)
		now = s.Warning.Bold(true).Render(l.Who+" asks: ") + s.Base.Render(l.Question)
	case threadfold.StripDeciding:
		lead, name = s.Warning.Render("?"), s.Warning.Bold(true).Render(l.Name)
		now = s.Warning.Render(l.Line)
	default:
		lead, name = s.Faint.Render("‖"), s.Muted.Bold(true).Render(l.Name)
		now = s.Subtle.Render(l.Line)
	}
	var facts []string
	if l.Mood != threadfold.StripAsking && l.Mood != threadfold.StripDeciding {
		facts = append(facts, s.Muted.Render(domain.FormatDollars(r.Spent)))
		if el != "" && l.Mood == threadfold.StripRunning {
			facts = append(facts, s.Faint.Render(el))
		}
	}
	dot := s.Faint.Render(" · ")
	compose := func(withStrip, withFacts bool) string {
		left := lead + " " + name + "  "
		if withStrip {
			left += stripEntries(s, r.Strip, "", " › ") + s.Faint.Render("  · ")
		}
		left += now
		if withFacts && len(facts) > 0 {
			left += dot + strings.Join(facts, dot)
		}
		return left
	}
	hint := s.KeyHint.Render("alt+w") + s.Faint.Render(" jump")
	room := w - ansi.StringWidth(hint) - 2
	left := compose(l.Mood == threadfold.StripRunning && len(r.Strip) > 0, true)
	if ansi.StringWidth(left) > room {
		left = compose(false, true)
	}
	if ansi.StringWidth(left) > room {
		left = compose(false, false)
	}
	return spread(left, hint, w)
}

// stepRowLines is one step row, and its question beneath it while it
// asks. selected rings it. A step at work stands out from the rest: its
// glyph is the spinner's frame spin, its bar, name and line are lit.
func stepRowLines(s *theme.Styles, row threadfold.StepRow, selected bool, spin string, w int) []string {
	working := row.State == string(domain.StepRunning)
	lead := s.Faint.Render("│ ")
	if working {
		lead = s.Info.Render("┃ ")
	}
	if selected {
		lead = s.Cursor.Render("▸ ")
	}
	where := ""
	if row.Iter > 0 {
		where = fmt.Sprintf("↺ %d", row.Iter)
	}
	name, role := row.ID, row.Role
	line := row.Line
	if row.Folded && row.Pass > 0 {
		// a watch's pass: the event it ran for, how it ended, what ran in it
		role, line = "", row.Event+" · "+row.Line
		if row.Steps != "" {
			line += " — " + row.Steps
		}
	} else if row.Folded {
		role = ""
		line = row.Steps
		if row.Verdict != "" {
			line += " — " + row.Verdict
		}
	}
	cost := "–"
	if row.Spent > 0 {
		cost = domain.FormatDollars(row.Spent)
	}
	if !row.Started.IsZero() && !row.Ended.IsZero() && !row.Folded {
		cost = runDur(row.Ended.Sub(row.Started)) + " " + cost
	}
	glyph, nameStyle, lineStyle := stepGlyph(row.State), s.Base, s.Subtle
	if working {
		if spin != "" {
			glyph = spin
		}
		nameStyle, lineStyle = s.Info.Bold(true), s.Base
	}
	body := stepStateStyle(s, row.State).Render(glyph) + " " +
		s.Faint.Render(padTo(where, 4)) + " " +
		nameStyle.Render(padTo(name, 14)) + " " +
		roleStyle(s, domain.WorkflowRole(role)).Render(padTo(role, 12)) + " "
	if row.Waiting {
		body += s.Warning.Render("waits on you · ")
	}
	if row.Flag != "" {
		body += s.Warning.Render(row.Flag + " · ")
	}
	costW := ansi.StringWidth(cost) + 1
	room := max(w-ansi.StringWidth(lead)-ansi.StringWidth(body)-costW, 4)
	out := []string{lead + body + lineStyle.Render(padTo(ansi.Truncate(line, room, "…"), room)) + " " + s.Muted.Render(cost)}
	if q := row.Question; q != nil {
		bar := s.Faint.Render("│ ")
		qw := max(w-8, 8)
		for i, l := range strings.Split(wrapText(row.ID+" asks: "+q.Question, qw), "\n") {
			if i == 3 {
				break
			}
			out = append(out, ansi.Truncate(bar+"    "+s.Warning.Render(l), w, "…"))
		}
		for i, o := range q.Options {
			label := fmt.Sprintf("%d. %s", i+1, recommendedLabel(o))
			if o.Detail != "" {
				label += s.Faint.Render(" — " + o.Detail)
			}
			out = append(out, ansi.Truncate(bar+"      "+s.Base.Render(label), w, "…"))
		}
		out = append(out, ansi.Truncate(bar+"      "+s.Faint.Render("open the step (alt+w, enter) to answer"), w, "…"))
	}
	return out
}

// recommendedLabel is an option's label, saying it is the recommended one
// when it is and its own words do not already.
func recommendedLabel(o threadfold.QuestionOption) string {
	if o.Recommended && !strings.Contains(strings.ToLower(o.Label), "recommended") {
		return o.Label + " (recommended)"
	}
	return o.Label
}

// padTo pads str with spaces to n columns, cutting it when longer.
func padTo(str string, n int) string {
	if ansi.StringWidth(str) > n {
		return ansi.Truncate(str, n, "…")
	}
	return str + strings.Repeat(" ", n-ansi.StringWidth(str))
}

// stepGlyph is a step's state as one glyph.
func stepGlyph(state string) string {
	switch state {
	case "done":
		return "✓"
	case "failed":
		return "✕"
	case "stopped":
		return "–"
	case "asking":
		return "?"
	case "running":
		return "●"
	case string(domain.StepWatching):
		return "◷"
	case threadfold.RowPaused:
		return "‖"
	}
	return "○"
}

// stepMark is the mark a strip entry wears after its name.
func stepMark(state string) string {
	switch state {
	case "done", "failed", "asking":
		return stepGlyph(state)
	}
	return ""
}

// stepStateStyle colours a step's (or strip entry's) state.
func stepStateStyle(s *theme.Styles, state string) lipgloss.Style {
	switch state {
	case "done":
		return s.Success
	case "failed":
		return s.Error
	case "asking":
		return s.Warning
	case "running":
		return s.Info
	}
	return s.Faint
}

// runStateStyle colours the run's own state word.
func runStateStyle(s *theme.Styles, r threadfold.Run) lipgloss.Style {
	switch domain.RunState(r.State) {
	case domain.RunRunning:
		return s.Info
	case domain.RunProposed, domain.RunPaused:
		return s.Warning
	case domain.RunDone:
		return s.Success
	case domain.RunPartial:
		return s.Warning
	}
	return s.Faint
}

// roleStyle is the colour a step's role is named in.
func roleStyle(s *theme.Styles, role domain.WorkflowRole) lipgloss.Style {
	switch role {
	case domain.RoleContractor:
		return s.Info
	case domain.RoleImplementer:
		return s.Warning
	case domain.RoleReviewer:
		return s.Error
	case domain.RoleTester:
		return s.Success
	case domain.RolePlanner:
		return s.Title
	case domain.RoleScout:
		return s.Subtitle
	}
	return s.Muted
}

func verdictMark(v string) string {
	switch v {
	case "met":
		return "✓"
	case "unmet":
		return "✕"
	}
	return "–"
}

func verdictStyle(s *theme.Styles, v string) lipgloss.Style {
	switch v {
	case "met":
		return s.Success
	case "unmet":
		return s.Error
	}
	return s.Faint
}
