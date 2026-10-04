package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/golden"
	"github.com/spf13/pflag"
)

// Every flag the shipped commands declare must be documented somewhere in
// the bundle — this is the drift lock (DESIGN §7): add a flag to `gummi
// run` and this test fails until the skill regenerates.
//
// The bundle, not SKILL.md alone: a goal-only flag belongs in
// references/goals.md, and an agent shipping one card should never have to
// read past SKILL.md to find what it can use.
func TestSkillDocumentsEveryFlag(t *testing.T) {
	var bundle strings.Builder
	for _, f := range skillBundle() {
		bundle.WriteString(f.body)
	}
	doc := bundle.String()

	for _, path := range []string{"run", "research", "diagnose", "goal", "resume", "merge", "squash", "commit", "log", "rewrite", "status", "watch", "doctor"} {
		cmd, _, err := rootCmd.Find(strings.Fields(path))
		if err != nil {
			t.Fatalf("finding %q: %v", path, err)
		}
		cmd.Flags().VisitAll(func(f *pflag.Flag) {
			if f.Name == "help" {
				return
			}
			if !strings.Contains(doc, "--"+f.Name) {
				t.Errorf("the skill bundle does not mention %s flag --%s", path, f.Name)
			}
		})
	}

	for _, cmd := range []string{
		"gummi run", "gummi research", "gummi diagnose", "gummi resume", "gummi verify",
		"gummi merge", "gummi squash", "gummi commit", "gummi log", "gummi rewrite", "gummi clean", "gummi handoff",
		"gummi status", "gummi watch", "gummi spec", "gummi diff", "gummi doctor",
		"gummi deps add", "gummi skill",
	} {
		if !strings.Contains(skillBody(), cmd) {
			t.Errorf("SKILL.md does not mention command %q", cmd)
		}
	}
	if !strings.Contains(doc, "gummi goal") {
		t.Error("the skill bundle does not mention `gummi goal`")
	}
}

// A flag advertised in the skill must be one the binary really accepts.
// The generated grammar is read off the cobra tree that parses, so this
// checks the hand-written prose too — where `--m`, a flag the binary never
// had, survived for as long as the grammar generator invented it.
func TestSkillNamesNoFlagTheBinaryLacks(t *testing.T) {
	known := declaredFlags()
	seenForeign := map[string]bool{}
	for _, f := range skillBundle() {
		for _, m := range longFlagRe.FindAllStringSubmatch(f.body, -1) {
			name := m[1]
			if known[name] {
				continue
			}
			if _, ok := foreignSkillFlags[name]; ok {
				seenForeign[name] = true
				continue
			}
			t.Errorf("%s documents --%s, which no gummi command declares", f.path, name)
		}
	}
	// The opt-out list must not outlive the prose it excuses, or it
	// quietly becomes a way to hide a real flag from the check.
	for name := range foreignSkillFlags {
		if !seenForeign[name] {
			t.Errorf("foreignSkillFlags still excuses --%s, which the skill no longer mentions; drop the entry", name)
		}
	}
}

// foreignSkillFlags are long flags the skill quotes that belong to ANOTHER
// tool, so gummi is not expected to declare them. This is the only way to
// opt out of the check above, so every entry carries whose flag it is.
//
// --model was here once, excusing the Claude CLI's own --model in the
// setup reference — until `gummi schedule add --model` gave gummi a flag
// of its own by that name, at which point the excuse became an unused
// hide and was dropped.
var foreignSkillFlags = map[string]string{}

// The exit contract in the doc must carry the real exit codes, generated
// from driver.Status.ExitCode() — so a code change surfaces here too.
func TestSkillDocumentsExitCodes(t *testing.T) {
	doc := skillBody()
	for _, want := range []string{
		"| 0 | `verified` |", "| 2 | `question` |", "| 3 | `blocked` |",
		"| 4 | `escalation` |", "| 5 | `exhausted` |", "| 6 | `timeout` |",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("SKILL.md exit table missing row %q", want)
		}
	}
}

