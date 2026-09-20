package agent

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Pi is an Agent backed by the pi coding agent's RPC mode:
//
//	pi --mode rpc --model <model> [--provider <provider>] [--no-extensions]
//
// One process per session (cwd = the feature's worktree); commands go to
// the child's stdin as JSON lines, events stream back on stdout as JSON
// lines, and the process stays alive between turns — the long-lived stdio
// shape of the claude adapter, with pi's own wire protocol. A turn ends at
// agent_settled, which pi emits only once no automatic retry, compaction
// retry, or queued continuation remains (agent_end, by contrast, can still
// be followed by any of those). Protocol facts verified against pi 0.85.1:
// strict LF framing; `--model provider/id` resolves provider and id;
// `--tools` is a hard allowlist (a denied tool is never executed, not
// merely unapproved); `--session <id>` reopens a prior session by uuid;
// the child exits 0 on stdin EOF.
//
// pi has no native MCP client, so gummi's tools reach it through a
// generated --extension (pi_extension.go): the extension spawns `gummi
// __mcp` and mirrors its tools/list into pi.registerTool calls. That is
// what Capabilities().MCPTools reports, once SessionOpts.MCPSockPath is
// bound to a feature id or the Workspace flag. pi's RPC mode has no
// approval gate, so guarded collapses to allow-all.
type Pi struct {
	bin string

	mu       sync.Mutex
	sessions []*piSession
	closed   bool
}

// NewPi returns an Agent that drives the pi binary (default "pi", found on
// PATH). It fails fast when the binary is missing.
func NewPi(bin string) (*Pi, error) {
	if bin == "" {
		bin = "pi"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("pi binary %q not found: %w", bin, err)
	}
	return &Pi{bin: resolved}, nil
}

// Name implements Agent.
func (p *Pi) Name() string { return "pi" }

// Capabilities implements Agent. Resume is a real restart-resume: pi
// persists each session's transcript itself (sessionFile from get_state),
// and the engine hands the id back via SessionOpts.ResumeID. Interrupt is
// the abort command. ReadOnlyEnforce is --tools: a ReadOnly session's
// allowlist structurally omits every mutating tool (bash, edit, write,
// powershell), so nothing the model says can arm them. MCPTools reports
// that gummi's tools are reached via the generated extension over
// SessionOpts.MCPSockPath — not via SessionOpts.Tools, which pi has no
// native surface for. WriteCage is cwd-only until pi's edit/write tools
// are proven to refuse paths outside the working directory.
func (p *Pi) Capabilities() Capabilities {
	return Capabilities{Resume: true, UsageEvents: true, Interrupt: true, MCPTools: true, ReadOnlyEnforce: true, WriteCage: WriteCageCwd}
}

// CreditRate implements Agent. pi reports its own USD cost per assistant
// message (see mapLine), so the engine must not re-price its tokens.
func (p *Pi) CreditRate(string) float64 { return 0 }

// piReadOnlyTools is the --tools allowlist for a ReadOnly research
// session: every read/navigation built-in, none of the writers. pi's
// built-in tool roster is read, bash, powershell, edit, write, grep,
// find, ls (verified against the CLI's docs); naming only the readers
// structurally strips bash/edit/write/powershell, which is the whole
// guarantee — no sandbox mode can re-arm a tool the child never has.
func piReadOnlyTools() []string {
	return []string{"read", "grep", "find", "ls"}
}

// piProvider returns the provider a session routes to when its model id
// does not name one ("openrouter/z-ai/glm-flash-latest" carries its own;
// "claude-sonnet-4.5" does not and would hit pi's built-in default).
// Operator config via GUMMI_PI_PROVIDER; empty means "let pi decide".
func piProvider() string { return strings.TrimSpace(os.Getenv("GUMMI_PI_PROVIDER")) }

// piMaterializeExtension renders the MCP tool extension for a session and
// writes it to a temp file pi will load with --extension. An unbound
// session (no socket, or neither feature id nor Workspace) returns an
// empty path and no file — the one case that must stay toolless.
func piMaterializeExtension(opts SessionOpts) (path string, err error) {
	ext, err := buildPiExtensionArgs(opts)
	if err != nil || ext == nil {
		return "", err
	}
	f, err := os.CreateTemp("", "gummi-pi-*.ts")
	if err != nil {
		return "", fmt.Errorf("pi adapter: creating tool extension: %w", err)
	}
	if _, err := f.Write(ext); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("pi adapter: writing tool extension: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("pi adapter: closing tool extension: %w", err)
	}
	return f.Name(), nil
}

