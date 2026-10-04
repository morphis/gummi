package agentplugins

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverPopulatesDescription(t *testing.T) {
	store, _, repo := testStore(t)
	skillDir := filepath.Join(repo, "plugins", "team", "skills", "review")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: review\ndescription: A short description.\n---\n\n# Review\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cands, err := store.Discover()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cands {
		if c.Repo == "griffin" && c.Path == "plugins/team/skills/review/SKILL.md" {
			if c.Description != "A short description." {
				t.Fatalf("description = %q, want %q", c.Description, "A short description.")
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("candidate not found in Discover results: %+v", cands)
	}
}

func TestItemDescriptionDefaultsEmpty(t *testing.T) {
	store, _, repo := testStore(t)
	skillDir := filepath.Join(repo, "plugins", "team", "skills", "nodesc")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# No description\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cands, err := store.Discover()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.Repo == "griffin" && c.Path == "plugins/team/skills/nodesc/SKILL.md" {
			if c.Description != "" {
				t.Fatalf("expected empty description, got %q", c.Description)
			}
			return
		}
	}
	t.Fatalf("candidate not found in Discover results: %+v", cands)
}

func TestDiscoverAtFindsBundledItemsAndImportMany(t *testing.T) {
	store, _, repo := testStore(t)
	bundle := filepath.Join(repo, "bundle")
	if err := os.MkdirAll(filepath.Join(bundle, "skill-a"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bundle, "skill-b"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bundle, "agent-b"), 0o700); err != nil {
		t.Fatal(err)
	}
	// skill-a with description
	if err := os.WriteFile(filepath.Join(bundle, "skill-a", "SKILL.md"), []byte("---\nname: skill-a\ndescription: Alpha skill\n---\n\n# A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// skill-b without description
	if err := os.WriteFile(filepath.Join(bundle, "skill-b", "SKILL.md"), []byte("# B\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// agent file
	if err := os.WriteFile(filepath.Join(bundle, "agent-b", "agent-b.agent.md"), []byte("# Agent B\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cands, err := store.DiscoverAt("bundle", "griffin")
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 3 {
		t.Fatalf("DiscoverAt found %d candidates, want 3: %+v", len(cands), cands)
	}
	// import them
	exported := make([]Candidate, 0, len(cands))
	for _, c := range cands {
		exported = append(exported, c)
	}
	items, err := store.ImportMany(exported)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("ImportMany created %d items, want 3: %+v", len(items), items)
	}
	// check descriptions populated for the one with frontmatter
	foundDesc := false
	for _, it := range items {
		if it.Kind == KindSkill && it.Name == "skill-a" {
			if it.Description != "Alpha skill" {
				t.Fatalf("skill-a description = %q, want %q", it.Description, "Alpha skill")
			}
			foundDesc = true
		}
	}
	if !foundDesc {
		t.Fatalf("did not find imported skill-a with description among items: %+v", items)
	}
}
