package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
)

// askToolName is the client tool the agent calls to put a multiple-choice
// question to the user. gummi surfaces it as an inline picker (attached)
// or a needs-attention item (detached/autonomous), then feeds the chosen
// answer back as the tool's result so the model's turn resumes in-context.
const askToolName = "ask_user"

// Ask is a parsed ask_user invocation awaiting the user's answer.
//
// There is no free-form flag. Every question gummi puts to a person
// accepts their own words as well as its options — Engine.Answer takes
// arbitrary text and hands it back as the tool's result either way, so a
// flag that could turn the channel off would only ever hide an
// affordance that still worked. The model owns the question and the
// options; whether the person may answer in a sentence is gummi's, and
// the answer is always yes.
type Ask struct {
	CallID     string      // the agent-side tool-call id, for Resolve
	Question   string      `json:"question"`
	Options    []AskOption `json:"options"`
	MultiPick  bool        `json:"multi_select"`
	SpecAnchor string      `json:"spec_anchor"`
	// ChangesSection names the artifact section whose CONTENT differs
	// depending on the answer. It is the design stage's proof that the
	// question is a decision rather than a confirmation, and it must name
	// a section that stage decides rather than one a later stage rewrites
	// regardless — see askChangesSomething.
	ChangesSection string `json:"changes_section"`
	// Gate marks this question AS the stage's gate: answering it is the
	// crossing, rather than a decision the stage then acts on itself.
	//
	// The model supplies the sentence and the anchor — what it is good at,
	// and what makes the gate read as part of the conversation instead of
	// a control to go find. gummi supplies the options (gateAskOptions),
	// because what "yes" means has to be reliable and a model-authored
	// option list is not. That split is the whole reason this is a flag
	// rather than a convention about option order.
	Gate bool `json:"gate"`
	// DecisionID is the identity of the durable decision this ask opened
	// (the decision_open row's id): the tool-call id, or a minted,
	// generation-scoped stand-in on the convention path. It correlates the
	// answer event to its open decision, and it is what a restored ask
	// re-arms under — the answer then closes the same record the ask
	// opened. Engine-side only; never parsed from or persisted to the
	// tool arguments.
	DecisionID string `json:"-"`
	// Outlived marks a question whose tool call ended before anyone
	// answered it: the backend stopped waiting and the agent's turn closed
	// with the question still up (see Engine.askOutlivedItsCall). Its
	// answer travels as a turn, to the session that asked.
	Outlived bool `json:"-"`
	// Permission marks this ask AS a guarded tool-call approval the
	// backend itself raised, not the agent's own question: its options are
	// gummi's approve/deny, its answer is a ruling delivered through the
	// session's PermissionResolver by request id (the id in CallID), and
	// it never rides a turn while the held call is still live — the turn
	// continues server-side the moment the ruling lands. Free-form words
	// that are not the approve option deny: the held call takes a ruling,
	// and the only one typed words can be is no.
	Permission bool `json:"-"`
	// Restored marks a question re-armed from its durable decision after
	// the process that asked it died (Engine.openAskFor). Its options died
	// with that process — a decision row never stores them (DESIGN §10
	// D18) — so it carries none, and the surfaces that offer it say why
	// rather than presenting a bare chat row as all the agent offered.
	Restored bool `json:"-"`
	// onAnswer, when set, turns the person's answer into what the agent's
	// blocked call returns: the tool that asked acts on the answer itself
	// (card_create mints the card) and tells the agent what happened. It
	// lives only in this process — an ask restored after a restart has
	// none, and the agent then gets the person's answer as plain text.
	onAnswer func(answer string) string
}

