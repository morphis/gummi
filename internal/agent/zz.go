package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// zzExecPath locates gummi's own executable when materializing the
// per-turn `--mcp "<exe> __mcp --feature <FID>"` (or, for a workspace
// session, `--mcp "<exe> __mcp --workspace"`) value, so zz's MCP child
// is a real `gummi __mcp` process rather than whatever shadows "gummi" on
// $PATH. Production uses the real os.Executable; tests rebind it.
var zzExecPath = os.Executable

// zzArgvPromptMaxBytes bounds the prompt Send will pass as a positional
// argv string. Linux caps a single argv string at MAX_ARG_STRLEN (128
// KiB); 96 KiB leaves headroom for the rest of argv so a too-long prompt
// fails deterministically here instead of a bare E2BIG from exec.
const zzArgvPromptMaxBytes = 96 * 1024

// zzCreditRateEnv is the operator's escape hatch for pricing zz's token
// spend into credits: zz reports no cost of its own (it drives an
// arbitrary OpenAI-compatible endpoint), so absent an override the
// engine's token-priced fallback applies.
const zzCreditRateEnv = "GUMMI_ZZ_CREDITS_PER_1K" //nolint:gosec // an env var name, not a credential

// zzMaxTurnsEnv overrides zz's per-session turn cap. gummi's real spend
// limiter is the credit envelope, not a turn count; this cap only exists
// as a runaway-loop backstop, so the override is lenient by design (see
// zzMaxTurns).
const zzMaxTurnsEnv = "GUMMI_ZZ_MAX_TURNS"

// zzMaxTurnsDefault is passed on every zz invocation unless overridden.
// zz's own default is 50, which a long implement stage can exceed; 200
// is well above a normal stage's turn count while still catching a
// genuine runaway loop.
const zzMaxTurnsDefault = 200

// zzMaxTurns reads the operator's turn-cap override. Absent, unparseable,
// zero or negative all fall back to zzMaxTurnsDefault rather than
// erroring — an operator typo must not wedge every zz session, the same
// lenient shape as CreditRate's GUMMI_ZZ_CREDITS_PER_1K parse.
func zzMaxTurns() int {
	v := strings.TrimSpace(os.Getenv(zzMaxTurnsEnv))
	if v == "" {
		return zzMaxTurnsDefault
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return zzMaxTurnsDefault
	}
	return n
}

// ZZ drives the zz CLI, a small Rust coding agent that fronts any
// OpenAI-compatible endpoint. `zz -p ask "<prompt>"` is strictly one
// process per turn — there is no stdin form and no long-lived
// interactive mode compatible with `-p` — so this adapter has the same
// process-per-turn shape as codex.go; only the wire vocabulary (JSONL
// event types) and the resume mechanism (a `--session` transcript file
// instead of a thread id) differ.
type ZZ struct {
	bin      string
	mu       sync.Mutex
	sessions []*zzSession
	closed   bool
}

// NewZZ returns an Agent that drives the zz binary (default "zz", found
// on PATH). It fails fast when the binary is missing.
func NewZZ(bin string) (*ZZ, error) {
	if bin == "" {
		bin = "zz"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("zz binary %q not found: %w", bin, err)
	}
	return &ZZ{bin: resolved}, nil
}

func (z *ZZ) Name() string { return "zz" }

// Capabilities implements Agent. ReadOnlyEnforce is false: zz has no flag
// to disable its write/edit/bash tools, so a ReadOnly session is refused
// in NewSession rather than silently run read-write. ClientTools is
// false: zz's tools reach gummi over MCP (MCPTools), not SessionOpts.Tools.
func (z *ZZ) Capabilities() Capabilities {
	return Capabilities{Resume: true, UsageEvents: true, Interrupt: true, MCPTools: true}
}

// CreditRate implements Agent. Reads the env-configured rate (credits per
// 1k tokens); zz reports no cost, so absent an override the engine's
// default token-priced fallback applies.
func (z *ZZ) CreditRate(string) float64 {
	v := strings.TrimSpace(os.Getenv(zzCreditRateEnv))
	if v == "" {
		return 0
	}
	r, err := strconv.ParseFloat(v, 64)
	if err != nil || r < 0 {
		return 0
	}
	return r
}

