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
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ClaudeCode is an Agent backed by the Claude Code CLI in its long-lived
// bidirectional stream-json mode:
//
//	claude -p --input-format stream-json --output-format stream-json \
//	  --verbose --include-partial-messages --permission-mode bypassPermissions
//
// One process per session (cwd = the feature's worktree), user turns
// written as JSON lines on stdin, activity read as JSON lines on stdout —
// the same wire protocol the official TS/Python Agent SDKs speak. A turn
// ends at a "result" line and the process then waits for the next stdin
// turn, so the lifecycle is the headless shape (long-lived stdio child),
// not opencode's process-per-turn. Protocol facts verified against CLI
// 2.1.204 (proposal §2/§6): the CLI emits nothing until the first user
// frame, then opens every turn with a system/init line; "assistant" lines
// arrive one per content block with a stub usage repeated on each, so
// per-request metering reads the message_delta stream event instead.
//
// P1 scope: allow-all only (guarded needs the control-protocol approval
// path, P3) and no client tools (MCP, P2).
type ClaudeCode struct {
	bin string

	mu       sync.Mutex
	sessions []*claudeSession
	closed   bool
}

// NewClaudeCode returns an Agent that drives the claude binary (default
// "claude", found on PATH). It fails fast when the binary is missing.
func NewClaudeCode(bin string) (*ClaudeCode, error) {
	if bin == "" {
		bin = "claude"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("claude binary %q not found: %w", bin, err)
	}
	return &ClaudeCode{bin: resolved}, nil
}

// Name implements Agent.
func (c *ClaudeCode) Name() string { return "claude" }

// Capabilities implements Agent. Resume here is continuity across turns,
// inherent in the long-lived process (the same bar opencode meets; the
// CLI's --resume also survives restarts, unused until the engine needs
// it). Interrupt is the control protocol's interrupt request. BYOK is off:
// Claude Code routes only to Anthropic-shaped endpoints via its own env.
// ClientTools arrives with the MCP bridge (P2).
func (c *ClaudeCode) Capabilities() Capabilities {
	return Capabilities{Resume: true, UsageEvents: true, Interrupt: true}
}

// NewSession implements Agent: spawn one claude process in opts.WorkDir.
func (c *ClaudeCode) NewSession(_ context.Context, opts SessionOpts) (Session, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, errors.New("claude agent is closed")
	}
	// Guarded mode needs the control-protocol approval path (P3). Until it
	// ships, fail loudly rather than downgrade silently: bypassPermissions
	// would un-guard the session, and the CLI's default mode auto-denies
	// every permission-needing tool with no visible prompt (proposal §2).
	if opts.Permission == PermissionGuarded {
		return nil, errors.New("claude adapter: guarded permissions are not supported yet " +
			"(the CLI's default mode silently auto-denies tools and bypassPermissions would " +
			"un-guard the session); set permissions: allow-all or use the copilot backend")
	}
	// Claude Code has no per-session OpenAI-compatible provider surface —
	// its endpoint routing is Anthropic-shaped env config. Fail clearly
	// like opencode rather than silently ignore the BYOK block.
	if opts.Provider.BaseURL != "" {
		return nil, fmt.Errorf("claude code manages its own endpoint routing (ANTHROPIC_BASE_URL "+
			"env, Anthropic-shaped only); BYOK provider %q is not supported — drop the provider "+
			"block for claude sessions", opts.Provider.BaseURL)
	}

	args := []string{
		"-p", "--input-format", "stream-json", "--output-format", "stream-json",
		// --include-partial-messages (requires --verbose) is what turns on
		// stream_event lines: without it there are no text/thinking deltas
		// and no message_delta usage, so sessions would look frozen and the
		// engine's budget check would only move at turn ends.
		"--verbose", "--include-partial-messages",
		"--permission-mode", "bypassPermissions",
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if len(opts.SystemHints) > 0 {
		args = append(args, "--append-system-prompt", strings.Join(opts.SystemHints, "\n\n"))
	}

	// spawn OUTSIDE the lock (fork/exec must not serialize session creation
	// or block a concurrent Close).
	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, c.bin, args...) //nolint:gosec // bin is operator config (GUMMI_CLAUDE_BIN), args are gummi-built
	cmd.Dir = opts.WorkDir
	// The child inherits gummi's environment: auth is out of band (the
	// user's claude login or ANTHROPIC_API_KEY), exactly like copilot's gh
	// auth, and the CLI reads its own config from $HOME.
	cmd.Env = os.Environ()
	// Run the child in its own process group and, on cancel/close, kill the
	// whole group: claude spawns tool subprocesses (bash, editors) that
	// would otherwise orphan and keep the stdout pipe open, stalling
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
		return nil, fmt.Errorf("claude stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("claude stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("starting claude: %w", err)
	}

	s := &claudeSession{
		cmd:         cmd,
		cancel:      cancel,
		stdin:       stdin,
		stderr:      stderr,
		workdir:     opts.WorkDir,
		raw:         make(chan Event, 64),
		events:      make(chan Event),
		stop:        make(chan struct{}),
		readDone:    make(chan struct{}),
		prevCostUSD: map[string]float64{},
		estimated:   map[string]float64{},
	}
	go s.forward()
	go s.read(stdout)

	// register under the lock, re-checking closed so a session started
	// concurrently with Close is torn down rather than leaked.
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = s.Close()
		return nil, errors.New("claude agent is closed")
	}
	c.sessions = append(c.sessions, s)
	c.mu.Unlock()
	return s, nil
}

