package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Headless is an Agent backed by an external agent process that speaks a
// line-delimited JSON protocol on stdio (DESIGN §4.1: the interface is
// pluggable beyond the Copilot SDK). Each session spawns the configured
// command in the feature's worktree; gummi writes turns to the process's
// stdin and reads its events from stdout. This is the generic escape
// hatch for any agent that can emit a handful of JSON event lines.
//
// Wire protocol — gummi → agent, one JSON object per line on stdin:
//
//	{"type":"init","workdir":"…","model":"…","hints":[…]}
//	{"type":"send","text":"…"}
//	{"type":"interrupt"}
//
// agent → gummi, one JSON object per line on stdout:
//
//	{"type":"text","text":"…"}      → EventTextDelta
//	{"type":"reasoning","text":"…"} → EventReasoningDelta
//	{"type":"message","text":"…"}   → EventMessage
//	{"type":"tool","name":"…","detail":"…"} → EventToolCall (detail optional:
//	                                  the salient argument, e.g. command or path)
//	{"type":"usage","credits":N,"input":I,"output":O,"model":"…"} → EventUsage
//	{"type":"ask","id":"…","ask":{…}} → EventClientToolCall (ask_user)
//	{"type":"idle"}                 → EventIdle
//	{"type":"error","message":"…"}  → EventError
//
// gummi answers an ask with a turn frame back to the child:
//
//	{"type":"resolve","id":"…","result":"…"}
//
// The declared client tools are advertised in the init frame's "tools".
// The child inherits gummi's environment, so any provider config it needs
// (base URL, API key) it reads from env directly — gummi does not carry
// endpoint or key material in the protocol.
type Headless struct {
	argv []string

	mu       sync.Mutex
	sessions []*headlessSession
	closed   bool
}

// NewHeadless returns an Agent that runs argv (command + fixed args) once
// per session. argv must be non-empty.
func NewHeadless(argv []string) (*Headless, error) {
	if len(argv) == 0 {
		return nil, errors.New("headless agent: empty command")
	}
	return &Headless{argv: append([]string(nil), argv...)}, nil
}

// Name implements Agent: the configured command's basename, so the UI
// names the binary doing the work.
func (h *Headless) Name() string { return filepath.Base(h.argv[0]) }

// Capabilities implements Agent. The protocol carries interrupt and
// usage frames, so those are supported; there is no session persistence,
// so resume is not. A given child may choose not to emit usage frames —
// the orchestrator meters whatever arrives and enforces caps itself as a
// backstop regardless.
func (h *Headless) Capabilities() Capabilities {
	return Capabilities{Interrupt: true, UsageEvents: true, ClientTools: true}
}

// headlessCreditRateEnv is the operator's escape hatch for pricing a
// headless child's token spend into credits — the local-heavy case, where
// the child speaks to llama.cpp and the engine still meters against a
// credit-denominated envelope. Absent or unparseable, CreditRate returns
// 0 and the engine's default rate applies.
const headlessCreditRateEnv = "GUMMI_HEADLESS_CREDITS_PER_1K"

// CreditRate implements Agent. Reads the env-configured rate (credits per
// 1k tokens) so operator config, not a repo-committed profile, controls
// how a local/BYOK-behind-headless endpoint is priced. Zero disables the
// override; the engine falls back to its own default.
func (h *Headless) CreditRate(string) float64 {
	v := strings.TrimSpace(os.Getenv(headlessCreditRateEnv))
	if v == "" {
		return 0
	}
	r, err := strconv.ParseFloat(v, 64)
	if err != nil || r < 0 {
		return 0
	}
	return r
}

// headlessInit is the gummi → agent init frame.
type headlessInit struct {
	Type    string    `json:"type"`
	WorkDir string    `json:"workdir"`
	Model   string    `json:"model,omitempty"`
	Hints   []string  `json:"hints,omitempty"`
	Tools   []ToolDef `json:"tools,omitempty"`
}

