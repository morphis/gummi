package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// askToolName is the client tool the agent calls to put a multiple-choice
// question to the user. gummi surfaces it as an inline picker (attached)
// or a needs-attention item (detached/autonomous), then feeds the chosen
// answer back as the tool's result so the model's turn resumes in-context.
const askToolName = "ask_user"

// Ask is a parsed ask_user invocation awaiting the user's answer.
type Ask struct {
	CallID     string      // the agent-side tool-call id, for Resolve
	Question   string      `json:"question"`
	Options    []AskOption `json:"options"`
	MultiPick  bool        `json:"multi_select"`
	FreeForm   bool        `json:"allow_free_form"`
	SpecAnchor string      `json:"spec_anchor"`
}

// AskOption is one selectable answer.
type AskOption struct {
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

const (
	annotateToolName = "spec_annotate"
	verdictToolName  = "submit_verdict"
	resolveToolName  = "resolve_annotation"

	specViewToolName           = "spec_view"
	specReplaceSectionToolName = "spec_replace_section"
)

// The verdict-semantics constants are the shared clauses used by both
// tool descriptions (below) and the stage-hint prose (hints.go's
// review/plan-critique hints). Restating them in more than one place
// let them drift; the tool description and the fallback VERDICT: line
// contract must always mean the same thing per stage.
const (
	// verdictPassBlockingFindings is the shared pass-verdict base for
	// review/critique — nits ride along on a pass.
	verdictPassBlockingFindings = "no blocking findings (nits alone pass)"
	// verdictChangesBase is the shared changes-verdict base for
	// review/critique — a single blocking finding is enough.
	verdictChangesBase = "at least one blocking finding"
)

// stageTools returns the gummi-owned client tools offered on a stage.
// ask_user is interactive-only (it blocks on a human, and only the chat
// picker can answer it); spec_annotate is offered to the interactive
// architect; submit_verdict to the reviewer/verifier; resolve_annotation
// to the implementer/fixer so it can mark diff review comments addressed
// (DESIGN §6.1's resolve event for diffs — always registered on those
// stages because comments can also arrive mid-run as a live turn). Every
// stage that works with the design artifact reads and rewrites it through
// spec_view/spec_replace_section rather than raw file access, so a backend
// caged to the worktree still has a gummi-mediated path to it. The
// non-blocking tools (annotate, verdict, resolve, spec_view,
// spec_replace_section) are gummi-resolved immediately, so they are safe
// on autonomous stages. The plan-critique pass reviews the plan and files
// findings, so it gets both; the rebase-resolve pass is judged by git
// state alone and gets none.
func stageTools(stage domain.Stage, flavor runFlavor) []agent.ToolDef {
	switch flavor {
	case flavorCritique:
		return []agent.ToolDef{critiqueVerdictTool(), specAnnotateTool()}
	case flavorRebase:
		return nil
	}
	switch stage {
	case domain.StageBrainstorm, domain.StageSpec, domain.StageTriage, domain.StageDiagnose,
		domain.StageShape:
		return []agent.ToolDef{askUserTool(), specAnnotateTool(), specViewTool(), specReplaceSectionTool()}
	case domain.StageReview:
		return []agent.ToolDef{submitVerdictTool(), specViewTool(), specReplaceSectionTool()}
	case domain.StageVerify:
		return []agent.ToolDef{verifyVerdictTool(), specViewTool(), specReplaceSectionTool()}
	case domain.StageImplement, domain.StageFix, domain.StageInvestigate:
		return []agent.ToolDef{resolveAnnotationTool(), specViewTool(), specReplaceSectionTool()}
	case domain.StagePlan:
		return []agent.ToolDef{specViewTool(), specReplaceSectionTool()}
	default:
		return nil
	}
}

// filterReadOnlyTools strips the artifact-rewriting tools from a stage's
// gummi-mediated surface for a read-only research session. The read-only
// contract is all-or-nothing at the adapter boundary, so only the tools
// that never mutate the main checkout remain: spec_view (a read) and the
// gummi-state tools (submit_verdict / resolve_annotation / verify_verdict,
// which write to the store, never the artifact). spec_replace_section and
// spec_annotate rewrite the artifact and are structurally absent, so the
// engine serves — over opts.Tools, MCP list_tools, and the prompt's
// toolHint — exactly the stripped set.
func filterReadOnlyTools(defs []agent.ToolDef, readOnly bool) []agent.ToolDef {
	if !readOnly {
		return defs
	}
	out := make([]agent.ToolDef, 0, len(defs))
	for _, d := range defs {
		if d.Name == specReplaceSectionToolName || d.Name == annotateToolName {
			continue
		}
		out = append(out, d)
	}
	return out
}

func askUserTool() agent.ToolDef {
	return agent.ToolDef{
		Name: askToolName,
		Description: "Ask the user a question with a small set of options and wait for their " +
			"answer. Use this whenever you need a decision from the user: it is cheaper and " +
			"clearer than asking in prose. Returns the chosen option(s).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "The question to put to the user.",
				},
				"options": map[string]any{
					"type":        "array",
					"description": "2–6 distinct options.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"label":  map[string]any{"type": "string", "description": "Short choice text."},
							"detail": map[string]any{"type": "string", "description": "Optional one-line explanation."},
						},
						"required": []any{"label"},
					},
				},
				"multi_select":    map[string]any{"type": "boolean", "description": "Allow choosing more than one option."},
				"allow_free_form": map[string]any{"type": "boolean", "description": "Let the user type their own answer instead (default true)."},
				"spec_anchor": map[string]any{
					"type": "string",
					"description": "Optional: a unique snippet of a spec line this decision belongs to. " +
						"gummi records the answer as a resolved %% marker under it.",
				},
			},
			"required": []any{"question", "options"},
		},
	}
}

