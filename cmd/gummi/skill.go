package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"

	"github.com/morphis/gummi/internal/driver"
)

// The skill ships as a bundle: SKILL.md plus the reference files it points
// at. Splitting it is what keeps it cheap — SKILL.md is loaded on every
// invocation by the agent driving gummi, while first-run setup, the long
// form of the resume verbs, and everything about goals are each needed by
// a fraction of those invocations. An agent shipping one card reads
// SKILL.md and nothing else.
//
// Every template's command grammar and exit table are generated from the
// live cobra tree and driver.Status, so the shipped doc can never document
// a flag the binary lacks — or miss one it has. A golden + drift test
// (skill_test.go) locks that.
//
//go:embed skill.tmpl.md
var skillTemplate string

//go:embed skill_setup.tmpl.md
var skillSetupTemplate string

//go:embed skill_resume.tmpl.md
var skillResumeTemplate string

//go:embed skill_goals.tmpl.md
var skillGoalsTemplate string

const skillName = "gummi"

// skillAgentList is the --agent value list, shared by the flag's help text
// and its parser so the two cannot disagree about which agents exist.
const skillAgentList = "claude|codex|opencode|copilot|pi"

const skillDescription = "Ship one PR-sized feature or bug to a verified branch via gummi's headless, spec-driven workflow (spec, review, verify; gummi never merges). Use when the work warrants a spec, an independent code review, and an isolated branch — not for trivial one-line edits."

// skillShow prints the rendered SKILL.md (frontmatter + body) to stdout,
// or — given a name — one of the reference files it points at.
func skillShow(args []string) error {
	files := skillBundle()
	switch len(args) {
	case 0:
		_, err := os.Stdout.Write(renderSkill(version()))
		return err
	case 1:
		want := args[0]
		for _, f := range files {
			if f.path == want || f.path == "references/"+want+".md" {
				_, err := io.WriteString(os.Stdout, f.body)
				return err
			}
		}
		var names []string
		for _, f := range files[1:] {
			names = append(names, strings.TrimSuffix(strings.TrimPrefix(f.path, "references/"), ".md"))
		}
		return fmt.Errorf("no skill file %q; the references are: %s", want, strings.Join(names, ", "))
	}
	return fmt.Errorf("skill show takes at most one file name")
}

// --- rendering + version stamp ----------------------------------------

// skillFile is one file of the installed bundle: a path relative to the
// skill directory, and the rendered body that belongs at it.
type skillFile struct {
	path string
	body string
}

// skillBundle renders every file the skill installs, SKILL.md first. It is
// deterministic and version-free, so it is safe to hash and to golden-test.
func skillBundle() []skillFile {
	return []skillFile{
		{"SKILL.md", renderTmpl("skill", skillTemplate)},
		{"references/setup.md", renderTmpl("setup", skillSetupTemplate)},
		{"references/resume.md", renderTmpl("resume", skillResumeTemplate)},
		{"references/goals.md", renderTmpl("goals", skillGoalsTemplate)},
	}
}

// renderTmpl executes one embedded template against the generated sections.
// Every template sees the same data, so a section can move between files
// without rewiring anything.
func renderTmpl(name, text string) string {
	tmpl := template.Must(template.New(name).Parse(text))
	var b strings.Builder
	data := struct {
		Grammar     string
		GoalGrammar string
		ExitTable   string
	}{
		Grammar:     commandGrammar(),
		GoalGrammar: goalGrammar(),
		ExitTable:   exitTable(),
	}
	if err := tmpl.Execute(&b, data); err != nil {
		// the templates are embedded and covered by tests; a runtime
		// failure here is a programmer error, not a user-facing condition.
		panic("rendering " + name + " template: " + err.Error())
	}
	return b.String()
}

// skillBody is SKILL.md's body — the part the frontmatter sits above.
func skillBody() string { return skillBundle()[0].body }

// skillBodyHash fingerprints the WHOLE bundle, not just SKILL.md. It is
// stamped into SKILL.md's frontmatter and compared for drift, so an
// installed skill whose reference file was edited — or whose reference
// file a newer binary added — reads as drifted, exactly as an edited
// SKILL.md does.
func skillBodyHash() string { return bundleHash(skillBundle()) }

