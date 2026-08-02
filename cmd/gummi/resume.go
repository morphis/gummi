package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/driver"
)

// runResume implements `gummi resume <FD-id> [--answer <text> | --approve
// | --request-changes <note>]` (DESIGN §8.2): it rehydrates the parked
// feature, applies the caller's decision, and drives on — streaming the
// same NDJSON and exiting with a typed status. With no decision flag it
// simply re-runs the parked stage (after a timeout, an escalation, or an
// envelope top-up).
func runResume(args []string) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	answer := fs.String("answer", "", "answer a delegated ask_user question")
	approve := fs.Bool("approve", false, "approve a caller design gate")
	requestChanges := fs.String("request-changes", "", "send a caller design gate back with a note")
	gate := fs.String("gate-approval", driver.GateAuto, "who approves later design gates: auto|caller")
	timeout := fs.Duration("stage-timeout", defaultStageTimeout, "per-stage inactivity timeout (0 disables)")
	autonomous := fs.Bool("autonomous", false, "auto-take the recommended answer instead of checkpointing questions")
	verbose := fs.Bool("verbose", false, "add per-tool-call activity lines to the stream")
	ref := fs.String("ref", "", "external correlation id, echoed in the stream")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi resume <FD-id> [--answer <text> | --approve | --request-changes <note>]")
		fs.PrintDefaults()
	}
	// The id leads (`resume FD-042 --answer no`), but Go's flag parser stops
	// at the first positional — so pull a leading id out before parsing, and
	// still accept a trailing one (flags-first) as a fallback.
	var idArg string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		idArg, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if idArg == "" {
		idArg = fs.Arg(0)
	}
	if idArg == "" || fs.NArg() > 1 {
		fs.Usage()
		return fmt.Errorf("resume needs exactly one work-item id")
	}
	id, err := domain.ParseFeatureID(idArg)
	if err != nil {
		return err
	}

	in, err := resumeInput(*answer, *approve, *requestChanges, isSet(fs, "answer"), isSet(fs, "request-changes"))
	if err != nil {
		return err
	}
	// resume carries no envelope (the feature already has one); the rest of
	// the driving options mirror run so the continued tail behaves the same.
	if *gate != driver.GateAuto && *gate != driver.GateCaller {
		return fmt.Errorf("--gate-approval must be %q or %q, got %q", driver.GateAuto, driver.GateCaller, *gate)
	}
	opts := driver.Options{
		GateApproval: *gate, StageTimeout: *timeout,
		Autonomous: *autonomous, Verbose: *verbose, Ref: *ref,
	}

	return withRunEngine(func(ctx context.Context, d *driver.Driver) (driver.Outcome, error) {
		return d.Resume(ctx, id, in)
	}, opts)
}

// resumeInput builds the ResumeInput from the mutually exclusive decision
// flags, refusing more than one. All unset means "re-run the parked
// stage". answerSet/changesSet distinguish an explicitly-empty flag from
// an unset one, so `--answer ""` is still an (empty-answer) decision the
// driver can reject cleanly rather than silently re-running.
func resumeInput(answer string, approve bool, requestChanges string, answerSet, changesSet bool) (driver.ResumeInput, error) {
	n := 0
	var in driver.ResumeInput
	if answerSet {
		n++
		a := answer
		in = driver.ResumeInput{Answer: &a}
	}
	if approve {
		n++
		in = driver.ResumeInput{Approve: true}
	}
	if changesSet {
		n++
		c := requestChanges
		in = driver.ResumeInput{RequestChanges: &c}
	}
	if n > 1 {
		return driver.ResumeInput{}, fmt.Errorf("give at most one of --answer, --approve, --request-changes")
	}
	return in, nil
}

// isSet reports whether a flag was present on the command line (vs left at
// its zero default), so an explicit empty value is distinguishable.
func isSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}