// piExecPath locates gummi's own executable when materializing the tool
// extension, so the extension's `gummi __mcp` child is a real gummi rather
// than whatever shadows "gummi" on $PATH. Production uses the real
// os.Executable; tests rebind it (like opencode's).
var piExecPath = os.Executable

// buildPiExtensionArgs resolves the extension's inputs from SessionOpts:
// gummi's own executable (the __mcp child must be a real gummi, not a
// $PATH shadow) and the session's scope.
func buildPiExtensionArgs(opts SessionOpts) ([]byte, error) {
	if opts.MCPSockPath == "" || (opts.FeatureID == "" && !opts.Workspace) {
		return nil, nil
	}
	exe, err := piExecPath()
	if err != nil {
		return nil, fmt.Errorf("pi adapter: locating own executable: %w", err)
	}
	return buildPiExtension(exe, opts.FeatureID, opts.MCPSockPath, opts.Workspace, piMCPToolFromDefs(opts.Tools))
}

// NewSession implements Agent: spawn one pi process in opts.WorkDir. The
// child loads lazily but speaks immediately; a get_state is pipelined
// ahead of the first prompt so SessionID and the model's context window
// are known before the first turn is even written.
func (p *Pi) NewSession(_ context.Context, opts SessionOpts) (Session, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, errors.New("pi agent is closed")
	}
	if opts.Model == "" {
		return nil, errors.New("pi requires a model (provider/id, e.g. openrouter/z-ai/glm-flash-latest)")
	}

	args := []string{"--mode", "rpc", "--no-extensions"}
	if provider := piProvider(); provider != "" {
		args = append(args, "--provider", provider)
	}
	args = append(args, "--model", opts.Model)
	if opts.FeatureID != "" {
		args = append(args, "--name", "gummi-"+opts.FeatureID)
	}
	// A restored ask: pi picks its own conversation back up rather than
	// opening one that has never seen the question. Guarded by a liveness
	// scan of pi's session store (see piResumable) because --session with
	// an unknown id is not a slow start, it is a failed one.
	if opts.ResumeID != "" && piResumable(opts.ResumeID) {
		args = append(args, "--session", opts.ResumeID)
	}
	if opts.ReadOnly {
		args = append(args, "--tools", strings.Join(piReadOnlyTools(), ","))
	}
	// gummi's tools over MCP: materialize the generated extension (see
	// pi_extension.go) and hand it to pi explicitly — explicit --extension
	// paths load even under --no-extensions, so a session whose worktree
	// happens to carry project-local extensions still sees only what gummi
	// built for it. Unbound sessions (no socket, or neither feature id nor
	// Workspace) get no extension and no flag, exactly as before.
	extPath, err := piMaterializeExtension(opts)
	if err != nil {
		return nil, err
	}
	if extPath != "" {
		args = append(args, "--extension", extPath)
	}
	// Guarded and allow-all collapse to the same posture here: pi's RPC
	// mode executes tools without an approval gate (verified 0.85.1), so
	// there is nothing to bridge Guarded onto. Guarded is accepted, like
	// the opencode adapter's, rather than failing a run that cannot be
	// more guarded than its backend.

	// spawn OUTSIDE the lock (fork/exec must not serialize session creation
	// or block a concurrent Close).
	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, p.bin, args...) //nolint:gosec // bin is operator config (GUMMI_PI_BIN), args are gummi-built
	cmd.Dir = opts.WorkDir
	// The child inherits gummi's environment: auth is out of band (pi's
	// auth.json, or the provider's API-key env var), exactly like the
	// claude adapter's ANTHROPIC_API_KEY.
	cmd.Env = os.Environ()
	// pi spawns tool subprocesses (bash) that would otherwise orphan and
	// keep the stdout pipe open, stalling teardown. Same shape as every
	// process-backed adapter: own process group, kill the whole group,
	// WaitDelay force-closes the pipes if a grandchild lingers.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	// capWriter, not strings.Builder: a misconfigured pi can spew
	// arbitrarily to stderr before it gives up.
	stderr := &capWriter{max: 8 << 10}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("pi stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("pi stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("starting pi: %w", err)
	}

	s := &piSession{
		cmd: cmd, cancel: cancel, stdin: stdin, stderr: stderr,
		workdir: opts.WorkDir, model: opts.Model, hints: opts.SystemHints,
		extPath: extPath,
		raw:     make(chan Event, 64), events: make(chan Event),
		stop: make(chan struct{}), readDone: make(chan struct{}),
	}
	go s.forward()
	go s.read(stdout)
	// Pipeline a get_state ahead of whatever turn comes first: its
	// response carries the child's sessionId and the model's context
	// window. A failure here is not fatal — the child is being torn down
	// or already dead, and the first Send's events say so.
	_ = s.write(piCommand{ID: s.nextID(), Type: "get_state"})

	// register under the lock, re-checking closed so a session started
	// concurrently with Close is torn down rather than leaked.
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = s.Close()
		return nil, errors.New("pi agent is closed")
	}
	p.sessions = append(p.sessions, s)
	p.mu.Unlock()
	return s, nil
}

