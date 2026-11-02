package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// opencodeExecPath locates gummi's own executable when materializing the
// per-session config, so opencode's MCP child is a real `gummi __mcp`
// process rather than whatever shadows "gummi" on $PATH. Production uses
// the real os.Executable; tests rebind it (see opencode_integration_test).
var opencodeExecPath = os.Executable

// Opencode is an Agent backed by opencode's own HTTP server: one
// `opencode serve` process per gummi session, spawned when the session
// opens and killed when it closes, with every action — a turn, an abort,
// the model catalog, compaction, a guarded approval — an HTTP call
// against it. The server runs in the session's worktree with the same
// per-session OPENCODE_CONFIG the per-turn CLI used to run with, so the
// worktree permission cage and the session's mcp.gummi endpoint carry
// over 1:1.
//
// A turn is one POST of the message that blocks until the turn resolves,
// while the server's event bus feeds the activity in parallel — so a
// turn's latency no longer pays opencode's own startup, an interrupt is
// a server-side abort (partial work aborted, not lost) instead of a
// process-group kill, and the conversation id is read from the session
// the server created rather than scraped from event lines.
type Opencode struct {
	bin string
	// v2 marks a 2.x binary, whose server speaks 2.x's API
	// (opencode_v2.go); read once from its version when the agent starts.
	v2 bool

	mu       sync.Mutex
	sessions []*opencodeSession
	closed   bool
}

// NewOpencode returns an Agent that drives the opencode binary (default
// "opencode", found on PATH), 1.x or 2.x. It fails fast when the binary
// is missing.
func NewOpencode(bin string) (*Opencode, error) {
	if bin == "" {
		bin = "opencode"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("opencode binary %q not found: %w", bin, err)
	}
	return &Opencode{bin: resolved, v2: opencodeMajor(resolved) >= 2}, nil
}

// Name implements Agent.
func (o *Opencode) Name() string { return "opencode" }

// Capabilities implements Agent. opencode persists sessions server-side,
// reports per-step token/cost usage on its event bus, aborts a turn on
// request, and reaches gummi's tools via its MCP child.
func (o *Opencode) Capabilities() Capabilities {
	return Capabilities{Resume: true, UsageEvents: true, Interrupt: true, MCPTools: true, ReadOnlyEnforce: true, WriteCage: WriteCagePaths, Images: true, Compact: true, SkillDirs: true}
}

// CreditRate implements Agent. opencode reports its own USD cost per step
// (see mapEvent), so the engine must not re-price its tokens.
func (o *Opencode) CreditRate(string) float64 { return 0 }

// ModelCatalog implements ModelCataloger: the provider/model pairs
// opencode itself offers, from its own catalog — asked live through a
// transient serve spawned for the ask (OpencodeModelCatalog), since the
// agent holds sessions, not the one server a picker would ask. The engine
// caches the answer (SessionModelCatalog), so that is one transient serve
// per cache miss, not per render.
func (o *Opencode) ModelCatalog(ctx context.Context) ([]string, error) {
	return OpencodeModelCatalog(ctx, o.bin)
}

// NewSession implements Agent. It spawns the session's server — the
// process this session owns for its whole life, started with the
// per-session config (worktree permission cage, the session's mcp.gummi
// endpoint, scratch/read allows, output-token cap) —
// and answers immediately: readiness is the first turn's wait, so a
// wedged server surfaces there, as a turn failure, not a wedged start.
func (o *Opencode) NewSession(_ context.Context, opts SessionOpts) (Session, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, errors.New("opencode agent is closed")
	}
	if _, _, err := SplitOpencodeModel(opts.Model); err != nil {
		return nil, err
	}
	// Resolve gummi's own executable and materialize the session config
	// before anything starts, so a failure here is terminal rather than
	// silently falling back to a $PATH "opencode" that could spawn a
	// mismatched MCP child.
	exe, err := opencodeExecPath()
	if err != nil {
		return nil, fmt.Errorf("opencode adapter: locating own executable: %w", err)
	}
	cfg, err := buildOpencodeConfig(opts.WorkDir, opts.MCPSockPath, opts.FeatureID, exe, append(slices.Clone(opts.ExtraReadAllows), opts.SkillDirs...), opts.ReadOnly, append([]string{opts.ScratchDir}, opts.WriteDirs...), opts.Permission)
	if err != nil {
		return nil, fmt.Errorf("opencode adapter: building session config: %w", err)
	}
	// 2.x takes no system field on a prompt: the stage hints ride the
	// session config as an instruction file instead.
	var hintsPath string
	if o.v2 && len(opts.SystemHints) > 0 {
		hf, err := os.CreateTemp("", "gummi-opencode-hints-*.md")
		if err != nil {
			return nil, fmt.Errorf("opencode adapter: creating session hints: %w", err)
		}
		hintsPath = hf.Name()
		_, werr := hf.WriteString(strings.Join(opts.SystemHints, "\n\n"))
		if cerr := hf.Close(); werr == nil {
			werr = cerr
		}
		if werr == nil {
			cfg, werr = withInstructions(cfg, hintsPath)
		}
		if werr != nil {
			_ = os.Remove(hintsPath)
			return nil, fmt.Errorf("opencode adapter: writing session hints: %w", werr)
		}
	}
	cf, err := os.CreateTemp("", "gummi-opencode-*.json")
	if err != nil {
		if hintsPath != "" {
			_ = os.Remove(hintsPath)
		}
		return nil, fmt.Errorf("opencode adapter: creating session config: %w", err)
	}
	configPath := cf.Name()
	cleanup := func() {
		_ = os.Remove(configPath)
		if hintsPath != "" {
			_ = os.Remove(hintsPath)
		}
	}
	if _, err := cf.Write(cfg); err != nil {
		_ = cf.Close()
		cleanup()
		return nil, fmt.Errorf("opencode adapter: writing session config: %w", err)
	}
	if err := cf.Close(); err != nil {
		cleanup()
		return nil, fmt.Errorf("opencode adapter: closing session config: %w", err)
	}
	port, err := freeLoopbackPort()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("opencode adapter: %w", err)
	}
	password, err := randomToken()
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("opencode adapter: %w", err)
	}
	// A password, so nothing else on this host can drive this session's
	// server for the life of the session.
	env := append(childEnvFor(opts.OutputTokenMax, opts.MCPSockPath, configPath),
		"OPENCODE_SERVER_PASSWORD="+password)
	if skills := opencodeSkillsContent(opts.WorkDir, opts.SkillDirs); skills != "" {
		env = append(env, "OPENCODE_CONFIG_CONTENT="+skills)
	}
	proc, err := serveOpencode(context.Background(), o.bin, port, opts.WorkDir, env)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("opencode adapter: starting opencode serve: %w", err)
	}
	srvCtx, srvCancel := context.WithCancel(context.Background())
	s := &opencodeSession{
		o: o,
		// The session the engine says this one continues. A server session
		// is addressed by id on its first message POST; one the server
		// cannot find falls back to a fresh conversation on that turn, so
		// a resume is never the reason a stage fails.
		sessionID:      opts.ResumeID,
		resumed:        opts.ResumeID != "",
		workdir:        opts.WorkDir,
		model:          opts.Model,
		hints:          opts.SystemHints,
		mcpSock:        opts.MCPSockPath,
		outputTokenMax: opts.OutputTokenMax,
		configPath:     configPath,
		hintsPath:      hintsPath,
		featureID:      opts.FeatureID,
		srv:            opencodeServer{base: proc.url, password: password, v2: o.v2},
		proc:           proc,
		sctx:           srvCtx,
		srvCancel:      srvCancel,
		upDone:         make(chan struct{}),
		raw:            make(chan Event, 32),
		events:         make(chan Event, 64),
		stop:           make(chan struct{}),
		partLen:        map[string]int{},
	}
	go s.forward()
	o.sessions = append(o.sessions, s)
	return s, nil
}