// AskOption is one selectable answer.
type AskOption struct {
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

// UnmarshalJSON accepts an option written as a bare string as well as the
// documented {label, detail} object. The tool schema asks for objects, but
// a model (or a scripted backend) that sends ["yes", "no"] is asking a
// perfectly clear question, and rejecting it at the boundary is the worst
// outcome available: the tool error goes back to the agent, the agent
// carries on without the answer, and the person at the keyboard never
// sees that a question was asked at all. A bare string is the label.
func (o *AskOption) UnmarshalJSON(b []byte) error {
	var label string
	if err := json.Unmarshal(b, &label); err == nil {
		o.Label, o.Detail = label, ""
		return nil
	}
	type plain AskOption
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*o = AskOption(p)
	return nil
}

// RecommendedOption picks the option the agent flagged as recommended —
// by convention it marks that option's label ("… (recommended)") — and
// falls back to the first option when none is marked. It is the single
// implementation both the TUI and the headless driver take an unattended
// answer from: the value surfaced on the question event/notice, and the
// answer an autonomous loop auto-takes when it may not park (DESIGN §10
// decision 17). Living beside Ask itself, rather than in either loop,
// is what keeps the two from drifting into two different ideas of
// "recommended".
func RecommendedOption(a *Ask) string {
	if a == nil || len(a.Options) == 0 {
		return ""
	}
	for _, o := range a.Options {
		if strings.Contains(strings.ToLower(o.Label), "recommend") {
			return o.Label
		}
	}
	return a.Options[0].Label
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
func stageTools(stage domain.Stage, flavor runFlavor, deciding []string) []agent.ToolDef {
	switch flavor {
	case flavorCritique:
		// spec_view because the artifact lives beside the workspace, not
		// in the worktree: a backend that cages its file tools to the
		// worktree (opencode denies every other directory) left the
		// critique no way to read the plan it exists to refute, and it
		// spent its turns probing the cage instead
		return []agent.ToolDef{critiqueVerdictTool(), specAnnotateTool(), specViewTool()}
	case flavorRebase:
		return nil
	}
	switch stage {
	case domain.StagePlan:
		// the design stage: it converges with the user and writes the
		// artifact, so it keeps ask_user and the annotation tools the
		// three design stages used to share between them.
		return []agent.ToolDef{askUserTool(deciding), specAnnotateTool(), specViewTool(), specReplaceSectionTool()}
	case domain.StageVerify:
		return []agent.ToolDef{verifyVerdictTool(), specViewTool(), specReplaceSectionTool()}
	case domain.StageImplement:
		return []agent.ToolDef{resolveAnnotationTool(), specViewTool(), specReplaceSectionTool()}
	case domain.StageOpen:
		// A freeform card (DESIGN §19): ask_user and resolve_annotation. It
		// gets resolve_annotation because the reader's diff comments are
		// exactly how work is steered on such a card, and the count has to
		// burn down as they are addressed. It gets ask_user (the no-
		// artifact form, below) because a freeform card is otherwise an
		// ordinary coding-agent session, and an ordinary coding-agent
		// session that needs a decision asks for one instead of guessing —
		// the person being in the thread already is not a reason to take
		// its one way of stopping and waiting away. It gets no spec tools:
		// it has no artifact to read or write.
		return []agent.ToolDef{askUserToolFreeform(), resolveAnnotationTool()}
	default:
		return nil
	}
}

// A read-only research session used to be served a STRIPPED gummi surface:
// spec_replace_section and spec_annotate were removed from opts.Tools, from
// MCP tools/list and from the prompt's tool hint, on the reasoning that a
// read-only pass "must never mutate the main checkout".
//
// They never could. Both are gummi-mediated: they write the card's artifact
// at its workspace home (.gummi/research/<id>.md), through the engine, and
// have no path to the repository at all. What protects the operator's
// checkout is the ADAPTER's read-only mode — the backend's own file and
// shell tools, which Engine.run refuses to start a research session without
// (Capabilities().ReadOnlyEnforce). Stripping gummi's own document tools
// added nothing to that guarantee and took away the only way the stage
// could do its job.
//
// The cost was total. A research card's build stage is the survey: its
// entire deliverable is the Findings section, its own contract tells it to
// "record your findings and open questions in the research document as you
// go", and researchReadOnly covers every stage except plan — so the stage
// was instructed to write and served nothing that writes. On the lxd
// autopilot drive case C ran three times, spent 428.8 credits across 84
// greps and reads, wrote nothing, and was failed by its own verify for the
// section it had no tool to fill; the recommended recovery (--bounce, and
// the card page's "draft the missing section") returned it to the same
// stage each time. Its critique could not file a finding either, for the
// same reason.
//
// So there is no filter. A stage's gummi surface is stageTools, read-only
// or not, and the read-only contract means what it says: the repository.

// askUserTool builds the ask_user definition. deciding is this spec's own
// list of sections an answer could change, which the description names
// outright when it is known.
//
// It is named rather than described in the abstract because the runtime
// refuses a design-stage question that does not carry changes_section,
// and the refusal's whole value was listing the headings the model
// should have chosen from. The schema said the field was optional and
// the runtime said it was required, so the model learned the rule by
// being refused: measured on one goal, eight of ten questions were
// asked twice — a wasted call, a wasted turn and a refusal message
// each — to arrive at a list gummi could have handed over up front.
func askUserTool(deciding []string) agent.ToolDef {
	return agent.ToolDef{
		Name: askToolName,
		Description: "Ask the user a question with a small set of options and wait for their " +
			"answer. Use this whenever you need a decision from the user: it is cheaper and " +
			"clearer than asking in prose. Returns the chosen option(s) — or, since every " +
			"question also offers to talk it over, whatever the user wrote instead. Read the " +
			"result as an answer in their own words when it matches none of your options.",
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
				"multi_select": map[string]any{"type": "boolean", "description": "Allow choosing more than one option."},
				"gate": map[string]any{
					"type": "boolean",
					"description": "Set true ONLY for the question that closes an attended stage — " +
						"\"may this card move on?\". gummi replaces the options with its own " +
						"(move on / not yet / say what is wrong), because answering a gate IS the " +
						"crossing. Write the question and the spec_anchor; leave the choices to gummi.",
				},
				"spec_anchor": map[string]any{
					"type": "string",
					"description": "Optional: a unique snippet of a spec line this decision belongs to. " +
						"gummi records the answer as a resolved %% marker under it.",
				},
				"changes_section": changesSectionSchema(deciding),
			},
			"required": []any{"question", "options"},
		},
	}
}

// askUserToolFreeform is ask_user stripped to what a card with no
// artifact and no stage can use: no changes_section (there is no
// section for an answer to change the content of), no gate (a freeform
// card has no stage to cross) and no spec_anchor (nothing to anchor
// into). What is left is the whole of what a plain coding-agent
// session's "ask the user" tool needs — question, options, multi-select.
func askUserToolFreeform() agent.ToolDef {
	return agent.ToolDef{
		Name: askToolName,
		Description: "Ask the user a question with a small set of options and wait for their " +
			"answer. Use this whenever you need a decision from the user: it is cheaper and " +
			"clearer than asking in prose. Returns the chosen option(s) — or, since every " +
			"question also offers to talk it over, whatever the user wrote instead. Read the " +
			"result as an answer in their own words when it matches none of your options.",
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
				"multi_select": map[string]any{"type": "boolean", "description": "Allow choosing more than one option."},
			},
			"required": []any{"question", "options"},
		},
	}
}

// changesSectionDescription names this spec's own deciding sections when
// they are known, and falls back to the general rule when they are not.
func changesSectionDescription(deciding []string) string {
	base := "The name of the spec section whose CONTENT would be different depending on " +
		"which option is chosen. It must be a section this stage decides — the Verification " +
		"plan, Progress and Review are rewritten by later stages, so an answer that only " +
		"changes one of those changes nothing about the work and is your call, not the " +
		"user's. If you cannot name a deciding section, this is not a decision for the " +
		"user — record your recommendation in the spec and carry on without asking."
	if len(deciding) == 0 {
		return "At the design stage, required. " + base +
			" (e.g. \"Chosen approach\", \"Out of scope\", \"Implementation notes\")"
	}
	return "Required unless gate is true. " + base +
		" This spec's deciding sections are: " + strings.Join(deciding, ", ") + "."
}

