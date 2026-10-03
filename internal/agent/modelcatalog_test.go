package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
)

// modelBin writes a fake opencode executable that prints its argv's
// "models" invocation as ids, one per line — the catalog test drives the
// CLI-shaped seam without the real CLI.
func modelBin(t *testing.T, body string) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	path := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestOpencodeModelCatalogParsesItsCLI: the ids are the CLI's own lines,
// verbatim, blanks dropped — `opencode models` prints provider/model pairs
// (and ids that themselves carry slashes), and the catalog forwards them
// untouched.
func TestOpencodeModelCatalogParsesItsCLI(t *testing.T) {
	bin := modelBin(t, "#!/bin/sh\n"+
		`[ "$1" = "models" ] || { echo "unexpected args: $*" >&2; exit 9; }`+"\n"+
		"echo anthropic/claude-sonnet-5-5\n"+
		"echo openrouter/z-ai/glm-5.3\n"+
		"echo\n"+
		"echo   openai/gpt-5.1  \n")
	ids, err := OpencodeModelCatalog(context.Background(), bin)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"anthropic/claude-sonnet-5-5", "openrouter/z-ai/glm-5.3", "openai/gpt-5.1"}
	if !slices.Equal(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}

// TestOpencodeModelCatalogRefusesFailure: a CLI that exits non-zero, and
// a binary that is not there at all, are a failed probe — the catalog is
// the error, never a half-read list.
func TestOpencodeModelCatalogRefusesFailure(t *testing.T) {
	if _, err := OpencodeModelCatalog(context.Background(), filepath.Join(t.TempDir(), "no-such-opencode")); err == nil {
		t.Error("a missing binary cataloged without error")
	}
	bin := modelBin(t, "#!/bin/sh\necho some/model\necho boom >&2\nexit 3\n")
	if _, err := OpencodeModelCatalog(context.Background(), bin); err == nil {
		t.Error("a failing CLI cataloged without error")
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
