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
	"charm.land/glamour/v2"
	gstyles "charm.land/glamour/v2/styles"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// specView is the spec surface state: one feature's design doc, in
// glamour read mode or line-addressed annotate mode (DESIGN §6.1 —
// glamour re-wraps text, so stable line addressing needs the source).
type specView struct {
	f        domain.Feature
	path     string
	content  string
	doc      spec.Doc
	annotate bool
	cursor   int // 1-based source line (annotate mode)
	offset   int // scroll offset (both modes)
	// maxOffset is the largest useful read-mode offset, derived from the
	// wrapped render (which only the renderer knows) and cached each frame
	// so key handling can clamp to it instead of over-scrolling into a run
	// of dead keypresses at the bottom.
	maxOffset int
}

// specLoadedMsg delivers a (re)loaded spec document.
type specLoadedMsg struct {
	f       domain.Feature
	path    string
	content string
	err     error
}

// openSpec resolves the feature's spec file — the worktree copy once
// one exists, the draft under .gummi/state/drafts/ before then
// (created from the template on first open).
func (m *Shell) openSpec(f domain.Feature) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		// Once the worktree exists the spec travels with the feature
		// branch: ensure it is committed there (idempotent) and read
		// that copy. Before then, it is a draft under state/drafts/.
		var path string
		if ok, err := m.wt.Exists(ctx, &f); err == nil && ok {
			if err := m.migrateDraft(ctx, &f); err != nil {
				return specLoadedMsg{err: err}
			}
			path = filepath.Join(m.wt.Root(), f.WorktreePath(), f.ArtifactPath())
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
	cmd := exec.CommandContext(context.Background(), editor, sv.path) //nolint:gosec // $EDITOR is the user's own trusted setting
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return noticeMsg{text: fmt.Sprintf("editor: %v", err), isErr: true}
		}
		return reload()
	})
}

// bindings is the spec surface's key table (see keymap.go), split by
// mode like handleSpecKey routes.
func (sv *specView) bindings() []binding {
	if sv.annotate {
		return []binding{
			{key: "tab", label: "read", help: "switch to read mode", bar: true},
			{key: "j/k", label: "line", help: "move the line cursor"},
			{key: "pgup/pgdn", label: "page", help: "move the line cursor by a page"},
			{key: "c", label: "comment", help: "comment on the cursor line", bar: true},
			{key: "n/p", label: "markers", help: "jump between %% markers", bar: true},
			{key: "R", label: "request changes", help: "send the open %% questions to the architect", bar: true},
			{key: "e", label: "editor", help: "open in $EDITOR at the cursor line"},
			{key: "esc", label: "back", help: "back to the board (also q)", bar: true},
			{key: "?", label: "help", bar: true},
		}
	}
	return []binding{
		{key: "tab", label: "annotate", help: "switch to annotate mode", bar: true},
		{key: "j/k", label: "scroll", bar: true},
		{key: "pgup/pgdn", label: "page", help: "scroll by a page"},
		{key: "R", label: "request changes", help: "send the open %% questions to the architect"},
		{key: "e", label: "editor", help: "open in $EDITOR at the cursor line", bar: true},
		{key: "esc", label: "back", help: "back to the board (also q)", bar: true},
		{key: "?", label: "help", bar: true},
	}
}

// handleSpecKey processes keys while the spec surface is open.
func (m *Shell) handleSpecKey(key string) tea.Cmd {
	sv := m.spec
	switch key {
	case "esc", "q":
		m.spec = nil
		return nil
	case "tab":
		sv.annotate = !sv.annotate
		return nil
	case "?":
		m.Overlay.Push(m.helpOverlay())
		return nil
	case "e":
		return m.editSpec()
	case "R":
		// request changes: send the open %% questions to the architect
		return m.requestSpecChanges(sv)
	}
	if !sv.annotate {
		switch key {
		case "j", "down":
			// clamp to the last render's real maximum so scrolling stops at
			// the bottom instead of running into dead keypresses
			sv.offset = min(sv.offset+1, sv.scrollMax())
		case "k", "up":
			sv.offset = max(sv.offset-1, 0)
		case "pgdown":
			sv.offset = min(sv.offset+m.mainPage(), sv.scrollMax())
		case "pgup":
			sv.offset = max(sv.offset-m.mainPage(), 0)
		}
		return nil
	}
	switch key {
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
	}
	return nil
}

