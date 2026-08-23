package main

import (
	"strconv"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// rootCmd is the cobra command tree root. Every top-level command is
// registered as a child in init() (and each command file wires its own
// flags). With no subcommand the root launches the TUI board, preserving
// the default behavior of the old hand-rolled dispatch.
var rootCmd = &cobra.Command{
	Use:   "gummi",
	Short: "Meta-harness for coding agents",
	Long: `gummi drives a fleet of coding agents through a spec-driven workflow
across git worktrees, from one TUI (DESIGN §8.2).

Run 'gummi' with no arguments to launch the board. The subcommands run the
same operations headlessly so agents and scripts can drive the workflow
without the TUI. Use 'gummi <command> --help' for each command's flags.`,
	RunE: runBoardCobra,
}

func init() {
	// A bespoke completion command (completion.go) is registered in place of
	// cobra's built-in one, which also emits powershell and is configured out
	// here so the command list stays exactly the app's surface.
	rootCmd.CompletionOptions.DisableDefaultCmd = true
	// Errors and usage are handled in main() (the typed exitError path), so
	// cobra keeps quiet and lets main's single reporting point talk.
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true
	// --version / -v short-circuit to the version stamp, mirroring the old
	// dispatch's "version", "--version", "-v" handling.
	rootCmd.Flags().BoolP("version", "v", false, "print the version and exit")

	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(ingestCmd)
	rootCmd.AddCommand(bugsCmd)
	rootCmd.AddCommand(depsCmd)
	rootCmd.AddCommand(prCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(researchCmd)
	rootCmd.AddCommand(resumeCmd)
	rootCmd.AddCommand(verifyCmd)
	rootCmd.AddCommand(mergeCmd)
	rootCmd.AddCommand(squashCmd)
	rootCmd.AddCommand(cleanCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(specCmd)
	rootCmd.AddCommand(diffCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(skillCmd)
	rootCmd.AddCommand(completionCmd)
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

// buildFlagArgs reconstructs the []string a cobra command parsed into, in
// the shape the existing flag.FlagSet-based runXxx functions expect: every
// flag that differs from its default as `--name value` (bare `--name` for an
// on boolean), then the positional args unchanged. Each runXxx keeps its own
// flag.NewFlagSet parsing untouched while cobra supplies routing and help.
func buildFlagArgs(cmd *cobra.Command, positional []string) []string {
	var out []string
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if !f.Changed || f.Value.String() == f.DefValue {
			return
		}
		if f.Value.Type() == "bool" {
			if b, err := strconv.ParseBool(f.Value.String()); err == nil && b {
				out = append(out, "--"+f.Name)
			}
			return
		}
		out = append(out, "--"+f.Name, f.Value.String())
	})
	return append(out, positional...)
}
