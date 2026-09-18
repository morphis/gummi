package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/morphis/gummi/internal/experiment"
	"github.com/spf13/cobra"
)

// experimentCmd is the hidden `gummi __experiment` subcommand: the process
// that makes one live run on a substrate (internal/experiment). The engine
// prepares a run directory and spawns this detached, and that separation
// is load-bearing twice over. A run outlasts the gummi that asked for it —
// a board closed for the night, a driver restarted — because nothing about
// it lives in that process: the record is the directory. And the substrate
// lease is an advisory lock on a file this process holds open, so the
// substrate is held exactly as long as something is really using it: a
// runner that dies lets go, and a gummi that dies does not take a running
// experiment's exclusivity down with it.
var experimentCmd = &cobra.Command{
	Use:    "__experiment",
	Hidden: true,
	Short:  "Make one prepared experiment run on its substrate (internal)",
	RunE:   runExperiment,
}

func init() {
	experimentCmd.Flags().String("dir", "", "the prepared run directory")
}

func runExperiment(cmd *cobra.Command, _ []string) error {
	dir, _ := cmd.Flags().GetString("dir")
	if dir == "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "gummi __experiment: --dir is required; this command only runs as a child spawned by gummi")
		return &exitError{code: 2}
	}
	job, err := experiment.LoadJob(dir)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "gummi __experiment: %v\n", err)
		return &exitError{code: 2}
	}
	// SIGTERM stops the run between the lines rather than under them: the
	// phase in flight is killed with its process group and the record says
	// the run was stopped, which reads as inconclusive — never as a fail.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	res := experiment.Execute(ctx, job)
	fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s — %s\n", res.Experiment, res.ID, res.Outcome, res.Reason)
	return nil
}