// NewSession implements Agent. zz has no read-only mode, no approval
// callback for guarded permissions, and splits --mcp on whitespace with
// no quoting — each of those is refused here, at session start, with a
// named actionable error, rather than failing deep inside the first turn.
func (z *ZZ) NewSession(_ context.Context, opts SessionOpts) (Session, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.closed {
		return nil, errors.New("zz agent is closed")
	}
	if opts.ReadOnly {
		return nil, errors.New("zz backend cannot enforce a read-only research session; " +
			"point this role at `claude` or `opencode`, or accept that autonomous research cannot run on zz")
	}
	if opts.Permission == PermissionGuarded {
		return nil, errors.New("zz adapter: guarded permissions are not supported (zz has no approval callback, " +
			"and running allow-all under a guarded config would be a silent policy drop); " +
			"set permissions: allow-all for this role, or use a guarded-capable backend (claude, copilot)")
	}
	if opts.Model == "" {
		return nil, errors.New("zz requires a model")
	}
	// zz splits --mcp on whitespace with no quoting, so a gummi executable
	// path containing whitespace cannot be represented on that flag.
	var mcpLabel string
	if opts.MCPSockPath != "" && (opts.FeatureID != "" || opts.Workspace) {
		exe, err := zzExecPath()
		if err != nil {
			return nil, fmt.Errorf("zz adapter: locating own executable: %w", err)
		}
		if strings.ContainsAny(exe, " \t\n\r") {
			return nil, fmt.Errorf("zz adapter: gummi executable path %q contains whitespace; "+
				"zz splits --mcp on whitespace with no quoting, so this session cannot register MCP tools; "+
				"move the gummi binary to a path without spaces, or accept this role runs without MCP", exe)
		}
		// Stashed now (rather than recomputed from zzExecPath in mapLine) so
		// a mid-session rebind of zzExecPath cannot cause the expected MCP
		// label to drift from the one buildArgs actually launched.
		mcpLabel = filepath.Base(exe)
	}
	// The transcript path is allocated once and stays stable for the
	// session's lifetime; without it turn 2 would forget turn 1 entirely.
	// A caller-supplied ResumePath is a durable path the engine derived
	// (survives a gummi restart); otherwise fall back to an agent-owned
	// temp root that Close removes.
	var sessionPath, tempRoot string
	if opts.ResumePath != "" {
		sessionPath = opts.ResumePath
		if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
			return nil, fmt.Errorf("zz session at %s: %w", sessionPath, err)
		}
	} else {
		dir, err := os.MkdirTemp("", "gummi-zz-session-*")
		if err != nil {
			return nil, fmt.Errorf("zz adapter: allocating session dir: %w", err)
		}
		sessionPath, tempRoot = filepath.Join(dir, "session.json"), dir
	}
	s := &zzSession{
		z: z, workdir: opts.WorkDir, model: opts.Model, provider: opts.Provider, think: opts.Think, hints: opts.SystemHints,
		featureID: opts.FeatureID, mcpSock: opts.MCPSockPath, mcpLabel: mcpLabel, workspace: opts.Workspace,
		sessionPath: sessionPath, tempRoot: tempRoot, maxTurns: zzMaxTurns(),
		// zz's cage is a single --cwd root with no per-file allowlist; a
		// transient session with ExtraReadAllows must read outside
		// WorkDir, so it runs without --cwd rather than being denied.
		cwdSuppressed: len(opts.ExtraReadAllows) > 0,
		raw:           make(chan Event, 32), events: make(chan Event), stop: make(chan struct{}),
	}
	go s.forward()
	z.sessions = append(z.sessions, s)
	return s, nil
}

func (z *ZZ) Close() error {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.closed = true
	for _, s := range z.sessions {
		_ = s.Close()
	}
	return nil
}

