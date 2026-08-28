package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/morphis/gummi/internal/state"
)

// initWorkspace resolves cwd's roots and creates/seeds the .gummi workspace
// there, reporting whether it already existed. existed is determined by a
// stat on GummiDir() taken before ensureWorkspace runs, mirroring
// effectiveProfiles' (doctor.go) stat-first idiom rather than threading a
// "did we just create this" flag through ensureWorkspace and its seven
// existing call sites.
func initWorkspace(cwd string) (ws state.Workspace, existed bool, err error) {
	wsRoot, defaultRoot, _, err := resolveAllRoots(cwd)
	if err != nil {
		return state.Workspace{}, false, err
	}
	if _, statErr := os.Lstat((state.Workspace{Root: wsRoot}).GummiDir()); statErr == nil {
		existed = true
	}
	ws, err = ensureWorkspace(wsRoot, defaultRoot)
	if err != nil {
		return state.Workspace{}, false, err
	}
	return ws, existed, nil
}

// runInit implements `gummi init`: create and seed the workspace in the
// current directory, or report that one is already there. Finding an
// initialized workspace is not an error — both paths return nil.
func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, "usage: gummi init") }
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	ws, existed, err := initWorkspace(cwd)
	if err != nil {
		return err
	}
	if existed {
		fmt.Printf("gummi: workspace already initialized in %s\n", ws.Root)
		return nil
	}
	fmt.Printf("gummi: initialized workspace in %s\n", ws.Root)
	return nil
}
