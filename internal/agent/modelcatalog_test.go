package agent

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
)

// TestOpencodeModelCatalogRefusesFailure: a binary that is not there at
// all is a failed probe — the catalog is the error, never a half-read
// list. (The probe's own spawn/answer path is covered by
// TestOpencodeServerModelCatalog through the spawn seam; a spawn that
// fails is the same "no catalog" answer.)
func TestOpencodeModelCatalogRefusesFailure(t *testing.T) {
	if _, err := OpencodeModelCatalog(context.Background(), filepath.Join(t.TempDir(), "no-such-opencode")); err == nil {
		t.Error("a missing binary cataloged without error")
	}
	// the seam is rebindable — the engine's no-adapter probe path points
	// it at its own answer without spawning anything
	old := OpencodeModelCatalog
	t.Cleanup(func() { OpencodeModelCatalog = old })
	OpencodeModelCatalog = func(context.Context, string) ([]string, error) {
		return nil, errors.New("the probe failed")
	}
	if _, err := OpencodeModelCatalog(context.Background(), "opencode"); err == nil || !strings.Contains(err.Error(), "the probe failed") {
		t.Errorf("a rebound probe's failure = %v, want the probe's own error", err)
	}
}

// TestCopilotModelCatalog: the ids are the CLI's model list's own, sorted
// for a stable picker.
func TestCopilotModelCatalog(t *testing.T) {
	c := visionModelClient(map[string]bool{"z-model": false, "a-model": true})
	ids, err := c.ModelCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []string{"a-model", "z-model"}) {
		t.Errorf("ids = %v, want [a-model z-model]", ids)
	}
}

// TestCopilotModelCatalogReportsFailure: a CLI whose model list fails is
// no catalog — the picker keeps its suggestions and typed entry.
func TestCopilotModelCatalogReportsFailure(t *testing.T) {
	client := copilot.NewClient(&copilot.ClientOptions{
		OnListModels: func(context.Context) ([]copilot.ModelInfo, error) {
			return nil, errors.New("CLI is not connected")
		},
	})
	c := &Copilot{client: client}
	if _, err := c.ModelCatalog(context.Background()); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Errorf("err = %v, want the CLI's own failure", err)
	}
}

// TestFakeModelCatalogStagesItsModels: the fake's Models answer verbatim,
// and a fake with none stages a backend that cannot enumerate.
func TestFakeModelCatalogStagesItsModels(t *testing.T) {
	f := &Fake{Models: []string{"fake-large", "fake-small"}}
	ids, err := f.ModelCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []string{"fake-large", "fake-small"}) {
		t.Errorf("ids = %v, want the staged ones", ids)
	}
	if ids, _ := (&Fake{}).ModelCatalog(context.Background()); ids != nil {
		t.Errorf("an empty fake cataloged %v, want none", ids)
	}
}
