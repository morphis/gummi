package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
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
)

// stageTools returns the gummi-owned client tools offered on a stage.
// ask_user is interactive-only (it blocks on a human, and only the chat
// picker can answer it); spec_annotate is offered to the interactive
// architect; submit_verdict to the reviewer; resolve_annotation to the
// implementer/fixer so it can mark diff review comments addressed
// (DESIGN §6.1's resolve event for diffs — always registered on those
// stages because comments can also arrive mid-run as a live turn). The
// non-blocking tools (annotate, verdict, resolve) are gummi-resolved
// immediately, so they are safe on autonomous stages. The plan-critique
// pass reviews the plan and files findings, so it gets both; the
// rebase-resolve pass is judged by git state alone and gets none.
func stageTools(stage domain.Stage, flavor runFlavor) []agent.ToolDef {
	switch flavor {
	case flavorCritique:
		return []agent.ToolDef{submitVerdictTool(), specAnnotateTool()}
	case flavorRebase:
		return nil
	}
	switch stage {
	case domain.StageBrainstorm, domain.StageSpec, domain.StageTriage, domain.StageDiagnose:
		return []agent.ToolDef{askUserTool(), specAnnotateTool()}
	case domain.StageReview:
		return []agent.ToolDef{submitVerdictTool()}
	case domain.StageVerify:
		return []agent.ToolDef{verifyVerdictTool()}
	case domain.StageImplement, domain.StageFix:
		return []agent.ToolDef{resolveAnnotationTool()}
	default:
		return nil
	}
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

func submitVerdictTool() agent.ToolDef {
	return agent.ToolDef{
		Name: verdictToolName,
		Description: "Submit your review verdict. Call this exactly once at the end of a review " +
			"to drive gummi's automatic review→fix→review loop.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verdict": map[string]any{
					"type":        "string",
					"enum":        []any{"pass", "changes"},
					"description": "pass = ready to verify; changes = serious findings, bounce back to implement.",
				},
				"summary": map[string]any{"type": "string", "description": "One-line rationale."},
			},
			"required": []any{"verdict"},
		},
	}
}

// verifyVerdictTool is submit_verdict with the Verify stage's outcome
// vocabulary: verification either held up (pass) or it didn't (fail) —
// there is no reviewer to negotiate changes with.
func verifyVerdictTool() agent.ToolDef {
	return agent.ToolDef{
		Name: verdictToolName,
		Description: "Submit your verification verdict. Call this exactly once at the end, " +
			"after recording the evidence in the design artifact — gummi gates on it.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"verdict": map[string]any{
					"type":        "string",
					"enum":        []any{"pass", "fail"},
					"description": "pass = everything verified, ready to land; fail = verification found real problems.",
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
	case domain.StageBrainstorm, domain.StageSpec, domain.StageTriage, domain.StageDiagnose:
		return `You have two gummi tools. ask_user: put a decision to the user as a
few options and get their choice back — prefer it over asking in prose
(faster for the user, cheaper); lead with your recommended option,
marked as such in its label; ask one question at a time (parallel
ask_user calls are bounced); pass spec_anchor to have gummi record
the answer into the artifact. spec_annotate: attach an open question to a
line and let gummi place the %% marker with correct anchoring, instead
of writing %% lines yourself.`
	case domain.StageReview:
		return `Call the submit_verdict tool exactly once at the end of your review
(verdict "pass" or "changes") to drive gummi's review loop, instead of
writing a VERDICT: line.`
	case domain.StageVerify:
		return `Call the submit_verdict tool exactly once at the end (verdict "pass"
or "fail") instead of writing a VERDICT: line — gummi gates on it.`
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

// handleClientTool routes a client-tool invocation. ask_user parses into
// a pending question surfaced to the UI/inbox; an unparseable ask or an
// unknown tool is resolved immediately with an error result so the
// agent's turn never hangs on a call gummi won't answer.
func (e *Engine) handleClientTool(s *Session, tc *agent.ToolCall) {
	if tc == nil {
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
	default:
		e.resolveNow(s, tc.ID, fmt.Sprintf("unknown tool %q — proceed without it", tc.Name))
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
	raw, err := os.ReadFile(path) //nolint:gosec // gummi-owned spec path
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
	if verdict != "pass" && verdict != "changes" && verdict != "fail" {
		e.resolveNow(s, tc.ID, `verdict must be "pass", "changes", or "fail"`)
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
// gummi declines to route. Best-effort: a backend without ToolResolver
// simply drops it.
func (e *Engine) resolveNow(s *Session, callID, result string) {
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
	if !s.Interactive || e.cfg.Agent == nil || e.cfg.Agent.Capabilities().ClientTools {
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
	// deliver the answer as a normal turn (the convention path).
	a := s.agent()
	if r, ok := a.(agent.ToolResolver); ok && ask.CallID != "" {
		if err := r.Resolve(ctx, ask.CallID, answer); err != nil {
			// Resolve failed: restore the question so the user can retry,
			// rather than leaving the agent's blocked tool call orphaned to
			// hang the turn. trySet avoids clobbering a newer ask.
			s.trySetPendingAsk(ask)
			return err
		}
		return nil
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
	raw, err := os.ReadFile(path) //nolint:gosec // gummi-owned spec path
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
	return "recorded your answer in the spec"
}
