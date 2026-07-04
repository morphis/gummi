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

	tea "charm.land/bubbletea/v2"

	"github.com/morphia/gummi/internal/agent"
	"github.com/morphia/gummi/internal/config"
	"github.com/morphia/gummi/internal/engine"
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
	if len(args) == 0 || args[0] == "version" || args[0] == "--version" {
		fmt.Printf("gummi %s\n", version())
		return nil
	}
	switch args[0] {
	case "init":
		return runInit()
	case "board":
		return runBoard()
	default:
		return fmt.Errorf("unknown command %q (commands: init, board, version)", args[0])
	}
}

func runBoard() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	ws, err := state.Open(cwd)
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
	shell := ui.NewShell(theme.GummiDark(), version())
	shell.Attach(store, wt, ws)

	// Wire the agent engine best-effort: a missing/unstartable CLI just
	// leaves the board static (chat reports "no agent configured").
	if eng, names, cleanup := buildEngine(store, wt, ws); eng != nil {
		shell.AttachEngine(eng)
		shell.SetProfileNames(names)
		defer cleanup()
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
	ag, err := agent.NewCopilot(context.Background(), agent.CopilotOptions{LogLevel: "error"})
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
	// reload any sessions from a previous run so the board shows where
	// each feature left off.
	if err := eng.Restore(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "gummi: restoring sessions:", err)
	}
	names := profiles.Names()
	sort.Strings(names)
	return eng, names, func() { _ = eng.Close(); _ = ag.Close() }
}

func runInit() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	w, err := state.Init(cwd)
	if err != nil {
		return err
	}
	// scaffold the repo config + profiles if absent
	for _, f := range []struct{ path, body string }{
		{w.ConfigFile(), config.Template},
		{w.ProfilesFile(), config.ProfilesTemplate},
	} {
		if _, err := os.Stat(f.path); os.IsNotExist(err) {
			if err := os.WriteFile(f.path, []byte(f.body), 0o600); err != nil {
				return fmt.Errorf("writing %s: %w", filepath.Base(f.path), err)
			}
		}
	}
	fmt.Printf("initialized gummi workspace in %s\n", w.GummiDir())
	return nil
}