// bundleHash hashes a bundle's paths and bodies in order. Lengths are
// hashed alongside the bodies so no rearrangement of content between two
// files can collide with another.
func bundleHash(files []skillFile) string {
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\n%d\n%s", f.path, len(f.body), f.body)
	}
	return hex.EncodeToString(h.Sum(nil))
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

// --- generated command grammar ----------------------------------------

// The grammar is generated from the live cobra tree (root.go), which is
// also what parses a real command line. It therefore cannot document a
// flag the binary rejects, and cannot omit one the binary accepts — both
// of which it used to do, because it enumerated a second, parallel set of
// flag declarations that nothing kept in step with cobra's.

// grammarEntry is one block of the rendered grammar: the signature lines
// to print (two, where one flag surface serves two verbs), the command
// path to read the flags from, and the flags to leave out of this block.
type grammarEntry struct {
	sigs []string
	path string
	omit []string
}

// coreGrammar is the command surface SKILL.md documents: everything an
// agent needs to take one card from a description to a verified branch and
// land it. Goals live in their own reference, and so do their flags.
func coreGrammar() []grammarEntry {
	return []grammarEntry{
		{sigs: []string{`gummi run [flags] "<description>"`}, path: "run"},
		{sigs: []string{`gummi research [flags] "<brief>"`, `gummi diagnose [flags] "<symptom>"`}, path: "research"},
		{sigs: []string{"gummi resume <id|ref> [decision]"}, path: "resume", omit: goalResumeFlagNames},
		{sigs: []string{"gummi verify <id|ref>"}, path: "verify"},
		{sigs: []string{"gummi merge <id|ref> -m <message|->"}, path: "merge"},
		{sigs: []string{"gummi squash <id|ref> -m <message|->"}, path: "squash"},
		{sigs: []string{"gummi commit <id|ref> -m <message|->"}, path: "commit"},
		{sigs: []string{"gummi log <id|ref>"}, path: "log"},
		{sigs: []string{"gummi rewrite <id|ref> --plan <file|->"}, path: "rewrite"},
		{sigs: []string{"gummi handoff <id|ref>"}, path: "handoff"},
		{sigs: []string{"gummi clean <id|ref>"}, path: "clean"},
		{sigs: []string{"gummi status <id|ref>"}, path: "status"},
		{sigs: []string{"gummi watch <id|ref>"}, path: "watch"},
		{sigs: []string{"gummi spec <id|ref>"}, path: "spec"},
		{sigs: []string{"gummi diff <id|ref>"}, path: "diff"},
		{sigs: []string{"gummi doctor"}, path: "doctor"},
		{sigs: []string{"gummi deps add <dependent> <depends-on>"}, path: "deps add"},
		{sigs: []string{"gummi deps rm <dependent> <depends-on>"}, path: "deps rm"},
		{sigs: []string{"gummi deps list <id>"}, path: "deps list"},
		{sigs: []string{"gummi skill show|install|list"}, path: "skill install"},
	}
}

// commandGrammar renders the core grammar, with the driving flags `gummi
// run` and another verb word identically hoisted into one shared block.
//
// Reprinting those ten flags under all five driving verbs was 30% of the
// whole listing — 2.6KB of byte-identical repeats in a document an agent
// loads on every invocation. Hoisting them is not just shorter: a flag now
// appears under a verb precisely when that verb means something different
// by it, so a real difference (a goal's --envelope is the goal's WHOLE
// budget) reads as a difference instead of drowning in restatement.
func commandGrammar() string {
	var b strings.Builder
	b.WriteString("Flags shared by run, research, diagnose and resume — listed again under a\nverb only where that verb means something different by one:\n\n")
	writeFlagLines(&b, sharedFlagLines())
	for _, e := range coreGrammar() {
		b.WriteString("\n")
		writeEntry(&b, e, true)
	}
	return strings.TrimRight(b.String(), "\n")
}

