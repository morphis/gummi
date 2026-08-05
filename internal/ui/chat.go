package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

// chatPane is the interactive brainstorm/spec surface: a scrollable
// transcript over an engine session plus an input textarea. It reads
// the session's state via Snapshot and never owns transcript state.
type chatPane struct {
	feature domain.FeatureID
	session *engine.Session
	input   textarea.Model
	width   int // last width the input was sized to

	scroll     int  // lines scrolled up from the bottom (0 = latest)
	bodyH      int  // transcript viewport height, from the last render
	totalLines int  // total transcript lines, from the last render
	showOutput bool // expand every captured tool output (ctrl+o); failures always show a tail

	// picker state, live while the agent has an open ask_user question
	askID    string       // pending ask CallID the picker is bound to
	cursor   int          // highlighted option index
	picked   map[int]bool // chosen indices (multi-select)
	freeForm bool         // typing a custom answer instead of picking
}

func newChatPane(feature domain.FeatureID, session *engine.Session) *chatPane {
	in := newChatInput()
	in.Focus()
	return &chatPane{feature: feature, session: session, input: in}
}

// newChatInput builds the message textarea (shared with tests).
func newChatInput() textarea.Model {
	in := textarea.New()
	in.Placeholder = "message the agent…"
	in.CharLimit = 4000
	in.ShowLineNumbers = false
	in.SetHeight(3)
	return in
}

// view renders the chat pane into the main area. spin is the shell's
// shared spinner frame for the busy marker.
func (c *chatPane) view(s *theme.Styles, w, h int, spin string) string {
	snap := c.session.Snapshot()
	var b strings.Builder

	head := s.Title.Render(string(snap.Feature.ID)) + " " +
		s.Base.Render("· "+string(snap.Feature.Stage)) + "  " +
		s.ProfileTag.Render("["+string(snap.Role)+"]")
	if snap.Busy {
		head += "  " + s.Info.Render(spin+" thinking")
	}
	if c.scroll > 0 {
		head += "  " + s.Faint.Render("↑ scrolled — pgdn to latest")
	}
	b.WriteString("\n" + head + "\n")
	if meta := chatMeta(s, snap); meta != "" {
		b.WriteString(ansi.Truncate(meta, max(w, 8), "…") + "\n")
	}
	b.WriteString(s.Separator.Render(strings.Repeat("─", max(min(w, 80), 0))) + "\n")

	if newW := max(w-2, 10); newW != c.width {
		c.input.SetWidth(newW)
		c.width = newW
	}

	// the footer is either the option picker (open ask, not in free-form)
	// or the message input.
	c.syncAsk(snap.PendingAsk)
	var footer string
	if snap.PendingAsk != nil && !c.freeForm {
		footer = c.pickerView(s, snap.PendingAsk, w)
	} else {
		footer = c.input.View()
	}
	footerH := lineCount(footer)

	errH := 0
	var errText string
	if snap.Err != nil {
		errText = wrapError(snap.Err.Error(), max(w-2, 4))
		errH = lineCount(errText)
	}
	// header (3 lines) + trailing newline + optional error + footer; the
	// transcript takes whatever is left, never overflowing the pane.
	bodyH := max(h-4-footerH-errH, 1)
	b.WriteString(c.transcript(s, snap, w, bodyH))
	b.WriteString("\n")
	if snap.Err != nil {
		b.WriteString(s.Error.Render(errText) + "\n")
	}
	b.WriteString(footer)
	return b.String()
}

