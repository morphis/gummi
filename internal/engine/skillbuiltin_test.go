package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/skillpack"
)

// packDirsFor is skillDirsFor without gummi's own skills: the pack's
// directories alone, nil where skillDirsFor gives nil.
func packDirsFor(ctx context.Context, e *Engine, ag agent.Agent, f *domain.Feature, key string, continuing bool) []string {
	all := e.skillDirsFor(ctx, ag, f, key, continuing)
	if all == nil {
		return nil
	}
	out := []string{}
	for _, d := range all {
		if !strings.Contains(d, string(filepath.Separator)+"skillbuiltin"+string(filepath.Separator)) {
			out = append(out, d)
		}
	}
	return out
}

// TestEverySessionGetsGummisOwnSkills: gummi's own skills reach a session
// on every backend that takes skills, first, under the pack's names, with
// no pack at all; the hints name them as the backend does.
func TestEverySessionGetsGummisOwnSkills(t *testing.T) {
	ctx := context.Background()
	cf := newCuratorFake(nil)
	e := curatorEngine(t, cf, nil)
	f := &domain.Feature{ID: "FF-1"}

	got := e.skillDirsFor(ctx, cf, f, "FF-1", false)
	if names := baseNames(got); names != "gummi-publishing-prs,gummi-workflow-recipes,gummi-workflows" {
		t.Fatalf("a session with no pack gets %q", names)
	}
	if _, err := os.Stat(filepath.Join(got[1], "references", "deliver.yaml")); err != nil {
		t.Errorf("the recipe skill lacks the bundled deliver: %v", err)
	}

	namespaced := agent.NewFake("")
	namespaced.Caps = agent.Capabilities{SkillDirs: true, SkillNamespace: true}
	if names := baseNames(e.skillDirsFor(ctx, namespaced, f, "FF-1", false)); names != "publishing-prs,workflow-recipes,workflows" {
		t.Errorf("a backend that namespaces skills itself gets %q", names)
	}
	noSkills := agent.NewFake("")
	noSkills.Caps = agent.Capabilities{}
	if got := e.skillDirsFor(ctx, noSkills, f, "FF-1", false); got != nil {
		t.Errorf("a backend without skill directories got %v", got)
	}

	if !strings.Contains(sessionWorkflowHintFor(cf), "the gummi-workflows skill") ||
		!strings.Contains(sessionWorkflowHintFor(namespaced), "gummi-skills:workflow-recipes") ||
		sessionWorkflowHintFor(noSkills) != sessionWorkflowHint {
		t.Errorf("the hint names the skills wrongly:\n%s", sessionWorkflowHintFor(cf))
	}
	for _, n := range skillpack.BuiltinNames() {
		if !strings.Contains(skillStageHintFor(true), n) {
			t.Errorf("the staging hint does not say %s is gummi's own", n)
		}
	}
}
