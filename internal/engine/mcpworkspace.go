package engine

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/cardmint"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/mcp"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// workspaceMCPSockPath is the process-lifetime, board-level socket a
// hosted agent's `gummi __mcp --workspace` child dials — the counterpart
// to mcpSockPath, but pid-and-nonce-suffixed rather than
// feature-suffixed.
//
// The suffix earns its keep for a reason mcpSockPath doesn't have to
// worry about. mcpSockPath's per-feature path is unique per card and
// guarded by that card's per-card lock, so two processes legitimately
// binding it at once can't happen. A workspace endpoint has no such
// guard — every gummi board process wants one — so a single fixed
// "workspace.sock" would let a second board silently steal the first's
// endpoint via the same stale-socket os.Remove that
// StartWorkspaceMCPEndpoint (like startMCPEndpoint) does before Listen.
// The pid half makes that collision structurally impossible between two
// live *processes*: they never share a pid. The random nonce half covers
// the case pid alone can't: more than one Engine bound to the same
// workspace inside one process (tests do this; nothing rules out a future
// caller doing it too), where two StartWorkspaceMCPEndpoint calls would
// otherwise compute the identical pid-only path and the second bind would
// steal the first's socket via the very same os.Remove. A stale file
// surviving under a *recycled* pid-and-nonce pair from a past crash is the
// same vanishingly-rare case mcpSockPath already accepts for the
// per-feature path, and it degrades the same way — remove and rebind.
func workspaceMCPSockPath(w state.Workspace, nonce string) string {
	path := filepath.Join(w.StateDir(), "mcp", fmt.Sprintf("ws-%d-%s.sock", os.Getpid(), nonce))
	if len(path) <= unixPathMax {
		return path
	}
	sum := sha256.Sum256([]byte(path))
	return filepath.Join(os.TempDir(), fmt.Sprintf("gummi-mcp-ws-%x.sock", sum[:6]))
}

