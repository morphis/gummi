package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// specView is the spec surface state: one feature's design doc, shown as
// line-addressed source (DESIGN §6.1).
//
// There is one view, not a read mode and an annotate mode. Read mode was
// a glamour render, and glamour re-wraps text — so a cursor on a
// rendered row could never say which source line it was on, and comments
// are addressed by source line. The two modes were therefore two
// different documents, and the key that toggled between them was one of
// five things tab meant. The source is now styled in place instead
// (mdsource.go): headings, code and emphasis read as themselves without
// a character moving, so one view is both readable and addressable.
type specView struct {
	f       domain.Feature
	path    string
	content string
	doc     spec.Doc
	cursor  int // 1-based source line
}

// specLoadedMsg delivers a (re)loaded spec document.
type specLoadedMsg struct {
	f       domain.Feature
	path    string
	content string
	err     error
}

// openSpec resolves the feature's spec file — the workspace copy under
// .gummi/specs|bugs once the feature has a worktree, the draft under
// .gummi/state/drafts/ before then (created from the template on first
// open).
func (m *Shell) openSpec(f domain.Feature) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		// Once the worktree exists the artifact lives at its workspace
		// home: ensure it was promoted there (idempotent) and read that
		// copy. Before then, it is a draft under state/drafts/.
		var path string
		if ok, err := m.wt.Exists(ctx, &f); err == nil && ok {
			if err := m.migrateDraft(&f); err != nil {
				return specLoadedMsg{err: err}
			}
			path = filepath.Join(m.wt.Root(), f.ArtifactPath())
		} else {
			path = filepath.Join(m.ws.DraftsDir(), spec.DraftFilename(&f))
			if err := spec.EnsureDraft(path, &f); err != nil {
				return specLoadedMsg{err: err}
			}
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return specLoadedMsg{err: err}
		}
		return specLoadedMsg{f: f, path: path, content: string(raw)}
	}
}

// reloadSpec re-reads the currently open spec from disk.
func (m *Shell) reloadSpec() tea.Cmd {
	sv := m.spec
	if sv == nil {
		return nil
	}
	return func() tea.Msg {
		raw, err := os.ReadFile(sv.path)
		if err != nil {
			return specLoadedMsg{err: err}
		}
		return specLoadedMsg{f: sv.f, path: sv.path, content: string(raw)}
	}
}

// addSpecComment writes an annotation into the doc and reloads it.
func (m *Shell) addSpecComment(line int, text string) tea.Cmd {
	sv := m.spec
	if sv == nil {
		return nil
	}
	reload := m.reloadSpec()
	path := sv.path
	return func() tea.Msg {
		date := m.now().Format("2006-01-02")
		// Serialize against the engine's annotate/answer-capture writers and
		// re-read the current file (not the load-time copy) under the lock, so
		// a concurrent marker isn't clobbered; write atomically.
		unlock := spec.LockFile(path)
		defer unlock()
		raw, err := os.ReadFile(path)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		out, err := spec.AddComment(string(raw), line, "user", date, text)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if err := atomicfile.Write(path, []byte(out), 0o600); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return reload()
	}
}

// resolveSpecComment writes a resolution for the thread at the given
// marker line and reloads the doc. Same writer discipline as
// addSpecComment: serialize under the per-file lock, re-read the current
// file, splice, and write atomically.
func (m *Shell) resolveSpecComment(line int) tea.Cmd {
	sv := m.spec
	if sv == nil {
		return nil
	}
	reload := m.reloadSpec()
	path := sv.path
	return func() tea.Msg {
		date := m.now().Format("2006-01-02")
		unlock := spec.LockFile(path)
		defer unlock()
		raw, err := os.ReadFile(path)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		out, err := spec.ResolveComment(string(raw), line, "user", date)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if err := atomicfile.Write(path, []byte(out), 0o600); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return reload()
	}
}

// approveSurface leaves the active spec/diff surface and runs the same
// gate the board's g does — advanceStage. Exactly one approve path, so
// the two surfaces can't disagree about what "approved" means.
func (m *Shell) approveSurface(f domain.Feature) tea.Cmd {
	m.spec, m.diff = nil, nil
	return m.advanceStage(f.ID)
}