// Close implements Agent: end every live session.
func (p *Pi) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for _, s := range p.sessions {
		_ = s.Close()
	}
	return nil
}

type piSession struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	workdir string   // opts.WorkDir, for repo-relative tool-call details
	model   string   // opts.Model, the Usage.Model fallback when a message omits one
	hints   []string // opts.SystemHints, riding the first turn's prompt
	stdin   io.WriteCloser
	stderr  *capWriter // bounded tail of the child's stderr, for crash diagnostics

	raw      chan Event
	events   chan Event
	stop     chan struct{}
	readDone chan struct{} // closed when read() has finished draining stdout

	wmu       sync.Mutex // serializes writes to stdin
	closeOnce sync.Once
	waitOnce  sync.Once // guards the single cmd.Wait() shared by read() and Close()
	waitErr   error

	mu          sync.Mutex
	seq         int    // command id counter (prompt, abort, get_state)
	inTurn      bool   // a Send is unanswered by agent_settled
	interrupted bool   // the in-flight turn was aborted by our Interrupt (not a failure)
	primed      bool   // system hints injected on the first turn
	sessionID   string // pi's own session id (Identified)
	// extPath is the generated MCP extension file, materialized at
	// NewSession when the session is MCP-bound and removed at Close.
	extPath string
	// hadIdle marks that some prior turn on this session reached a clean
	// idle — RunFailure.FirstTurn on a later failure reads the negation
	// of this.
	hadIdle bool

	// Turn state, owned exclusively by the read goroutine. The engine
	// serializes turns, so at most one is in flight; these need no lock.
	msg      strings.Builder // the turn's assistant text, since the last flush
	ctxLimit int64           // model's context window, from get_state
	// errDiag holds a diagnosed turn failure not yet surfaced: an assistant
	// message that ended stopReason=error, a compaction that failed, a
	// retry loop that exhausted. A later message_start clears it (the
	// retry restarted the request); agent_settled surfaces it instead of
	// a clean idle. Empty means nothing pending.
	errDiag string
}

// markHadIdle records that some turn on this session reached a clean
// idle — RunFailure.FirstTurn on a later failure reads the negation of
// this.
func (s *piSession) markHadIdle() {
	s.mu.Lock()
	s.hadIdle = true
	s.mu.Unlock()
}

func (s *piSession) hadIdleValue() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hadIdle
}

