package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/morphis/gummi/internal/driver"
)

// skill.tmpl.md is the SKILL.md *body* (no frontmatter, no version string),
// embedded so `gummi skill show/install` can render it. Its command-grammar
// and exit-code sections are generated from the real flag sets and
// driver.Status, so the shipped doc can never drift from the binary — a
// golden + drift test (skill_test.go) locks that.
//
//go:embed skill.tmpl.md
var skillTemplate string

const skillName = "gummi"

const skillDescription = "Ship one PR-sized feature or bug to a verified branch via gummi's headless, spec-driven workflow (spec, review, verify; gummi never merges). Use when the work warrants a spec, an independent code review, and an isolated branch — not for trivial one-line edits."

// runSkill implements `gummi skill show|install|list` (DESIGN §7): it
// generates the SKILL.md all three agents (Claude, Copilot, opencode) read
// and installs it. No engine dependency — this command lands independently.
func runSkill(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: gummi skill show|install|list [--agent claude|opencode|copilot] [--scope user|project] [--force] [--dry-run]")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "show":
		return skillShow(rest)
	case "install":
		return skillInstall(rest)
	case "list":
		return skillList(rest)
	default:
		return fmt.Errorf("unknown skill subcommand %q (usage: gummi skill show|install|list)", sub)
	}
}

// skillShow prints the rendered SKILL.md (frontmatter + body) to stdout.
func skillShow(args []string) error {
	fs := flag.NewFlagSet("skill show", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, "usage: gummi skill show") }
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, err := os.Stdout.Write(renderSkill(version()))
	return err
}

// --- rendering + version stamp ----------------------------------------

// skillBody renders the embedded template with the generated grammar and
// exit table. It is deterministic and version-free, so it is safe to hash
// and to golden-test.
func skillBody() string {
	tmpl := template.Must(template.New("skill").Parse(skillTemplate))
	var b strings.Builder
	data := struct {
		Grammar   string
		ExitTable string
	}{Grammar: commandGrammar(), ExitTable: exitTable()}
	if err := tmpl.Execute(&b, data); err != nil {
		// the template is embedded and covered by tests; a runtime failure
		// here is a programmer error, not a user-facing condition.
		panic("rendering SKILL.md template: " + err.Error())
	}
	return b.String()
}

// skillBodyHash is the content fingerprint stamped into the frontmatter and
// compared for drift: an installed skill whose body hashes differently than
// the current binary would generate is stale (older binary) or hand-edited.
func skillBodyHash() string { return sha256hex(skillBody()) }

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// renderSkill assembles the full SKILL.md: YAML frontmatter (name +
// description for agent discovery, gummi_version informational, and the
// gummi_skill_hash drift stamp) over the body. The version lives only in
// frontmatter and is excluded from the hash, so a patch release does not
// force a needless reinstall; a hand-edit to the body still flips the hash.
func renderSkill(version string) []byte {
	body := skillBody()
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + skillName + "\n")
	b.WriteString("description: " + yamlQuote(skillDescription) + "\n")
	b.WriteString("gummi_version: " + yamlQuote(version) + "\n")
	b.WriteString("gummi_skill_hash: " + skillBodyHash() + "\n")
	b.WriteString("---\n\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// yamlQuote double-quotes a scalar so a description containing YAML-special
// runs (a colon, a leading dash) parses back cleanly.
func yamlQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// installStamp is the drift-detection metadata parsed from an installed
// SKILL.md's frontmatter.
type installStamp struct {
	Version string `yaml:"gummi_version"`
	Hash    string `yaml:"gummi_skill_hash"`
}

// parseInstalledStamp reads the gummi_version / gummi_skill_hash stamp from
// an installed SKILL.md. ok is false when the file has no gummi frontmatter
// (not one of ours, or hand-written without a stamp).
func parseInstalledStamp(raw []byte) (stamp installStamp, ok bool) {
	front, _, split := splitFrontmatter(raw)
	if !split {
		return installStamp{}, false
	}
	if err := yaml.Unmarshal([]byte(front), &stamp); err != nil {
		return installStamp{}, false
	}
	return stamp, stamp.Hash != ""
}

// splitFrontmatter separates a "---\n…\n---\n" YAML frontmatter block from
// the markdown body beneath it. A freshly rendered SKILL.md round-trips
// exactly: body == skillBody(), so hashing the returned body reproduces
// skillBodyHash(). split is false when there is no leading frontmatter.
func splitFrontmatter(raw []byte) (front, body string, split bool) {
	s := string(raw)
	if !strings.HasPrefix(s, "---\n") {
		return "", s, false
	}
	rest := s[len("---\n"):]
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return "", s, false
	}
	front = rest[:idx]
	body = strings.TrimLeft(rest[idx+len("\n---\n"):], "\n")
	return front, body, true
}