// changesSectionSchema constrains the field to this spec's own sections, so
// a backend that validates arguments catches a wrong one before the call
// is made rather than after it is refused. Nil when the sections are not
// known, which leaves the field unconstrained rather than guessing.
//
// The field stays out of "required" on purpose. A gate ask IS the
// crossing rather than a decision inside it, carries no changes_section,
// and is exempted by the runtime for that reason; a schema demanding one
// outright would make the model invent a section for a question that
// changes none. Naming the sections and the exemption in the description
// is the part that costs nothing and was missing.
func changesSectionSchema(deciding []string) map[string]any {
	out := map[string]any{
		"type":        "string",
		"description": changesSectionDescription(deciding),
	}
	if len(deciding) == 0 {
		// No enum key at all rather than an empty one: a typed nil under
		// an interface is not absent, and a schema carrying "enum": null
		// is a schema some backend will read as "no value is valid".
		return out
	}
	vals := make([]any, 0, len(deciding))
	for _, h := range deciding {
		vals = append(vals, h)
	}
	out["enum"] = vals
	return out
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
		return `You have three gummi tools. spec_view: read the spec's sections —
pass the section heading text for one section's body, omit it for the
whole document (read-only); read the plan with it rather than opening
the spec file, which may sit outside what your file tools can reach.
spec_annotate: attach each finding to the plan line it indicts and let
gummi place the %% marker with correct anchoring, instead of writing %%
lines yourself. submit_verdict: call it exactly once at the end of your
critique (verdict "pass" or "changes") to drive gummi's critique→replan
loop, instead of writing a VERDICT: line.`
	}
	switch stage {
	case domain.StagePlan:
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
	case domain.StageVerify:
		return `Record the verification evidence in the design artifact with
spec_view and spec_replace_section (a section's body sits between its ##
heading and the next; re-emit any %% @user: marker lines yourself), then
call the submit_verdict tool exactly once at the end (verdict "pass",
"fail", or "blocked") instead of writing a VERDICT: line — gummi gates
on it.`
	case domain.StageOpen:
		return `You have two gummi tools. ask_user: put a decision to the user as a
few options and get their choice back — prefer it over asking in prose
(faster for the user, cheaper); lead with your recommended option, marked
as such in its label; ask one question at a time (parallel ask_user calls
are bounced). This card has no gate and no artifact, so leave gate and
changes_section unset. resolve_annotation: mark a diff review comment
addressed — call it once per comment, right after you make the edit it
asks for.`
	default:
		return ""
	}
}

// askConventionHint is the fallback for backends without client tools:
// the agent emits a fenced block gummi parses into the same picker.
const askConventionHint = "When you need a decision from the user, end your message with a fenced " +
	"block tagged `gummi-ask` containing JSON: " +
	"{\"question\":\"…\",\"options\":[{\"label\":\"…\",\"detail\":\"…\"}]," +
	"\"multi_select\":false,\"spec_anchor\":\"…\"}. " +
	"gummi shows the user a picker — which always also offers to talk it over — and " +
	"delivers their answer as the next message, in their own words if that is how they " +
	"gave it. " +
	"Ask about one decision at a time."

// unattendedAskHint is appended for a card running on GateAutopilot, whichever
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
	case watchToolName, unwatchToolName:
		e.handleWatchTool(s, tc)
	case memoryReadToolName, memoryWriteToolName:
		e.handleMemoryTool(s, tc)
	case cardCreateToolName, cardListToolName, cardLandToolName:
		e.handleDelegateTool(s, tc)
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

// AskBouncedNote prefixes the activity line a bounced ask_user leaves on
// the card. A rejected ask is answered straight back to the model as a
// tool error and the agent carries on without the answer, so without this
// the only trace of a question that never reached the reader lived in the
// backend's own log: the card showed a bare ask_user tool line, no
// question, and a run that appeared to hang for no stated reason. Three
// separate investigations have started from that silence.
const AskBouncedNote = "question not put to you"

// bounceAsk rejects an ask_user call back to the model AND records why on
// the card, so a question that never became a decision is visible to the
// person the question was for. The activity line is best-effort narration
// beside the tool call; resolveNow still owns the actual rejection.
func (e *Engine) bounceAsk(s *Session, callID, reason string) {
	s.appendActivity(AskBouncedNote + ": " + reason)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventUpdated})
	e.resolveNow(s, callID, reason)
}

// askChangesSomething is the design stage's toll on asking a person
// something: name the artifact section whose content the answer changes,
// or do not ask.
//
// The hints have always said this in prose — "confirmations are not
// decisions", "every question you ask a person stops the work and waits" —
// and a model that has just written a recommendation still asks whether
// the recommendation is acceptable. Prose cannot hold that line, because
// the model is not disobeying: asking feels cooperative. The check makes
// the claim checkable instead. A question whose answer changes the Chosen
// approach, or Out of scope, or Implementation notes, names that section
// and goes through. A question whose answer changes nothing names nothing,
// and the model is told to write its recommendation down and continue —
// which is what it would have done with the answer anyway.
//
// The section must be one the design stage DECIDES, not one a later stage
// rewrites regardless (spec.SectionDecidesWork). Naming any section at all
// turned out to be too low a bar: "which interfaces should the tests
// exercise?" truthfully changes the Verification plan and changes nothing
// about what gets built, so it passed a toll meant to stop exactly that
// question. A testing question is the stage's own call.
//
// It binds only where the cost is a full process restart and only to the
// stage that owns the artifact's sections: the design stage, and never a
// gate ask, which IS the crossing rather than a decision inside it.
func askChangesSomething(s *Session, ask *Ask) (string, bool) {
	if s == nil || ask == nil || ask.Gate || s.Feature.Stage != domain.StagePlan {
		return "", true
	}
	raw, err := os.ReadFile(s.SpecPath())
	if err != nil {
		// no artifact to point at is not the model's fault; asking is the
		// best it can do
		return "", true
	}
	headings := spec.DecidingHeadings(string(raw))
	if len(headings) == 0 {
		return "", true
	}
	named := strings.TrimSpace(ask.ChangesSection)
	if named == "" {
		return "this question does not say what it changes. Pass changes_section — the spec " +
			"section whose content differs depending on the answer (one of: " +
			strings.Join(headings, ", ") + "). If no section would differ, it is a confirmation, " +
			"not a decision: write your recommended answer into the spec where the user can see " +
			"and change it, and carry on without asking", false
	}
	if _, ok := spec.HeadingLine(string(raw), named); !ok {
		return fmt.Sprintf("changes_section %q is not a section of this spec. Use one of: %s — "+
			"or, if the answer would change none of them, record your recommendation in the spec "+
			"and carry on without asking", named, strings.Join(headings, ", ")), false
	}
	if !spec.SectionDecidesWork(named) {
		return fmt.Sprintf("%q is not a section this question can justify itself with: it is "+
			"rewritten by the stage that does the work, so an answer that only changes it "+
			"changes nothing about what gets built. Decide it yourself, write the decision "+
			"there, and carry on. Ask only if the answer changes one of: %s",
			named, strings.Join(headings, ", ")), false
	}
	return "", true
}