// goalGrammar renders the goal surface for the goals reference: `gummi
// goal` itself plus the `resume` flags that only ever apply to one.
func goalGrammar() string {
	var b strings.Builder
	writeEntry(&b, grammarEntry{sigs: []string{`gummi goal [flags] "<objective>"`}, path: "goal"}, false)
	b.WriteString("\ngummi resume GL-NNN [decision]   (the goal-only flags; every flag in SKILL.md's\n                                 grammar applies to a goal too)\n")
	writeFlagLines(&b, flagLines(mustFindCmd("resume"), keepOnly(goalResumeFlagNames)))
	return strings.TrimRight(b.String(), "\n")
}

// writeEntry renders one block: its signature lines, then its flags.
func writeEntry(b *strings.Builder, e grammarEntry, hoistShared bool) {
	for _, sig := range e.sigs {
		b.WriteString(sig + "\n")
	}
	omit := map[string]bool{}
	for _, n := range e.omit {
		omit[n] = true
	}
	shared := map[string]string{}
	if hoistShared {
		for _, fl := range sharedFlagLines() {
			shared[fl.Name] = fl.Usage
		}
	}
	writeFlagLines(b, flagLines(mustFindCmd(e.path), func(fl flagLine) bool {
		if omit[fl.Name] {
			return false
		}
		// A shared flag is printed under a verb only where that verb
		// words it differently — which is exactly where it means
		// something different.
		if u, ok := shared[fl.Name]; ok && u == fl.Usage {
			return false
		}
		return true
	}))
}

// sharedFlagLines is the shared driving surface as `gummi run` words it —
// the reference wording every other verb is compared against.
func sharedFlagLines() []flagLine {
	want := map[string]bool{}
	for _, n := range sharedDriveFlagNames {
		want[n] = true
	}
	return flagLines(mustFindCmd("run"), func(fl flagLine) bool { return want[fl.Name] })
}

// keepOnly builds a flagLines filter admitting exactly the named flags.
func keepOnly(names []string) func(flagLine) bool {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	return func(fl flagLine) bool { return want[fl.Name] }
}

// mustFindCmd resolves a space-separated command path against the cobra
// tree. A path that does not resolve is a programmer error in
// coreGrammar's table, caught by the grammar's own tests.
func mustFindCmd(path string) *cobra.Command {
	cmd, _, err := rootCmd.Find(strings.Fields(path))
	if err != nil || cmd == rootCmd {
		panic("skill grammar names a command that is not on the tree: " + path)
	}
	return cmd
}

// flagLine is one flag's contribution to the generated grammar.
type flagLine struct {
	Name  string
	Short string // one-letter shorthand, where the flag has one
	Type  string // "" for bool, else "int"/"string"/"duration"/…
	Usage string
}

// flagLines enumerates the flags cobra has bound on cmd, in pflag's sorted
// order, keeping those keep admits. cobra's own --help is never part of
// the grammar.
func flagLines(cmd *cobra.Command, keep func(flagLine) bool) []flagLine {
	var out []flagLine
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Name == "help" {
			return
		}
		fl := flagLine{Name: f.Name, Short: f.Shorthand, Type: flagType(f), Usage: f.Usage}
		if keep == nil || keep(fl) {
			out = append(out, fl)
		}
	})
	return out
}

// flagType is the value placeholder shown after a flag name. Bool flags
// take none; pflag names every other type itself.
func flagType(f *pflag.Flag) string {
	if f.Value.Type() == "bool" {
		return ""
	}
	return f.Value.Type()
}

// writeFlagLines renders a block of flags, aligned on the widest token.
func writeFlagLines(b *strings.Builder, lines []flagLine) {
	width := 0
	for _, fl := range lines {
		if n := len(flagToken(fl)); n > width {
			width = n
		}
	}
	for _, fl := range lines {
		fmt.Fprintf(b, "    %-*s  %s\n", width, flagToken(fl), strings.ReplaceAll(fl.Usage, "`", ""))
	}
}

// flagToken formats a flag's --name plus type placeholder, with the
// shorthand where one exists (`-m, --message string`).
func flagToken(fl flagLine) string {
	tok := "--" + fl.Name
	if fl.Short != "" {
		tok = "-" + fl.Short + ", " + tok
	}
	if fl.Type != "" {
		tok += " " + fl.Type
	}
	return tok
}