// SplitOpencodeModel splits an opencode model id into the provider and
// model halves its server addresses a model by. An id without both — a
// bare "claude-sonnet-5" from a profile written for another backend — is
// refused here, at session start: the server would take it as a provider
// with no model and fail every turn with an opaque 500.
func SplitOpencodeModel(model string) (provider, id string, err error) {
	provider, id, ok := strings.Cut(strings.TrimSpace(model), "/")
	if !ok || provider == "" || id == "" || strings.ContainsAny(provider, " \t") {
		if model == "" {
			return "", "", errors.New("opencode requires a model (provider/model, e.g. opencode/deepseek-v4-flash-free)")
		}
		return "", "", fmt.Errorf("opencode model %q is not provider/model (e.g. opencode/deepseek-v4-flash-free)", model)
	}
	return provider, id, nil
}

// Close implements Agent. The sessions close outside the agent's lock:
// each one takes it on its way out to drop itself from the list.
func (o *Opencode) Close() error {
	o.mu.Lock()
	o.closed = true
	sessions := slices.Clone(o.sessions)
	o.mu.Unlock()
	for _, s := range sessions {
		_ = s.Close()
	}
	return nil
}

// forget drops a closed session from the agent's list, so a long-lived
// board does not hold every session it ever opened.
func (o *Opencode) forget(s *opencodeSession) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sessions = slices.DeleteFunc(o.sessions, func(x *opencodeSession) bool { return x == s })
}

// childEnvFor is the environment an opencode process runs with: its own,
// plus the session's config and gummi's socket. It is shared by the
// session's server spawn and the transient one the no-adapter catalog
// probe runs (which passes no session config at all).
func childEnvFor(outputTokenMax int, mcpSock, configPath string) []string {
	env := os.Environ()
	// opencode caps each step's output at min(limit.output, 32000) and only
	// this env var lifts the 32000 ceiling (opencode.jsonc can't). Set per
	// the role's output_token_max so reasoning-heavy stages aren't truncated
	// (reason=length, output=0). gummi forwards os.Environ() to opencode, so
	// appending here reaches the child.
	if outputTokenMax > 0 {
		env = append(env, fmt.Sprintf("OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX=%d", outputTokenMax))
	}
	if mcpSock != "" {
		env = append(env, "GUMMI_MCP_SOCK="+mcpSock)
	}
	// The per-session config carries the worktree cage and the mcp.gummi
	// endpoint. Exporting OPENCODE_CONFIG alongside GUMMI_MCP_SOCK (which
	// opencode's mcp.local.environment only applies to the spawned MCP
	// subprocess, not the main process) makes the child inherit the socket
	// too.
	if configPath != "" {
		env = append(env, "OPENCODE_CONFIG="+configPath)
	}
	return env
}

type opencodeSession struct {
	o              *Opencode
	workdir        string
	model          string
	hints          []string
	mcpSock        string // opts.MCPSockPath (exported to the child when set)
	outputTokenMax int    // >0 → export OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX
	// configPath is the per-session OPENCODE_CONFIG temp file, materialized
	// at NewSession and removed at Close — after the server process is
	// dead, which is the only reader of it.
	configPath string
	// hintsPath is a 2.x session's instruction file of stage hints, named
	// by its config and removed with it; "" on 1.x.
	hintsPath string
	// featureID mirrors SessionOpts.FeatureID, threaded into the config's
	// mcp.gummi command so the spawned child serves the right feature.
	featureID string

	// srv is the HTTP client for the session's server; proc is that
	// server's process.
	srv  opencodeServer
	proc *opencodeProc
	// sctx is the session's lifetime: it bounds the event bus's GET and,
	// by parentage, every turn's calls. Closing cancels it before the
	// server process is killed.
	sctx      context.Context
	srvCancel context.CancelFunc

	raw       chan Event
	events    chan Event
	stop      chan struct{}
	mu        sync.Mutex
	sessionID string             // the server session's id, from its create response
	cancel    context.CancelFunc // the in-flight compaction's summarize call
	upOnce    sync.Once
	up        bool // the server answered its readiness poll
	upErr     error
	upDone    chan struct{}
	turn      *ocTurn
	partLen   map[string]int // per text-part emitted length, for deltas
	// partKind names each open text or reasoning part ("text" |
	// "reasoning") by id, so a message.part.delta — which carries only the
	// part id — knows what it streams. Dropped when the part finishes.
	partKind map[string]string
	// children holds the server sessions descended from this one — the
	// task tool's subagents, and theirs — learned from session.created.
	// Their parts are not this session's to surface, but what their steps
	// cost is spend this session ran up.
	children map[string]bool
	// announced holds the tool calls announced while running, so their
	// finished part reports only the outcome.
	announced map[string]bool
	// resumed marks a session id handed in by the engine whose first turn
	// has not landed yet. A server that cannot find the id fails the
	// turn's POST, and a resume must never be the reason a stage fails, so
	// that first failure drops the id and runs the turn again on a fresh
	// conversation.
	resumed     bool
	interrupted bool // the current turn was stopped by Interrupt (not a failure)
	disposed    bool // 1.x: the bus reported its instance disposed, which ends the stream
	closed      bool
	closeOnce   sync.Once
	// hadIdle marks that some prior turn on this session reached a clean
	// idle — the signal RunFailure.FirstTurn is built from: a failure
	// before this is ever true is the "misconfigured backend" shape a
	// brand new user hits first, not a mid-task crash.
	hadIdle bool
	// ctxLimit is the model's context window from opencode's catalog,
	// asked once the server is up (limitOnce); 0 until it answers, or
	// when the catalog does not know the model.
	ctxLimit  int64
	limitOnce sync.Once

	// 2.x only (opencode_v2.go). exec is the execution a turn or a
	// compaction waits on; modelBound marks that the server session is on
	// this session's model (2.x keeps it on the session, so a resumed one
	// is put on it before its first turn); toolNames holds each open call's
	// name from its input's start; permSession the session each held call
	// was asked under, which its reply is addressed to.
	exec        *ocExec
	modelBound  bool
	toolNames   map[string]string
	permSession map[string]string
}

