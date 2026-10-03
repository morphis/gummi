package ui

// The thread's conversation rendering: the full transcript of a session —
// messages with their author labels, tool calls with their captured
// output — as a plain list of lines. This is what the chat pane used to
// render in its own viewport; the thread renders the same lines into its
// scrollable body, which is what retires the pane: reading a run is a
// requirement of the card, not of the pane that happened to hold it.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/threadfold"
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
	for i := 0; i < len(snap.Transcript); i++ {
		msg := snap.Transcript[i]
		// everything the agent did between two messages is one summary row
		// until alt+o expands it; expanded, each entry renders as below.
		if isActivityMsg(msg) && !showOutput {
			j := i
			for j < len(snap.Transcript) && isActivityMsg(snap.Transcript[j]) {
				j++
			}
			if line := activityLine(s, snap.Transcript[i:j], w); line != "" {
				lines = append(lines, line, "")
			}
			i = j - 1
			continue
		}
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
			lines = append(lines, "  "+toolMarker(s, msg.Tool, msg.ToolStatus)+
				toolLineView(s, sanitize(msg.Content), max(w-6, 8)))
			lines = append(lines, toolOutputLines(s, msg.ToolStatus, msg.ToolOutput, w, showOutput)...)
			if i+1 == len(snap.Transcript) || snap.Transcript[i+1].Author != engine.AuthorTool {
				lines = append(lines, "")
			}
			continue
		}
		if msg.Author == engine.AuthorThinking {
			lines = append(lines, thinkingLines(s, msg.Content, w, showOutput)...)
			continue
		}
		if msg.Author == engine.AuthorTasks {
			continue // pinned below the conversation, not in its flow
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
		for _, img := range msg.Images {
			lines = append(lines, "  "+s.Faint.Render("[image: "+img.Name+"]"))
		}
		lines = append(lines, "")
	}
	lines = append(lines, taskLines(s, snap.Tasks, w)...)
	lines = append(lines, watchLines(s, snap.Watches, w)...)
	return append(lines, queuedLines(s, snap.Queued, w)...)
}

// queuedLines renders what was said while the agent was mid-turn, waiting
// to go to it as its next turn; alt+u takes the newest back.
func queuedLines(s *theme.Styles, queued []string, w int) []string {
	if len(queued) == 0 {
		return nil
	}
	lines := []string{s.Faint.Render("queued · alt+u takes the last back")}
	for _, q := range queued {
		lines = append(lines, "  "+s.Subtle.Render(ansi.Truncate(sanitize(strings.Join(strings.Fields(q), " ")), max(w-4, 8), "…")))
	}
	return append(lines, "")
}

// watchLines lists the gummi watches a freeform session has running:
// each will speak up as a turn of its own, so the reader should know they
// are there.
func watchLines(s *theme.Styles, watches []string, w int) []string {
	if len(watches) == 0 {
		return nil
	}
	lines := []string{s.Faint.Render("watching")}
	for _, x := range watches {
		lines = append(lines, "  "+s.Subtle.Render(ansi.Truncate(sanitize(x), max(w-4, 8), "…")))
	}
	return append(lines, "")
}

// taskLines renders the agent's checklist after the conversation, where
// the thread's window opens: what it is doing and what is left stays in
// view however long the turn runs, the way a coding agent pins its todo
// list. A finished list folds to its count.
func taskLines(s *theme.Styles, ts []agent.Task, w int) []string {
	if len(ts) == 0 {
		return nil
	}
	done := 0
	for _, t := range ts {
		if t.Status == agent.TaskDone {
			done++
		}
	}
	lines := []string{s.Faint.Render(fmt.Sprintf("tasks %d/%d", done, len(ts)))}
	if done == len(ts) {
		return append(lines, "")
	}
	for _, t := range ts {
		text := ansi.Truncate(sanitize(t.Text), max(w-6, 8), "…")
		switch t.Status {
		case agent.TaskDone:
			lines = append(lines, "  "+s.Faint.Render("✓ "+text))
		case agent.TaskInProgress:
			lines = append(lines, "  "+s.Title.Render("▸ "+text))
		default:
			lines = append(lines, "  "+s.Subtle.Render("○ "+text))
		}
	}
	return append(lines, "")
}

// thinkingTailLines is how much of the agent's reasoning shows without
// expanding: the newest of it, which is what it is thinking now.
const thinkingTailLines = 3

// thinkingLines renders a thinking entry faint, behind the reply it led
// to: its tail only, and the whole of it while alt+o expands output.
func thinkingLines(s *theme.Styles, content string, w int, showOutput bool) []string {
	block := strings.Split(wrapText(sanitize(strings.TrimSpace(content)), max(w-4, 8)), "\n")
	label := "thinking"
	if !showOutput && len(block) > thinkingTailLines {
		label = fmt.Sprintf("thinking · %d more lines (alt+o)", len(block)-thinkingTailLines)
		block = block[len(block)-thinkingTailLines:]
	}
	lines := []string{s.Faint.Render(label)}
	for _, l := range block {
		lines = append(lines, "  "+s.Faint.Render(l))
	}
	return append(lines, "")
}

func isActivityMsg(msg engine.Message) bool {
	return msg.Author == engine.AuthorTool || msg.Author == engine.AuthorThinking
}

// activityLine is a run of tool calls and thoughts as one summary row: the
// call in flight, if any, is named on it. Empty when the run holds nothing
// but a folded answer note.
func activityLine(s *theme.Styles, run []engine.Message, w int) string {
	var calls, fails, thoughts int
	var inFlight string
	var watching bool
	for _, msg := range run {
		switch {
		case msg.Author == engine.AuthorThinking:
			thoughts++
		case msg.Content == engine.AnswerCapturedNote:
		default:
			calls++
			switch msg.ToolStatus {
			case engine.ToolFail:
				fails++
			case engine.ToolOK:
			default:
				// a watch never settles while it runs, so it reads as
				// watching, not as a call in flight (as on the web)
				if threadfold.WatchTool(msg.Tool) {
					watching = true
				} else {
					inFlight = msg.Content
				}
			}
		}
	}
	if calls+thoughts == 0 {
		return ""
	}
	var parts []string
	if calls > 0 {
		parts = append(parts, s.Faint.Render(fmt.Sprintf("%d tool call%s", calls, plural(calls))))
	}
	if fails > 0 {
		parts = append(parts, s.Error.Render(fmt.Sprintf("%d failed", fails)))
	}
	if thoughts > 0 {
		parts = append(parts, s.Faint.Render(fmt.Sprintf("%d thought%s", thoughts, plural(thoughts))))
	}
	if watching {
		parts = append(parts, s.Info.Render("watching"))
	} else if inFlight != "" {
		parts = append(parts, toolLineView(s, sanitize(inFlight), max(w-6, 8)))
	}
	return "  " + s.Faint.Render("▸ ") + strings.Join(parts, s.Faint.Render(" · ")) + s.Faint.Render("  (alt+o)")
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
// confirmed failure, a backend's own background watch still outstanding
// (threadfold.WatchTool — Claude Code's Monitor tool, which never gets a
// reported outcome while it runs), or a neutral dot when the outcome is
// genuinely unknown (notes and backends that don't report results) —
// never a dishonest ✓.
func toolMarker(s *theme.Styles, tool string, st engine.ToolStatus) string {
	switch st {
	case engine.ToolOK:
		return s.Success.Render("✓ ")
	case engine.ToolFail:
		return s.Error.Render("✗ ")
	default:
		if threadfold.WatchTool(tool) {
			return s.Info.Render("◎ ")
		}
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