// exitTable renders the exit contract with codes pulled straight from
// driver.Status.ExitCode(), so the documented codes stay locked to the code.
func exitTable() string {
	rows := []struct {
		s       driver.Status
		meaning string
	}{
		{driver.StatusVerified, "verified branch ready — report it upward, stop"},
		{driver.StatusStopped, "clean `--until` stop — `resume <id> --approve` crosses the gate and continues"},
		{driver.StatusError, "setup/agent failure — check `status <id>`; resumable if a non-terminal card exists (`resumable` on the error event)"},
		{driver.StatusQuestion, "a delegated ask (`question` event) → `resume <id> --answer <text>`; a caller gate (`gate` event) → `resume <id> --approve` or `--request-changes <note>`"},
		{driver.StatusBlocked, "open %% or diff threads block a gate — resolve them, then `resume <id>`; or `resume <id> --request-changes <note>` to bounce it back"},
		{driver.StatusEscalation, "rerun/critique cap or unclear verdict — report to the human; once they address it, `resume <id>` re-runs the stage. A verify-fail (or review cap-hit) carries `next: resume <id> --bounce --note \"<why>\"`, which rewinds the feature to implement/fix and drives review→verify again"},
		{driver.StatusExhausted, "budget dry — `resume <id> --envelope <dollars>` (more than the dry budget) raises it and continues"},
		{driver.StatusTimeout, "a stage went quiet — read the event `hint`, report to the human; `resume <id>` re-runs the stage"},
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
	agentCodex    skillAgent = "codex"
	agentOpencode skillAgent = "opencode"
	agentCopilot  skillAgent = "copilot"
	agentPi       skillAgent = "pi"
)

// installTarget is one skill-bundle destination — the `gummi` skill
// directory, which holds SKILL.md and its references/ — and a human label
// for output.
type installTarget struct {
	dir   string
	label string
}

// skillPath is the target's SKILL.md, the file that carries the stamp.
func (t installTarget) skillPath() string { return filepath.Join(t.dir, "SKILL.md") }

// installBundle is the bundle as it goes to disk: SKILL.md carries the
// frontmatter stamp, every reference file is its body verbatim.
func installBundle(version string) []skillFile {
	files := skillBundle()
	out := make([]skillFile, len(files))
	out[0] = skillFile{path: files[0].path, body: string(renderSkill(version))}
	copy(out[1:], files[1:])
	return out
}

func skillInstall(fl cliFlags) error {
	scope, err := resolveScope(fl.String("scope"))
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	ws, _, err := resolveRoots(cwd)
	if err != nil {
		return err
	}
	if scope == "project" {
		if _, ok := findGummiRoot(cwd); !ok {
			return fmt.Errorf("skill install --scope project: no gummi workspace found at or above %s; run `gummi init` first, or pass --scope user", cwd)
		}
	}
	targets, err := resolveTargets(scope, fl.String("agent"), ws)
	if err != nil {
		return err
	}
	curHash := skillBodyHash()
	if fl.Bool("check") {
		return checkTargets(targets, curHash)
	}
	bundle := installBundle(version())
	for _, t := range targets {
		if err := installOne(t, bundle, curHash, fl.Bool("force"), fl.Bool("dry-run")); err != nil {
			return err
		}
	}
	return nil
}

// checkTargets is the --check drift guard: it writes nothing and reports
// every target whose install state isn't "up-to-date" (absent/foreign/
// drifted), printing an OK line for each target that is. Used by CI to fail
// when a committed SKILL.md has drifted from what the current binary would
// generate.
func checkTargets(targets []installTarget, curHash string) error {
	var stale []string
	for _, t := range targets {
		status := describeInstall(t.dir, curHash)
		if status == "up-to-date" {
			fmt.Printf("  ✓ %s — up to date: %s\n", t.label, t.dir)
			continue
		}
		fmt.Printf("  ✗ %s — %s: %s\n", t.label, status, t.dir)
		stale = append(stale, t.dir)
	}
	if len(stale) > 0 {
		return fmt.Errorf("skill install --check: out of date: %s", strings.Join(stale, ", "))
	}
	return nil
}

// installOne writes (or reports) one target's whole bundle. It never
// overwrites an existing install without --force: an identical one is a
// no-op, a differing one warns about drift and points at --force (S5).
func installOne(t installTarget, bundle []skillFile, curHash string, force, dryRun bool) error {
	if raw, err := os.ReadFile(t.skillPath()); err == nil {
		_, gummiOwned := parseInstalledStamp(raw)
		onDisk, complete := installedBundleHash(t.dir)
		upToDate := gummiOwned && complete && onDisk == curHash
		switch {
		case upToDate && !force:
			fmt.Printf("  ✓ %s — already up to date: %s\n", t.label, t.dir)
			return nil
		case !force:
			why := "a non-gummi SKILL.md is present"
			if gummiOwned {
				why = "installed skill has drifted (stale or edited)"
			}
			fmt.Printf("  ! %s — %s; re-run with --force to overwrite: %s\n", t.label, why, t.dir)
			return nil
		}
	}
	if dryRun {
		for _, f := range bundle {
			fmt.Printf("  · would write %s (%s)\n", filepath.Join(t.dir, f.path), t.label)
		}
		return nil
	}
	for _, f := range bundle {
		path := filepath.Join(t.dir, f.path)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		// A skill is documentation an agent reads, not a secret; 0644 is
		// the conventional mode for one in a shared config dir.
		if err := os.WriteFile(path, []byte(f.body), 0o644); err != nil { //nolint:gosec // G306: a public skill doc, world-readable by design
			return err
		}
	}
	fmt.Printf("  ✓ wrote %s (%d files, %s)\n", t.dir, len(bundle), t.label)
	return nil
}

// installedBundleHash fingerprints what is on disk at dir the same way
// bundleHash fingerprints what this binary would write, so a byte-for-byte
// comparison detects staleness and hand-edits — in a reference file as
// readily as in SKILL.md. complete is false when a file the current bundle
// has is missing there, which is how an install from an older binary that
// knew fewer files reads as drift rather than as up to date.
func installedBundleHash(dir string) (hash string, complete bool) {
	want := skillBundle()
	got := make([]skillFile, 0, len(want))
	for _, f := range want {
		raw, err := os.ReadFile(filepath.Join(dir, f.path))
		if err != nil {
			return "", false
		}
		body := string(raw)
		if f.path == "SKILL.md" {
			_, body, _ = splitFrontmatter(raw)
		}
		got = append(got, skillFile{path: f.path, body: body})
	}
	return bundleHash(got), true
}

// skillList reports every known target's install state (absent / up-to-date
// / drift), so a caller (or doctor) can see what needs a --force refresh.
func skillList() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	ws, _, err := resolveRoots(cwd)
	if err != nil {
		return err
	}
	curHash := skillBodyHash()
	rows := []installTarget{
		{dir: projectSkillDir(ws), label: "project (claude/copilot/opencode)"},
		{dir: codexProjectSkillDir(ws), label: "project (codex, pi)"},
		{dir: userSkillDir(agentClaude), label: "user (claude/opencode)"},
		{dir: userSkillDir(agentCopilot), label: "user (copilot)"},
		{dir: userSkillDir(agentCodex), label: "user (codex, pi)"},
	}
	for _, r := range rows {
		fmt.Printf("  %-32s %-12s %s\n", r.label, describeInstall(r.dir, curHash), r.dir)
	}
	return nil
}

