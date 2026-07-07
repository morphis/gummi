package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/workflow"
)

// diffCell sanitizes an untrusted diff line (it is agent/repo-authored
// code, so OSC-52 and other control sequences are stripped, DESIGN
// threat list) and truncates it to width w.
func diffCell(line string, w int) string {
	return ansi.Truncate(sanitize(line), max(w, 8), "…")
}

// diffViewRender draws the diff surface into the main pane.
func (m *Shell) diffViewRender(w, h int) string {
	dv := m.diff
	s := m.styles
	if dv == nil {
		return ""
	}
	var b strings.Builder
	mode := "read"
	if dv.annotate {
		mode = "annotate"
	}
	head := s.Title.Render(string(dv.f.ID)) + " " + s.Base.Render("· diff") + "  " + s.Pill.Render(mode)
	if open := dv.openCount(); open > 0 {
		head += " " + s.Warning.Render(fmt.Sprintf("✎ %d open", open))
	}
	if len(dv.orphans) > 0 {
		head += " " + s.Faint.Render(fmt.Sprintf("(%d orphaned)", len(dv.orphans)))
	}
	b.WriteString("\n" + head + "\n")
	b.WriteString(s.Separator.Render(strings.Repeat("─", max(min(w, 76), 0))) + "\n")

	// keys live in the status bar (keymap.go), so the body gets the pane
	// minus the three header lines (plus one line of slack).
	body := h - 4
	if dv.annotate {
		b.WriteString(dv.renderAnnotate(m, w, body))
	} else {
		b.WriteString(dv.renderRead(m, w, body))
	}
	return b.String()
}

// diffLineStyle colors a unified-diff line by its role. The line must be
// already sanitized (see diffCell).
func diffLineStyle(m *Shell, line string) string {
	s := m.styles
	switch {
	case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
		return s.Subtitle.Render(line)
	case strings.HasPrefix(line, "diff --git") || strings.HasPrefix(line, "index "):
		return s.Faint.Render(line)
	case strings.HasPrefix(line, "@@"):
		return s.Info.Render(line)
	case strings.HasPrefix(line, "+"):
		return s.Success.Render(line)
	case strings.HasPrefix(line, "-"):
		return s.Error.Render(line)
	default:
		return s.Base.Render(line)
	}
}

