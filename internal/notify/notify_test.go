package notify

import (
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"":        Off,
		"off":     Off,
		"none":    Off,
		"bell":    Bell,
		"BELL":    Bell,
		"desktop": Desktop,
		"osc":     Desktop,
		"weird":   Bell, // unknown non-empty → bell
	}
	for in, want := range cases {
		if got := ParseMode(in); got != want {
			t.Errorf("ParseMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAlertBell(t *testing.T) {
	var b strings.Builder
	New(Bell, &b).Alert("FD-001 needs you")
	if b.String() != "\a" {
		t.Errorf("bell wrote %q, want BEL", b.String())
	}
}

func TestAlertDesktopWrapsAndScrubs(t *testing.T) {
	var b strings.Builder
	// a title carrying an embedded BEL (which would terminate the OSC) and
	// an ESC must be scrubbed so the sequence stays well-formed.
	New(Desktop, &b).Alert("FD-001: \x07evil\x1b]9;spoof")
	out := b.String()
	if !strings.HasPrefix(out, "\x1b]9;") || !strings.HasSuffix(out, "\a") {
		t.Fatalf("malformed OSC 9: %q", out)
	}
	// exactly one terminating BEL (the scrubbed payload contributes none)
	if strings.Count(out, "\a") != 1 {
		t.Errorf("payload BEL not scrubbed: %q", out)
	}
	if strings.Contains(out[:len(out)-1], "\x1b]9;spoof") {
		// the payload's ESC was scrubbed, so no nested OSC introducer remains
		if strings.Count(out, "\x1b") != 1 {
			t.Errorf("payload ESC not scrubbed: %q", out)
		}
	}
}

func TestAlertOffAndNil(t *testing.T) {
	var b strings.Builder
	New(Off, &b).Alert("x")
	if b.String() != "" {
		t.Errorf("off mode wrote %q, want nothing", b.String())
	}
	var n *Notifier
	n.Alert("x") // must not panic
}
