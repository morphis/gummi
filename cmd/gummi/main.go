// Command gummi is a meta-harness for coding agents: it drives a fleet of
// agents through a spec-driven workflow across git worktrees, from one TUI.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/agentcli"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/hooks"
	"github.com/morphis/gummi/internal/notify"
	"github.com/morphis/gummi/internal/state"
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
	resetFlags(rootCmd)
	rootCmd.SetArgs(args)
	return rootCmd.Execute()
}

// wireHooks loads the layered hooks config and installs the dispatcher on
// the store (committed card events) and the pool (squash-merge landings).
// It returns the dispatcher for the caller to Close — nil-safe — and nil
// itself when no hooks are configured. Loading here, at each command's
// store-construction site, is what keeps the TUI, the headless driver and
// the CLI verbs on one hook surface; the store reports its own writes, so
// each process fires only for what it drove.
func wireHooks(st *state.Store, pool *worktree.Pool, ws state.Workspace) *hooks.Dispatcher {
	userPath, err := config.UserConfigPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gummi:", err)
		userPath = ""
	}
	cfg, _, err := config.LoadLayered(userPath, ws.ConfigFile())
	if err != nil {
		fmt.Fprintln(os.Stderr, "gummi:", err)
		return nil
	}
	d := hooks.New(cfg.Hooks, ws.Root, st)
	st.SetObserver(d.Observe)
	pool.SetMergeHook(d.Merged)
	return d
}

// runBoard is `gummi` with no arguments: the board in this terminal.
func runBoard() error {
	h, err := openBoard(boardOpts{
		holder: state.InstanceHolder{Host: state.HostTUI},
		// bell when GUMMI_NOTIFY is unset. Escapes go to stderr so they
		// reach the terminal without disturbing the render surface.
		notifyDefault: notify.Bell,
		notifyOut:     os.Stderr,
	})
	if err != nil {
		return err
	}
	defer h.Close()
	p := tea.NewProgram(h.shell)
	defer quitOnHangup(p)()
	_, err = p.Run()
	return err
}

// quitOnHangup makes SIGHUP — the terminal closed, the tmux pane killed —
// quit the board the way SIGTERM does (Bubble Tea handles that one), so
// Run returns and openBoard's host is closed: the instance record removed
// and the lock released. Left to the default, SIGHUP kills the process
// where it stands and the record outlives it. A program that cannot quit
// cleanly on a terminal that is gone is killed after webGrace, which
// still returns from Run. The returned func stops listening.
func quitOnHangup(p *tea.Program) func() {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		select {
		case <-hup:
			p.Quit()
		case <-done:
			return
		}
		select {
		case <-time.After(webGrace):
			p.Kill()
		case <-done:
		}
	}()
	return func() {
		signal.Stop(hup)
		close(done)
	}
}

// buildEngine constructs the agent orchestrator from environment
// config. Returns (nil, nil) when no agent can be started, so the board
// degrades to static rather than failing to launch.
//
// Env config (M1 stand-in for profiles):
//
//	GUMMI_MODEL             model id (default "gpt-5")
//	GUMMI_AGENT             default backend (copilot|claude|codex|opencode|pi|headless)
//	GUMMI_HEADLESS_CREDITS_PER_1K
//	                        headless adapter's token→credit rate, for a
//	                        local endpoint (llama.cpp) that the engine
//	                        still needs to meter against a credit budget
//
// With no engine, why says what stopped it, for the board to repeat when
// a card is asked to run: the line on stderr is gone by then, and on a
// web host was never in front of the person asking.
func buildEngine(store *state.Store, pool *worktree.Pool, ws state.Workspace, locks *state.CardLocks) (_ *engine.Engine, _ []string, cleanup func(), why string) {
	eng, agents, names, why, err := engineFromEnv(store, pool, ws)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gummi:", err)
		why = err.Error()
	}
	if eng == nil {
		return nil, nil, nil, why
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
	}, ""
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
	eng, agents, names, _, err := engineFromEnv(store, pool, ws)
	return eng, agents, names, err
}

