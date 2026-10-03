package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// TestAScribeFailureIsSaidOnceOnTheCard is the regression for a scribe
// whose model the backend refused: check discovery, the estimate and the
// landing draft all failed on every card, and each swallowed it. The first
// failure now leaves one note on the card naming the model and the fix;
// the next failure on the same card and model adds nothing.
func TestAScribeFailureIsSaidOnceOnTheCard(t *testing.T) {
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, _ string) []agent.Event {
		return []agent.Event{{Kind: agent.EventError, Err: errors.New("There's an issue with the selected model (claude-haiku-4.5)")}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "claude-haiku-4.5"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()
	f := feature(1, "count chars", domain.StageImplement)
	createFeature(t, store, f)
	withWorktree(t, wt, f)

	checks, err := e.DiscoverChecks(ctx, f)
	var sf *ScribeFailure
	if len(checks) != 0 || !errors.As(err, &sf) {
		t.Fatalf("DiscoverChecks = %v, %v; want no checks and a ScribeFailure", checks, err)
	}
	if _, err := e.Estimate(ctx, f); !errors.As(err, &sf) {
		t.Fatalf("Estimate err = %v, want a ScribeFailure", err)
	}

	notes := cardNotes(t, store, f.ID)
	var failed, noChecks int
	for _, n := range notes {
		if strings.Contains(n, "The scribe (claude-haiku-4.5") && strings.Contains(n, "profiles.yaml") {
			failed++
		}
		if strings.HasPrefix(n, NoChecksRow+" — check discovery failed") && strings.Contains(n, NoChecksConsequence) {
			noChecks++
		}
	}
	if failed != 1 {
		t.Errorf("%d scribe-failure notes on the card, want exactly 1:\n%s", failed, strings.Join(notes, "\n"))
	}
	if noChecks != 1 {
		t.Errorf("%d no-checks notes on the card, want exactly 1:\n%s", noChecks, strings.Join(notes, "\n"))
	}
	if !e.ChecksBlockMissing(ctx, f) {
		t.Error("ChecksBlockMissing = false for a card discovery left without a block")
	}
}

// runVerifyWithoutChecks runs a feature's verify over a spec with no
// gummi-checks block, with a scribe that answers discovery with one
// `true` check, and returns the verify session.
func runVerifyWithoutChecks(t *testing.T, priorDiscoveryFailed bool) (*Session, *state.Store) {
	t.Helper()
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, _ string) []agent.Event {
		if opts.Role == agent.RoleScribe {
			return []agent.Event{{Kind: agent.EventMessage, Text: "```gummi-checks\n- name: build\n  cmd: \"true\"\n```"}, {Kind: agent.EventIdle}}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "VERDICT: pass"}, {Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Permission: agent.PermissionAllowAll})
	t.Cleanup(func() { e.Close() })
	f := feature(1, "count chars", domain.StageVerify)
	createFeature(t, store, f)
	withWorktree(t, wt, f)
	p := filepath.Join(wt.Root(), f.WorktreePath(), f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("# FD-001\n\n## Verification plan\n\nrun it\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if priorDiscoveryFailed {
		e.noteNoChecks(context.Background(), f, errors.New("scribe down"))
	}
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, f.ID, StateDone)
	waitActivity(t, e, f.ID, "worktree committed", "checkpoint commit failed", "nothing to commit")
	return e.Get(f.ID), store
}

// TestAVerifyWithNoChecksSaysSo: a verify with no gummi-checks block to
// run writes a "no gummi-checks" row — the row the thread draws as "no
// checks" — instead of saying nothing and letting a pass read as though
// gummi had checked anything.
func TestAVerifyWithNoChecksSaysSo(t *testing.T) {
	s, _ := runVerifyWithoutChecks(t, false)
	acts := strings.Join(s.Snapshot().Activity, "\n")
	if !strings.Contains(acts, NoChecksRow+" — ") || !strings.Contains(acts, NoChecksConsequence) {
		t.Errorf("verify with no checks left no no-checks row:\n%s", acts)
	}
}

// TestVerifyRetriesDiscoveryThatFailedAtApproval: a card whose discovery
// failed at approval gets another try when verify starts, and a block
// found then is run like any other.
func TestVerifyRetriesDiscoveryThatFailedAtApproval(t *testing.T) {
	s, _ := runVerifyWithoutChecks(t, true)
	acts := strings.Join(s.Snapshot().Activity, "\n")
	if !strings.Contains(acts, "retried at verify, found 1 command") || !strings.Contains(acts, "check build: pass") {
		t.Errorf("verify did not retry discovery and run what it found:\n%s", acts)
	}
	if strings.Contains(acts, NoChecksRow+" — ") {
		t.Errorf("a verify that found checks on retry still says it has none:\n%s", acts)
	}
}

// cardNotes returns the content of every system message on a card.
func cardNotes(t *testing.T, store *state.Store, id domain.FeatureID) []string {
	t.Helper()
	evs, err := store.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, ev := range evs {
		if ev.Kind != state.EventMessage {
			continue
		}
		if i := strings.Index(ev.Payload, `"content":"`); i >= 0 {
			s := ev.Payload[i+len(`"content":"`):]
			if j := strings.LastIndex(s, `"`); j >= 0 {
				s = s[:j]
			}
			out = append(out, strings.ReplaceAll(s, `\u0026`, "&"))
		}
	}
	return out
}