type zzSession struct {
	z                  *ZZ
	workdir, model     string
	provider           string
	think              string
	maxTurns           int // frozen at NewSession from zzMaxTurns(); argv and the max_turns error both read this, never the env directly
	featureID, mcpSock string
	workspace          bool   // opts.Workspace: bind --mcp's __mcp child to --workspace instead of --feature
	mcpLabel           string // filepath.Base of the resolved gummi exe; "" when the session was not started with --mcp
	hints              []string
	sessionPath        string // --session value; stable for the session's lifetime
	tempRoot           string // agent-owned os.MkdirTemp root holding sessionPath; removed on Close
	cwdSuppressed      bool   // true when opts.ExtraReadAllows was non-empty at NewSession

	raw    chan Event
	events chan Event
	stop   chan struct{}

	mu                          sync.Mutex
	sessionID                   string
	lastContextBudget           int64 // most recent context_warning.budget seen; 0 when none seen
	cancel                      context.CancelFunc
	primed, interrupted, closed bool
	closeOnce                   sync.Once

	// syntheticCallSeq is a monotonic per-session counter for CallIDs the
	// adapter itself generates (waiting/compaction), which zz's stream
	// never assigns an id of its own. Touched only via atomic.AddUint64.
	syntheticCallSeq uint64

	// accum buffers text deltas for the turn in flight, flushed as one
	// EventMessage at turn_end. Touched only by readTurn: Send resets it
	// before spawning the goroutine that owns it for the rest of the turn,
	// and only one turn runs at a time (Send refuses while s.cancel != nil).
	accum strings.Builder
}

func (s *zzSession) Events() <-chan Event { return s.events }
func (s *zzSession) SessionID() string    { s.mu.Lock(); defer s.mu.Unlock(); return s.sessionID }

func (s *zzSession) forward() {
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

// Send spawns zz once for this turn, in its own process group with its
// prompt as a positional argv string (zz's `-p ask` mode has no stdin
// form). The goroutine readTurn scans stdout, maps lines to events, reaps
// the child, and emits the terminal event.
func (s *zzSession) Send(_ context.Context, msg string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("session closed")
	}
	if s.cancel != nil {
		s.mu.Unlock()
		return errors.New("a turn is already in progress")
	}
	prompt := msg
	// Never pass --system-prompt: it REPLACES zz's built-in prompt (and
	// its tool instructions), which breaks tool use. Hints are prepended
	// to the first turn's message instead.
	if !s.primed && len(s.hints) > 0 {
		prompt = strings.Join(s.hints, "\n\n") + "\n\n" + msg
	}
	if len(prompt) > zzArgvPromptMaxBytes {
		s.mu.Unlock()
		return fmt.Errorf("zz prompt exceeds argv limit (%d > %d bytes); Linux caps a single argv "+
			"string at 128 KiB, shorten the turn or split it into multiple messages", len(prompt), zzArgvPromptMaxBytes)
	}
	args, err := s.buildArgs()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	args = append(args, prompt)
	s.primed = true
	s.accum.Reset()
	// No env scrubbing: zz has no session-marker vars to launder. Inherit
	// os.Environ() unchanged, plus the MCP socket path when --mcp is
	// emitted (zz's --mcp has no env table of its own).
	env := os.Environ()
	if s.mcpSock != "" && (s.featureID != "" || s.workspace) {
		env = append(env, "GUMMI_MCP_SOCK="+s.mcpSock)
	}
	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, s.z.bin, args...) //nolint:gosec // executable is operator-selected; argv is adapter-built
	cmd.Dir, cmd.Env = s.workdir, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		s.mu.Unlock()
		return fmt.Errorf("zz stdout: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		s.mu.Unlock()
		return fmt.Errorf("starting zz: %w", err)
	}
	s.cancel = cancel
	s.mu.Unlock()
	go s.readTurn(cmd, stdout, &stderr, cancel)
	return nil
}

// buildArgs assembles the argv for one `zz ask` invocation, everything up
// to (but not including) the trailing prompt argument Send appends.
func (s *zzSession) buildArgs() ([]string, error) {
	args := []string{"-p", "--model", s.model}
	if s.provider != "" {
		args = append(args, "--provider", s.provider)
	}
	if s.think != "" {
		args = append(args, "--think", s.think)
	}
	args = append(args, "--session", s.sessionPath)
	if s.primed || fileExists(s.sessionPath) {
		args = append(args, "--continue")
	}
	if s.mcpSock != "" && (s.featureID != "" || s.workspace) {
		exe, err := zzExecPath()
		if err != nil {
			return nil, fmt.Errorf("zz adapter: locating own executable: %w", err)
		}
		mcpArg := exe + " __mcp --feature " + s.featureID
		if s.workspace {
			mcpArg = exe + " __mcp --workspace"
		}
		args = append(args, "--mcp", mcpArg)
	}
	if !s.cwdSuppressed {
		args = append(args, "--cwd", s.workdir)
	}
	args = append(args, "--max-turns", strconv.Itoa(s.maxTurns))
	args = append(args, "ask")
	return args, nil
}

