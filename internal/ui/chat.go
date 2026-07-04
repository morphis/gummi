package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/engine"
	"github.com/morphia/gummi/internal/ui/theme"
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

// view renders the chat pane into the main area.
func (c *chatPane) view(s *theme.Styles, w, h int) string {
	snap := c.session.Snapshot()
	var b strings.Builder

	head := s.Title.Render(string(snap.Feature.ID)) + " " +
		s.Base.Render("· "+string(snap.Feature.Stage)) + "  " +
		s.ProfileTag.Render("["+string(snap.Role)+"]")
	if snap.Busy {
		head += "  " + s.Info.Render("⣾ thinking")
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
	inputView := c.input.View()
	inputH := lineCount(inputView)

	errH := 0
	if snap.Err != nil {
		errH = 1
	}
	// header (3 lines) + trailing newline + optional error + input; the
	// transcript takes whatever is left, never overflowing the pane.
	bodyH := max(h-4-inputH-errH, 1)
	b.WriteString(c.transcript(s, snap, w, bodyH))
	b.WriteString("\n")
	if snap.Err != nil {
		b.WriteString(s.Error.Render("✗ "+ansi.Truncate(sanitize(snap.Err.Error()), max(w-2, 4), "…")) + "\n")
	}
	b.WriteString(inputView)
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

// handleKey processes a key while the chat pane is focused. It returns
// (detach, send, cmd): detach closes the pane; send carries a message
// to deliver to the engine.
func (c *chatPane) handleKey(msg tea.KeyPressMsg) (detach bool, send string, cmd tea.Cmd) {
	switch msg.String() {
	case "esc":
		return true, "", nil
	case "enter":
		text := strings.TrimSpace(c.input.Value())
		if text == "" {
			return false, "", nil
		}
		c.input.Reset()
		c.scroll = 0 // jump back to the latest on send
		return false, text, nil
	case "pgup", "ctrl+u":
		c.scrollBy(c.page())
		return false, "", nil
	case "pgdown", "ctrl+d":
		c.scrollBy(-c.page())
		return false, "", nil
	}
	c.input, cmd = c.input.Update(msg)
	return false, "", cmd
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
