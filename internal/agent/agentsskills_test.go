package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Repo skills come before the operator's, so a repo skill wins a name
// collision in the first-wins materializers; a directory without a
// SKILL.md is not a skill.
func TestAgentsSkillDirsRepoThenUser(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	repo := skillDirAt(t, filepath.Join(wt, ".agents", "skills"), "shared")
	user := skillDirAt(t, filepath.Join(home, ".agents", "skills"), "shared")
	if err := os.MkdirAll(filepath.Join(home, ".agents", "skills", "not-a-skill"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := agentsSkillDirs(wt)
	if want := []string{repo, user}; !slices.Equal(got, want) {
		t.Errorf("agentsSkillDirs = %v, want %v", got, want)
	}
	if got := withAgentsSkills([]string{"/fwd/x"}, wt); len(got) != 3 || got[0] != "/fwd/x" {
		t.Errorf("forwarded skills must lead: %v", got)
	}
}

// agy runs under a redirected HOME, so the operator's own user-scope
// skills are linked in; the worktree's are left to agy's own scan.
func TestAntigravitySkillDirsCarryUserSkills(t *testing.T) {
	home, wt := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	gem := skillDirAt(t, filepath.Join(home, ".gemini", "config", "skills"), "gem")
	agents := skillDirAt(t, filepath.Join(home, ".agents", "skills"), "agents")
	skillDirAt(t, filepath.Join(wt, ".agents", "skills"), "repo")

	got := antigravitySkillDirs([]string{"/fwd/x"})
	if want := []string{"/fwd/x", agents, gem}; !slices.Equal(got, want) {
		t.Errorf("antigravitySkillDirs = %v, want %v", got, want)
	}
}
