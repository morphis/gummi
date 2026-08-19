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
	"strings"
	"sync"
	"syscall"
	"time"
)

// codexExecPath locates gummi's own executable when materializing the
// per-invocation `-c mcp_servers.gummi=…` override, so codex's MCP child is
// a real `gummi __mcp` process rather than whatever shadows "gummi" on
// $PATH. Production uses the real os.Executable; tests rebind it.
var codexExecPath = os.Executable

// Codex drives the stable, machine-readable Codex CLI exec interface. Each
// turn is a separate process; Codex owns authentication, provider settings,
// configuration, and the durable thread state used by exec resume.
type Codex struct {
	bin      string
	mu       sync.Mutex
	sessions []*codexSession
	closed   bool
}

func NewCodex(bin string) (*Codex, error) {
	if bin == "" {
		bin = "codex"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("codex binary %q not found: %w", bin, err)
	}
	return &Codex{bin: resolved}, nil
}

func (c *Codex) Name() string { return "codex" }
func (c *Codex) Capabilities() Capabilities {
	return Capabilities{Resume: true, UsageEvents: true, Interrupt: true, MCPTools: true}
}
func (c *Codex) CreditRate(string) float64 { return 0 }

func (c *Codex) NewSession(_ context.Context, opts SessionOpts) (Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("codex agent is closed")
	}
	if opts.Model == "" {
		return nil, errors.New("codex requires a model")
	}
	// A ReadOnly research session runs in the main checkout with no
	// worktree. This backend has no structural read-only cage
	// (ReadOnlyEnforce is false), so refuse rather than silently run
	// read-write — the engine gate is the first line, this is the second
	// so a stray direct call cannot drop the deny.
	if opts.ReadOnly {
		return nil, errors.New("codex backend cannot enforce a read-only research session; " +
			"point this role at `claude` or `opencode`, or accept that autonomous research cannot run on codex")
	}
	s := &codexSession{
		c: c, workdir: opts.WorkDir, model: opts.Model, hints: opts.SystemHints,
		featureID: opts.FeatureID, mcpSock: opts.MCPSockPath,
		raw: make(chan Event, 32), events: make(chan Event), stop: make(chan struct{}),
	}
	go s.forward()
	c.sessions = append(c.sessions, s)
	return s, nil
}

func (c *Codex) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for _, s := range c.sessions {
		_ = s.Close()
	}
	return nil
}

type codexSession struct {
	c                           *Codex
	workdir, model              string
	featureID, mcpSock          string // opts.FeatureID, opts.MCPSockPath (feature gates the -c override)
	hints                       []string
	raw                         chan Event
	events                      chan Event
	stop                        chan struct{}
	mu                          sync.Mutex
	threadID                    string
	cancel                      context.CancelFunc
	primed, interrupted, closed bool
	closeOnce                   sync.Once
	started                     map[string]bool
}

func (s *codexSession) Events() <-chan Event { return s.events }
func (s *codexSession) SessionID() string    { s.mu.Lock(); defer s.mu.Unlock(); return s.threadID }

func (s *codexSession) forward() {
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

func (s *codexSession) Send(_ context.Context, msg string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("session closed")
	}
	if s.cancel != nil {
		s.mu.Unlock()
		return errors.New("a turn is already in progress")
	}
	args, err := s.buildArgs()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	prompt := msg
	if !s.primed && len(s.hints) > 0 {
		prompt = strings.Join(s.hints, "\n\n") + "\n\n" + msg
	}
	s.primed = true
	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, s.c.bin, args...) //nolint:gosec // executable is operator-selected; argv is adapter-built
	cmd.Dir, cmd.Env, cmd.Stdin = s.workdir, os.Environ(), strings.NewReader(prompt)
	// The socket path reaches the __mcp child via the TOML env table on the
	// -c override, not the parent process env; codex auth still resolves via
	// the inherited environment above.
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
		return fmt.Errorf("codex stdout: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		s.mu.Unlock()
		return fmt.Errorf("starting codex exec: %w", err)
	}
	s.cancel = cancel
	s.mu.Unlock()
	go s.readTurn(cmd, stdout, &stderr, cancel)
	return nil
}

