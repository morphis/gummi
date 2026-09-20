package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// claude_skills.go materializes forwarded skill directories as a Claude
// Code plugin, which is the only way to point that CLI at skills outside
// its project scope.
//
// The CLI's --plugin-dir loads a PLUGIN for the session, not a folder of
// skills: a directory holding .claude-plugin/plugin.json, with its skills
// under skills/<name>/. Handed a bare folder of skill directories it loads
// nothing and says nothing, so the manifest is not ceremony — it is the
// difference between the flag working and silently doing nothing.
//
// Two properties were measured against the CLI (2.1.278) rather than
// assumed, because both shape the design:
//
//   - The skills are SYMLINKED, not copied. A forwarded skill may ship
//     reference files or scripts beside its SKILL.md, and a symlink keeps
//     them at their real paths where the skill's own instructions expect
//     them; copying would also mean deciding when a copy goes stale. The
//     CLI follows the links.
//   - The plugin is loaded by the roster alone. Skill does not need to
//     appear in --allowedTools: it is auto-approved, so gummi does not
//     widen the allowlist to make forwarding work. claudeStageTools adds
//     Skill to the roster when a session has skills to invoke; that is the
//     whole permission story.
//
// Skills arrive namespaced by the plugin, i.e. gummi-skills:<name>.

// claudeSkillPluginName names the generated plugin. It appears to the
// model as the namespace on every forwarded skill, so it says where the
// skill came from rather than looking like one of the repository's own.
const claudeSkillPluginName = "gummi-skills"

// claudeMaterializeSkills writes the plugin for opts.SkillDirs into a
// fresh temp directory and returns the plugin directory to pass as
// --plugin-dir. A session with no forwarded skills returns an empty path
// and creates nothing — the ordinary case must touch no disk.
//
// The caller owns the returned root and removes it when the session ends.
func claudeMaterializeSkills(opts SessionOpts) (pluginDir, root string, err error) {
	if len(opts.SkillDirs) == 0 {
		return "", "", nil
	}
	root, err = os.MkdirTemp("", "gummi-claude-skills-*")
	if err != nil {
		return "", "", fmt.Errorf("claude adapter: creating skill plugin: %w", err)
	}
	pluginDir = filepath.Join(root, claudeSkillPluginName)
	if err := writeClaudeSkillPlugin(pluginDir, opts.SkillDirs); err != nil {
		_ = os.RemoveAll(root)
		return "", "", err
	}
	return pluginDir, root, nil
}

// writeClaudeSkillPlugin lays out one plugin: the manifest, then a symlink
// per forwarded skill under skills/.
//
// A skill whose link cannot be made is skipped rather than failing the
// session: the session is about to do a card's work, and losing one
// forwarded skill is not a reason to refuse to start. A name collision
// between two forwarded directories is resolved first-wins, matching the
// engine's own root ordering.
func writeClaudeSkillPlugin(pluginDir string, dirs []string) error {
	skillsDir := filepath.Join(pluginDir, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return fmt.Errorf("claude adapter: creating skill plugin: %w", err)
	}
	manifest, err := json.Marshal(map[string]string{
		"name":        claudeSkillPluginName,
		"description": "Workspace skills forwarded by gummi",
		"version":     "0.0.0",
	})
	if err != nil {
		// A fixed map of strings; Marshal cannot fail on it.
		panic(err)
	}
	metaDir := filepath.Join(pluginDir, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return fmt.Errorf("claude adapter: creating skill plugin: %w", err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), manifest, 0o644); err != nil {
		return fmt.Errorf("claude adapter: writing skill plugin manifest: %w", err)
	}
	for _, dir := range dirs {
		name := filepath.Base(filepath.Clean(dir))
		if name == "" || name == "." || name == string(filepath.Separator) {
			continue
		}
		link := filepath.Join(skillsDir, name)
		if _, err := os.Lstat(link); err == nil {
			continue // first wins
		}
		_ = os.Symlink(dir, link)
	}
	return nil
}