// workspaceMCPNonce returns a short random hex string distinguishing one
// StartWorkspaceMCPEndpoint call from another — see workspaceMCPSockPath.
// Falls back to a timestamp if the system random source is somehow
// unavailable: any distinguishing value is enough here, this is not
// security-sensitive, and a bind that still collides just fails loudly
// (Listen returns an error) rather than corrupting anything.
func workspaceMCPNonce() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// workspaceEndpoint is the process-lifetime counterpart to mcpEndpoint.
// Where mcpEndpoint scopes one live feature's session tools to one stage
// session, workspaceEndpoint scopes gummi's board-level verbs — list the
// backlog, inspect a card, drive one — to the whole running engine. It is
// bound once, when the TUI starts (alongside AttachEngine), and torn down
// once, when the TUI exits; nothing stashes its teardown on a Session,
// because it doesn't belong to one.
//
// Its tools answer directly against the engine's control-plane API
// (cfg.Store, the worktree pool, Run/RunWith) rather than bridging into a
// single live *Session's handleClientTool path the way mcpEndpoint does —
// there is no one session to bridge into here, only the engine itself.
// This is also why it carries no readOnly flag: filterReadOnlyTools exists
// to strip artifact-mutating stage tools from a research session's
// surface, and none of the board-level tools below touch the artifact
// that way (card_run/card_resume kick off a session; they don't write to
// it directly).
//
// Its hello handshake is {"mode":"workspace"}, not {"feature":"<id>"}:
// there is no feature id to validate a connection against, so overloading
// the feature endpoint's hello with a sentinel feature value would be a
// worse fit than a handshake that says what it actually checks.
type workspaceEndpoint struct {
	engine *Engine
	ln     net.Listener

	ctx    context.Context
	cancel context.CancelFunc

	wg     sync.WaitGroup
	connMu sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

// StartWorkspaceMCPEndpoint binds and serves the board-level tool-call
// socket for the life of this engine. Exported because, unlike the
// per-feature endpoint (bound internally from newAgentSession as a stage
// session starts), the workspace endpoint is started once from outside
// this package — by the TUI at startup — and torn down once at shutdown.
// The listener is accepting before this returns, so a hosted-agent child
// spawned right after with GUMMI_MCP_SOCK set to the returned path can
// dial immediately without racing the bind. The returned teardown is
// single-shot and idempotent, mirroring startMCPEndpoint's contract:
// cancel in-flight dispatches, close the listener, join every goroutine,
// remove the socket file.
func (e *Engine) StartWorkspaceMCPEndpoint() (string, func(), error) {
	path := workspaceMCPSockPath(e.cfg.Workspace, workspaceMCPNonce())
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("workspace mcp socket dir: %w", err)
	}
	// see workspaceMCPSockPath: the pid suffix already makes this path
	// unique among live processes, so removing a stale file here only
	// ever clears this same pid's own leftover from a past crash.
	_ = os.Remove(path)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", path)
	if err != nil {
		return "", nil, fmt.Errorf("workspace mcp listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return "", nil, fmt.Errorf("workspace mcp chmod %s: %w", path, err)
	}
	epCtx, epCancel := context.WithCancel(context.Background())
	ep := &workspaceEndpoint{
		engine: e,
		ln:     ln,
		ctx:    epCtx,
		cancel: epCancel,
		conns:  map[net.Conn]struct{}{},
	}
	ep.wg.Add(1)
	go func() {
		defer ep.wg.Done()
		ep.acceptLoop()
	}()
	var once sync.Once
	teardown := func() {
		once.Do(func() {
			epCancel()
			_ = ln.Close()
			ep.connMu.Lock()
			ep.closed = true
			for conn := range ep.conns {
				_ = conn.Close()
			}
			ep.connMu.Unlock()
			ep.wg.Wait()
			_ = os.Remove(path)
		})
	}
	return path, teardown, nil
}

// acceptLoop accepts connections until the listener closes (teardown).
func (ep *workspaceEndpoint) acceptLoop() {
	for {
		conn, err := ep.ln.Accept()
		if err != nil {
			return
		}
		ep.connMu.Lock()
		if ep.closed {
			ep.connMu.Unlock()
			_ = conn.Close()
			return
		}
		ep.conns[conn] = struct{}{}
		ep.connMu.Unlock()
		ep.wg.Add(1)
		go func() {
			defer ep.wg.Done()
			defer func() {
				ep.connMu.Lock()
				delete(ep.conns, conn)
				ep.connMu.Unlock()
			}()
			ep.serveConn(conn)
		}()
	}
}

// workspaceHelloParams is the workspace endpoint's own handshake payload
// — deliberately not helloParams (mcpsock.go's {"feature":"<id>"}), since
// a workspace connection has no feature id to carry.
type workspaceHelloParams struct {
	Mode string `json:"mode"`
}

// serveConn speaks the same session-socket JSON-RPC 2.0 codec mcpEndpoint
// does — a handshake first, then any number of list_tools / call_tool
// requests, each in its own subgoroutine with a shared write mutex — but
// checks a {"mode":"workspace"} hello instead of a feature id.
func (ep *workspaceEndpoint) serveConn(conn net.Conn) {
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var wmu sync.Mutex
	first := true
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		req, err := mcp.Decode(line)
		if err != nil {
			continue // unparseable frame: no id to answer
		}
		if first {
			var h workspaceHelloParams
			_ = json.Unmarshal(req.Params, &h)
			first = false
			if req.Method != "hello" || h.Mode != "workspace" {
				if len(req.ID) > 0 {
					ep.writeFrame(conn, &wmu, mcp.ErrorObject{
						JSONRPC: mcp.JSONRPC, ID: req.ID,
						Error: mcp.ErrorData{Code: mcp.ModeMismatch, Message: `workspace hello required: {"mode":"workspace"}`},
					})
				}
				return
			}
			if len(req.ID) > 0 {
				ep.writeFrame(conn, &wmu, mcp.Response{
					JSONRPC: mcp.JSONRPC, ID: req.ID,
					Result: json.RawMessage(`{"mode":"workspace"}`),
				})
			}
			continue
		}
		switch req.Method {
		case "list_tools", "call_tool":
			// each request in its own goroutine so one slow call (e.g. a
			// card_diff on a large worktree) never blocks a peer call on
			// this connection.
			ep.wg.Add(1)
			go func(req *mcp.Request) {
				defer ep.wg.Done()
				ep.dispatch(conn, &wmu, req)
			}(req)
		default:
			if len(req.ID) > 0 {
				ep.writeFrame(conn, &wmu, mcp.ErrorObject{
					JSONRPC: mcp.JSONRPC, ID: req.ID,
					Error: mcp.ErrorData{Code: mcp.MethodNotFound, Message: "method not found"},
				})
			}
		}
	}
}

