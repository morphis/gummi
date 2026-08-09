package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// versionCmd implements `gummi version`: it prints the version stamp. The
// --version / -v short-circuit lives on the root command (root.go) and
// shares the same runVersion implementation.
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runVersion(args)
	},
}

// runVersion prints the version stamp in the historical "gummi <version>"
// format. It was extracted from the old inline dispatch so both the
// `version` subcommand and the root --version flag share one implementation.
func runVersion(_ []string) error {
	fmt.Printf("gummi %s\n", version())
	return nil
}
