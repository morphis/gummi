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
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/morphis/gummi/internal/childproc"
	"github.com/morphis/gummi/internal/rmtree"
)

// antigravityExecPath locates gummi's own executable when rendering the
// per-session `gummi __mcp` entry in a card home's mcp_config.json, so a
// session's MCP child is a real `gummi __mcp` process rather than
// whatever shadows "gummi" on $PATH. Production uses the real
// os.Executable; tests rebind it.
var antigravityExecPath = os.Executable

// Antigravity is an Agent backed by Google's Antigravity CLI (binary
// `agy`, overridable with GUMMI_ANTIGRAVITY_BIN) in its bidirectional
// stream-json mode:
//
//	agy --input-format stream-json --output-format stream-json \
//	  --dangerously-skip-permissions [--model <id>] [--conversation <id>]
//
// One process per session (cwd = the feature's worktree, HOME = the
// card's redirected home — antigravityhome.go), user turns written as one
// user-event line on stdin, activity read as JSON lines on stdout. A turn
// ends at the result line and the child then waits for the next stdin
// turn — the long-lived stdio shape of the claude adapter. Protocol facts
// (config isolation, auth seeding, resume, usage totals, MCP wiring,
// skill discovery) were verified against agy 1.2.16.
//
// Scope posture, v1: agy's print mode either auto-approves everything
// (--dangerously-skip-permissions) or auto-denies approval-needing tools
// with no surface a human can answer from, so guarded is refused rather
// than run half-guarded; there is no structural tool-stripping, so a
// ReadOnly session is refused rather than run read-write; the stream
// protocol accepts no cancel event, so Interrupt reports false; images
// are not carried.
type Antigravity struct {
	bin string

	mu       sync.Mutex
	homes    map[string]*antigravityHome // card homes by directory
	sessions []*antigravitySession
	closed   bool
}

// NewAntigravity returns an Agent that drives the agy binary (default
// "agy", found on PATH). It fails fast when the binary is missing — the
// codex posture, so a board configured for antigravity fails loudly at
// startup instead of at every stage.
func NewAntigravity(bin string) (*Antigravity, error) {
	if bin == "" {
		bin = "agy"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("antigravity binary %q not found: %w", bin, err)
	}
	return &Antigravity{bin: resolved}, nil
}

// Name implements Agent.
func (a *Antigravity) Name() string { return "antigravity" }

// Capabilities implements Agent. Resume is a real restart-resume: agy
// keeps each conversation under the card home, and the engine hands the
// id back via SessionOpts.ResumeID (`--conversation <id>`). UsageEvents
// is the result line's cumulative usage, emitted as per-turn deltas.
// MCPTools reports that gummi's tools are reached through the card home's
// mcp_config.json, not through SessionOpts.Tools. SkillDirs reports that
// forwarded dirs are symlinked into the card home's skill root before
// the child spawns. WriteCage is cwd-only (agy, like codex, runs with no
// structural path cage). Everything not listed is false: no Interrupt,
// no ClientTools, no ReadOnlyEnforce, no Images, no NativeWatch, no
// Compact.
func (a *Antigravity) Capabilities() Capabilities {
	return Capabilities{Resume: true, UsageEvents: true, MCPTools: true, WriteCage: WriteCageCwd, SkillDirs: true}
}

// AntigravityRateEnv is the operator's token price for an antigravity
// session, in credits per 1k tokens. agy reports only token counts,
// never USD, so without it the engine's default token pricing applies.
const AntigravityRateEnv = "GUMMI_ANTIGRAVITY_CREDITS_PER_1K"

// CreditRate implements Agent: the operator-configured rate, zero when
// unset (the engine's token fallback covers then).
func (a *Antigravity) CreditRate(string) float64 { return AntigravityCreditRate() }

// AntigravityCreditRate is the rate AntigravityRateEnv configures, zero
// when it is unset or not a positive number — the one parse the adapter
// and doctor share.
func AntigravityCreditRate() float64 {
	v := strings.TrimSpace(os.Getenv(AntigravityRateEnv))
	if v == "" {
		return 0
	}
	r, err := strconv.ParseFloat(v, 64)
	if err != nil || r <= 0 {
		return 0
	}
	return r
}

// ModelCatalog implements ModelCataloger: agy's own model list, asked
// live through the same seam opencode's picker uses. The probe runs
// under its own freshly created temp home (INV-1 covers probes), seeded
// from the operator's token, and is removed when it returns.
func (a *Antigravity) ModelCatalog(ctx context.Context) ([]string, error) {
	return AntigravityModelCatalog(ctx, a.bin)
}

// AntigravityModelCatalog is the no-adapter catalog probe, the seam the
// engine's SessionModelCatalog dials for a board that runs no
// antigravity adapter (the opencode precedent). Rebindable so tests can
// stand in for the binary.
var AntigravityModelCatalog = antigravityModelCatalog