// writeFrame serializes one response onto conn under wmu.
func (ep *workspaceEndpoint) writeFrame(conn net.Conn, wmu *sync.Mutex, v any) {
	wmu.Lock()
	defer wmu.Unlock()
	_ = mcp.Encode(conn, v)
}

// dispatch answers one list_tools / call_tool request against the
// board-level tool set below.
func (ep *workspaceEndpoint) dispatch(conn net.Conn, wmu *sync.Mutex, req *mcp.Request) {
	var result json.RawMessage
	var err error
	switch req.Method {
	case "list_tools":
		result, err = ep.listTools()
	case "call_tool":
		result, err = ep.callTool(req)
	}
	if err != nil {
		ep.writeFrame(conn, wmu, mcp.ErrorObject{
			JSONRPC: mcp.JSONRPC, ID: req.ID,
			Error: mcp.ErrorData{Code: mcp.ToolError, Message: err.Error()},
		})
		return
	}
	ep.writeFrame(conn, wmu, mcp.Response{JSONRPC: mcp.JSONRPC, ID: req.ID, Result: result})
}

// listTools advertises the fixed board-level tool set — unlike
// mcpEndpoint.listTools, this never varies by stage or flavor, because it
// isn't scoped to one.
func (ep *workspaceEndpoint) listTools() (json.RawMessage, error) {
	return mcp.MarshalTools(workspaceTools())
}

// callTool unmarshals one call_tool request and forwards it to the named
// board-level tool. Reuses callToolParams (mcpsock.go): the wire shape is
// identical, only what answers it differs.
func (ep *workspaceEndpoint) callTool(req *mcp.Request) (json.RawMessage, error) {
	var p callToolParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return nil, fmt.Errorf("call_tool: %w", err)
	}
	result, err := ep.dispatchTool(p.Name, p.Args)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"result": result})
}

// dispatchTool routes one board-level tool call by name to the engine's
// shared dispatcher (dispatchBoardTool) — factored out of this type so
// BoardSession's client-tool path (boardsession.go, the copilot-style
// SessionOpts.Tools route rather than this socket) can answer the same
// calls without standing up a workspaceEndpoint of its own. See
// dispatchBoardTool's own comment for why the split landed here.
func (ep *workspaceEndpoint) dispatchTool(name string, args json.RawMessage) (string, error) {
	return ep.engine.dispatchBoardTool(ep.ctx, name, args)
}

// dispatchBoardTool routes one board-level tool call by name. Every
// handler returns exactly one of (text result, nil) or ("", error) —
// never a nil error with an empty success, matching DispatchClientTool's
// contract on the per-feature side.
//
// It lives on *Engine, not *workspaceEndpoint, so both tool-call routes
// into gummi's board-level surface share one implementation: the
// workspace MCP endpoint above (a hosted agent dialing in over the
// socket) and BoardSession's own client-tool handling (an in-process
// board session on a ClientTools backend, which has no socket and no
// endpoint — see boardsession.go). Each caller supplies its own ctx —
// the endpoint's, canceled at its teardown; the board session's, canceled
// when that session stops — so a dispatch outlives neither caller's
// lifecycle.
func (e *Engine) dispatchBoardTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case boardListToolName:
		return e.boardList(ctx)
	case cardStatusToolName:
		return e.cardStatus(ctx, args)
	case cardSpecToolName:
		return e.cardSpec(ctx, args)
	case cardDiffToolName:
		return e.cardDiff(ctx, args)
	case cardRunToolName:
		return e.cardRun(ctx, args)
	case cardResumeToolName:
		return e.cardResume(ctx, args)
	case cardNewToolName:
		return e.cardNew(ctx, args)
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

