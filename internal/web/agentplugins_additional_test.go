package web

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/morphis/gummi/internal/webapi"
)

func TestDiscoverEndpointAcceptsPath(t *testing.T) {
	b := newBoardHarness(t)
	skillDir := filepath.Join(b.root, "plugins", "team", "skills", "review")
	if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Review\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var discovered webapi.AgentPluginDiscover
	status := b.call(http.MethodPost, "/api/plugins/discover", struct {
		Path string `json:"path"`
	}{Path: "plugins/team/skills/review"}, &discovered)
	if status != http.StatusOK {
		t.Fatalf("discover status = %d, want 200", status)
	}
	if len(discovered.Candidates) == 0 {
		t.Fatalf("discover returned no candidates for path scan: %+v", discovered)
	}
}