func specAnnotateTool() agent.ToolDef {
	return agent.ToolDef{
		Name: annotateToolName,
		Description: "Attach an open question or note to a spec line as a %% marker. Use this " +
			"instead of writing %% lines by hand: gummi places the marker with correct " +
			"anchoring so it surfaces as its own checklist thread.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"anchor": map[string]any{
					"type":        "string",
					"description": "A unique snippet of the spec line to annotate.",
				},
				"note": map[string]any{
					"type":        "string",
					"description": "The question or note (one line).",
				},
			},
			"required": []any{"anchor", "note"},
		},
	}
}

func specViewTool() agent.ToolDef {
	return agent.ToolDef{
		Name: specViewToolName,
		Description: "Read a section of the current spec's artifact. Pass the section's heading " +
			"text (case-insensitive) to return that section's body, or omit section to return the " +
			"whole document. Read-only — never modifies the artifact.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"section": map[string]any{
					"type":        "string",
					"description": "Optional: the section's heading text, e.g. \"Problem\". Omit for the whole document.",
				},
			},
		},
	}
}

func specReplaceSectionTool() agent.ToolDef {
	return agent.ToolDef{
		Name: specReplaceSectionToolName,
		Description: "Rewrite the body of one section of the current spec's artifact — the lines between " +
			"its `## ` heading and the next `## ` heading (or end of file). Heading match is " +
			"case-insensitive; the heading line itself is never touched. The write is a naive splice: " +
			"re-emit any `%% @user:` marker lines yourself, since gummi does not preserve, diff, or merge " +
			"them for you. The body must not contain a top-level `## ` heading.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"section": map[string]any{"type": "string", "description": "The section's heading text, case-insensitive."},
				"body":    map[string]any{"type": "string", "description": "The new section body."},
			},
			"required": []any{"section", "body"},
		},
	}
}

func submitVerdictTool() agent.ToolDef {
	return agent.ToolDef{
		Name: verdictToolName,
		Description: "Submit your review verdict. Call this exactly once at the end of a review " +
			"to drive gummi's automatic review→fix→review loop.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verdict": map[string]any{
					"type": "string",
					"enum": []any{"pass", "changes"},
					"description": "pass = " + verdictPassBlockingFindings + ", ready to verify; " +
						"changes = " + verdictChangesBase + ", bounce back to implement.",
				},
				"summary": map[string]any{"type": "string", "description": "One-line rationale."},
			},
			"required": []any{"verdict"},
		},
	}
}

