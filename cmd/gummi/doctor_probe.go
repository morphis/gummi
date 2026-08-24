package main

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/morphis/gummi/internal/agent"
)

// probeTimeout bounds one live reachability probe's turn. It is a
// compiled-in constant (no config knob): a probe that neither idles nor
// errors within it degrades to unknown rather than hanging the report.
const probeTimeout = 30 * time.Second

// probeResult is the outcome of one per-role reachability probe. ok and
// fail are definitive (the backend served / rejected the model); unknown
// covers everything inconclusive — a missing binary, a session that cannot
// start offline, a timeout, or an interactive-login backend that cannot
// run without a human login. Only fail flips readiness.
type probeResult string

const (
	reachOK      probeResult = "ok"
	reachFail    probeResult = "fail"
	reachUnknown probeResult = "unknown"
)

// probeModel asks the backend whether it can actually serve model — the
// live half of the per-role reachability probe. It constructs the adapter
// the way a run would (startAdapter owns the env wiring) and drives one
// minimal turn, mapping the outcome:
//
//   - headless has no model to reach (the role routes through the env
//     command), so it is always ok.
//   - opencode/codex: EventIdle → ok, EventError → fail, a construction
//     error (missing binary, closed adapter), a session that cannot start,
//     a Send failure, a timeout, or a closed event stream → unknown. A
//     missing binary is unknown here — the backend:<name> check already
//     owns "not on PATH".
//   - claude/copilot: attempted the same way; a probe that cannot run
//     without a human login (binary absent, session construction failing
//     for an offline reason) degrades to unknown, mirroring auth:<name>.
//
// probeModel is the default value of doctorOpts.Probe; tests inject a stub
// so the report stays offline and deterministic.
func probeModel(bi backendInfo, model string, timeout time.Duration) probeResult {
	if bi.name == "headless" {
		return reachOK
	}
	ag, err := startAdapter(bi.name)
	if err != nil {
		return reachUnknown
	}
	defer func() { _ = ag.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	sess, err := ag.NewSession(ctx, agent.SessionOpts{Model: model})
	if err != nil {
		return reachUnknown
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(ctx, "ok"); err != nil {
		return reachUnknown
	}
	for {
		select {
		case e, ok := <-sess.Events():
			if !ok {
				return reachUnknown
			}
			switch e.Kind {
			case agent.EventIdle:
				return reachOK
			case agent.EventError:
				return reachFail
			}
		case <-ctx.Done():
			return reachUnknown
		}
	}
}

// zzAuthProbeTimeout bounds the offline zz auth probe's `zz status` call.
const zzAuthProbeTimeout = 5 * time.Second

// zzAuthResult is the outcome of the offline `auth:zz` doctor probe. Summary
// is always one of a small fixed set of strings — never the subcommand's raw
// stdout/stderr, which can name provider tokens.
type zzAuthResult struct {
	Status  string
	Summary string
}

// zzAuthProbeFn runs zz's offline status probe, bounded by timeout, and
// classifies the outcome. It is the seam doctorOpts.ZZAuthProbe injects a
// stub into so the default test suite never spawns a real zz process.
type zzAuthProbeFn func(bin string, timeout time.Duration) zzAuthResult

// probeZZAuth is the default zzAuthProbeFn: it runs `<bin> status`, zz's
// offline-status subcommand (named in FD-100's problem statement), which
// reports the configured provider set without needing a live one. It never
// surfaces the subcommand's stdout/stderr — only a classified status word
// and a short fixed Summary reach the doctor report.
func probeZZAuth(bin string, timeout time.Duration) zzAuthResult {
	if _, err := exec.LookPath(bin); err != nil {
		return zzAuthResult{Status: statusUnknown, Summary: "zz not on PATH"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "status")
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return zzAuthResult{Status: statusUnknown, Summary: "probe timed out"}
	}
	if err == nil {
		return zzAuthResult{Status: statusOK, Summary: "configured"}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return zzAuthResult{Status: statusFail, Summary: "not configured"}
	}
	return zzAuthResult{Status: statusUnknown, Summary: "unexpected exit"}
}