func (sv *specView) setCursor(n int) {
	sv.cursor = min(max(n, 1), len(sv.doc.Lines))
}

// scrollMax is the largest read-mode offset the last render allowed. Before
// the first render it falls back to a loose bound so scrolling still works.
func (sv *specView) scrollMax() int {
	if sv.maxOffset > 0 {
		return sv.maxOffset
	}
	return len(sv.doc.Lines)
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

// specViewRender renders the spec surface into the main pane.
func (m *Shell) specViewRender(w, h int) string {
	sv := m.spec
	s := m.styles
	if sv == nil {
		return ""
	}
	var b strings.Builder
	mode := "read"
	if sv.annotate {
		mode = "annotate"
	}
	open := len(sv.doc.OpenQuestions())
	head := s.Title.Render(string(sv.f.ID)) + " " + s.Base.Render("· spec") +
		"  " + s.Pill.Render(mode)
	if open > 0 {
		head += " " + s.Warning.Render(fmt.Sprintf("✎ %d open", open))
	}
	b.WriteString("\n" + head + "\n")
	b.WriteString(s.Separator.Render(strings.Repeat("─", max(min(w, 76), 0))) + "\n")

	// keys live in the status bar (keymap.go), so the body gets the pane
	// minus the three header lines (plus one line of slack).
	body := h - 4
	if sv.annotate {
		b.WriteString(sv.renderAnnotate(m, w, body))
	} else {
		b.WriteString(sv.renderRead(m, w, body))
	}
	return b.String()
}

// renderRead renders the glamour view plus the open-question checklist.
func (sv *specView) renderRead(m *Shell, w, h int) string {
	s := m.styles
	var b strings.Builder

	if open := sv.doc.OpenQuestions(); len(open) > 0 {
		b.WriteString(s.Subtitle.Render("open questions") + "\n")
		for _, t := range open {
			q := t.Markers[0].Text
			b.WriteString(s.Warning.Render("  ☐ ") + s.Subtle.Render(ansi.Truncate(q, max(w-8, 4), "…")) +
				s.Faint.Render("  L"+strconv.Itoa(t.Markers[0].Line)) + "\n")
		}
		b.WriteString("\n")
	}

	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(gstyles.DarkStyle),
		glamour.WithWordWrap(min(w, 100)),
	)
	if err != nil {
		return b.String() + s.Error.Render(err.Error())
	}
	out, err := r.Render(sv.content)
	if err != nil {
		return b.String() + s.Error.Render(err.Error())
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	used := strings.Count(b.String(), "\n")
	visible := max(h-used, 3)
	// clamp locally — render must not mutate the stored offset — but cache
	// the real maximum so key handling can bound scrolling to it.
	maxOff := max(len(lines)-visible, 0)
	sv.maxOffset = maxOff
	off := min(sv.offset, maxOff)
	end := min(off+visible, len(lines))
	b.WriteString(strings.Join(lines[off:end], "\n"))
	return b.String()
}

// renderAnnotate renders the source view: line numbers, gutter markers
// on annotated lines, tinted marker lines, and the line cursor.
func (sv *specView) renderAnnotate(m *Shell, w, h int) string {
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
	for i, raw := range sv.doc.Lines {
		n := i + 1
		var segs []string
		style := s.Base
		if spec.IsMarkerLine(raw) {
			style = s.Warning
			if resolvedLine[n] {
				style = s.Success
			}
			segs = strings.Split(ansi.Wrap(strings.TrimSpace(raw), max(textW-2, 4), ""), "\n")
			for j := range segs {
				segs[j] = "  " + segs[j]
			}
		} else {
			segs = strings.Split(ansi.Wrap(raw, textW, ""), "\n")
		}
		if n == sv.cursor {
			cursorRow = len(rows)
		}
		for j, seg := range segs {
			rows = append(rows, row{n: n, content: style.Render(seg), first: j == 0})
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
