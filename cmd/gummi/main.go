// Command gummi is a meta-harness for coding agents: it drives a fleet of
// agents through a spec-driven workflow across git worktrees, from one TUI.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/notify"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/worktree"
)

// Version is the release version, injected via -ldflags at build time.
// When built without ldflags (go run, go install), it falls back to the
// module version recorded by the Go toolchain, or "devel".
var Version = ""

func version() string {
	if Version != "" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "devel"
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		// A driver invocation reports its typed exit via exitError, having
		// already told the story on the NDJSON stream — exit with that code
		// and stay quiet. Everything else is a setup/usage failure.
		var ec *exitError
		if errors.As(err, &ec) {
			os.Exit(ec.code)
		}
		fmt.Fprintln(os.Stderr, "gummi:", err)
		os.Exit(1)
	}
}

// exitError carries a driver invocation's typed exit code up to main
// without a stderr line — the NDJSON stream is the report.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

func run(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-v":
			fmt.Printf("gummi %s\n", version())
			return nil
		case "ingest":
			return runIngest(args[1:])
		case "bugs":
			return runBugs(args[1:])
		case "run":
			return runRun(args[1:])
		case "resume":
			return runResume(args[1:])
		case "verify":
			return runVerify(args[1:])
		case "status":
			return runStatus(args[1:])
		case "spec":
			return runSpec(args[1:])
		case "diff":
			return runDiff(args[1:])
		case "doctor":
			return runDoctor(args[1:])
		case "skill":
			return runSkill(args[1:])
		default:
			return fmt.Errorf("unknown argument %q (usage: gummi [version|ingest|bugs|run|resume|verify|status|spec|diff|doctor|skill])", args[0])
		}
	}
	// `gummi` with no arguments launches the board, creating the .gummi
	// workspace lazily on first run.
	return runBoard()
}

func runBoard() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	ws, err := ensureWorkspace(cwd)
	if err != nil {
		return err
	}
	// Hold the workspace's exclusive lock for the TUI's lifetime, so a
	// headless `gummi run`/`resume` refuses to touch the same .gummi while
	// the board is open (and vice versa) — DESIGN §8.2 D13.
	release, err := state.AcquireLock(ws.LockFile())
	if err != nil {
		return err
	}
	defer release()
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		return err
	}
	defer store.Close()
	wt, err := newManager(context.Background(), cwd)
	if err != nil {
		return err
	}
	// GUMMI_THEME selects the palette (dark|light|neon); default dark.
	th, _ := theme.ByName(cmp.Or(os.Getenv("GUMMI_THEME"), "dark"))
	shell := ui.NewShell(th, version())
	shell.Attach(store, wt, ws)

	// Wire the agent engine best-effort: a missing/unstartable CLI just
	// leaves the board static (chat reports "no agent configured").
	if eng, names, cleanup := buildEngine(store, wt, ws); eng != nil {
		shell.AttachEngine(eng)
		shell.SetProfileNames(names)
		defer cleanup()
	}
	// layer-3 budget: new features get this credit envelope, drawn on by
	// every stage until it runs dry and a human gate offers a top-up.
	if v := os.Getenv("GUMMI_ENVELOPE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if float64(n) < domain.TurnReserveCredits {
				fmt.Fprintf(os.Stderr, "gummi: GUMMI_ENVELOPE=%d is below one agent turn (~%d credits); "+
					"stage budgets will be floored at a turn and overshoot the envelope\n", n, int(domain.TurnReserveCredits))
			}
			shell.SetEnvelope(n)
		}
	}
	// needs-attention notification hook: GUMMI_NOTIFY=bell|desktop|off
	// (default bell when unset). Escapes go to stderr so they reach the
	// terminal without disturbing the render surface.
	notifyMode := notify.Bell
	if v := os.Getenv("GUMMI_NOTIFY"); v != "" {
		notifyMode = notify.ParseMode(v)
	}
	shell.SetNotifier(notify.New(notifyMode, os.Stderr))
	// GUMMI_COPILOT_HINT=off hides the status-bar Copilot quota pill
	// (on by default; it needs an authenticated gh CLI to show anything).
	if strings.EqualFold(os.Getenv("GUMMI_COPILOT_HINT"), "off") {
		shell.SetCopilotHint(false)
	}

	_, err = tea.NewProgram(shell).Run()
	return err
}