// installedBodyHash fingerprints an installed file's body the same way
// skillBodyHash fingerprints the current binary's, so a byte-for-byte
// comparison detects both staleness and hand-edits.
func installedBodyHash(raw []byte) string {
	_, body, _ := splitFrontmatter(raw)
	return sha256hex(body)
}

// --- generated command grammar ----------------------------------------

// flagLine is one flag's contribution to the generated grammar.
type flagLine struct {
	Name  string
	Type  string // "" for bool, else "int"/"string"/"duration"/…
	Usage string
}

// flagLines enumerates a command's flags by registering them onto a
// throwaway FlagSet — the same register funcs the real commands use, so the
// grammar cannot list a flag the command lacks (or miss one it has).
func flagLines(register func(*flag.FlagSet)) []flagLine {
	fs := flag.NewFlagSet("grammar", flag.ContinueOnError)
	register(fs)
	var out []flagLine
	fs.VisitAll(func(f *flag.Flag) {
		out = append(out, flagLine{Name: f.Name, Type: flagType(f), Usage: f.Usage})
	})
	return out
}

// flagType derives a flag's type placeholder from its value (not from the
// usage string, which carries markdown backticks UnquoteUsage would
// misread). Bool flags take no placeholder.
func flagType(f *flag.Flag) string {
	g, ok := f.Value.(flag.Getter)
	if !ok {
		return "value"
	}
	switch g.Get().(type) {
	case bool:
		return ""
	case time.Duration:
		return "duration"
	case int, int64:
		return "int"
	case uint, uint64:
		return "uint"
	case float64:
		return "float"
	case string:
		return "string"
	default:
		return "value"
	}
}

// commandGrammar renders the whole command surface as an aligned block. The
// run/resume/status flags come from the real register funcs; spec/diff have
// none; doctor/skill are shown with their fixed shapes.
func commandGrammar() string {
	var b strings.Builder
	writeCmd := func(sig string, lines []flagLine) {
		b.WriteString(sig + "\n")
		width := 0
		for _, fl := range lines {
			if n := flagToken(fl); len(n) > width {
				width = len(n)
			}
		}
		for _, fl := range lines {
			usage := strings.ReplaceAll(fl.Usage, "`", "")
			fmt.Fprintf(&b, "    %-*s  %s\n", width, flagToken(fl), usage)
		}
	}
	writeCmd(`gummi run [flags] "<description>"`, flagLines(func(fs *flag.FlagSet) { registerRunFlags(fs) }))
	b.WriteString("\n")
	writeCmd("gummi resume <id|ref> [decision]", flagLines(func(fs *flag.FlagSet) { registerResumeFlags(fs) }))
	b.WriteString("\n")
	b.WriteString("gummi verify <id|ref>\n\n")
	writeCmd("gummi status <id|ref>", flagLines(func(fs *flag.FlagSet) { registerStatusFlags(fs) }))
	b.WriteString("\n")
	b.WriteString("gummi spec <id|ref>\n\n")
	b.WriteString("gummi diff <id|ref>\n\n")
	writeCmd("gummi doctor", flagLines(func(fs *flag.FlagSet) { registerDoctorFlags(fs) }))
	b.WriteString("\n")
	b.WriteString("gummi skill show|install|list [--agent claude|opencode|copilot] [--scope user|project] [--force] [--dry-run]")
	return b.String()
}

// flagToken formats a flag's --name plus type placeholder.
func flagToken(fl flagLine) string {
	if fl.Type == "" {
		return "--" + fl.Name
	}
	return "--" + fl.Name + " " + fl.Type
}

