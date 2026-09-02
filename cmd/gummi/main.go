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
	"github.com/morphis/gummi/internal/agentcli"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/notify"
	"github.com/morphis/gummi/internal/pr"
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

// run executes the CLI for args, delegating dispatch to the cobra command
// tree (root.go). It stays a standalone function taking explicit args so the
// tests and any embedder can drive the CLI without touching os.Args. `gummi`
// with no arguments launches the board, creating the .gummi workspace lazily
// on first run.
func run(args []string) error {
	rootCmd.SetArgs(args)
	return rootCmd.Execute()
}

func runBoard() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	wsRoot, defaultRoot, named, err := resolveAllRoots(cwd)
	if err != nil {
		return err
	}
	ws, err := ensureWorkspace(wsRoot, defaultRoot)
	if err != nil {
		return err
	}
	// Hold the workspace's exclusive lock for the TUI's lifetime so a second
	// interactive board refuses to open while one is up. It does NOT block
	// headless run/resume/verify/merge/clean: those hold a per-card lock for
	// the card they drive, so independent cards run while the board is open.
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
	pool, err := newPool(context.Background(), wsRoot, defaultRoot, named, store, true)
	if err != nil {
		return err
	}
	// GUMMI_THEME selects the palette (dark|light|neon); default dark.
	th, _ := theme.ByName(cmp.Or(os.Getenv("GUMMI_THEME"), "dark"))
	shell := ui.NewShell(th, version())
	shell.Attach(store, pool, ws)
	// One registry of per-card locks for the whole board, shared by the
	// engine (which holds a card while it drives it) and the board's own
	// git verbs (merge, rebase, clean, …). Sharing it is what lets a merge
	// on a card this board is already driving join the lock instead of
	// deadlocking against this very process.
	locks := state.NewCardLocks(ws)
	shell.AttachCardLocks(locks)

	// Profile names for the new-feature/bug/ingest dialogs come purely
	// from .gummi/profiles.yaml and are available whether or not any agent
	// backend can start — surface them unconditionally so the dialogs show
	// the real profiles even on a static board.
	shell.SetProfileNames(profileNames(ws))
	// The configured managed repositories feed the new-card forms' repo
	// selector; the default is always implicit, so only named repos here.
	shell.SetRepoNames(pool.Names())
	// The agent tab's hosted-CLI choice: a prior picker answer persisted
	// to config.yaml's `agent:` key, or empty when nothing has been
	// chosen yet (in which case MaybeShowAgentPicker below shows the
	// picker).
	shell.SetAgentConfig(configuredAgentCLI(ws), ws.ConfigFile())
	// Wire the agent engine best-effort: a missing/unstartable CLI just
	// leaves the board static (chat reports "no agent configured").
	if eng, _, cleanup := buildEngine(store, pool, ws, locks); eng != nil {
		shell.AttachEngine(eng)
		defer cleanup()
		// The workspace MCP endpoint is what lets a coding agent hosted in
		// the agent tab drive *this* gummi rather than starting a second
		// one: its `gummi __mcp --workspace` child dials this socket and
		// every tool call lands on the engine above, sharing its card
		// locks instead of contending for them. Best-effort, like the
		// engine itself — a board that cannot bind it still runs, the
		// hosted agent just gets no gummi tools.
		if sock, teardown, err := eng.StartWorkspaceMCPEndpoint(); err != nil {
			fmt.Fprintf(os.Stderr, "gummi: workspace agent tools unavailable: %v\n", err)
		} else {
			shell.SetAgentMCPSock(sock)
			defer teardown()
		}
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
	// prlink/prpull need a real answer from GitHub, not the local
	// approximation openReviewThreads' nil fallback uses for the
	// warn-before-squash check — wire them to the same internal/pr
	// functions cmd/gummi/pr.go's link/comments verbs already call.
	shell.SetPRResolver(func(ctx context.Context, spec, repoDir, branch string) (domain.PullRequestRef, error) {
		return pr.Resolve(ctx, pr.GHBinary(), spec, repoDir, branch)
	})
	shell.SetPRThreadFetcher(func(ctx context.Context, ref domain.PullRequestRef) ([]pr.ReviewThread, []pr.TopLevelComment, string, error) {
		return pr.FetchReviewThreads(ctx, pr.GHBinary(), ref)
	})
	shell.SetPRSquashMergeChecker(func(ctx context.Context, repo string) (bool, error) {
		return pr.RepoAllowsSquashMerge(ctx, pr.GHBinary(), repo)
	})
	// GUMMI_COPILOT_HINT=off hides the status-bar Copilot quota pill
	// (on by default; it needs an authenticated gh CLI to show anything).
	if strings.EqualFold(os.Getenv("GUMMI_COPILOT_HINT"), "off") {
		shell.SetCopilotHint(false)
	}
	// GUMMI_MOTION=off freezes every activity glyph in the UI to its
	// static first frame and stops the shared clock's tick loop from
	// ever starting (on by default).
	if strings.EqualFold(os.Getenv("GUMMI_MOTION"), "off") {
		shell.SetMotion(false)
	}
	// First-run ask for the agent tab's hosted CLI: a no-op once
	// GUMMI_ATTACH_CMD/GUMMI_AGENT is set or a prior choice is recorded in
	// config.yaml's `agent:` key. Must run before Run() starts (see
	// MaybeShowAgentPicker's own comment for why that ordering matters).
	shell.MaybeShowAgentPicker()

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
//	GUMMI_AGENT             default backend (copilot|claude|codex|opencode|headless|zz)
//	GUMMI_HEADLESS_CREDITS_PER_1K
//	                        headless adapter's token→credit rate, for a
//	                        local endpoint (llama.cpp) that the engine
//	                        still needs to meter against a credit budget
func buildEngine(store *state.Store, pool *worktree.Pool, ws state.Workspace, locks *state.CardLocks) (*engine.Engine, []string, func()) {
	eng, agents, names, err := newEngineFromEnv(store, pool, ws)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gummi:", err)
	}
	if eng == nil {
		return nil, nil, nil
	}
	// The board drives cards for as long as it is open, so it takes each
	// card's lock the way a headless command does — before the first
	// session, so a `gummi run` for the same card is refused rather than
	// racing it (and vice versa).
	eng.UseCardLocks(locks)
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
// The returned error is non-nil only when permissions: guarded is paired
// with a backend that can't honor it (agent.GuardedSupport) — every other
// nil-eng path (buildAgents failure, zero agents configured) keeps
// returning a nil error, so callers that already have their own generic
// "no coding agent is configured" message for those cases are unaffected.
func newEngineFromEnv(store *state.Store, pool *worktree.Pool, ws state.Workspace) (*engine.Engine, map[string]agent.Agent, []string, error) {
	// per-role model routing from .gummi/profiles.yaml (falls back to
	// the single env model when absent or a role isn't covered)
	profiles, err := config.LoadProfiles(ws.ProfilesFile())
	if err != nil {
		fmt.Fprintln(os.Stderr, "gummi:", err)
	}
	// Honor the repo's permission mode from config.yaml. Without this the
	// parsed value was inert and "permissions: guarded" silently ran allow-all.
	// Loaded before buildAgents so a guarded/backend mismatch is caught
	// before any backend process starts.
	perm := agent.PermissionAllowAll
	var sandboxMode string
	var instructions []string
	// autopilotLanesCfg is the configured autopilot_lanes value (0 = unset,
	// resolved to the built-in default of 2 below).
	var autopilotLanesCfg int
	userPath, err := config.UserConfigPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gummi:", err)
		userPath = ""
	}
	if cfg, _, err := config.LoadLayered(userPath, ws.ConfigFile()); err != nil {
		fmt.Fprintln(os.Stderr, "gummi:", err)
	} else {
		if cfg.Guarded() {
			perm = agent.PermissionGuarded
			if issues := guardedIncompatibilities(defaultBackendName(), profiles); len(issues) > 0 {
				return nil, nil, nil, fmt.Errorf("permissions: guarded is incompatible with the resolved backend for %s",
					formatGuardedIncompatibilities(issues))
			}
		}
		sandboxMode = cfg.Sandbox
		instructions = cfg.Instructions
		autopilotLanesCfg = cfg.AutopilotLanes
	}
	// Adapter selection: GUMMI_AGENT picks the default backend, and any
	// distinct `backend:` referenced across the loaded profiles is
	// started too. The map is keyed by adapter name, and the default is
	// duplicated under the "" key so the engine's fallback lookup works.
	agents, err := buildAgents(profiles)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gummi:", err)
		return nil, nil, nil, nil
	}
	if len(agents) == 0 {
		return nil, nil, nil, nil
	}
	model := cmp.Or(os.Getenv("GUMMI_MODEL"), "gpt-5")
	// Two independent attention pools (internal/engine): attended (a card
	// whose gate-approval mode is off — a human is expected to stay with
	// it) defaults to one lane so it never queues behind autopilot work;
	// autopilot (every other card, including the everyday default)
	// defaults to two. GUMMI_MAX_ACTIVE overrides only the attended pool's
	// size; autopilot_lanes in config.yaml overrides the autopilot pool's.
	maxActive := 1
	if v := os.Getenv("GUMMI_MAX_ACTIVE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxActive = n
		}
	}
	autopilotLanes := cmp.Or(autopilotLanesCfg, 2)
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
	eng := engine.New(engine.Config{
		Agents: agents, Store: store, Pool: pool, Workspace: ws,
		Model: model, MaxActive: maxActive, AutopilotLanes: autopilotLanes, Persist: true,
		Profiles: profiles, StageBudget: stageBudget, TurnReserve: turnReserve,
		Permission: perm, Sandbox: sandboxMode, Instructions: instructions,
	})
	// Names() already orders the declared default first (the rest sorted) so
	// index 0 is the intended default for the forms and the CLI --profile
	// fallback. Re-sorting alphabetically here would silently pick the wrong
	// default (e.g. "premium" ahead of the configured "thrifty").
	names := profiles.Names()
	return eng, agents, names, nil
}