// critiqueVerdictTool is submit_verdict with the plan-critique's
// outcome vocabulary: there is no code to bounce to yet — "changes"
// sends the plan back for a replan round, "pass" hands it to the
// human's approval gate. Only blocking findings justify "changes";
// nit-level threads ride along to the gate on a pass.
func critiqueVerdictTool() agent.ToolDef {
	return agent.ToolDef{
		Name: verdictToolName,
		Description: "Submit your critique verdict. Call this exactly once at the end of your " +
			"critique to drive gummi's automatic critique→replan loop.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verdict": map[string]any{
					"type": "string",
					"enum": []any{"pass", "changes"},
					"description": "pass = " + verdictPassBlockingFindings + ", ready for the " +
						"user's approval; changes = " + verdictChangesBase + ", the plan must be revised.",
				},
				"summary": map[string]any{"type": "string", "description": "One-line rationale."},
			},
			"required": []any{"verdict"},
		},
	}
}

// verifyVerdictTool is submit_verdict with the Verify stage's outcome
// vocabulary: verification held up (pass), it didn't (fail), or the
// environment couldn't execute the plan at all (blocked) — there is no
// reviewer to negotiate changes with.
func verifyVerdictTool() agent.ToolDef {
	return agent.ToolDef{
		Name: verdictToolName,
		Description: "Submit your verification verdict. Call this exactly once at the end, " +
			"after recording the evidence in the design artifact — gummi gates on it.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verdict": map[string]any{
					"type": "string",
					"enum": []any{"pass", "fail", "blocked"},
					"description": "pass = everything verified, ready to land; fail = verification " +
						"found real problems in this feature's changes; blocked = the environment " +
						"cannot execute the verification plan — name each missing prerequisite in " +
						"your summary and in the artifact.",
				},
				"summary": map[string]any{"type": "string", "description": "One-line rationale."},
			},
			"required": []any{"verdict"},
		},
	}
}

func resolveAnnotationTool() agent.ToolDef {
	return agent.ToolDef{
		Name: resolveToolName,
		Description: "Mark a diff review comment as addressed. Call it once per comment, " +
			"right after you make the edit the comment asks for — the id is the [N] marker " +
			"on the comment's line in your instructions. Unresolved comments keep the " +
			"changes-requested gate blocked.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "integer",
					"description": "The comment's numeric id, from its [N] marker.",
				},
			},
			"required": []any{"id"},
		},
	}
}

// toolHint tells a client-tool-capable agent which gummi tools this
// stage offers and when to use them. Implement/fix stages return no
// hint here: resolve_annotation is explained by the diff-comments turn
// itself (CompileDiffComments), which only exists when there are
// comments to resolve.
func toolHint(stage domain.Stage, flavor runFlavor) string {
	if flavor == flavorRebase {
		return "" // no gummi tools: the rebase outcome is read from git state
	}
	if flavor == flavorCritique {
		return `You have two gummi tools. spec_annotate: attach each finding to the
plan line it indicts and let gummi place the %% marker with correct
anchoring, instead of writing %% lines yourself. submit_verdict: call it
exactly once at the end of your critique (verdict "pass" or "changes")
to drive gummi's critique→replan loop, instead of writing a VERDICT:
line.`
	}
	switch stage {
	case domain.StageBrainstorm, domain.StageSpec, domain.StageTriage, domain.StageDiagnose,
		domain.StageShape:
		return `You have four gummi tools. ask_user: put a decision to the user as a
few options and get their choice back — prefer it over asking in prose
(faster for the user, cheaper); lead with your recommended option,
marked as such in its label; ask one question at a time (parallel
ask_user calls are bounced); pass spec_anchor to have gummi record
the answer into the artifact. spec_annotate: attach an open question to a
line and let gummi place the %% marker with correct anchoring, instead
of writing %% lines yourself. spec_view: read the spec's sections — pass
the section heading text for one section's body, omit it for the whole
document (read-only). spec_replace_section: rewrite a whole section
between its ## heading and the next — heading match is case-insensitive.
The write is a naive splice: re-emit any %% @user: marker lines yourself,
since gummi does not preserve them for you; never include a top-level
## heading in the body.`
	case domain.StageReview:
		return `Read the relevant parts of the design artifact with spec_view (pass a
section heading for one section's body, omit it for the whole document).
Record your findings in the artifact with spec_replace_section: rewrite a
section's body between its ## heading and the next, and re-emit any
%% @user: marker lines yourself. Then call the submit_verdict tool exactly
once at the end of your review (verdict "pass" or "changes") to drive
gummi's review loop, instead of writing a VERDICT: line.`
	case domain.StageVerify:
		return `Record the verification evidence in the design artifact with
spec_view and spec_replace_section (a section's body sits between its ##
heading and the next; re-emit any %% @user: marker lines yourself), then
call the submit_verdict tool exactly once at the end (verdict "pass",
"fail", or "blocked") instead of writing a VERDICT: line — gummi gates
on it.`
	case domain.StagePlan:
		return `Write the plan into the design artifact with spec_view (pass a
section heading for one section's body, omit it for the whole document)
and spec_replace_section: rewrite the Implementation notes section body
between its ## heading and the next, and re-emit any %% @user: marker
lines yourself. Each replan round rewrites the section wholesale.`
	default:
		return ""
	}
}

