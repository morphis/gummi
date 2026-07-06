package engine

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/agent/fakeopenai"
	"github.com/morphis/gummi/internal/domain"
)

// TestEngineWithCopilotBYOK drives the whole stack — engine → Copilot
// adapter → CLI → fake OpenAI server — for one interactive turn. It
// skips when the CLI is absent. This is the M1 "core loop" proof.
func TestEngineWithCopilotBYOK(t *testing.T) {
	cli := os.Getenv("COPILOT_CLI_PATH")
	if cli == "" {
		if p, err := exec.LookPath("copilot"); err == nil {
			cli = p
		} else if home, herr := os.UserHomeDir(); herr == nil {
			if _, serr := os.Stat(home + "/.local/bin/copilot"); serr == nil {
				cli = home + "/.local/bin/copilot"
			}
		}
	}
	if cli == "" {
		t.Skip("copilot CLI not found")
	}

	ws, store, wt := newRepo(t)
	srv := fakeopenai.New(fakeopenai.WithReply("Two approaches: localStorage vs synced account."))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ag, err := agent.NewCopilot(ctx, agent.CopilotOptions{CLIPath: cli, LogLevel: "error"})
	if err != nil {
		t.Skipf("cannot start copilot CLI: %v", err)
	}
	defer ag.Close()

	e := New(Config{
		Agent: ag, Store: store, Worktrees: wt, Workspace: ws,
		Model:    "fake-model",
		Provider: agent.Provider{Type: "openai", BaseURL: srv.BaseURL()},
	})
	defer e.Close()

	f := feature(1, "Dark mode", domain.StageBrainstorm)
	s, err := e.Attach(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Send(ctx, f.ID, "How should dark mode persist?"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventIdle)

	snap := s.Snapshot()
	if len(snap.Transcript) < 2 {
		t.Fatalf("transcript too short: %+v", snap.Transcript)
	}
	last := snap.Transcript[len(snap.Transcript)-1]
	if last.Author != AuthorAssistant || last.Content == "" {
		t.Errorf("no assistant reply: %+v", last)
	}
	if len(srv.Requests()) == 0 {
		t.Fatal("BYOK provider was never called")
	}
}
