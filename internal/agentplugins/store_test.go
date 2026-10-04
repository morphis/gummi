package agentplugins

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".gummi"), 0o700); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(workspace, "git", "griffin")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := New(workspace, []Repo{{Name: "griffin", Root: repo}})
	if err != nil {
		t.Fatal(err)
	}
	return store, workspace, repo
}

// Every managed skill is globally available — SkillDirs returns all of
// them regardless of which repository a card is working in.
func TestCreateSkillsAreGloballyAvailable(t *testing.T) {
	store, workspace, _ := testStore(t)
	global, err := store.Create(KindSkill, "Code Reviewer", "# review\n")
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.Create(KindSkill, "Griffin", "# griffin\n")
	if err != nil {
		t.Fatal(err)
	}

	got, err := SkillDirs(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != filepath.Join(workspace, ".gummi", "agent-plugins", "items", global.ID) ||
		got[1] != filepath.Join(workspace, ".gummi", "agent-plugins", "items", project.ID) {
		t.Fatalf("SkillDirs() = %v", got)
	}
}

func TestImportLinksAndEditCopiesWithoutChangingSource(t *testing.T) {
	store, _, repo := testStore(t)
	source := filepath.Join(repo, ".github", "skills", "style")
	if err := os.MkdirAll(filepath.Join(source, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("# Original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "references", "rules.md"), []byte("Keep this file.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	item, err := store.Import(KindSkill, ".github/skills/style/SKILL.md", "griffin")
	if err != nil {
		t.Fatal(err)
	}
	if !item.Linked || item.SourcePath != "git/griffin/.github/skills/style/SKILL.md" {
		t.Fatalf("imported item = %+v", item)
	}
	if info, err := os.Lstat(store.itemPath(item.ID)); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("import was not linked: info=%v err=%v", info, err)
	}
	if _, _, err := store.Export(item.ID); err != nil {
		t.Fatalf("export linked item: %v", err)
	}
	updated, err := store.Update(item.ID, item.Name, "# Edited\n")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Linked {
		t.Fatal("edited item still reports linked")
	}
	if info, err := os.Lstat(store.itemPath(item.ID)); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("edit did not detach link: info=%v err=%v", info, err)
	}
	if got, err := os.ReadFile(filepath.Join(source, "SKILL.md")); err != nil || string(got) != "# Original\n" {
		t.Fatalf("source skill changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(store.itemPath(item.ID), "references", "rules.md")); err != nil || string(got) != "Keep this file.\n" {
		t.Fatalf("supporting file was not preserved: %q, %v", got, err)
	}
}

func TestDiscoverFindsSkillsAndAgentDefinitions(t *testing.T) {
	store, _, repo := testStore(t)
	files := map[string]string{
		".agents/skills/build/SKILL.md":                      "# Build\n",
		"plugins/tarek/skills/review/SKILL.md":               "# Review\n",
		"plugins/tarek/agents/reviewer.agent.md":             "# Reviewer\n",
		"plugins/tarek/agents/AGENTS.md":                     "# Agent instructions\n",
		"AGENTS.md":                                          "# Repo guidance\n",
		".gummi/agent-plugins/ignore/SKILL.md":               "# Ignore generated state\n",
		"plugins/tarek/skills/.git/hidden/SKILL.md":          "# Ignore git metadata\n",
		"plugins/tarek/skills/vendor/copied/SKILL.md":        "# Ignore vendor\n",
		"plugins/tarek/agents/node_modules/x/agent.agent.md": "# Ignore node modules\n",
	}
	for name, content := range files {
		path := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("Discover() found %d candidates: %+v", len(got), got)
	}
	for _, c := range got {
		if c.Repo != "griffin" || filepath.IsAbs(c.Path) {
			t.Errorf("candidate path is not repo-relative: %+v", c)
		}
	}
}

func TestImportRejectsPathsOutsideWorkspace(t *testing.T) {
	store, _, _ := testStore(t)
	outside := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(outside, []byte("# secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Import(KindSkill, outside, ""); err == nil {
		t.Fatal("import outside the workspace succeeded")
	}
}

func TestAgentImportCopiesOnlySelectedFile(t *testing.T) {
	store, _, repo := testStore(t)
	source := filepath.Join(repo, "AGENTS.md")
	if err := os.WriteFile(source, []byte("# Repository guidance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "unrelated.go"), []byte("package example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	item, err := store.Import(KindAgent, "AGENTS.md", "griffin")
	if err != nil {
		t.Fatal(err)
	}
	if item.Linked {
		t.Fatal("agent file import should be a workspace copy")
	}
	entries, err := os.ReadDir(store.itemPath(item.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "AGENTS.md" {
		t.Fatalf("imported agent directory contains %v", entries)
	}
	if got, err := os.ReadFile(filepath.Join(store.itemPath(item.ID), "AGENTS.md")); err != nil || string(got) != "# Repository guidance\n" {
		t.Fatalf("imported agent content = %q, %v", got, err)
	}
}

func TestRejectsSymlinkedLibraryAndItemsDirectories(t *testing.T) {
	store, workspace, _ := testStore(t)
	if err := os.MkdirAll(store.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Remove(store.dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, store.dir); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err == nil {
		t.Fatal("listed items through a symlinked library directory")
	}
	if err := os.Remove(store.dir); err != nil {
		t.Fatal(err)
	}

	item, err := store.Create(KindSkill, "external", "# External\n")
	if err != nil {
		t.Fatal(err)
	}
	itemsBackup := store.itemsDir + ".backup"
	if err := os.Rename(store.itemsDir, itemsBackup); err != nil {
		t.Fatal(err)
	}
	externalItems := t.TempDir()
	externalItem := filepath.Join(externalItems, item.ID)
	if err := os.Mkdir(externalItem, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(externalItem, "SKILL.md"), []byte("# External\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalItems, store.itemsDir); err != nil {
		t.Fatal(err)
	}
	if _, err := SkillDirs(workspace); err == nil {
		t.Fatal("forwarded a skill through a symlinked items directory")
	}
}

func TestExportContainsItemAndSupportingFiles(t *testing.T) {
	store, _, repo := testStore(t)
	source := filepath.Join(repo, "plugins", "one", "skills", "trace")
	if err := os.MkdirAll(filepath.Join(source, "references"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"SKILL.md":             "# Trace\n",
		"references/detail.md": "Details\n",
	} {
		if err := os.WriteFile(filepath.Join(source, filepath.FromSlash(name)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	item, err := store.Import(KindSkill, filepath.Join(source, "SKILL.md"), "griffin")
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := store.Export(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() {
			files++
		}
	}
	if files != 2 {
		t.Fatalf("zip contains %d files, want 2", files)
	}
}

func TestImportWorkspaceRelativeCandidateInMultiRepoWorkspace(t *testing.T) {
	// Reproduces the multi-repo workspace shape where a "default" repo is
	// not configured; ensure a workspace-relative discovery candidate (repo
	// == "default" label) can be imported when the Store was created with
	// only named repositories.
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".gummi"), 0o700); err != nil {
		t.Fatal(err)
	}
	repoA := filepath.Join(workspace, "git", "repoA")
	if err := os.MkdirAll(filepath.Join(repoA, ".github", "skills", "demo"), 0o700); err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(repoA, ".github", "skills", "demo", "SKILL.md")
	if err := os.WriteFile(skill, []byte("# Demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(workspace, []Repo{{Name: "repoA", Root: repoA}})
	if err != nil {
		t.Fatal(err)
	}
	cands, err := store.DiscoverAt("git/repoA/.github/skills/demo/SKILL.md", "")
	if err != nil {
		t.Fatalf("discover failed: %v", err)
	}
	if len(cands) == 0 {
		t.Fatalf("no candidates discovered")
	}
	if _, err := store.ImportMany(cands); err != nil {
		t.Fatalf("import failed: %v", err)
	}
}
