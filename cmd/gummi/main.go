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
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphia/gummi/internal/agent"
	"github.com/morphia/gummi/internal/config"
	"github.com/morphia/gummi/internal/engine"
	"github.com/morphia/gummi/internal/notify"
	"github.com/morphia/gummi/internal/state"
	"github.com/morphia/gummi/internal/ui"
	"github.com/morphia/gummi/internal/ui/theme"
	"github.com/morphia/gummi/internal/worktree"
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
	wt, err := worktree.NewManager(context.Background(), cwd)
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
	// layer-3 spend plan: new features get this credit envelope, split
	// into per-stage allocations with rollover and a protected review floor.
	if v := os.Getenv("GUMMI_ENVELOPE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
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
	eng := engine.New(engine.Config{
		Agent: ag, Store: store, Worktrees: wt, Workspace: ws,
		Model: model, Provider: provider, MaxActive: maxActive, Persist: true,
		Profiles: profiles, StageBudget: stageBudget,
	})
	names := profiles.Names()
	sort.Strings(names)
	return eng, ag, names
}

// buildAgent selects the agent backend from GUMMI_AGENT:
//
//	copilot   (default) — the GitHub Copilot SDK adapter
//	opencode            — the opencode CLI adapter (GUMMI_OPENCODE_BIN overrides the binary)
//	headless            — a generic subprocess agent (GUMMI_AGENT_CMD is its command line)
//
// For back-compat, setting GUMMI_AGENT_CMD alone still selects headless.
// Command lines are split on spaces (operator config, not untrusted
// input); use a wrapper script for arguments containing spaces.
func buildAgent() (agent.Agent, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GUMMI_AGENT"))) {
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
