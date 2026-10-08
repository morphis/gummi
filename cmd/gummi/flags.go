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
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/driver"
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

func (f cliFlags) Int(name string) int {
	v, err := f.fs.GetInt(name)
	if err != nil {
		panic(fmt.Sprintf("reading --%s as an int: %v", name, err))
	}
	return v
}

// Budget reads a dollar flag (budgetFlag) as the credits it was parsed to.
func (f cliFlags) Budget(name string) int {
	g := f.fs.Lookup(name)
	if g == nil {
		panic(fmt.Sprintf("--%s is not a flag of this command", name))
	}
	v, ok := g.Value.(*dollarsValue)
	if !ok {
		panic(fmt.Sprintf("reading --%s as a budget: it is a %s flag", name, g.Value.Type()))
	}
	return int(*v)
}

// dollarsValue is a budget flag: typed in dollars ("5", "$12.50"), held
// in credits, the unit every envelope is stored in.
type dollarsValue int

func (v *dollarsValue) String() string {
	return domain.DollarsInput(int(*v))
}

func (v *dollarsValue) Set(s string) error {
	n, err := domain.ParseDollars(s)
	if err != nil {
		return err
	}
	*v = dollarsValue(n)
	return nil
}

func (*dollarsValue) Type() string { return "dollars" }

// envelopeFlag declares --envelope on fs: a budget typed in dollars and
// read back in credits, defaulting to 0.
func envelopeFlag(fs *pflag.FlagSet, usage string) {
	fs.Var(new(dollarsValue), "envelope", usage)
}

func (f cliFlags) Bool(name string) bool {
	v, err := f.fs.GetBool(name)
	if err != nil {
		panic(fmt.Sprintf("reading --%s as a bool: %v", name, err))
	}
	return v
}

func (f cliFlags) Duration(name string) time.Duration {
	v, err := f.fs.GetDuration(name)
	if err != nil {
		panic(fmt.Sprintf("reading --%s as a duration: %v", name, err))
	}
	return v
}

// opt reads a string flag the command may or may not declare, reporting ""
// when it does not. The shared driving options are assembled once
// (driverOptions), but not every verb offers every one of them: a goal is
// not in a repository so takes no --repo, and a resumed card can change
// neither its repository nor its base branch.
func (f cliFlags) opt(name string) string {
	if f.fs.Lookup(name) == nil {
		return ""
	}
	return f.String(name)
}

// Changed reports whether the flag was present on the command line, so an
// explicitly empty value (`--answer ""`) is distinguishable from an unset
// one. This is what the old stdlib round trip could not carry: a []string
// rebuilt from parsed values cannot say "the user typed this", which is
// why `resume --gate-approval attended` used to be dropped on the floor.
func (f cliFlags) Changed(name string) bool {
	g := f.fs.Lookup(name)
	if g == nil {
		panic(fmt.Sprintf("--%s is not a flag of this command", name))
	}
	return g.Changed
}

// --- the shared driving surface ---------------------------------------

// driveFlags is the flag surface the card-driving verbs share — run,
// research, diagnose, goal and resume. Each field is that flag's help
// text; an empty field omits the flag from the verb entirely.
//
// Name, type and default live here once, so those cannot drift between
// verbs. Help text is per-verb on purpose: a goal's --envelope really does
// mean something different from a card's, and stating the difference at
// the call site makes a deliberate divergence visible and an accidental
// one impossible.
type driveFlags struct {
	envelope   string
	profile    string
	gate       string
	timeout    string
	autonomous string
	verbose    string
	ref        string
	repo       string
	base       string
	until      string
}