// editSpec suspends the TUI and opens the doc in $EDITOR at the cursor
// line (best effort — plain `$EDITOR file` when unknown).
func (m *Shell) editSpec() tea.Cmd {
	sv := m.spec
	if sv == nil {
		return nil
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		return func() tea.Msg {
			return noticeMsg{text: "$EDITOR is not set", isErr: true}
		}
	}
	reload := m.reloadSpec()
	path := sv.path
	cmd := exec.CommandContext(context.Background(), editor, path) //nolint:gosec // $EDITOR is the user's own trusted setting
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return noticeMsg{text: fmt.Sprintf("editor: %v", err), isErr: true}
		}
		return reload()
	})
}

// threadAtCursor returns the thread whose markers include the cursor
// line, or nil when the cursor is not on a marker line — the target for
// the resolve key.
func (sv *specView) threadAtCursor() *spec.Thread {
	for _, t := range sv.doc.Threads() {
		for _, mk := range t.Markers {
			if mk.Line == sv.cursor {
				return &t
			}
		}
	}
	return nil
}

// bindings is the spec surface's key table (see keymap.go). One table,
// because there is one mode: every verb is live whenever the surface is.
func (sv *specView) bindings() []binding {
	// Bar order is shedding order: the status bar drops hints from the
	// second-to-last backwards and never the last, so the two rows that
	// answer "what do I do about this document" lead, and the way out
	// goes last where it outlives everything else (threadInputBindings
	// keeps esc last for the same reason). Reading and annotating are
	// what the surface is obviously for; approving it, sending it back
	// and leaving are the rows a reader would otherwise have to go
	// looking for.
	return []binding{
		{key: "j/k ↓↑", label: "line", help: "move the line cursor"},
		{key: "pgup/pgdn", label: "page", help: "move the line cursor by a page"},
		{key: "g", label: "approve", help: "cross the gate — the same g as the board", bar: true},
		{key: "R", label: "request changes", help: "send the open %% questions to the architect", bar: true},
		{key: "c", label: "comment", help: "comment on the cursor line", bar: true},
		{key: "x", label: "resolve", help: "resolve the %% thread at the cursor", bar: true},
		{key: "n/p", label: "markers", help: "jump between %% markers", bar: true},
		{key: "e", label: "editor", help: "open in $EDITOR at the cursor line"},
		{key: "?", label: "help", bar: true},
		{key: "esc", label: "back", help: "back to the board (also q)", bar: true},
	}
}

// handleSpecKey processes keys while the spec surface is open.
func (m *Shell) handleSpecKey(key string) tea.Cmd {
	sv := m.spec
	switch key {
	case "esc", "q":
		m.spec = nil
	case "e":
		return m.editSpec()
	case "R":
		// request changes: send the open %% questions to the architect
		return m.requestSpecChanges(sv)
	case "g":
		// cross the gate: leave the surface and run the board's g
		return m.approveSurface(sv.f)
	case "j", "down":
		sv.setCursor(sv.cursor + 1)
	case "k", "up":
		sv.setCursor(sv.cursor - 1)
	case "pgdown":
		sv.setCursor(sv.cursor + m.mainPage())
	case "pgup":
		sv.setCursor(sv.cursor - m.mainPage())
	case "n":
		sv.jumpMarker(1)
	case "p":
		sv.jumpMarker(-1)
	case "c":
		line := sv.cursor
		m.Overlay.Push(newCommentDialog(func(text string) tea.Cmd {
			return m.addSpecComment(line, text)
		}))
	case "x":
		t := sv.threadAtCursor()
		if t == nil {
			m.notice = noticeMsg{text: "no marker on this line"}
			return nil
		}
		if t.Resolved {
			m.notice = noticeMsg{text: "already resolved"}
			return nil
		}
		return m.resolveSpecComment(sv.cursor)
	}
	return nil
}

func (sv *specView) setCursor(n int) {
	sv.cursor = min(max(n, 1), len(sv.doc.Lines))
}