// NewSession implements Agent: spawn the command in opts.WorkDir and send
// the init frame.
func (h *Headless) NewSession(_ context.Context, opts SessionOpts) (Session, error) {
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return nil, errors.New("headless agent is closed")
	}

	// A ReadOnly research session runs in the main checkout with no
	// worktree. This backend has no structural read-only cage
	// (ReadOnlyEnforce is false), so refuse rather than silently run
	// read-write — the engine gate is the first line, this is the second
	// so a stray direct call cannot drop the deny.
	if opts.ReadOnly {
		return nil, errors.New("headless backend cannot enforce a read-only research session; " +
			"point this role at `claude` or `opencode`, or accept that autonomous research cannot run on headless")
	}

	// spawn + init OUTSIDE the lock (fork/exec and the init write must not
	// serialize session creation or block a concurrent Close).
	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, h.argv[0], h.argv[1:]...) //nolint:gosec // argv is operator config (GUMMI_AGENT_CMD), not agent/repo input
	cmd.Dir = opts.WorkDir
	// The child inherits gummi's full environment: a generic operator-chosen
	// agent binary may need arbitrary env (its own provider config, an API
	// key, HOME, PATH) so — unlike the Copilot CLI, which takes a scrubbed
	// allowlist — this passes the whole environment. The command is trusted
	// operator config, not repo input.
	cmd.Env = os.Environ()
	// Run the child in its own process group and, on cancel/close, kill the
	// whole group: an agent binary spawns tool subprocesses (bash, editors)
	// that would otherwise orphan and keep the stdout pipe open, stalling
	// read()'s EOF and burning Close's readDone timeout. WaitDelay force-
	// closes the pipes if a grandchild lingers so Wait can't hang.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	stderr := &capWriter{max: 8 << 10}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("headless stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("headless stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("starting headless agent %q: %w", h.argv[0], err)
	}

	s := &headlessSession{
		cmd:      cmd,
		cancel:   cancel,
		stdin:    stdin,
		stderr:   stderr,
		raw:      make(chan Event, 16),
		events:   make(chan Event),
		stop:     make(chan struct{}),
		readDone: make(chan struct{}),
	}
	initFrame := headlessInit{Type: "init", WorkDir: opts.WorkDir, Model: opts.Model, Hints: opts.SystemHints, Tools: opts.Tools}
	if err := s.write(initFrame); err != nil {
		// no goroutines started yet: tear down directly (Close would block
		// on readDone, which read() will never close).
		_ = stdin.Close()
		cancel()
		_ = cmd.Wait()
		return nil, fmt.Errorf("headless init: %w", err)
	}
	go s.forward()
	go s.read(stdout)

	// register under the lock, re-checking closed so a session started
	// concurrently with Close is torn down rather than leaked.
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = s.Close()
		return nil, errors.New("headless agent is closed")
	}
	h.sessions = append(h.sessions, s)
	h.mu.Unlock()
	return s, nil
}

// Close implements Agent: end every live session.
func (h *Headless) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, s := range h.sessions {
		_ = s.Close()
	}
	return nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

type headlessSession struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stdin  io.WriteCloser
	stderr *capWriter // bounded tail of the child's stderr, for crash diagnostics

	raw      chan Event
	events   chan Event
	stop     chan struct{}
	readDone chan struct{} // closed when read() has finished draining stdout

	wmu       sync.Mutex // serializes writes to stdin
	closeOnce sync.Once
	waitOnce  sync.Once // guards the single cmd.Wait() shared by read() and Close()
	waitErr   error
}

// reap waits for the child exactly once (read() reaps a self-exited child;
// Close reaps a killed one) and caches the exit status. Wait is safe here
// because the only stdout reader — read() — is done before either caller
// reaches it.
func (s *headlessSession) reap() error {
	s.waitOnce.Do(func() { s.waitErr = s.cmd.Wait() })
	return s.waitErr
}

// stopping reports whether Close has begun tearing the session down, so a
// kill-induced stdout EOF isn't misreported as a child crash.
func (s *headlessSession) stopping() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

// capWriter keeps at most the last max bytes written — a bounded tail of a
// subprocess's stderr, enough to carry a panic/exit message without letting
// a chatty child grow memory without bound.
type capWriter struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.max {
		w.buf = append([]byte(nil), w.buf[len(w.buf)-w.max:]...)
	}
	return len(p), nil
}

func (w *capWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}

func (s *headlessSession) Events() <-chan Event { return s.events }

// forward owns events: it copies raw→events and closes events exactly
// once (the read goroutine and Send never touch events directly).
func (s *headlessSession) forward() {
	defer close(s.events)
	for {
		select {
		case <-s.stop:
			return
		case e := <-s.raw:
			select {
			case s.events <- e:
			case <-s.stop:
				return
			}
		}
	}
}

// read scans the child's stdout, maps each JSON line to an Event, and
// feeds the forwarder. A malformed line becomes an EventError rather than
// silently vanishing. When stdout closes (the child exited), it stops.
func (s *headlessSession) read(stdout io.Reader) {
	defer close(s.readDone)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // allow large messages
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		ev, ok := decodeHeadless(line)
		if !ok {
			continue
		}
		select {
		case s.raw <- ev:
		case <-s.stop:
			return
		}
	}
	// stdout closed: the child exited (a per-turn end is an explicit "idle"
	// frame, not EOF — so EOF means the process is gone). Reap it for the
	// exit status and diagnose: a scan error or a non-zero exit is a failed
	// turn, not a finished one. Without this a child that panics or exits
	// non-zero mid-turn produces a clean EOF that would be reported as a
	// successful idle, and the orchestrator would advance on garbage output.
	waitErr := s.reap()
	final := Event{Kind: EventIdle}
	if err := sc.Err(); err != nil {
		final = Event{Kind: EventError, Err: fmt.Errorf("headless agent stream: %w", err)}
	} else if waitErr != nil && !s.stopping() {
		detail := strings.TrimSpace(s.stderr.String())
		if detail == "" {
			detail = waitErr.Error()
		}
		final = Event{Kind: EventError, Err: fmt.Errorf("headless agent exited abnormally: %s", detail)}
	}
	select {
	case s.raw <- final:
	case <-s.stop:
	}
}