// ocTurn is one turn in flight: its context (canceled by Interrupt, which
// unblocks the message POST, and by Close), the text its events
// accumulate into, and whether anything was relayed at all — the
// silent-backend check a completed turn with zero events ends on.
type ocTurn struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed when the turn has ended
	// finishing is set once endTurn began (under the session's mutex): the
	// relay drops everything captured after it, so no event outlives the
	// turn's own final emission.
	finishing bool
	msg       ocMsg
	// errDetail is the first session.error the bus reported during the
	// turn (under the session's mutex). opencode publishes the real cause
	// there — "Model not found: …" — and answers the message POST with an
	// opaque 500, so the turn's one failure carries this, not the POST's.
	errDetail string
	// dropped: an event stream was lost under the turn and opened again,
	// so some of what it did may not have been relayed (under the
	// session's mutex).
	dropped bool
	// v2Text is how much of each 2.x text or reasoning part, by partKey,
	// the turn relayed (under the session's mutex): what reconcileTextV2
	// reads to relay only what a dropped stream lost.
	v2Text map[string]int
}

// ocMsg is the per-turn text accumulator: the event relay writes into it
// and the turn's end reads out, on different goroutines, under one mutex.
type ocMsg struct {
	mu     sync.Mutex
	sb     strings.Builder
	sawAny bool
}

func (m *ocMsg) WriteString(p string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sawAny = true
	return m.sb.WriteString(p)
}

func (m *ocMsg) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sb.String()
}

func (m *ocMsg) sawAnyValue() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sawAny
}

// mark folds whether the last mapping relayed anything into sawAny — the
// silent-backend check reads it. Text writes mark through WriteString;
// tool and usage events mark here, since they carry no prose.
func (m *ocMsg) mark(relayed bool) {
	m.mu.Lock()
	m.sawAny = m.sawAny || relayed
	m.mu.Unlock()
}

func (m *ocMsg) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sb.Reset()
}

func (s *opencodeSession) Events() <-chan Event { return s.events }

func (s *opencodeSession) forward() {
	defer close(s.events)
	for {
		select {
		case <-s.stop:
			// Drain what was already emitted into raw: Close relays a
			// mid-turn turn's interrupted-idle through here. It does not
			// wait on a consumer that has stopped reading — a full buffer's
			// tail is dropped, not held.
			for {
				select {
				case e := <-s.raw:
					select {
					case s.events <- e:
					default:
						return
					}
				default:
					return
				}
			}
		case e := <-s.raw:
			s.events <- e
		}
	}
}

// Send runs one turn: the message POSTed to the session's server, with
// the event bus's stream mapped to gummi Events as it arrives. It returns
// once the turn has been accepted; the turn streams asynchronously and
// ends (idle) when the POST returns.
func (s *opencodeSession) Send(ctx context.Context, msg string) error {
	return s.SendTurn(ctx, Turn{Text: msg})
}

// SendTurn implements ImageSender: each image becomes a file part of the
// message POST, then shares Send's path.
func (s *opencodeSession) SendTurn(_ context.Context, turn Turn) error {
	msg := turn.Text
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("session closed")
	}
	if s.turn != nil || s.cancel != nil {
		// a turn (or compaction) is already running; the orchestrator
		// serializes turns (one message per idle), so this only guards
		// against misuse that would double-send. It is a refusal, not a
		// failure — ErrBusy so the caller keeps the line instead of failing
		// the run over it.
		s.mu.Unlock()
		return ErrBusy
	}
	t := &ocTurn{done: make(chan struct{})}
	t.ctx, t.cancel = context.WithCancel(s.sctx)
	s.turn = t
	s.mu.Unlock()

	go s.runTurn(t, msg, turn.Images)
	return nil
}

// ensureUp waits for the server to answer its first readiness poll,
// starting the event bus reader on the way. A turn is never POSTed before
// this answers — no request races a server that is still starting.
func (s *opencodeSession) ensureUp(ctx context.Context, within time.Duration) error {
	s.upOnce.Do(func() {
		go func() {
			defer close(s.upDone)
			if err := s.srv.waitReady(s.sctx, within, s.proc.exited); err != nil {
				s.mu.Lock()
				s.upErr = err
				s.mu.Unlock()
				return
			}
			go s.readBus()
			s.mu.Lock()
			s.up = true
			s.mu.Unlock()
		}()
	})
	select {
	case <-s.upDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.up {
		return s.upErr
	}
	return nil
}

// runTurn is one turn's own goroutine: it waits for the server, creates
// the server session when this is the first turn, POSTs the message —
// which blocks until the turn resolves server-side — and ends the turn
// with the events the bus relayed.
func (s *opencodeSession) runTurn(t *ocTurn, msg string, imgs []Image) {
	defer close(t.done)
	defer t.cancel()
	if err := s.ensureUp(t.ctx, opencodeServeReady); err != nil {
		s.failTurn(t, &RunFailure{
			Backend: "opencode", Diagnostic: s.procStderr(),
			FirstTurn: !s.hadIdleValue(),
			Err:       fmt.Errorf("opencode serve: %w", err),
		})
		return
	}
	s.limitOnce.Do(func() { go s.fetchContextLimit() })
	for attempt := 0; ; attempt++ {
		err := s.postTurn(t, msg, imgs)
		if err == nil {
			// The turn resolved. The bus relays the final events
			// microseconds behind the POST's return; wait them out before
			// reading what the turn produced, so a turn that produced text
			// does not read as empty and retry.
			select {
			case <-s.stop:
			case <-time.After(ocTurnTail):
			}
			if attempt == 0 && !t.msg.sawAnyValue() && s.resumable() {
				// nothing was relayed at all: a backend or gateway that
				// died silently, not a real empty pass. A resumed first
				// turn retries fresh, once.
				s.dropResume()
				s.clearTurnErr(t)
				continue
			}
			break
		}
		if attempt == 0 && errors.Is(err, errOcLostSession) && s.resumable() {
			// the handed-in session id is one the server no longer knows:
			// a fresh conversation, stage hints back on the wire
			s.dropResume()
			s.clearTurnErr(t)
			continue
		}
		if t.ctx.Err() != nil {
			// the turn was stopped deliberately (Interrupt) or the session
			// closed under it: a clean idle, never a failure
			s.endTurn(t, nil)
		} else {
			// the server publishes the failure's cause on the bus around
			// the time it answers the POST; give it the same tail a clean
			// turn's last events get, so the failure carries it
			select {
			case <-s.stop:
			case <-time.After(ocTurnTail):
			}
			s.failTurn(t, err)
		}
		return
	}
	s.endTurn(t, nil)
}

// fetchContextLimit asks the server's catalog for the session model's
// context window, so the context events it reports carry a limit the
// meter and auto-compaction can read. A failed ask leaves it unknown.
func (s *opencodeSession) fetchContextLimit() {
	provider, model, ok := strings.Cut(s.model, "/")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(s.sctx, 15*time.Second)
	defer cancel()
	limit, err := s.srv.contextLimit(ctx, provider, model)
	if err != nil || limit <= 0 {
		return
	}
	s.mu.Lock()
	s.ctxLimit = limit
	s.mu.Unlock()
}

func (s *opencodeSession) contextLimitValue() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctxLimit
}