// resolveFeature resolves a board-level tool's "id" argument to a stored
// feature: an FD-/BG-/RS- id first, falling back to an external ref
// match. Mirrors cmd/gummi/read.go's resolveFeatureID, which this package
// cannot import (cmd/gummi is package main; internal/driver, which could
// otherwise share it, already imports internal/engine, so the reverse
// import would cycle) — replicated here rather than factored out because
// it is six lines wrapping two Store methods, not a design worth a new
// shared package for.
func (e *Engine) resolveFeature(ctx context.Context, idOrRef string) (domain.Feature, error) {
	store := e.cfg.Store
	if id, err := domain.ParseFeatureID(idOrRef); err == nil {
		return store.GetFeature(ctx, id)
	}
	f, err := store.FeatureByExternalRef(ctx, idOrRef)
	if err != nil {
		return domain.Feature{}, fmt.Errorf("no card %q (not an FD-NNN/BG-NNN/RS-NNN id, and no card carries it as an external ref): %w", idOrRef, err)
	}
	return f, nil
}

// workspaceBranchState collapses the worktree pool's branch queries into
// one word, exactly like cmd/gummi/status.go's branchState (unreachable
// from here for the same package-main reason resolveFeature is). A nil
// pool (an engine wired without one, e.g. a bare unit test) reads as
// "none" rather than panicking.
func workspaceBranchState(ctx context.Context, pool *worktree.Pool, f *domain.Feature) string {
	if pool == nil {
		return "none"
	}
	exists, err := pool.BranchExists(ctx, f)
	if err != nil || !exists {
		return "none"
	}
	if landed, err := pool.Landed(ctx, f); err == nil && landed {
		return "landed"
	}
	if ahead, err := pool.BranchAhead(ctx, f); err == nil && ahead {
		return "ahead"
	}
	return "created"
}

// boardListItem is one row of board_list's JSON array result.
type boardListItem struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Title        string  `json:"title"`
	Stage        string  `json:"stage"`
	SpendCredits float64 `json:"spend_credits"`
	Envelope     int     `json:"envelope"`
	Verified     bool    `json:"verified"`
	Done         bool    `json:"done"`
	Ref          string  `json:"ref,omitempty"`
}

// boardList answers board_list: every card in the workspace, ordered by
// number (state.Store.ListFeatures's own order).
func (e *Engine) boardList(ctx context.Context) (string, error) {
	feats, err := e.cfg.Store.ListFeatures(ctx)
	if err != nil {
		return "", err
	}
	items := make([]boardListItem, 0, len(feats))
	for _, f := range feats {
		kind := f.Kind
		if kind == "" {
			kind = domain.KindFeature
		}
		items = append(items, boardListItem{
			ID: string(f.ID), Kind: string(kind), Title: f.Title, Stage: string(f.Stage),
			SpendCredits: f.Spend.Credits, Envelope: f.Budget.Envelope,
			Verified: !f.VerifiedAt.IsZero(), Done: f.Stage == domain.StageDone, Ref: f.ExternalRef,
		})
	}
	b, err := json.Marshal(items)
	return string(b), err
}

// cardIDArgs is the shared {"id": "..."} argument shape for the four
// single-card tools below.
type cardIDArgs struct {
	ID string `json:"id"`
}

// cardStatusItem is card_status's JSON object result.
type cardStatusItem struct {
	ID               string  `json:"id"`
	Kind             string  `json:"kind"`
	Title            string  `json:"title"`
	Stage            string  `json:"stage"`
	Branch           string  `json:"branch"`
	BranchState      string  `json:"branch_state"`
	SpendCredits     float64 `json:"spend_credits"`
	Envelope         int     `json:"envelope"`
	Verified         bool    `json:"verified"`
	Done             bool    `json:"done"`
	Running          bool    `json:"running"`
	OpenQuestions    int     `json:"open_questions"`
	OpenDiffComments int     `json:"open_diff_comments"`
}