// profileNames returns the profile names declared in .gummi/profiles.yaml
// in display order (the declared default first, the rest sorted), or nil
// when none could be loaded. It is deliberately independent of the agent
// engine: the yaml list is available whether or not any CLI agent can
// start, so the board's dialogs show the real profiles even when the
// backend is down.
func profileNames(ws state.Workspace) []string {
	profiles, err := config.LoadProfiles(ws.ProfilesFile())
	if err != nil {
		fmt.Fprintln(os.Stderr, "gummi:", err)
		return nil
	}
	return profiles.Names()
}

// configuredAgentCLI returns the layered `agent:` value (config.Config.Agent)
// for the agent tab's picker precedence — the third rung, below
// GUMMI_ATTACH_CMD/GUMMI_AGENT and above the picker itself (see
// ui.Shell.resolveAgentAttach). It is deliberately independent of
// newEngineFromEnv's own config load: that one governs the engine's
// permissions/sandbox/instructions, an entirely separate concern from
// which CLI a human hosts in their own tab, and the two must never be
// merged into one load just because they happen to read the same file.
func configuredAgentCLI(ws state.Workspace) string {
	userPath, err := config.UserConfigPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gummi:", err)
		userPath = ""
	}
	cfg, _, err := config.LoadLayered(userPath, ws.ConfigFile())
	if err != nil {
		fmt.Fprintln(os.Stderr, "gummi:", err)
		return ""
	}
	return cfg.Agent
}

