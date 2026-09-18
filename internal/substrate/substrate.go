// Package substrate looks after the external environments work is proved
// on — a test cluster, a device farm, a staging account. A substrate is
// what an env prerequisite (internal/envprobe) is not: scarce, slow,
// stateful and shared. It can be brought up and reset as well as probed,
// it expires, and two jobs using it at once are not slow but wrong — wrong
// in a way that reads as the code's fault. So this package answers three
// questions and nothing else: what state is it in (Status), who may use it
// now (Acquire — one holder per substrate, across every gummi process on
// the workspace), and can it be made ready (Lease.EnsureReady).
//
// The commands are operator config from outside every worktree
// (config.Substrate), as env probes are, and run in the workspace root.
// Nothing here knows about goals, cards or credits: what a minute of
// substrate costs, and who is entitled to spend it, is policy that lives
// with its caller.
package substrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/state"
)

// State is what a substrate can be asked to do right now.
type State int

const (
	// Ready: the probe passed and it has not expired. A job may take it.
	Ready State = iota
	// Held: a job has it. Nothing is probed meanwhile — a health check
	// fired into someone else's experiment is its own kind of interference.
	Held
	// Absent: the probe said, cleanly, that it is not there.
	Absent
	// Broken: the probe could not give an answer — it timed out, could not
	// start, or died. Something is there and it is not well.
	Broken
	// Expired: it outlived its TTL. Whatever the probe says, the machines
	// may be reclaimed under a job at any moment.
	Expired
)

func (s State) String() string {
	return [...]string{"ready", "held", "absent", "broken", "expired"}[s]
}

// Holder says who has a substrate and what for.
type Holder struct {
	Who     string    `json:"who"`               // a goal or card id, or "user"
	Purpose string    `json:"purpose,omitempty"` // "integration run of ovn-matrix", "provision"
	PID     int       `json:"pid"`
	Since   time.Time `json:"since"`
}

func (h Holder) String() string {
	if h.Who == "" {
		return "another process"
	}
	if h.Purpose == "" {
		return h.Who
	}
	return h.Who + " (" + h.Purpose + ")"
}

// Status is one substrate at one moment.
type Status struct {
	Name     string
	Describe string
	State    State
	// Detail is the probe's own output, or why no probe ran.
	Detail string
	// Holder is set when State is Held.
	Holder Holder
	// ProvisionedAt is when gummi last provisioned it; zero when it never
	// has (someone brought it up by hand), in which case ExpiresAt is
	// unknown and zero too.
	ProvisionedAt time.Time
	ExpiresAt     time.Time
	CheckedAt     time.Time
}

// Fits reports whether a job expected to take d can finish before the
// substrate expires. A substrate with no known expiry fits anything.
func (s Status) Fits(d time.Duration) bool {
	return s.ExpiresAt.IsZero() || s.CheckedAt.Add(d).Before(s.ExpiresAt)
}

// ErrUnknown is returned for a name no substrate is configured under.
var ErrUnknown = errors.New("no such substrate")

// ErrNotReady is returned by EnsureReady when nothing it may do made the
// substrate ready.
var ErrNotReady = errors.New("the substrate could not be made ready")

// HeldError is returned by Acquire when someone else has the substrate.
type HeldError struct {
	Name   string
	Holder Holder
}

func (e *HeldError) Error() string {
	return fmt.Sprintf("%s is held by %s", e.Name, e.Holder)
}

// Manager knows a workspace's substrates.
type Manager struct {
	dir   string // where the locks and records live
	root  string // where the commands run
	specs map[string]config.Substrate
	// Now is the clock; tests replace it.
	Now func() time.Time
}

// New returns a Manager keeping its locks and records under stateDir and
// running its commands in root.
func New(stateDir, root string, specs map[string]config.Substrate) *Manager {
	return &Manager{dir: filepath.Join(stateDir, "substrates"), root: root, specs: specs, Now: time.Now}
}

// Root is where the substrates' commands run.
func (m *Manager) Root() string { return m.root }