// buildEngine constructs the agent orchestrator from environment
// config. Returns (nil, nil) when no agent can be started, so the board
// degrades to static rather than failing to launch.
//
// Env config (M1 stand-in for profiles):
//
//	GUMMI_MODEL             model id (default "gpt-5")
//	GUMMI_AGENT             default backend (copilot|claude|codex|opencode|headless)
//	GUMMI_HEADLESS_CREDITS_PER_1K
//	                        headless adapter's token→credit rate, for a
//	                        local endpoint (llama.cpp) that the engine
//	                        still needs to meter against a credit budget
func buildEngine(store *state.Store, wt *worktree.Manager, ws state.Workspace) (*engine.Engine, []string, func()) {
	eng, agents, names := newEngineFromEnv(store, wt, ws)
	if eng == nil {
		return nil, nil, nil
	}
	// reload any sessions from a previous run so the board shows where
	// each feature left off.
	if err := eng.Restore(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "gummi: restoring sessions:", err)
	}
	return eng, names, func() {
		_ = eng.Close()
		// close every distinct agent exactly once (the "" default key
		// aliases one of the concrete-name entries).
		seen := map[agent.Agent]struct{}{}
		for _, a := range agents {
			if _, ok := seen[a]; ok {
				continue
			}
			seen[a] = struct{}{}
			_ = a.Close()
		}
	}
}

// newEngineFromEnv constructs the orchestrator and its agents from the
// environment, without restoring prior sessions. buildEngine wraps it for
// the board (adding Restore + a combined cleanup); one-shot commands like
// `gummi ingest` use it directly and own the agents' lifetimes. Returns
// (nil, nil, nil) when no agent can be started.
func newEngineFromEnv(store *state.Store, wt *worktree.Manager, ws state.Workspace) (*engine.Engine, map[string]agent.Agent, []string) {
	// per-role model routing from .gummi/profiles.yaml (falls back to
	// the single env model when absent or a role isn't covered)
	profiles, err := config.LoadProfiles(ws.ProfilesFile())
	if err != nil {
		fmt.Fprintln(os.Stderr, "gummi:", err)
	}
	// Adapter selection: GUMMI_AGENT picks the default backend, and any
	// distinct `backend:` referenced across the loaded profiles is
	// started too. The map is keyed by adapter name, and the default is
	// duplicated under the "" key so the engine's fallback lookup works.
	agents, err := buildAgents(profiles)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gummi:", err)
		return nil, nil, nil
	}
	if len(agents) == 0 {
		return nil, nil, nil
	}
	model := cmp.Or(os.Getenv("GUMMI_MODEL"), "gpt-5")
	maxActive := 1
	if v := os.Getenv("GUMMI_MAX_ACTIVE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxActive = n
		}
	}
	var stageBudget float64
	if v := os.Getenv("GUMMI_STAGE_BUDGET"); v != "" {
		if b, err := strconv.ParseFloat(v, 64); err == nil && b > 0 {
			stageBudget = b
		}
	}
	// one turn's credits, the floor for envelope-derived stage budgets
	// (default domain.TurnReserveCredits; override for unusual models)
	var turnReserve float64
	if v := os.Getenv("GUMMI_TURN_RESERVE"); v != "" {
		if b, err := strconv.ParseFloat(v, 64); err == nil && b > 0 {
			turnReserve = b
		}
	}
	// Honor the repo's permission mode from config.yaml. Without this the
	// parsed value was inert and "permissions: guarded" silently ran allow-all.
	perm := agent.PermissionAllowAll
	if cfg, err := config.Load(ws.ConfigFile()); err != nil {
		fmt.Fprintln(os.Stderr, "gummi:", err)
	} else if cfg.Guarded() {
		perm = agent.PermissionGuarded
	}
	eng := engine.New(engine.Config{
		Agents: agents, Store: store, Worktrees: wt, Workspace: ws,
		Model: model, MaxActive: maxActive, Persist: true,
		Profiles: profiles, StageBudget: stageBudget, TurnReserve: turnReserve,
		Permission: perm,
	})
	// Names() already orders the declared default first (the rest sorted) so
	// index 0 is the intended default for the forms and the CLI --profile
	// fallback. Re-sorting alphabetically here would silently pick the wrong
	// default (e.g. "premium" ahead of the configured "thrifty").
	names := profiles.Names()
	return eng, agents, names
}

// defaultBackendName returns the backend name selected by GUMMI_AGENT
// (or "copilot" when unset). For back-compat, GUMMI_AGENT_CMD without
// GUMMI_AGENT selects headless.
func defaultBackendName() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GUMMI_AGENT"))) {
	case "claude":
		return "claude"
	case "opencode":
		return "opencode"
	case "codex":
		return "codex"
	case "headless":
		return "headless"
	}
	if strings.TrimSpace(os.Getenv("GUMMI_AGENT_CMD")) != "" {
		return "headless"
	}
	return "copilot"
}