// cardStatus answers card_status: the same snapshot `gummi status`
// prints, minus the pull-request line (a hosted agent has no use for it,
// and PullRequestRef.StatusPayload lives behind cmd/gummi's JSON view).
func (e *Engine) cardStatus(ctx context.Context, args json.RawMessage) (string, error) {
	var a cardIDArgs
	if err := json.Unmarshal(args, &a); err != nil || a.ID == "" {
		return "", fmt.Errorf("card_status: id is required")
	}
	f, err := e.resolveFeature(ctx, a.ID)
	if err != nil {
		return "", err
	}
	specOpen, diffOpen, _, err := e.GateBlockers(ctx, f.ID)
	if err != nil {
		return "", err
	}
	kind := f.Kind
	if kind == "" {
		kind = domain.KindFeature
	}
	item := cardStatusItem{
		ID: string(f.ID), Kind: string(kind), Title: f.Title, Stage: string(f.Stage),
		Branch: f.BranchName(), BranchState: workspaceBranchState(ctx, e.pool, &f),
		SpendCredits: f.Spend.Credits, Envelope: f.Budget.Envelope,
		Verified: !f.VerifiedAt.IsZero(), Done: f.Stage == domain.StageDone,
		Running:          state.ProcessAlive(state.ReadPIDFile(e.cfg.Workspace.PIDFile(f.ID))),
		OpenQuestions:    specOpen,
		OpenDiffComments: diffOpen,
	}
	b, err := json.Marshal(item)
	return string(b), err
}

