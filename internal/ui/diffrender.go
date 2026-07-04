package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/workflow"
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

	body := h - 5
	if dv.annotate {
		b.WriteString(dv.renderAnnotate(m, w, body))
		b.WriteString("\n" + s.KeyHint.Render("c") + s.KeyLabel.Render(" comment") +
			s.Faint.Render(" · ") + s.KeyHint.Render("x") + s.KeyLabel.Render(" resolve") +
			s.Faint.Render(" · ") + s.KeyHint.Render("n/p") + s.KeyLabel.Render(" annotations") +
			s.Faint.Render(" · ") + s.KeyHint.Render("R") + s.KeyLabel.Render(" request changes") +
			s.Faint.Render(" · ") + s.KeyHint.Render("esc") + s.KeyLabel.Render(" back"))
	} else {
		b.WriteString(dv.renderRead(m, w, body))
		b.WriteString("\n" + s.KeyHint.Render("tab") + s.KeyLabel.Render(" annotate") +
			s.Faint.Render(" · ") + s.KeyHint.Render("j/k") + s.KeyLabel.Render(" scroll") +
			s.Faint.Render(" · ") + s.KeyHint.Render("R") + s.KeyLabel.Render(" request changes") +
			s.Faint.Render(" · ") + s.KeyHint.Render("esc") + s.KeyLabel.Render(" back"))
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
	off := min(dv.offset, max(len(dv.lines)-visible, 0))
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
	total := len(dv.lines)
	visible := max(h, 3)
	off := min(max(dv.cursor-1-(visible-1)/2, 0), max(total-visible, 0))
	numW := len(strconv.Itoa(total))
	var b strings.Builder
	end := min(off+visible, total)
	for i := off; i < end; i++ {
		n := i + 1
		num := fmt.Sprintf("%*d", numW, n)
		gutter := " "
		if idxs, ok := dv.located[i]; ok && len(idxs) > 0 {
			gutter = s.Warning.Render("▍")
		}
		content := diffLineStyle(m, diffCell(dv.lines[i], w-numW-3))
		var lineStr string
		if n == dv.cursor {
			lineStr = s.KeyHint.Render("▸") + s.Selection.Render(num) + gutter + " " + content
		} else {
			lineStr = " " + s.Faint.Render(num) + gutter + " " + content
		}
		b.WriteString(lineStr + "\n")
		// annotation block(s) under the line
		for _, ai := range dv.located[i] {
			b.WriteString(dv.annBlock(m, dv.anns[ai], numW) + "\n")
		}
	}
	// orphaned annotations degrade to a footer, grouped by file
	if len(dv.orphans) > 0 {
		b.WriteString("\n" + s.Faint.Render("orphaned (line changed since comment):") + "\n")
		for _, oi := range dv.orphans {
			a := dv.anns[oi]
			b.WriteString(dv.annBlock(m, a, numW) + s.Faint.Render(" — "+sanitize(a.File)) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
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

// requestDiffChanges bounces the feature to implement and re-runs it; the
// engine folds the open diff annotations into the implement stage's hints
// (see newAgentSession), so the implementer addresses each comment. Blocks
// with a notice when there is nothing open to send.
func (m *Shell) requestDiffChanges(dv *diffView) tea.Cmd {
	if m.engine == nil {
		m.notice = noticeMsg{text: "no agent configured", isErr: true}
		return nil
	}
	if dv.openCount() == 0 {
		m.notice = noticeMsg{text: "no open diff comments to send"}
		return nil
	}
	// "request changes" bounces to implement; only offer it from a stage
	// where that edge is legal (review/verify/plan), so it never tears
	// down a running session for a transition that will just be rejected.
	if err := workflow.CanTransition(dv.f.Stage, domain.StageImplement, dv.f.Skip); err != nil {
		m.notice = noticeMsg{text: "request changes works from the review or verify gate", isErr: true}
		return nil
	}
	f := dv.f
	n := dv.openCount()
	m.diff = nil // close the surface; the fix runs on the board
	return func() tea.Msg {
		ctx := context.Background()
		// transition first (it validates the edge); only then drop the
		// stale session, so a rejected bounce is never destructive.
		nf, err := m.store.Transition(ctx, f.ID, domain.StageImplement, "user")
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