// Every file of the bundle is goldened so prose changes are reviewed
// deliberately (run `go test ./cmd/gummi -run TestSkillBodyGolden -update`).
func TestSkillBodyGolden(t *testing.T) {
	for _, f := range skillBundle() {
		name := strings.TrimSuffix(strings.TrimPrefix(f.path, "references/"), ".md")
		t.Run(name, func(t *testing.T) {
			golden.RequireEqual(t, []byte(f.body))
		})
	}
}

// renderSkill stamps the frontmatter with the version and the body hash, and
// the body round-trips: parsing the installed file reproduces skillBodyHash.
func TestSkillFrontmatterStamp(t *testing.T) {
	raw := renderSkill("v1.2.3")
	stamp, ok := parseInstalledStamp(raw)
	if !ok {
		t.Fatal("rendered skill has no parseable gummi stamp")
	}
	if stamp.Version != "v1.2.3" {
		t.Errorf("gummi_version = %q, want v1.2.3", stamp.Version)
	}
	if stamp.Hash != skillBodyHash() {
		t.Errorf("stamped hash %q != skillBodyHash %q", stamp.Hash, skillBodyHash())
	}
	_, body, split := splitFrontmatter(raw)
	if !split || body != skillBody() {
		t.Error("SKILL.md's body did not round-trip through its frontmatter")
	}
}

// The bundle hash covers every file, not just SKILL.md: a hand-edited
// reference file must read as drift exactly as an edited SKILL.md does.
// Before the split there was one file and nothing to get this wrong.
func TestSkillBundleHashCoversReferences(t *testing.T) {
	files := skillBundle()
	if len(files) < 2 {
		t.Fatal("the bundle has no reference files to cover")
	}
	base := bundleHash(files)
	edited := append([]skillFile(nil), files...)
	edited[len(edited)-1].body += "\nHAND EDIT\n"
	if bundleHash(edited) == base {
		t.Errorf("editing %s did not change the bundle hash", edited[len(edited)-1].path)
	}
}

// A file without gummi frontmatter is not claimed as ours.
func TestParseInstalledStampForeign(t *testing.T) {
	if _, ok := parseInstalledStamp([]byte("# just some markdown\n")); ok {
		t.Error("claimed a non-frontmatter file as a gummi skill")
	}
	if _, ok := parseInstalledStamp([]byte("---\nname: other\n---\nbody\n")); ok {
		t.Error("claimed a frontmatter file without gummi_skill_hash as ours")
	}
}