// headlessEvent is the agent → gummi frame.
type headlessEvent struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Detail  string          `json:"detail"` // tool: salient argument (optional)
	Message string          `json:"message"`
	Credits float64         `json:"credits"`
	Input   int64           `json:"input"`
	Output  int64           `json:"output"`
	Model   string          `json:"model"`
	ID      string          `json:"id"`  // client-tool call id (ask)
	Ask     json.RawMessage `json:"ask"` // ask_user payload
}

func decodeHeadless(line []byte) (Event, bool) {
	var m headlessEvent
	if err := json.Unmarshal(line, &m); err != nil {
		return Event{Kind: EventError, Err: fmt.Errorf("headless agent sent malformed JSON: %w", err)}, true
	}
	switch m.Type {
	case "text":
		return Event{Kind: EventTextDelta, Text: m.Text}, true
	case "reasoning":
		return Event{Kind: EventReasoningDelta, Text: m.Text}, true
	case "message":
		return Event{Kind: EventMessage, Text: m.Text}, true
	case "tool":
		// the child knows its own workdir and is expected to emit short,
		// relative details; gummi only normalizes to one bounded line.
		return Event{Kind: EventToolCall, Tool: m.Name, Detail: collapseDetail("", m.Detail)}, true
	case "ask":
		// the ask payload is passed through verbatim; the orchestrator
		// parses it into a question (name defaults to ask_user).
		return Event{Kind: EventClientToolCall, ToolCall: &ToolCall{ID: m.ID, Name: cmpOr(m.Name, "ask_user"), Args: m.Ask}}, true
	case "usage":
		return Event{Kind: EventUsage, Usage: Usage{Credits: m.Credits, InputTokens: m.Input, OutputTokens: m.Output, Model: m.Model}}, true
	case "idle":
		return Event{Kind: EventIdle}, true
	case "error":
		return Event{Kind: EventError, Err: errors.New(cmpOr(m.Message, "headless agent reported an error"))}, true
	default:
		return Event{}, false // unknown type: ignore rather than guess
	}
}

// headlessWriteTimeout bounds a single stdin write. A well-behaved child
// always drains stdin promptly (the protocol is asynchronous), so this only
// trips when the child has wedged — in which case a write that blocked
// forever would hang the caller, including the pump goroutine's budget-stop
// Interrupt. Failing the write instead lets teardown proceed.
const headlessWriteTimeout = 10 * time.Second

func (s *headlessSession) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	s.wmu.Lock()
	defer s.wmu.Unlock()
	// The stdin pipe is an *os.File and supports write deadlines; bound the
	// write so a child that stopped reading can't block us indefinitely.
	if f, ok := s.stdin.(interface{ SetWriteDeadline(time.Time) error }); ok {
		_ = f.SetWriteDeadline(time.Now().Add(headlessWriteTimeout))
	}
	if _, err := s.stdin.Write(b); err != nil {
		return err
	}
	return nil
}

func (s *headlessSession) Send(_ context.Context, msg string) error {
	return s.write(map[string]string{"type": "send", "text": msg})
}

func (s *headlessSession) Interrupt(_ context.Context) error {
	return s.write(map[string]string{"type": "interrupt"})
}

// Resolve implements ToolResolver: answer a client-tool call (ask_user)
// with result, letting the child resume the model's blocked turn.
func (s *headlessSession) Resolve(_ context.Context, callID, result string) error {
	return s.write(map[string]string{"type": "resolve", "id": callID, "result": result})
}

func (s *headlessSession) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()    // SIGKILL the child → stdout EOF → read()'s Scan ends
		close(s.stop) // stop forward; unblock read()'s raw sends
		_ = s.stdin.Close()
		// join the read goroutine before Wait: reading a StdoutPipe after
		// Wait closes it is a documented error, so wait for the pipe to be
		// drained first (bounded, since cancel() EOFs it).
		select {
		case <-s.readDone:
		case <-time.After(3 * time.Second):
		}
		_ = s.reap() // reap (read() may already have)
	})
	return nil
}
