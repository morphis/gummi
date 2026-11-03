package engine

// A session's specs (DESIGN §25.3): a list, one per piece of work. Most
// sessions have none. A session reaches them through spec_start, which
// adds one and returns its number, spec_list, and the same spec_view,
// spec_replace_section and spec_annotate a stage's architect uses, each
// taking the spec to act on. A spec lives with the workspace
// (.gummi/specs/<ID>/<n>-<slug>.md, or the old Feature.SpecPath for a
// session's spec from before), never on the branch, and every write is a
// numbered revision of that spec in the store: a run freezes one.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/gummidir"
	"github.com/morphis/gummi/internal/spec"
)

const (
	specStartToolName = "spec_start"
	specListToolName  = "spec_list"
)

func specStartTool() agent.ToolDef {
	return agent.ToolDef{
		Name: specStartToolName,
		Description: "Add a spec to this session from gummi's template: Problem, Approach, Done when " +
			"(a gummi-done-when block), Limits and Notes. A session keeps one spec per piece of work; " +
			"specs are optional. Returns the new spec's number: pass it as spec to spec_view, " +
			"spec_replace_section and spec_annotate once the session has more than one. Fill it with " +
			"spec_replace_section.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{"type": "string", "description": "The spec's title: the piece of work it is for."},
			},
		},
	}
}

