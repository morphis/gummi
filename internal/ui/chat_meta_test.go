package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

func TestHumanTokens(t *testing.T) {
	cases := map[int64]string{0: "0", 42: "42", 999: "999", 1200: "1.2k", 45000: "45.0k", 2_000_000: "2.0M"}
	for n, want := range cases {
		if got := humanTokens(n); got != want {
			t.Errorf("humanTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestChatMeta(t *testing.T) {
	s := theme.New(theme.GummiDark())
	strip := ansi.Strip

	// full snapshot: model, spend, context with a limit
	snap := engine.Snapshot{
		Spend:   agent.Usage{Model: "gpt-5", InputTokens: 8000, OutputTokens: 2000},
		Context: agent.Context{Tokens: 12000, Limit: 400000},
	}
	got := strip(chatMeta(s, snap))
	for _, want := range []string{"gpt-5", "10.0k tok spent", "12.0k/400.0k ctx", "3%"} {
		if !strings.Contains(got, want) {
			t.Errorf("meta %q missing %q", got, want)
		}
	}

	// no context limit → "ctx" without a fraction
	snap2 := engine.Snapshot{Spend: agent.Usage{Model: "m"}, Context: agent.Context{Tokens: 500}}
	if g := strip(chatMeta(s, snap2)); !strings.Contains(g, "500 ctx") || strings.Contains(g, "/") {
		t.Errorf("meta without limit = %q, want '500 ctx' and no fraction", g)
	}

	// empty snapshot → empty meta
	if g := chatMeta(s, engine.Snapshot{}); g != "" {
		t.Errorf("empty meta = %q, want empty", g)
	}
}
