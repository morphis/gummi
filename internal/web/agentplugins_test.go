package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/morphis/gummi/internal/agentplugins"
	"github.com/morphis/gummi/internal/webapi"
)

func TestAgentPluginCRUDAndProviderCapabilities(t *testing.T) {
	b := newBoardHarness(t)
	var created agentplugins.Item
	status := b.call(http.MethodPost, "/api/plugins", webapi.AgentPluginCreateRequest{
		Kind: agentplugins.KindSkill, Name: "Code Reviewer", Content: "# Review\n",
	}, &created)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, item = %+v", status, created)
	}
	var detail webapi.AgentPluginDetail
	if status := b.call(http.MethodGet, "/api/plugins/"+created.ID, nil, &detail); status != http.StatusOK {
		t.Fatalf("get status = %d", status)
	}
	if detail.Content != "# Review\n" {
		t.Fatalf("content = %q", detail.Content)
	}

	var summary webapi.AgentPlugins
	if status := b.call(http.MethodGet, "/api/plugins", nil, &summary); status != http.StatusOK {
		t.Fatalf("list status = %d", status)
	}
	providers := map[string]bool{}
	for _, p := range summary.Providers {
		providers[p.Name] = p.SkillDirs
	}
	if !providers["copilot"] || !providers["claude"] || !providers["opencode"] {
		t.Errorf("supported backends not reported: %+v", summary.Providers)
	}
	if providers["codex"] || providers["pi"] || providers["headless"] {
		t.Errorf("unsupported backends reported skill forwarding: %+v", summary.Providers)
	}

	status = b.call(http.MethodPut, "/api/plugins/"+created.ID, webapi.AgentPluginUpdateRequest{
		Name: "Code Reviewer", Content: "# Updated\n",
	}, &created)
	if status != http.StatusOK {
		t.Fatalf("update status=%d item=%+v", status, created)
	}
	if status := b.call(http.MethodDelete, "/api/plugins/"+created.ID, nil, nil); status != http.StatusOK {
		t.Fatalf("delete status = %d", status)
	}
	if status := b.call(http.MethodGet, "/api/plugins/"+created.ID, nil, &detail); status != http.StatusNotFound {
		t.Fatalf("get deleted item status = %d, want 404", status)
	}
}

func TestAgentPluginDiscoveryImportAndExport(t *testing.T) {
	b := newBoardHarness(t)
	skillDir := filepath.Join(b.root, "plugins", "team", "skills", "review")
	agentDir := filepath.Join(b.root, "plugins", "team", "agents")
	for _, path := range []string{
		filepath.Join(skillDir, "references"),
		agentDir,
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		filepath.Join(skillDir, "SKILL.md"):               "# Review\n",
		filepath.Join(skillDir, "references", "guide.md"): "Guide\n",
		filepath.Join(agentDir, "reviewer.agent.md"):      "# Reviewer\n",
	} {
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var discovered webapi.AgentPluginDiscover
	if status := b.call(http.MethodPost, "/api/plugins/discover", struct{}{}, &discovered); status != http.StatusOK {
		t.Fatalf("discover status = %d", status)
	}
	if len(discovered.Candidates) != 2 {
		t.Fatalf("discovered %+v, want the skill and agent", discovered.Candidates)
	}
	var imported struct {
		Items []agentplugins.Item `json:"items"`
	}
	if status := b.call(http.MethodPost, "/api/plugins/import", webapi.AgentPluginImportRequest{
		Sources: discovered.Candidates,
	}, &imported); status != http.StatusCreated {
		t.Fatalf("import status = %d, response = %+v", status, imported)
	}
	if len(imported.Items) != 2 {
		t.Fatalf("imported %+v", imported.Items)
	}
	var skill agentplugins.Item
	for _, item := range imported.Items {
		if item.Kind == agentplugins.KindSkill {
			skill = item
		}
	}
	if skill.ID == "" {
		t.Fatal("skill item missing from import response")
	}
	res, err := b.c.Get(b.http.URL + "/api/plugins/" + skill.ID + "/export")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("export response = %d, %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	var raw bytes.Buffer
	if _, err := raw.ReadFrom(res.Body); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw.Bytes()), int64(raw.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]bool{}
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() {
			files[f.Name] = true
		}
	}
	if len(files) != 2 {
		t.Fatalf("exported files = %v", files)
	}
}

// An import whose source path does not exist is rejected with a 400; there
// is no availability scope to validate anymore, since every item is
// globally available once imported.
func TestAgentPluginImportRejectsMissingPath(t *testing.T) {
	b := newBoardHarness(t)
	res := b.call(http.MethodPost, "/api/plugins/import", webapi.AgentPluginImportRequest{
		Sources: []agentplugins.Candidate{{Kind: agentplugins.KindSkill, Repo: "default", Path: "missing/SKILL.md"}},
	}, nil)
	if res != http.StatusBadRequest {
		t.Fatalf("missing import path status = %d, want 400", res)
	}
}

func TestAgentPluginResponseShape(t *testing.T) {
	body, err := json.Marshal(webapi.AgentPlugins{
		Items:     []agentplugins.Item{{ID: "skill-review", Kind: agentplugins.KindSkill, Name: "Review", Entry: "SKILL.md"}},
		Providers: []webapi.AgentPluginProvider{{Name: "copilot", SkillDirs: true, SkillDetail: "Enabled skills are available to new sessions."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"items", "providers"} {
		if _, ok := got[key]; !ok {
			t.Errorf("response missing %q: %s", key, body)
		}
	}
}