// describeInstall classifies one installed bundle against the current one.
func describeInstall(dir, curHash string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
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
	if h, complete := installedBundleHash(dir); complete && h == curHash {
		return "up-to-date"
	}
	return "drift (" + ver + ")"
}

// --- detection, scope & paths -----------------------------------------

// detectAgents reports which agents are present via env + PATH (§7). Used
// only for user-scope installs; project scope needs no detection.
func detectAgents() []skillAgent {
	var out []skillAgent
	if os.Getenv("CLAUDECODE") != "" || os.Getenv("CLAUDE_CODE_ENTRYPOINT") != "" ||
		os.Getenv("CLAUDE_CONFIG_DIR") != "" || onPath("claude") {
		out = append(out, agentClaude)
	}
	if hasEnvPrefix("OPENCODE") || onPath("opencode") {
		out = append(out, agentOpencode)
	}
	if os.Getenv("CODEX_HOME") != "" || hasEnvPrefix("CODEX_") || onPath("codex") {
		out = append(out, agentCodex)
	}
	if onPath("copilot") || onPath("gh") {
		out = append(out, agentCopilot)
	}
	if onPath("pi") || os.Getenv("PI_CODING_AGENT_DIR") != "" {
		out = append(out, agentPi)
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
	case agentClaude, agentCodex, agentOpencode, agentCopilot, agentPi:
		return skillAgent(s), nil
	default:
		return "", fmt.Errorf("--agent must be one of %s, got %q", skillAgentList, s)
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
			"(project scope installs the shared skill for claude/copilot/opencode and the Codex skill under .agents/skills).")
		return "project", nil
	default:
		return "", fmt.Errorf("--scope must be project or user, got %q", flagVal)
	}
}

