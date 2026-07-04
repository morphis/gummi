package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/morphia/gummi/internal/agent"
	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/spec"
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

// clientTools is the set of gummi-owned tools registered on every
// session whose backend supports client tools.
func clientTools() []agent.ToolDef {
	return []agent.ToolDef{{
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
	}}
}

// askToolHint tells a client-tool-capable agent how gummi routes ask_user.
const askToolHint = `You have an ask_user tool: call it to ask the user a question with a
small set of options and get their choice back. Prefer it over asking
in prose whenever you need a decision — it is faster for the user and
cheaper. Pass spec_anchor (a unique snippet of the spec line the
decision belongs to) to have gummi record the answer into the spec for
you.`

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
	if tc.Name != askToolName {
		e.resolveNow(s, tc.ID, fmt.Sprintf("unknown tool %q — proceed without it", tc.Name))
		return
	}
	ask, err := parseAsk(tc.ID, tc.Args)
	if err != nil {
		e.resolveNow(s, tc.ID, err.Error()+" — ask again with valid arguments, or proceed")
		return
	}
	s.setPendingAsk(ask)
	e.persist(s)
	// the agent is waiting on the user, not the model; drop the busy spinner
	s.setBusy(false)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventQuestion})
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
		s.setPendingAsk(ask) // keep the question open; nothing was chosen
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
		return r.Resolve(ctx, ask.CallID, answer)
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
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		return "spec capture failed: " + err.Error()
	}
	return "recorded your answer in the spec"
}