// stdDriveFlags is the wording the plain card verbs (run, research,
// diagnose) all use. goal and resume start from it and override the
// entries whose meaning genuinely differs for them.
func stdDriveFlags() driveFlags {
	return driveFlags{
		envelope:   "spend budget for the card, in dollars (required; falls back to GUMMI_ENVELOPE)",
		profile:    "profile mapping roles to models (default: first configured)",
		gate:       "who crosses this card's gates: attended|autopilot (retired spellings off/gates/caller/full still accepted; persisted on the card; resume keeps it)",
		timeout:    "per-stage inactivity timeout (0 disables)",
		autonomous: "auto-take the recommended answer instead of checkpointing questions",
		verbose:    "add per-tool-call activity lines to the stream",
		ref:        "external correlation id, echoed in the stream and persisted for status/resume lookup",
		repo:       "managed repository to create the card in (a configured repos: name; required when repos: is configured)",
		base:       "branch the card's work forks from and lands on (default: whatever the repository has checked out)",
		until:      "stop cleanly before crossing the gate that leaves the design stage (default: run to a verified branch)",
	}
}

// bind declares whichever of the shared flags carry help text onto fs.
func (d driveFlags) bind(fs *pflag.FlagSet) {
	str := func(name, usage string) {
		if usage != "" {
			fs.String(name, "", usage)
		}
	}
	if d.envelope != "" {
		envelopeFlag(fs, d.envelope)
	}
	str("profile", d.profile)
	if d.gate != "" {
		fs.String("gate-approval", driver.GateAttended, d.gate)
	}
	if d.timeout != "" {
		fs.Duration("stage-timeout", defaultStageTimeout, d.timeout)
	}
	if d.autonomous != "" {
		fs.Bool("autonomous", false, d.autonomous)
	}
	if d.verbose != "" {
		fs.Bool("verbose", false, d.verbose)
	}
	str("ref", d.ref)
	str("repo", d.repo)
	str("base", d.base)
	str("until", d.until)
}

// sharedDriveFlagNames are the flags driveFlags owns, in the order bind
// declares them. The skill's grammar generator factors exactly these out
// of the per-verb listings into one shared block instead of reprinting
// them under all five verbs.
var sharedDriveFlagNames = []string{
	"envelope", "profile", "gate-approval", "stage-timeout",
	"autonomous", "verbose", "ref", "repo", "base", "until",
}

// adoptionFlags declares the two ways to mint a card onto a branch gummi
// did not cut (DESIGN §10 D22). Shared by `run` and `bugs new`, which
// offer the identical choice.
func adoptionFlags(fs *pflag.FlagSet) {
	fs.String("adopt", "", "mint the card onto this existing branch instead of cutting one for it; gummi never deletes or rewrites it")
	fs.String("pr", "", "mint the card onto the branch behind this pull request (url or number), link it, and pull its review comments in as diff annotations")
}

// messageFlag declares the -m/--message pair the three committing verbs
// share. They differ only in what the message is for, so that is the
// argument; the sentinel "-" reads the message from stdin everywhere.
//
// It is one flag with a shorthand, not the two separate long flags the
// stdlib flag package forced (`--m` plus a `--message` aliased onto it).
// That shim is why SKILL.md documented a `--m` the binary rejects.
func messageFlag(fs *pflag.FlagSet, what string) {
	fs.StringP("message", "m", "", what+" (required; - reads from stdin)")
}

// jsonFlag declares the --json switch the read-only verbs share.
func jsonFlag(fs *pflag.FlagSet, what string) {
	fs.Bool("json", false, what)
}

// commitMessage reads the -m/--message value for one of the committing
// verbs, resolving the "-" sentinel from stdin. required says whether an
// empty message is an error: it is everywhere except a goal's merge, whose
// commit message gummi writes from the goal and its cards.
func commitMessage(fl cliFlags, verb string, required bool) (string, error) {
	msg := fl.String("message")
	switch {
	case msg == "" && required:
		return "", fmt.Errorf("%s needs a commit message: pass -m <message> (or -m - to read one from stdin)", verb)
	case msg == "":
		return "", nil
	case msg == "-":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading commit message from stdin: %w", err)
		}
		return string(b), nil
	}
	return msg, nil
}
