package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/state"
)

// writeSkill lays down a skill directory the way an operator would, at
// <root>/<skillRoot>/<name>/SKILL.md.
func writeSkill(t *testing.T, root, skillRoot, name string) string {
	t.Helper()
	dir := filepath.Join(root, skillRoot, name)
	writeUnder(t, filepath.Join(dir, "SKILL.md"), "---\nname: "+name+"\n---\nrule\n")
	return dir
}

// writeUnder is write plus the parent directories, which a skill always
// needs (.agents/skills/<name>/SKILL.md is three levels deep).
func writeUnder(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, path, body)
}

// newSkillsEngine builds an Engine with nothing but a workspace and a
// skills list — enough for resolution, which touches no store, no pool and
// no agent. Notices are captured rather than dropped so a test can assert
// what the operator would have been told.
func newSkillsEngine(t *testing.T, wsRoot string, forward ...string) (*Engine, *[]string) {
	t.Helper()
	var notices []string
	e := &Engine{cfg: Config{Workspace: state.Workspace{Root: wsRoot}, Skills: forward}}
	e.envWarn = func(msg string) { notices = append(notices, msg) }
	return e, &notices
}

// A bare name is resolved against the workspace's skill roots. This is the
// case the feature exists for: the skill sits beside .gummi, and the card
// that needs it runs in a worktree that is a sibling of the repository.
func TestForwardedSkillResolvesBareNameAtWorkspaceRoot(t *testing.T) {
	ws := t.TempDir()
	want := writeSkill(t, ws, filepath.Join(".agents", "skills"), "container-env")

	e, notices := newSkillsEngine(t, ws, "container-env")
	got := e.forwardedSkillDirs()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("forwardedSkillDirs() = %v, want [%s] (notices: %v)", got, want, *notices)
	}
}

// The roots are searched in a fixed order, so a workspace that keeps the
// same skill under two conventions forwards one directory, predictably,
// rather than whichever the filesystem happened to return first.
func TestForwardedSkillPrefersClaudeRootOverAgents(t *testing.T) {
	ws := t.TempDir()
	claude := writeSkill(t, ws, filepath.Join(".claude", "skills"), "toolchain")
	writeSkill(t, ws, filepath.Join(".agents", "skills"), "toolchain")

	e, _ := newSkillsEngine(t, ws, "toolchain")
	if got := e.forwardedSkillDirs(); len(got) != 1 || got[0] != claude {
		t.Errorf("forwardedSkillDirs() = %v, want [%s]", got, claude)
	}
}

// A name that resolves to nothing is reported and dropped. Failing the
// card instead would punish whichever card happened to start first for a
// workspace-level typo.
func TestForwardedSkillMissingIsWarnedNotFatal(t *testing.T) {
	ws := t.TempDir()
	e, notices := newSkillsEngine(t, ws, "not-there")

	if got := e.forwardedSkillDirs(); len(got) != 0 {
		t.Fatalf("forwardedSkillDirs() = %v, want none", got)
	}
	if len(*notices) != 1 || !strings.Contains((*notices)[0], "not-there") {
		t.Errorf("the missing skill was not reported by name: %v", *notices)
	}
}

// A directory without a SKILL.md is not a skill. Catching it here keeps a
// typo from becoming a backend that silently loads nothing.
func TestForwardedSkillNeedsASkillFile(t *testing.T) {
	ws := t.TempDir()
	writeUnder(t, filepath.Join(ws, ".agents", "skills", "empty", "README.md"), "not a skill\n")

	e, notices := newSkillsEngine(t, ws, "empty")
	if got := e.forwardedSkillDirs(); len(got) != 0 {
		t.Fatalf("a directory with no SKILL.md was forwarded: %v", got)
	}
	if len(*notices) != 1 {
		t.Errorf("notices = %v, want one", *notices)
	}
}

// gummi's own skill is never forwarded into a card's session. `gummi skill
// install --scope project` writes it into the very roots this resolves
// against, and it instructs an agent to drive gummi — which is the one
// thing a card must not do.
func TestGummiOwnSkillIsRefused(t *testing.T) {
	ws := t.TempDir()
	writeSkill(t, ws, filepath.Join(".claude", "skills"), "gummi")

	e, notices := newSkillsEngine(t, ws, "gummi")
	if got := e.forwardedSkillDirs(); len(got) != 0 {
		t.Fatalf("gummi's own skill was forwarded: %v", got)
	}
	if len(*notices) != 1 || !strings.Contains((*notices)[0], "second one") {
		t.Errorf("the refusal did not say why: %v", *notices)
	}
}

// An absolute path is taken as given, so a skill kept outside the
// workspace entirely can still be forwarded.
func TestForwardedSkillAcceptsAbsolutePath(t *testing.T) {
	ws := t.TempDir()
	elsewhere := t.TempDir()
	want := writeSkill(t, elsewhere, "skills", "hardware")

	e, _ := newSkillsEngine(t, ws, want)
	if got := e.forwardedSkillDirs(); len(got) != 1 || got[0] != want {
		t.Errorf("forwardedSkillDirs() = %v, want [%s]", got, want)
	}
}

// stubSkillAgent reports a fixed capability set, standing in for an
// adapter without starting one.
type stubSkillAgent struct{ caps agent.Capabilities }

func (s stubSkillAgent) Capabilities() agent.Capabilities { return s.caps }

// A backend that cannot take skill directories does not silently drop the
// forwarding: the operator is told, naming the backend. Silence here would
// be indistinguishable from a model that read the skill and ignored it.
func TestForwardingToAnIncapableBackendWarns(t *testing.T) {
	ws := t.TempDir()
	writeSkill(t, ws, filepath.Join(".agents", "skills"), "container-env")
	e, notices := newSkillsEngine(t, ws, "container-env")

	got := e.skillDirsFor(stubSkillAgent{}, "codex")
	if len(got) != 0 {
		t.Fatalf("skillDirsFor handed dirs to a backend that cannot use them: %v", got)
	}
	if len(*notices) != 1 || !strings.Contains((*notices)[0], "codex") {
		t.Fatalf("the operator was not told which backend dropped the skills: %v", *notices)
	}

	// Once per backend, not once per card: a board runs many cards on the
	// same backend and must not repeat itself for each of them.
	e.skillDirsFor(stubSkillAgent{}, "codex")
	if len(*notices) != 1 {
		t.Errorf("the notice repeated: %v", *notices)
	}
}

// A capable backend gets the directories.
func TestForwardingToACapableBackendPasses(t *testing.T) {
	ws := t.TempDir()
	want := writeSkill(t, ws, filepath.Join(".agents", "skills"), "container-env")
	e, notices := newSkillsEngine(t, ws, "container-env")

	got := e.skillDirsFor(stubSkillAgent{caps: agent.Capabilities{SkillDirs: true}}, "opencode")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("skillDirsFor() = %v, want [%s]", got, want)
	}
	if len(*notices) != 0 {
		t.Errorf("a capable backend produced notices: %v", *notices)
	}
}

// With nothing configured, nothing is passed and nothing is said — the
// ordinary board must be untouched by this feature.
func TestNoForwardingIsSilent(t *testing.T) {
	e, notices := newSkillsEngine(t, t.TempDir())
	if got := e.skillDirsFor(stubSkillAgent{}, "codex"); got != nil {
		t.Errorf("skillDirsFor() = %v, want nil", got)
	}
	if len(*notices) != 0 {
		t.Errorf("notices = %v, want none", *notices)
	}
}
