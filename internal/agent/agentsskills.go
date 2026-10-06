package agent

import (
	"os"
	"path/filepath"
)

// agentsskills.go is the one skill convention gummi guarantees on every
// backend: .agents/skills, in the repository and in the operator's home
// (DESIGN §4.1a). Everything else a CLI discovers — its own directories,
// the operator's plugins and extensions — it keeps discovering; gummi
// neither adds to it nor takes from it.
//
// Copilot, opencode and codex read .agents/skills themselves, so their
// adapters do nothing here. The adapters whose CLI does not — claude never
// looks there, and antigravity runs under a redirected HOME where the
// operator's ~/.agents is not — hand these directories over alongside the
// forwarded ones.

// agentsSkillsRel is the cross-agent skill root, relative to a repository
// or to a home directory.
var agentsSkillsRel = filepath.Join(".agents", "skills")

// agentsSkillDirs returns the skill directories under workDir's
// .agents/skills, then under the operator's ~/.agents/skills: each a
// directory holding a SKILL.md, in name order within its root. Repo
// comes first, so a repo skill wins a name collision with a user one in
// the first-wins materializers. A root that does not exist contributes
// nothing.
func agentsSkillDirs(workDir string) []string {
	var out []string
	if workDir != "" {
		out = append(out, skillDirsUnder(filepath.Join(workDir, agentsSkillsRel))...)
	}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, skillDirsUnder(filepath.Join(home, agentsSkillsRel))...)
	}
	return out
}

// skillDirsUnder lists root's immediate children that are skill
// directories. A child may be a symlink to one; it is kept as the link
// path, so the skill's reference files stay where its SKILL.md says.
func skillDirsUnder(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if info, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil && info.Mode().IsRegular() {
			out = append(out, dir)
		}
	}
	return out
}

// withAgentsSkills returns forwarded followed by the .agents skill
// directories for workDir. Forwarded entries come first: the operator
// named them, so they win a basename collision.
func withAgentsSkills(forwarded []string, workDir string) []string {
	extra := agentsSkillDirs(workDir)
	if len(extra) == 0 {
		return forwarded
	}
	out := make([]string, 0, len(forwarded)+len(extra))
	out = append(out, forwarded...)
	return append(out, extra...)
}