// handleAsk turns an ask_user call into a pending question (blocks the
// agent's turn until Answer). One question at a time: a parallel ask_user
// while another is pending is bounced with an immediate result — letting
// it displace the pending ask would orphan that call's blocked tool
// handler, hanging the agent's turn until the session dies.
func (e *Engine) handleAsk(s *Session, tc *agent.ToolCall) {
	ask, err := parseAsk(tc.ID, tc.Args)
	if err != nil {
		e.bounceAsk(s, tc.ID, err.Error()+" — ask again with valid arguments, or proceed")
		return
	}
	if ask.Gate && s.Feature.Stage == domain.StageOpen {
		// A freeform card has no stage to cross, so "gate" has nothing to
		// mean here — the tool schema omits it, but a bare JSON call could
		// still set it, and parseAsk has already swapped in the gate's own
		// options by this point. Bounce rather than let an advance/hold
		// picker stand in for a plain question nothing will act on.
		e.bounceAsk(s, tc.ID, "a freeform card has no gate to cross — ask again without gate")
		return
	}
	if reason, ok := askChangesSomething(s, ask); !ok {
		e.bounceAsk(s, tc.ID, reason)
		return
	}
	e.installAsk(s, tc.ID, ask)
}

// installAsk puts a parsed ask to the person: the pending question, its
// durable decision row and the event that surfaces it. ask_user is one
// caller; a tool that needs the person's yes first (card_create, under a
// freeform card's delegation) is the other.
func (e *Engine) installAsk(s *Session, callID string, ask *Ask) {
	// mint the decision id before the ask installs: the pump goroutine owns
	// the ask's identity until takePendingAsk hands it over, and minting
	// after install would race the answer that reads it.
	ask.DecisionID = decisionIDFor(s, ask)
	if !s.trySetPendingAsk(ask) {
		e.bounceAsk(s, callID, "the user is still answering your previous question — "+
			"ask one question at a time; re-ask this after that answer arrives")
		return
	}
	// the question is now blocking a human: the open decision's durable
	// row goes down in the same breath, on both the tool and convention
	// paths, TUI and driver alike — this is the one seam every ask_user
	// passes through (DESIGN §10.18: nothing may block a card without
	// leaving a row).
	// the question is now blocking a human: the open decision's durable
	// row goes down in the same breath, on both the tool and convention
	// paths, TUI and driver alike — this is the one seam every ask_user
	// passes through (DESIGN §10.18: nothing may block a card without
	// leaving a row). It sits after the spinner drop so the render pass
	// that sees the pending ask does not also see a busy spinner.
	s.setBusy(false)
	e.openAskDecision(s, ask)
	e.persist(s)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventQuestion})
}

// decisionIDFor mints the ask's durable identity: the tool-call id on the
// tool path, scoped to the session generation so a fresh headless process
// minting the same call id (mcpSeq always restarts at 1) never collides
// with a prior generation's already-answered decision, or a
// generation-scoped stand-in on the convention path — a natural key (the
// question alone) would no-op the same question re-asked after a bounce.
func decisionIDFor(s *Session, ask *Ask) string {
	if ask.CallID != "" {
		return "call:" + strconv.FormatInt(s.startedAt.UnixNano(), 10) + ":" + ask.CallID
	}
	return askDecisionID(s, ask.Question)
}

// openAskDecision records the blocking ask as a durable decision_open
// under the id minted before the ask installed. Best-effort, like every
// mirror write: the pending ask is already installed, and the log must
// never break a live question.
func (e *Engine) openAskDecision(s *Session, ask *Ask) {
	if e.cfg.Store == nil || ask.DecisionID == "" {
		return
	}
	_ = e.cfg.Store.OpenDecision(context.Background(), s.Feature.ID, s.Feature.Stage,
		state.DecisionPayload{
			ID: ask.DecisionID, Kind: state.DecisionKindAsk,
			Question: ask.Question, Multi: ask.MultiPick,
			Anchor: ask.SpecAnchor,
		}, e.now())
}

// askDecisionID mints a convention-path ask's decision id, scoped to the
// session generation so two generations of the same question never share
// a dedupe key.
func askDecisionID(s *Session, question string) string {
	return "ask:" + strconv.FormatInt(s.startedAt.UnixNano(), 10) + ":" + question
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

// AskOutlivedNote is the activity line left on a card whose agent stopped
// waiting on its own question.
const AskOutlivedNote = "the agent stopped waiting for your answer — the question stays open, " +
	"and your answer reaches it as its next turn"

// askOutlivedReply is what a bridge call still parked on the question is
// released with. The backend that made the call has usually stopped
// listening for it; a client that has not reads this as the call's result.
const askOutlivedReply = "the question is still open with the user — their answer will arrive " +
	"as your next message; do not ask it again"

// askOutlivedItsCall handles a turn that ended while its question was
// still open, reporting whether that is what happened.
//
// ask_user blocks the agent's turn on a person, and a person may take an
// hour. The backends between gummi and the model do not wait an hour: an
// MCP client bounds every tool call (a minute, on opencode and codex), and
// the engine's side of the bridge is never told the client gave up. The
// model reads "timed out", asks again, is bounced because the first
// question is still up, says the question is with the user and ends its
// turn. That idle used to read as the design stage finishing: the critique
// replaced the session, the question was filed "unanswered, superseded"
// under the eyes of the person reading it, and the gate then held the card
// for the sections the answer was supposed to decide.
//
// A turn that ended on an open question has finished nothing. The card
// stays where it is, parked on the question, and the ask is cut loose from
// its dead call so the answer is delivered as a turn rather than resolved
// into a call nobody is waiting on — which would also raise the spinner
// over a session with no turn running.
func (e *Engine) askOutlivedItsCall(s *Session) bool {
	waiter, ok := s.outlivePendingAsk()
	if !ok {
		return false
	}
	if waiter != nil {
		select {
		case waiter <- askOutlivedReply:
		default:
		}
	}
	s.appendActivity(AskOutlivedNote)
	return true
}

// GateAdvanceLabel is the option that crosses the gate. The UI matches an
// answer against it, so it is an exported constant rather than prose
// either side is free to reword.
const GateAdvanceLabel = "Yes, move on"

// gateHoldLabel is the option that leaves the card where it is.
const gateHoldLabel = "Not yet"

// GateAskOptions are the choices every gate offers, whatever stage asked
// and whatever words it asked in. gummi supplies them rather than the
// model: "yes" has to mean the crossing, every time.
func GateAskOptions() []AskOption {
	return []AskOption{
		{Label: GateAdvanceLabel, Detail: "cross the gate and start what is behind it"},
		{Label: gateHoldLabel, Detail: "leave the card here — nothing moves"},
	}
}

// PermissionApproveLabel and PermissionDenyLabel are the options a held
// tool call offers. The wording is gummi's own, like a gate's: what
// "Approve" does has to be reliable, and the labels are what both faces
// render and what an answer is matched against.
const (
	PermissionApproveLabel = "Approve"
	PermissionDenyLabel    = "Deny"
)

// PermissionAskOptions are the choices a guarded tool call's decision
// offers. There is no recommended row: a permission is the operator's
// call, not one autopilot takes for them (§10.17 — an approval widens
// what runs).
func PermissionAskOptions() []AskOption {
	return []AskOption{
		{Label: PermissionApproveLabel, Detail: "let this tool call run"},
		{Label: PermissionDenyLabel, Detail: "refuse this tool call — the model sees the refusal and can go another way"},
	}
}

// permissionQuestion is the question a held tool call puts up: the tool,
// then what it was about to touch, when the backend said.
func permissionQuestion(tool, detail string) string {
	if detail == "" {
		return "Allow " + tool + "?"
	}
	return "Allow " + tool + " — " + detail + "?"
}

// handlePermissionEvent turns a guarded tool-call approval into the card's
// open decision, on the same machinery an ask_user question rides: one
// open decision at a time, a durable row the moment it is installed, and
// the answer delivered through the ask answer route to the session's
// resolver by request id. The turn continues when it is answered —
// server-side, where the call was held.
//
// A permission arriving while another decision is open is refused at
// once rather than parked: two things cannot both hold the card, and the
// refused call's model sees the refusal and can go another way.
func (e *Engine) handlePermissionEvent(s *Session, ev agent.Event) {
	if ev.CallID == "" {
		return // nothing to answer by request id; refusing blind helps nobody
	}
	ask := &Ask{
		CallID:     ev.CallID,
		Question:   permissionQuestion(ev.Tool, ev.Detail),
		Options:    PermissionAskOptions(),
		Permission: true,
	}
	ask.DecisionID = decisionIDFor(s, ask)
	if !s.trySetPendingAsk(ask) {
		s.appendActivity("tool call refused while another decision is open: " +
			permissionQuestion(ev.Tool, ev.Detail))
		e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventUpdated})
		if r, ok := s.agent().(agent.PermissionResolver); ok {
			_ = r.ResolvePermission(context.Background(), ev.CallID, false)
		}
		return
	}
	// the call is now holding a human: the open decision's durable row
	// goes down in the same breath, and the spinner drops the same way an
	// ask_user's does — the turn is blocked, and the decision is what the
	// card is doing.
	s.setBusy(false)
	e.openAskDecision(s, ask)
	e.persist(s)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventQuestion})
}