// askConventionHint is the fallback for backends without client tools:
// the agent emits a fenced block gummi parses into the same picker.
const askConventionHint = "When you need a decision from the user, end your message with a fenced " +
	"block tagged `gummi-ask` containing JSON: " +
	"{\"question\":\"…\",\"options\":[{\"label\":\"…\",\"detail\":\"…\"}]," +
	"\"multi_select\":false,\"allow_free_form\":true,\"spec_anchor\":\"…\"}. " +
	"gummi shows the user a picker and delivers their answer as the next message. " +
	"Ask about one decision at a time."

// unattendedAskHint is appended for a card running on GateFull, whichever
// way it asks. On that mode nobody is at the keyboard: gummi takes the
// agent's own recommended option and the run carries on. The agent is
// told so plainly, because a recommendation that will be acted on
// unread has to be defensible in a way a recommendation someone is
// about to weigh does not — and because the alternative, letting it
// believe a human is reading, would make its own reasoning wrong.
//
// It is deliberately not an instruction to stop asking. The question is
// still the record of what was decided and why, and it is what the run's
// receipt is built from; suppressing it would buy nothing and lose that.
const unattendedAskHint = "This card is running unattended: no one will read your question " +
	"before it is answered. gummi takes your recommended option automatically and the run " +
	"continues, so make sure the option you mark as recommended is the one you would defend " +
	"on the evidence you have, and put the reason in its detail. Ask anyway when a decision " +
	"is real — the question and the answer taken are recorded for the user to read afterwards."

// handleClientTool routes a client-tool invocation. ask_user parses into
// a pending question surfaced to the UI/inbox; an unparseable ask or an
// unknown tool is resolved immediately with an error result so the
// agent's turn never hangs on a call gummi won't answer.
func (e *Engine) handleClientTool(s *Session, tc *agent.ToolCall) {
	if tc == nil {
		return
	}
	// Defense in depth: a read-only research session refuses the stripped
	// mutating tools outright, so a hand-crafted MCP call that names them
	// cannot rewrite the artifact even though filterReadOnlyTools kept
	// them out of the advertised surface (and no prompt told the model
	// they exist).
	if s.ReadOnly && (tc.Name == specReplaceSectionToolName || tc.Name == annotateToolName) {
		e.resolveNow(s, tc.ID, "read-only research session: "+tc.Name+" is not available")
		return
	}
	switch tc.Name {
	case askToolName:
		e.handleAsk(s, tc)
	case annotateToolName:
		e.handleAnnotate(s, tc)
	case verdictToolName:
		e.handleVerdict(s, tc)
	case resolveToolName:
		e.handleResolveAnnotation(s, tc)
	case specViewToolName:
		e.handleSpecView(s, tc)
	case specReplaceSectionToolName:
		e.handleSpecReplaceSection(s, tc)
	default:
		e.resolveNow(s, tc.ID, fmt.Sprintf("unknown tool %q — proceed without it", tc.Name))
	}
}