// Names lists the configured substrates, sorted.
func (m *Manager) Names() []string {
	out := make([]string, 0, len(m.specs))
	for n := range m.specs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Has reports whether name is a configured substrate.
func (m *Manager) Has(name string) bool {
	_, ok := m.specs[name]
	return ok
}

func (m *Manager) lockPath(name string) string   { return filepath.Join(m.dir, name+".lock") }
func (m *Manager) holderPath(name string) string { return filepath.Join(m.dir, name+".holder.json") }
func (m *Manager) recordPath(name string) string { return filepath.Join(m.dir, name+".json") }

// record is what gummi remembers about a substrate between processes.
type record struct {
	ProvisionedAt time.Time `json:"provisioned_at,omitempty"`
}

func (m *Manager) readRecord(name string) record {
	var r record
	if raw, err := os.ReadFile(m.recordPath(name)); err == nil {
		_ = json.Unmarshal(raw, &r)
	}
	return r
}

// Status reads a substrate's state. A held substrate is reported held
// without being probed; any other is probed now, with no cache — the
// caller decides how often that is worth doing.
func (m *Manager) Status(ctx context.Context, name string) (Status, error) {
	spec, ok := m.specs[name]
	if !ok {
		return Status{}, fmt.Errorf("%q: %w", name, ErrUnknown)
	}
	st := m.base(name, spec)
	release, err := state.AcquireLock(m.lockPath(name))
	if errors.Is(err, state.ErrLocked) {
		st.State, st.Holder = Held, m.readHolder(name)
		st.Detail = "held by " + st.Holder.String()
		return st, nil
	}
	if err != nil {
		return st, err
	}
	release()
	m.probe(ctx, spec, &st)
	return st, nil
}

func (m *Manager) base(name string, spec config.Substrate) Status {
	st := Status{Name: name, Describe: spec.Describe, CheckedAt: m.Now()}
	st.ProvisionedAt = m.readRecord(name).ProvisionedAt
	if ttl := spec.Lifetime(); ttl > 0 && !st.ProvisionedAt.IsZero() {
		st.ExpiresAt = st.ProvisionedAt.Add(ttl)
	}
	return st
}

// probe classifies the way an env probe does, then lets expiry outrank a
// passing answer.
func (m *Manager) probe(ctx context.Context, spec config.Substrate, st *Status) {
	out, code, err := RunShell(ctx, m.root, spec.Probe, probeTimeout, []string{"GUMMI_SUBSTRATE=" + st.Name}, nil)
	st.Detail = strings.TrimSpace(out)
	switch {
	case err != nil || code == 126 || code == 127 || code < 0:
		st.State = Broken
		if err != nil && st.Detail == "" {
			st.Detail = err.Error()
		}
	case code != 0:
		st.State = Absent
	default:
		st.State = Ready
	}
	if st.State == Ready && !st.ExpiresAt.IsZero() && !st.CheckedAt.Before(st.ExpiresAt) {
		st.State = Expired
		st.Detail = "provisioned " + st.ProvisionedAt.Format(time.RFC3339) + " and past its ttl"
	}
}

func (m *Manager) readHolder(name string) Holder {
	var h Holder
	if raw, err := os.ReadFile(m.holderPath(name)); err == nil {
		_ = json.Unmarshal(raw, &h)
	}
	return h
}

// Lease is exclusive use of one substrate. It is an advisory lock on an
// open file, so the process that dies holding one lets go of it: a crash
// can leave a substrate dirty, never unreachable.
type Lease struct {
	m       *Manager
	name    string
	spec    config.Substrate
	release func()
}

// Acquire takes name for h, or fails at once with a *HeldError: a caller
// that would rather wait decides how.
func (m *Manager) Acquire(name string, h Holder) (*Lease, error) {
	spec, ok := m.specs[name]
	if !ok {
		return nil, fmt.Errorf("%q: %w", name, ErrUnknown)
	}
	release, err := state.AcquireLock(m.lockPath(name))
	if errors.Is(err, state.ErrLocked) {
		return nil, &HeldError{Name: name, Holder: m.readHolder(name)}
	}
	if err != nil {
		return nil, err
	}
	if h.PID == 0 {
		h.PID = os.Getpid()
	}
	if h.Since.IsZero() {
		h.Since = m.Now()
	}
	if raw, err := json.Marshal(h); err == nil {
		_ = atomicfile.Write(m.holderPath(name), raw, 0o600)
	}
	return &Lease{m: m, name: name, spec: spec, release: release}, nil
}

// Name is the substrate this lease holds.
func (l *Lease) Name() string { return l.name }

// Release gives the substrate up. Safe to call more than once.
func (l *Lease) Release() {
	if l == nil || l.release == nil {
		return
	}
	_ = os.Remove(l.m.holderPath(l.name))
	l.release()
	l.release = nil
}

// Status probes the held substrate.
func (l *Lease) Status(ctx context.Context) Status {
	st := l.m.base(l.name, l.spec)
	l.m.probe(ctx, l.spec, &st)
	return st
}

// Op is one thing EnsureReady did to a substrate, for whoever is keeping
// count of what bringing it up cost.
type Op struct {
	Kind   string // "reset" or "provision"
	OK     bool
	Took   time.Duration
	Output string
}

// EnsureReady makes the held substrate ready if it is not: a reset when
// there is one and the substrate has not expired, then a provision when
// there is one. The reset is tried even when the probe says absent: a
// probe's exit status cannot tell "not there" from "there and dirty", a
// reset of nothing fails in seconds, and the step it might save is the
// slowest thing gummi ever waits for. It reports each step it took. It tries each at most once —
// how many times a caller comes back is the caller's budget to spend.
func (l *Lease) EnsureReady(ctx context.Context, log io.Writer) ([]Op, Status, error) {
	st := l.Status(ctx)
	if st.State == Ready {
		return nil, st, nil
	}
	var ops []Op
	if l.spec.Reset != "" && st.State != Expired {
		ops = append(ops, l.run(ctx, "reset", l.spec.Reset, log))
		if st = l.Status(ctx); st.State == Ready {
			return ops, st, nil
		}
	}
	if l.spec.Provision != "" {
		op := l.run(ctx, "provision", l.spec.Provision, log)
		ops = append(ops, op)
		if op.OK {
			l.m.stampProvisioned(l.name)
		}
		if st = l.Status(ctx); st.State == Ready {
			return ops, st, nil
		}
	}
	return ops, st, fmt.Errorf("%s is %s: %w", l.name, st.State, ErrNotReady)
}

// Provision brings the held substrate up again whatever state it is in —
// for a substrate that is ready but will not live long enough for the job
// that wants it.
func (l *Lease) Provision(ctx context.Context, log io.Writer) (Op, bool) {
	if l.spec.Provision == "" {
		return Op{}, false
	}
	op := l.run(ctx, "provision", l.spec.Provision, log)
	if op.OK {
		l.m.stampProvisioned(l.name)
	}
	return op, true
}

// Reset returns the held substrate to its known state. A substrate with no
// reset command is left as it is, which is reported as having done nothing.
func (l *Lease) Reset(ctx context.Context, log io.Writer) (Op, bool) {
	if l.spec.Reset == "" {
		return Op{}, false
	}
	return l.run(ctx, "reset", l.spec.Reset, log), true
}

func (l *Lease) run(ctx context.Context, kind, cmd string, log io.Writer) Op {
	start := l.m.Now()
	out, code, err := RunShell(ctx, l.m.root, cmd, l.spec.OpTimeout(), []string{"GUMMI_SUBSTRATE=" + l.name}, log)
	return Op{Kind: kind, OK: err == nil && code == 0, Took: l.m.Now().Sub(start), Output: out}
}

func (m *Manager) stampProvisioned(name string) {
	if raw, err := json.Marshal(record{ProvisionedAt: m.Now().UTC()}); err == nil {
		_ = atomicfile.Write(m.recordPath(name), raw, 0o600)
	}
}

// probeTimeout bounds a readiness probe. Longer than an env probe's 60s:
// asking five machines whether their routing has converged is not a
// `command -v`.
const probeTimeout = 3 * time.Minute

// maxOutput bounds what a command's output keeps in memory; a log writer
// gets all of it.
const maxOutput = 16 << 10

// RunShell runs cmd through sh in dir, in its own process group so a
// timeout takes the whole tree with it. It returns the (bounded) combined
// output, the exit code (-1 when the command never produced one) and an
// error only for the ways a command fails to give an answer at all.
func RunShell(ctx context.Context, dir, cmd string, timeout time.Duration, env []string, log io.Writer) (string, int, error) {
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c := exec.CommandContext(rctx, "sh", "-c", cmd) //nolint:gosec // operator config from outside the worktree
	c.Dir = dir
	c.Env = append(os.Environ(), env...)
	var buf bytes.Buffer
	var w io.Writer = &tail{buf: &buf, max: maxOutput}
	if log != nil {
		w = io.MultiWriter(w, log)
	}
	c.Stdout, c.Stderr = w, w
	c.SysProcAttr = phaseProcAttr()
	c.Cancel = func() error {
		if c.Process != nil {
			_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	c.WaitDelay = 2 * time.Second
	// A parent-death signal is tied to the THREAD that started the child,
	// not the process, so the goroutine stays on its thread for the
	// command's life: otherwise the runtime retiring an idle thread would
	// kill a healthy command.
	runtime.LockOSThread()
	err := c.Run()
	runtime.UnlockOSThread()
	out := buf.String()
	if rctx.Err() != nil {
		return out, -1, fmt.Errorf("timed out after %s", timeout)
	}
	if err == nil {
		return out, 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		return out, exit.ExitCode(), nil
	}
	return out, -1, err
}

// tail keeps the last max bytes written to it.
type tail struct {
	buf *bytes.Buffer
	max int
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf.Write(p)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
	}
	return len(p), nil
}
