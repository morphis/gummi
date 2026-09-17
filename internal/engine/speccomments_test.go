package engine

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// TestWriterKickoffCarriesOpenSpecComments: a comment the human left while
// the plan critique held the card is not sent to the reviewer, so the
// next writer run is where it lands — in its kickoff, read from the
// artifact. The critique's own kickoff never carries it, and a RunWith
// note holding the same compiled list says it once.
func TestWriterKickoffCarriesOpenSpecComments(t *testing.T) {
	var mu sync.Mutex
	var kickoffs []string // one per run: every responder call here opens one
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		kickoffs = append(kickoffs, string(opts.Role)+"\n"+msg)
		mu.Unlock()
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m", MaxActive: 1})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "planned", domain.StagePlan)
	withWorktree(t, wt, f)
	// a plan card's artifact is still a draft
	path := filepath.Join(ws.DraftsDir(), spec.DraftFilename(&f))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := spec.Template(&f) + "\n%% @user: take the version from scripts/openshell-env.sh\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	compiled := CompileSpecComments(spec.Parse(body))
	if compiled == "" {
		t.Fatal("setup: the comment did not compile as open")
	}

	// critique, a bare writer re-run (the replan round), and the spec
	// view's R, whose note is the same compiled list
	if err := e.RunCritique(f, ""); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)
	for _, note := range []string{"", compiled} {
		if err := e.RunWith(f, note); err != nil {
			t.Fatal(err)
		}
		waitState(t, e, "FD-001", StateDone)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(kickoffs) != 3 {
		t.Fatalf("want 3 kickoffs, got %d: %q", len(kickoffs), kickoffs)
	}
	if got := kickoffs[0]; !strings.HasPrefix(got, string(agent.RoleReviewer)) || strings.Contains(got, "openshell-env.sh") {
		t.Errorf("the critique's kickoff carried the writer's comments:\n%s", got)
	}
	for _, got := range kickoffs[1:] {
		if n := strings.Count(got, "openshell-env.sh"); !strings.HasPrefix(got, string(agent.RoleArchitect)) || n != 1 {
			t.Errorf("writer kickoff names the comment %d times, want once:\n%s", n, got)
		}
	}
}