// jumpMarker moves the cursor to the next/previous marker line.
func (sv *specView) jumpMarker(dir int) {
	lines := sv.doc.MarkerLines()
	if len(lines) == 0 {
		return
	}
	if dir > 0 {
		for _, l := range lines {
			if l > sv.cursor {
				sv.cursor = l
				return
			}
		}
		sv.cursor = lines[0] // wrap
		return
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] < sv.cursor {
			sv.cursor = lines[i]
			return
		}
	}
	sv.cursor = lines[len(lines)-1] // wrap
}

// specViewRender renders the spec surface into the main pane: the
// status header (live dependencies and the open-thread checklist) over
// the scrolling source.
func (m *Shell) specViewRender(w, h int) string {
	sv := m.spec
	s := m.styles
	if sv == nil {
		return ""
	}
	var b strings.Builder
	// the same noun the card page used to send the reader here: this view
	// opens over a bug's report as often as over a feature's spec, and
	// calling both "spec" renamed the document between the line that
	// pointed at it and the header of the thing it opened.
	head := s.Title.Render(string(sv.f.ID)) + " " + s.Base.Render("· "+artifactNoun(sv.f.Kind))
	if open := len(sv.doc.OpenQuestions()); open > 0 {
		head += " " + s.Warning.Render(fmt.Sprintf("✎ %d open", open))
	}
	b.WriteString("\n" + head + "\n")
	b.WriteString(s.Separator.Render(strings.Repeat("─", max(min(w, 76), 0))) + "\n")

	// the status header used to belong to read mode alone, which meant
	// the mode you could actually act in was the one that never told you
	// what was blocking the gate. It is fixed above the body now, so it
	// is true of the surface rather than of a mode.
	b.WriteString(sv.renderStatus(m, w))

	// keys live in the status bar (keymap.go), so the body gets whatever
	// the header left of the pane (plus one line of slack).
	used := strings.Count(b.String(), "\n")
	b.WriteString(sv.renderSource(m, w, max(h-used-1, 3)))
	return b.String()
}

// renderStatus renders the fixed header: live dependency status, then
// the open threads split by who they wait on. An unresolved @user
// comment blocks the approval gate (DESIGN §6.1); agent-authored threads
// (questions, reviewer findings) are informational here and don't gate —
// the gate math counts only the user threads, so surfacing them under
// one "open questions" header misread the agent's threads as blockers.
func (sv *specView) renderStatus(m *Shell, w int) string {
	s := m.styles
	var b strings.Builder
	b.WriteString(sv.renderDependencyStatus(m, w))

	var blocking, informational []spec.Thread
	for _, t := range sv.doc.OpenQuestions() {
		if userMarker(t) != nil {
			blocking = append(blocking, t)
		} else {
			informational = append(informational, t)
		}
	}
	renderThreadGroup := func(label string, threads []spec.Thread) {
		if len(threads) == 0 {
			return
		}
		b.WriteString(s.Subtitle.Render(label) + "\n")
		for _, t := range threads {
			mk := t.Markers[0]
			if u := userMarker(t); u != nil {
				mk = *u
			}
			b.WriteString(s.Warning.Render("  ☐ ") + s.Subtle.Render(ansi.Truncate(mk.Text, max(w-8, 4), "…")) +
				s.Faint.Render("  L"+strconv.Itoa(mk.Line)) + "\n")
		}
		b.WriteString("\n")
	}
	renderThreadGroup("blocks approval (you)", blocking)
	renderThreadGroup("informational (agent)", informational)
	return b.String()
}