// defaultBackendName returns the backend name selected by GUMMI_AGENT
// (or fallbackBackendName's answer when unset). For back-compat,
// GUMMI_AGENT_CMD without GUMMI_AGENT selects headless. These explicit
// rungs are unconditional — set GUMMI_AGENT=claude and that's the
// answer whether or not claude is actually installed, exactly as
// before; only the last-resort fallback below is allowed to look at
// what's on PATH.
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
	case "zz":
		return "zz"
	}
	if strings.TrimSpace(os.Getenv("GUMMI_AGENT_CMD")) != "" {
		return "headless"
	}
	return fallbackBackendName()
}

// fallbackBackendName is defaultBackendName's last rung: nothing named
// in GUMMI_AGENT, no GUMMI_AGENT_CMD either. It used to return "copilot"
// unconditionally — not "copilot if present", just copilot — so a
// machine without it installed had every path silently resolve to a
// missing binary, discovered only when startAdapter tried to run it.
// internal/agentcli's picker made that failure mode visible for the
// hosted agent tab; this closes the same hole for the engine's own
// backend selection by picking something that actually exists.
//
// The preference order among several installed CLIs comes from
// agentcli.Known()/Detect(), not map iteration (which Go randomizes per
// run and would make the choice differ between two otherwise identical
// invocations): copilot first, so a machine that has it keeps behaving
// exactly as every prior release did, then claude, codex, opencode, zz
// in agentcli's own declaration order. copilot remains the answer when
// nothing at all is detected — a bare machine's behavior, and the error
// path startAdapter("copilot") takes from there, are both unchanged.
func fallbackBackendName() string {
	for _, a := range agentcli.Detect() {
		if a.Installed {
			return a.Name
		}
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
	case "zz":
		return agent.NewZZ(os.Getenv("GUMMI_ZZ_BIN"))
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

// newPool builds the per-repository manager pool from the workspace root,
// the default repo root, and the configured named roots, then keeps .gummi
// out of each product repo's tracking (exclude + untrack-if-tracked) as
// each manager is created — the default eagerly, named repos lazily on
// first use. Exclusion is a no-op for a repo that does not contain .gummi,
// and exclusion problems warn rather than block the launch.
func newPool(ctx context.Context, ws, defaultRoot string, named []worktree.NamedRepo, fs worktree.ForkPointStore, exclude bool) (*worktree.Pool, error) {
	return worktree.NewPool(ctx, ws, defaultRoot, named, fs, exclude)
}

// ensureWorkspace returns the .gummi workspace at ws, creating it (and
// scaffolding config.yaml + profiles.yaml) on first run. repo is the git
// repository root gummi manages, validated by state.Init. Idempotent: an
// existing workspace and its files are left untouched.
func ensureWorkspace(ws, repo string) (state.Workspace, error) {
	w, err := state.Init(ws, repo)
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

// findGummiRoot searches upward from dir (dir included) for the nearest
// ancestor holding a real .gummi directory — never a symlink, matching the
// anti-symlink-smuggle convention used elsewhere for the same directory
// (state.enclosingWorkspace). ok is false when no ancestor up to the
// filesystem root has one.
func findGummiRoot(dir string) (root string, ok bool) {
	dir = filepath.Clean(dir)
	for {
		fi, err := os.Lstat(filepath.Join(dir, ".gummi"))
		if err == nil && fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// resolveAllRoots resolves the workspace root (where .gummi lives), the
// default managed repo root, and the ordered list of named repositories from
// cwd. The workspace root is the nearest ancestor of cwd (cwd included) that
// already has a .gummi directory, falling back to cwd itself when none
// exists yet — the pre-init state that `gummi init` and other callers that
// tolerate an absent workspace rely on. The repo roots come from
// config.yaml's `repo:` and `repos:` keys, defaulting to the workspace root.
// A configured root that escapes the workspace, or that is not a git
// toplevel, is a resolution-time config error naming the offending repo.
func resolveAllRoots(cwd string) (ws, defaultRoot string, named []worktree.NamedRepo, err error) {
	ws = cwd
	if found, ok := findGummiRoot(cwd); ok {
		ws = found
	}
	userPath, err := config.UserConfigPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gummi:", err)
		userPath = ""
	}
	cfg, _, err := config.LoadLayered(userPath, filepath.Join(ws, ".gummi", "config.yaml"))
	if err != nil {
		return "", "", nil, err
	}
	def, list, err := config.ResolveRepos(ws, cfg)
	if err != nil {
		return "", "", nil, err
	}
	for _, n := range list {
		named = append(named, worktree.NamedRepo{Name: n.Name, Root: n.Root})
	}
	return ws, def, named, nil
}

// resolveRoots resolves the workspace root and the default managed repo
// root. Most call sites that only ever touch the default repository use this
// convenience; multi-repo call sites use resolveAllRoots.
func resolveRoots(cwd string) (ws, repo string, err error) {
	ws, repo, _, err = resolveAllRoots(cwd)
	return ws, repo, err
}
