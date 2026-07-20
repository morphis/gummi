// Command gummi is a meta-harness for coding agents: it drives a fleet of
// agents through a spec-driven workflow across git worktrees, from one TUI.
package main

import (
	"cmp"
	"context"
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
		fmt.Fprintln(os.Stderr, "gummi:", err)
		os.Exit(1)
	}
}

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
		default:
			return fmt.Errorf("unknown argument %q (usage: gummi [version|ingest|bugs])", args[0])
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
//	GUMMI_MODEL              model id (default "gpt-5")
//	GUMMI_PROVIDER_BASE_URL  BYOK OpenAI-compatible endpoint (optional)
//	GUMMI_PROVIDER_TYPE      "openai"|"azure"|"anthropic" (default openai)
//	GUMMI_PROVIDER_KEY_ENV   env var holding the provider key (optional)
func buildEngine(store *state.Store, wt *worktree.Manager, ws state.Workspace) (*engine.Engine, []string, func()) {
	eng, ag, names := newEngineFromEnv(store, wt, ws)
	if eng == nil {
		return nil, nil, nil
	}
	// reload any sessions from a previous run so the board shows where
	// each feature left off.
	if err := eng.Restore(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "gummi: restoring sessions:", err)
	}
	return eng, names, func() { _ = eng.Close(); _ = ag.Close() }
}

// newEngineFromEnv constructs the orchestrator and its agent from the
// environment, without restoring prior sessions. buildEngine wraps it for
// the board (adding Restore + a combined cleanup); one-shot commands like
// `gummi ingest` use it directly and own the agent's lifetime. Returns
// (nil, nil, nil) when no agent can be started.
func newEngineFromEnv(store *state.Store, wt *worktree.Manager, ws state.Workspace) (*engine.Engine, agent.Agent, []string) {
	// Adapter selection: GUMMI_AGENT_CMD picks the generic headless
	// adapter (any agent binary speaking the stdio JSON protocol);
	// otherwise gummi uses the first-class Copilot SDK adapter.
	ag, err := buildAgent()
	if err != nil {
		return nil, nil, nil
	}
	model := cmp.Or(os.Getenv("GUMMI_MODEL"), "gpt-5")
	var provider agent.Provider
	if base := os.Getenv("GUMMI_PROVIDER_BASE_URL"); base != "" {
		provider = agent.Provider{
			Type:      cmp.Or(os.Getenv("GUMMI_PROVIDER_TYPE"), "openai"),
			BaseURL:   base,
			APIKeyEnv: os.Getenv("GUMMI_PROVIDER_KEY_ENV"),
		}
	}
	maxActive := 1
	if v := os.Getenv("GUMMI_MAX_ACTIVE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxActive = n
		}
	}
	// per-role model routing from .gummi/profiles.yaml (falls back to
	// the single env model when absent or a role isn't covered)
	profiles, err := config.LoadProfiles(ws.ProfilesFile())
	if err != nil {
		fmt.Fprintln(os.Stderr, "gummi:", err)
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
		Agent: ag, Store: store, Worktrees: wt, Workspace: ws,
		Model: model, Provider: provider, MaxActive: maxActive, Persist: true,
		Profiles: profiles, StageBudget: stageBudget, TurnReserve: turnReserve,
		Permission: perm,
	})
	// Names() already orders the declared default first (the rest sorted) so
	// index 0 is the intended default for the forms and the CLI --profile
	// fallback. Re-sorting alphabetically here would silently pick the wrong
	// default (e.g. "premium" ahead of the configured "thrifty").
	names := profiles.Names()
	return eng, ag, names
}

// buildAgent selects the agent backend from GUMMI_AGENT:
//
//	copilot   (default) — the GitHub Copilot SDK adapter
//	claude              — the Claude Code CLI adapter (GUMMI_CLAUDE_BIN overrides the binary)
//	opencode            — the opencode CLI adapter (GUMMI_OPENCODE_BIN overrides the binary)
//	headless            — a generic subprocess agent (GUMMI_AGENT_CMD is its command line)
//
// For back-compat, setting GUMMI_AGENT_CMD alone still selects headless.
// Command lines are split on spaces (operator config, not untrusted
// input); use a wrapper script for arguments containing spaces.
func buildAgent() (agent.Agent, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GUMMI_AGENT"))) {
	case "claude":
		return agent.NewClaudeCode(os.Getenv("GUMMI_CLAUDE_BIN"))
	case "opencode":
		return agent.NewOpencode(os.Getenv("GUMMI_OPENCODE_BIN"))
	case "headless":
		return agent.NewHeadless(strings.Fields(os.Getenv("GUMMI_AGENT_CMD")))
	}
	if cmd := strings.TrimSpace(os.Getenv("GUMMI_AGENT_CMD")); cmd != "" {
		return agent.NewHeadless(strings.Fields(cmd))
	}
	return agent.NewCopilot(context.Background(), agent.CopilotOptions{LogLevel: "error"})
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