// cardSpec answers card_spec: the card's design artifact as raw markdown,
// wherever it lives right now. Mirrors cmd/gummi/spec.go, but resolves the
// path through the engine's own artifactFile instead of cmd/gummi's
// unreachable artifactPath.
func (e *Engine) cardSpec(ctx context.Context, args json.RawMessage) (string, error) {
	var a cardIDArgs
	if err := json.Unmarshal(args, &a); err != nil || a.ID == "" {
		return "", fmt.Errorf("card_spec: id is required")
	}
	f, err := e.resolveFeature(ctx, a.ID)
	if err != nil {
		return "", err
	}
	path := e.artifactFile(&f)
	if path == "" {
		return "", fmt.Errorf("%s has no spec yet — it is created when the spec/brainstorm stage first runs", f.ID)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// cardDiff answers card_diff: the card's worktree diff against main.
// Mirrors cmd/gummi/diff.go.
func (e *Engine) cardDiff(ctx context.Context, args json.RawMessage) (string, error) {
	var a cardIDArgs
	if err := json.Unmarshal(args, &a); err != nil || a.ID == "" {
		return "", fmt.Errorf("card_diff: id is required")
	}
	f, err := e.resolveFeature(ctx, a.ID)
	if err != nil {
		return "", err
	}
	if e.pool == nil {
		return "", fmt.Errorf("card_diff: this engine has no worktree pool configured")
	}
	out, err := e.pool.Diff(ctx, &f)
	if err != nil {
		return "", err
	}
	return out, nil
}

// cardRun answers card_run: kick off (or, for a queued/running session,
// no-op) an autonomous stage session for the card, in this process — see
// cardRunTool's description for why this exists instead of a shell-out.
func (e *Engine) cardRun(ctx context.Context, args json.RawMessage) (string, error) {
	var a cardIDArgs
	if err := json.Unmarshal(args, &a); err != nil || a.ID == "" {
		return "", fmt.Errorf("card_run: id is required")
	}
	f, err := e.resolveFeature(ctx, a.ID)
	if err != nil {
		return "", err
	}
	if err := e.Run(f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: run requested for stage %s", f.ID, f.Stage), nil
}

// cardResume answers card_resume: RunWith(note), the same path Run takes
// but with a note appended to the fresh session's kickoff — for
// re-running a parked stage after a top-up, timeout, or with review
// comments attached.
func (e *Engine) cardResume(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		ID   string `json:"id"`
		Note string `json:"note"`
	}
	if err := json.Unmarshal(args, &a); err != nil || a.ID == "" {
		return "", fmt.Errorf("card_resume: id is required")
	}
	f, err := e.resolveFeature(ctx, a.ID)
	if err != nil {
		return "", err
	}
	if err := e.RunWith(f, a.Note); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: resume requested for stage %s", f.ID, f.Stage), nil
}

// cardNewArgs is card_new's argument shape. Envelope's zero value means
// "no cap" (matching `gummi feature`/`bugs new` --envelope's own "0 =
// none"), not "unset" — the workspace endpoint has no config-resolved
// default envelope to fall back to the way cmd/gummi's flag parsing does,
// so an agent that wants a cap has to say so.
type cardNewArgs struct {
	Kind         string `json:"kind"`
	Description  string `json:"description"`
	Profile      string `json:"profile"`
	Envelope     int    `json:"envelope"`
	Repo         string `json:"repo"`
	GateApproval string `json:"gate_approval"`
}

// cardNew answers card_new: mint a fresh card via internal/cardmint, the
// same recipe (*driver.Driver).createFeature uses for headless
// `gummi run`/`bugs new`/`gummi research` — see that package's doc
// comment for why the recipe lives there instead of here or in
// internal/driver.
//
// Its gate-approval default deliberately differs from every headless
// entry point's: cardmint reads an empty GateApproval as domain.GateGates
// (matching driver.Options' own default), but a card minted by an agent
// hosted inside someone else's TUI is not the same thing as a card a
// human typed `gummi run` for — the human at this board did not ask for
// this specific card to exist yet, so its design gates checkpoint for
// them by default (D5's spirit applied to a card the human didn't
// initiate) instead of auto-crossing on the hosted agent's say-so. An
// agent that has an explicit mandate to run unattended (the human said
// "and don't wait on me for the follow-up cards either") can still pass
// gate_approval: "auto" to opt out.
func (e *Engine) cardNew(ctx context.Context, args json.RawMessage) (string, error) {
	var a cardNewArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("card_new: %w", err)
	}
	kind := domain.Kind(a.Kind)
	if !kind.Valid() {
		return "", fmt.Errorf("card_new: kind must be one of feature, bug, research (got %q)", a.Kind)
	}
	if strings.TrimSpace(a.Description) == "" {
		return "", fmt.Errorf("card_new: description is required")
	}
	gate := a.GateApproval
	if gate == "" {
		gate = domain.GateOff
	} else if norm, ok := domain.NormalizeGateApproval(gate); !ok {
		return "", fmt.Errorf("card_new: gate_approval must be %q or %q (got %q)", domain.GateGates, domain.GateOff, gate)
	} else {
		gate = norm
	}
	f, err := cardmint.Mint(ctx, e.cfg.Store, e.cfg.Workspace, cardmint.Input{
		Kind: kind, Description: a.Description, Profile: a.Profile, Envelope: a.Envelope,
		Repo: a.Repo, RepoKnown: e.RepoKnown, GateApproval: gate,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: created, stage %s, gate approval %s", f.ID, f.Stage, f.GateApproval), nil
}

const (
	boardListToolName  = "board_list"
	cardStatusToolName = "card_status"
	cardSpecToolName   = "card_spec"
	cardDiffToolName   = "card_diff"
	cardRunToolName    = "card_run"
	cardResumeToolName = "card_resume"
	cardNewToolName    = "card_new"
)

// workspaceTools is the fixed board-level tool set the workspace endpoint
// advertises — unlike stageTools, it never depends on a stage or flavor,
// because it isn't scoped to a single card's session.
func workspaceTools() []agent.ToolDef {
	return []agent.ToolDef{
		boardListTool(), cardStatusTool(), cardSpecTool(), cardDiffTool(), cardRunTool(), cardResumeTool(),
		cardNewTool(),
	}
}

func boardListTool() agent.ToolDef {
	return agent.ToolDef{
		Name: boardListToolName,
		Description: "List every card in this workspace's backlog: id, kind, title, stage, " +
			"spend/envelope, and whether it is verified or done. Returns a JSON array. Use this " +
			"to see what exists before acting on a specific card.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func cardStatusTool() agent.ToolDef {
	return agent.ToolDef{
		Name: cardStatusToolName,
		Description: "Read-only snapshot of one card: stage, branch state, spend/envelope, " +
			"verified/done/running flags, and open gate blockers (unanswered spec questions, " +
			"unresolved diff comments). Returns a JSON object. Takes no lock — safe to poll a " +
			"card this process, or another gummi process, is actively driving.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "The card's id (FD-NNN/BG-NNN/RS-NNN) or external ref."},
			},
			"required": []any{"id"},
		},
	}
}

func cardSpecTool() agent.ToolDef {
	return agent.ToolDef{
		Name: cardSpecToolName,
		Description: "Read-only dump of a card's current design artifact (spec or report) as " +
			"markdown, wherever it lives right now (workspace home, draft, or mid-flight worktree " +
			"copy). Takes no lock.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "The card's id or external ref."},
			},
			"required": []any{"id"},
		},
	}
}