// postTurn addresses one message POST at the turn's session, creating the
// server session first when none is held.
func (s *opencodeSession) postTurn(t *ocTurn, msg string, imgs []Image) error {
	if s.srv.v2 {
		return s.postTurnV2(t, msg, imgs)
	}
	id, err := s.sessionForTurn(t.ctx)
	if err != nil {
		return err
	}
	return s.srv.message(t.ctx, id, s.promptBody(msg, imgs))
}

// sessionForTurn returns the server session id to address, creating the
// server session when none is held. The id is read from the create
// response, never scraped from event lines.
func (s *opencodeSession) sessionForTurn(ctx context.Context) (string, error) {
	s.mu.Lock()
	id := s.sessionID
	s.mu.Unlock()
	if id != "" {
		return id, nil
	}
	var created string
	var err error
	if s.srv.v2 {
		provider, model, _ := strings.Cut(s.model, "/")
		created, err = s.srv.createV2(ctx, provider, model)
	} else {
		created, err = s.srv.create(ctx)
	}
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.sessionID = created
	s.modelBound = true // created on it
	s.mu.Unlock()
	return created, nil
}

// resumable reports whether the handed-in session id can still be dropped
// for a fresh conversation: the first turn has not landed, the session has
// not idled cleanly yet, and the session is not closed.
func (s *opencodeSession) resumable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resumed && !s.hadIdle && !s.closed
}

// dropResume drops a handed-in session id so the retry runs on a fresh
// conversation: nothing of the failed attempt survived to be repeated —
// no events reached the caller — and the stage hints ride every turn's
// system field, so the fresh session has them too.
func (s *opencodeSession) dropResume() {
	s.mu.Lock()
	s.resumed, s.sessionID = false, ""
	s.mu.Unlock()
}

// promptBody builds the message POST's payload: the model, the stage
// system hints, then the turn's parts — images as file parts, then the
// text. The hints ride in the message's own system field on every turn:
// opencode adds the latest user message's system to the prompt it builds,
// so the stage instructions hold after a compaction has summarized the
// first message away, and a resumed conversation is not handed them a
// second time as a prompt of their own.
func (s *opencodeSession) promptBody(msg string, imgs []Image) map[string]any {
	provider, model, _ := strings.Cut(s.model, "/")
	parts := make([]map[string]any, 0, len(imgs)+1)
	for _, img := range imgs {
		if part := filePart(img); part != nil {
			parts = append(parts, part)
		}
	}
	parts = append(parts, map[string]any{"type": "text", "text": msg})
	body := map[string]any{
		"model": map[string]string{"providerID": provider, "modelID": model},
		"parts": parts,
	}
	if len(s.hints) > 0 {
		body["system"] = strings.Join(s.hints, "\n\n")
	}
	return body
}

// filePart turns an attachment into the message part opencode's prompt
// takes: the file inline as a data URI, named.
func filePart(img Image) map[string]any {
	raw, err := os.ReadFile(img.Path)
	if err != nil {
		return nil
	}
	mime := img.MediaType
	if mime == "" {
		mime = "application/octet-stream"
	}
	name := img.Path
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return map[string]any{
		"type":     "file",
		"mime":     mime,
		"filename": name,
		"url":      "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw),
	}
}

// failTurn ends a turn on a backend failure. A failure the caller already
// shaped (the server died mid-turn) passes through; anything else becomes
// one, with FirstTurn the signal a caller needs to tell a misconfigured
// backend from a mid-task crash.
func (s *opencodeSession) failTurn(t *ocTurn, err error) {
	rf, ok := err.(*RunFailure)
	if !ok {
		rf = &RunFailure{Backend: "opencode", FirstTurn: !s.hadIdleValue(), Err: err}
	}
	if rf.Diagnostic == "" {
		rf.Diagnostic = s.turnErrDetail(t)
	}
	s.endTurn(t, rf)
}

// turnErrDetail is the session.error the bus reported during t, if any.
func (s *opencodeSession) turnErrDetail(t *ocTurn) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return t.errDetail
}

// clearTurnErr forgets a session.error before t is run again on a fresh
// conversation: it belonged to the attempt that is being dropped.
func (s *opencodeSession) clearTurnErr(t *ocTurn) {
	s.mu.Lock()
	t.errDetail = ""
	s.mu.Unlock()
}

// endTurn ends the in-flight turn exactly once, emitting its trailing
// message and then idle — or the failure. A turn stopped deliberately
// (Interrupt) or by the session closing ends idle, never an error: the
// orchestrator's pause path already recorded why. A turn that ends with
// nothing relayed is a backend that died silently, not a real empty pass,
// and surfaces as a failure so the operator can tell an outage from a
// genuine unclear verdict on sight.
func (s *opencodeSession) endTurn(t *ocTurn, failure error) {
	s.mu.Lock()
	if t.finishing {
		s.mu.Unlock()
		return
	}
	t.finishing = true
	s.turn = nil
	closed := s.closed
	aborted := s.interrupted // stopped by Interrupt: a clean stop, not a failure
	s.interrupted = false
	s.mu.Unlock()
	if text := strings.TrimSpace(t.msg.String()); text != "" {
		s.emit(Event{Kind: EventMessage, Text: text})
	}
	switch {
	case closed:
		// the session was torn down under the turn: an interrupted idle, so
		// the orchestrator's ask machinery sees the turn end — never a
		// failure that would double-report what the teardown already did.
		s.emit(Event{Kind: EventIdle})
		return
	case aborted:
		s.markHadIdle()
		s.emit(Event{Kind: EventIdle})
		return
	}
	if failure != nil {
		s.emit(Event{Kind: EventError, Err: failure})
		return
	}
	if detail := s.turnErrDetail(t); detail != "" {
		// the POST resolved, but the server reported the turn failed
		s.emit(Event{Kind: EventError, Err: &RunFailure{
			Backend: "opencode", FirstTurn: !s.hadIdleValue(), Err: errors.New(detail),
		}})
		return
	}
	if !t.msg.sawAnyValue() {
		s.emit(Event{Kind: EventError, Err: &RunFailure{
			Backend:   "opencode",
			FirstTurn: !s.hadIdleValue(),
			Err:       errors.New("produced no output (backend/gateway may have failed silently)"),
		}})
		return
	}
	s.markHadIdle()
	s.emit(Event{Kind: EventIdle})
}

func (s *opencodeSession) emit(e Event) {
	select {
	case s.raw <- e:
	case <-s.stop:
	}
}

// markHadIdle records that some turn on this session reached a clean
// idle — RunFailure.FirstTurn on a later failure reads the negation of
// this.
func (s *opencodeSession) markHadIdle() {
	s.mu.Lock()
	s.hadIdle = true
	// the handed-in session worked; from here a failure is this session's
	// own, and re-running the turn would repeat real work
	s.resumed = false
	s.mu.Unlock()
}

func (s *opencodeSession) hadIdleValue() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hadIdle
}

// SessionID implements Identified: opencode's own conversation id, read
// from the server session's create response — published so the engine can
// persist it and hand it back to a session that continues this one.
func (s *opencodeSession) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

