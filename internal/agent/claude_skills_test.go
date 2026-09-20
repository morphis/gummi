package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// skillDirAt lays down a skill the way an operator's workspace holds one.
func skillDirAt(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: "+name+"\n---\nrule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The ordinary session forwards nothing and must touch no disk at all.
func TestClaudeSkillsMaterializeNothingWhenNoneForwarded(t *testing.T) {
	pluginDir, root, err := claudeMaterializeSkills(SessionOpts{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("claudeMaterializeSkills: %v", err)
	}
	if pluginDir != "" || root != "" {
		t.Errorf("materialized %q / %q with nothing forwarded", pluginDir, root)
	}
}

// The CLI's --plugin-dir loads a plugin, not a folder of skill
// directories: without .claude-plugin/plugin.json it loads nothing and
// says nothing. The manifest is what makes the flag work at all.
func TestClaudeSkillsWriteAPluginManifest(t *testing.T) {
	ws := t.TempDir()
	skill := skillDirAt(t, ws, "container-env")

	pluginDir, root, err := claudeMaterializeSkills(SessionOpts{SkillDirs: []string{skill}})
	if err != nil {
		t.Fatalf("claudeMaterializeSkills: %v", err)
	}
	defer os.RemoveAll(root)

	raw, err := os.ReadFile(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatalf("no plugin manifest: %v", err)
	}
	var man map[string]string
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatalf("manifest is not valid JSON: %v\n%s", err, raw)
	}
	if man["name"] != claudeSkillPluginName {
		t.Errorf("manifest name = %q, want %q", man["name"], claudeSkillPluginName)
	}
}

// Skills are symlinked rather than copied: a forwarded skill may ship
// reference files or scripts beside its SKILL.md, and the link keeps them
// at the paths the skill's own instructions name.
func TestClaudeSkillsAreSymlinkedNotCopied(t *testing.T) {
	ws := t.TempDir()
	skill := skillDirAt(t, ws, "container-env")
	if err := os.WriteFile(filepath.Join(skill, "probe.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	pluginDir, root, err := claudeMaterializeSkills(SessionOpts{SkillDirs: []string{skill}})
	if err != nil {
		t.Fatalf("claudeMaterializeSkills: %v", err)
	}
	defer os.RemoveAll(root)

	link := filepath.Join(pluginDir, "skills", "container-env")
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("skill not linked into the plugin: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("skill was copied, not symlinked (mode %v)", info.Mode())
	}
	target, err := os.Readlink(link)
	if err != nil || target != skill {
		t.Errorf("link target = %q (err %v), want %q", target, err, skill)
	}
	// the skill's bundled file is reachable through the link
	if _, err := os.Stat(filepath.Join(link, "probe.sh")); err != nil {
		t.Errorf("a file bundled with the skill is unreachable through the link: %v", err)
	}
}

// Two forwarded directories with the same basename resolve first-wins,
// matching the order the engine searched its roots in — rather than one
// silently replacing the other.
func TestClaudeSkillsNameCollisionIsFirstWins(t *testing.T) {
	a := skillDirAt(t, t.TempDir(), "toolchain")
	b := skillDirAt(t, t.TempDir(), "toolchain")

	pluginDir, root, err := claudeMaterializeSkills(SessionOpts{SkillDirs: []string{a, b}})
	if err != nil {
		t.Fatalf("claudeMaterializeSkills: %v", err)
	}
	defer os.RemoveAll(root)

	target, err := os.Readlink(filepath.Join(pluginDir, "skills", "toolchain"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != a {
		t.Errorf("collision resolved to %q, want the first entry %q", target, a)
	}
}

// The plugin directory is the one handed to --plugin-dir, and it sits
// inside the returned root so Close can remove the whole tree.
func TestClaudeSkillsPluginDirIsUnderRoot(t *testing.T) {
	skill := skillDirAt(t, t.TempDir(), "container-env")
	pluginDir, root, err := claudeMaterializeSkills(SessionOpts{SkillDirs: []string{skill}})
	if err != nil {
		t.Fatalf("claudeMaterializeSkills: %v", err)
	}
	defer os.RemoveAll(root)

	if want := filepath.Join(root, claudeSkillPluginName); pluginDir != want {
		t.Errorf("pluginDir = %q, want %q", pluginDir, want)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Errorf("the root is not removable as one tree: %v", err)
	}
	if _, err := os.Stat(pluginDir); !os.IsNotExist(err) {
		t.Error("removing the root left the plugin behind")
	}
}
