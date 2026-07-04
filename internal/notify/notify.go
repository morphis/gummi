// Package notify emits a terminal bell or a desktop notification when a
// feature needs the user's attention (DESIGN §4.2). It writes control
// sequences to a supplied writer (the terminal, via os.Stderr in
// production) so the escapes reach the terminal without disturbing the
// Bubbletea render surface — the bell and OSC sequences are consumed by
// the terminal, not drawn into the screen buffer.
package notify

import (
	"fmt"
	"io"
	"strings"
)

// Mode selects how an attention event is signalled.
type Mode string

const (
	// Off disables notifications.
	Off Mode = "off"
	// Bell rings the terminal bell (BEL, \a).
	Bell Mode = "bell"
	// Desktop posts an OSC 9 notification (iTerm2/kitty/WezTerm/foot and
	// others surface it as a desktop toast); terminals that don't
	// understand it ignore the sequence.
	Desktop Mode = "desktop"
)

// ParseMode maps a config/env string to a Mode, falling back to Bell for
// unknown values and to Off for the empty string.
func ParseMode(s string) Mode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "none", "false":
		return Off
	case "desktop", "osc", "toast":
		return Desktop
	case "":
		return Off
	default:
		return Bell
	}
}

// Notifier emits attention signals in one Mode to one writer.
type Notifier struct {
	mode Mode
	w    io.Writer
}

// New builds a Notifier. A nil writer or the Off mode makes Alert a no-op.
func New(mode Mode, w io.Writer) *Notifier {
	return &Notifier{mode: mode, w: w}
}

// Alert signals one needs-attention event. title is used only by the
// desktop mode; it is stripped of control bytes so it cannot break out of
// the OSC sequence (the source may be agent-authored).
func (n *Notifier) Alert(title string) {
	if n == nil || n.w == nil {
		return
	}
	// notifications are best-effort: a failed write to the terminal must
	// never surface as an error in the caller's path.
	switch n.mode {
	case Bell:
		_, _ = fmt.Fprint(n.w, "\a")
	case Desktop:
		_, _ = fmt.Fprintf(n.w, "\x1b]9;%s\a", scrub(title))
	}
}

// scrub removes control characters — C0 (< 0x20, incl. the BEL/ESC that
// terminate/introduce an OSC), DEL, and C1 (0x7f–0x9f, incl. the C1 CSI
// 0x9b and String Terminator 0x9c) — so an untrusted title can't inject
// or truncate the sequence. Mirrors internal/ui.sanitize's control range.
func scrub(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}