// pickerView renders the inline ask_user option picker: the question,
// then one line per option with a cursor and (multi-select) tick, then a
// muted key-hint line. No dialog frame — it reads as part of the chat,
// per gummi's minimal styling.
func (c *chatPane) pickerView(s *theme.Styles, ask *engine.Ask, w int) string {
	width := max(w-2, 10)
	var b strings.Builder
	b.WriteString(s.Title.Render(string(c.feature)+" asks") + "  " +
		s.Base.Render(ansi.Truncate(sanitize(ask.Question), max(width-len(c.feature)-8, 8), "…")) + "\n")
	for i, o := range ask.Options {
		cursor := "  "
		label := s.Base
		if i == c.cursor {
			cursor = s.KeyHint.Render("▸ ")
			label = s.Title
		}
		tick := ""
		if ask.MultiPick {
			box := "○ "
			if c.picked[i] {
				box = "● "
			}
			tick = s.Faint.Render(box)
		}
		line := fmt.Sprintf("%s%s%d. %s", cursor, tick, i+1, sanitize(o.Label))
		if o.Detail != "" {
			line += s.Faint.Render(" — " + sanitize(o.Detail))
		}
		b.WriteString(ansi.Truncate(label.Render(line), width, "…") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// bindings is the chat pane's key table (see keymap.go), split by
// footer mode: the ask_user option picker, its free-form answer input,
// or the plain message input.
func (c *chatPane) bindings() []binding {
	var ask *engine.Ask
	if c.session != nil {
		ask = c.session.Snapshot().PendingAsk
	}
	switch {
	case ask != nil && !c.freeForm:
		bs := []binding{
			{key: "↑↓", label: "move", bar: true},
			{key: "1..9", label: "pick", help: "jump to an option", bar: true},
		}
		if ask.MultiPick {
			bs = append(bs,
				binding{key: "space", label: "tick", bar: true},
				binding{key: "enter", label: "submit", bar: true})
		} else {
			bs = append(bs, binding{key: "enter", label: "select", bar: true})
		}
		if ask.FreeForm {
			bs = append(bs, binding{key: "o", label: "own answer", help: "type your own answer", bar: true})
		}
		return append(bs, binding{key: "esc", label: "detach", help: "detach — the question stays pending", bar: true})
	case ask != nil && c.freeForm:
		return []binding{
			{key: "enter", label: "answer", bar: true},
			{key: "pgup/pgdn", label: "scroll", bar: true},
			{key: "esc", label: "picker", help: "back to the option picker", bar: true},
		}
	default:
		return []binding{
			{key: "enter", label: "send", bar: true},
			{key: "pgup/pgdn", label: "scroll", bar: true},
			{key: "ctrl+o", label: "outputs", help: "expand/collapse captured tool outputs", bar: true},
			{key: "esc", label: "detach", help: "detach — the session keeps running", bar: true},
		}
	}
}

// transcript renders the conversation, tail-anchored to bodyH lines.
func (c *chatPane) transcript(s *theme.Styles, snap engine.Snapshot, w, bodyH int) string {
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
			lines = append(lines, c.toolOutputLines(s, msg, w)...)
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
			label = s.Faint.Render("gummi")
			style = s.Faint
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
	if len(snap.Transcript) == 0 {
		lines = append(lines, s.Faint.Render("  start the conversation below"))
	}

	// record layout for the key handler (paging clamps against these);
	// clamp the scroll locally so render never mutates the stored offset.
	total := len(lines)
	c.bodyH, c.totalLines = bodyH, total
	maxScroll := max(total-bodyH, 0)
	scroll := min(max(c.scroll, 0), maxScroll)

	end := total - scroll
	start := max(end-bodyH, 0)
	visible := lines[start:end]
	// pad to fill so the input stays pinned to the bottom
	for len(visible) < bodyH {
		visible = append([]string{""}, visible...)
	}
	return strings.Join(visible, "\n")
}

// failTailLines is how much of a failed tool's output shows inline
// without expanding — enough to read the error, not flood the pane.
const failTailLines = 8

// toolOutputLines renders a tool entry's captured output: a failure
// always shows its tail (the error is the point), and ctrl+o expands
// every entry's full output. Indented and faint so it reads as detail
// behind the tool line above it.
func (c *chatPane) toolOutputLines(s *theme.Styles, msg engine.Message, w int) []string {
	if msg.ToolOutput == "" {
		return nil
	}
	show := c.showOutput || msg.ToolStatus == engine.ToolFail
	if !show {
		return nil
	}
	body := strings.Split(wrapText(sanitize(msg.ToolOutput), max(w-8, 8)), "\n")
	if !c.showOutput && len(body) > failTailLines {
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

// syncAsk aligns the picker with the session's current pending ask,
// resetting selection state when a new question arrives and clearing it
// when the question is answered.
func (c *chatPane) syncAsk(ask *engine.Ask) {
	if ask == nil {
		c.askID, c.freeForm, c.picked = "", false, nil
		return
	}
	key := ask.CallID + "|" + ask.Question // stable per question (CallID empty on convention path)
	if key != c.askID {
		c.askID = key
		c.cursor = 0
		c.picked = map[int]bool{}
		c.freeForm = false
	}
}

// handleKey processes a key while the chat pane is focused. It returns
// (detach, send, answer, cmd): detach closes the pane; send carries a
// chat message; answer carries the user's reply to an open ask_user
// question. At most one of send/answer is non-empty.
func (c *chatPane) handleKey(msg tea.KeyPressMsg) (detach bool, send, answer string, cmd tea.Cmd) {
	var ask *engine.Ask
	if c.session != nil {
		ask = c.session.Snapshot().PendingAsk
	}
	c.syncAsk(ask)
	if ask != nil && !c.freeForm {
		return c.handlePickerKey(msg, ask)
	}

	switch msg.String() {
	case "esc":
		if ask != nil { // leave free-form back to the picker, don't detach
			c.freeForm = false
			return false, "", "", nil
		}
		return true, "", "", nil
	case "enter":
		text := strings.TrimSpace(c.input.Value())
		if text == "" {
			return false, "", "", nil
		}
		c.input.Reset()
		c.scroll = 0    // jump back to the latest on send
		if ask != nil { // free-form answer to the open question
			return false, "", text, nil
		}
		return false, text, "", nil
	case "pgup", "ctrl+u":
		c.scrollBy(c.page())
		return false, "", "", nil
	case "pgdown", "ctrl+d":
		c.scrollBy(-c.page())
		return false, "", "", nil
	case "ctrl+o":
		c.showOutput = !c.showOutput
		return false, "", "", nil
	}
	c.input, cmd = c.input.Update(msg)
	return false, "", "", cmd
}

// handlePaste inserts bracketed-paste text into the message input.
// While the option picker is up (and not in free-form) there is no
// input to paste into, so the paste is dropped.
func (c *chatPane) handlePaste(msg tea.PasteMsg) tea.Cmd {
	var ask *engine.Ask
	if c.session != nil {
		ask = c.session.Snapshot().PendingAsk
	}
	c.syncAsk(ask)
	if ask != nil && !c.freeForm {
		return nil
	}
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(msg)
	return cmd
}

// handlePickerKey drives the inline option picker for an open ask.
func (c *chatPane) handlePickerKey(msg tea.KeyPressMsg, ask *engine.Ask) (detach bool, send, answer string, cmd tea.Cmd) {
	switch msg.String() {
	case "esc":
		return true, "", "", nil // detach; the question stays pending
	case "up", "k", "ctrl+p":
		c.cursor = (c.cursor - 1 + len(ask.Options)) % len(ask.Options)
	case "down", "j", "ctrl+n":
		c.cursor = (c.cursor + 1) % len(ask.Options)
	case "o":
		if ask.FreeForm {
			c.freeForm = true
			c.input.Reset()
		}
	case "space":
		if ask.MultiPick {
			c.picked[c.cursor] = !c.picked[c.cursor]
		}
	case "enter":
		return false, "", c.answerText(ask), nil
	default:
		// number keys 1-9 jump to an option
		if d := msg.String(); len(d) == 1 && d[0] >= '1' && d[0] <= '9' {
			if i := int(d[0] - '1'); i < len(ask.Options) {
				c.cursor = i
				if !ask.MultiPick {
					return false, "", c.answerText(ask), nil
				}
				c.picked[i] = !c.picked[i]
			}
		}
	}
	return false, "", "", nil
}

// answerText renders the current selection into the answer string sent
// back to the agent: the chosen labels (comma-joined for multi-select),
// falling back to the cursor's label when nothing is explicitly ticked.
func (c *chatPane) answerText(ask *engine.Ask) string {
	if ask.MultiPick {
		var picks []string
		for i, o := range ask.Options {
			if c.picked[i] {
				picks = append(picks, o.Label)
			}
		}
		if len(picks) > 0 {
			return strings.Join(picks, ", ")
		}
	}
	if c.cursor >= 0 && c.cursor < len(ask.Options) {
		return ask.Options[c.cursor].Label
	}
	return ""
}

// page is the scroll step: most of a viewport, with a small overlap.
func (c *chatPane) page() int {
	if c.bodyH > 2 {
		return c.bodyH - 1
	}
	return 5
}

// scrollBy moves the scrollback offset, clamped to the transcript.
func (c *chatPane) scrollBy(delta int) {
	maxScroll := max(c.totalLines-c.bodyH, 0)
	c.scroll = min(max(c.scroll+delta, 0), maxScroll)
}

// chatMeta is the backend / model / provider / spend / context-window
// status line under the chat header. Backend, model, and provider are
// known from spawn; spend and context appear as the agent reports them.
func chatMeta(s *theme.Styles, snap engine.Snapshot) string {
	var parts []string
	if snap.AgentName != "" {
		parts = append(parts, s.Muted.Render(snap.AgentName))
	}
	if m := runModel(snap); m != "" {
		parts = append(parts, s.Muted.Render(m))
	}
	if sp := spendSummary(snap); sp != "" {
		parts = append(parts, s.Faint.Render(sp+" spent"))
	}
	if c := snap.Context; c.Tokens > 0 {
		ctx := humanTokens(c.Tokens) + " ctx"
		if c.Limit > 0 {
			ctx = fmt.Sprintf("%s/%s ctx (%d%%)", humanTokens(c.Tokens), humanTokens(c.Limit), c.Tokens*100/c.Limit)
		}
		parts = append(parts, s.Faint.Render(ctx))
	}
	return strings.Join(parts, s.Faint.Render("  ·  "))
}

// humanTokens renders a token count compactly: 1234 → "1.2k", 2e6 → "2M".
func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func lineCount(s string) int { return strings.Count(s, "\n") + 1 }

// errLines caps a wrapped error so it can't crowd out the transcript.
const errLines = 6

// wrapError wraps an error's full text to the pane width instead of
// truncating it to one line — session-start failures (backend refusals,
// provider errors) carry their diagnosis in the tail.
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
