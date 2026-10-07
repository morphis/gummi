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
)

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
	// Every open comment sitting on a line that has since changed is what
	// a finished rework round looks like when the agent edited the code
	// and never called resolve_annotation: the work is done, the comment
	// still holds the gate shut, and the gate's own advice — request
	// changes — sends the identical round again. Nothing said so, so the
	// round could repeat indefinitely. Said here, at the surface holding
	// both keys, because this is where the reader is when they choose.
	if n := dv.openCount(); n > 0 && dv.openOrphanCount() == n {
		b.WriteString(s.Warning.Render("  every open comment's line has already changed — x resolves one that the change addressed; R sends them back again") + "\n")
	}
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
		content := style.Render(seg)
		if j == 0 {
			lead = prefix + mark + " "
			if a.SourceRef != "" {
				// a non-empty SourceRef is the GitHub thread id pr.AnnotationFor
				// stamps on an ingested comment — tag it so it reads as
				// "someone on GitHub said this", not "the reviewer agent did".
				content = s.Faint.Render("PR") + " " + content
			}
		}
		rows = append(rows, lead+content)
	}
	return strings.Join(rows, "\n")
}

// requestDiffChanges sends the open diff annotations to the implementer
// (DESIGN §6.1). Already at the work stage (the implement gate) there is
// no edge to take, so the stage takes them in place — the engine folds
// the open annotations into every implement run's hints (see
// newAgentSession). From verify, or with a spec comment open that an
// earlier stage owns, the card goes back to the stage that owns them
// (engine.RouteComments), asked first. Blocks with a notice when there is
// nothing open to send.
func (m *Shell) requestDiffChanges(dv *diffView) tea.Cmd {
	cmd, refused, ask := m.diffChanges(dv.f, dv.anns)
	if ask != nil {
		// the surface closes on the yes; the fix runs on the board
		return m.confirmChanges(ask, func() { m.diff = nil })
	}
	if cmd == nil {
		m.notice = refused
		return nil
	}
	m.diff = nil // close the surface; the fix runs on the board
	return cmd
}

// diffChanges is requestDiffChanges for any face: the command that sends
// anns' open comments on; or the question to ask before a send-back that
// moves the card; or — both nil — the notice saying why nothing can be
// sent.
func (m *Shell) diffChanges(f domain.Feature, anns []domain.DiffAnnotation) (tea.Cmd, noticeMsg, *changesAsk) {
	if m.engine == nil {
		return nil, noticeMsg{text: m.noAgent(""), isErr: true}, nil
	}
	n := 0
	for _, a := range anns {
		if !a.Resolved {
			n++
		}
	}
	if n == 0 {
		return nil, noticeMsg{text: "no open diff comments to send"}, nil
	}
	// A freeform card has exactly one session and it is always the writer,
	// so all of the routing below collapses: there is no stage to
	// transition to, no critique pass that must not receive the comments,
	// and no kickoff to append them to. They go to that session as its
	// next turn. If its backend idled out, Send respawns one carrying the
	// transcript; if the card page was never opened this session,
	// OpenFreeform is what opens it — idempotent per card, so asking twice
	// costs nothing.
	if f.IsFreeform() {
		turn := engine.CompileDiffComments(anns, m.engine.ClientTools())
		return func() tea.Msg {
			ctx := context.Background()
			ff, err := m.engine.OpenFreeform(ctx, f)
			if err != nil {
				return noticeMsg{text: sanitize(err.Error()), isErr: true}
			}
			if err := ff.Send(ctx, turn); err != nil {
				return noticeMsg{text: sanitize(err.Error()), isErr: true}
			}
			return noticeMsg{text: fmt.Sprintf("%s: sent %d diff comment%s to its session", f.ID, n, plural(n)), reload: true}
		}, noticeMsg{}, nil
	}
	// the diff's comments route with the artifact's: a design note left
	// beside them sends the card to plan, not to the implementer, who
	// would otherwise be handed a comment it has no business answering
	route := engine.RouteComments(domain.CardTypeOf(&f), f.Stage, m.artifactDoc(f), n)
	if route.Rewinds() {
		return nil, noticeMsg{}, m.commentRewind(f, route)
	}
	// in place is only the work stage's: before it there is no code to
	// have commented on as the implementer's work, and past verify there
	// is no edge back
	if f.Stage != domain.StageImplement {
		return nil, noticeMsg{text: "request changes works from the implement or verify gate", isErr: true}, nil
	}
	// The notices below used to hard-code "comment(s)" and let a single
	// open comment read "sent 1 diff comment(s) to the implementer"
	// verbatim. plural(n) (reviewloop.go) picks the right suffix instead
	// of punting the choice onto the reader.
	turn := engine.CompileDiffComments(anns, m.engine.ClientTools())
	return func() tea.Msg {
		ctx := context.Background()
		// deliver to a running session as a live turn, or re-run the
		// stage (a fresh run reads the open annotations from the store).
		if s := m.engine.Get(f.ID); s != nil {
			switch s.State() {
			case engine.StateRunning:
				// implement runs carry the open diff comments in their
				// hints, so a held one reaches the next writer run
				if held := heldForWriter(s.Snapshot(), f, n, "diff comment"); held != "" {
					return noticeMsg{text: held}
				}
				if err := m.engine.Send(ctx, f.ID, turn); err != nil {
					return noticeMsg{text: sanitize(err.Error()), isErr: true}
				}
				return noticeMsg{text: fmt.Sprintf("%s: sent %d diff comment%s to the running %s agent", f.ID, n, plural(n), f.Stage), reload: true}
			}
		}
		m.dropSession(f.ID)
		if err := m.engine.Run(f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s: re-running %s with %d diff comment%s", f.ID, f.Stage, n, plural(n)), reload: true}
	}, noticeMsg{}, nil
}