// renderDependencyStatus renders each direct dependency of the spec's
// feature with its live status — ID, current stage, and whether it is done
// or still pending — resolved from the dependency store at render, plus an
// all-done line when every dependency is Done. Returns empty when there is
// no store or no dependencies, so it composes cleanly onto the read view.
func (sv *specView) renderDependencyStatus(m *Shell, w int) string {
	if m.store == nil {
		return ""
	}
	ctx := context.Background()
	ids, err := m.store.ListDependencies(ctx, sv.f.ID)
	if err != nil || len(ids) == 0 {
		return ""
	}
	s := m.styles
	var b strings.Builder
	b.WriteString(s.Subtitle.Render("Dependencies") + "\n")
	allDone := true
	for _, id := range ids {
		dep, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return ""
		}
		done := dep.Stage == domain.StageDone
		if !done {
			allDone = false
		}
		mark := s.Warning.Render("◌")
		status := s.Warning.Render("pending")
		if done {
			mark = s.Success.Render("✔")
			status = s.Success.Render("done")
		}
		line := fmt.Sprintf("  %s %s @ %s · %s",
			mark, s.Base.Render(string(id)), s.Base.Render(string(dep.Stage)), status)
		b.WriteString(ansi.Truncate(line, w, "…") + "\n")
	}
	if allDone {
		b.WriteString(s.Success.Render("  all dependencies done") + "\n")
	}
	return b.String()
}

// renderSource renders the document: line numbers, gutter markers on
// annotated lines, tinted %% marker lines, in-place markdown styling for
// everything else (mdsource.go), and the line cursor.
func (sv *specView) renderSource(m *Shell, w, h int) string {
	s := m.styles
	anchored := map[int]bool{}
	for _, mk := range sv.doc.Markers {
		if mk.Anchor > 0 {
			anchored[mk.Anchor] = true
		}
	}
	resolvedLine := map[int]bool{}
	for _, t := range sv.doc.Threads() {
		for _, mk := range t.Markers {
			resolvedLine[mk.Line] = t.Resolved
		}
	}

	total := len(sv.doc.Lines)
	numW := len(strconv.Itoa(total))
	textW := max(w-numW-3, 4)

	// Lines wider than the pane wrap into continuation rows (line number
	// and gutter only on the first), so the window and cursor centering
	// work in display rows rather than source lines.
	type row struct {
		n       int    // 1-based source line
		content string // styled segment
		first   bool   // first row of its source line
	}
	var rows []row
	cursorRow := 0
	// one styler for the whole document: fenced-block state is carried
	// line to line, so a ``` block has to be walked in order.
	var md mdSource
	for i, raw := range sv.doc.Lines {
		n := i + 1
		var segs []string
		if spec.IsMarkerLine(raw) {
			// a %% thread is gummi's own annotation, not the document's
			// prose: it keeps its status color and its indent, and never
			// goes through the markdown styler.
			style := s.Warning
			if resolvedLine[n] {
				style = s.Success
			}
			segs = strings.Split(ansi.Wrap(strings.TrimSpace(raw), max(textW-2, 4), ""), "\n")
			for j := range segs {
				segs[j] = "  " + style.Render(segs[j])
			}
			// the styler still has to see the line, or a %% marker inside
			// a fenced block would be read as prose on the way past.
			md.line(s, raw)
		} else {
			segs = wrapStyled(md.line(s, raw), textW)
		}
		if n == sv.cursor {
			cursorRow = len(rows)
		}
		for j, seg := range segs {
			rows = append(rows, row{n: n, content: seg, first: j == 0})
		}
	}

	visible := max(h, 3)
	// the window is derived purely from the cursor (render must not
	// mutate state): keep the cursor centered where possible
	off := min(max(cursorRow-(visible-1)/2, 0), max(len(rows)-visible, 0))

	var b strings.Builder
	end := min(off+visible, len(rows))
	for i := off; i < end; i++ {
		r := rows[i]
		num := strings.Repeat(" ", numW)
		gutter := " "
		if r.first {
			num = fmt.Sprintf("%*d", numW, r.n)
			if anchored[r.n] {
				gutter = s.Warning.Render("▍")
			}
		}
		lineStr := s.Faint.Render(num) + gutter + " " + r.content
		if r.n == sv.cursor && r.first {
			lineStr = s.Selection.Render(num) + gutter + " " + r.content
			lineStr = s.Cursor.Render("▸") + lineStr
		} else {
			lineStr = " " + lineStr
		}
		b.WriteString(lineStr)
		if i < end-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}