// Pid implements OSProcess: the session's server process, so a supervisor
// outside this process can find and kill what a card's session owns. 0
// once the server is gone.
func (s *opencodeSession) Pid() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc == nil || s.proc.cmd == nil || s.proc.cmd.Process == nil {
		return 0
	}
	return s.proc.cmd.Process.Pid
}

// procStderr is the server process's bounded stderr, for a diagnostic
// when the server never came up. Empty once the server is gone.
func (s *opencodeSession) procStderr() string {
	s.mu.Lock()
	p := s.proc
	s.mu.Unlock()
	if p == nil || p.stderr == nil {
		return ""
	}
	return strings.TrimSpace(p.stderr.String())
}

// Interrupt stops the in-flight turn server-side: an abort POST of the
// message in flight, its POST's context canceled so the turn ends now
// with the partial work the bus already relayed. Interrupting a running
// compaction cancels the summarize call instead. With nothing running it
// is a no-op — a later turn must not read a stale stop as its own.
func (s *opencodeSession) Interrupt(ctx context.Context) error {
	s.mu.Lock()
	t := s.turn
	cc := s.cancel
	if t != nil || cc != nil {
		s.interrupted = true // mark it a deliberate stop so the turn ends idle
	}
	s.mu.Unlock()
	if t != nil {
		t.cancel() // unblocks the message POST; the turn ends idle
		if id := s.sessionIDValue(); id != "" {
			// the caller's context bounds Interrupt's return, not the
			// abort's delivery: the server-side stop must still go out
			// when the caller returns at once
			go func() {
				actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer acancel()
				_ = s.srv.abort(actx, id)
			}()
		}
		return nil
	}
	if cc != nil {
		cc() // the compaction's summarize call
		if id := s.sessionIDValue(); id != "" && s.srv.v2 {
			// a 2.x compaction runs on after its call: stop it server-side
			go func() {
				actx, acancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer acancel()
				_ = s.srv.abort(actx, id)
			}()
		}
	}
	return nil
}

func (s *opencodeSession) sessionIDValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

// ResolvePermission implements PermissionResolver: it answers the
// server's held tool call. Approve lets this one call run ("once" — never
// a saved rule); deny refuses it, the refusal reaching the model as the
// call's error. The request id names the call whichever session raised
// it — this session's own, or a task child's.
func (s *opencodeSession) ResolvePermission(ctx context.Context, requestID string, approve bool) error {
	reply := "reject"
	if approve {
		reply = "once"
	}
	return s.srv.respond(ctx, s.permissionSession(requestID), requestID, reply)
}

func (s *opencodeSession) interruptedValue() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.interrupted
}

// Close ends the session: the server dies, then the per-session config
// file it was started with is removed. A turn in flight ends interrupted-
// idle first — the bus relays it before the event channel closes — so the
// orchestrator's ask machinery sees the turn end rather than the stream
// just stopping under it.
func (s *opencodeSession) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		t := s.turn
		cc := s.cancel
		s.mu.Unlock()
		if t != nil {
			t.cancel() // the blocked POST returns; the turn ends interrupted-idle
			select {
			case <-t.done:
			case <-time.After(3 * time.Second):
			}
		}
		if cc != nil {
			cc()
		}
		s.srvCancel() // the event bus's GET returns
		close(s.stop) // forward drains raw, then closes events
		if s.proc != nil {
			s.proc.cancel()
			s.proc.wait()
		}
		if s.configPath != "" {
			_ = os.Remove(s.configPath)
		}
		if s.hintsPath != "" {
			_ = os.Remove(s.hintsPath)
		}
		if s.o != nil {
			s.o.forget(s)
		}
	})
	return nil
}

// readBus reads the server's event bus for as long as the session lives,
// mapping each event through the same grammar the per-turn CLI lines went
// through (mapOcEvent), so the events a caller sees do not change with
// the transport. A stream the still-running server dropped under a turn
// is opened again at once and the turn carries on (busResumable). Any
// other stream that ends with a turn in flight — and without an abort or
// a session close — ends that turn as a failure, like today's truncated stream, so the
// orchestrator never advances on partial output. While the server process
// lives the bus is opened again, so a later turn still has one; once it
// has exited there is nothing to reconnect to, and the next turn's
// request fails on it.
func (s *opencodeSession) readBus() {
	quick := 0        // streams in a row lost under a turn within ocBusMinLife of opening
	reopened := false // this stream replaces one dropped under a turn
	for {
		if s.stopped() {
			return
		}
		s.mu.Lock()
		s.disposed = false
		s.mu.Unlock()
		opened := time.Now()
		body, err := s.srv.events(s.sctx)
		connected := err == nil
		if connected {
			if reopened && s.srv.v2 {
				go s.settleExecV2()
			}
			err = s.readStream(body)
			_ = body.Close()
		}
		reopened = false
		if s.stopped() {
			return
		}
		if s.turnInFlight() {
			if time.Since(opened) < ocBusMinLife {
				quick++
			} else {
				quick = 0
			}
			if connected && quick <= ocBusQuickDrops && s.busResumable(err) {
				s.mu.Lock()
				if s.turn != nil {
					s.turn.dropped = true
				}
				s.mu.Unlock()
				reopened = true
				continue
			}
			s.serverDied(err)
		}
		quick = 0
		if _, exited := s.procExit(0); exited {
			return
		}
		select {
		case <-s.stop:
			return
		case <-s.sctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// ocBusMinLife and ocBusQuickDrops bound how often a turn's dropped event
// stream is reopened: a stream lost within ocBusMinLife of opening counts
// as a quick drop, and more than ocBusQuickDrops of them in a row fail the
// turn rather than spin on a server that will not hold one open.
const (
	ocBusMinLife    = time.Second
	ocBusQuickDrops = 3
)

// busResumable reports whether a turn may carry on over a reopened event
// stream after this one ended on readErr. opencode drops the stream now
// and then while it keeps running (an unexpected EOF mid-turn), and
// nothing is lost but the events the gap swallowed: a part that finishes
// after it carries its whole text, and relays what its deltas did not. A
// 1.x turn ends when its message POST does; a 2.x turn ends on a bus
// event the gap could swallow, which settleExecV2 asks the server about
// once the stream is open again. Not so when the server has exited, an
// event was too large to read (it is gone), or the instance was reset
// under the turn.
func (s *opencodeSession) busResumable(readErr error) bool {
	if errors.Is(readErr, bufio.ErrTooLong) {
		return false
	}
	if _, exited := s.procExit(0); exited {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.disposed
}

// readStream reads one open event stream to its end, dispatching each
// event in order on a goroutine of its own. The socket is drained however
// slowly the caller takes events: a 2.x server fails the stream of a
// consumer that falls behind, and a stalled read was one. It returns once
// every event read has been dispatched, with what the read ended on.
func (s *opencodeSession) readStream(body io.Reader) error {
	q := &ocBusQueue{}
	q.cond = sync.NewCond(&q.mu)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			data, ok := q.pop()
			if !ok {
				return
			}
			s.dispatch(data)
		}
	}()
	err := scanSSELines(body, func(data []byte) error {
		q.push(bytes.Clone(data)) // the scanner reuses its buffer
		return nil
	})
	q.close()
	<-drained
	return err
}

// ocBusQueue holds the events read off the stream until they are
// dispatched: unbounded, so the read never waits on the caller.
type ocBusQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  [][]byte
	closed bool
}

func (q *ocBusQueue) push(data []byte) {
	q.mu.Lock()
	q.items = append(q.items, data)
	q.mu.Unlock()
	q.cond.Signal()
}

func (q *ocBusQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Signal()
}

// pop returns the next event, waiting for one; false once the queue is
// closed and empty.
func (q *ocBusQueue) pop() ([]byte, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.items) == 0 {
		return nil, false
	}
	data := q.items[0]
	q.items[0] = nil
	q.items = q.items[1:]
	return data, true
}

