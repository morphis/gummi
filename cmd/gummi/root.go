package main

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// rootCmd is the cobra command tree root. Every top-level command is
// registered as a child in init() (and each command file wires its own
// flags). With no subcommand the root launches the TUI board, preserving
// the default behavior of the old hand-rolled dispatch.
var rootCmd = &cobra.Command{
	Use: "gummi",
	// "Meta-harness" was the Short line for a long time. It is what gummi
	// is to the people who build it and nothing at all to someone typing
	// --help for the first time, and the Long text pointed at a design-doc
	// section number that a user of the binary has no copy of. Both now
	// say what the program does instead.
	Short: "Drives coding agents through plan, implement and verify",
	Long: `gummi drives coding agents through a spec-driven workflow: each piece of
work is a card, and every card is planned, implemented and verified on its
own git branch before you decide whether to land it.

Run 'gummi' with no arguments to launch the board in the terminal, or
'gummi web' to serve it to a browser. 'gummi doctor' checks the setup.`,
	RunE: runBoardCobra,
}

func init() {
	// Cobra's built-in completion command is configured out so the command
	// list stays exactly the app's surface.
	rootCmd.CompletionOptions.DisableDefaultCmd = true
	// Errors and usage are handled in main() (the typed exitError path), so
	// cobra keeps quiet and lets main's single reporting point talk.
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true
	// --version / -v short-circuit to the version stamp, mirroring the old
	// dispatch's "version", "--version", "-v" handling.
	rootCmd.Flags().BoolP("version", "v", false, "print the version and exit")

	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(webCmd)
	rootCmd.AddCommand(mcpCmd)
}

// runBoardCobra is the root command's RunE. --version/-v short-circuits to
// the version stamp; every other invocation (including no args) launches
// the TUI board.
func runBoardCobra(cmd *cobra.Command, _ []string) error {
	if v, _ := cmd.Flags().GetBool("version"); v {
		return runVersion(nil)
	}
	return runBoard()
}

// resetFlags restores a command tree's flags to their declared defaults.
// The cobra tree is package-level state, so a value parsed by one
// invocation would otherwise be inherited by the next one in the same
// process — which matters to the tests that drive the real tree, and to
// anything embedding run().
func resetFlags(cmd *cobra.Command) {
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if !f.Changed {
			return
		}
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	})
	for _, sub := range cmd.Commands() {
		resetFlags(sub)
	}
}
