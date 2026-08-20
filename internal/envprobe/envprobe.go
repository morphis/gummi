// Package envprobe runs operator-configured environment prerequisite probes.
//
// The commands come from .gummi/config.yaml (the Env map), not from an agent-
// authored artifact. That ownership difference matters for sandbox policy:
// unlike agent-authored gummi-checks, which the engine does not auto-run in
// guarded mode, env probes run in all sandbox modes including guarded. The
// probe source is operator config from outside the worktree; only the working
// directory is the card's worktree.
//
// Probes run fresh on every Verify kickoff and every `gummi doctor` run — no
// cache, no TTL. Each probe is bounded by a compiled-in 60s cap; a hung probe
// is killed by process group so it cannot orphan children.
package envprobe

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/morphis/gummi/internal/config"
)

// Result is the outcome of probing one environment prerequisite.
type Result struct {
	Name     string
	Describe string
	Output   string
	Present  bool
	Err      error
}

// probeTimeout is the compiled-in per-prerequisite cap.
const probeTimeout = 60 * time.Second

// maxOutput bounds captured output so a chatty probe can't blow up the
// transcript or doctor report.
const maxOutput = 4 << 10

// Run probes each declared prerequisite and returns one Result per
// prerequisite, sorted by name. The classification is three-state:
//   - Present: clean exit 0 (Present=true, Err=nil).
//   - Absent: clean non-zero exit, excluding shell "not executable"/"command
//     not found" codes 126/127 (Present=false, Err=nil).
//   - Errored: timeout, cancellation, start failure, shell 126/127, or any
//     negative exit code (Err != nil).
//
// Only a clean ABSENT licenses skipping an [env: <name>] verification step.
func Run(ctx context.Context, dir string, prereqs map[string]config.EnvPrereq) []Result {
	names := make([]string, 0, len(prereqs))
	for name := range prereqs {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]Result, 0, len(prereqs))
	for _, name := range names {
		p := prereqs[name]
		out = append(out, runOne(ctx, dir, name, p))
	}
	return out
}

func runOne(ctx context.Context, dir string, name string, p config.EnvPrereq) Result {
	res := Result{Name: name, Describe: p.Describe}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(pctx, "sh", "-c", p.Probe) //nolint:gosec // operator config from .gummi/config.yaml outside the worktree
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second

	err := cmd.Run()
	res.Output = truncate(buf.String())

	// Classification precedence is fixed and context-first.
	if pctx.Err() != nil {
		res.Err = pctx.Err()
		return res
	}
	if err == nil {
		res.Present = true
		return res
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		// Shell "not executable" / "command not found" codes indicate a
		// broken or misspelled probe, not a clean answer.
		if code == 126 || code == 127 {
			res.Err = err
			return res
		}
		if code > 0 {
			return res
		}
	}
	res.Err = err
	return res
}

func truncate(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	return s[:maxOutput] + "\n…(truncated)"
}

// StatusString returns a fixed display label for a result.
func StatusString(r Result) string {
	switch {
	case r.Err != nil:
		return "errored"
	case r.Present:
		return "PRESENT"
	default:
		return "ABSENT"
	}
}

// FormatReport builds a compact PRESENT/ABSENT/errored report block listing
// each prerequisite with its describe and output.
func FormatReport(results []Result) string {
	var b strings.Builder
	for _, r := range results {
		b.WriteString("- ")
		b.WriteString(r.Name)
		b.WriteString(": ")
		b.WriteString(StatusString(r))
		if r.Describe != "" {
			b.WriteString(" — ")
			b.WriteString(r.Describe)
		}
		b.WriteString("\n")
		if r.Output != "" {
			b.WriteString(indentLines(r.Output))
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func indentLines(s string) string {
	if s == "" {
		return ""
	}
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}