// engineFromEnv is newEngineFromEnv, plus why: on a nil engine with a
// nil error, what kept every agent from starting. It is a string rather
// than a second error because it is not one to its callers — a board
// with no agent is a static board, which is allowed.
func engineFromEnv(store *state.Store, pool *worktree.Pool, ws state.Workspace) (_ *engine.Engine, _ map[string]agent.Agent, _ []string, why string, _ error) {
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
	var forwardSkills []string
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
				return nil, nil, nil, "", fmt.Errorf("permissions: guarded is incompatible with the resolved backend for %s",
					formatGuardedIncompatibilities(issues))
			}
		}
		sandboxMode = cfg.Sandbox
		instructions = cfg.Instructions
		forwardSkills = cfg.Skills.Forward
		autopilotLanesCfg = cfg.AutopilotLanes
	}
	// Adapter selection: GUMMI_AGENT picks the default backend, and any
	// distinct `backend:` referenced across the loaded profiles is
	// started too. The map is keyed by adapter name, and the default is
	// duplicated under the "" key so the engine's fallback lookup works.
	agents, err := buildAgents(profiles)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gummi:", err)
		return nil, nil, nil, err.Error(), nil
	}
	if len(agents) == 0 {
		return nil, nil, nil, "none of the backends the profiles name could start", nil
	}
	model := cmp.Or(os.Getenv("GUMMI_MODEL"), "gpt-5")
	// Two independent attention pools (internal/engine): attended — every
	// card that is not explicitly on autopilot, which after
	// engine.lanePoolFor's resolution through domain.Feature.GateMode
	// means the empty default and so every ordinary card — defaults to one
	// lane, so it never queues behind autopilot work. Autopilot (cards
	// whose mode is domain.GateAutopilot, and only those) defaults to two.
	// GUMMI_MAX_ACTIVE overrides only the attended pool's size;
	// autopilot_lanes in config.yaml overrides the autopilot pool's.
	//
	// The one lane is therefore what an everyday board runs at: a card you
	// have not handed over is one you are expected to be reading, and two
	// of those at once is two things to attend to. Widen it with
	// GUMMI_MAX_ACTIVE, or hand cards to autopilot to reach the other two
	// lanes. (This comment used to describe the reverse split — the
	// default in the autopilot pool, one attended lane for the rare
	// opted-in card — which is the classification lanePoolFor no longer
	// makes.)
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
		Skills: forwardSkills,
		// A session may run on any installed agent, not only the ones the
		// profiles name (DESIGN §19.8); this is how the board starts one it
		// did not launch.
		StartAgent: startSessionAdapter,
	})
	// Teach every worktree manager how to resolve a card's base. Until
	// this is installed a card forks from whatever the checkout has out,
	// which is what gummi did before bases were selectable — so the
	// lookup going in late is a widening, never a change of behavior for
	// a card that names no base.
	//
	// It is installed here rather than at pool construction because only
	// the engine can answer the question: a stacked card's base is the
	// branch of the card below it, which takes the store.
	pool.SetBaseLookup(eng.StackBaseFor)
	// Names() already orders the declared default first (the rest sorted) so
	// index 0 is the intended default for the forms and the CLI --profile
	// fallback. Re-sorting alphabetically here would silently pick the wrong
	// default (e.g. "premium" ahead of the configured "thrifty").
	names := profiles.Names()
	return eng, agents, names, "", nil
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
	case "pi":
		return "pi"
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
// engine's own backend selection, and this closes the same hole by
// picking something that actually exists.
//
// The preference order among several installed CLIs comes from
// agentcli.Known()/Detect(), not map iteration (which Go randomizes per
// run and would make the choice differ between two otherwise identical
// invocations): copilot first, so a machine that has it keeps behaving
// exactly as every prior release did, then claude, codex, opencode, pi
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
	case "copilot":
		return agent.NewCopilot(context.Background(), agent.CopilotOptions{LogLevel: "error"})
	case "pi":
		return agent.NewPi(os.Getenv("GUMMI_PI_BIN"))
	}
	return nil, fmt.Errorf("unknown backend %q", name)
}

// startSessionAdapter starts a backend a session asked for that the board
// did not launch. It refuses an agent CLI that is not on PATH by name,
// before the adapter's own lookup fails with a less direct message, and
// headless without the command line that is its whole definition.
func startSessionAdapter(name string) (agent.Agent, error) {
	if bin, ok := agentcli.Binary(name); ok {
		if _, err := exec.LookPath(bin); err != nil {
			return nil, fmt.Errorf("%s is not installed on this host (no %s on PATH)", name, bin)
		}
	}
	if name == "headless" && strings.TrimSpace(os.Getenv("GUMMI_AGENT_CMD")) == "" {
		return nil, errors.New("headless needs GUMMI_AGENT_CMD")
	}
	return startAdapter(name)
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
