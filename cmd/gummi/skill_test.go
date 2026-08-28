package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/golden"
)

// The rendered SKILL.md must name every flag the run/resume/status/doctor
// commands actually register, plus every command — this is the drift lock
// (DESIGN §7): add a flag to `gummi run` and this test fails until SKILL.md
// regenerates from the real flag set.
func TestSkillDocumentsEveryFlag(t *testing.T) {
	doc := skillBody()

	registrars := map[string]func(*flag.FlagSet){
		"run":      func(fs *flag.FlagSet) { registerRunFlags(fs) },
		"research": func(fs *flag.FlagSet) { registerResearchFlags(fs) },
		"resume":   func(fs *flag.FlagSet) { registerResumeFlags(fs) },
		"merge":    func(fs *flag.FlagSet) { registerMergeFlags(fs) },
		"commit":   func(fs *flag.FlagSet) { registerCommitFlags(fs) },
		"status":   func(fs *flag.FlagSet) { registerStatusFlags(fs) },
		"doctor":   func(fs *flag.FlagSet) { registerDoctorFlags(fs) },
	}
	for cmd, reg := range registrars {
		fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
		reg(fs)
		fs.VisitAll(func(f *flag.Flag) {
			if !strings.Contains(doc, "--"+f.Name) {
				t.Errorf("SKILL.md does not mention %s flag --%s", cmd, f.Name)
			}
		})
	}

	for _, cmd := range []string{
		"gummi run", "gummi research", "gummi resume", "gummi verify", "gummi merge", "gummi commit", "gummi clean",
		"gummi status", "gummi spec", "gummi diff", "gummi doctor", "gummi skill",
	} {
		if !strings.Contains(doc, cmd) {
			t.Errorf("SKILL.md does not mention command %q", cmd)
		}
	}
}

// The exit contract in the doc must carry the real exit codes, generated
// from driver.Status.ExitCode() — so a code change surfaces here too.
func TestSkillDocumentsExitCodes(t *testing.T) {
	doc := skillBody()
	for _, want := range []string{
		"| 0 | `done` |", "| 2 | `question` |", "| 3 | `blocked` |",
		"| 4 | `escalation` |", "| 5 | `exhausted` |", "| 6 | `timeout` |",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("SKILL.md exit table missing row %q", want)
		}
	}
}

// The whole rendered body is goldened so prose changes are reviewed
// deliberately (run `go test ./cmd/gummi -run TestSkillBodyGolden -update`).
func TestSkillBodyGolden(t *testing.T) {
	golden.RequireEqual(t, []byte(skillBody()))
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
	if got := installedBodyHash(raw); got != skillBodyHash() {
		t.Errorf("installedBodyHash %q != skillBodyHash %q (body did not round-trip)", got, skillBodyHash())
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
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "skills", "gummi", "SKILL.md")
	tgt := installTarget{path: path, label: "test"}
	content := renderSkill("vtest")
	curHash := skillBodyHash()

	// dry-run: nothing on disk.
	if err := installOne(tgt, content, curHash, false, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("dry-run created a file: %v", err)
	}

	// real install: file present and stamped, body matches.
	if err := installOne(tgt, content, curHash, false, false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if installedBodyHash(raw) != curHash {
		t.Fatal("installed body does not match the current skill")
	}

	// re-install without --force is a no-op (content unchanged).
	if err := installOne(tgt, content, curHash, false, false); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(path); string(again) != string(raw) {
		t.Fatal("idempotent re-install rewrote the file")
	}

	// hand-edit → drift → refuse without --force (edit survives).
	edited := append(raw, []byte("\nHAND EDIT\n")...)
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installOne(tgt, content, curHash, false, false); err != nil {
		t.Fatal(err)
	}
	if now, _ := os.ReadFile(path); !strings.Contains(string(now), "HAND EDIT") {
		t.Fatal("drifted file was overwritten without --force")
	}

	// --force replaces it; body matches the current skill again.
	if err := installOne(tgt, content, curHash, true, false); err != nil {
		t.Fatal(err)
	}
	if now, _ := os.ReadFile(path); installedBodyHash(now) != curHash || strings.Contains(string(now), "HAND EDIT") {
		t.Fatal("--force did not restore the file")
	}
}

// resolveTargets: project scope is one file; user scope resolves per agent
// and rejects an unknown --agent.
func TestResolveTargets(t *testing.T) {
	proj, err := resolveTargets("project", "", "/repo")
	if err != nil || len(proj) != 2 {
		t.Fatalf("project targets = %+v, err=%v", proj, err)
	}
	if !strings.HasSuffix(proj[0].path, filepath.Join(".claude", "skills", "gummi", "SKILL.md")) {
		t.Errorf("project path = %q", proj[0].path)
	}
	if !strings.HasSuffix(proj[1].path, filepath.Join(".agents", "skills", "gummi", "SKILL.md")) {
		t.Errorf("codex project path = %q", proj[1].path)
	}
	codexProject, err := resolveTargets("project", "codex", "/repo")
	if err != nil || len(codexProject) != 1 || codexProject[0].path != codexProjectSkillPath("/repo") {
		t.Fatalf("explicit codex project target = %+v, err=%v", codexProject, err)
	}
	codex, err := resolveTargets("user", "codex", "/repo")
	if err != nil || len(codex) != 1 || !strings.Contains(codex[0].path, filepath.Join(".agents", "skills", "gummi")) {
		t.Fatalf("codex user target = %+v, err=%v", codex, err)
	}

	cop, err := resolveTargets("user", "copilot", "/repo")
	if err != nil || len(cop) != 1 || !strings.Contains(cop[0].path, filepath.Join(".copilot", "skills", "gummi")) {
		t.Fatalf("copilot user target = %+v, err=%v", cop, err)
	}
	if _, err := resolveTargets("user", "bogus", "/repo"); err == nil {
		t.Error("resolveTargets accepted an unknown --agent")
	}
}

// userSkillPath honors CLAUDE_CONFIG_DIR for the claude/opencode home.
func TestUserSkillPathHonorsClaudeConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/custom/cc")
	got := userSkillPath(agentClaude)
	if want := filepath.Join("/custom/cc", "skills", "gummi", "SKILL.md"); got != want {
		t.Errorf("userSkillPath = %q, want %q", got, want)
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

func TestParseAgentAcceptsZZ(t *testing.T) {
	got, err := parseAgent("zz")
	if err != nil || got != agentZZ {
		t.Fatalf("parseAgent(\"zz\") = %q, %v, want agentZZ, nil", got, err)
	}
	_, err = parseAgent("bogus")
	if err == nil {
		t.Fatal("bogus agent accepted")
	}
	for _, name := range []string{"claude", "codex", "opencode", "copilot", "zz"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("unknown-agent error should mention %q, got: %v", name, err)
		}
	}
}

func TestUserSkillPathZZ(t *testing.T) {
	got := userSkillPath(agentZZ)
	want := filepath.Join(homeDir(), ".config", "zz", "skills", "gummi", "SKILL.md")
	if got != want {
		t.Errorf("userSkillPath(agentZZ) = %q, want %q", got, want)
	}
}

func TestDetectAgentsIncludesZZOnPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zz"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	found := false
	for _, a := range detectAgents() {
		if a == agentZZ {
			found = true
		}
	}
	if !found {
		t.Fatal("zz was not detected on PATH")
	}
}
