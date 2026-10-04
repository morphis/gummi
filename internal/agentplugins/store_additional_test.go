package agentplugins

import (
	"os"
	"path/filepath"
	"strings"
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

func TestDiscoverMarksAlreadyImportedCandidates(t *testing.T) {
	store, _, repo := testStore(t)
	bundle := filepath.Join(repo, "bundle")
	for _, name := range []string{"kept", "fresh"} {
		if err := os.MkdirAll(filepath.Join(bundle, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bundle, name, "SKILL.md"), []byte("# "+name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bundle, "kept.agent.md"), []byte("# Kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cands, err := store.DiscoverAt("bundle", "griffin")
	if err != nil {
		t.Fatal(err)
	}
	var pick []Candidate
	for _, c := range cands {
		if c.Imported {
			t.Fatalf("%s/%s flagged imported before any import", c.Kind, c.Name)
		}
		if c.Name == "kept" {
			pick = append(pick, c)
		}
	}
	if len(pick) != 2 {
		t.Fatalf("want a kept skill and agent, got %+v", pick)
	}
	if _, err := store.ImportMany(pick); err != nil {
		t.Fatal(err)
	}
	for _, scan := range []func() ([]Candidate, error){
		func() ([]Candidate, error) { return store.DiscoverAt("bundle", "griffin") },
		store.Discover,
	} {
		got, err := scan()
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range got {
			if !strings.HasPrefix(c.Path, "bundle/") {
				continue
			}
			if want := c.Name == "kept"; c.Imported != want {
				t.Fatalf("%s/%s imported = %v, want %v", c.Kind, c.Name, c.Imported, want)
			}
		}
	}
}

func TestPickedSkillDirsNarrowsTheLibrary(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".gummi"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := New(root, []Repo{{Name: "default", Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	review, err := store.Create(KindSkill, "Review", "# Review\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(KindSkill, "Deploy", "# Deploy\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(KindAgent, "Helper", "# Helper\n"); err != nil {
		t.Fatal(err)
	}

	all, missing, err := PickedSkillDirs(root, nil)
	if err != nil || len(all) != 2 || len(missing) != 0 {
		t.Fatalf("PickedSkillDirs(nil) = %v, %v, %v; want both skills", all, missing, err)
	}
	dirs, missing, err := PickedSkillDirs(root, []string{review.ID, "skill-gone"})
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || dirs[0] != ItemDir(root, review.ID) {
		t.Fatalf("dirs = %v, want only %s", dirs, ItemDir(root, review.ID))
	}
	if len(missing) != 1 || missing[0] != "skill-gone" {
		t.Fatalf("missing = %v, want [skill-gone]", missing)
	}

	skills, err := Skills(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 || skills[0].Name != "Deploy" || skills[1].Name != "Review" {
		t.Fatalf("Skills = %+v, want Deploy and Review only, by name", skills)
	}
}

func TestPickedSkillDirsWithoutWorkspaceReportsPicksMissing(t *testing.T) {
	dirs, missing, err := PickedSkillDirs(t.TempDir(), []string{"skill-a"})
	if err != nil || len(dirs) != 0 || len(missing) != 1 {
		t.Fatalf("got %v, %v, %v; want no dirs and skill-a missing", dirs, missing, err)
	}
}