// DispatchClientTool executes a client tool on a live session from the
// MCP bridge, blocking until the tool resolves. It generates the engine-
// side call id, registers a waiter on the session, and funnels the call
// through the exact handleClientTool path a native ClientTools backend
// exercises, so ask_user/verdict/resolve behaviours are identical. It
// returns exactly one of the tool's result string or ctx.Err(); never a
// nil-error empty success.
func (e *Engine) DispatchClientTool(ctx context.Context, s *Session, name string, args json.RawMessage) (string, error) {
	callID := fmt.Sprintf("mcp-%d", e.mcpSeq.Add(1))
	ch := s.registerResolver(callID)
	// mark the waiter live before handling so Answer can distinguish a
	// still-blocked bridge call from a stale one whose backend is gone
	// (see Answer; a buffered channel cannot falsify delivery).
	s.markResolverWaiting(callID)
	e.handleClientTool(s, &agent.ToolCall{ID: callID, Name: name, Args: args})
	select {
	case result := <-ch:
		return result, nil
	case <-ctx.Done():
		// the caller went away: mark the waiter as no longer receiving
		// (but still registered) so Answer can see it gave up and will
		// not drop the answer into a buffer nobody reads, then drop the
		// waiter so a late resolve is a no-op (the backend, not a
		// ToolResolver, is silent on it). The cleared liveness flag is
		// what tells Answer the difference between "still parked in the
		// select" and "gone".
		s.clearResolverWaiting(callID)
		s.takeResolver(callID)
		return "", ctx.Err()
	}
}

// handleAsk turns an ask_user call into a pending question (blocks the
// agent's turn until Answer). One question at a time: a parallel ask_user
// while another is pending is bounced with an immediate result — letting
// it displace the pending ask would orphan that call's blocked tool
// handler, hanging the agent's turn until the session dies.
func (e *Engine) handleAsk(s *Session, tc *agent.ToolCall) {
	ask, err := parseAsk(tc.ID, tc.Args)
	if err != nil {
		e.resolveNow(s, tc.ID, err.Error()+" — ask again with valid arguments, or proceed")
		return
	}
	if !s.trySetPendingAsk(ask) {
		e.resolveNow(s, tc.ID, "the user is still answering your previous question — "+
			"ask one question at a time; re-ask this after that answer arrives")
		return
	}
	e.persist(s)
	// the agent is waiting on the user, not the model; drop the busy spinner
	s.setBusy(false)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventQuestion})
}

// handleAnnotate writes a %% marker onto a spec line and resolves the
// call immediately — a mechanical write, no human involved.
func (e *Engine) handleAnnotate(s *Session, tc *agent.ToolCall) {
	var a struct {
		Anchor string `json:"anchor"`
		Note   string `json:"note"`
	}
	if err := json.Unmarshal(tc.Args, &a); err != nil || strings.TrimSpace(a.Anchor) == "" || strings.TrimSpace(a.Note) == "" {
		e.resolveNow(s, tc.ID, "spec_annotate needs an anchor and a note")
		return
	}
	path := s.SpecPath()
	// Serialize the full read → annotate → write against the UI comment path
	// and the answer-capture path, which touch the same file; and write
	// atomically so a crash can't tear the artifact.
	unlock := spec.LockFile(path)
	defer unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		e.resolveNow(s, tc.ID, "could not read the spec: "+err.Error())
		return
	}
	line, ok := spec.FindAnchor(string(raw), a.Anchor)
	if !ok {
		e.resolveNow(s, tc.ID, fmt.Sprintf("anchor %q not found or not unique — write the %%%% marker yourself", a.Anchor))
		return
	}
	out, err := spec.AddComment(string(raw), line, string(s.Role), e.now().Format("2006-01-02"), a.Note)
	if err != nil {
		e.resolveNow(s, tc.ID, "could not annotate: "+err.Error())
		return
	}
	if err := atomicfile.Write(path, []byte(out), 0o600); err != nil {
		e.resolveNow(s, tc.ID, "could not write the spec: "+err.Error())
		return
	}
	s.appendActivity("annotated spec: " + a.Note)
	e.persist(s)
	e.resolveNow(s, tc.ID, "annotation added to the spec")
}

// handleSpecView resolves a spec_view call with a section body (or the
// whole document) read directly from disk. Read-only: no lock, no
// activity, no persist — spec_view may return pre- or post-write bytes
// during a concurrent write, never a torn mix, because the writer swaps
// the inode atomically.
func (e *Engine) handleSpecView(s *Session, tc *agent.ToolCall) {
	var a struct {
		Section string `json:"section"`
	}
	_ = json.Unmarshal(tc.Args, &a)
	raw, err := os.ReadFile(s.SpecPath())
	if err != nil {
		e.resolveNow(s, tc.ID, "could not read the spec: "+err.Error())
		return
	}
	body, ok := spec.ViewSection(string(raw), a.Section)
	if !ok {
		e.resolveNow(s, tc.ID, fmt.Sprintf("spec_view: unknown section %q", a.Section))
		return
	}
	e.resolveNow(s, tc.ID, body)
}