// fileExists reports whether path names an existing file. A stat error
// other than "not found" (e.g. permission denied) is treated as existing
// so it cannot silently drop --continue.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	return !errors.Is(err, fs.ErrNotExist)
}

func (s *zzSession) readTurn(cmd *exec.Cmd, stdout io.Reader, stderr fmt.Stringer, cancel context.CancelFunc) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	terminal := false
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		events, term, err := s.mapLine(sc.Bytes())
		if err != nil {
			cancel()
			_ = cmd.Wait()
			s.finishTurn(cancel)
			s.emit(Event{Kind: EventError, Err: err})
			return
		}
		terminal = terminal || term
		for _, ev := range events {
			// Idle is published only after the subprocess is reaped and
			// the in-flight marker is cleared, so a caller may
			// immediately Send again.
			if ev.Kind != EventIdle {
				s.emit(ev)
			}
		}
	}
	scanErr := sc.Err()
	if scanErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	cancel()
	s.mu.Lock()
	s.cancel = nil
	closed, aborted := s.closed, s.interrupted
	s.interrupted = false
	s.mu.Unlock()
	if closed {
		return
	}
	if aborted {
		s.emit(Event{Kind: EventIdle})
		return
	}
	if scanErr != nil {
		s.emit(Event{Kind: EventError, Err: fmt.Errorf("zz stream aborted: %w", scanErr)})
		return
	}
	if waitErr != nil {
		s.emit(Event{Kind: EventError, Err: fmt.Errorf("zz exited: %s", diagnostic(stderr.String(), waitErr.Error()))})
		return
	}
	if !terminal {
		s.emit(Event{Kind: EventError, Err: fmt.Errorf("zz exited without a terminal done event: %s", diagnostic(stderr.String(), "no diagnostics"))})
		return
	}
	s.emit(Event{Kind: EventIdle})
}

// nextSyntheticCall allocates a CallID for an adapter-synthesized call+result
// pair (waiting, compaction) that zz's own stream carries no id for. The
// "zz-<kind>-" prefix marks these as adapter-synthesized to a future
// transcript reader; zz's own tool_call ids are opaque strings, so there is
// no collision risk.
func (s *zzSession) nextSyntheticCall(kind string) string {
	n := atomic.AddUint64(&s.syntheticCallSeq, 1)
	return fmt.Sprintf("zz-%s-%d", kind, n)
}

// zzToolName composes the ticker-facing tool name from zz's tool_call /
// tool_result source field, mirroring codex.go's mcp_tool_call convention:
// "builtin" (or an absent/empty source) is the bare name; "mcp:<label>" is
// dotted with the label.
func zzToolName(source, name string) string {
	label, ok := strings.CutPrefix(source, "mcp:")
	if !ok {
		return name
	}
	return strings.Trim(strings.Join([]string{label, name}, "."), ".")
}

func (s *zzSession) finishTurn(cancel context.CancelFunc) {
	cancel()
	s.mu.Lock()
	s.cancel = nil
	s.mu.Unlock()
}

func (s *zzSession) emit(e Event) {
	select {
	case s.raw <- e:
	case <-s.stop:
	}
}

// zzUsage is zz's raw OpenAI-compatible turn_end.usage object.
type zzUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// zzDerived is the turn_end.derived fallback used when usage is null.
type zzDerived struct {
	ActualPromptTokens int64 `json:"actual_prompt_tokens"`
	CompletionTokens   int64 `json:"completion_tokens"`
	CachedTokens       int64 `json:"cached_tokens"`
}