// Close implements Agent: end every live session.
func (c *ClaudeCode) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for _, s := range c.sessions {
		_ = s.Close()
	}
	return nil
}

type claudeSession struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	workdir string // opts.WorkDir, for repo-relative tool-call details
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
	inTurn      bool // a Send is unanswered by a result line
	interrupted bool // the in-flight turn was aborted by our Interrupt (not a failure)
	intSeq      int  // interrupt control_request id counter

	// Metering state, owned exclusively by the read goroutine (proposal §3).
	// The CLI's result lines report *cumulative* cost/tokens per model, so
	// settlement is a delta against the previous result's snapshot; mid-turn
	// message_delta events are metered as estimates at the session's
	// realized USD-per-token rate and subtracted back out at settlement so
	// cumulative credits always equal the CLI's actual cost.
	sessionID   string
	mainModel   string             // resolved model id from init (modelUsage key)
	reqModel    string             // model of the in-flight API request (message_start)
	rate        float64            // realized USD per token, set at each result
	prevCostUSD map[string]float64 // per-model cumulative costUSD at the last result
	estimated   map[string]float64 // per-model credits estimated mid-turn, un-settled
	ctxTokens   int64              // main model's last request: input+cache tokens
}

// reap waits for the child exactly once (read() reaps a self-exited child;
// Close reaps a killed one) and caches the exit status. Wait is safe here
// because the only stdout reader — read() — is done before either caller
// reaches it.
func (s *claudeSession) reap() error {
	s.waitOnce.Do(func() { s.waitErr = s.cmd.Wait() })
	return s.waitErr
}

// stopping reports whether Close has begun tearing the session down, so a
// kill-induced stdout EOF isn't misreported as a child crash.
func (s *claudeSession) stopping() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *claudeSession) Events() <-chan Event { return s.events }