func cardDiffTool() agent.ToolDef {
	return agent.ToolDef{
		Name: cardDiffToolName,
		Description: "Read-only dump of a card's worktree diff against main. Takes no lock. " +
			"Before a worktree exists (a card still in a design stage), this errors clearly.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "The card's id or external ref."},
			},
			"required": []any{"id"},
		},
	}
}

func cardRunTool() agent.ToolDef {
	return agent.ToolDef{
		Name: cardRunToolName,
		Description: "Start an autonomous stage session for a card in THIS gummi process — the " +
			"only safe way to drive a card from here. Do not shell out to `gummi run`/`gummi " +
			"resume`: that spawns a second gummi process, which contends for this card's " +
			"per-card lock and fails outright since this process already holds it while its TUI " +
			"is open. Errors if the card's current stage is interactive (brainstorm/spec/plan's " +
			"human turn) — those need a human at the board, not this tool.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "The card's id or external ref."},
			},
			"required": []any{"id"},
		},
	}
}

func cardResumeTool() agent.ToolDef {
	return agent.ToolDef{
		Name: cardResumeToolName,
		Description: "Resume a parked autonomous stage (after a timeout or an exhausted-envelope " +
			"pause) in THIS gummi process — same caveats as card_run. note, if given, is appended " +
			"to the fresh session's kickoff so it starts by addressing it.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":   map[string]any{"type": "string", "description": "The card's id or external ref."},
				"note": map[string]any{"type": "string", "description": "Optional note appended to the stage kickoff."},
			},
			"required": []any{"id"},
		},
	}
}

func cardNewTool() agent.ToolDef {
	return agent.ToolDef{
		Name: cardNewToolName,
		Description: "Mint a new card (feature, bug, or research item) from a free-form " +
			"description and add it to this workspace's backlog. Does not start it — follow up " +
			"with card_run once it is ready to drive. Unlike a card a human creates at the board, " +
			"a card minted this way defaults to checkpointing its design gates for the human " +
			"(gate_approval \"caller\") rather than auto-crossing them, since the human did not ask " +
			"for this specific card yet; pass gate_approval \"auto\" only when explicitly told to " +
			"run it unattended.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind":        map[string]any{"type": "string", "description": "One of feature, bug, research."},
				"description": map[string]any{"type": "string", "description": "Free-form description. The first line becomes the title; anything beyond it seeds the design draft."},
				"profile":     map[string]any{"type": "string", "description": "Optional model-role profile (workspace default if omitted)."},
				"envelope":    map[string]any{"type": "integer", "description": "Optional credit envelope; omit or 0 for no cap."},
				"repo":        map[string]any{"type": "string", "description": "Optional configured repo name (workspace default if omitted)."},
				"gate_approval": map[string]any{
					"type":        "string",
					"description": "Optional: \"auto\" or \"caller\" (default \"caller\" — see description).",
				},
			},
			"required": []any{"kind", "description"},
		},
	}
}
