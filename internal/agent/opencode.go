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

// Opencode is an Agent backed by the opencode CLI's headless scripting
// interface (`opencode run --format json`), which streams the model's
// activity as one JSON event per line and continues a persistent session
// via --session (DESIGN §4.1: a second concrete adapter). A gummi turn is
// one `opencode run` process: gummi spawns it with the message, maps its
// event stream, and the process exiting is the turn going idle.
//
// (The roadmap's "opencode adapter (HTTP)" predates the tool being on
// hand; opencode's actual machine interface for automation is this JSON
// event protocol, so the adapter is built on it. Interruption kills the
// turn's process; the --session state persists on opencode's side.)
type Opencode struct {
	bin string

	mu       sync.Mutex
	sessions []*opencodeSession
	closed   bool
}

// NewOpencode returns an Agent that drives the opencode binary (default
// "opencode", found on PATH). It fails fast when the binary is missing.
func NewOpencode(bin string) (*Opencode, error) {
	if bin == "" {
		bin = "opencode"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("opencode binary %q not found: %w", bin, err)
	}
	return &Opencode{bin: resolved}, nil
}

// Capabilities implements Agent. opencode persists sessions (--session),
// reports per-step token/cost usage, and can be interrupted by killing the
// turn's process. BYOK routing needs opencode-side provider config rather
// than a per-session env, so it is not advertised here.
func (o *Opencode) Capabilities() Capabilities {
	return Capabilities{Resume: true, UsageEvents: true, Interrupt: true}
}

// NewSession implements Agent. No process starts until the first Send; the
// opencode session id is captured from that turn's events and threaded
// into later turns via --session.
func (o *Opencode) NewSession(_ context.Context, opts SessionOpts) (Session, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, errors.New("opencode agent is closed")
	}
	if opts.Model == "" {
		return nil, errors.New("opencode requires a model (provider/model, e.g. opencode/deepseek-v4-flash-free)")
	}
	s := &opencodeSession{
		o:       o,
		workdir: opts.WorkDir,
		model:   opts.Model,
		hints:   opts.SystemHints,
		raw:     make(chan Event, 32),
		events:  make(chan Event),
		stop:    make(chan struct{}),
		partLen: map[string]int{},
	}
	go s.forward()
	o.sessions = append(o.sessions, s)
	return s, nil
}

// Close implements Agent.
func (o *Opencode) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	for _, s := range o.sessions {
		_ = s.Close()
	}
	return nil
}

type opencodeSession struct {
	o       *Opencode
	workdir string
	model   string
	hints   []string

	raw    chan Event
	events chan Event
	stop   chan struct{}

	mu          sync.Mutex
	sessionID   string             // captured from the first turn's events
	cancel      context.CancelFunc // cancels the in-flight turn's process
	partLen     map[string]int     // per text-part emitted length, for deltas
	primed      bool               // system hints injected on the first turn
	interrupted bool               // the current turn was killed by Interrupt (not a failure)
	closed      bool
	closeOnce   sync.Once
}

func (s *opencodeSession) Events() <-chan Event { return s.events }

func (s *opencodeSession) forward() {
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

// Send runs one turn: `opencode run --format json …` in the worktree,
// mapping the JSON event stream to gummi Events. It returns once the
// process has started; the turn streams asynchronously and ends (idle)
// when the process exits.
func (s *opencodeSession) Send(_ context.Context, msg string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("session closed")
	}
	if s.cancel != nil {
		// a turn is already streaming; the orchestrator serializes turns
		// (one message per idle), so this only guards against misuse that
		// would spawn a second concurrent run and orphan the first.
		s.mu.Unlock()
		return errors.New("a turn is already in progress")
	}
	args := []string{"run", "--format", "json", "-m", s.model}
	if s.sessionID != "" {
		args = append(args, "--session", s.sessionID)
	}
	// On the first turn, prepend the stage system hints to the message so
	// opencode's agent has gummi's stage instructions (opencode has no
	// separate system-prompt channel on the run interface).
	prompt := msg
	if !s.primed && len(s.hints) > 0 {
		prompt = strings.Join(s.hints, "\n\n") + "\n\n" + msg
		s.primed = true
	}
	args = append(args, prompt)

	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, s.o.bin, args...) //nolint:gosec // bin is operator config, args are gummi-built
	cmd.Dir = s.workdir
	cmd.Env = os.Environ()
	// Run opencode in its own process group and, on cancel/interrupt, kill
	// the whole group — opencode spawns tool subprocesses (bash, editors)
	// that would otherwise be orphaned and keep the stdout pipe open,
	// stalling the turn's teardown. WaitDelay force-closes the pipes if a
	// child lingers, so Wait can't hang.
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
		return fmt.Errorf("opencode stdout: %w", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		s.mu.Unlock()
		return fmt.Errorf("starting opencode run: %w", err)
	}
	s.cancel = cancel
	s.mu.Unlock()

	go s.readTurn(cmd, stdout, &stderr, cancel)
	return nil
}