// forward owns events: it copies raw→events and closes events exactly
// once (the read goroutine and Send never touch events directly).
func (s *claudeSession) forward() {
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

// read scans the child's stdout, maps each JSON line to events, and feeds
// the forwarder until stdout closes.
func (s *claudeSession) read(stdout io.Reader) {
	defer close(s.readDone)
	sc := bufio.NewScanner(stdout)
	// result/assistant lines embed whole messages; 8 MiB matches opencode.
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
	// stdout closed. Unlike headless, a clean EOF is NOT an idle: turns end
	// at result lines and the process stays alive between them, so EOF while
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
		final = Event{Kind: EventError, Err: fmt.Errorf("claude stream aborted: %w", scanErr)}
	} else {
		detail := strings.TrimSpace(s.stderr.String())
		if detail == "" && waitErr != nil {
			detail = waitErr.Error()
		}
		if detail == "" {
			detail = "process exited unexpectedly"
		}
		final = Event{Kind: EventError, Err: fmt.Errorf("claude exited mid-session: %s", detail)}
	}
	select {
	case s.raw <- final:
	case <-s.stop:
	}
}

// ccLine is one stdout line's envelope; only the fields the adapter reads.
type ccLine struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	Model     string          `json:"model"`   // system/init
	Event     json.RawMessage `json:"event"`   // stream_event: the API SSE event
	Message   json.RawMessage `json:"message"` // assistant: the full API message
	// result fields
	IsError    bool                    `json:"is_error"`
	Result     string                  `json:"result"`
	Errors     []json.RawMessage       `json:"errors"`
	ModelUsage map[string]ccModelUsage `json:"modelUsage"`
}

// ccModelUsage is one result line's per-model cumulative usage entry.
type ccModelUsage struct {
	InputTokens              int64   `json:"inputTokens"`
	OutputTokens             int64   `json:"outputTokens"`
	CacheReadInputTokens     int64   `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64   `json:"cacheCreationInputTokens"`
	CostUSD                  float64 `json:"costUSD"`
	ContextWindow            int64   `json:"contextWindow"`
}

// ccStreamEvent is the API SSE event inside a stream_event line.
type ccStreamEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"delta"`
	Usage   *ccAPIUsage `json:"usage"` // message_delta: the request's final usage
	Message struct {
		Model string `json:"model"`
	} `json:"message"` // message_start
}

// ccAPIUsage is the API-shaped usage object (message_delta, result.usage).
type ccAPIUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// ccAssistantMessage is the API message inside an assistant line.
type ccAssistantMessage struct {
	Model   string `json:"model"`
	Content []struct {
		Type  string         `json:"type"`
		Text  string         `json:"text"`
		Name  string         `json:"name"`  // tool_use
		Input map[string]any `json:"input"` // tool_use arguments
	} `json:"content"`
}

// mapLine converts one stdout line into zero or more gummi Events and
// advances the metering state. It runs only on the read goroutine, so the
// metering fields need no lock. Unknown types/subtypes are dropped, not
// errored — the protocol is unversioned and drifts (risk R1); non-JSON
// lines are ignored quietly for the same reason.
func (s *claudeSession) mapLine(line []byte) []Event {
	var l ccLine
	if err := json.Unmarshal(line, &l); err != nil {
		return nil
	}
	switch l.Type {
	case "system":
		// init opens every turn (not just the first — P0). Capture the
		// resolved model id: it is the modelUsage key for settlement and
		// context, and opts.Model may be an alias ("haiku") or empty.
		if l.Subtype == "init" {
			if s.sessionID == "" {
				s.sessionID = l.SessionID
			}
			if s.mainModel == "" {
				s.mainModel = l.Model
			}
		}
		return nil // thinking_tokens and other advisory subtypes: dropped
	case "stream_event":
		return s.mapStreamEvent(l.Event)
	case "assistant":
		return s.mapAssistant(l.Message)
	case "result":
		return s.mapResult(&l)
	default:
		// user (tool-result echoes), rate_limit_event, control_response
		// (our interrupt's ack), and anything the CLI grows later.
		return nil
	}
}

