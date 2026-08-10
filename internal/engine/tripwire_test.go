package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

// tripRig is a single-slot engine on a fresh repo whose Fake's responder
// is scripted per test to write into the main checkout.
type tripRig struct {
	t    *testing.T
	e    *Engine
	wt   *worktree.Manager
	ag   *agent.Fake
	root string
}

func newTripRig(t *testing.T) *tripRig {
	t.Helper()
	ag := agent.NewFake("ack")
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })
	return &tripRig{t: t, e: e, wt: wt, ag: ag, root: wt.Root()}
}

// write makes the fake's responder create rel paths under the main root
// before idling its turn.
func (r *tripRig) write(rels ...string) {
	r.ag.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		for _, rel := range rels {
			writeAt(r.t, r.root, rel)
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "done"}, {Kind: agent.EventIdle}}
	}
}

// gitOut runs git in the main checkout, failing the test on error and
// returning stdout.
func gitOut(t *testing.T, root string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "git",
		append([]string{"-C", root}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// writeAt creates rel under root (creating dirs) with an optional body.
func writeAt(t *testing.T, root, rel string, body ...string) {
	t.Helper()
	content := "x\n"
	if len(body) > 0 {
		content = body[0]
	}
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestTripNewTrackedWrite reports the path and leaves it on disk,
// without reverting the agent's write.
func TestTripNewTrackedWrite(t *testing.T) {
	r := newTripRig(t)
	r.write("cmd/gummi/main.go")
	if _, err := r.e.Attach(context.Background(), feature(1, "Dark mode", domain.StageBrainstorm)); err != nil {
		t.Fatal(err)
	}
	ev := waitFor(t, r.e, EventTripwire)
	if want := []string{"cmd/gummi/main.go"}; !reflect.DeepEqual(ev.DirtyPaths, want) {
		t.Fatalf("DirtyPaths = %v, want %v", ev.DirtyPaths, want)
	}
	// no auto-revert: git still sees the file dirty (all-mode so the
	// nested path isn't collapsed to its directory)
	if out := gitOut(t, r.root, "status", "--porcelain", "--untracked-files=all"); !strings.Contains(out, "cmd/gummi/main.go") {
		t.Fatalf("agent write was reverted? status:\n%s", out)
	}
}

// TestTripMultiPathSorted reports both new paths, sorted, in one event.
func TestTripMultiPathSorted(t *testing.T) {
	r := newTripRig(t)
	r.write("b.go", "a.go")
	if _, err := r.e.Attach(context.Background(), feature(1, "Dark mode", domain.StageBrainstorm)); err != nil {
		t.Fatal(err)
	}
	ev := waitFor(t, r.e, EventTripwire)
	if want := []string{"a.go", "b.go"}; !reflect.DeepEqual(ev.DirtyPaths, want) {
		t.Fatalf("DirtyPaths = %v, want %v", ev.DirtyPaths, want)
	}
}

// TestTripGummiInvisible ignores writes under .gummi/.
func TestTripGummiInvisible(t *testing.T) {
	r := newTripRig(t)
	r.write(".gummi/scratch.txt")
	if _, err := r.e.Attach(context.Background(), feature(1, "Dark mode", domain.StageBrainstorm)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, r.e, EventIdle) // completes normally; a trip would emit EventTripwire instead
}

// TestTripPreexistingDirtyNoTrip: dirt present before the turn, with the
// same path edited during it, does not trip.
func TestTripPreexistingDirtyNoTrip(t *testing.T) {
	r := newTripRig(t)
	writeAt(t, r.root, "README.md", "operator dirty\n") // operator dirt present before the turn
	r.write("README.md")                                // agent re-edits the same path during the turn
	if _, err := r.e.Attach(context.Background(), feature(1, "Dark mode", domain.StageBrainstorm)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, r.e, EventIdle)
}

// TestTripNewDirtOnDirty: a new path on top of a pre-dirited one trips
// naming only the new path.
func TestTripNewDirtOnDirty(t *testing.T) {
	r := newTripRig(t)
	writeAt(t, r.root, "README.md", "operator dirty\n") // operator dirt
	r.write("NEWFILE.md")
	if _, err := r.e.Attach(context.Background(), feature(1, "Dark mode", domain.StageBrainstorm)); err != nil {
		t.Fatal(err)
	}
	ev := waitFor(t, r.e, EventTripwire)
	if want := []string{"NEWFILE.md"}; !reflect.DeepEqual(ev.DirtyPaths, want) {
		t.Fatalf("DirtyPaths = %v, want %v", ev.DirtyPaths, want)
	}
}

// TestTripGitignoreQuiet: a path matching .gitignore is absent from both
// snapshots and does not trip.
func TestTripGitignoreQuiet(t *testing.T) {
	r := newTripRig(t)
	writeAt(t, r.root, ".gitignore", "*.tmp\n")
	gitOut(t, r.root, "add", "-A")
	gitOut(t, r.root, "commit", "-qm", "add gitignore")
	r.write("build.tmp")
	if _, err := r.e.Attach(context.Background(), feature(1, "Dark mode", domain.StageBrainstorm)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, r.e, EventIdle)
}

// TestTripSessionHalted: after the trip, the engine refuses a further
// dispatch and the fake session records no additional send — the run is
// dead, not paused-and-resumable.
func TestTripSessionHalted(t *testing.T) {
	r := newTripRig(t)
	r.write("cmd/gummi/main.go")
	if _, err := r.e.Attach(context.Background(), feature(1, "Dark mode", domain.StageBrainstorm)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, r.e, EventTripwire)

	if err := r.e.Send(context.Background(), "FD-001", "another turn"); err == nil {
		t.Fatal("Send after trip succeeded, want error (session should be dead)")
	}
	// the underlying fake session must not have accepted the follow-up
	// turn: stop() closed it, so Send errors before incrementing.
	if s := r.e.Get("FD-001"); s != nil {
		if f, ok := s.agent().(interface{ SendCount() int }); ok && f.SendCount() != 1 {
			t.Fatalf("send count = %d, want 1 (the trip killed the session's only turn)", f.SendCount())
		}
	}
}

// TestTripPreTurnErrorDoesNotSpuriouslyTrip: a pre-turn snapshot failure
// (fault injected on call 1 only) skips the trip decision rather than
// misattributing the operator's pre-existing dirt to the agent; the
// post-turn call succeeds and returns the operator's path, so a regressed
// arm that defaulted the missing pre-set to {} would fail this.
func TestTripPreTurnErrorDoesNotSpuriouslyTrip(t *testing.T) {
	r := newTripRig(t)
	writeAt(t, r.root, "README.md", "operator dirty\n") // operator dirt

	calls := 0
	real := r.wt.MainDirtyPaths
	r.e.dirtyPathsFn = func(ctx context.Context) ([]string, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("injected fault")
		}
		return real(ctx)
	}

	if _, err := r.e.Attach(context.Background(), feature(1, "Dark mode", domain.StageBrainstorm)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, r.e, EventIdle) // no trip; EventTripwire would have replaced it
	if calls != 1 {
		t.Fatalf("dirtyPathsFn called %d times, want 1: the pre-turn failure skipped beginTurn, so checkTrip short-circuits on the unset pre-set and never calls it again", calls)
	}
	if out := gitOut(t, r.root, "status", "--porcelain"); !strings.Contains(out, "README.md") {
		t.Fatal("operator's README.md dirt vanished")
	}
}

// TestCloseCancelsInFlightTripSnapshot locks the teardown race: the
// post-turn trip snapshot (a git status that can run past a finished
// test, racing t.TempDir cleanup) must be bound to the session's
// lifecycle, so finalizing the engine cancels an in-flight snapshot
// rather than letting it outlive Close. Fails without the session-scoped
// context because the snapshot then holds a background context that
// cancellation can never reach.
func TestCloseCancelsInFlightTripSnapshot(t *testing.T) {
	r := newTripRig(t)
	entered := make(chan struct{})
	released := make(chan struct{})
	calls := 0
	// a snapshot that stands in for a git subprocess still walking the
	// worktree: the post-turn one blocks until the session context is
	// canceled. The pre-turn snapshot (call 1) returns immediately so the
	// turn can actually run and reach the post-turn check.
	r.e.dirtyPathsFn = func(ctx context.Context) ([]string, error) {
		calls++
		if calls == 1 {
			return nil, nil
		}
		close(entered)
		<-ctx.Done()
		close(released)
		return nil, ctx.Err()
	}
	r.ag.Responder = func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{{Kind: agent.EventMessage, Text: "done"}, {Kind: agent.EventIdle}}
	}
	if _, err := r.e.Attach(context.Background(), feature(1, "Dark mode", domain.StageBrainstorm)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("post-turn trip snapshot never entered")
	}
	// finalize the engine while the snapshot is in flight: it must be
	// canceled, so no git subprocess survives Close to race teardown.
	r.e.Close()
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight trip snapshot was not canceled by Close")
	}
}