// renderRead shows the colorized unified diff scrolled to the offset.
func (dv *diffView) renderRead(m *Shell, w, h int) string {
	var b strings.Builder
	visible := max(h, 3)
	dv.maxOffset = max(len(dv.lines)-visible, 0)
	off := min(dv.offset, dv.maxOffset)
	end := min(off+visible, len(dv.lines))
	for i := off; i < end; i++ {
		line := diffCell(dv.lines[i], w-1)
		b.WriteString(diffLineStyle(m, line))
		if i < end-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderAnnotate shows the source diff with line numbers, gutter markers
// on annotated lines, the annotation blocks, and the line cursor.
func (dv *diffView) renderAnnotate(m *Shell, w, h int) string {
	s := m.styles
	numW := len(strconv.Itoa(len(dv.lines)))
	// Build every display line — each source line plus the annotation
	// block(s) beneath it — and note where the cursor line lands. The
	// window is then taken over these *rendered* lines, so the interleaved
	// annotation blocks are counted in the height budget; the old math
	// counted source lines only and let annotations push the cursor (and
	// the bottom rows) off the clipped pane.
	var rendered []string
	cursorIdx := 0
	push := func(str string) { rendered = append(rendered, strings.Split(str, "\n")...) }
	for i := range dv.lines {
		n := i + 1
		num := fmt.Sprintf("%*d", numW, n)
		gutter := " "
		if idxs, ok := dv.located[i]; ok && len(idxs) > 0 {
			gutter = s.Warning.Render("▍")
		}
		content := diffLineStyle(m, diffCell(dv.lines[i], w-numW-3))
		if n == dv.cursor {
			cursorIdx = len(rendered)
			push(s.Cursor.Render("▸") + s.Selection.Render(num) + gutter + " " + content)
		} else {
			push(" " + s.Faint.Render(num) + gutter + " " + content)
		}
		for _, ai := range dv.located[i] {
			push(dv.annBlock(m, dv.anns[ai], numW))
		}
	}
	// orphaned annotations degrade to a footer, grouped by file
	var footer []string
	if len(dv.orphans) > 0 {
		footer = append(footer, "", s.Faint.Render("orphaned (line changed since comment):"))
		for _, oi := range dv.orphans {
			a := dv.anns[oi]
			footer = append(footer, strings.Split(dv.annBlock(m, a, numW)+s.Faint.Render(" — "+sanitize(a.File)), "\n")...)
		}
	}
	// Window the main content to keep the cursor visible, then append as
	// much of the orphan footer as still fits.
	win := append([]string(nil), windowLines(rendered, cursorIdx, h)...)
	if room := h - len(win); room > 0 && len(footer) > 0 {
		win = append(win, footer[:min(room, len(footer))]...)
	}
	return strings.Join(win, "\n")
}

// annBlock renders one annotation as an indented, tinted comment.
func (dv *diffView) annBlock(m *Shell, a domain.DiffAnnotation, numW int) string {
	s := m.styles
	pad := strings.Repeat(" ", numW+3)
	mark := s.Warning.Render("✎")
	body := sanitize(a.Comment)
	if a.Resolved {
		mark = s.Success.Render("✓")
		body = s.Faint.Render(sanitize(a.Comment))
	} else {
		body = s.Subtle.Render(body)
	}
	return pad + mark + " " + body
}

// requestDiffChanges sends the open diff annotations to the implementer
// (DESIGN §6.1). From review/verify it bounces the feature to the work
// stage and re-runs it; already at the work stage (the implement/fix
// gate) there is no edge to take, so the stage is re-run in place — the
// engine folds the open annotations into every implement/fix run's
// hints (see newAgentSession), so either way the implementer addresses
// each comment. Blocks with a notice when there is nothing open to send.
func (m *Shell) requestDiffChanges(dv *diffView) tea.Cmd {
	if m.engine == nil {
		m.notice = noticeMsg{text: "no agent configured", isErr: true}
		return nil
	}
	if dv.openCount() == 0 {
		m.notice = noticeMsg{text: "no open diff comments to send"}
		return nil
	}
	// "request changes" targets the work stage (implement/fix); only
	// offer it there or from a stage with a legal edge to it
	// (review/verify), so it never tears down a running session for a
	// transition that will just be rejected.
	workStage := workflow.WorkStage(dv.f.Kind)
	atWork := dv.f.Stage == workStage
	if !atWork {
		if err := workflow.CanTransition(dv.f.Kind, dv.f.Stage, workStage, dv.f.Skip); err != nil {
			m.notice = noticeMsg{text: "request changes works from the implement, review, or verify gate", isErr: true}
			return nil
		}
	}
	f := dv.f
	n := dv.openCount()
	turn := compileDiffComments(dv.anns)
	m.diff = nil // close the surface; the fix runs on the board
	return func() tea.Msg {
		ctx := context.Background()
		if atWork {
			// no transition: deliver to a running session as a live turn,
			// or re-run the stage (a fresh run reads the open annotations
			// from the store).
			if s := m.engine.Get(f.ID); s != nil {
				switch s.State() {
				case engine.StateRunning:
					if err := m.engine.Send(ctx, f.ID, turn); err != nil {
						return noticeMsg{text: sanitize(err.Error()), isErr: true}
					}
					return noticeMsg{text: fmt.Sprintf("%s: sent %d diff comment(s) to the running %s agent", f.ID, n, f.Stage)}
				case engine.StateQueued:
					return noticeMsg{text: fmt.Sprintf("%s: %s is queued — it will read the open diff comments when it starts", f.ID, f.Stage)}
				}
			}
			m.dropSession(f.ID)
			if err := m.engine.Run(f); err != nil {
				return noticeMsg{text: err.Error(), isErr: true}
			}
			return noticeMsg{text: fmt.Sprintf("%s: re-running %s with %d diff comment(s)", f.ID, f.Stage, n)}
		}
		// transition first (it validates the edge); only then drop the
		// stale session, so a rejected bounce is never destructive.
		nf, err := m.store.Transition(ctx, f.ID, workStage, "user")
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		m.dropSession(f.ID)
		if err := m.engine.Run(nf); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s: sent %d diff comment(s) to the implementer", f.ID, n)}
	}
}

// compileDiffComments builds a fix-up turn from the open diff
// annotations — the same shape the engine folds into a fresh
// implement/fix run's hints (diffReviewHints), for delivery to a
// session that is already running.
func compileDiffComments(anns []domain.DiffAnnotation) string {
	var lines []string
	for _, a := range anns {
		if a.Resolved {
			continue
		}
		loc := a.File
		if a.Excerpt != "" {
			loc += " — " + a.Excerpt
		}
		lines = append(lines, fmt.Sprintf("- %s: %s", loc, a.Comment))
	}
	if len(lines) == 0 {
		return ""
	}
	return "Address these diff review comments; make the edits and keep " +
		"the change minimal:\n" + strings.Join(lines, "\n")
}