// startAdapter starts one named backend. Command lines for headless are
// split on spaces (operator config, not untrusted input); use a wrapper
// script for arguments containing spaces.
func startAdapter(name string) (agent.Agent, error) {
	switch name {
	case "claude":
		return agent.NewClaudeCode(os.Getenv("GUMMI_CLAUDE_BIN"))
	case "opencode":
		return agent.NewOpencode(os.Getenv("GUMMI_OPENCODE_BIN"))
	case "codex":
		return agent.NewCodex(os.Getenv("GUMMI_CODEX_BIN"))
	case "headless":
		return agent.NewHeadless(strings.Fields(os.Getenv("GUMMI_AGENT_CMD")))
	case "copilot":
		return agent.NewCopilot(context.Background(), agent.CopilotOptions{LogLevel: "error"})
	}
	return nil, fmt.Errorf("unknown backend %q", name)
}

// requiredBackends returns the set of backend names the loaded profiles
// actually need, expanding a role's omitted `backend:` field to the
// engine default. With no profiles at all every role falls through to the
// default, so it is always required. It is the single place that decides
// whether the default backend must start: it is needed only when some
// role lacks an explicit backend or a profile references it directly —
// never when every role in every profile names a different backend.
func requiredBackends(def string, profiles config.Profiles) map[string]struct{} {
	needed := map[string]struct{}{}
	if len(profiles.Profiles) == 0 {
		needed[def] = struct{}{}
	}
	for _, prof := range profiles.Profiles {
		for _, rc := range prof {
			name := rc.Backend
			if name == "" {
				name = def
			}
			needed[name] = struct{}{}
		}
	}
	return needed
}

// buildAgents starts exactly the backends the loaded profiles reference,
// returning them keyed by adapter name: the default first when it is
// required (aliased under the empty-string key so engine.agentFor("")
// resolves), then every distinct profile backend. A default that no role
// needs is not started at all, so gummi works with all-non-default
// backends (e.g. opencode/headless) even when the default CLI — copilot,
// when GUMMI_AGENT is unset — is not installed. If a profile-referenced
// backend fails to start, its error is reported and it is skipped; only
// sessions routed at that missing backend fail at newAgentSession.
func buildAgents(profiles config.Profiles) (map[string]agent.Agent, error) {
	def := defaultBackendName()
	agents := map[string]agent.Agent{}

	needed := requiredBackends(def, profiles)
	if _, ok := needed[def]; ok {
		ag, err := startAdapter(def)
		if err != nil {
			return nil, err
		}
		agents[def] = ag
		agents[""] = ag // default alias, matches engine.agentFor's fallback
		delete(needed, def)
	}

	// start the remaining required backends
	for name := range needed {
		a, err := startAdapter(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gummi: skipping backend %q: %v\n", name, err)
			continue
		}
		agents[name] = a
	}
	return agents, nil
}

// newManager binds the worktree manager to cwd and keeps .gummi out of
// the product repo's tracking (exclude + untrack-if-tracked) before any
// agent session can touch the repo. Exclusion problems warn rather than
// block the launch: the board still works.
func newManager(ctx context.Context, cwd string) (*worktree.Manager, error) {
	wt, err := worktree.NewManager(ctx, cwd)
	if err != nil {
		return nil, err
	}
	if untracked, err := wt.EnsureGummiExcluded(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "gummi: excluding .gummi from tracking:", err)
	} else if untracked {
		fmt.Fprintln(os.Stderr, "gummi: .gummi was tracked in this repo — untracked it (index only; the removal rides into your next commit)")
	}
	return wt, nil
}

// ensureWorkspace returns the .gummi workspace at cwd, creating it (and
// scaffolding config.yaml + profiles.yaml) on first run. Idempotent: an
// existing workspace and its files are left untouched. cwd must be a git
// repository root (gummi manages worktrees).
func ensureWorkspace(cwd string) (state.Workspace, error) {
	w, err := state.Init(cwd)
	if err != nil {
		return state.Workspace{}, err
	}
	for _, f := range []struct{ path, body string }{
		{w.ConfigFile(), config.Template},
		{w.ProfilesFile(), config.ProfilesTemplate},
	} {
		if _, err := os.Stat(f.path); os.IsNotExist(err) {
			if err := os.WriteFile(f.path, []byte(f.body), 0o600); err != nil {
				return state.Workspace{}, fmt.Errorf("writing %s: %w", filepath.Base(f.path), err)
			}
		}
	}
	return w, nil
}