// installOne: dry-run writes nothing; a real install writes a stamped file;
// re-install is idempotent; a drifted (hand-edited) file is not overwritten
// without --force; --force replaces it.
func TestInstallOneLifecycle(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".claude", "skills", "gummi")
	tgt := installTarget{dir: dir, label: "test"}
	bundle := installBundle("vtest")
	curHash := skillBodyHash()
	upToDate := func() bool {
		h, complete := installedBundleHash(dir)
		return complete && h == curHash
	}

	// dry-run: nothing on disk.
	if err := installOne(tgt, bundle, curHash, false, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tgt.skillPath()); !os.IsNotExist(err) {
		t.Fatalf("dry-run created a file: %v", err)
	}

	// real install: every file present and stamped, bundle matches.
	if err := installOne(tgt, bundle, curHash, false, false); err != nil {
		t.Fatal(err)
	}
	for _, f := range bundle {
		if _, err := os.Stat(filepath.Join(dir, f.path)); err != nil {
			t.Fatalf("install did not write %s: %v", f.path, err)
		}
	}
	if !upToDate() {
		t.Fatal("installed bundle does not match the current skill")
	}
	raw, err := os.ReadFile(tgt.skillPath())
	if err != nil {
		t.Fatal(err)
	}

	// re-install without --force is a no-op (content unchanged).
	if err := installOne(tgt, bundle, curHash, false, false); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(tgt.skillPath()); string(again) != string(raw) {
		t.Fatal("idempotent re-install rewrote the file")
	}

	// a hand-edited REFERENCE file is drift too, and is refused without
	// --force just as an edited SKILL.md is.
	ref := filepath.Join(dir, bundle[len(bundle)-1].path)
	refRaw, err := os.ReadFile(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ref, append(refRaw, []byte("\nHAND EDIT\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if upToDate() {
		t.Fatal("an edited reference file still read as up to date")
	}
	if err := installOne(tgt, bundle, curHash, false, false); err != nil {
		t.Fatal(err)
	}
	if now, _ := os.ReadFile(ref); !strings.Contains(string(now), "HAND EDIT") {
		t.Fatal("drifted reference was overwritten without --force")
	}

	// --force replaces the whole bundle.
	if err := installOne(tgt, bundle, curHash, true, false); err != nil {
		t.Fatal(err)
	}
	if now, _ := os.ReadFile(ref); strings.Contains(string(now), "HAND EDIT") {
		t.Fatal("--force did not restore the reference file")
	}
	if !upToDate() {
		t.Fatal("--force did not restore the bundle")
	}
}

// checkTargets: all up-to-date targets → nil; a missing target, and
// separately a hand-edited one, each yield a non-nil error naming that
// target's path.
func TestSkillInstallCheck(t *testing.T) {
	dir := t.TempDir()
	curHash := skillBodyHash()
	bundle := installBundle("vtest")

	upToDate := installTarget{dir: filepath.Join(dir, "up-to-date"), label: "up-to-date"}
	if err := installOne(upToDate, bundle, curHash, false, false); err != nil {
		t.Fatal(err)
	}

	if err := checkTargets([]installTarget{upToDate}, curHash); err != nil {
		t.Errorf("all targets up to date: checkTargets = %v, want nil", err)
	}

	absent := installTarget{dir: filepath.Join(dir, "absent"), label: "absent"}
	if err := checkTargets([]installTarget{upToDate, absent}, curHash); err == nil {
		t.Error("missing target: checkTargets = nil, want an error naming it")
	} else if !strings.Contains(err.Error(), absent.dir) {
		t.Errorf("checkTargets error %q does not name absent target %q", err, absent.dir)
	}

	drifted := installTarget{dir: filepath.Join(dir, "drifted"), label: "drifted"}
	if err := installOne(drifted, bundle, curHash, false, false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(drifted.skillPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(drifted.skillPath(), append(raw, []byte("\nHAND EDIT\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkTargets([]installTarget{upToDate, drifted}, curHash); err == nil {
		t.Error("drifted target: checkTargets = nil, want an error naming it")
	} else if !strings.Contains(err.Error(), drifted.dir) {
		t.Errorf("checkTargets error %q does not name drifted target %q", err, drifted.dir)
	}
}

// resolveTargets: project scope is one file; user scope resolves per agent
// and rejects an unknown --agent.
func TestResolveTargets(t *testing.T) {
	proj, err := resolveTargets("project", "", "/repo")
	if err != nil || len(proj) != 2 {
		t.Fatalf("project targets = %+v, err=%v", proj, err)
	}
	if !strings.HasSuffix(proj[0].dir, filepath.Join(".claude", "skills", "gummi")) {
		t.Errorf("project dir = %q", proj[0].dir)
	}
	if !strings.HasSuffix(proj[1].dir, filepath.Join(".agents", "skills", "gummi")) {
		t.Errorf("codex project dir = %q", proj[1].dir)
	}
	codexProject, err := resolveTargets("project", "codex", "/repo")
	if err != nil || len(codexProject) != 1 || codexProject[0].dir != codexProjectSkillDir("/repo") {
		t.Fatalf("explicit codex project target = %+v, err=%v", codexProject, err)
	}
	codex, err := resolveTargets("user", "codex", "/repo")
	if err != nil || len(codex) != 1 || !strings.Contains(codex[0].dir, filepath.Join(".agents", "skills", "gummi")) {
		t.Fatalf("codex user target = %+v, err=%v", codex, err)
	}

	cop, err := resolveTargets("user", "copilot", "/repo")
	if err != nil || len(cop) != 1 || !strings.Contains(cop[0].dir, filepath.Join(".copilot", "skills", "gummi")) {
		t.Fatalf("copilot user target = %+v, err=%v", cop, err)
	}
	if _, err := resolveTargets("user", "bogus", "/repo"); err == nil {
		t.Error("resolveTargets accepted an unknown --agent")
	}
}

// userSkillDir honors CLAUDE_CONFIG_DIR for the claude/opencode home.
func TestUserSkillDirHonorsClaudeConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/custom/cc")
	got := userSkillDir(agentClaude)
	if want := filepath.Join("/custom/cc", "skills", "gummi"); got != want {
		t.Errorf("userSkillDir = %q, want %q", got, want)
	}
}

func TestDetectAgentsIncludesCodex(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	found := false
	for _, a := range detectAgents() {
		if a == agentCodex {
			found = true
		}
	}
	if !found {
		t.Fatal("Codex was not detected from CODEX_HOME")
	}
}

func TestParseAgentRejectsBogus(t *testing.T) {
	_, err := parseAgent("bogus")
	if err == nil {
		t.Fatal("bogus agent accepted")
	}
	for _, name := range []string{"claude", "codex", "opencode", "copilot", "pi"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("unknown-agent error should mention %q, got: %v", name, err)
		}
	}
}

// A repo gummi has no workspace relationship with at all (no .gummi
// anywhere on or above cwd) must never receive a project-scope install: the
// old resolveRoots(cwd) treated any directory as its own workspace root, so
// `skill install --scope project` from inside a bystander repo seeded
// SKILL.md straight into that repo's working tree (BG-020).
func TestSkillInstallProjectScopeRefusesUnmanagedRepo(t *testing.T) {
	dir := gitRepo(t)
	t.Chdir(dir)

	err := runCLI("skill", "install", "--scope", "project", "--agent", "claude")
	if err == nil {
		t.Fatal("skillInstall in a repo with no gummi workspace = nil error, want a refusal")
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".claude", "skills", "gummi", "SKILL.md")); statErr == nil {
		t.Error("skillInstall wrote SKILL.md into the unmanaged repo despite returning an error")
	}
}

// When cwd is a repo managed by a workspace whose .gummi sits above it (the
// FD-070/071/072 layout), project scope must land beside .gummi at the
// workspace root, not inside the repo gummi happens to be driving from.
func TestSkillInstallProjectScopeAnchorsToWorkspaceRoot(t *testing.T) {
	ws := t.TempDir()
	repo := filepath.Join(ws, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, ws, "repo: repo\n")
	t.Chdir(repo)

	if err := runCLI("skill", "install", "--scope", "project", "--agent", "claude"); err != nil {
		t.Fatalf("skillInstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, ".claude", "skills", "gummi", "SKILL.md")); err != nil {
		t.Errorf("SKILL.md not written beside .gummi at the workspace root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".claude", "skills", "gummi", "SKILL.md")); err == nil {
		t.Error("SKILL.md was written into the managed repo instead of beside .gummi")
	}
}

// A symlinked .gummi at cwd must not be trusted as a real workspace: the
// refusal guard has to agree with findGummiRoot's anti-symlink-smuggle rule
// (also used to resolve ws itself), not re-derive "does a workspace exist"
// from a plain Lstat that a symlink satisfies just as well as a real
// directory (BG-020 review finding).
func TestSkillInstallProjectScopeRefusesSymlinkedGummi(t *testing.T) {
	dir := gitRepo(t)
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(dir, ".gummi")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	err := runCLI("skill", "install", "--scope", "project", "--agent", "claude")
	if err == nil {
		t.Fatal("skillInstall with a symlinked .gummi = nil error, want a refusal")
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".claude", "skills", "gummi", "SKILL.md")); statErr == nil {
		t.Error("skillInstall wrote SKILL.md into the bystander repo despite the untrusted symlinked .gummi")
	}
}