// handleSpecReplaceSection swaps a section's body under the same
// lock + atomic-write path as annotate, emits one activity note, and
// resolves the call immediately. On any error the call resolves with the
// error and the file on disk is byte-identical.
func (e *Engine) handleSpecReplaceSection(s *Session, tc *agent.ToolCall) {
	var a struct {
		Section string `json:"section"`
		Body    string `json:"body"`
	}
	if err := json.Unmarshal(tc.Args, &a); err != nil || strings.TrimSpace(a.Section) == "" {
		e.resolveNow(s, tc.ID, "spec_replace_section needs a section name")
		return
	}
	path := s.SpecPath()
	unlock := spec.LockFile(path)
	defer unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		e.resolveNow(s, tc.ID, "could not read the spec: "+err.Error())
		return
	}
	// Heal any headings a pre-normalization splice welded mid-line before
	// applying this write, so a corrupted artifact recovers through the same
	// mediated path that once damaged it.
	raw = []byte(spec.HealWeldedHeadings(string(raw)))
	out, matchedTitle, err := spec.ReplaceSection(string(raw), a.Section, a.Body)
	if err != nil {
		e.resolveNow(s, tc.ID, err.Error())
		return
	}
	if err := atomicfile.Write(path, []byte(out), 0o600); err != nil {
		e.resolveNow(s, tc.ID, "could not write the spec: "+err.Error())
		return
	}
	note := "updated " + matchedTitle + " section"
	s.appendActivity(note)
	e.persist(s)
	e.resolveNow(s, tc.ID, note)
}

// handleResolveAnnotation flips a diff review comment to resolved and
// resolves the call immediately — a mechanical store write, no human
// involved. The id must belong to this feature, so an agent can never
// resolve another feature's comments.
func (e *Engine) handleResolveAnnotation(s *Session, tc *agent.ToolCall) {
	var a struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(tc.Args, &a); err != nil || a.ID == 0 {
		e.resolveNow(s, tc.ID, "resolve_annotation needs the comment's numeric id (its [N] marker)")
		return
	}
	if e.cfg.Store == nil {
		e.resolveNow(s, tc.ID, "no annotation store — nothing to resolve")
		return
	}
	ctx := context.Background()
	anns, err := e.cfg.Store.ListDiffAnnotations(ctx, s.Feature.ID)
	if err != nil {
		e.resolveNow(s, tc.ID, "could not read diff comments: "+err.Error())
		return
	}
	var ann *domain.DiffAnnotation
	open := 0
	for i := range anns {
		if !anns[i].Resolved {
			open++
		}
		if anns[i].ID == a.ID {
			ann = &anns[i]
		}
	}
	if ann == nil {
		e.resolveNow(s, tc.ID, fmt.Sprintf("no diff comment with id %d on this feature", a.ID))
		return
	}
	if ann.Resolved {
		e.resolveNow(s, tc.ID, fmt.Sprintf("comment [%d] was already resolved", a.ID))
		return
	}
	if err := e.cfg.Store.SetDiffAnnotationResolved(ctx, a.ID, true); err != nil {
		e.resolveNow(s, tc.ID, "could not resolve the comment: "+err.Error())
		return
	}
	open--
	s.appendActivity(fmt.Sprintf("resolved diff comment [%d]: %s", a.ID, ann.Comment))
	e.persist(s)
	// nudge the UI: open-count badges on the card and diff surface burn down
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventAnnotations})
	e.resolveNow(s, tc.ID, fmt.Sprintf("comment [%d] resolved — %d still open", a.ID, open))
}

// allowedVerdicts is the verdict vocabulary of the session's contract:
// critique and review negotiate changes; verify reports pass/fail and
// may declare the environment unable to run the plan (blocked). Scoping
// per session keeps one stage's vocabulary from leaking into another's
// loop — a review "blocked" or a verify "changes" is a contract
// violation, bounced back to the agent to retry. Stages that offer no
// submit_verdict tool return nil, so a stray call is refused instead of
// silently accepted (the fallthrough previously admitted a "fail"
// verdict on Review, which no downstream loop distinguished from
// "changes").
func allowedVerdicts(s *Session) []string {
	switch {
	case s.Feature.Stage == domain.StageVerify:
		return []string{"pass", "fail", "blocked"}
	case s.Critique:
		return []string{"pass", "changes"}
	case s.Feature.Stage == domain.StageReview:
		return []string{"pass", "changes"}
	default:
		return nil
	}
}