// mapLine converts one zz JSONL stream line (v:6) into zero or more gummi
// Events. terminal is true only for the `done` line — turn_end is not
// terminal, it just flushes the turn's usage and buffered text.
func (s *zzSession) mapLine(line []byte) ([]Event, bool, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, false, fmt.Errorf("malformed zz JSONL: %w", err)
	}
	var typ string
	_ = json.Unmarshal(raw["type"], &typ)
	switch typ {
	case "session":
		var id string
		_ = json.Unmarshal(raw["id"], &id)
		if id != "" {
			s.mu.Lock()
			s.sessionID = id
			s.mu.Unlock()
		}
		// MCP registration is asserted only for a session configured with
		// --mcp (the same condition buildArgs uses to emit the flag); a
		// builtin-only roster on a non-MCP session is expected and silent.
		if s.mcpSock != "" && (s.featureID != "" || s.workspace) {
			var tools []struct {
				Name   string `json:"name"`
				Source string `json:"source"`
			}
			_ = json.Unmarshal(raw["tools"], &tools)
			want := "mcp:" + s.mcpLabel
			registered := false
			for _, t := range tools {
				if t.Source == want {
					registered = true
					break
				}
			}
			if !registered {
				return nil, false, fmt.Errorf("gummi's MCP server did not register with zz (no tool advertised source %q); "+
					"ask_user and gummi's other tools are unavailable this session; "+
					"the __mcp child may have failed to start, or the socket path was unreachable", want)
			}
		}
		return nil, false, nil
	case "text":
		var delta string
		_ = json.Unmarshal(raw["delta"], &delta)
		if delta == "" {
			return nil, false, nil
		}
		s.accum.WriteString(delta)
		return []Event{{Kind: EventTextDelta, Text: delta}}, false, nil
	case "reasoning":
		var delta string
		_ = json.Unmarshal(raw["delta"], &delta)
		if delta == "" {
			return nil, false, nil
		}
		return []Event{{Kind: EventReasoningDelta, Text: delta}}, false, nil
	case "tool_call":
		var c struct {
			ID     string          `json:"id"`
			Name   string          `json:"name"`
			Source string          `json:"source"`
			Args   json.RawMessage `json:"args"`
		}
		if err := json.Unmarshal(line, &c); err != nil {
			return nil, false, fmt.Errorf("malformed zz tool_call: %w", err)
		}
		var detail string
		var argsMap map[string]any
		var argsStr string
		if err := json.Unmarshal(c.Args, &argsMap); err == nil {
			detail = toolDetail(s.workdir, argsMap)
		} else if err := json.Unmarshal(c.Args, &argsStr); err == nil {
			detail = collapseDetail(s.workdir, argsStr)
		}
		return []Event{{Kind: EventToolCall, Tool: zzToolName(c.Source, c.Name), CallID: c.ID, Detail: detail}}, false, nil
	case "tool_result":
		var r struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Source  string `json:"source"`
			OK      bool   `json:"ok"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, false, fmt.Errorf("malformed zz tool_result: %w", err)
		}
		return []Event{{Kind: EventToolResult, Tool: zzToolName(r.Source, r.Name), CallID: r.ID, Result: &ToolResult{OK: r.OK, Output: boundTail(r.Content, r.OK)}}}, false, nil
	case "turn_end":
		return s.mapTurnEnd(raw), false, nil
	case "context_warning":
		var c struct {
			EstTokens int64 `json:"est_tokens"`
			Budget    int64 `json:"budget"`
		}
		if err := json.Unmarshal(line, &c); err != nil {
			return nil, false, fmt.Errorf("malformed zz context_warning: %w", err)
		}
		s.mu.Lock()
		s.lastContextBudget = c.Budget
		s.mu.Unlock()
		return []Event{{Kind: EventContext, Context: Context{Tokens: c.EstTokens, Limit: c.Budget}}}, false, nil
	case "waiting":
		var w struct {
			Status  int   `json:"status"`
			Attempt int   `json:"attempt"`
			DelayMS int64 `json:"delay_ms"`
		}
		if err := json.Unmarshal(line, &w); err != nil {
			return nil, false, fmt.Errorf("malformed zz waiting: %w", err)
		}
		id := s.nextSyntheticCall("waiting")
		detail := fmt.Sprintf("http %d · attempt %d · retry in %.1fs", w.Status, w.Attempt, float64(w.DelayMS)/1000)
		return []Event{
			{Kind: EventToolCall, Tool: "waiting", CallID: id, Detail: detail},
			{Kind: EventToolResult, Tool: "waiting", CallID: id, Result: &ToolResult{OK: true}},
		}, false, nil
	case "compaction":
		var c struct {
			MessagesFolded  int64 `json:"messages_folded"`
			EstTokensBefore int64 `json:"est_tokens_before"`
			EstTokensAfter  int64 `json:"est_tokens_after"`
		}
		if err := json.Unmarshal(line, &c); err != nil {
			return nil, false, fmt.Errorf("malformed zz compaction: %w", err)
		}
		id := s.nextSyntheticCall("compaction")
		detail := collapseDetail(s.workdir, fmt.Sprintf("folded %d messages · ~%d -> ~%d tokens", c.MessagesFolded, c.EstTokensBefore, c.EstTokensAfter))
		s.mu.Lock()
		limit := s.lastContextBudget
		s.mu.Unlock()
		return []Event{
			{Kind: EventToolCall, Tool: "compaction", CallID: id, Detail: detail},
			{Kind: EventToolResult, Tool: "compaction", CallID: id, Result: &ToolResult{OK: true}},
			{Kind: EventContext, Context: Context{Tokens: c.EstTokensAfter, Limit: limit}},
		}, false, nil
	case "error":
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(line, &e)
		msg := e.Message
		if msg == "" {
			msg = "unknown error"
		}
		return []Event{{Kind: EventError, Err: errors.New(msg)}}, false, nil
	case "done":
		var d struct {
			StopReason string `json:"stop_reason"`
		}
		_ = json.Unmarshal(line, &d)
		if d.StopReason == "end_turn" {
			return []Event{{Kind: EventIdle}}, true, nil
		}
		if d.StopReason == "max_turns" {
			return nil, true, fmt.Errorf("zz turn ended: hit the %d-turn cap (raise it with %s)", s.maxTurns, zzMaxTurnsEnv)
		}
		return nil, true, fmt.Errorf("zz turn ended: %s", d.StopReason)
	case "thinking", "turn_start", "user":
		return nil, false, nil
	}
	return nil, false, nil
}

// mapTurnEnd splits turn_end.usage (falling back to turn_end.derived when
// usage is null) per the agent.Usage contract, then flushes the turn's
// buffered text as one EventMessage: zz emits assistant prose only as
// `text` deltas, and engine.Session.finishAssistant closes the streaming
// transcript bubble on EventMessage while engine.assistantText.message
// resets its delta tail, so this does not double-count.
func (s *zzSession) mapTurnEnd(raw map[string]json.RawMessage) []Event {
	var input, cached, output int64
	if u := raw["usage"]; len(u) > 0 && string(u) != "null" {
		var parsed zzUsage
		if err := json.Unmarshal(u, &parsed); err == nil {
			input = parsed.PromptTokens - parsed.PromptTokensDetails.CachedTokens
			cached = parsed.PromptTokensDetails.CachedTokens
			output = parsed.CompletionTokens
		}
	} else if d := raw["derived"]; len(d) > 0 && string(d) != "null" {
		var parsed zzDerived
		if err := json.Unmarshal(d, &parsed); err == nil {
			input = parsed.ActualPromptTokens - parsed.CachedTokens
			cached = parsed.CachedTokens
			output = parsed.CompletionTokens
		}
	}
	if input < 0 {
		input = 0
	}
	out := []Event{{Kind: EventUsage, Usage: Usage{Model: s.model, InputTokens: input, CachedTokens: cached, OutputTokens: output}}}
	if s.accum.Len() > 0 {
		out = append(out, Event{Kind: EventMessage, Text: s.accum.String()})
		s.accum.Reset()
	}
	return out
}

func (s *zzSession) Interrupt(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.interrupted = true
		s.cancel()
	}
	return nil
}

func (s *zzSession) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		if s.cancel != nil {
			s.cancel()
		}
		s.mu.Unlock()
		close(s.stop)
		if s.tempRoot != "" {
			_ = os.RemoveAll(s.tempRoot)
		}
	})
	return nil
}