// NewSession implements Agent: spawn one agy process in opts.WorkDir
// under the card's redirected home.
func (a *Antigravity) NewSession(_ context.Context, opts SessionOpts) (Session, error) {
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return nil, errors.New("antigravity agent is closed")
	}
	// Refusals happen before any child spawns or home is created — a
	// refused session leaves no side effects behind.
	if opts.Permission == PermissionGuarded {
		return nil, errors.New("antigravity adapter: guarded permissions are not supported " +
			"(agy's print mode either auto-approves everything or auto-denies approval-needing " +
			"tools with no surface for a human); set permissions: allow-all")
	}
	if opts.ReadOnly {
		return nil, errors.New("antigravity backend cannot enforce a read-only research session; " +
			"point this role at `claude` or `opencode`, or accept that autonomous research cannot run on antigravity")
	}
	// The home, lazily: the card home when the session carries a scratch
	// anchor (stage, consult, freeform — resumable kinds), a seeded temp
	// home otherwise (one-shot kinds, doctor probes, tests — they never
	// resume and their home goes at Close).
	home, err := a.homeFor(opts)
	if err != nil {
		return nil, err
	}

	// --disable-slash-commands: without it agy reads a message that
	// starts with "/" as one of its own commands, and in stream-json mode
	// an unknown or interactive one (a chat reply of "/help", a pasted
	// path) fails the turn with an ERROR result instead of reaching the
	// model. Skills the model discovers on its own still load with it
	// set; only the typed expansion goes (verified on agy 1.2.16).
	args := []string{
		"--input-format", "stream-json", "--output-format", "stream-json",
		"--dangerously-skip-permissions", "--disable-slash-commands",
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	// A restored ask, a reattached chat: the conversation id the engine
	// says this session continues. agy's conversations live under the
	// card home, so a resumed id is found there even across restarts.
	if opts.ResumeID != "" {
		args = append(args, "--conversation", opts.ResumeID)
	}

	hints := opts.SystemHints
	if opts.MCPSockPath != "" {
		hints = append(slices.Clip(hints), antigravityMCPHint(opts.MCPSockPath))
	}
	s := &antigravitySession{
		a:         a,
		home:      home,
		workdir:   opts.WorkDir,
		model:     opts.Model,
		hints:     hints,
		sock:      opts.MCPSockPath,
		raw:       make(chan Event, 64),
		events:    make(chan Event),
		stop:      make(chan struct{}),
		readDone:  make(chan struct{}),
		sessionID: opts.ResumeID,
		// A resumed session has no in-memory previous total: agy's
		// resumed init carries only the conversation id (verified on agy
		// 1.2.16), so its first result sets the usage baseline instead of
		// emitting a delta — pre-restart tokens are never re-reported.
		baselined: opts.ResumeID == "",
	}

	// The card home's wiring happens between spawns, under the adapter's
	// mutex, before the child spawns — agy reads mcp_config.json and the
	// skill roster once at startup, so both must be in place first.
	exe, err := antigravityExecPath()
	if err != nil {
		return nil, fmt.Errorf("antigravity adapter: locating own executable: %w", err)
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		s.teardown()
		return nil, errors.New("antigravity agent is closed")
	}
	home.register(opts.MCPSockPath, opts.FeatureID)
	if err := home.writeMCPConfig(exe); err != nil {
		a.mu.Unlock()
		s.teardown()
		return nil, err
	}
	if err := materializeAntigravitySkills(home.dir, opts.SkillDirs); err != nil {
		a.mu.Unlock()
		s.teardown()
		return nil, err
	}
	a.mu.Unlock()

	// Spawn OUTSIDE the lock (fork/exec must not serialize session
	// creation or block a concurrent Close); registration happens after
	// the child is up.
	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, a.bin, args...) //nolint:gosec // bin is operator config (GUMMI_ANTIGRAVITY_BIN), args are gummi-built
	cmd.Dir = opts.WorkDir
	// HOME is the card's redirected home: every file agy writes lands
	// inside it and never inside the operator's real config (INV-1). The
	// tools agy runs inherit that HOME too, so the few settings they need
	// from the real one are pinned back (antigravityToolEnv). The rest of
	// the environment passes through.
	realHome, _ := os.UserHomeDir()
	cmd.Env = antigravityToolEnv(envWithAntigravityHome(os.Environ(), home.dir), realHome)
	// Run the child in its own process group and, on cancel/close, kill
	// the whole group: agy spawns tool subprocesses that would otherwise
	// orphan and keep the stdout pipe open, stalling read()'s EOF.
	childproc.Group(cmd)
	stderr := &capWriter{max: 8 << 10}
	cmd.Stderr = stderr
	// A failure from here to Start leaves no child, but the home already
	// carries this session's MCP entry (and a temp home exists): teardown
	// releases both, or every later spawn on the card would list a dead
	// endpoint.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		s.teardown()
		return nil, fmt.Errorf("antigravity stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		s.teardown()
		return nil, fmt.Errorf("antigravity stdout: %w", err)
	}
	if err := childproc.Start(cmd); err != nil {
		cancel()
		s.teardown()
		return nil, fmt.Errorf("starting agy: %w", err)
	}
	s.cmd, s.cancel, s.stdin, s.stderr = cmd, cancel, stdin, stderr
	go s.forward()
	go s.read(stdout)

	// register under the lock, re-checking closed so a session started
	// concurrently with Close is torn down rather than leaked.
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		_ = s.Close()
		return nil, errors.New("antigravity agent is closed")
	}
	a.sessions = append(a.sessions, s)
	a.mu.Unlock()
	return s, nil
}

