package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
	head := s.Title.Render(string(dv.f.ID)) + " " + s.Base.Render("· diff")
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
	b.WriteString(dv.render(m, w, h-4))
	return b.String()
}

// diffLineStyle colors a unified-diff line by its role. The line must be
// already sanitized (see diffCell).
func diffLineStyle(m *Shell, line string) string {
	return diffStyleFor(m, line).Render(line)
}

// diffStyleFor picks the style for a unified-diff line by its role, so
// wrapped continuation rows (which lose the +/- prefix) keep the color
// of the line they belong to.
func diffStyleFor(m *Shell, line string) lipgloss.Style {
	s := m.styles
	switch {
	case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
		return s.Subtitle
	case strings.HasPrefix(line, "diff --git") || strings.HasPrefix(line, "index "):
		return s.Faint
	case strings.HasPrefix(line, "@@"):
		return s.Info
	case strings.HasPrefix(line, "+"):
		return s.Success
	case strings.HasPrefix(line, "-"):
		return s.Error
	default:
		return s.Base
	}
}

// render draws the diff: line numbers, gutter markers on annotated
// lines, the annotation blocks interleaved under the lines they anchor
// to, the orphan footer, and the line cursor.
func (dv *diffView) render(m *Shell, w, h int) string {
	s := m.styles
	numW := len(strconv.Itoa(len(dv.lines)))
	textW := max(w-numW-3, 4)
	// Build every display row — each source line (wrapped into
	// continuation rows when wider than the pane, number and gutter only
	// on the first) plus the annotation block(s) beneath it — and note
	// where the cursor line lands. The window is then taken over these
	// *rendered* rows, so wrapping and interleaved annotation blocks are
	// counted in the height budget and can't push the cursor off-screen.
	var rendered []string
	cursorIdx := 0
	push := func(str string) { rendered = append(rendered, strings.Split(str, "\n")...) }
	for i := range dv.lines {
		n := i + 1
		gutter := " "
		if idxs, ok := dv.located[i]; ok && len(idxs) > 0 {
			gutter = s.Warning.Render("▍")
		}
		raw := sanitize(dv.lines[i])
		style := diffStyleFor(m, raw)
		segs := strings.Split(ansi.Wrap(raw, textW, ""), "\n")
		if n == dv.cursor {
			cursorIdx = len(rendered)
		}
		for j, seg := range segs {
			content := style.Render(seg)
			switch {
			case j == 0 && n == dv.cursor:
				push(s.Cursor.Render("▸") + s.Selection.Render(fmt.Sprintf("%*d", numW, n)) + gutter + " " + content)
			case j == 0:
				push(" " + s.Faint.Render(fmt.Sprintf("%*d", numW, n)) + gutter + " " + content)
			default:
				push(" " + strings.Repeat(" ", numW) + " " + " " + content)
			}
		}
		for _, ai := range dv.located[i] {
			push(dv.annBlock(m, dv.anns[ai], numW+3, w))
		}
	}
	// Orphaned annotations degrade to a footer. Its entries stay
	// cursor-addressable (positions past the last diff line, see
	// annAtCursor) so x/D can still resolve or delete a comment whose
	// line changed.
	if len(dv.orphans) > 0 {
		push("")
		push(s.Faint.Render("orphaned (line changed since comment):"))
		for k, oi := range dv.orphans {
			rows := dv.orphanRows(m, dv.anns[oi], numW+3, w)
			if dv.orphanRowPos(k) == dv.cursor {
				cursorIdx = len(rendered)
				rows[0] = s.Cursor.Render("▸") + rows[0][1:]
			}
			push(strings.Join(rows, "\n"))
		}
	}
	return strings.Join(windowLines(rendered, cursorIdx, h), "\n")
}

// orphanRows renders one orphaned annotation: its comment block plus a
// faint file row beneath (the anchor no longer names a diff line, so the
// file is the only location left).
func (dv *diffView) orphanRows(m *Shell, a domain.DiffAnnotation, pad, w int) []string {
	rows := strings.Split(dv.annBlock(m, a, pad, w), "\n")
	file := ansi.Truncate("— "+sanitize(a.File), max(w-pad-2, 8), "…")
	return append(rows, strings.Repeat(" ", pad+2)+m.styles.Faint.Render(file))
}

// orphanRowPos is the cursor position addressing the k-th orphan: the
// positions directly past the last diff line (see setCursor/annAtCursor).
func (dv *diffView) orphanRowPos(k int) int {
	return len(dv.lines) + 1 + k
}

// annBlock renders one annotation as an indented, tinted comment,
// wrapped to the pane (continuation rows align under the first).
func (dv *diffView) annBlock(m *Shell, a domain.DiffAnnotation, pad, w int) string {
	s := m.styles
	prefix := strings.Repeat(" ", pad)
	mark := s.Warning.Render("✎")
	style := s.Subtle
	if a.Resolved {
		mark = s.Success.Render("✓")
		style = s.Faint
	}
	segs := strings.Split(ansi.Wrap(sanitize(a.Comment), max(w-pad-2, 4), ""), "\n")
	rows := make([]string, 0, len(segs))
	for j, seg := range segs {
		lead := prefix + "  "
		if j == 0 {
			lead = prefix + mark + " "
		}
		rows = append(rows, lead+style.Render(seg))
	}
	return strings.Join(rows, "\n")
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
	turn := engine.CompileDiffComments(dv.anns, m.engine.ClientTools())
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
					return noticeMsg{text: fmt.Sprintf("%s: sent %d diff comment(s) to the running %s agent", f.ID, n, f.Stage), reload: true}
				case engine.StateQueued:
					return noticeMsg{text: fmt.Sprintf("%s: %s is queued — it will read the open diff comments when it starts", f.ID, f.Stage)}
				}
			}
			m.dropSession(f.ID)
			if err := m.engine.Run(f); err != nil {
				return noticeMsg{text: err.Error(), isErr: true}
			}
			return noticeMsg{text: fmt.Sprintf("%s: re-running %s with %d diff comment(s)", f.ID, f.Stage, n), reload: true}
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
		return noticeMsg{text: fmt.Sprintf("%s: sent %d diff comment(s) to the implementer", f.ID, n), reload: true}
	}
}
