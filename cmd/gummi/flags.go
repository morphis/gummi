package main

// The flag surface, declared once.
//
// Every verb's flags are declared here, on the cobra command's own
// pflag.FlagSet, and a command body reads them back through cliFlags. That
// single site is the point: cobra owns routing, help, completion AND
// parsing, and `gummi skill` generates its command grammar from the same
// tree, so what --help advertises, what the parser accepts, and what the
// skill documents cannot disagree.
//
// They used to disagree. Each verb declared its flags twice — once as
// pflag for cobra, once as a stdlib flag.FlagSet inside runXxx — and cobra
// re-serialized its parsed flags back into a []string for that second
// parser to read. Nothing kept the two copies honest: eight help strings
// had drifted (`gummi research --help` promised `--until shape`, which the
// binary rejects), `stack new --name` and `stack add --pos` were declared
// only on the inner set and so were unreachable, and `merge --m` was
// documented in SKILL.md and rejected by the binary. The round trip also
// lost "was this flag passed at all", which one verb had to patch back by
// hand.

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// cliFlags is a command body's view of the flags cobra has already parsed.
// The getters panic on an unknown name or a type mismatch: both are wiring
// mistakes in this package, not conditions a user can produce, and every
// command's flags are walked by the grammar generator and its tests — so a
// typo fails the build's tests rather than reaching a terminal.
type cliFlags struct{ fs *pflag.FlagSet }

// cmdFlags views the flags cobra parsed for cmd.
func cmdFlags(cmd *cobra.Command) cliFlags { return cliFlags{fs: cmd.Flags()} }

func (f cliFlags) String(name string) string {
	v, err := f.fs.GetString(name)
	if err != nil {
		panic(fmt.Sprintf("reading --%s as a string: %v", name, err))
	}
	return v
}

func (f cliFlags) Bool(name string) bool {
	v, err := f.fs.GetBool(name)
	if err != nil {
		panic(fmt.Sprintf("reading --%s as a bool: %v", name, err))
	}
	return v
}

// Changed reports whether the flag was present on the command line, so an
// explicitly empty value (`--answer ""`) is distinguishable from an unset
// one. This is what the old stdlib round trip could not carry: a []string
// rebuilt from parsed values cannot say "the user typed this".
func (f cliFlags) Changed(name string) bool {
	g := f.fs.Lookup(name)
	if g == nil {
		panic(fmt.Sprintf("--%s is not a flag of this command", name))
	}
	return g.Changed
}

// jsonFlag declares the --json switch the read-only verbs share.
func jsonFlag(fs *pflag.FlagSet, what string) {
	fs.Bool("json", false, what)
}