// stopped reports whether the session has closed, or is closing.
func (s *opencodeSession) stopped() bool {
	select {
	case <-s.stop:
		return true
	case <-s.sctx.Done():
		return true
	default:
		return false
	}
}

// turnInFlight reports whether a turn is running right now.
func (s *opencodeSession) turnInFlight() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turn != nil
}

// serverDied ends a turn in flight as a failure. The server's event
// stream (or the server itself) is gone: nothing more will be relayed,
// and ending the turn idle would let the orchestrator advance on partial
// output. readErr is what reading the stream ended on (nil for a clean
// close); the failure names why the stream was lost.
func (s *opencodeSession) serverDied(readErr error) {
	s.mu.Lock()
	t := s.turn
	s.mu.Unlock()
	if t != nil {
		s.failTurn(t, s.streamLost(readErr))
	}
}

// ocExitGrace is how long a lost stream waits to learn whether the server
// process exited: its socket closes a moment before it is reaped.
const ocExitGrace = 2 * time.Second

// streamLost is the failure of a turn whose event stream ended under it,
// naming the cause: the server exited (with its status and the tail of its
// stderr), sent an event too large to read, reset its instance (1.x ends
// the stream on that), or failed or closed the stream while running.
func (s *opencodeSession) streamLost(readErr error) *RunFailure {
	rf := &RunFailure{Backend: "opencode", FirstTurn: !s.hadIdleValue()}
	if status, exited := s.procExit(ocExitGrace); exited {
		rf.Err = fmt.Errorf("the opencode server exited mid-turn (%s)", status)
		if tail := stderrTail(s.procStderr(), 5); tail != "" {
			rf.Diagnostic = rf.Err.Error() + "; its last output: " + tail
		}
		return rf
	}
	s.mu.Lock()
	disposed := s.disposed
	s.mu.Unlock()
	switch {
	case errors.Is(readErr, bufio.ErrTooLong):
		rf.Err = fmt.Errorf("the opencode server sent an event over %d MB, larger than gummi reads, so its event stream was lost mid-turn", ocMaxSSEEvent>>20)
	case disposed:
		rf.Err = errors.New("opencode reset its instance for this worktree mid-turn, which ends its event stream (the server is still running)")
	case readErr != nil:
		rf.Err = fmt.Errorf("reading the opencode server's event stream failed mid-turn (the server is still running): %w", readErr)
	default:
		rf.Err = errors.New("the opencode server closed its event stream mid-turn (the server is still running)")
	}
	return rf
}

// procExit reports whether the server process has exited, waiting up to
// grace for it to, and its exit status when it has. A stand-in with no
// process behind it never exits.
func (s *opencodeSession) procExit(grace time.Duration) (string, bool) {
	s.mu.Lock()
	p := s.proc
	s.mu.Unlock()
	if p == nil || p.exited == nil {
		return "", false
	}
	select {
	case <-p.exited:
	default:
		// not yet: wait out the grace (a zero grace is a plain look —
		// selecting on time.After(0) beside exited could miss an exit)
		if grace <= 0 {
			return "", false
		}
		select {
		case <-p.exited:
		case <-time.After(grace):
			return "", false
		}
	}
	if p.cmd != nil && p.cmd.ProcessState != nil {
		return p.cmd.ProcessState.String(), true
	}
	return "status unknown", true
}

