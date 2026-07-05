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

	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/spec"
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
	return func() tea.Msg {
		date := m.now().Format("2006-01-02")
		out, err := spec.AddComment(sv.content, line, "user", date, text)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if err := os.WriteFile(sv.path, []byte(out), 0o600); err != nil {
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
	case "e":
		return m.editSpec()
	case "R":
		// request changes: send the open %% questions to the architect
		return m.requestSpecChanges(sv)
	}
	if !sv.annotate {
		switch key {
		case "j", "down":
			// loose upper bound (render clamps precisely per width);
			// ×2 leaves room for glamour's wrapping to add lines
			sv.offset = min(sv.offset+1, len(sv.doc.Lines)*2)
		case "k", "up":
			sv.offset = max(sv.offset-1, 0)
		}
		return nil
	}
	switch key {
	case "j", "down":
		sv.setCursor(sv.cursor + 1)
	case "k", "up":
		sv.setCursor(sv.cursor - 1)
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

	body := h - 5
	if sv.annotate {
		b.WriteString(sv.renderAnnotate(m, w, body))
		b.WriteString("\n" + s.KeyHint.Render("c") + s.KeyLabel.Render(" comment") +
			s.Faint.Render(" · ") + s.KeyHint.Render("n/p") + s.KeyLabel.Render(" markers") +
			s.Faint.Render(" · ") + s.KeyHint.Render("R") + s.KeyLabel.Render(" request changes") +
			s.Faint.Render(" · ") + s.KeyHint.Render("e") + s.KeyLabel.Render(" editor") +
			s.Faint.Render(" · ") + s.KeyHint.Render("esc") + s.KeyLabel.Render(" back"))
	} else {
		b.WriteString(sv.renderRead(m, w, body))
		b.WriteString("\n" + s.KeyHint.Render("tab") + s.KeyLabel.Render(" annotate") +
			s.Faint.Render(" · ") + s.KeyHint.Render("j/k") + s.KeyLabel.Render(" scroll") +
			s.Faint.Render(" · ") + s.KeyHint.Render("e") + s.KeyLabel.Render(" editor") +
			s.Faint.Render(" · ") + s.KeyHint.Render("esc") + s.KeyLabel.Render(" back"))
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
	// clamp locally — render must not mutate the stored offset
	off := min(sv.offset, max(len(lines)-visible, 0))
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
	visible := max(h, 3)
	// the window is derived purely from the cursor (render must not
	// mutate state): keep the cursor centered where possible
	off := min(max(sv.cursor-1-(visible-1)/2, 0), max(total-visible, 0))

	numW := len(strconv.Itoa(total))
	var b strings.Builder
	end := min(off+visible, total)
	for i := off; i < end; i++ {
		n := i + 1
		raw := sv.doc.Lines[i]
		num := fmt.Sprintf("%*d", numW, n)
		gutter := " "
		if anchored[n] {
			gutter = s.Warning.Render("▍")
		}
		var content string
		switch {
		case spec.IsMarkerLine(raw):
			style := s.Warning
			if resolvedLine[n] {
				style = s.Success
			}
			content = style.Render(ansi.Truncate("  "+strings.TrimSpace(raw), max(w-numW-3, 4), "…"))
		default:
			content = s.Base.Render(ansi.Truncate(raw, max(w-numW-3, 4), "…"))
		}
		lineStr := s.Faint.Render(num) + gutter + " " + content
		if n == sv.cursor {
			lineStr = s.Selection.Render(ansi.Strip(num)) + gutter + " " + content
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