func specListTool() agent.ToolDef {
	return agent.ToolDef{
		Name: specListToolName,
		Description: "List this session's specs: each one's number, title, state (draft, running, met " +
			"or partial), latest revision and done-when count. Read-only.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

// specArg is the optional spec argument of a session's spec tools.
var specArg = map[string]any{
	"type": "integer",
	"description": "Which of the session's specs to act on, by its number (spec_list names them). " +
		"May be left out while the session has a single spec; with more than one it is required.",
}

// withSpecArg adds the optional spec argument to a spec tool.
func withSpecArg(def agent.ToolDef, also string) agent.ToolDef {
	props := map[string]any{"spec": specArg}
	if p, ok := def.Parameters["properties"].(map[string]any); ok {
		for k, v := range p {
			props[k] = v
		}
	}
	params := map[string]any{}
	for k, v := range def.Parameters {
		params[k] = v
	}
	params["properties"] = props
	def.Parameters = params
	def.Description += also
	return def
}

const specArgNote = " On a session, spec names which of its specs; it may be left out while there is one."

// sessionSpecTools are a session's spec tools, offered to every session
// that is not a run's step.
func sessionSpecTools() []agent.ToolDef {
	return []agent.ToolDef{
		specStartTool(),
		specListTool(),
		withSpecArg(specViewTool(), specArgNote),
		withSpecArg(specReplaceSectionTool(), specArgNote),
		withSpecArg(specAnnotateTool(), specArgNote),
	}
}

// isSession reports whether f keeps specs of its own: a freeform card
// that is no step's, nor a node of a crew from an old database.
func isSession(f domain.Feature) bool { return f.IsFreeform() && !f.IsChild() }

// SessionSpecs lists a session's specs, each with its state.
func (e *Engine) SessionSpecs(ctx context.Context, f domain.Feature) ([]domain.SessionSpec, error) {
	if e.cfg.Store == nil || !isSession(f) {
		return nil, nil
	}
	return e.cfg.Store.SessionSpecs(ctx, f.ID)
}

// SessionSpecPath is where a session's spec n lives, "" when it has no
// such spec.
func (e *Engine) SessionSpecPath(f domain.Feature, n int) string {
	if e.cfg.Store == nil {
		return ""
	}
	path, _ := e.cfg.Store.SessionSpecFile(context.Background(), e.pool.Root(), f.ID, n)
	return path
}

// SessionSpec reads a session's spec n; ok is false when it has none.
func (e *Engine) SessionSpec(f domain.Feature, n int) (string, bool) {
	path := e.SessionSpecPath(f, n)
	if path == "" {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// specTarget resolves the spec a session's spec tool acts on: the one its
// spec argument names, or the session's only spec when the argument is
// left out. With more than one spec, leaving it out is refused, so no
// write lands in the wrong spec.
func (e *Engine) specTarget(f domain.Feature, args json.RawMessage) (int, string, error) {
	var a struct {
		Spec json.RawMessage `json:"spec"`
	}
	_ = json.Unmarshal(args, &a)
	n, given, err := parseSpecArg(a.Spec)
	if err != nil {
		return 0, "", err
	}
	specs, err := e.SessionSpecs(context.Background(), f)
	if err != nil {
		return 0, "", err
	}
	switch {
	case len(specs) == 0:
		return 0, "", errNoSessionSpec
	case !given && len(specs) > 1:
		return 0, "", fmt.Errorf("this session has %d specs: name the one to act on with spec (spec_list lists them)", len(specs))
	case !given:
		n = specs[0].N
	}
	path := e.SessionSpecPath(f, n)
	if path == "" {
		return 0, "", fmt.Errorf("this session has no spec %d; spec_list lists them", n)
	}
	return n, path, nil
}

// parseSpecArg reads a spec argument: a number, or a string holding one
// (2, "2" or "S2"). given is false when it was left out.
func parseSpecArg(raw json.RawMessage) (n int, given bool, err error) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0, false, nil
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		s = strings.TrimPrefix(strings.TrimSpace(str), "S")
	}
	n, err = strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, true, fmt.Errorf("spec must be a spec's number, as spec_list names them; got %s", string(raw))
	}
	return n, true, nil
}

var errNoSessionSpec = errors.New("this session has no spec yet; spec_start creates one")

// handleSpecStart adds a spec to the session.
func (e *Engine) handleSpecStart(s *Session, tc *agent.ToolCall) {
	var a struct {
		Title string `json:"title"`
	}
	_ = json.Unmarshal(tc.Args, &a)
	f := s.Feature
	if !isSession(f) {
		e.resolveNow(s, tc.ID, "spec_start is a session's: this card has no spec of its own")
		return
	}
	title := strings.TrimSpace(a.Title)
	if title == "" {
		title = f.Title
	}
	sp, rev, err := e.StartSessionSpec(context.Background(), f, title, ActorAgent)
	if err != nil {
		e.resolveNow(s, tc.ID, err.Error())
		return
	}
	s.appendActivity(fmt.Sprintf("started spec %d: %s", sp.N, sp.Title))
	e.persist(s)
	// The arguments are named here because an agent that defers gummi's
	// tool schemas calls spec_replace_section next without reading one.
	e.resolveNow(s, tc.ID, fmt.Sprintf("created spec %d, %q (revision %d); fill its sections with spec_replace_section {section, body}, passing spec %d once the session has more than one spec",
		sp.N, sp.Title, rev, sp.N))
}

// handleSpecList names the session's specs.
func (e *Engine) handleSpecList(s *Session, tc *agent.ToolCall) {
	f := s.Feature
	if !isSession(f) {
		e.resolveNow(s, tc.ID, "spec_list is a session's: this card has no specs of its own")
		return
	}
	ctx := context.Background()
	specs, err := e.SessionSpecs(ctx, f)
	if err != nil {
		e.resolveNow(s, tc.ID, "could not list the specs: "+err.Error())
		return
	}
	if len(specs) == 0 {
		e.resolveNow(s, tc.ID, errNoSessionSpec.Error())
		return
	}
	var b strings.Builder
	for _, sp := range specs {
		rev := 0
		if revs, _ := e.cfg.Store.SpecRevisions(ctx, f.ID, sp.N); len(revs) > 0 {
			rev = revs[0].Rev
		}
		items := 0
		if text, ok := e.SessionSpec(f, sp.N); ok {
			if dw, _, err := spec.ParseDoneWhen(text); err == nil {
				items = len(dw)
			}
		}
		fmt.Fprintf(&b, "S%d %q — %s, revision %d, %d done-when item%s\n", sp.N, sp.Title, sp.State, rev, items, plural(items))
	}
	e.resolveNow(s, tc.ID, strings.TrimRight(b.String(), "\n"))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ActorAgent is who a spec revision the session's agent wrote is by.
const ActorAgent = "agent"

// StartSessionSpec adds a spec to a session from the template, with its
// first revision, and returns it.
func (e *Engine) StartSessionSpec(ctx context.Context, f domain.Feature, title, by string) (domain.SessionSpec, int, error) {
	if !isSession(f) {
		return domain.SessionSpec{}, 0, fmt.Errorf("%s has no spec of its own", f.ID)
	}
	if e.cfg.Store == nil {
		return domain.SessionSpec{}, 0, errors.New("a session's specs need the store")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = f.Title
	}
	sp, err := e.cfg.Store.AddSessionSpec(ctx, f.ID, title, e.now())
	if err != nil {
		return sp, 0, err
	}
	path := gummidir.FromRel(e.pool.Root(), sp.Path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return sp, 0, err
	}
	body := spec.SessionTemplate(title)
	if err := atomicfile.Write(path, []byte(body), 0o600); err != nil {
		return sp, 0, err
	}
	rev, err := e.recordSpecRevision(ctx, f.ID, sp.N, body, by)
	return sp, rev, err
}

// WriteSessionSpec replaces a session's spec n with content a person wrote
// on the Spec tab, as one new revision of that spec.
func (e *Engine) WriteSessionSpec(ctx context.Context, f domain.Feature, n int, content, by string) (int, error) {
	if !isSession(f) {
		return 0, fmt.Errorf("%s has no spec of its own", f.ID)
	}
	path := e.SessionSpecPath(f, n)
	if path == "" {
		if n == 1 {
			return 0, errors.New("this session has no spec yet")
		}
		return 0, fmt.Errorf("this session has no spec %d", n)
	}
	if _, err := os.Stat(path); err != nil {
		return 0, fmt.Errorf("spec %d's file is missing: %w", n, err)
	}
	unlock := spec.LockFile(path)
	defer unlock()
	if err := atomicfile.Write(path, []byte(content), 0o600); err != nil {
		return 0, err
	}
	rev, err := e.recordSpecRevision(ctx, f.ID, n, content, by)
	if err == nil {
		e.workflowSpecEdited(ctx, f, n, rev)
	}
	return rev, err
}

// recordSpecRevision saves spec n's current text as its next revision.
func (e *Engine) recordSpecRevision(ctx context.Context, id domain.FeatureID, n int, content, by string) (int, error) {
	if e.cfg.Store == nil {
		return 0, nil
	}
	rev, err := e.cfg.Store.AddSpecRevision(ctx, id, n, content, by, e.now())
	if err == nil {
		e.send(Event{Feature: id, Stage: domain.StageOpen, Kind: EventUpdated})
	}
	return rev, err
}

// noteSessionSpecWrite records a revision of spec n after the agent wrote
// it at path through a spec tool, and tells an open run that froze it. A no-op on every card that is not a session (n is 0).
func (e *Engine) noteSessionSpecWrite(s *Session, n int, path string) {
	f := s.Feature
	if n == 0 || !isSession(f) {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	ctx := context.Background()
	if rev, err := e.recordSpecRevision(ctx, f.ID, n, string(raw), ActorAgent); err == nil {
		e.workflowSpecEdited(ctx, f, n, rev)
	}
}

// specToolPath is the file a spec tool call acts on, and the session spec
// it is (0 on a card that is not a session, whose spec is the session's
// own resolved path).
func (e *Engine) specToolPath(s *Session, args json.RawMessage) (int, string, error) {
	if !isSession(s.Feature) {
		return 0, s.SpecPath(), nil
	}
	return e.specTarget(s.Feature, args)
}

const sessionSpecHint = `This session may keep specs (gummi's DESIGN §25.3). A spec is the written plan for one piece of work; a session keeps one per piece and most keep none. spec_start adds one from the template (Problem, Approach, Done when, Limits, Notes) and returns its number; spec_list names them with their state; spec_view and spec_replace_section read and write one — pass spec (its number) once the session has more than one; the person edits them too. A spec's done-when items, each with a check command or judge: true, are what a workflow run on it is judged against.`

func writeFile(path, content string) error { return atomicfile.Write(path, []byte(content), 0o600) }