// resolveTargets maps a scope (+ optional --agent) to concrete SKILL.md
// paths. Project scope uses the shared .claude path plus Codex's .agents
// path, both rooted at the gummi workspace (beside .gummi) rather than
// whatever repo the caller happens to be standing in; user scope diverges
// per agent (claude+opencode share the claude home).
func resolveTargets(scope, agentFlag, ws string) ([]installTarget, error) {
	if scope == "project" {
		shared := installTarget{
			dir:   projectSkillDir(ws),
			label: "project (read by claude, copilot, opencode)",
		}
		codex := installTarget{dir: codexProjectSkillDir(ws), label: "project (read by codex)"}
		switch agentFlag {
		case "":
			return []installTarget{shared, codex}, nil
		case string(agentCodex):
			return []installTarget{codex}, nil
		default:
			if _, err := parseAgent(agentFlag); err != nil {
				return nil, err
			}
			return []installTarget{shared}, nil
		}
	}
	var agents []skillAgent
	if agentFlag != "" {
		a, err := parseAgent(agentFlag)
		if err != nil {
			return nil, err
		}
		agents = []skillAgent{a}
	} else if agents = detectAgents(); len(agents) == 0 {
		return nil, fmt.Errorf("user scope needs an agent, but none was detected; pass --agent %s (or use --scope project)", skillAgentList)
	}
	seen := map[string]bool{}
	var targets []installTarget
	for _, a := range agents {
		p := userSkillDir(a)
		if seen[p] {
			continue
		}
		seen[p] = true
		targets = append(targets, installTarget{dir: p, label: "user (" + string(a) + ")"})
	}
	return targets, nil
}

// projectSkillDir is the shared project-scope install Claude, Copilot, and
// opencode read, rooted at the gummi workspace (beside .gummi).
func projectSkillDir(ws string) string {
	return filepath.Join(ws, ".claude", "skills", "gummi")
}

// codexProjectSkillDir is Codex's workspace-scoped skill location, rooted
// the same way as projectSkillDir.
func codexProjectSkillDir(ws string) string {
	return filepath.Join(ws, ".agents", "skills", "gummi")
}

// userSkillDir is an agent's user-scope home. Claude and opencode share the
// Claude home ($CLAUDE_CONFIG_DIR, else ~/.claude); Copilot and Codex
// each have their own native homes.
func userSkillDir(a skillAgent) string {
	if a == agentCopilot {
		return filepath.Join(homeDir(), ".copilot", "skills", "gummi")
	}
	// pi reads the shared agents-skills locations natively (~/.agents/skills
	// and project .agents/skills), alongside its own ~/.pi/agent/skills —
	// the codex install is what a pi user wants, so the two share a target
	// rather than seeding pi a duplicate of the same skill.
	if a == agentCodex || a == agentPi {
		return filepath.Join(homeDir(), ".agents", "skills", "gummi")
	}
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		base = filepath.Join(homeDir(), ".claude")
	}
	return filepath.Join(base, "skills", "gummi")
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