// stderrTail is the last n non-blank lines of a process's stderr, joined
// on " | " so they read on one line.
func stderrTail(stderr string, n int) string {
	var lines []string
	for l := range strings.SplitSeq(stderr, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// dispatch maps one event-bus payload into gummi Events.
func (s *opencodeSession) dispatch(data []byte) {
	if s.srv.v2 {
		s.dispatchV2(data)
		return
	}
	var ev ocBusEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return // a keep-alive or something the grammar does not know; ignore quietly
	}
	switch ev.Type {
	case "session.created":
		s.adoptChild(ev.Properties.Info.ID, ev.Properties.Info.ParentID)
	case "server.instance.disposed":
		// the instance this stream is scoped to was torn down, and 1.x
		// ends the stream with it: what a turn's failure then names
		s.mu.Lock()
		s.disposed = true
		s.mu.Unlock()
	case "message.part.updated":
		s.relayPart(ev.Properties.Part)
	case "message.part.delta":
		pr := ev.Properties
		s.relayDelta(pr.SessionID, pr.PartID, pr.Field, pr.Delta)
	case "session.error":
		if ev.Properties.Error == nil || ev.Properties.SessionID != s.sessionIDValue() {
			return // another session's error, or an unshaped one
		}
		if s.interruptedValue() {
			return // the abort's own error: the turn ends idle, not failed
		}
		detail := ev.Properties.Error.Data.Message
		if detail == "" {
			detail = ev.Properties.Error.Name
		}
		if detail == "" {
			detail = "opencode reported an error"
		}
		// During a turn the error becomes part of that turn's one failure
		// (failTurn/endTurn): emitting it here as well failed the run twice,
		// and the POST's opaque 500 that followed overwrote the real cause.
		s.mu.Lock()
		t := s.turn
		folded := t != nil && !t.finishing
		if folded && t.errDetail == "" {
			t.errDetail = detail
		}
		s.mu.Unlock()
		if !folded {
			s.emit(Event{Kind: EventError, Err: errors.New(detail)})
		}
	case "permission.asked":
		// A guarded board's tool-call approval, held server-side until it
		// is answered by its request id. Unlike every other bus event this
		// one is not dropped on a foreign session id — a task child asks
		// under its own, and its held call holds this session's turn with
		// it, so it must surface and be answerable. The turn produced
		// activity the moment it was held, whatever else it relays after.
		s.mu.Lock()
		if t := s.turn; t != nil && !t.finishing {
			t.msg.mark(true)
		}
		s.mu.Unlock()
		s.emit(Event{
			Kind:   EventPermission,
			Tool:   ev.Properties.Permission,
			Detail: strings.Join(ev.Properties.Patterns, ", "),
			CallID: ev.Properties.ID,
		})
	case "question.asked":
		// opencode's question tool is denied in the session config; one
		// that is asked anyway (an operator config that re-enables it)
		// would hold the turn on an answer gummi never gives. Refuse it,
		// so the model sees the call fail and carries on. Like a held
		// permission, a task child's question holds this session's turn
		// too, so the session id is not checked.
		if ev.Properties.ID == "" {
			return
		}
		go func(id string) {
			rctx, cancel := context.WithTimeout(s.sctx, 5*time.Second)
			defer cancel()
			_ = s.srv.rejectQuestion(rctx, id)
		}(ev.Properties.ID)
	}
}

// adoptChild records a server session created under this one, or under
// one of its children: a task subagent whose steps this session pays for.
func (s *opencodeSession) adoptChild(id, parentID string) {
	if id == "" || parentID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if parentID != s.sessionID && !s.children[parentID] {
		return
	}
	if s.children == nil {
		s.children = map[string]bool{}
	}
	s.children[id] = true
}

func (s *opencodeSession) isChild(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.children[id]
}

// relayPart maps one message-part event through the CLI-line grammar when
// the part is one the CLI would have printed, and relays the result. A
// part from another session on this server (a task child's) is not ours
// to surface, but a child's step-finish still carries spend: that is
// relayed as usage alone. So is a step-finish that arrives with no turn
// open — a compaction's summarize call, or a step the turn's end raced
// past — since the spend was run up all the same. A text or reasoning part that is still open (no end time
// yet) only announces its kind: its text streams as message.part.delta
// events (relayDelta), and the finished part relays whatever the deltas
// did not. A tool part is announced when it starts running and resolved
// when it completes or errors.
func (s *opencodeSession) relayPart(p ocPart) {
	if p.SessionID == "" {
		return
	}
	if p.SessionID != s.sessionIDValue() {
		if p.Type == "step-finish" && s.isChild(p.SessionID) {
			s.relaySpend(p)
		}
		return
	}
	var kind string
	switch p.Type {
	case "step-start":
		kind = "step_start"
	case "step-finish":
		kind = "step_finish"
	case "text", "reasoning":
		if p.Time.End == 0 {
			s.mu.Lock()
			if s.partKind == nil {
				s.partKind = map[string]string{}
			}
			s.partKind[p.ID] = p.Type
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		delete(s.partKind, p.ID)
		s.mu.Unlock()
		kind = p.Type
	case "tool":
		switch p.State.Status {
		case "running":
			kind = "tool_running"
		case "completed", "error":
			kind = "tool_use"
		default:
			return // pending: the arguments are not in yet
		}
	default:
		return
	}
	s.mu.Lock()
	t := s.turn
	finishing := t == nil || t.finishing
	s.mu.Unlock()
	if finishing {
		if kind == "step_finish" {
			s.relaySpend(p)
		}
		return
	}
	s.relayEvents(t, s.mapOcEvent(&ocEvent{Type: kind, SessionID: p.SessionID, Part: p}, &t.msg))
}

// relaySpend relays a step-finish's cost alone: no context size, which
// would be another conversation's (a child's, or the history a compaction
// summarized), and no turn activity.
func (s *opencodeSession) relaySpend(p ocPart) {
	if u, ok := s.stepUsage(p); ok {
		s.emit(Event{Kind: EventUsage, Usage: u})
	}
}

// stepUsage is what one step-finish part cost, and whether it reports any.
func (s *opencodeSession) stepUsage(p ocPart) (Usage, bool) {
	tok := p.Tokens
	// Usage's convention: InputTokens is fresh input plus cache writes,
	// CachedTokens the cache reads. opencode reports the three apart.
	u := Usage{
		Model:        s.model,
		InputTokens:  tok.Input + tok.Cache.Write,
		OutputTokens: tok.Output,
		CachedTokens: tok.Cache.Read,
		// opencode prices each step itself from its catalog, so its
		// figure is the metered one even at zero — a free or
		// subscription model must not be re-priced by the engine's
		// token fallback into spend nobody charged.
		Metered: true,
		// opencode cost is USD; gummi credits are $0.01 units.
		Credits: p.Cost * 100,
	}
	return u, u.Credits != 0 || u.InputTokens != 0 || u.OutputTokens != 0 || u.CachedTokens != 0
}

// relayDelta streams one message.part.delta into the turn: a slice of an
// open text or reasoning part's text. The part's relayed length grows by
// it, so the finished part's own event relays only what no delta carried.
func (s *opencodeSession) relayDelta(sessionID, partID, field, delta string) {
	if field != "text" || delta == "" || sessionID == "" || sessionID != s.sessionIDValue() {
		return
	}
	s.mu.Lock()
	kind := s.partKind[partID] // empty for a part never announced open (the prompt's own)
	t := s.turn
	finishing := t == nil || t.finishing
	if kind != "" && !finishing {
		s.partLen[partID] += len(delta)
	}
	s.mu.Unlock()
	if kind == "" || finishing {
		return
	}
	s.relayEvents(t, textEvents(kind, delta, &t.msg))
}

// relayEvents relays one mapping's events inside turn t. They emit
// unconditionally once the dispatch began inside the turn: an event that
// arrived before the turn's end belongs to it, and one the end raced past
// still reaches the caller as its own delta rather than vanishing with
// the turn's final flush.
func (s *opencodeSession) relayEvents(t *ocTurn, evs []Event) {
	t.msg.mark(len(evs) > 0)
	for _, ev := range evs {
		s.emit(ev)
	}
}

// textEvents is the event one slice of streamed text or reasoning maps
// to; text also accumulates into msg for the turn's final message.
// Reasoning closes the prose before it, as a tool call does: the engine
// starts a new bubble after a thought, so text left in msg would be
// written into that next bubble again.
func textEvents(kind, delta string, msg ocMsgText) []Event {
	if delta == "" {
		return nil
	}
	if kind == "reasoning" {
		return append(flushText(msg), Event{Kind: EventReasoningDelta, Text: delta})
	}
	_, _ = msg.WriteString(delta)
	return []Event{{Kind: EventTextDelta, Text: delta}}
}

// flushText finalizes the prose accumulated in msg as its own message and
// resets it, so each stretch of text between a turn's tool calls and
// thoughts maps to one bubble.
func flushText(msg ocMsgText) []Event {
	text := strings.TrimSpace(msg.String())
	msg.Reset()
	if text == "" {
		return nil
	}
	return []Event{{Kind: EventMessage, Text: text}}
}

// ocPart is one message part, as the event bus and the CLI's JSON lines
// both carry it. Only the fields the mapping reads are decoded.
type ocPart struct {
	ID        string  `json:"id"`
	SessionID string  `json:"sessionID"`
	Type      string  `json:"type"`
	Text      string  `json:"text"`
	Tool      string  `json:"tool"`
	CallID    string  `json:"callID"`
	Cost      float64 `json:"cost"`
	Error     string  `json:"error"`
	Reason    string  `json:"reason"` // step-finish: "stop" | "tool-calls" | "length" | …
	Time      struct {
		Start int64 `json:"start"`
		End   int64 `json:"end"`
	} `json:"time"`
	State  ocToolState `json:"state"` // tool parts: arguments, a pre-rendered title, and the outcome
	Tokens struct {
		Input     int64 `json:"input"` // uncached input only; cache reads and writes are below
		Output    int64 `json:"output"`
		Reasoning int64 `json:"reasoning"`
		Cache     struct {
			Read  int64 `json:"read"`
			Write int64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
}

// ocToolState is a tool part's state.
type ocToolState struct {
	Title  string         `json:"title"`
	Input  map[string]any `json:"input"`
	Status string         `json:"status"` // "completed" | "error" on a tool part
	Output string         `json:"output"`
	Error  string         `json:"error"`
}

// ocEvent is one line of the event grammar the mapping reads — the shape
// `opencode run --format json` printed and the server's bus events are
// reduced to. The session id is carried but never trusted for identity:
// it is read from the create response.
type ocEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      ocPart `json:"part"`
}

// ocBusEvent is one event on the server's event bus: a type and the
// properties that type carries. Only the slice the adapter maps is
// decoded.
type ocBusEvent struct {
	Type       string `json:"type"`
	Properties struct {
		SessionID  string   `json:"sessionID"`
		Permission string   `json:"permission"`
		Patterns   []string `json:"patterns"`
		ID         string   `json:"id"`
		Part       ocPart   `json:"part"`
		// session.created: the new session, and the one it was made under
		Info struct {
			ID       string `json:"id"`
			ParentID string `json:"parentID"`
		} `json:"info"`
		// message.part.delta: a slice of an open part's field
		PartID string `json:"partID"`
		Field  string `json:"field"`
		Delta  string `json:"delta"`
		Error  *struct {
			Name string `json:"name"`
			Data struct {
				Message string `json:"message"`
			} `json:"data"`
		} `json:"error"`
	} `json:"properties"`
}

// ocMsgText is the turn-text accumulator the mapping writes into:
// strings.Builder's own surface, so both a plain builder and the
// mutex-guarded one a live turn uses fit.
type ocMsgText interface {
	WriteString(string) (int, error)
	String() string
	Reset()
}

// mapEvent converts one JSON line of the event grammar into zero or more
// gummi Events. The CLI-run lines this once read are gone; it remains the
// line-shaped entry the mapping's own tests drive.
func (s *opencodeSession) mapEvent(line []byte, msg *strings.Builder) []Event {
	var e ocEvent
	if err := json.Unmarshal(line, &e); err != nil {
		return nil // opencode also prints non-event lines; ignore quietly
	}
	return s.mapOcEvent(&e, msg)
}

// mapOcEvent converts one event of the grammar into zero or more gummi
// Events, accumulating assistant text into msg for a final EventMessage.
func (s *opencodeSession) mapOcEvent(e *ocEvent, msg ocMsgText) []Event {
	switch e.Type {
	case "text", "reasoning":
		return textEvents(e.Type, s.partDelta(e.Part.ID, e.Part.Text), msg)
	case "tool_running":
		// the call has its arguments and is running: announce it now, so a
		// long command shows as activity while it runs, not only once it
		// has finished. opencode repeats the running state as the call's
		// metadata moves; the call is announced once.
		if e.Part.Tool == "" || e.Part.CallID == "" || !s.announceCall(e.Part.CallID) {
			return nil
		}
		return s.toolCallEvents(e, msg)
	case "tool", "tool_use":
		if e.Part.Tool == "" {
			return nil
		}
		if s.calledBefore(e.Part.CallID) {
			return toolResultEvents(e)
		}
		return append(s.toolCallEvents(e, msg), toolResultEvents(e)...)
	case "step_finish":
		tok := e.Part.Tokens
		var out []Event
		if u, ok := s.stepUsage(e.Part); ok {
			out = append(out, Event{Kind: EventUsage, Usage: u})
		}
		// the step's whole prompt — fresh, cache-read and cache-written —
		// is the current context size; the window is the catalog's.
		if ctxTok := tok.Input + tok.Cache.Read + tok.Cache.Write; ctxTok > 0 {
			out = append(out, Event{Kind: EventContext, Context: Context{Tokens: ctxTok, Limit: s.contextLimitValue()}})
		}
		// reason="length" means the step hit its max_tokens cap. For a
		// reasoning-capable model this usually presents as "reasoning ate
		// the whole completion budget, output=0" — no visible assistant
		// text emits, and the driver would otherwise see a clean idle
		// with empty output and escalate as "unclear verdict". Surface a
		// legible error instead so the failure mode is diagnosable.
		if e.Part.Reason == "length" && e.Part.Tokens.Output == 0 {
			out = append(out, Event{Kind: EventError, Err: fmt.Errorf(
				"opencode step truncated at output cap (reason=length, reasoning=%d, output=0): "+
					"raise the model's limit.output in opencode.jsonc or reduce reasoning",
				e.Part.Tokens.Reasoning,
			)})
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

// toolCallEvents announces a tool call. The prose accumulated so far is
// flushed as a finalized message BEFORE the tool line (flushText).
// Otherwise the whole turn's text (across every tool call) would be
// emitted as one final EventMessage and the engine would write it into
// the last streamed bubble, duplicating every pre-tool segment. Each
// segment maps to its own bubble.
func (s *opencodeSession) toolCallEvents(e *ocEvent, msg ocMsgText) []Event {
	out := flushText(msg)
	// the salient argument from the part's input, falling back to
	// opencode's own rendered title when the args carry nothing.
	detail := toolDetail(s.workdir, e.Part.State.Input)
	if detail == "" {
		detail = collapseDetail(s.workdir, e.Part.State.Title)
	}
	out = append(out, Event{Kind: EventToolCall, Tool: e.Part.Tool, Detail: detail, CallID: e.Part.CallID})
	return out
}

// toolResultEvents is a finished tool part's outcome. It is reported
// because a refused call is otherwise invisible: a permission denial is
// an "error" part, and a model that retries one is a stage that loops
// until something outside it gives up.
func toolResultEvents(e *ocEvent) []Event {
	switch e.Part.State.Status {
	case "completed":
		return []Event{{
			Kind: EventToolResult, Tool: e.Part.Tool, CallID: e.Part.CallID,
			Result: &ToolResult{OK: true, Output: boundTail(e.Part.State.Output, true)},
		}}
	case "error":
		return []Event{{
			Kind: EventToolResult, Tool: e.Part.Tool, CallID: e.Part.CallID,
			Result: &ToolResult{OK: false, Output: boundTail(e.Part.State.Error, false)},
		}}
	}
	return nil
}

// announceCall records that callID's tool call was announced while
// running, reporting false when it already was.
func (s *opencodeSession) announceCall(callID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.announced[callID] {
		return false
	}
	if s.announced == nil {
		s.announced = map[string]bool{}
	}
	s.announced[callID] = true
	return true
}

// calledBefore reports, once, whether callID's tool call was already
// announced while running — its finished part then carries only the
// outcome.
func (s *opencodeSession) calledBefore(callID string) bool {
	if callID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	was := s.announced[callID]
	delete(s.announced, callID)
	return was
}

// partDelta returns only the new suffix of a streamed part, robust
// whether opencode streams a part incrementally or sends it whole.
func (s *opencodeSession) partDelta(id, full string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.partLen[id]
	s.partLen[id] = len(full)
	if len(full) >= prev {
		return full[prev:]
	}
	return full // part reset unexpectedly
}