// mapStreamEvent handles the incremental API SSE events: text/thinking
// deltas for display, message_start for the request's model, and
// message_delta for the request's final usage — the mid-turn metering
// point (the assistant lines' usage is a stub repeated per content block,
// proposal §2).
func (s *claudeSession) mapStreamEvent(raw json.RawMessage) []Event {
	var e ccStreamEvent
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil
	}
	switch e.Type {
	case "content_block_delta":
		switch e.Delta.Type {
		case "text_delta":
			if e.Delta.Text == "" {
				return nil
			}
			return []Event{{Kind: EventTextDelta, Text: e.Delta.Text}}
		case "thinking_delta":
			if e.Delta.Thinking == "" {
				return nil
			}
			return []Event{{Kind: EventReasoningDelta, Text: e.Delta.Thinking}}
		}
		return nil // input_json_delta, signature_delta
	case "message_start":
		s.reqModel = e.Message.Model
		return nil
	case "message_delta":
		if e.Usage == nil {
			return nil
		}
		u := *e.Usage
		total := u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
		if total == 0 {
			return nil
		}
		model := cmpOr(s.reqModel, s.mainModel)
		// The request's input side approximates the context window right
		// now; keep the main model's latest for EventContext at result
		// (side-model requests would understate it).
		if model == s.mainModel || s.mainModel == "" {
			s.ctxTokens = u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
		}
		ev := Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, Model: model}
		// Estimated credits at the session's realized rate keep the engine's
		// budget check live mid-turn (a claude turn is a whole agentic loop;
		// metering only at result would let a runaway turn blow past the
		// cap). Before the first result the rate is unknown — token-only
		// events, and the engine's token-derived fallback covers turn one.
		if s.rate > 0 {
			est := float64(total) * s.rate * 100 // credits are $0.01 units
			ev.Credits = est
			ev.Estimate = true // rate-derived; the result's settle reconciles it
			s.estimated[model] += est
		}
		return []Event{{Kind: EventUsage, Usage: ev}}
	}
	return nil
}

// mapAssistant surfaces a completed content block: prose blocks become
// their own message bubbles (blocks arrive in content order, so each text
// block lands before the tool_use that follows it — the "flush prose
// before the tool line" semantics opencode implements by hand), tool_use
// blocks become tool-call lines. Thinking blocks were already streamed as
// reasoning deltas; the block's usage stub is ignored (metered at
// message_delta instead).
func (s *claudeSession) mapAssistant(raw json.RawMessage) []Event {
	var m ccAssistantMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	var out []Event
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				out = append(out, Event{Kind: EventMessage, Text: b.Text})
			}
		case "tool_use":
			if b.Name != "" {
				out = append(out, Event{Kind: EventToolCall, Tool: b.Name, Detail: toolDetail(s.workdir, b.Input)})
			}
		}
	}
	return out
}

