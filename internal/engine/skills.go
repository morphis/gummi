package engine

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/morphis/gummi/internal/agent"
)

// Workspace skills, forwarded into the sessions that run inside a card's
// worktree.
//
// A card's worktree lives at <workspace>/.gummi/worktrees/<ID> — a sibling
// of the managed repository, not a directory inside it. That is the whole
// problem this file exists for. Skills the REPOSITORY carries ride along in
// the worktree checkout and every backend finds them; skills the OPERATOR
// keeps at the workspace root, beside .gummi, are outside every backend's
// project scope and reach nothing. In the multi-repo layout (`repos:`,
// DESIGN §10.10) the workspace root is exactly where rules that are not any
// one repository's belong — how to work in this container, which toolchain
// the machine has — so the skills that describe the ENVIRONMENT are
// precisely the ones no session could see.
//
// The resolution is deliberately small: a name or an absolute path in,
// an absolute directory out. Nothing here reads a SKILL.md or reasons about
// its contents; what a skill says is the backend's business, and gummi's
// job ends at making the directory reachable.
//
// Backends differ on whether it IS reachable — see agent.Capabilities'
// SkillDirs. Where a backend cannot be pointed at an outside directory the
// forwarding does not silently evaporate: skillDirsFor warns, naming the
// backend, so an operator who configured forwarding finds out from gummi
// rather than from a session that behaved as though the skill did not
// exist.
var skillRoots = []string{
	filepath.Join(".claude", "skills"),
	filepath.Join(".agents", "skills"),
	filepath.Join(".github", "skills"),
}

// gummiSkillName is gummi's own skill, as `gummi skill install --scope
// project` writes it into the workspace's skill roots. It is refused for
// forwarding: a stage session is a card doing work inside gummi, and the
// one instruction it must never receive is how to start another one (the
// stage hints tell every session so). An operator naming it has almost
// certainly confused the outside driver's skill with the work's.
const gummiSkillName = "gummi"

// forwardedSkillDirs resolves the configured skills to absolute
// directories, at most once per Engine lifetime. An entry that cannot be
// resolved is warned about and dropped: a missing skill is a
// misconfiguration to report, never a reason to fail the card that
// happened to start first.
func (e *Engine) forwardedSkillDirs() []string {
	e.skillsOnce.Do(func() {
		for _, name := range e.cfg.Skills {
			dir, err := resolveSkillDir(e.cfg.Workspace.Root, name)
			if err != nil {
				e.warn(err.Error())
				continue
			}
			e.skillDirs = append(e.skillDirs, dir)
		}
	})
	return e.skillDirs
}

// resolveSkillDir turns one configured entry into the absolute directory
// holding its SKILL.md. A bare name is looked up under the workspace's
// conventional skill roots in order; an absolute path is taken as given.
// Both are then verified to hold a SKILL.md, so a typo is caught here
// rather than becoming a backend that quietly loads nothing.
func resolveSkillDir(wsRoot, name string) (string, error) {
	if filepath.IsAbs(name) {
		if !hasSkillFile(name) {
			return "", fmt.Errorf("skills.forward: %s holds no SKILL.md; the skill was not forwarded", name)
		}
		return filepath.Clean(name), nil
	}
	if name == gummiSkillName {
		return "", fmt.Errorf("skills.forward: refusing to forward gummi's own skill into a card's session — " +
			"it instructs an agent to drive gummi, and a card must never start a second one")
	}
	for _, root := range skillRoots {
		dir := filepath.Join(wsRoot, root, name)
		if hasSkillFile(dir) {
			return dir, nil
		}
	}
	return "", fmt.Errorf("skills.forward: no skill named %q under %s in the workspace; the skill was not forwarded",
		name, skillRootList())
}

// hasSkillFile reports whether dir is a skill directory — one holding a
// readable SKILL.md. Anything else (a missing directory, a stray file, a
// directory whose SKILL.md cannot be read) is not one.
func hasSkillFile(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "SKILL.md"))
	return err == nil && info.Mode().IsRegular()
}

// skillRootList renders the searched roots for an error message.
func skillRootList() string {
	out := ""
	for i, r := range skillRoots {
		if i > 0 {
			out += ", "
		}
		out += r
	}
	return out
}

// skillDirsFor returns the skill directories to hand a session opened on
// backend ag, warning once per backend when forwarding is configured but
// the backend cannot honor it. The warning is the point: silence here
// would look exactly like a skill whose instructions the model chose to
// ignore.
func (e *Engine) skillDirsFor(ag skillCapable, backend string) []string {
	dirs := e.forwardedSkillDirs()
	if len(dirs) == 0 {
		return nil
	}
	if ag == nil || !ag.Capabilities().SkillDirs {
		e.warnSkillBackendOnce(backend)
		return nil
	}
	return dirs
}

// warnSkillBackendOnce emits the "this backend cannot take forwarded
// skills" notice at most once per backend per Engine lifetime, so a board
// running many cards on the same backend says it once rather than once a
// card.
func (e *Engine) warnSkillBackendOnce(backend string) {
	e.skillsMu.Lock()
	if e.skillWarned == nil {
		e.skillWarned = map[string]bool{}
	}
	already := e.skillWarned[backend]
	e.skillWarned[backend] = true
	e.skillsMu.Unlock()
	if already {
		return
	}
	e.warn(fmt.Sprintf("skills.forward is configured, but the %s backend cannot load skills from outside the worktree; "+
		"those skills will not reach its sessions (point the role at opencode or copilot, or state the rules in "+
		".gummi/environment.md, which every backend receives)", backend))
}

// skillCapable is the sliver of agent.Agent skillDirsFor needs, kept as a
// local interface so a test can pass a stub instead of a whole adapter.
type skillCapable interface{ Capabilities() agent.Capabilities }

// backendLabel names a backend for an operator-facing notice. A role that
// names none resolved to the workspace default, which has a name the
// operator would not recognize from the message alone.
func backendLabel(backend string) string {
	if backend == "" {
		return "default"
	}
	return backend
}

// warn routes a notice onto the same buffered channel the environment card
// uses, so a skills notice reaches a session's activity feed the way an
// unreadable instruction file already does. A nil envWarn (an Engine built
// directly in a test) drops them.
func (e *Engine) warn(msg string) {
	if e.envWarn == nil {
		return
	}
	e.envWarn(msg)
}