// homeFor resolves and prepares the session's redirected home: the card
// home keyed by its directory (created lazily, kept across restarts),
// or a fresh seeded temp home the owning session removes at Close.
//
// A consult session gets a card home of its own beside the stage's. agy
// loads every server a home's mcp_config.json lists, and that file is
// the union of the home's live sessions — so a consult sharing the stage
// home would load the stage's endpoint too, and with it spec_replace and
// submit_verdict, from a conversation whose tool surface is meant to be
// the read-only three.
func (a *Antigravity) homeFor(opts SessionOpts) (*antigravityHome, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if opts.AgentHomeDir == "" {
		return newAntigravityTempHome()
	}
	name, legacy := "agy", "agy-home"
	if opts.Role == RoleConsult {
		name, legacy = "agy-consult", "agy-home-consult"
	}
	dir := filepath.Join(opts.AgentHomeDir, name)
	if opts.ScratchDir != "" {
		dir = adoptLegacyAntigravityHome(filepath.Join(opts.ScratchDir, legacy), dir)
	}
	if a.homes == nil {
		a.homes = map[string]*antigravityHome{}
	}
	if h := a.homes[dir]; h != nil {
		return h, nil
	}
	h, err := newAntigravityHome(dir, false)
	if err != nil {
		return nil, err
	}
	a.homes[dir] = h
	return h, nil
}

// antigravityMCPHint tells the session where its gummi tools are. agy
// reaches every MCP tool through its own call_mcp_tool, and a session
// told only the tool names spent its first turns listing and reading its
// home's MCP cache to learn the server's name.
func antigravityMCPHint(sockPath string) string {
	return "Your gummi tools are served by the MCP server `" + gummiMCPEntryName(sockPath) +
		"`. Call them with call_mcp_tool, ServerName set to that server; " +
		"there is no need to look them up on disk first."
}

// envWithAntigravityHome returns env with HOME redirected — the one
// variable that decides where agy's config tree lives.
func envWithAntigravityHome(env []string, home string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, "HOME=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HOME="+home)
}

// antigravityToolEnv pins back to the operator's real home the settings
// that the tools an agy child runs would otherwise resolve under the
// redirected HOME. Without them, every card works as a fresh user:
//
//   - git has no global config, so the commit an implementer is told to
//     make fails with "Author identity unknown" (or the agent writes an
//     identity into the repo's own config), and a signing setup is lost;
//   - gpg has no keyring for a commit.gpgsign the global config asks for;
//   - Go starts with an empty module and build cache per card — gigabytes
//     per card, cold every time — and ignores the operator's `go env -w`
//     settings (GOPROXY, GOPRIVATE, …).
//
// XDG_CONFIG_HOME and XDG_CACHE_HOME are deliberately not pinned: agy
// reads both itself, and pointing them at the real home would let it
// write there. Each pin is the tool's own variable instead, set only when
// the operator has not set it and only to a location that exists (or,
// for the Go caches, to where Go itself would put them). ssh needs no pin
// — it finds ~/.ssh through the passwd entry, not $HOME.
func antigravityToolEnv(env []string, realHome string) []string {
	if realHome == "" {
		return env
	}
	set := make(map[string]bool, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && v != "" {
			set[k] = true
		}
	}
	exists := func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}
	pin := func(k, v string) {
		if !set[k] && v != "" {
			env = append(env, k+"="+v)
			set[k] = true
		}
	}

	// git reads ~/.gitconfig and, when XDG_CONFIG_HOME is unset,
	// ~/.config/git/config; GIT_CONFIG_GLOBAL names exactly one, so take
	// the first that exists. An operator-set XDG_CONFIG_HOME already
	// passes through and git finds its file there itself.
	if p := filepath.Join(realHome, ".gitconfig"); exists(p) {
		pin("GIT_CONFIG_GLOBAL", p)
	} else if p := filepath.Join(realHome, ".config", "git", "config"); !set["XDG_CONFIG_HOME"] && exists(p) {
		pin("GIT_CONFIG_GLOBAL", p)
	}
	if p := filepath.Join(realHome, ".gnupg"); exists(p) {
		pin("GNUPGHOME", p)
	}

	// Go: GOENV first, so the operator's `go env -w` file is read; then
	// the defaults Go derives from HOME, unless that file sets them.
	var goenvKeys map[string]bool
	goenv := filepath.Join(realHome, ".config", "go", "env")
	if set["XDG_CONFIG_HOME"] || !exists(goenv) {
		goenv = ""
	}
	if goenv != "" {
		pin("GOENV", goenv)
		goenvKeys = goEnvFileKeys(goenv)
	}
	if !goenvKeys["GOPATH"] {
		pin("GOPATH", filepath.Join(realHome, "go"))
	}
	if !set["XDG_CACHE_HOME"] {
		cache := filepath.Join(realHome, ".cache")
		if !goenvKeys["GOCACHE"] {
			pin("GOCACHE", filepath.Join(cache, "go-build"))
		}
		pin("GOLANGCI_LINT_CACHE", filepath.Join(cache, "golangci-lint"))
	}
	return env
}

