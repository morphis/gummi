package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

// captureNDJSON runs fn with stdout redirected and returns what the
// driver streamed. A refused landing verb reports WHY on the event
// stream — the process error is only the exit status — so a test about
// the sentence has to read the stream the caller reads.
func captureNDJSON(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := fn()
	w.Close()
	os.Stdout = orig
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out), runErr
}

// `gummi handoff` closes a verified card and leaves its branch alone:
// done, unlanded, branch still there. This is the headless half of the
// same ending the board's h key reaches, and the only way to end a card
// from a script without either merging it or destroying its work.
func TestHandOffCommandClosesWithoutLanding(t *testing.T) {
	store, f := verifiedCLIRepo(t)
	before := cliGit(t, ".", "rev-parse", "HEAD")

	if err := runHandOff([]string{string(f.ID)}); err != nil {
		t.Fatalf("runHandOff: %v", err)
	}

	got, err := store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != domain.StageDone {
		t.Fatalf("stage = %s, want done", got.Stage)
	}
	if !got.HandedOff() {
		t.Fatal("no hand-off stamp — a done card with no ending reads as abandoned")
	}
	if head := cliGit(t, ".", "rev-parse", "HEAD"); head != before {
		t.Fatal("hand-off moved main — nothing was supposed to land")
	}
	if out := cliGit(t, ".", "branch", "--list", f.BranchName()); !strings.Contains(out, f.BranchName()) {
		t.Fatalf("branch gone after hand-off: %q", out)
	}
}

// The cobra layer reaches the same verb end-to-end.
func TestHandOffCobra(t *testing.T) {
	store, f := verifiedCLIRepo(t)
	rootCmd.SetArgs([]string{"handoff", string(f.ID)})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute(handoff): %v", err)
	}
	if got, _ := store.GetFeature(context.Background(), f.ID); !got.HandedOff() {
		t.Fatal("cobra handoff did not stamp the ending")
	}
}

// Landing a handed-off card after all is still available for as long as
// the branch is there — and it retracts the hand-off, because the card
// ended on main after all.
func TestMergeAfterHandOffRetractsIt(t *testing.T) {
	store, f := verifiedCLIRepo(t)
	if err := runHandOff([]string{string(f.ID)}); err != nil {
		t.Fatalf("runHandOff: %v", err)
	}
	if err := runMerge([]string{string(f.ID), "-m", "feat(export): land it after all"}); err != nil {
		t.Fatalf("runMerge after hand-off: %v", err)
	}
	got, err := store.GetFeature(context.Background(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.HandedOff() {
		t.Fatal("a landed card still reads as handed off")
	}
	if got.Stage != domain.StageDone {
		t.Fatalf("stage = %s, want done", got.Stage)
	}
	if msg := cliGit(t, ".", "log", "-1", "--format=%s"); msg != "feat(export): land it after all" {
		t.Fatalf("landed subject = %q", msg)
	}
}

// Cleaning a handed-off card would delete the one thing the reader chose
// to keep, so it is refused — and the refusal says so rather than
// reporting a landing that is never coming.
func TestCleanRefusesAHandedOffCard(t *testing.T) {
	_, f := verifiedCLIRepo(t)
	if err := runHandOff([]string{string(f.ID)}); err != nil {
		t.Fatalf("runHandOff: %v", err)
	}
	out, err := captureNDJSON(t, func() error { return runClean([]string{string(f.ID)}) })
	if err == nil {
		t.Fatal("clean accepted a handed-off card")
	}
	if !strings.Contains(out, "handed off, not landed") {
		t.Fatalf("refusal does not name the hand-off: %s", out)
	}
	if !strings.Contains(out, f.BranchName()) {
		t.Fatalf("refusal does not name the branch it would delete: %s", out)
	}
}

// Hand-off ends a card that finished; a card that never reached a
// verified branch has nothing to end.
func TestHandOffCommandRefusesUnverified(t *testing.T) {
	store, f := verifiedCLIRepo(t)
	if _, err := store.Transition(context.Background(), f.ID, domain.StageImplement, "user"); err != nil {
		t.Fatal(err)
	}
	out, err := captureNDJSON(t, func() error { return runHandOff([]string{string(f.ID)}) })
	if err == nil {
		t.Fatal("handoff accepted a card mid-flight")
	}
	if !strings.Contains(out, "not at a verified branch") {
		t.Fatalf("refusal does not say why: %s", out)
	}
}
