package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// captureStdout runs fn with the process-wide stdout redirected to a pipe and
// returns everything written to it. The cobra commands print version and help
// via os.Stdout (OutOrStdout), so swapping stdout is the reliable way to
// assert on their output in-process.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The `version` subcommand prints the same stamp as the old dispatch.
func TestCobraVersion(t *testing.T) {
	out := captureStdout(t, func() {
		rootCmd.SetArgs([]string{"version"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Execute(version): %v", err)
		}
	})
	want := fmt.Sprintf("gummi %s\n", version())
	if out != want {
		t.Fatalf("version output = %q, want %q", out, want)
	}
}

// The root --version / -v flags short-circuit to the same stamp.
func TestCobraVersionFlag(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"-v"}} {
		out := captureStdout(t, func() {
			rootCmd.SetArgs(args)
			if err := rootCmd.Execute(); err != nil {
				t.Fatalf("Execute(%v): %v", args, err)
			}
		})
		want := fmt.Sprintf("gummi %s\n", version())
		if out != want {
			t.Fatalf("%v output = %q, want %q", args, out, want)
		}
	}
}

// --help shows the hierarchical command list cobra derives from the tree.
func TestCobraHelp(t *testing.T) {
	out := captureStdout(t, func() {
		rootCmd.SetArgs([]string{"--help"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Execute(--help): %v", err)
		}
	})
	if !strings.Contains(out, "Available Commands:") {
		t.Fatalf("help output missing the command list:\n%s", out)
	}
}

// bugs help lists its ingest/new subcommands.
func TestCobraBugsHelp(t *testing.T) {
	out := captureStdout(t, func() {
		rootCmd.SetArgs([]string{"bugs", "--help"})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("Execute(bugs --help): %v", err)
		}
	})
	if !strings.Contains(out, "ingest") || !strings.Contains(out, "new") {
		t.Fatalf("bugs help missing subcommands:\n%s", out)
	}
}

// An unknown command errors (cobra's unknown-command path).
func TestCobraUnknownCommand(t *testing.T) {
	rootCmd.SetArgs([]string{"frobnicate"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("Execute(frobnicate): want error, got nil")
	}
}

// canonicalAndCobra pairs a cobra command with the function that registers
// its canonical flag grammar, so the test can assert the two never drift.
func TestCobraFlagsMirrorCanonical(t *testing.T) {
	cases := []struct {
		name     string
		cmd      *cobra.Command
		register func(fs *flag.FlagSet)
	}{
		{name: "run", cmd: runCmd, register: func(fs *flag.FlagSet) { registerRunFlags(fs) }},
		{name: "bugs new", cmd: bugsNewCmd, register: func(fs *flag.FlagSet) { registerBugsNewFlags(fs) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			canonical := flag.NewFlagSet(c.name, flag.ContinueOnError)
			c.register(canonical)

			canonical.VisitAll(func(f *flag.Flag) {
				if c.cmd.Flags().Lookup(f.Name) == nil {
					t.Errorf("%s: canonical flag --%s is not bound on the cobra command", c.name, f.Name)
				}
			})
		})
	}
}
