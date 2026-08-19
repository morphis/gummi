package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

const testCard = "ENVIRONMENT CARD: this repo is built inside a container; the real hardware is a remote ARM dev VM at 10.0.0.7."

func writeEnvironmentCard(t *testing.T, wsRoot, content string) {
	t.Helper()
	p := filepath.Join(wsRoot, ".gummi", "environment.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentCardAbsent(t *testing.T) {
	rec := recordingAgent()
	e := newEngine(t, rec)

	ctx := context.Background()
	if _, err := e.Attach(ctx, feature(1, "x", domain.StageSpec)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventIdle)

	for _, h := range rec.opts().SystemHints {
		if h == testCard {
			t.Errorf("absent environment.md still injected the card as a hint")
		}
	}
}

func TestEnvironmentCardIsFirstHint(t *testing.T) {
	ws, store, wt := newRepo(t)
	writeEnvironmentCard(t, ws.Root, testCard)
	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	ctx := context.Background()
	if _, err := e.Attach(ctx, feature(1, "x", domain.StageSpec)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventIdle)

	hints := rec.opts().SystemHints
	if len(hints) == 0 {
		t.Fatal("no SystemHints captured")
	}
	if hints[0] != testCard {
		t.Errorf("SystemHints[0] = %q, want %q", hints[0], testCard)
	}
}

func TestEnvironmentCardStageAgnostic(t *testing.T) {
	ws, store, wt := newRepo(t)
	writeEnvironmentCard(t, ws.Root, testCard)
	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	// Triage is an interactive bug stage: the card should still be first.
	f := bugFeature(1, "x", domain.StageTriage)
	ctx := context.Background()
	if _, err := e.Attach(ctx, f); err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventIdle)

	hints := rec.opts().SystemHints
	if len(hints) == 0 {
		t.Fatal("no SystemHints captured")
	}
	if hints[0] != testCard {
		t.Errorf("bug Triage SystemHints[0] = %q, want %q", hints[0], testCard)
	}
}

func TestEnvironmentCardNotInOneShot(t *testing.T) {
	t.Run("Estimate", func(t *testing.T) {
		ws, store, wt := newRepo(t)
		writeEnvironmentCard(t, ws.Root, testCard)
		rec := recordingAgent()
		e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
		t.Cleanup(func() { e.Close() })

		// Use an interactive stage so no worktree is required.
		f := feature(1, "x", domain.StageBrainstorm)
		if _, err := e.Estimate(context.Background(), f); err != nil {
			t.Fatal(err)
		}
		for _, h := range rec.opts().SystemHints {
			if strings.Contains(h, testCard) {
				t.Errorf("Estimate session received the environment card: %q", h)
			}
		}
	})

	t.Run("CommitMessage", func(t *testing.T) {
		ws, store, wt := newRepo(t)
		writeEnvironmentCard(t, ws.Root, testCard)
		rec := recordingAgent()
		// Produce a parseable fenced block so the draft path completes cleanly.
		rec.Responder = func(_ agent.SessionOpts, _ string) []agent.Event {
			return []agent.Event{
				{Kind: agent.EventMessage, Text: "```gummi-commit\nfeat(ui): x\n\n- reason\n```"},
				{Kind: agent.EventIdle},
			}
		}
		e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
		t.Cleanup(func() { e.Close() })

		f := feature(1, "x", domain.StageImplement)
		withWorktree(t, wt, f)
		if _, err := e.DraftCommitMessage(context.Background(), f); err != nil {
			// Only NewSession failures matter here; guard/parse errors happen
			// after the session opts are already captured.
			t.Fatalf("DraftCommitMessage failed before capturing session opts: %v", err)
		}
		for _, h := range rec.opts().SystemHints {
			if strings.Contains(h, testCard) {
				t.Errorf("DraftCommitMessage session received the environment card: %q", h)
			}
		}
	})
}

func TestEnvironmentCardOversizeTruncates(t *testing.T) {
	ws, store, wt := newRepo(t)
	oversize := strings.Repeat("x", maxEnvironmentCard+100)
	writeEnvironmentCard(t, ws.Root, oversize)

	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	var warnings int32
	e.envWarn = func(msg string) {
		atomic.AddInt32(&warnings, 1)
		e.envMu.Lock()
		e.envNotices = append(e.envNotices, msg)
		e.envMu.Unlock()
	}

	// Repeated calls must still only warn once because the read sits under
	// envOnce.
	for i := 0; i < 3; i++ {
		card := e.environmentCard()
		if len(card) != maxEnvironmentCard {
			t.Errorf("call %d: len(card) = %d, want %d", i, len(card), maxEnvironmentCard)
		}
		if card != strings.Repeat("x", maxEnvironmentCard) {
			t.Errorf("call %d: card is not the expected prefix", i)
		}
	}
	if atomic.LoadInt32(&warnings) != 1 {
		t.Errorf("warnings = %d, want 1", atomic.LoadInt32(&warnings))
	}
}

func TestEnvironmentCardOversizeSurfacesOnNextSession(t *testing.T) {
	ws, store, wt := newRepo(t)
	oversize := strings.Repeat("x", maxEnvironmentCard+100)
	writeEnvironmentCard(t, ws.Root, oversize)

	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	// Trigger the lazy read (and warning) without a session that flushes.
	_ = e.environmentCard()

	f := feature(1, "x", domain.StageSpec)
	s, err := e.Attach(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventIdle)

	acts := s.Snapshot().Activity
	found := false
	for _, a := range acts {
		if strings.Contains(a, "environment card") && strings.Contains(a, "exceeds") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("oversize warning not found in activity feed: %v", acts)
	}
}

func TestEnvironmentCardOversizeSurfacesAfterDiscoverChecks(t *testing.T) {
	ws, store, wt := newRepo(t)
	oversize := strings.Repeat("x", maxEnvironmentCard+100)
	writeEnvironmentCard(t, ws.Root, oversize)

	rec := recordingAgent()
	e := New(Config{Agents: singleAgent(rec), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "discover env", domain.StagePlan)
	withWorktree(t, wt, f)
	p := filepath.Join(wt.Root(), f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(spec.Template(&f)), 0o600); err != nil {
		t.Fatal(err)
	}

	// DiscoverChecks triggers the lazy environment-card read but never
	// flushes notices; the warning must survive to the next live session.
	if _, err := e.DiscoverChecks(context.Background(), f); err != nil {
		t.Fatal(err)
	}

	sf := feature(2, "x", domain.StageSpec)
	s, err := e.Attach(context.Background(), sf)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, e, EventIdle)

	acts := s.Snapshot().Activity
	found := false
	for _, a := range acts {
		if strings.Contains(a, "environment card") && strings.Contains(a, "exceeds") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("oversize warning not found in activity feed after DiscoverChecks: %v", acts)
	}
}
