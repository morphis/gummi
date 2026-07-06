package agent

import (
	"context"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent/fakeopenai"
)

func TestCopilotBudgetCap(t *testing.T) {
	cli := findCopilot(t)
	srv := fakeopenai.New(fakeopenai.WithReply("ok"))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ag, err := NewCopilot(ctx, CopilotOptions{CLIPath: cli, LogLevel: "error"})
	if err != nil {
		t.Skip(err)
	}
	defer ag.Close()
	_, err = ag.NewSession(ctx, SessionOpts{
		WorkDir: t.TempDir(), Model: "fake-model",
		Provider:   Provider{Type: "openai", BaseURL: srv.BaseURL()},
		MaxCredits: 9,
	})
	if err != nil {
		t.Fatalf("session with MaxCredits=9 failed: %v", err)
	}
}
