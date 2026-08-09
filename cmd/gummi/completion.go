package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// completionCmd implements `gummi completion <shell>`: it generates shell
// completion scripts for bash, zsh, and fish so operators get tab-completion
// over commands and flags. It replaces cobra's built-in completion command
// (cobra.default) so the surface stays exactly bash/zsh/fish (see root.go).
var completionCmd = &cobra.Command{
	Use:       "completion [bash|zsh|fish]",
	Short:     "Generate a shell completion script",
	Long:      "Generate the autocompletion script for gummi for bash, zsh, or fish.\n\nTo use it, source the output in your shell resource file, e.g.:\n  gummi completion bash > /etc/bash_completion.d/gummi",
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"bash", "zsh", "fish"},
	RunE: func(cmd *cobra.Command, args []string) error {
		root := cmd.Root()
		switch args[0] {
		case "bash":
			return root.GenBashCompletion(os.Stdout)
		case "zsh":
			return root.GenZshCompletion(os.Stdout)
		case "fish":
			return root.GenFishCompletion(os.Stdout, true)
		default:
			return fmt.Errorf("unsupported shell %q (want bash, zsh, or fish)", args[0])
		}
	},
}