// buildArgs assembles the argv for one `codex exec` invocation. The
// approval policy is expressed as an inline config override (`-c`) rather
// than the top-level `-a` flag, codex exec does not accept; keeping every
// token on the exec subcommand mirrors the MCP override injected below,
// and leaves the argument vector testable against a real codex binary
// without a live session.
func (s *codexSession) buildArgs() ([]string, error) {
	args := []string{
		"exec", "--json", "--color", "never", "-m", s.model,
		"-s", "workspace-write", "-c", `approval_policy="never"`,
		"--skip-git-repo-check", "--ignore-user-config",
	}
	// With a feature id and MCP socket both present, register gummi's tool
	// server via an inline TOML config override (`-c`), codex's only
	// per-invocation MCP injection point. Missing either field -> no MCP
	// flags, so a session without a feature id starts without MCP rather
	// than failing (mirrors claudecode/opencode).
	if s.featureID != "" && s.mcpSock != "" {
		exe, err := codexExecPath()
		if err != nil {
			return nil, fmt.Errorf("codex adapter: locating own executable: %w", err)
		}
		override, err := buildCodexGummiOverride(exe, s.featureID, s.mcpSock)
		if err != nil {
			return nil, fmt.Errorf("codex adapter: building gummi override: %w", err)
		}
		args = append(args, "-c", override)
	}
	if s.threadID != "" {
		args = append(args, "resume", s.threadID)
	}
	args = append(args, "-")
	return args, nil
}

func (s *codexSession) readTurn(cmd *exec.Cmd, stdout io.Reader, stderr fmt.Stringer, cancel context.CancelFunc) {
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
			// Idle is published only after the subprocess is reaped and the
			// in-flight marker is cleared, so a caller may immediately Send.
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
		s.emit(Event{Kind: EventError, Err: fmt.Errorf("codex exec stream aborted: %w", scanErr)})
		return
	}
	if waitErr != nil {
		s.emit(Event{Kind: EventError, Err: fmt.Errorf("codex exec failed: %s", diagnostic(stderr.String(), waitErr.Error()))})
		return
	}
	if !terminal {
		s.emit(Event{Kind: EventError, Err: fmt.Errorf("codex exec exited without a terminal turn event: %s", diagnostic(stderr.String(), "no diagnostics"))})
		return
	}
	s.emit(Event{Kind: EventIdle})
}

func (s *codexSession) finishTurn(cancel context.CancelFunc) {
	cancel()
	s.mu.Lock()
	s.cancel = nil
	s.mu.Unlock()
}

func (s *codexSession) emit(e Event) {
	select {
	case s.raw <- e:
	case <-s.stop:
	}
}

func (s *codexSession) mapLine(line []byte) ([]Event, bool, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, false, fmt.Errorf("malformed codex JSONL: %w", err)
	}
	var typ string
	_ = json.Unmarshal(raw["type"], &typ)
	switch typ {
	case "thread.started":
		var id string
		_ = json.Unmarshal(raw["thread_id"], &id)
		if id == "" {
			return nil, false, errors.New("codex thread.started omitted thread_id")
		}
		s.mu.Lock()
		if s.threadID == "" {
			s.threadID = id
		}
		s.mu.Unlock()
	case "item.completed", "item.started":
		var i struct {
			ID               string          `json:"id"`
			Type             string          `json:"type"`
			Text             string          `json:"text"`
			Command          string          `json:"command"`
			AggregatedOutput string          `json:"aggregated_output"`
			Status           string          `json:"status"`
			ExitCode         *int            `json:"exit_code"`
			Server           string          `json:"server"`
			Tool             string          `json:"tool"`
			Error            json.RawMessage `json:"error"`
			Result           json.RawMessage `json:"result"`
			Changes          []struct {
				Path string `json:"path"`
				Kind string `json:"kind"`
			} `json:"changes"`
		}
		if err := json.Unmarshal(raw["item"], &i); err != nil {
			return nil, false, fmt.Errorf("malformed codex item: %w", err)
		}
		if typ == "item.started" {
			s.mu.Lock()
			if s.started == nil {
				s.started = map[string]bool{}
			}
			s.started[i.ID] = true
			s.mu.Unlock()
			return itemStarted(i.ID, i.Type, i.Command, i.Server, i.Tool, i.Changes), false, nil
		}
		s.mu.Lock()
		started := s.started[i.ID]
		delete(s.started, i.ID)
		s.mu.Unlock()
		return itemCompleted(started, i.ID, i.Type, i.Text, i.Command, i.AggregatedOutput, i.Status, i.ExitCode, i.Server, i.Tool, rawMessageText(i.Error), i.Result, i.Changes), false, nil
	case "turn.completed":
		var u struct {
			Input  int64 `json:"input_tokens"`
			Cached int64 `json:"cached_input_tokens"`
			Output int64 `json:"output_tokens"`
		}
		_ = json.Unmarshal(raw["usage"], &u)
		fresh := u.Input - u.Cached
		if fresh < 0 {
			fresh = 0
		}
		out := []Event{}
		if u.Input != 0 || u.Output != 0 {
			out = append(out, Event{Kind: EventUsage, Usage: Usage{Model: s.model, InputTokens: fresh, CachedTokens: u.Cached, OutputTokens: u.Output}})
		}
		return append(out, Event{Kind: EventIdle}), true, nil
	case "turn.failed", "error":
		return nil, true, fmt.Errorf("codex %s: %s", typ, errorMessage(raw))
	}
	return nil, false, nil
}

