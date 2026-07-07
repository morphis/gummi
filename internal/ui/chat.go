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

	scroll     int // lines scrolled up from the bottom (0 = latest)
	bodyH      int // transcript viewport height, from the last render
	totalLines int // total transcript lines, from the last render

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
	in.Placeholder = "message the agent…  (enter send · pgup/pgdn scroll · esc detach)"
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
	if snap.Err != nil {
		errH = 1
	}
	// header (3 lines) + trailing newline + optional error + footer; the
	// transcript takes whatever is left, never overflowing the pane.
	bodyH := max(h-4-footerH-errH, 1)
	b.WriteString(c.transcript(s, snap, w, bodyH))
	b.WriteString("\n")
	if snap.Err != nil {
		b.WriteString(s.Error.Render("✗ "+ansi.Truncate(sanitize(snap.Err.Error()), max(w-2, 4), "…")) + "\n")
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
	hint := "↑↓ move · enter select"
	if ask.MultiPick {
		hint = "↑↓ move · space tick · enter submit"
	}
	if ask.FreeForm {
		hint += " · o type your own"
	}
	hint += " · esc detach"
	b.WriteString(s.Faint.Render(hint))
	return b.String()
}

// transcript renders the conversation, tail-anchored to bodyH lines.
func (c *chatPane) transcript(s *theme.Styles, snap engine.Snapshot, w, bodyH int) string {
	var lines []string
	for _, msg := range snap.Transcript {
		var label string
		style := s.Base
		switch msg.Author {
		case engine.AuthorUser:
			label = s.KeyHint.Render("you")
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
	case " ":
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
	if p := snap.Provider.Describe(); p != "" {
		parts = append(parts, s.Faint.Render(p))
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