// Pid implements agent.OSProcess: cmd is set once at construction and
// never reassigned, so this needs no lock, same as the claude adapter's.
func (s *piSession) Pid() int {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// SessionID implements Identified: pi's own session id, learned from the
// pipelined get_state (and re-confirmed when resuming). The engine
// persists it and hands it back as SessionOpts.ResumeID after a restart.
func (s *piSession) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

func (s *piSession) Events() <-chan Event { return s.events }

// forward owns events: it copies raw→events and closes events exactly
// once (the read goroutine and Send never touch events directly).
func (s *piSession) forward() {
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

// nextID mints a correlation id for the next command. Called from NewSession
// (before registration, no concurrency) and from Send/Interrupt under mu.
func (s *piSession) nextID() string {
	s.seq++
	return fmt.Sprintf("gummi-%d", s.seq)
}

// piCommand is one stdin line. Only the fields the adapter sends.
type piCommand struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
}

func (s *piSession) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	s.wmu.Lock()
	defer s.wmu.Unlock()
	// The stdin pipe is an *os.File and supports write deadlines; bound the
	// write so a child that stopped reading can't block us indefinitely
	// (same rationale as claude/headless: a wedged child must not hang the
	// engine's pump goroutine, whose budget-stop Interrupt writes here too).
	if f, ok := s.stdin.(interface{ SetWriteDeadline(time.Time) error }); ok {
		_ = f.SetWriteDeadline(time.Now().Add(headlessWriteTimeout))
	}
	_, err = s.stdin.Write(b)
	return err
}

func (s *piSession) Send(_ context.Context, msg string) error {
	s.mu.Lock()
	// pi queues a prompt sent mid-stream only when the client asks
	// (streamingBehavior); gummi serializes turns, so a second Send is
	// always misuse — a refusal, not a failure (ErrBusy keeps the line).
	if s.inTurn {
		s.mu.Unlock()
		return ErrBusy
	}
	// a stale interrupted flag from a race (Interrupt landing after its
	// turn's settled) must not mask the new turn's genuine errors.
	s.interrupted = false
	s.inTurn = true
	id := s.nextID()
	hints := s.primeHints()
	s.mu.Unlock()

	if err := s.write(piCommand{ID: id, Type: "prompt", Message: hints + msg}); err != nil {
		s.mu.Lock()
		s.inTurn = false
		s.mu.Unlock()
		return fmt.Errorf("pi prompt: %w", err)
	}
	return nil
}

// primeHints prepends the stage system hints to the first turn's message:
// pi has a --append-system-prompt flag, but it is a startup flag and the
// adapter spawns before it knows whether a turn is coming, so hints ride
// the first prompt instead (same shape as the opencode adapter's).
func (s *piSession) primeHints() string {
	if s.primed || len(s.hints) == 0 {
		return ""
	}
	s.primed = true
	return strings.Join(s.hints, "\n\n") + "\n\n"
}

// Interrupt aborts the in-flight turn. pi answers abort only once the
// session is idle again, and the settled that follows maps to a clean
// idle via the interrupted flag — never an error, because the engine
// itself interrupts sessions on budget stops. No turn in flight is a
// no-op — setting the flag anyway would swallow the next turn's errors.
func (s *piSession) Interrupt(_ context.Context) error {
	s.mu.Lock()
	if !s.inTurn {
		s.mu.Unlock()
		return nil
	}
	s.interrupted = true
	id := s.nextID()
	s.mu.Unlock()
	return s.write(piCommand{ID: id, Type: "abort"})
}

func (s *piSession) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()    // SIGKILL the process group → stdout EOF → read()'s Scan ends
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
		if s.extPath != "" {
			_ = os.Remove(s.extPath)
		}
	})
	return nil
}

// reap waits for the child exactly once (read() reaps a self-exited child;
// Close reaps a killed one) and caches the exit status.
func (s *piSession) reap() error {
	s.waitOnce.Do(func() { s.waitErr = s.cmd.Wait() })
	return s.waitErr
}

// stopping reports whether Close has begun tearing the session down, so a
// kill-induced stdout EOF isn't misreported as a child crash.
func (s *piSession) stopping() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

