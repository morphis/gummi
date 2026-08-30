package ui

// The thread's conversation rendering: the full transcript of a session —
// messages with their author labels, tool calls with their captured
// output — as a plain list of lines. This is what the chat pane used to
// render in its own viewport; the thread renders the same lines into its
// scrollable body, which is what retires the pane: reading a run is a
// requirement of the card, not of the pane that happened to hold it.

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

// transcriptLines renders a session's whole conversation — every turn,
// every tool call, every captured output — as the body lines the thread
// scrolls through. It is the chat pane's transcript, freed of the
// viewport: the thread's own window (composeThread) takes whatever fits,
// and threadScroll pages back through the rest.
//
// Tool calls render as compact ticker lines, in order with the messages
// around them; consecutive ones group without blanks. A failure always
// shows its output tail (the error is the point) and showOutput expands
// every entry's full output.
func transcriptLines(s *theme.Styles, snap engine.Snapshot, w int, showOutput bool) []string {
	var lines []string
	for i, msg := range snap.Transcript {
		// tool calls render as compact ticker lines, in order with the
		// messages around them; consecutive ones group without blanks.
		if msg.Author == engine.AuthorTool {
			// the spec-capture note is folded into the answer bubble above
			// it (see AuthorUser), so an answer isn't recorded twice — once
			// as its own chat message and again as this note.
			if msg.Content == engine.AnswerCapturedNote && i > 0 && snap.Transcript[i-1].Author == engine.AuthorUser {
				continue
			}
			lines = append(lines, "  "+toolMarker(s, msg.ToolStatus)+
				toolLineView(s, sanitize(msg.Content), max(w-6, 8)))
			lines = append(lines, toolOutputLines(s, msg.ToolStatus, msg.ToolOutput, w, showOutput)...)
			if i+1 == len(snap.Transcript) || snap.Transcript[i+1].Author != engine.AuthorTool {
				lines = append(lines, "")
			}
			continue
		}
		var label string
		style := s.Base
		switch msg.Author {
		case engine.AuthorUser:
			label = s.KeyHint.Render("you")
			// an answer captured into the spec is marked in place instead of
			// trailed by a separate "recorded…" note (deduped above)
			if i+1 < len(snap.Transcript) && snap.Transcript[i+1].Author == engine.AuthorTool &&
				snap.Transcript[i+1].Content == engine.AnswerCapturedNote {
				label += " " + s.Faint.Render("· recorded in the spec")
			}
		case engine.AuthorSystem:
			// the label is what marks a turn as gummi's own; the body is
			// still something you are meant to read, and rendering it at
			// the faintest weight on the palette made the one message that
			// opens every stage the hardest to read on the page
			label = s.Faint.Render("gummi")
			style = s.Subtle
		default:
			label = s.Title.Render(string(snap.Role))
			style = s.Subtle
		}
		// assistant text is untrusted model output; strip escapes before render
		wrapped := wrapText(sanitize(msg.Content), max(w-4, 8))
		block := strings.Split(wrapped, "\n")
		lines = append(lines, label)
		for _, l := range block {
			lines = append(lines, "  "+style.Render(l))
		}
		lines = append(lines, "")
	}
	return lines
}

// failTailLines is how much of a failed tool's output shows inline
// without expanding — enough to read the error, not flood the pane.
const failTailLines = 8

// toolOutputLines renders one tool entry's captured output: a failure
// always shows its tail (the error is the point), and alt+o expands
// every entry's full output. Indented and faint so it reads as detail
// behind the tool line above it.
func toolOutputLines(s *theme.Styles, status engine.ToolStatus, output string, w int, showOutput bool) []string {
	if output == "" {
		return nil
	}
	if !showOutput && status != engine.ToolFail {
		return nil
	}
	body := strings.Split(wrapText(sanitize(output), max(w-8, 8)), "\n")
	if !showOutput && len(body) > failTailLines {
		body = append([]string{"…"}, body[len(body)-failTailLines:]...)
	}
	out := make([]string, 0, len(body))
	for _, l := range body {
		out = append(out, "      "+s.Faint.Render(l))
	}
	return out
}

// toolMarker is the outcome glyph before a tool line: confirmed success,
// confirmed failure, or a neutral dot when the outcome is unknown (notes
// and backends that don't report results) — never a dishonest ✓.
func toolMarker(s *theme.Styles, st engine.ToolStatus) string {
	switch st {
	case engine.ToolOK:
		return s.Success.Render("✓ ")
	case engine.ToolFail:
		return s.Error.Render("✗ ")
	default:
		return s.Faint.Render("· ")
	}
}

// toolLineView styles one activity-ticker line, truncated ANSI-aware to
// width. A tool call arrives composed as "name  detail" (the engine's
// toolLine, double-space separator): the name renders Muted and the
// detail Faint, so a run of calls scans as a column of verbs with the
// arguments receding behind them. Lines without that shape — check
// results, budget nudges, notes — stay single-style Faint as before.
func toolLineView(s *theme.Styles, content string, width int) string {
	name, detail, ok := strings.Cut(content, "  ")
	if !ok || name == "" || strings.Contains(name, " ") {
		return s.Faint.Render(ansi.Truncate(content, width, "…"))
	}
	return ansi.Truncate(s.Muted.Render(name)+"  "+s.Faint.Render(detail), width, "…")
}

// errLines caps a wrapped error so it can't crowd out the transcript.
const errLines = 6

// wrapError wraps an error's full text to width instead of truncating it
// to one line — session-start failures (backend refusals, provider
// errors) carry their diagnosis in the tail.
func wrapError(msg string, w int) string {
	lines := strings.Split(wrapText("✗ "+sanitize(msg), w), "\n")
	if len(lines) > errLines {
		lines = append(lines[:errLines], "…")
	}
	return strings.Join(lines, "\n")
}

// wrapText hard-wraps text to width on word boundaries (ANSI-safe).
func wrapText(text string, width int) string {
	if width < 1 {
		width = 1
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			switch {
			case line == "":
				line = word
			case ansi.StringWidth(line)+1+ansi.StringWidth(word) <= width:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