// mapResult settles the turn's spend and terminates it. modelUsage is
// cumulative across the whole process (P0), so each model's new spend is
// a delta against the previous result's snapshot, minus the estimates
// already emitted mid-turn — cumulative credits then equal the CLI's
// actual cost (a slightly negative correction is fine; the engine's
// addSpend is plain accumulation). Every model is settled — haiku
// side-calls are real spend — with the main model last so the session's
// last-writer spend.Model lands on the model that matters.
func (s *claudeSession) mapResult(l *ccLine) []Event {
	var out []Event
	if len(l.ModelUsage) > 0 {
		models := make([]string, 0, len(l.ModelUsage))
		for m := range l.ModelUsage {
			if m != s.mainModel {
				models = append(models, m)
			}
		}
		sort.Strings(models)
		if mu, ok := l.ModelUsage[s.mainModel]; ok {
			models = append(models, s.mainModel)
			// context: the main model's last request vs its window
			out = append(out, Event{Kind: EventContext, Context: Context{Tokens: s.ctxTokens, Limit: mu.ContextWindow}})
		}
		var cumUSD float64
		var cumTokens int64
		for _, m := range models {
			mu := l.ModelUsage[m]
			cumUSD += mu.CostUSD
			cumTokens += mu.InputTokens + mu.OutputTokens + mu.CacheReadInputTokens + mu.CacheCreationInputTokens
			delta := (mu.CostUSD-s.prevCostUSD[m])*100 - s.estimated[m]
			s.prevCostUSD[m] = mu.CostUSD
			// A settle goes out even when the correction is zero if this
			// model had mid-turn estimates: the flag is what tells the
			// engine the turn's estimates are now provider-metered.
			if delta != 0 || s.estimated[m] != 0 {
				out = append(out, Event{Kind: EventUsage, Usage: Usage{Credits: delta, Model: m, Settled: true}})
			}
		}
		s.estimated = map[string]float64{}
		// realized USD-per-token rate for next turn's mid-turn estimates
		if cumTokens > 0 {
			s.rate = cumUSD / float64(cumTokens)
		}
	}

	s.mu.Lock()
	aborted := s.interrupted
	s.interrupted = false
	s.inTurn = false
	s.mu.Unlock()

	// A turn our own Interrupt killed ends idle, not in error: the engine
	// itself interrupts sessions on budget stops, and EventError would
	// downgrade that clean stop to a failed run (failRun is unconditional).
	if l.IsError && !aborted {
		detail := strings.TrimSpace(l.Result)
		if detail == "" && len(l.Errors) > 0 {
			parts := make([]string, 0, len(l.Errors))
			for _, e := range l.Errors {
				parts = append(parts, string(e))
			}
			detail = strings.Join(parts, "; ")
		}
		if detail == "" {
			detail = l.Subtype
		}
		return append(out, Event{Kind: EventError, Err: fmt.Errorf("claude turn failed (%s): %s", l.Subtype, detail)})
	}
	return append(out, Event{Kind: EventIdle})
}

func (s *claudeSession) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	s.wmu.Lock()
	defer s.wmu.Unlock()
	// The stdin pipe is an *os.File and supports write deadlines; bound the
	// write so a child that stopped reading can't block us indefinitely
	// (same rationale as headless: a wedged child must not hang the
	// engine's pump goroutine, whose budget-stop Interrupt writes here too).
	if f, ok := s.stdin.(interface{ SetWriteDeadline(time.Time) error }); ok {
		_ = f.SetWriteDeadline(time.Now().Add(headlessWriteTimeout))
	}
	_, err = s.stdin.Write(b)
	return err
}

// ccUserFrame is a user turn on the wire.
type ccUserFrame struct {
	Type    string        `json:"type"`
	Message ccUserMessage `json:"message"`
}

type ccUserMessage struct {
	Role    string        `json:"role"`
	Content []ccTextBlock `json:"content"`
}

type ccTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (s *claudeSession) Send(_ context.Context, msg string) error {
	s.mu.Lock()
	// a stale interrupted flag from a race (Interrupt landing after its
	// turn's result) must not mask the new turn's genuine errors.
	s.interrupted = false
	s.inTurn = true
	s.mu.Unlock()
	err := s.write(ccUserFrame{Type: "user", Message: ccUserMessage{
		Role: "user", Content: []ccTextBlock{{Type: "text", Text: msg}},
	}})
	if err != nil {
		s.mu.Lock()
		s.inTurn = false
		s.mu.Unlock()
	}
	return err
}

// Interrupt aborts the in-flight turn via the control protocol (P0: the
// CLI acks with a control_response and ends the turn ~immediately with an
// error_during_execution result, which the interrupted flag maps to idle;
// the session then accepts the next turn). No turn in flight is a no-op —
// setting the flag anyway would swallow the next turn's real errors.
func (s *claudeSession) Interrupt(_ context.Context) error {
	s.mu.Lock()
	if !s.inTurn {
		s.mu.Unlock()
		return nil
	}
	s.interrupted = true
	s.intSeq++
	id := fmt.Sprintf("gummi-interrupt-%d", s.intSeq)
	s.mu.Unlock()
	return s.write(map[string]any{
		"type":       "control_request",
		"request_id": id,
		"request":    map[string]string{"subtype": "interrupt"},
	})
}

func (s *claudeSession) Close() error {
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
	})
	return nil
}