// goEnvFileKeys reads the variable names a `go env -w` file sets.
func goEnvFileKeys(path string) map[string]bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	keys := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, _, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k != "" {
			keys[k] = true
		}
	}
	return keys
}

// Close implements Agent: end every live session (temp homes with
// them). The roster is taken under the lock and drained before the
// closes run — a session's own Close unregisters itself, which would
// deadlock against a Close still holding the lock.
func (a *Antigravity) Close() error {
	a.mu.Lock()
	a.closed = true
	sessions := a.sessions
	a.sessions = nil
	a.mu.Unlock()
	for _, s := range sessions {
		_ = s.Close()
	}
	return nil
}

// agyUsage is a usage block agy reports on a result line: cumulative
// conversation totals (a running total through the turn, the
// conversation total across turns). Verified on agy 1.2.16:
// input_tokens is the fresh input side (cache reads are reported
// separately), output_tokens already carries the thinking tokens inside
// it, and total_tokens is input+output — 11838+57=11895.
type agyUsage struct {
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ThinkingTokens  int64 `json:"thinking_tokens"`
	CacheReadTokens int64 `json:"cache_read_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
}

// totals projects the wire shape onto the fields the delta math uses:
// [fresh input, cache reads, output].
func (u agyUsage) totals() [3]int64 {
	return [3]int64{u.InputTokens, u.CacheReadTokens, u.OutputTokens}
}

// antigravitySession is one live agy conversation: one long-lived child
// process, turns framed on stdin, events streamed off stdout.
type antigravitySession struct {
	a      *Antigravity
	home   *antigravityHome // the redirected HOME; temp homes removed at Close
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stdin  io.WriteCloser
	stderr *capWriter // bounded tail of the child's stderr, for crash diagnostics

	workdir string // opts.WorkDir, for repo-relative tool-call details
	model   string // opts.Model, refined by init/result model ids when they arrive
	hints   []string
	primed  bool
	sock    string // this session's MCP endpoint socket, for its union entry

	raw      chan Event
	events   chan Event
	stop     chan struct{}
	readDone chan struct{} // closed when read() has finished draining stdout

	wmu       sync.Mutex // serializes writes to stdin
	closeOnce sync.Once
	waitOnce  sync.Once // guards the single cmd.Wait() shared by read() and Close()
	waitErr   error

	mu        sync.Mutex
	inTurn    bool   // a Send is unanswered by a result line
	sessionID string // the conversation id from init (or the handed-in ResumeID)

	// Metering state, owned exclusively by the read goroutine. baseline
	// is the cumulative usage the last settled result reported; a
	// resumed session's first result sets it instead of emitting a delta.
	baseline  [3]int64
	baselined bool

	// Tool steps of the in-flight turn, read-goroutine-owned: names by
	// id (ACTIVE → DONE pairing), and the DONE steps buffered until the
	// result line, whose denied_actions decide their final ok.
	toolNames map[string]string
	doneSteps []agyDoneStep
	hadIdle   bool // some turn reached a clean idle (RunFailure.FirstTurn reads ¬this)
}

// agyDoneStep is one completed tool step of the in-flight turn, held
// until the result line says whether its action was denied.
type agyDoneStep struct {
	id     string
	name   string
	detail string // for a call the stream never announced ACTIVE
	call   bool   // the call event must be emitted ahead of the result (the stream only ever said DONE)
	output string
}

// agyUsageDelta folds one result's cumulative usage into the session's
// baseline and returns the per-turn delta to emit. A fresh session
// baselines at zero, so its first result emits that turn's own usage; a
// resumed session's first post-resume result SETS the baseline and
// emits no delta — agy's resumed init carries no usage figure, so
// pre-restart tokens cannot be separated from the resumed turn's own
// and are never re-reported (that turn's own usage folds into the
// baseline, an undercount, never a double-count); every later result
// emits this-total minus baseline.
func (s *antigravitySession) agyUsageDelta(u agyUsage) (Usage, bool) {
	tot := u.totals()
	if !s.baselined {
		s.baseline, s.baselined = tot, true
		return Usage{}, false
	}
	in, cached, out := tot[0]-s.baseline[0], tot[1]-s.baseline[1], tot[2]-s.baseline[2]
	s.baseline = tot
	if cached < 0 {
		cached = 0
	}
	if in < 0 {
		in = 0
	}
	if out < 0 {
		out = 0
	}
	if in == 0 && cached == 0 && out == 0 {
		return Usage{}, false
	}
	return Usage{
		InputTokens:  in,
		CachedTokens: cached,
		OutputTokens: out,
		Model:        s.model,
	}, true
}

// modelForUsage keeps the last model id the stream named, so a result's
// usage lands on the model that produced it (agy's ids carry effort —
// gemini-3.1-pro-high — and pass through verbatim).
func (s *antigravitySession) setStreamModel(id string) {
	if id != "" {
		s.model = id
	}
}

// agyLine is one stdout line's envelope; only the fields the adapter
// reads. `event` is the discriminator agy's stream-json writes; `type`
// is tolerated alongside it. Verified against agy 1.2.16: init carries
// the conversation id (and a tools roster the adapter ignores);
// step_update's payload sits under a key of its own name; result's
// payload is nested under "result".
type agyLine struct {
	Event string `json:"event"`
	Type  string `json:"type"`

	// init
	ConversationID string `json:"conversation_id"`
	SessionID      string `json:"session_id"`
	Model          string `json:"model"`

	// step_update: the step payload under the key of the event's own
	// name ("step"), tolerated for drift.
	Step    json.RawMessage `json:"step_update"`
	StepAlt json.RawMessage `json:"step"`

	// result: the nested result object ("result"), tolerated flat.
	Result        json.RawMessage   `json:"result"`
	Status        string            `json:"status"`
	Reply         string            `json:"reply"`
	Text          string            `json:"text"`
	Usage         json.RawMessage   `json:"usage"`
	Error         json.RawMessage   `json:"error"`
	DeniedActions []json.RawMessage `json:"denied_actions"`
}

// agyStep is one step inside a step_update line (agy 1.2.16): steps
// carry no id — they are indexed per conversation — an agent_response
// streams its text in text_delta, and a tool names itself in
// tool_name/tool_info. user_input steps (the turn's own echo) are
// ignored.
type agyStep struct {
	Index        int             `json:"step_index"`
	Type         string          `json:"step_type"`
	State        string          `json:"state"`
	TextDelta    string          `json:"text_delta"`
	ToolName     string          `json:"tool_name"`
	ToolInfo     json.RawMessage `json:"tool_info"`
	SubagentInfo json.RawMessage `json:"subagent_info"`
}

// agyToolInfo is a tool step's tool_info: the invocation's parameters
// (a name→value map) at ACTIVE, its output at DONE.
type agyToolInfo struct {
	Name       string          `json:"name"`
	Parameters json.RawMessage `json:"parameters"`
	Output     json.RawMessage `json:"output"`
}

// agyResult is the nested result object (agy 1.2.16): the turn's status,
// full reply text, and the conversation's CUMULATIVE usage (the sum of
// the turn's per-step figures on a fresh session).
type agyResult struct {
	ConversationID string            `json:"conversation_id"`
	Status         string            `json:"status"`
	Response       string            `json:"response"`
	Usage          json.RawMessage   `json:"usage"`
	Error          json.RawMessage   `json:"error"`
	DeniedActions  []json.RawMessage `json:"denied_actions"`
}

// mapLine converts one stdout line into zero or more gummi Events and
// advances the metering state. It runs only on the read goroutine, so
// its fields need no lock. Unknown events and malformed lines are
// dropped — the protocol is unversioned and drifts; only a dead child
// or an ERROR result fails the session.
func (s *antigravitySession) mapLine(line []byte) []Event {
	var l agyLine
	if err := json.Unmarshal(line, &l); err != nil {
		return nil
	}
	switch kind := cmp.Or(l.Event, l.Type); kind {
	case "init":
		s.mu.Lock()
		if s.sessionID == "" {
			s.sessionID = cmp.Or(l.ConversationID, l.SessionID)
		}
		s.mu.Unlock()
		s.setStreamModel(l.Model)
		return nil
	case "step_update":
		return s.mapStep(firstNonEmptyJSON(l.Step, l.StepAlt))
	case "result":
		return s.mapResult(&l)
	default:
		return nil
	}
}

// mapStep handles one step_update: agent_response deltas while ACTIVE,
// tool calls announced at ACTIVE and completed at DONE.
func (s *antigravitySession) mapStep(raw json.RawMessage) []Event {
	if len(raw) == 0 {
		return nil
	}
	var st agyStep
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil
	}
	switch st.Type {
	case "agent_response":
		return s.mapAgentResponse(st)
	case "tool", "subagent":
		return s.mapToolStep(st)
	default:
		return nil
	}
}

// mapAgentResponse surfaces an in-flight reply's text deltas. Only an
// ACTIVE agent_response streams (a DONE step re-states no delta; the
// result's response is the turn's full text).
func (s *antigravitySession) mapAgentResponse(st agyStep) []Event {
	if st.State != "" && st.State != "ACTIVE" {
		return nil
	}
	if st.TextDelta == "" {
		return nil
	}
	return []Event{{Kind: EventTextDelta, Text: st.TextDelta}}
}

// agyDetailKeys is agy's own parameter vocabulary, in the order an
// activity line wants it: the command line, then the file or directory a
// tool reads or writes, then what a search looks for. agy spells these in
// CamelCase, which the shared detailKeys never match, and without them
// the line fell back to the alphabetically first string — the server
// name of every MCP call, a search's directory instead of its query.
var agyDetailKeys = []string{
	"CommandLine",
	"TargetFile", "AbsolutePath", "DirectoryPath", "File", "Path",
	"Query", "Pattern", "Url", "URL",
	"SearchPath", "SearchDirectory",
}

// agyArgDetail renders a tool step's salient argument from its
// tool_info.parameters (or a subagent step's subagent_info): the
// run_command's command line, the file tool's target path, the gummi
// tool an MCP call invokes. agy's own keys go first, then the shared
// probe, then the first string value the parameters carry.
func agyArgDetail(workdir string, st agyStep) string {
	if len(st.SubagentInfo) > 0 {
		var m map[string]any
		if err := json.Unmarshal(st.SubagentInfo, &m); err == nil {
			if d := agyParamDetail(workdir, "", m); d != "" {
				return d
			}
		}
	}
	if len(st.ToolInfo) == 0 {
		return ""
	}
	var ti agyToolInfo
	if err := json.Unmarshal(st.ToolInfo, &ti); err != nil {
		return ""
	}
	if len(ti.Parameters) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(ti.Parameters, &m); err != nil {
		return ""
	}
	return agyParamDetail(workdir, cmp.Or(st.ToolName, ti.Name), m)
}

// agyParamDetail picks the one parameter worth showing for tool.
// call_mcp_tool is how agy reaches every MCP tool — gummi's included —
// and its ServerName is the same on every call, so the line names the
// tool it calls.
func agyParamDetail(workdir, tool string, m map[string]any) string {
	if tool == "call_mcp_tool" {
		if v, ok := m["ToolName"].(string); ok && strings.TrimSpace(v) != "" {
			return collapseDetail(workdir, v)
		}
	}
	for _, k := range agyDetailKeys {
		if v, ok := m[k].(string); ok {
			if d := collapseDetail(workdir, v); d != "" {
				return d
			}
		}
	}
	if d := toolDetail(workdir, m); d != "" {
		return d
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return collapseDetail(workdir, v)
		}
	}
	return ""
}

// agyToolName resolves a tool step's name: tool_name, or tool_info's.
func agyToolName(st agyStep) string {
	if st.ToolName != "" {
		return st.ToolName
	}
	var ti agyToolInfo
	if err := json.Unmarshal(st.ToolInfo, &ti); err == nil && ti.Name != "" {
		return ti.Name
	}
	if st.Type == "subagent" {
		return "invoke_subagent"
	}
	return ""
}

// mapToolStep announces a tool call at ACTIVE and buffers its DONE —
// the result line's denied_actions decide the final ok, so a
// completion's result event is emitted once those are known, never
// swallowed. Steps carry no id: their per-conversation step_index is the
// call id the call/result pair share, stringified verbatim — index 0 is
// as valid an id as 3, so nothing may conflate it with an absent index.
func (s *antigravitySession) mapToolStep(st agyStep) []Event {
	detail := agyArgDetail(s.workdir, st)
	name := agyToolName(st)
	id := strconv.Itoa(st.Index)
	switch st.State {
	case "ACTIVE", "":
		if name == "" {
			return nil
		}
		if s.toolNames == nil {
			s.toolNames = map[string]string{}
		}
		s.toolNames[id] = name
		return []Event{{Kind: EventToolCall, Tool: name, Detail: detail, CallID: id}}
	case "DONE":
		output := agyStepOutput(firstNonEmptyJSON(st.ToolInfo, st.SubagentInfo))
		prev, announced := s.toolNames[id]
		delete(s.toolNames, id)
		name = cmp.Or(name, prev)
		s.doneSteps = append(s.doneSteps, agyDoneStep{
			id: id, name: name, detail: detail, call: !announced,
			output: output,
		})
		return nil
	default:
		return nil
	}
}

// agyStepOutput extracts a DONE tool step's captured output from its
// tool_info, which may carry a plain string or a structured value. A
// tool_info with no output is no output: echoing the object back would
// show the call's own name and parameters where its result belongs.
func agyStepOutput(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var ti agyToolInfo
	if err := json.Unmarshal(raw, &ti); err == nil && (ti.Name != "" || len(ti.Parameters) > 0 || len(ti.Output) > 0) {
		return agyStepOutput(ti.Output)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

// mapResult settles the turn: the buffered tool steps land as tool
// results with ok = not-in-denied_actions (a denied action surfaces
// there and never as a swallowed silence), the result's reply text lands
// as the turn's full assistant message, the cumulative usage becomes the
// turn's delta, and the turn ends idle. A status ERROR is a failed turn:
// EventError wrapping a RunFailure that carries the result's error
// message and agy's own stderr tail — the session itself stays alive and
// closable.
func (s *antigravitySession) mapResult(l *agyLine) []Event {
	var r agyResult
	if len(l.Result) > 0 {
		_ = json.Unmarshal(l.Result, &r)
	}
	status := cmp.Or(r.Status, l.Status)
	reply := cmp.Or(r.Response, l.Reply, l.Text)
	usageRaw := firstNonEmptyJSON(r.Usage, l.Usage)
	errRaw := firstNonEmptyJSON(r.Error, l.Error)
	deniedRaw := r.DeniedActions
	if deniedRaw == nil {
		deniedRaw = l.DeniedActions
	}

	denied := agyDeniedSet(deniedRaw)
	var out []Event
	for _, d := range s.doneSteps {
		ok := !denied[d.id] && !denied[d.name]
		if d.call {
			out = append(out, Event{Kind: EventToolCall, Tool: d.name, Detail: d.detail, CallID: d.id})
		}
		out = append(out, Event{
			Kind: EventToolResult, Tool: d.name, CallID: d.id,
			Result: &ToolResult{OK: ok, Output: boundTail(d.output, ok)},
		})
	}
	s.doneSteps = nil

	if strings.TrimSpace(reply) != "" {
		out = append(out, Event{Kind: EventMessage, Text: reply})
	}

	if len(usageRaw) > 0 {
		var usage agyUsage
		if json.Unmarshal(usageRaw, &usage) == nil {
			s.setStreamModel(l.Model)
			if u, ok := s.agyUsageDelta(usage); ok {
				out = append(out, Event{Kind: EventUsage, Usage: u})
			}
		}
	}

	s.mu.Lock()
	s.inTurn = false
	s.mu.Unlock()

	if strings.EqualFold(status, "ERROR") {
		s.stderr.settle(20*time.Millisecond, 200*time.Millisecond)
		detail := strings.TrimSpace(rawMessageText(errRaw))
		if tail := strings.TrimSpace(s.stderr.String()); tail != "" {
			if detail != "" {
				detail += "\n"
			}
			detail += tail
		}
		// A stale token copy: repair it before the error surfaces, so
		// the next session's spawn starts from the operator's current
		// token (INV-7 — the failure is reported, never retried
		// silently).
		if antigravityAuthFailure(detail) {
			_ = reseedAntigravityToken(s.home.dir)
		}
		return append(out, Event{Kind: EventError, Err: &RunFailure{
			Backend:    "antigravity",
			Diagnostic: boundTail(detail, false),
			FirstTurn:  !s.hadIdle,
			Err:        fmt.Errorf("turn failed (%s)", cmp.Or(status, "error")),
		}})
	}
	s.hadIdle = true
	return append(out, Event{Kind: EventIdle})
}

// firstNonEmptyJSON returns the first payload that is present.
func firstNonEmptyJSON(a, b json.RawMessage) json.RawMessage {
	if len(a) > 0 {
		return a
	}
	return b
}

// agyDeniedSet normalizes a result's denied_actions into the set of
// identifiers (step ids and action names) it names.
func agyDeniedSet(raw []json.RawMessage) map[string]bool {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]bool, len(raw))
	for _, r := range raw {
		var s string
		if err := json.Unmarshal(r, &s); err == nil && s != "" {
			out[s] = true
			continue
		}
		var m struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Tool string `json:"tool"`
		}
		if err := json.Unmarshal(r, &m); err == nil {
			out[m.ID] = out[m.ID] || m.ID != ""
			out[m.Name] = out[m.Name] || m.Name != ""
			out[m.Tool] = out[m.Tool] || m.Tool != ""
		}
	}
	return out
}

// Pid implements agent.OSProcess: cmd is set once at construction and
// never reassigned, so this needs no lock.
func (s *antigravitySession) Pid() int {
	if s.cmd == nil || s.cmd.Process == nil {
		return 0
	}
	return s.cmd.Process.Pid
}

// SessionID implements Identified: agy's conversation id, learned from
// the init line (or carried from the engine's ResumeID before the child
// has said anything). The engine hands it back as SessionOpts.ResumeID,
// which turns into `--conversation <id>` — and because agy's
// conversations live under the card home, that id is still there after a
// restart.
func (s *antigravitySession) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

func (s *antigravitySession) Events() <-chan Event { return s.events }

// forward owns events: it copies raw→events and closes events exactly
// once (the read goroutine and Send never touch events directly).
func (s *antigravitySession) forward() {
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

// read scans the child's stdout, maps each JSON line to events, and
// feeds the forwarder until stdout closes.
func (s *antigravitySession) read(stdout io.Reader) {
	defer close(s.readDone)
	sc := bufio.NewScanner(stdout)
	// result lines embed the reply; 8 MiB matches the other stream adapters.
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
	// stdout closed. A clean EOF is NOT an idle: turns end at result
	// lines and the process stays alive between them, so EOF while we
	// aren't tearing down means the process died mid-session.
	scanErr := sc.Err()
	if scanErr != nil {
		// scanner aborted (e.g. a line over the buffer cap): the child may
		// still be running, blocked writing to the undrained pipe. Kill
		// the process group first or reap()'s Wait would deadlock.
		s.cancel()
	}
	waitErr := s.reap()
	if s.stopping() {
		return
	}
	err := waitErr
	if err == nil {
		err = errors.New("process exited unexpectedly")
	}
	rf := &RunFailure{
		Backend:    "antigravity",
		Diagnostic: strings.TrimSpace(s.stderr.String()),
		FirstTurn:  !s.hadIdle,
		Err:        err,
	}
	// An auth failure's token copy may simply be stale (the operator
	// re-logged in since the card home was seeded): repair it before the
	// error surfaces, so the next session's spawn starts from the
	// operator's current token. Never silent — the RunFailure still
	// reports the failure that triggered this.
	if diag := rf.Diagnostic; antigravityAuthFailure(diag) {
		_ = reseedAntigravityToken(s.home.dir)
	}
	select {
	case s.raw <- Event{Kind: EventError, Err: rf}:
	case <-s.stop:
	}
}

// reap waits for the child exactly once (read() reaps a self-exited
// child; Close reaps a killed one) and caches the exit status.
func (s *antigravitySession) reap() error {
	s.waitOnce.Do(func() { s.waitErr = s.cmd.Wait() })
	return s.waitErr
}

// stopping reports whether Close has begun tearing the session down, so
// a kill-induced stdout EOF isn't misreported as a child crash.
func (s *antigravitySession) stopping() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

func (s *antigravitySession) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	s.wmu.Lock()
	defer s.wmu.Unlock()
	// The stdin pipe is an *os.File and supports write deadlines; bound
	// the write so a child that stopped reading can't block us
	// indefinitely (a wedged child must not hang the engine's pump).
	if f, ok := s.stdin.(interface{ SetWriteDeadline(time.Time) error }); ok {
		_ = f.SetWriteDeadline(time.Now().Add(headlessWriteTimeout))
	}
	_, err = s.stdin.Write(b)
	return err
}

// agyUserFrame is a user turn on the wire: exactly the line
// {"event":"user","message":{"content":"<msg>"}}.
type agyUserFrame struct {
	Event   string         `json:"event"`
	Message agyUserMessage `json:"message"`
}

type agyUserMessage struct {
	Content string `json:"content"`
}

func (s *antigravitySession) Send(_ context.Context, msg string) error {
	return s.SendTurn(context.Background(), Turn{Text: msg})
}

// SendTurn implements ImageSender only to refuse: agy carries no images
// (Capabilities().Images is false), and a Turn carrying them must get
// ErrImagesUnsupported rather than a silent drop of the attachments.
func (s *antigravitySession) SendTurn(_ context.Context, turn Turn) error {
	if len(turn.Images) > 0 {
		return ErrImagesUnsupported
	}
	msg := turn.Text
	// The stage/consult hints ride the first turn's frame — the same
	// prepend the codex adapter does for a backend with no
	// system-prompt flag — so a session that has never spoken still
	// receives the contract it was started with.
	s.mu.Lock()
	if !s.primed && len(s.hints) > 0 {
		msg = strings.Join(s.hints, "\n\n") + "\n\n" + msg
	}
	s.primed = true
	s.inTurn = true
	s.mu.Unlock()
	err := s.write(agyUserFrame{Event: "user", Message: agyUserMessage{Content: msg}})
	if err != nil {
		s.mu.Lock()
		s.inTurn = false
		s.mu.Unlock()
	}
	return err
}

// Interrupt is not supported: agy's stream protocol accepts no cancel
// event (verified on agy 1.2.16 — the cancel input is rejected), so the
// board's stop control waits for the turn to end. Closing the session
// still kills the child's process group.
func (s *antigravitySession) Interrupt(_ context.Context) error {
	return errors.New("antigravity has no mid-turn interrupt; close the session to stop it")
}

// Close ends the session: kill the child's process group, join the read
// goroutine, reap, unregister the MCP entry, and remove a temp home.
// The card home stays — its conversations are what resume is made of.
func (s *antigravitySession) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()    // SIGKILL the process group → stdout EOF → read()'s Scan ends
		close(s.stop) // stop forward; unblock read()'s raw sends
		_ = s.stdin.Close()
		// join the read goroutine before Wait: reading a StdoutPipe after
		// Wait closes it is a documented error (bounded — cancel() EOFs it).
		select {
		case <-s.readDone:
		case <-time.After(3 * time.Second):
		}
		_ = s.reap() // reap (read() may already have)
		s.unregister()
		if s.home.temp {
			_ = rmtree.RemoveAll(s.home.dir)
		}
	})
	return nil
}

// teardown releases everything a never-spawned session holds — the home
// registration and a temp home — when NewSession fails after preparing
// them.
func (s *antigravitySession) teardown() {
	s.unregister()
	if s.home != nil && s.home.temp {
		_ = rmtree.RemoveAll(s.home.dir)
	}
}

// unregister drops the session from the adapter's roster and from its
// home's MCP union.
func (s *antigravitySession) unregister() {
	a := s.a
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, s2 := range a.sessions {
		if s2 == s {
			a.sessions = append(a.sessions[:i], a.sessions[i+1:]...)
			break
		}
	}
	if s.home != nil {
		s.home.unregister(s.sock)
	}
}