func itemStarted(id, typ, command, server, tool string, changes []struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
},
) []Event {
	switch typ {
	case "command_execution":
		return []Event{{Kind: EventToolCall, Tool: "command", Detail: command, CallID: id}}
	case "file_change":
		return []Event{{Kind: EventToolCall, Tool: "file change", Detail: changeSummary(changes), CallID: id}}
	case "mcp_tool_call":
		return []Event{{Kind: EventToolCall, Tool: strings.Trim(strings.Join([]string{server, tool}, "."), "."), CallID: id}}
	}
	return nil
}

func itemCompleted(started bool, id, typ, text, command, output, status string, exit *int, server, tool, itemErr string, result json.RawMessage, changes []struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
},
) []Event {
	if typ == "agent_message" {
		if text != "" {
			return []Event{{Kind: EventMessage, Text: text}}
		}
		return nil
	}
	var name, detail, body string
	switch typ {
	case "command_execution":
		name, detail, body = "command", command, output
	case "file_change":
		name, detail, body = "file change", changeSummary(changes), changeSummary(changes)
	case "mcp_tool_call":
		name, body = strings.Trim(strings.Join([]string{server, tool}, "."), "."), string(result)
	default:
		return nil
	}
	ok := status == "completed" || status == "success" || status == "succeeded"
	if exit != nil {
		ok = *exit == 0
	}
	if itemErr != "" {
		ok, body = false, itemErr+"\n"+body
	}
	// Some Codex versions only emit completed items. Preserve correlation by
	// emitting the call immediately before its result in that case.
	resultEvent := Event{Kind: EventToolResult, Tool: name, CallID: id, Result: &ToolResult{OK: ok, Output: boundTail(body, ok)}}
	if started {
		return []Event{resultEvent}
	}
	return []Event{{Kind: EventToolCall, Tool: name, Detail: detail, CallID: id}, resultEvent}
}

func changeSummary(changes []struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
},
) string {
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		if c.Path != "" {
			parts = append(parts, strings.TrimSpace(c.Kind+" "+c.Path))
		}
	}
	return strings.Join(parts, ", ")
}

func errorMessage(raw map[string]json.RawMessage) string {
	var msg string
	_ = json.Unmarshal(raw["message"], &msg)
	if msg != "" {
		return msg
	}
	var e struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw["error"], &e)
	if e.Message != "" {
		return e.Message
	}
	return "unknown error"
}

func rawMessageText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var message struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &message) == nil && message.Message != "" {
		return message.Message
	}
	return string(raw)
}

func diagnostic(stderr, fallback string) string {
	if strings.TrimSpace(stderr) == "" {
		return fallback
	}
	return boundTail(stderr, false)
}

func (s *codexSession) Interrupt(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.interrupted = true
		s.cancel()
	}
	return nil
}

func (s *codexSession) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		if s.cancel != nil {
			s.cancel()
		}
		s.mu.Unlock()
		close(s.stop)
	})
	return nil
}