// parseAsk decodes an ask_user tool call's arguments into an Ask.
func parseAsk(callID string, args json.RawMessage) (*Ask, error) {
	var a Ask
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("ask_user args: %w", err)
	}
	a.CallID = callID
	if a.Gate {
		// gummi owns a gate's options; whatever the model sent is
		// replaced, so a gate can never offer a choice gummi does not
		// know how to honour. (The free-form channel needs no replacing:
		// every ask carries it — see Ask.)
		a.Options = GateAskOptions()
		a.MultiPick = false
	}
	if strings.TrimSpace(a.Question) == "" || len(a.Options) == 0 {
		return nil, fmt.Errorf("ask_user needs a question and at least one option")
	}
	// Every option must carry a label. An unlabelled option is not a
	// cosmetic problem: the label IS the answer text the picker delivers
	// (decisionAnswerText, internal/ui), so a blank one renders as a row
	// enter can never answer, and the card parks forever on a question
	// with no reachable answer. Reject it here, at the boundary, where the
	// agent still gets a tool error it can correct — rather than letting a
	// malformed call become an unanswerable picker.
	for i, o := range a.Options {
		if strings.TrimSpace(o.Label) == "" {
			return nil, fmt.Errorf("ask_user option %d has no label", i+1)
		}
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
	// mint the decision id before the ask installs, for the same
	// ownership the tool path keeps (see decisionIDFor).
	ask.DecisionID = decisionIDFor(s, ask)
	s.replaceMessage(idx, stripped)
	s.setPendingAsk(ask)
	e.openAskDecision(s, ask)
	return true
}

// Answer resolves a feature's open ask_user question with the user's
// chosen text, records it in the transcript, and — when the ask carried
// a spec anchor — writes it into the spec as a resolved marker. The
// model's blocked turn resumes with the answer as the tool's result.
// The answerer is the person who chose it (state.ActorUser); the
// unattended loop answers through AnswerAs.
func (e *Engine) Answer(ctx context.Context, id domain.FeatureID, answer string) error {
	return e.AnswerAs(ctx, id, answer, state.ActorUser)
}

