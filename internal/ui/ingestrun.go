package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/engine"
)

// ingestRunView is the live-progress surface for an in-flight ingest
// pass (DESIGN §11 phase A). The pass is transient — no board Session to
// snapshot — so the engine streams its steps here: the architect's tool
// calls and its running commentary, watch-only. It takes the main pane
// while nothing else is focused; esc backgrounds it (I brings it back),
// and the review surface replaces it when the decomposition lands.
type ingestRunView struct {
	source   string
	activity []ingestRunLine // milestones and tool calls, oldest first
	tail     string          // the architect's streamed commentary
	tailDone bool            // tail is a completed message; next delta starts fresh
	hidden   bool            // backgrounded with esc
}

// ingestRunLine is one discrete feed entry: a gummi milestone (note) or
// an architect tool call.
type ingestRunLine struct {
	note bool
	text string
}

// ingestRunKeep bounds the retained feed; only the tail of it renders.
const ingestRunKeep = 64

func newIngestRunView(source string) *ingestRunView {
	return &ingestRunView{source: source}
}

// apply folds one engine step into the feed.
func (rv *ingestRunView) apply(st engine.IngestStep) {
	switch st.Kind {
	case engine.IngestStepNote, engine.IngestStepTool:
		rv.activity = append(rv.activity, ingestRunLine{note: st.Kind == engine.IngestStepNote, text: st.Text})
		if len(rv.activity) > ingestRunKeep {
			rv.activity = rv.activity[len(rv.activity)-ingestRunKeep:]
		}
	case engine.IngestStepDelta:
		if rv.tailDone {
			rv.tail, rv.tailDone = "", false
		}
		rv.tail += st.Text
	case engine.IngestStepMessage:
		// the completed message is the authoritative text of what the
		// deltas were streaming — replace, don't double.
		rv.tail, rv.tailDone = st.Text, true
	}
}

// ingestRunRender paints the live feed into the main pane.
func (m *Shell) ingestRunRender(w, h int) string {
	rv := m.ingestRun
	s := m.styles
	if rv == nil {
		return ""
	}
	var b strings.Builder
	head := s.Title.Render("ingest") + " " + s.Base.Render("· "+rv.source) +
		"  " + s.Info.Render(m.spinner()+" decomposing")
	b.WriteString("\n" + head + "\n")
	b.WriteString(s.Separator.Render(strings.Repeat("─", max(min(w, 76), 0))) + "\n")

	// budget the pane: header (3) + trailing blank + hint (2) are fixed;
	// commentary gets up to a third of the rest, activity the remainder.
	avail := max(h-5, 3)
	tail := tailLines(rv.tail, max(w-2, 8), min(max(avail/3, 2), 8))
	acts := rv.activity
	if keep := max(avail-len(tail)-1, 1); len(acts) > keep {
		acts = acts[len(acts)-keep:]
	}
	for _, a := range acts {
		if a.note {
			b.WriteString("  " + s.Faint.Render("· "+ansi.Truncate(sanitize(a.text), max(w-4, 8), "…")) + "\n")
		} else {
			b.WriteString("  " + s.Success.Render("✓ ") + s.Subtle.Render(ansi.Truncate(sanitize(a.text), max(w-4, 8), "…")) + "\n")
		}
	}
	if len(tail) > 0 {
		b.WriteString("\n")
		for _, l := range tail {
			b.WriteString("  " + s.Faint.Render(l) + "\n")
		}
	}
	if len(acts) == 0 && len(tail) == 0 {
		b.WriteString("  " + s.Faint.Render("starting…") + "\n")
	}

	b.WriteString("\n" + s.Faint.Render("watch-only — the proposal opens here for review when the pass completes") +
		"\n" + s.KeyHint.Render("esc") + s.KeyLabel.Render(" board") +
		s.Faint.Render(" · ") + s.KeyHint.Render("I") + s.KeyLabel.Render(" back to this view"))

	return clipLines(b.String(), h)
}

// tailLines wraps the streamed commentary and returns its last few lines.
func tailLines(text string, w, keep int) []string {
	text = strings.TrimSpace(sanitize(text))
	if text == "" || keep <= 0 {
		return nil
	}
	lines := strings.Split(wrapText(text, w), "\n")
	if len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}
	return lines
}