// exitTable renders the exit contract with codes pulled straight from
// driver.Status.ExitCode(), so the documented codes stay locked to the code.
func exitTable() string {
	rows := []struct {
		s       driver.Status
		meaning string
	}{
		{driver.StatusDone, "verified branch ready — report it upward, stop"},
		{driver.StatusStopped, "--until reached its clean stop — resume --approve to continue"},
		{driver.StatusError, "setup/agent failure — check `status <id>`; resumable if a non-terminal card exists (`resumable` on the error event)"},
		{driver.StatusQuestion, "delegated question or caller gate — resume --answer/--approve/--request-changes"},
		{driver.StatusBlocked, "open %% or diff threads block a gate — resolve, or resume --request-changes"},
		{driver.StatusEscalation, "rerun/critique cap or unclear verdict — report to the human; resumable"},
		{driver.StatusExhausted, "credit envelope dry — resume <id> --envelope N (a larger number) to raise it and continue"},
		{driver.StatusTimeout, "a stage went quiet (likely hang) — report; resumable"},
	}
	var b strings.Builder
	b.WriteString("| Exit | Status | What it means / your action |\n")
	b.WriteString("|------|--------|-----------------------------|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %d | `%s` | %s |\n", r.s.ExitCode(), r.s, r.meaning)
	}
	return strings.TrimRight(b.String(), "\n")
}

// --- install / list ---------------------------------------------------

type skillAgent string

const (
	agentClaude   skillAgent = "claude"
	agentOpencode skillAgent = "opencode"
	agentCopilot  skillAgent = "copilot"
)

// installTarget is one SKILL.md destination and a human label for output.
type installTarget struct {
	path  string
	label string
}

func skillInstall(args []string) error {
	fs := flag.NewFlagSet("skill install", flag.ContinueOnError)
	agentFlag := fs.String("agent", "", "target a specific agent: claude|opencode|copilot (default: detect)")
	scopeFlag := fs.String("scope", "", "install scope: project|user (default: project, or ask when interactive)")
	force := fs.Bool("force", false, "overwrite an existing SKILL.md (default: refuse and warn on drift)")
	dryRun := fs.Bool("dry-run", false, "print what would be written, change nothing")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi skill install [--agent a] [--scope s] [--force] [--dry-run]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	scope, err := resolveScope(*scopeFlag)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	targets, err := resolveTargets(scope, *agentFlag, cwd)
	if err != nil {
		return err
	}
	content := renderSkill(version())
	curHash := skillBodyHash()
	for _, t := range targets {
		if err := installOne(t, content, curHash, *force, *dryRun); err != nil {
			return err
		}
	}
	return nil
}

// installOne writes (or reports) one target. It never overwrites an
// existing file without --force: an identical install is a no-op, a
// differing one warns about drift and points at --force (S5).
func installOne(t installTarget, content []byte, curHash string, force, dryRun bool) error {
	if raw, err := os.ReadFile(t.path); err == nil {
		_, gummiOwned := parseInstalledStamp(raw)
		upToDate := gummiOwned && installedBodyHash(raw) == curHash
		switch {
		case upToDate && !force:
			fmt.Printf("  ✓ %s — already up to date: %s\n", t.label, t.path)
			return nil
		case !force:
			why := "a non-gummi SKILL.md is present"
			if gummiOwned {
				why = "installed skill has drifted (stale or edited)"
			}
			fmt.Printf("  ! %s — %s; re-run with --force to overwrite: %s\n", t.label, why, t.path)
			return nil
		}
	}
	if dryRun {
		fmt.Printf("  · would write %s (%s)\n", t.path, t.label)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o750); err != nil {
		return err
	}
	// SKILL.md is documentation an agent reads, not a secret; 0644 is the
	// conventional mode for a skill file in a shared config dir.
	if err := os.WriteFile(t.path, content, 0o644); err != nil { //nolint:gosec // G306: a public skill doc, world-readable by design
		return err
	}
	fmt.Printf("  ✓ wrote %s (%s)\n", t.path, t.label)
	return nil
}

// skillList reports every known target's install state (absent / up-to-date
// / drift), so a caller (or doctor) can see what needs a --force refresh.
func skillList(args []string) error {
	fs := flag.NewFlagSet("skill list", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, "usage: gummi skill list") }
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	curHash := skillBodyHash()
	rows := []installTarget{
		{path: projectSkillPath(cwd), label: "project (claude/copilot/opencode)"},
		{path: userSkillPath(agentClaude), label: "user (claude/opencode)"},
		{path: userSkillPath(agentCopilot), label: "user (copilot)"},
	}
	for _, r := range rows {
		fmt.Printf("  %-32s %-12s %s\n", r.label, describeInstall(r.path, curHash), r.path)
	}
	return nil
}