// AnswerAs is Answer with the answerer declared: ActorUser for an answer
// a person chose, ActorAutopilot for one the unattended loop took by
// itself. The actor is the caller's to declare, not the engine's to
// infer from the card's stored gate-approval mode — a headless
// --autonomous run on a card stored at "gates" takes its own answers,
// and the record must say so, or the morning receipt silently
// under-counts what ran unattended (DESIGN §6.3).
func (e *Engine) AnswerAs(ctx context.Context, id domain.FeatureID, answer, by string) error {
	mu, _ := e.answering.LoadOrStore(id, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	s := e.Get(id)
	// e.live never holds a freeform card's session (DESIGN §19: no
	// nothing scheduled) — its own session lives in

	// e.freeform instead, and this is the one place that difference would
	// otherwise matter: an ask_user call blocks on a person exactly the
	// same way there, and the answer has to reach it the same way.
	var ff *FreeformSession
	if s == nil {
		if ff = e.Freeform(id); ff != nil {
			s = ff.Session()
		}
	}
	if s == nil {
		return fmt.Errorf("no session for %s", id)
	}
	open := s.Snapshot().PendingAsk
	if open == nil {
		return fmt.Errorf("%s has no open question", id)
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return fmt.Errorf("empty answer")
	}
	// A permission's answer is a ruling on a held tool call, not a reply
	// the turn consumes: the call stays blocked server-side until the
	// ruling lands through the session's own resolver, and the turn
	// continues on its own — nothing rides a turn, nothing echoes into
	// the transcript. Any words that are not the approve option's own
	// deny, so the only reading a held call can take is the one typed.
	// A session that is gone (a pause, a restart), or an ask carried over
	// one (its request id cleared), cannot take a ruling — its answer
	// falls through to the restoration machinery below.
	if open.Permission && open.CallID != "" {
		if s.Live() {
			if r, ok := s.agent().(agent.PermissionResolver); ok {
				ask := s.takePendingAsk()
				return e.answerPermission(ctx, s, ask, answer, by, r)
			}
		}
	}
	// Where the answer goes is settled before anything is recorded: an
	// answer written to the transcript, the card's log and the spec that
	// then reaches nobody is an answer the record says was given. The log
	// closes the decision on it, so the question the person is still
	// looking at is gone after the next restart, and the spec carries a
	// choice the agent never heard.
	byCall := open.CallID != "" && s.callLive(open.CallID)
	if open.CallID != "" && !byCall && s.takesTurns() {
		// the turn that asked is still running, but its client stopped
		// waiting on the call: the turn's own end cuts the question loose
		// (askOutlivedItsCall), and from then on it rides a turn
		return fmt.Errorf("answer for %s not delivered: the agent stopped waiting on the question "+
			"and is finishing its turn — answer again when it has", id)
	}
	// a run whose backend is gone is run again, carrying the answer; a
	// conversation is attached to again, and the answer is its next turn
	rerun := false
	if !byCall && !s.takesTurns() {
		switch {
		case ff != nil:
			// A freeform card's own reconnection: a fresh backend carrying
			// this session's own transcript (or resuming it natively),
			// exactly as its next ordinary turn would spawn one.
			// Engine.Attach is the stage session's reconnection path (the
			// headless driver's `resume --answer`, the TUI's enter) and
			// does not apply to a card with no stage to resume into.
			ns, err := ff.ensureBackend(ctx)
			if err != nil {
				return err
			}
			s = ns
		case s.Interactive:
			ns, err := e.reattachForAnswer(ctx, s)
			if err != nil {
				return err
			}
			s = ns
		default:
			rerun = true
		}
	}
	ask := s.takePendingAsk()
	if ask == nil {
		return fmt.Errorf("%s has no open question", id)
	}

	// record the exchange so the transcript and any restore read cleanly.
	// appendUserAs rather than appendUser: the echo carries who answered,
	// so the card-event mirror can leave a machine-taken answer out of
	// the log the stretch derivation reads (an unattributed echo lands
	// there as a user message and reads as a person taking the card
	// back). A human's answer is stamped with ActorUser and mirrors on.
	s.appendUserAs(answer, by)
	// best-effort card-event log capture, actor included, so the decision
	// receipt can tell an autopilot-taken answer from a typed one; recorded
	// before delivery, the same way the transcript line above is, once the
	// route above has found something that can take the answer.
	e.appendAskEvent(s, ask, answer, by)
	// best-effort spec capture; a bad anchor never blocks the answer
	if note := e.captureAnswer(s, ask, answer, by); note != "" {
		s.appendActivity(note)
	}
	e.persist(s)
	e.send(Event{Feature: id, Stage: s.Feature.Stage, Kind: EventUpdated})

	// resolve the blocked tool call if the backend supports it; otherwise
	// deliver the answer as a normal turn (the convention path, and the
	// only path a restored ask has — its blocked call died with the
	// process). An MCP dispatch — a non-ClientTools backend bridged over
	// the session socket — resolves via its registered waiter channel
	// first, so the bridge's blocked call resumes exactly like a native
	// one. The turn carries the answer text; the transcript above already
	// recorded it, so delivery must not append it a second time.
	if byCall && ask.onAnswer != nil {
		answer = ask.onAnswer(answer)
	}
	if byCall {
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
				e.resumeAfterAnswer(s)
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
			e.resumeAfterAnswer(s)
			return nil
		}
	}
	// the convention path — and the only path a restored ask has: the
	// blocked call and its resolver died with the process, so the answer
	// rides a fresh turn. The transcript above already recorded it; the
	// turn must deliver it without appending it a second time.
	//
	// What rides the wire is not the bare answer. A restored ask reaches a
	// backend session that never asked the question — the process that did
	// is gone — so "A. Subcommand-scoped flag" arrives with nothing to
	// attach to, and the session's only way forward is to re-read the
	// artifact and re-derive the repo from scratch. reentryTurn restates
	// the exchange the answer belongs to; the transcript still shows the
	// person's own words.
	if rerun {
		// the run that asked is over; the stage runs again, and its
		// kickoff carries the exchange to a session that did not ask
		carried := *ask
		carried.CallID, carried.Outlived = "", false
		turn := reentryTurn(&carried, answer)
		if s.isExhausted() {
			// ...unless it stopped on its budget. The answer stands, but
			// running the stage again is the top-up's call: it widens the
			// card's reach, and autopilot may never take it (§10.17). The
			// exchange waits for the run the top-up starts, and the card
			// is left on its budget stop.
			s.holdAnswerForNextRun(turn)
			return nil
		}
		if err := e.run(s.Feature, turn, s.flavor()); err != nil {
			s.trySetPendingAsk(ask)
			return err
		}
		return nil
	}
	if err := e.deliverTurn(ctx, s, reentryTurn(ask, answer), nil); err != nil {
		// Restore the question, exactly as every other failing branch
		// above does. This is the branch a restored ask always takes, and
		// deliverTurn refuses a session with no agent behind it — which a
		// restored one never has until something attaches. Returning the
		// error without putting the question back consumed the ask and
		// recorded an answer that reached nobody: the card was left with
		// nothing open to answer and no agent to answer it, and the
		// person who had just typed the answer was told only that the
		// card was "still starting".
		s.trySetPendingAsk(ask)
		return err
	}
	return nil
}

// answerPermission delivers a permission ruling to the session's resolver
// and records it: the activity line and the ask-answer event close the
// decision the held call opened, and the turn continues server-side on
// its own. A ruling the resolver could not take restores the ask, exactly
// as every other failing answer branch does.
func (e *Engine) answerPermission(ctx context.Context, s *Session, ask *Ask, answer, by string, r agent.PermissionResolver) error {
	if ask == nil {
		return fmt.Errorf("%s has no open question", s.Feature.ID)
	}
	approve := answer == PermissionApproveLabel
	if err := r.ResolvePermission(ctx, ask.CallID, approve); err != nil {
		s.trySetPendingAsk(ask)
		return fmt.Errorf("answer for %s not delivered: %w", s.Feature.ID, err)
	}
	verb := "denied"
	if approve {
		verb = "approved"
	}
	s.appendActivity("tool call " + verb + ": " + strings.TrimPrefix(ask.Question, "Allow "))
	e.appendAskEvent(s, ask, answer, by)
	e.persist(s)
	e.resumeAfterAnswer(s)
	return nil
}

