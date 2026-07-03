// Command gummi is a meta-harness for coding agents: it drives a fleet of
// agents through a spec-driven workflow across git worktrees, from one TUI.
package main

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"

	tea "charm.land/bubbletea/v2"

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
	_, err = tea.NewProgram(shell).Run()
	return err
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
	fmt.Printf("initialized gummi workspace in %s\n", w.GummiDir())
	return nil
}