// read scans the child's stdout, maps each JSON line to events, and feeds
// the forwarder until stdout closes.
func (s *piSession) read(stdout io.Reader) {
	defer close(s.readDone)
	sc := bufio.NewScanner(stdout)
	// turn/assistant lines embed whole messages; 8 MiB matches the other
	// adapters. pi frames on LF only — bufio splits on \n only and strips
	// an optional trailing \r, exactly the framing pi demands.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		for _, ev := range s.mapLine(line) {
			select {
			case s.raw <- ev:
			case <-s.stop:
				return
			}
		}
	}
	// stdout closed. A clean EOF is NOT an idle: turns end at
	// agent_settled and the process stays alive between them, so EOF while
	// we aren't tearing down means the process died mid-session.
	scanErr := sc.Err()
	if scanErr != nil {
		// scanner aborted (e.g. a line over the buffer cap): the child may
		// still be running, blocked writing to the undrained pipe. Kill the
		// process group first or reap()'s Wait would deadlock against it.
		s.cancel()
	}
	waitErr := s.reap()
	if s.stopping() {
		return
	}
	var final Event
	if scanErr != nil {
		final = Event{Kind: EventError, Err: fmt.Errorf("pi stream aborted: %w", scanErr)}
	} else {
		err := waitErr
		if err == nil {
			err = errors.New("process exited unexpectedly")
		}
		final = Event{Kind: EventError, Err: &RunFailure{
			Backend: "pi", Diagnostic: strings.TrimSpace(s.stderr.String()),
			FirstTurn: !s.hadIdleValue(), Err: err,
		}}
	}
	select {
	case s.raw <- final:
	case <-s.stop:
	}
}

// piLine is one stdout line's envelope; only the fields the adapter reads.
// Responses carry command/success/error/data; events carry their own
// shapes. The two are told apart by Type.
type piLine struct {
	Type string `json:"type"`
	// response fields
	ID      string          `json:"id"`
	Command string          `json:"command"`
	Success *bool           `json:"success"` // pointer: a failed response may be success:false
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
	// message_update
	AssistantMessageEvent struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
	// message_start / message_end
	Message json.RawMessage `json:"message"`
	// tool execution
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       map[string]any  `json:"args"`
	Result     json.RawMessage `json:"result"`
	IsError    bool            `json:"isError"`
	// compaction_end / auto_retry_end
	ErrorMessage string `json:"errorMessage"`
	FinalError   string `json:"finalError"`
	// extension_ui_request
	Method string `json:"method"`
}

// piMessage is the message payload of message_start / message_end.
type piMessage struct {
	Role         string        `json:"role"`
	Model        string        `json:"model"`
	StopReason   string        `json:"stopReason"`
	ErrorMessage string        `json:"errorMessage"`
	Content      []piTextBlock `json:"content"`
	Usage        *piUsage      `json:"usage"`
}

type piTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// piUsage is one assistant message's usage. pi splits the input side the
// way gummi's Usage expects it re-split: input is the fresh (uncached)
// part, cacheRead/cacheWrite are reported separately, and totalTokens —
// and pi's cost — cover all four.
type piUsage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Cost       struct {
		Total float64 `json:"total"`
	} `json:"cost"`
}

// piStateData is the get_state response payload; only what the adapter
// keeps (the session id and the model's context window).
type piStateData struct {
	SessionID string `json:"sessionId"`
	Model     *struct {
		ContextWindow int64 `json:"contextWindow"`
	} `json:"model"`
}

// piToolResult is the result payload of tool_execution_end; the adapter
// surfaces its first text block as the tool line's outcome.
type piToolResult struct {
	Content []piTextBlock `json:"content"`
}

// mapLine converts one stdout line into zero or more gummi Events and
// advances the turn state. It runs only on the read goroutine, so the
// turn-state fields need no lock. Unknown types are dropped, not errored —
// the protocol is unversioned and drifts; non-JSON lines are ignored
// quietly for the same reason.
func (s *piSession) mapLine(line []byte) []Event {
	var l piLine
	if err := json.Unmarshal(line, &l); err != nil {
		return nil
	}
	switch l.Type {
	case "response":
		return s.mapResponse(&l)
	case "message_update":
		return s.mapUpdate(&l)
	case "message_end":
		return s.mapMessageEnd(&l)
	case "tool_execution_start":
		return []Event{{Kind: EventToolCall, Tool: l.ToolName, Detail: toolDetail(s.workdir, l.Args)}}
	case "tool_execution_end":
		return s.mapToolEnd(&l)
	case "compaction_end":
		if l.ErrorMessage != "" {
			s.errDiag = l.ErrorMessage
		}
		return nil
	case "auto_retry_end":
		// a retry that ends without success is the retry loop's final
		// word — agent_settled is right behind it and will surface
		// errDiag in place of the clean idle.
		if !l.success() && l.FinalError != "" {
			s.errDiag = l.FinalError
		}
		return nil
	case "message_start":
		// a new attempt began (a retry after the previous one errored):
		// the pending diagnosis is stale, the request is starting over.
		s.errDiag = ""
		return nil
	case "agent_settled":
		return s.mapSettled()
	case "extension_ui_request":
		// An extension asked a question gummi cannot answer (RPC mode has
		// no human at the keyboard). Decline, so the extension's default
		// resolution applies instead of the call blocking forever.
		_ = s.write(map[string]any{"type": "extension_ui_response", "id": l.ID, "cancelled": true})
		return nil
	default:
		// turn_start/turn_end (metering is at message_end), agent_start/
		// agent_end (settled is the only trustworthy idle), queue_update,
		// bash_execution_update (we never send direct bash commands),
		// retry/summarization scheduling notices, and whatever pi grows.
		return nil
	}
}