// reattachForAnswer brings a backend up behind a conversation's question
// whose answer has to ride a turn and whose session can take none: one
// restored after a restart, one stopped under its question, or one whose
// backend died or failed its turn while the question was up. It is the
// attach the headless driver does before `resume --answer` and the TUI
// does on enter; the web face has no such step, and every answer given
// there was refused ("still starting", "no longer waiting")
// while the question stayed up asking for one. (A run in the same state
// is run again instead — see AnswerAs — so it carries on to its gate.)
//
// The question travels: Attach carries the pending ask, the transcript
// and the backend's conversation id over to the new session. It is
// carried as a question the new session did not ask — cut from any call
// the old one made, and not outlived, whose guidance tells the session
// that asked to carry on from where it left off — because a new backend
// session did not ask it, whatever conversation it resumes.
func (e *Engine) reattachForAnswer(ctx context.Context, s *Session) (*Session, error) {
	// a stopped or dead backend still hangs off the session, and Attach
	// would reuse it; stop is idempotent and closes it for good
	s.stop()
	s.clearAgent()
	ns, err := e.Attach(ctx, s.Feature)
	if err != nil {
		return nil, err
	}
	if ask := ns.takePendingAsk(); ask != nil {
		carried := *ask
		carried.CallID, carried.Outlived = "", false
		ns.setPendingAsk(&carried)
	}
	return ns, nil
}

// reentryTurn renders the turn that carries a restored ask's answer into a
// session that did not ask the question.
//
// The live path never needs this: the answer resolves the agent's own
// blocked tool call, so the question is right there in its context. A
// restored ask has no such call — the headless loop exits at every
// question and `gummi resume --answer` starts a new backend session — and
// the bare answer string was all that session received. It could not tell
// which question it answered, what the alternatives had been, or what the
// session before it had already established, so it re-read the artifact
// and re-explored the repository before it could act. Restating the
// exchange costs a few dozen tokens once; rediscovering a repository costs
// several tool calls every round trip.
//
// An ask with no question text (a malformed or legacy record) degrades to
// the bare answer rather than framing an exchange that cannot be quoted.
func reentryTurn(ask *Ask, answer string) string {
	if ask == nil || strings.TrimSpace(ask.Question) == "" {
		return answer
	}
	var b strings.Builder
	fmt.Fprintf(&b, "You asked: %s\n", strings.TrimSpace(ask.Question))
	if opts := askOptionLabels(ask); len(opts) > 0 {
		fmt.Fprintf(&b, "You offered: %s\n", strings.Join(opts, " · "))
	}
	fmt.Fprintf(&b, "The answer is: %s\n\n", answer)
	if ask.Outlived {
		b.WriteString(outlivedGuidance)
	} else {
		b.WriteString(reentryGuidance)
	}
	return b.String()
}

// outlivedGuidance is the standing half of the turn that answers a
// question whose call ended first. The session is the one that asked, so
// none of reentryGuidance is true of it; what it needs to be told is that
// this message is the result its call never returned.
const outlivedGuidance = `Your ask_user call stopped waiting before the user answered, so their
answer arrives as this message instead of as that call's result. The
question is settled: do not ask it again. Act on the answer and carry on
with the stage from where you left it.`

// reentryGuidance is the standing half of a re-entry turn: what this
// session is, and what it should not spend turns on. Fixed text — it does
// not vary by stage or kind, because the situation does not.
const reentryGuidance = `This is a fresh session. The one that asked has ended, along with
everything it had read, so nothing you did earlier is in your context —
but its work is not lost: it is written down in the artifact, which is
the record both sessions share. Read the artifact for what has already
been settled, resolve the thread this answer closes, and continue from
there. Do not re-ask a question the artifact already answers, and do not
re-derive decisions it already records.`

