// Package verify runs the repo's fixed Verify-stage check commands in a
// feature's worktree and reports pass/fail with output. The commands
// come from .gummi/config.yaml — repo-controlled input (DESIGN §4.4).
//
// Two callers, two safety stories: the manual verify dialog surfaces the
// commands and runs on confirmation (a bare host may be watching); the
// engine runs them autonomously at the Verify stage only in allow-all
// mode, where the sandbox is the boundary and the stage's agent would
// otherwise run the identical shell itself. In guarded mode the engine
// does not auto-run them — the agent handles verification instead.
package verify

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/morphis/gummi/internal/config"
)

// Result is the outcome of one check.
type Result struct {
	Name     string
	Cmd      string
	OK       bool
	ExitCode int
	Output   string // combined stdout+stderr, truncated
	Duration time.Duration
}

// maxOutput bounds captured output so a chatty check can't blow up the
// transcript/UI.
const maxOutput = 8 << 10

// Run executes each check in workDir via the shell and returns the
// results in order. It stops on the first context cancellation but
// otherwise runs every check even if an earlier one fails.
//
// The commands run through `sh -c`, which is the one deliberate shell
// exception in gummi (DESIGN §4.4): they are repo-authored and MUST be
// surfaced to the user before Run is called.
func Run(ctx context.Context, workDir string, checks []config.Check) []Result {
	out := make([]Result, 0, len(checks))
	for _, ch := range checks {
		if ctx.Err() != nil {
			break
		}
		out = append(out, runOne(ctx, workDir, ch))
	}
	return out
}

func runOne(ctx context.Context, workDir string, ch config.Check) Result {
	start := time.Now()
	cmd := exec.CommandContext(ctx, "sh", "-c", ch.Cmd) //nolint:gosec // repo-authored, surfaced before running (DESIGN §4.4)
	cmd.Dir = workDir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()

	res := Result{
		Name:     ch.Name,
		Cmd:      ch.Cmd,
		OK:       err == nil,
		Output:   truncate(buf.String()),
		Duration: time.Since(start),
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			res.ExitCode = exit.ExitCode()
		} else {
			res.ExitCode = -1 // failed to start (e.g. no shell)
		}
	}
	return res
}

func truncate(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	return s[:maxOutput] + "\n…(truncated)"
}

// AllOK reports whether every result passed (vacuously true for none).
func AllOK(results []Result) bool {
	for _, r := range results {
		if !r.OK {
			return false
		}
	}
	return true
}
