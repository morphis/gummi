package engine

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

// writeSpecBody drops an artifact into the feature's worktree, where the
// work stages read it.
func writeSpecBody(t *testing.T, wt *worktree.Manager, f domain.Feature, body string) {
	t.Helper()
	p := filepath.Join(wt.Root(), f.WorktreePath(), f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// implementKickoff runs one implement stage against the given artifact
// body and returns the kickoff the agent was handed.
func implementKickoff(t *testing.T, body string) string {
	t.Helper()
	ws, store, wt := newRepo(t)
	var mu sync.Mutex
	var got string
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		mu.Lock()
		got = msg
		mu.Unlock()
		return []agent.Event{{Kind: agent.EventMessage, Text: "done"}, {Kind: agent.EventIdle}}
	}}
	e := New(Config{
		Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws,
		Model: "m", MaxActive: 1, Permission: agent.PermissionAllowAll,
	})
	t.Cleanup(func() { e.Close() })

	f := feature(1, "build it", domain.StageImplement)
	withWorktree(t, wt, f)
	writeSpecBody(t, wt, f, body)
	if err := e.Run(f); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, "FD-001", StateDone)
	mu.Lock()
	defer mu.Unlock()
	return got
}

const manifestSpec = `# FD-001

## Implementation notes

1. do the thing

` + "```gummi-files" + `
- path: internal/engine/engine.go
  role: locate resolves the working directory
- path: internal/worktree/scratch.go
  role: the scratch tree itself
  new: true
` + "```" + `
`

// TestImplementKickoffCarriesFileManifest: the implementer was measured
// making its first edit at turn 32 of 97 — later than a bare agent given
// no spec at all — because the plan's file-level knowledge was prose it
// re-derived from the repo. It is handed the list now.
func TestImplementKickoffCarriesFileManifest(t *testing.T) {
	got := implementKickoff(t, manifestSpec)
	for _, want := range []string{
		"The plan's file manifest",
		"internal/engine/engine.go",
		"locate resolves the working directory",
		"internal/worktree/scratch.go",
		"(new file)", // so nobody goes looking for a file that isn't there yet
	} {
		if !strings.Contains(got, want) {
			t.Errorf("implement kickoff missing %q:\n%s", want, got)
		}
	}
	// A wrong or incomplete manifest is worse than none if it reads as
	// exhaustive, so the kickoff must say what to do in BOTH failure
	// directions rather than leaving it emergent.
	for _, want := range []string{
		"not the whole boundary",
		"change it and add a line to Progress",
		"do not invent work to justify an entry",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("implement kickoff missing the manifest's escape clause %q:\n%s", want, got)
		}
	}
}

// No manifest, an empty one, and a malformed one all read the same way:
// no preamble, and the stage works as it did before. A half-parsed list
// is the one outcome worth avoiding — it would be trusted.
func TestImplementKickoffWithoutUsableManifest(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"absent", "# FD-001\n\n## Implementation notes\n\n1. do the thing\n"},
		{"empty", "# FD-001\n\n```gummi-files\n[]\n```\n"},
		{"malformed", "# FD-001\n\n```gummi-files\n- path: [unclosed\n```\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := implementKickoff(t, tc.body); strings.Contains(got, "The plan's file manifest") {
				t.Errorf("kickoff carried a manifest it should not have:\n%s", got)
			}
		})
	}
}

// The design stage states the manifest's shape for every kind, and the
// work stage that acts on one knows it may arrive — one constant, so
// they cannot drift apart.
func TestPlanningStagesRequireTheFileManifest(t *testing.T) {
	for _, tc := range []struct {
		stage domain.Stage
		kind  domain.Kind
	}{
		{domain.StagePlan, domain.KindFeature},
		{domain.StagePlan, domain.KindBug},
	} {
		f := feature(1, "x", tc.stage)
		f.Kind = tc.kind
		h := unwrap(strings.Join(stageHints(f, "spec.md", flavorStage), "\n"))
		if !strings.Contains(h, unwrap("```gummi-files")) {
			t.Errorf("%s/%s hint does not ask for a file manifest", tc.stage, tc.kind)
		}
	}
	for _, kind := range []domain.Kind{domain.KindFeature, domain.KindBug} {
		f := feature(1, "x", domain.StageImplement)
		f.Kind = kind
		h := unwrap(strings.Join(stageHints(f, "spec.md", flavorStage), "\n"))
		if !strings.Contains(h, "file manifest") {
			t.Errorf("implement/%s hint never mentions the manifest it may be handed", kind)
		}
		if !strings.Contains(h, "starting point, not a boundary") {
			t.Errorf("implement/%s hint does not say the manifest is not exhaustive", kind)
		}
	}
}