// success reads the response's success field; absent (a malformed
// response) reads as failure.
func (l *piLine) success() bool { return l.Success != nil && *l.Success }

func (s *piSession) mapResponse(l *piLine) []Event {
	if l.Command == "get_state" && l.success() {
		var d piStateData
		if err := json.Unmarshal(l.Data, &d); err == nil {
			s.mu.Lock()
			if s.sessionID == "" {
				s.sessionID = d.SessionID
			}
			s.mu.Unlock()
			if d.Model != nil {
				s.ctxLimit = d.Model.ContextWindow
			}
		}
		return nil
	}
	// A rejected command while a turn is in flight means the turn never
	// started — no events follow, no settled will come; fail the turn
	// now (opencode's zero-event shape, with pi's own diagnosis).
	if !l.success() && s.inTurnLocked() {
		diag := cmp.Or(l.Error, "command rejected")
		s.endTurn()
		return []Event{{Kind: EventError, Err: &RunFailure{
			Backend: "pi", Diagnostic: boundTail(diag, true),
			FirstTurn: !s.hadIdleValue(), Err: errors.New("pi rejected the turn"),
		}}}
	}
	return nil
}

// mapUpdate handles streaming deltas. Text accumulates into msg (for the
// flush points) and streams as deltas; thinking streams through as
// display-only reasoning. A toolcall_start says the assistant is about to
// invoke a tool — the accumulated prose must land as its own message
// bubble BEFORE the tool line, or the turn's text would duplicate every
// pre-tool segment (the flush-before-tool shape opencode implements by
// hand). The call itself is not surfaced here: tool_execution_start owns
// that line.
func (s *piSession) mapUpdate(l *piLine) []Event {
	switch l.AssistantMessageEvent.Type {
	case "text_delta":
		if l.AssistantMessageEvent.Delta == "" {
			return nil
		}
		s.msg.WriteString(l.AssistantMessageEvent.Delta)
		return []Event{{Kind: EventTextDelta, Text: l.AssistantMessageEvent.Delta}}
	case "thinking_delta":
		if l.AssistantMessageEvent.Delta == "" {
			return nil
		}
		return []Event{{Kind: EventReasoningDelta, Text: l.AssistantMessageEvent.Delta}}
	case "toolcall_start":
		return s.flushMsg()
	default:
		return nil // text/thinking/toolcall start/end bookends, arg chunks
	}
}

// flushMsg emits the accumulated text as a finalized message bubble and
// resets the accumulator.
func (s *piSession) flushMsg() []Event {
	text := strings.TrimSpace(s.msg.String())
	s.msg.Reset()
	if text == "" {
		return nil
	}
	return []Event{{Kind: EventMessage, Text: text}}
}