// readTurn maps one `opencode run` process's stdout to events and ends the
// turn with idle (or error) when the process exits.
func (s *opencodeSession) readTurn(cmd *exec.Cmd, stdout io.Reader, stderr fmt.Stringer, cancel context.CancelFunc) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var msg strings.Builder
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		for _, ev := range s.mapEvent(line, &msg) {
			select {
			case s.raw <- ev:
			case <-s.stop:
				cancel()
				_ = cmd.Wait() // reap the killed process (no zombie/fd leak)
				return
			}
		}
	}
	// process finished (or stdout closed): finalize the assistant message,
	// then report idle — or an error if the run genuinely failed.
	waitErr := cmd.Wait()
	cancel()
	s.mu.Lock()
	s.cancel = nil
	closed := s.closed
	aborted := s.interrupted // killed by Interrupt: a clean stop, not a failure
	s.interrupted = false
	s.mu.Unlock()
	if closed {
		return // session torn down; the forwarder is closing
	}
	if text := strings.TrimSpace(msg.String()); text != "" {
		s.emit(Event{Kind: EventMessage, Text: text})
	}
	// an interrupted turn ends idle (the orchestrator's pause/budget path
	// already recorded why); only a non-zero exit we didn't cause is an error.
	if aborted {
		s.emit(Event{Kind: EventIdle})
		return
	}
	if waitErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = waitErr.Error()
		}
		s.emit(Event{Kind: EventError, Err: fmt.Errorf("opencode run failed: %s", detail)})
		return
	}
	s.emit(Event{Kind: EventIdle})
}

func (s *opencodeSession) emit(e Event) {
	select {
	case s.raw <- e:
	case <-s.stop:
	}
}

// ocEvent is one line of `opencode run --format json`.
type ocEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      struct {
		ID     string  `json:"id"`
		Type   string  `json:"type"`
		Text   string  `json:"text"`
		Tool   string  `json:"tool"`
		Cost   float64 `json:"cost"`
		Error  string  `json:"error"`
		Tokens struct {
			Input  int64 `json:"input"`
			Output int64 `json:"output"`
		} `json:"tokens"`
	} `json:"part"`
}

// mapEvent converts one opencode JSON line into zero or more gummi Events,
// accumulating assistant text into msg for a final EventMessage.
func (s *opencodeSession) mapEvent(line []byte, msg *strings.Builder) []Event {
	var e ocEvent
	if err := json.Unmarshal(line, &e); err != nil {
		return nil // opencode also prints non-event lines; ignore quietly
	}
	if e.SessionID != "" {
		s.mu.Lock()
		if s.sessionID == "" {
			s.sessionID = e.SessionID
		}
		s.mu.Unlock()
	}
	switch e.Type {
	case "text":
		// emit only the new suffix of this part (robust whether opencode
		// streams a part incrementally or sends it whole).
		s.mu.Lock()
		prev := s.partLen[e.Part.ID]
		full := e.Part.Text
		var delta string
		if len(full) >= prev {
			delta = full[prev:]
		} else {
			delta = full // part reset unexpectedly
		}
		s.partLen[e.Part.ID] = len(full)
		s.mu.Unlock()
		if delta == "" {
			return nil
		}
		msg.WriteString(delta)
		return []Event{{Kind: EventTextDelta, Text: delta}}
	case "tool", "tool_use":
		if e.Part.Tool == "" {
			return nil
		}
		return []Event{{Kind: EventToolCall, Tool: e.Part.Tool}}
	case "step_finish":
		u := Usage{Model: s.model, InputTokens: e.Part.Tokens.Input, OutputTokens: e.Part.Tokens.Output}
		// opencode cost is USD; gummi credits are $0.01 units.
		u.Credits = e.Part.Cost * 100
		var out []Event
		if u.Credits != 0 || u.InputTokens != 0 || u.OutputTokens != 0 {
			out = append(out, Event{Kind: EventUsage, Usage: u})
		}
		// the step's input tokens approximate the current context size
		// (opencode reports no window limit, so Limit stays 0/unknown).
		if e.Part.Tokens.Input > 0 {
			out = append(out, Event{Kind: EventContext, Context: Context{Tokens: e.Part.Tokens.Input}})
		}
		return out
	case "error":
		detail := e.Part.Error
		if detail == "" {
			detail = "opencode reported an error"
		}
		return []Event{{Kind: EventError, Err: errors.New(detail)}}
	default:
		return nil // step_start and other lifecycle events aren't surfaced
	}
}

func (s *opencodeSession) Interrupt(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.interrupted = true // mark it a deliberate stop so readTurn emits idle
		s.cancel()           // kill the turn's process
	}
	return nil
}

func (s *opencodeSession) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		if s.cancel != nil {
			s.cancel()
		}
		s.mu.Unlock()
		close(s.stop) // forward closes events; readTurn exits via stop/EOF
	})
	return nil
}