// askOptionLabels lists an ask's option labels for the re-entry turn,
// skipping empties so a partially-filled option list cannot produce a
// line of separators.
func askOptionLabels(ask *Ask) []string {
	out := make([]string, 0, len(ask.Options))
	for _, o := range ask.Options {
		if l := strings.TrimSpace(o.Label); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// resumeAfterAnswer marks the session working again once the answer has
// unblocked the agent's call, and says so on the stream.
//
// handleAsk drops the working flag when the question goes up, which is
// right — a card waiting on a person is not working, and a spinner
// beside a question you have not answered is a lie. But the two resolve
// paths hand the turn straight back to the agent without going through
// deliverTurn, which is where every other dispatch sets the flag. So the
// flag stayed down over a turn that was very much alive: from the moment
// you answered, the thread showed no spinner and no running label, and a
// run that was busy thinking read exactly like one that had stopped.
// Sending a message appeared to restart it only because Send sets the
// flag on its way past.
func (e *Engine) resumeAfterAnswer(s *Session) {
	s.setBusy(true)
	e.persist(s)
	e.send(Event{Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: EventUpdated})
}

// appendAskEvent records an answered ask_user question in the card's own
// event log, actor included — the raw material the decision receipt
// (internal/ui's receipt.go) counts as "took N answers", only for an
// autopilot-taken one. The answerer declares itself (by) instead of the
// event inferring it from the card's stored gate-approval mode: the
// headless driver auto-answers off its own --autonomous flag, so a mode
// read here recorded a machine-taken answer as a human's whenever the
// card's stored mode disagreed with how the run was actually being
// driven, and the receipt silently under-counted what ran unattended.
// The answerer's word is the record. Best-effort, like every other
// event write; deduped on the decision id the ask opened, so a
// redelivered Answer for the same question can never double-count.
func (e *Engine) appendAskEvent(s *Session, ask *Ask, answer, by string) {
	if e.cfg.Store == nil {
		return
	}
	choice := ""
	for _, o := range ask.Options {
		if o.Label == answer {
			choice = o.Label
			break
		}
	}
	payload, err := json.Marshal(state.AskPayload{
		Question: ask.Question, Answer: answer,
		Actor: by, By: by,
		ID: ask.DecisionID, Choice: choice,
	})
	if err != nil {
		return
	}
	dedupe := ""
	if ask.DecisionID != "" {
		dedupe = "decision:" + ask.DecisionID
	} else if ask.CallID != "" {
		dedupe = ask.CallID + ":ask"
	}
	_ = e.cfg.Store.AppendEvent(context.Background(), state.CardEvent{
		Feature: s.Feature.ID, Stage: s.Feature.Stage, Kind: state.EventAsk, At: e.now(),
		Payload: string(payload), Dedupe: dedupe,
	})
}

// answerAuthor maps the declared answerer onto the `%%` marker author the
// answer is filed under. It exists because the artifact is the one record
// of a decision that outlives the run: a reviewer reads the spec on the
// branch long after the stream and the card page are gone, and a later
// stage re-reads it as settled. Filing an answer autopilot took by itself
// as `@user` told both of them a person had weighed it, and on this
// drive's case A the architect duly wrote "confirmed by the user" into
// Chosen approach about a question no human ever saw.
//
// The answerer is already declared — AnswerAs carries it, appendAskEvent
// records it, and the card page renders "autopilot answered …" from it.
// Only this one write was hardcoded.
//
// It also lands the marker on the right side of spec.Parse's author rule:
// a `@user` marker closes only under a `@user` resolution, so an
// autopilot resolution can no longer silently close a human's own comment
// sharing the anchor — which is the floor that rule was added for.
func answerAuthor(by string) string {
	if by == state.ActorAutopilot {
		return state.ActorAutopilot
	}
	return state.ActorUser
}

// captureAnswer writes the answer into the spec under the ask's anchor,
// returning an activity note describing what happened (empty when there
// was no anchor to write). Failures degrade to a note, never an error:
// the answer already reached the agent.
//
// A missing or no-longer-unique anchor (spec.FindAnchor fails closed on
// either) used to drop the answer on the floor: the note said "skipped"
// and nothing was written, so the one place the workflow promises the
// decision lives — the artifact — never got it, and the only trace was
// a terse technical line ("spec capture skipped: ...") landing in the
// user's transcript looking like part of the conversation. Both halves
// are wrong: the answer must land SOMEWHERE deterministic even when the
// named anchor cannot be found, and whatever reaches the transcript
// must say what happened to the user's answer in plain words, not name
// gummi-internal machinery ("spec capture", "anchor") the user never
// agreed to know about.
//
// The deterministic fallback is the end of the document: AddComment
// only requires a line within range, and "the last line" is the one
// position that is always valid and always the same place, so an
// answer that cannot go where the model asked for still goes somewhere
// a person (or the next stage) will find it on their next pass over
// the artifact, tagged with the anchor text that missed so it can be
// moved by hand.
func (e *Engine) captureAnswer(s *Session, ask *Ask, answer, by string) string {
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
		return AnswerNotSavedPrefix + err.Error()
	}
	// a named person's answer (the web face) says who, as their spec
	// notes do: the author stays the word every rule reads as a human's
	date := spec.Stamp(e.now().Format("2006-01-02"), state.PersonName(by))
	content := string(raw)
	line, ok := spec.FindAnchor(content, anchor)
	text := "resolved — " + answer
	fallbackWhere := "at the end of the document"
	if !ok {
		// FindAnchor fails closed on zero matches (the line moved or was
		// edited away since the question was asked) and on multiple
		// matches (the snippet is no longer unique) alike — either way
		// there is no single line left to resolve, so fail open instead:
		// append a fresh note rather than resolving one that cannot be
		// found.
		lines := strings.Split(content, "\n")
		line = len(lines)
		// The end of the document is the fallback that is always valid;
		// it is not the fallback that is always useful. A design-stage ask
		// names the section its answer changes (changes_section), and that
		// is a second, better address for an answer whose anchor has moved:
		// the end of the right section is somewhere a reader is actually
		// looking, while the end of the document is where notes go to be
		// missed. The document end remains the backstop under it.
		if sec := strings.TrimSpace(ask.ChangesSection); sec != "" {
			if last, found := spec.SectionLastLine(content, sec); found {
				line, fallbackWhere = last, "at the end of "+sec
			}
		}
		// Still spelled as a RESOLUTION. The happy path above writes
		// "resolved — <answer>", which spec.Parse closes; this branch used
		// to write "answered …" instead, which Parse reads as a fresh open
		// @user thread — and an open @user thread shuts the approval gate
		// that only a human can reopen. So answering gummi's own question
		// filed a blocking comment against the person who answered it, at
		// the end of a document where they would not think to look (round 3
		// §1.3; the plan reviewer hit it too and called it "leftover
		// bookkeeping" it had no way to clear). The fallback POSITION is
		// right and stays; only its wording was gating the card.
		text = fmt.Sprintf("resolved — %s (recorded here: the line this answered, %q, is no longer in the document)", answer, anchor)
	}
	out, err := spec.AddComment(content, line, answerAuthor(by), date, text)
	if err != nil {
		return AnswerNotSavedPrefix + err.Error()
	}
	if err := atomicfile.Write(path, []byte(out), 0o600); err != nil {
		return AnswerNotSavedPrefix + err.Error()
	}
	if !ok {
		return fmt.Sprintf("%s%q — that text no longer matches a single line there, so it was "+
			"appended %s instead", AnswerAppendedPrefix, anchor, fallbackWhere)
	}
	return AnswerCapturedNote
}

// The three notes captureAnswer can record, and the one predicate that
// recognizes any of them.
//
// They exist as exported constants because the chat surface has to tell
// an answer note apart from an ordinary tool line to render either one
// correctly: the clean note is folded into the answer's own bubble (the
// answer would otherwise read twice), while the two unhappy notes stay
// on screen — they are the only place the user learns their answer did
// not go where the question said it would. Either way the bubble carries
// the outcome, so two answers to the same question never render one with
// a status and the other with nothing.
//
// Matching on prose is not ideal, but the alternative is a typed field on
// every transcript message for a distinction only this one surface makes.
// Keeping the notes' spellings here, next to the code that writes them,
// is what stops the two from drifting apart silently.
const (
	// AnswerCapturedNote: the answer landed as a resolved %% marker at
	// the anchor the ask named. The happy path.
	AnswerCapturedNote = "recorded your answer in the spec"
	// AnswerAppendedPrefix: the anchor no longer matched exactly one
	// line, so the answer went to the end of the document instead. Saved,
	// but not where the reader will look for it.
	AnswerAppendedPrefix = "your answer is saved in the spec, but not next to "
	// AnswerNotSavedPrefix: the artifact could not be written at all. The
	// agent still got the answer; the record did not.
	AnswerNotSavedPrefix = "your answer could not be saved to the spec (it still reached the agent): "
)

// IsAnswerNote reports whether an activity note is captureAnswer's, in
// any of its three outcomes — what the chat surface keys its folding and
// its bubble suffix off.
func IsAnswerNote(note string) bool {
	return note == AnswerCapturedNote ||
		strings.HasPrefix(note, AnswerAppendedPrefix) ||
		strings.HasPrefix(note, AnswerNotSavedPrefix)
}

// decidingHeadings is the sections of a card's artifact whose content an
// answer could change — what askChangesSomething holds a question to, and
// therefore what ask_user's schema should have been naming all along.
// Empty when there is no readable artifact yet, which leaves the tool
// described in the general terms it always was.
func (e *Engine) decidingHeadings(f *domain.Feature) []string {
	path := e.artifactFile(f)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return spec.DecidingHeadings(string(raw))
}
