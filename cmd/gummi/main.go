// Command gummi is a meta-harness for coding agents: it drives a fleet of
// agents through a spec-driven workflow across git worktrees, from one TUI.
package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/morphia/gummi/internal/state"
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
	default:
		return fmt.Errorf("unknown command %q (commands: init, version)", args[0])
	}
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