// handleVerdict records a review verdict and resolves immediately. The
// review loop prefers this structured verdict over parsing prose.
func (e *Engine) handleVerdict(s *Session, tc *agent.ToolCall) {
	var v struct {
		Verdict string `json:"verdict"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(tc.Args, &v); err != nil {
		e.resolveNow(s, tc.ID, "submit_verdict needs a verdict")
		return
	}
	verdict := strings.ToLower(strings.TrimSpace(v.Verdict))
	allowed := allowedVerdicts(s)
	if len(allowed) == 0 {
		e.resolveNow(s, tc.ID, "this stage does not accept a verdict")
		return
	}
	if !slices.Contains(allowed, verdict) {
		e.resolveNow(s, tc.ID, `verdict must be one of "`+strings.Join(allowed, `", "`)+`"`)
		return
	}
	s.setVerdict(verdict)
	note := "verdict: " + verdict
	if v.Summary != "" {
		note += " — " + v.Summary
	}
	s.appendActivity(note)
	e.persist(s)
	e.resolveNow(s, tc.ID, "verdict recorded")
}

// resolveNow answers a tool call directly (no user involved), for calls
// gummi declines to route. A registered MCP waiter for the call id wins
// over the backend's ToolResolver, so an in-flight MCP dispatch resolves
// to its own waiter; otherwise a backend without ToolResolver simply
// drops the result (best-effort).
func (e *Engine) resolveNow(s *Session, callID, result string) {
	if ch, ok := s.takeResolver(callID); ok {
		select {
		case ch <- result:
		default: // a waiter that already gave up: drop it
		}
		return
	}
	if r, ok := s.agent().(agent.ToolResolver); ok {
		_ = r.Resolve(context.Background(), callID, result)
	}
}

// parseAsk decodes an ask_user tool call's arguments into an Ask.
func parseAsk(callID string, args json.RawMessage) (*Ask, error) {
	var a Ask
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("ask_user args: %w", err)
	}
	a.CallID = callID
	if strings.TrimSpace(a.Question) == "" || len(a.Options) == 0 {
		return nil, fmt.Errorf("ask_user needs a question and at least one option")
	}
	return &a, nil
}

// askFenceRe matches a ```gummi-ask … ``` block (the convention-path
// carrier for backends without client tools).
var askFenceRe = regexp.MustCompile("(?s)```gummi-ask\\s*(.*?)```")

// parseAskConvention extracts a gummi-ask block from an assistant
// message, returning the parsed ask and the message with the block
// stripped. ok is false when there is no well-formed block.
func parseAskConvention(text string) (ask *Ask, stripped string, ok bool) {
	m := askFenceRe.FindStringSubmatchIndex(text)
	if m == nil {
		return nil, text, false
	}
	body := text[m[2]:m[3]]
	a, err := parseAsk("", json.RawMessage(strings.TrimSpace(body)))
	if err != nil {
		return nil, text, false // malformed block: leave it as prose
	}
	stripped = strings.TrimSpace(text[:m[0]] + text[m[1]:])
	return a, stripped, true
}

// maybeConventionAsk checks a just-idle non-client-tool session's last
// assistant message for a gummi-ask block; if present it becomes the
// pending question (block stripped from the transcript) and reports true
// so the caller surfaces EventQuestion instead of a plain idle.
func (e *Engine) maybeConventionAsk(s *Session) bool {
	// only interactive stages carry the convention hint and have a picker
	// to answer with (cf. tool gating in newAgentSession).
	if !s.Interactive || s.ClientTools() {
		return false
	}
	last, idx := s.lastAssistant()
	if idx < 0 {
		return false
	}
	ask, stripped, ok := parseAskConvention(last)
	if !ok {
		return false
	}
	s.replaceMessage(idx, stripped)
	s.setPendingAsk(ask)
	return true
}

// Answer resolves a feature's open ask_user question with the user's
// chosen text, records it in the transcript, and — when the ask carried
// a spec anchor — writes it into the spec as a resolved marker. The
// model's blocked turn resumes with the answer as the tool's result.
func (e *Engine) Answer(ctx context.Context, id domain.FeatureID, answer string) error {
	s := e.Get(id)
	if s == nil {
		return fmt.Errorf("no session for %s", id)
	}
	ask := s.takePendingAsk()
	if ask == nil {
		return fmt.Errorf("%s has no open question", id)
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		s.trySetPendingAsk(ask) // keep it open; don't clobber a newer ask
		return fmt.Errorf("empty answer")
	}

	// record the exchange so the transcript and any restore read cleanly
	s.appendUser(answer)
	// best-effort spec capture; a bad anchor never blocks the answer
	if note := e.captureAnswer(s, ask, answer); note != "" {
		s.appendActivity(note)
	}
	e.persist(s)
	e.send(Event{Feature: id, Stage: s.Feature.Stage, Kind: EventUpdated})

	// resolve the blocked tool call if the backend supports it; otherwise
	// deliver the answer as a normal turn (the convention path). An MCP
	// dispatch — a non-ClientTools backend bridged over the session
	// socket — resolves via its registered waiter channel first, so the
	// bridge's blocked call resumes exactly like a native one.
	if ask.CallID != "" {
		// Only treat the bridge's blocked call as resolved when it is
		// actually live. A buffered send alone proves nothing: the backend
		// behind the call may be gone, leaving the answer in a buffer
		// nobody reads. In that case restore the question and fail loudly
		// instead of claiming a success that never lands. The liveness is
		// read before takeResolver: DispatchClientTool marks the waiter
		// live on entry and clears it again in its ctx.Done branch, so a
		// waiter that has given up reads as not-waiting here while its
		// resolver is still registered.
		waiting := s.resolverWaiting(ask.CallID)
		if ch, ok := s.takeResolver(ask.CallID); ok {
			if !waiting {
				s.trySetPendingAsk(ask)
				return fmt.Errorf("answer for %s not delivered: the agent is no longer waiting on the question", ask.CallID)
			}
			select {
			case ch <- answer:
				return nil
			default: // live waiter but the buffer is unexpectedly full
				s.trySetPendingAsk(ask)
				return fmt.Errorf("answer for %s not delivered: the agent's blocked call could not accept it", ask.CallID)
			}
		}
		a := s.agent()
		if r, ok := a.(agent.ToolResolver); ok {
			if err := r.Resolve(ctx, ask.CallID, answer); err != nil {
				// Resolve failed: restore the question so the user can retry,
				// rather than leaving the agent's blocked tool call orphaned to
				// hang the turn. trySet avoids clobbering a newer ask.
				s.trySetPendingAsk(ask)
				return err
			}
			return nil
		}
	}
	return e.Send(ctx, id, answer)
}

// captureAnswer writes the answer into the spec under the ask's anchor,
// returning an activity note describing what happened (empty when there
// was no anchor to write). Failures degrade to a note, never an error:
// the answer already reached the agent.
func (e *Engine) captureAnswer(s *Session, ask *Ask, answer string) string {
	anchor := strings.TrimSpace(ask.SpecAnchor)
	if anchor == "" {
		return ""
	}
	path := s.SpecPath()
	if path == "" {
		return ""
	}
	unlock := spec.LockFile(path)
	defer unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return "spec capture skipped: " + err.Error()
	}
	line, ok := spec.FindAnchor(string(raw), anchor)
	if !ok {
		return fmt.Sprintf("spec capture skipped: %q not found (or not unique) — note it in the spec yourself", anchor)
	}
	out, err := spec.AddComment(string(raw), line, "user", e.now().Format("2006-01-02"), "resolved — "+answer)
	if err != nil {
		return "spec capture skipped: " + err.Error()
	}
	if err := atomicfile.Write(path, []byte(out), 0o600); err != nil {
		return "spec capture failed: " + err.Error()
	}
	return AnswerCapturedNote
}

// AnswerCapturedNote is the activity note captureAnswer records when an
// ask_user answer with a spec_anchor lands as a resolved %% marker. The
// chat surface folds it into the answer's own bubble rather than showing
// both the answer and this note (the answer would otherwise read twice).
const AnswerCapturedNote = "recorded your answer in the spec"