// describeInstall classifies one installed file against the current skill.
func describeInstall(path, curHash string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "absent"
	}
	stamp, ok := parseInstalledStamp(raw)
	if !ok {
		return "foreign"
	}
	ver := stamp.Version
	if ver == "" {
		ver = "?"
	}
	if installedBodyHash(raw) == curHash {
		return "up-to-date"
	}
	return "drift (" + ver + ")"
}

// --- detection, scope & paths -----------------------------------------

// detectAgents reports which agents are present via env + PATH (§7). Used
// only for user-scope installs; project scope needs no detection (one file
// covers all three).
func detectAgents() []skillAgent {
	var out []skillAgent
	if os.Getenv("CLAUDECODE") != "" || os.Getenv("CLAUDE_CODE_ENTRYPOINT") != "" ||
		os.Getenv("CLAUDE_CONFIG_DIR") != "" || onPath("claude") {
		out = append(out, agentClaude)
	}
	if hasEnvPrefix("OPENCODE") || onPath("opencode") {
		out = append(out, agentOpencode)
	}
	if onPath("copilot") || onPath("gh") {
		out = append(out, agentCopilot)
	}
	return out
}

func onPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

func hasEnvPrefix(prefix string) bool {
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

func parseAgent(s string) (skillAgent, error) {
	switch skillAgent(s) {
	case agentClaude, agentOpencode, agentCopilot:
		return skillAgent(s), nil
	default:
		return "", fmt.Errorf("--agent must be claude, opencode, or copilot, got %q", s)
	}
}

// resolveScope honors an explicit --scope; otherwise it asks when stdin is
// interactive and defaults to project (the widest-coverage single install)
// when it is not — the agent-driven case (S4).
func resolveScope(flagVal string) (string, error) {
	switch flagVal {
	case "project", "user":
		return flagVal, nil
	case "":
		if interactiveStdin() {
			return promptScope(), nil
		}
		fmt.Fprintln(os.Stderr, "gummi: no --scope given and stdin is not interactive; defaulting to project scope "+
			"(one .claude/skills/gummi install is read by claude, copilot, and opencode).")
		return "project", nil
	default:
		return "", fmt.Errorf("--scope must be project or user, got %q", flagVal)
	}
}

// resolveTargets maps a scope (+ optional --agent) to concrete SKILL.md
// paths. Project scope is one file for all three agents; user scope diverges
// per agent (claude+opencode share the claude home, copilot has its own).
func resolveTargets(scope, agentFlag, cwd string) ([]installTarget, error) {
	if scope == "project" {
		return []installTarget{{
			path:  projectSkillPath(cwd),
			label: "project (read by claude, copilot, opencode)",
		}}, nil
	}
	var agents []skillAgent
	if agentFlag != "" {
		a, err := parseAgent(agentFlag)
		if err != nil {
			return nil, err
		}
		agents = []skillAgent{a}
	} else if agents = detectAgents(); len(agents) == 0 {
		return nil, fmt.Errorf("user scope needs an agent, but none was detected; pass --agent claude|opencode|copilot (or use --scope project)")
	}
	seen := map[string]bool{}
	var targets []installTarget
	for _, a := range agents {
		p := userSkillPath(a)
		if seen[p] {
			continue
		}
		seen[p] = true
		targets = append(targets, installTarget{path: p, label: "user (" + string(a) + ")"})
	}
	return targets, nil
}

// projectSkillPath is the one project-scope install all three agents read.
func projectSkillPath(cwd string) string {
	return filepath.Join(cwd, ".claude", "skills", "gummi", "SKILL.md")
}

// userSkillPath is an agent's user-scope home. Claude and opencode share the
// Claude home ($CLAUDE_CONFIG_DIR, else ~/.claude); Copilot has its own.
func userSkillPath(a skillAgent) string {
	if a == agentCopilot {
		return filepath.Join(homeDir(), ".copilot", "skills", "gummi", "SKILL.md")
	}
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		base = filepath.Join(homeDir(), ".claude")
	}
	return filepath.Join(base, "skills", "gummi", "SKILL.md")
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

// interactiveStdin reports whether stdin is a terminal (the classic
// char-device check; no extra dependency), so install prompts only when a
// human is there to answer.
func interactiveStdin() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// promptScope asks project-vs-user, defaulting to project (recommended).
func promptScope() string {
	fmt.Print("Install scope? [P]roject (recommended, covers all agents) / [u]ser: ")
	var line string
	_, _ = fmt.Scanln(&line)
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "u") {
		return "user"
	}
	return "project"
}
