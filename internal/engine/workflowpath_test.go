package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestARecipeIsProposedFromTheSessionsTree: recipe_path reads a recipe
// file in the session's own tree and freezes it into the run; a path
// leading out of the tree, a link out of it and a second recipe beside it
// are refused.
func TestARecipeIsProposedFromTheSessionsTree(t *testing.T) {
	wf := newWFFake()
	e, store, f, _ := wfEngine(t, wf)
	ctx := context.Background()
	sess, _ := store.GetFeature(ctx, f.ID)
	tree := e.treeOf(ctx, sess)
	recipe := "name: mine\nsteps:\n  - {id: write, role: implementer, prompt: \"Do it.\\n\\n{{spec}}\"}\n"
	if err := os.MkdirAll(filepath.Join(tree, "recipes"), 0o750); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(tree, "recipes", "mine.yaml")
	if err := os.WriteFile(file, []byte(recipe), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "out.yaml")
	if err := os.WriteFile(outside, []byte(recipe), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tree, "link.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		req  WorkflowRequest
		want string
	}{
		{WorkflowRequest{RecipePath: "../x.yaml"}, "inside the session's tree"},
		{WorkflowRequest{RecipePath: outside}, "inside the session's tree"},
		{WorkflowRequest{RecipePath: "link.yaml"}, "leads outside"},
		{WorkflowRequest{RecipePath: "recipes"}, "not a file"},
		{WorkflowRequest{RecipePath: "nope.yaml"}, "nope.yaml"},
		{WorkflowRequest{RecipePath: "recipes/mine.yaml", Recipe: "deliver"}, "one of recipe"},
	} {
		if _, err := e.WorkflowPropose(ctx, f.ID, tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v, want %q", tc.req, err, tc.want)
		}
	}

	run, err := e.WorkflowPropose(ctx, f.ID, WorkflowRequest{RecipePath: "recipes/mine.yaml", Spec: 1})
	if err != nil {
		t.Fatal(err)
	}
	if run.RecipeYAML != recipe || len(run.Recipe.Steps) != 1 {
		t.Fatalf("the run froze %q", run.RecipeYAML)
	}
	if err := os.WriteFile(file, []byte("name: changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, _ := store.Run(ctx, f.ID, run.N); r == nil || r.RecipeYAML != recipe {
		t.Errorf("an edit of the file reached the proposed run: %+v", r)
	}
}
