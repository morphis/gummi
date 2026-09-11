package ui

// The thread's conversation rendering: the full transcript of a session —
// messages with their author labels, tool calls with their captured
// output — as a plain list of lines. This is what the chat pane used to
// render in its own viewport; the thread renders the same lines into its
// scrollable body, which is what retires the pane: reading a run is a
// requirement of the card, not of the pane that happened to hold it.

import (
	"regexp"
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
			// The CLEAN answer note is folded into the answer bubble above
			// it (see AuthorUser), so an answer isn't recorded twice — once
			// as its own chat message and again as this note. The two
			// unhappy notes are NOT folded: they are the only place the
			// user learns their answer did not land where the question
			// said it would, so they stay on screen and say why.
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
			// EVERY answer to an ask carries its outcome, not just the
			// happy one. The suffix used to appear only for a clean
			// capture, so a typed answer read "you · recorded in the spec"
			// and a picked option — whose capture had quietly failed —
			// read a bare "you". Two answers to the same question, one
			// with a status and one with nothing, and the reader's fair
			// conclusion was that the picked one was recorded nowhere.
			if i+1 < len(snap.Transcript) && snap.Transcript[i+1].Author == engine.AuthorTool {
				if suffix := answerOutcome(snap.Transcript[i+1].Content); suffix != "" {
					label += " " + s.Faint.Render(suffix)
				}
			}
		case engine.AuthorSystem:
			// the label is what marks a turn as gummi's own; the body is
			// still something you are meant to read, and rendering it at
			// the faintest weight on the palette made the one message that
			// opens every stage the hardest to read on the page
			label = s.Faint.Render("gummi")
			style = s.Subtle
		default:
			// The message's OWN role wins when it carries one; the session's
			// role is the fallback for the ordinary case, where every turn was
			// produced by the session holding it (engine.Message.Role).
			//
			// Labelling everything with snap.Role is what made a consult
			// session re-attribute the stage transcript OpenConsult seeds it
			// with: the plan stage's kickoff and the reviewer's own "VERDICT:
			// pass" rendered once under `reviewer` in the stage segment and
			// again, fifteen lines down the same card page, under `consult`
			// inside a block captioned with the architect's model.
			role := snap.Role
			if msg.Role != "" {
				role = msg.Role
			}
			label = s.Title.Render(string(role))
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

// answerOutcome is the short status an answer's own bubble wears, taken
// from the activity note captureAnswer left beside it. Empty for anything
// that is not an answer note — an ask with no spec anchor has no outcome
// to report, and an ordinary tool call following an answer is not about
// the answer at all.
//
// The wording is deliberately shorter than the note it summarizes: the
// unhappy notes stay on screen right below and carry the detail, so the
// bubble only has to say which of the three happened.
func answerOutcome(note string) string {
	switch {
	case note == engine.AnswerCapturedNote:
		return "· recorded in the spec"
	case strings.HasPrefix(note, engine.AnswerAppendedPrefix):
		return "· saved at the end of the spec"
	case strings.HasPrefix(note, engine.AnswerNotSavedPrefix):
		return "· not saved to the spec"
	}
	return ""
}

// toolLineView styles one activity-ticker line, truncated ANSI-aware to
// width. A tool call arrives composed as "name  detail" (the engine's
// toolLine, double-space separator): the name renders Muted and the
// detail Faint, so a run of calls scans as a column of verbs with the
// arguments receding behind them. Lines without that shape — check
// results, budget nudges, notes — stay single-style Faint as before.
func toolLineView(s *theme.Styles, content string, width int) string {
	content = plainToolNames(content)
	name, detail, ok := strings.Cut(content, "  ")
	if !ok || name == "" || strings.Contains(name, " ") {
		return s.Faint.Render(ansi.Truncate(content, width, "…"))
	}
	return ansi.Truncate(s.Muted.Render(name)+"  "+s.Faint.Render(detail), width, "…")
}

// mcpToolPrefix matches the wire spelling of an MCP tool name —
// mcp__<server>__<tool> — anywhere in an activity line.
//
// The prefix is a transport detail of how a tool reaches the backend, and
// it is the backend's own naming, not gummi's. On screen it turned every
// one of gummi's own tools into line noise:
//
//	· ToolSearch  select:mcp__gummi__spec_view,mcp__gummi__ask_user,mcp__gummi__spec_rep…
//	· mcp__gummi__spec_replace_section
//
// — where the reader wants "spec_view", "ask_user", "spec_replace_section".
// Worse, the prefix is 12 characters of nothing repeated per name, so the
// truncation that keeps the line inside the pane spends most of its budget
// on it and elides the part that says what happened.
var mcpToolPrefix = regexp.MustCompile(`\bmcp__[A-Za-z0-9_.-]+?__`)

// plainToolNames strips MCP transport prefixes from an activity line.
//
// It rewrites the DISPLAY only — nothing downstream reads these lines back
// — and it touches nothing else, so a genuinely foreign tool (Bash, Read,
// Glob, Edit) still renders under the name the backend actually ran, which
// is the name a reader would grep for.
func plainToolNames(content string) string {
	if !strings.Contains(content, "mcp__") {
		return content
	}
	return mcpToolPrefix.ReplaceAllString(content, "")
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