// mapMessageEnd surfaces a completed assistant message: its text (the
// accumulated deltas — message_end's snapshot is authoritative, and equal),
// its usage as the metering point (one LLM call per assistant message, so
// per-message usage IS per-call — no delta arithmetic), and the context
// window the message left the session in.
func (s *piSession) mapMessageEnd(l *piLine) []Event {
	var m piMessage
	if err := json.Unmarshal(l.Message, &m); err != nil {
		return nil
	}
	if m.Role != "assistant" {
		return nil // user echoes, toolResult messages
	}
	out := s.flushMsg()
	if m.StopReason == "error" {
		// The request died — but pi retries transient failures
		// transparently (message_start clears errDiag when the retry
		// restarts), so this is a diagnosis in waiting, not a verdict.
		s.errDiag = cmp.Or(m.ErrorMessage, "assistant turn errored (stopReason=error)")
		return out
	}
	if m.Usage != nil {
		u := Usage{
			InputTokens:  m.Usage.Input + m.Usage.CacheWrite,
			CachedTokens: m.Usage.CacheRead,
			OutputTokens: m.Usage.Output,
			Model:        cmp.Or(m.Model, s.model),
			// pi's cost is USD; gummi credits are $0.01 units. Metered:
			// authoritative even at zero — the engine records it as-is.
			Credits: m.Usage.Cost.Total * 100,
			Metered: true,
		}
		if u.Credits != 0 || u.InputTokens != 0 || u.OutputTokens != 0 {
			out = append(out, Event{Kind: EventUsage, Usage: u})
		}
		// the call's input side approximates the context window right now
		if t := m.Usage.Input + m.Usage.CacheRead + m.Usage.CacheWrite; t > 0 {
			out = append(out, Event{Kind: EventContext, Context: Context{Tokens: t, Limit: s.ctxLimit}})
		}
	}
	return out
}

func (s *piSession) mapToolEnd(l *piLine) []Event {
	var r piToolResult
	if err := json.Unmarshal(l.Result, &r); err != nil {
		return nil
	}
	var text string
	for _, b := range r.Content {
		if b.Type == "text" {
			text = b.Text
			break
		}
	}
	return []Event{{Kind: EventToolResult, CallID: l.ToolCallID, Result: &ToolResult{
		OK: !l.IsError, Output: boundTail(text, !l.IsError),
	}}}
}

// mapSettled terminates the turn. A settled arrives only once nothing
// automatic remains — retry, compaction retry, queued continuations — so
// it is the one trustworthy idle. A turn with a pending diagnosis fails
// there (no idle: opencode's shape, where a failed turn ends in error);
// an interrupted turn ends idle, because the engine itself interrupts
// sessions on budget stops and an EventError would downgrade that clean
// stop to a failed run.
func (s *piSession) mapSettled() []Event {
	if !s.inTurnLocked() {
		return nil // a stray settled between turns says nothing new
	}
	out := s.flushMsg()
	diag := s.errDiag
	interrupted := s.interrupted
	s.endTurn()
	if diag != "" && !interrupted {
		return append(out, Event{Kind: EventError, Err: &RunFailure{
			Backend: "pi", Diagnostic: boundTail(diag, true),
			FirstTurn: !s.hadIdleValue(), Err: errors.New("pi run failed"),
		}})
	}
	s.markHadIdle()
	return append(out, Event{Kind: EventIdle})
}

// inTurnLocked reports whether a turn is in flight; the few mu-guarded
// reads mapLine needs. Everything else it touches is read-goroutine-only.
func (s *piSession) inTurnLocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inTurn
}

// endTurn clears the turn flags after a settled or a rejected prompt.
func (s *piSession) endTurn() {
	s.mu.Lock()
	s.inTurn = false
	s.interrupted = false
	s.mu.Unlock()
}

// piResumable reports whether pi still holds the session named by id.
//
// The check is a file scan, not a probe, because the cheap ways to ask pi
// directly cost a turn: --session with an unknown id fails the spawn or
// the first prompt (a resume must never be the reason a stage fails). pi
// lays sessions out as <agentDir>/sessions/<cwd-slug>/<timestamp>_<id>.jsonl,
// but the slug is a guess about layout that a suffix match makes
// unnecessary — anything ending in the id works, at any depth. Fails
// CLOSED: anything it cannot confirm means no --session, and the session
// opens exactly as it did before.
func piResumable(id string) bool {
	if id == "" {
		return false
	}
	dir := piSessionsDir()
	if dir == "" {
		return false
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*", "*_"+id+".jsonl"))
	if len(matches) > 0 {
		return true
	}
	matches, _ = filepath.Glob(filepath.Join(dir, "*_"+id+".jsonl"))
	return len(matches) > 0
}

// piSessionsDir is where pi keeps its session transcripts:
// $PI_CODING_AGENT_DIR/sessions, or ~/.pi/agent/sessions.
func piSessionsDir() string {
	base := os.Getenv("PI_CODING_AGENT_DIR")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".pi", "agent")
	}
	return filepath.Join(base, "sessions")
}
