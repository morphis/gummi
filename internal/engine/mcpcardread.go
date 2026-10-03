package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// mcpNonce returns a short random hex string distinguishing one unix-socket
// bind from another: every endpoint in this package names its socket after
// the caller it serves, and a caller can rebind (an idle timeout respawning
// a consult backend) while the old socket is still on disk. Falls back to a
// timestamp if the system random source is somehow unavailable: any
// distinguishing value is enough here, this is not security-sensitive, and
// a bind that still collides just fails loudly (Listen returns an error)
// rather than corrupting anything.
func mcpNonce() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// resolveFeature resolves a card tool's "id" argument to a stored feature:
// an FD-/BG-/RS- id first, falling back to an external ref match. Mirrors
// cmd/gummi/read.go's resolveFeatureID, which this package cannot import
// (cmd/gummi is package main; internal/driver, which could otherwise share
// it, already imports internal/engine, so the reverse import would cycle) —
// replicated here rather than factored out because it is six lines wrapping
// two Store methods, not a design worth a new shared package for.
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

// cardIDArgs is the {"id": "..."} argument shape the card read handlers
// below take. A consult session never lets a caller fill it: its tool
// schemas carry no id, and dispatchConsultTool forces the bound card's in.
type cardIDArgs struct {
	ID string `json:"id"`
}

// cardStatusItem is card_status's JSON object result.
type cardStatusItem struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Title        string  `json:"title"`
	Stage        string  `json:"stage"`
	Branch       string  `json:"branch"`
	BranchState  string  `json:"branch_state"`
	SpendCredits float64 `json:"spend_credits"`
	Envelope     int     `json:"envelope"`
	Verified     bool    `json:"verified"`
	// Ending is how the card left gummi: "landed", "handed_off",
	// "dropped", or empty while it is still open — the same word every
	// other surface uses. It replaces the `done`/`handed_off` pair, which
	// took two booleans to name one fact and still could not name a drop.
	// "is the card closed" is stage == "done".
	Ending           domain.Ending `json:"ending,omitempty"`
	Running          bool          `json:"running"`
	OpenQuestions    int           `json:"open_questions"`
	OpenDiffComments int           `json:"open_diff_comments"`
}

// cardStatus answers card_status: the same snapshot `gummi status` prints,
// minus the pull-request line (PullRequestRef.StatusPayload lives behind
// cmd/gummi's JSON view).
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
	bs := workspaceBranchState(ctx, e.pool, &f)
	item := cardStatusItem{
		ID: string(f.ID), Kind: string(kind), Title: f.Title, Stage: string(f.Stage),
		Branch: f.BranchName(), BranchState: bs,
		SpendCredits: f.Spend.Credits, Envelope: f.Budget.Envelope,
		Verified:         !f.VerifiedAt.IsZero(),
		Ending:           f.Ending(bs == "landed"),
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
		return "", fmt.Errorf("%s has no spec yet — it is created when the design stage first runs", f.ID)
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

const (
	cardStatusToolName = "card_status"
	cardSpecToolName   = "card_spec"
	cardDiffToolName   = "card_diff"
)

// consultTools is the fixed, read-only tool set a card's ConsultSession
// advertises: what card_status/card_spec/card_diff answer, but with no "id"
// parameter at all — the schema itself is what keeps a consult session from
// ever answering about a different card than the one it is bound to (a
// cross-card consult is out of scope), rather than a runtime check that
// could be gotten around by an argument the model is never even offered.
func consultTools() []agent.ToolDef {
	return []agent.ToolDef{consultStatusTool(), consultSpecTool(), consultDiffTool()}
}

func consultStatusTool() agent.ToolDef {
	return agent.ToolDef{
		Name: cardStatusToolName,
		Description: "Read-only snapshot of this card: stage, branch state, spend/budget, " +
			"verified/done/running flags, and open gate blockers (unanswered spec questions, " +
			"unresolved diff comments). Returns a JSON object. Always answers for the one card " +
			"this conversation is bound to — there is no id to name a different one.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func consultSpecTool() agent.ToolDef {
	return agent.ToolDef{
		Name: cardSpecToolName,
		Description: "Read-only dump of this card's current design artifact (spec or report) as " +
			"markdown, wherever it lives right now. Always answers for the one card this " +
			"conversation is bound to.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func consultDiffTool() agent.ToolDef {
	return agent.ToolDef{
		Name: cardDiffToolName,
		Description: "Read-only dump of this card's worktree diff against main. Before a " +
			"worktree exists (a card still in a design stage), this errors clearly. Always " +
			"answers for the one card this conversation is bound to.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

// dispatchConsultTool routes one of a ConsultSession's three read-only tool
// calls, forcing the bound card's id in as the argument the zero-parameter
// schema never lets the model supply — the handlers above answer for the
// bound card no matter what (or nothing) the model's own args carried.
func (e *Engine) dispatchConsultTool(ctx context.Context, id domain.FeatureID, name string, _ json.RawMessage) (string, error) {
	forced, err := json.Marshal(cardIDArgs{ID: string(id)})
	if err != nil {
		return "", err
	}
	switch name {
	case cardStatusToolName:
		return e.cardStatus(ctx, forced)
	case cardSpecToolName:
		return e.cardSpec(ctx, forced)
	case cardDiffToolName:
		return e.cardDiff(ctx, forced)
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}
