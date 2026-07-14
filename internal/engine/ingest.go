package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
)

// Ingestion (DESIGN §11) decomposes an existing document into a set of
// pre-seeded features. Ingest runs one transient architect pass — like
// Estimate, it is not tracked on the board — that reads the source and
// returns a structured IngestResult. gummi then reviews and materializes
// it (see Materialize). Creating nothing here keeps the human gate real.

// ingestToolName is the client tool the architect calls once with its
// full decomposition. Backends without client tools use the fenced
// gummi-propose convention instead (same JSON, parsed from the reply).
const ingestToolName = "propose_features"

// ingestPrompt drives the decomposition. The source path and the
// granularity/coverage rules live in the system hints; this is the
// go-ahead.
const ingestPrompt = "Read the source document you were pointed at, then decompose it into a set " +
	"of features. Aim for PR-sized vertical slices: each feature cuts a narrow but complete path " +
	"through every layer, is demoable or verifiable on its own as one branch, and fits a single " +
	"fresh agent context window; prefactoring that several slices depend on is its own feature " +
	"the others depend on. Cover the whole document — every requirement " +
	"maps to a feature or is explicitly marked out of scope. For each feature give a title, a " +
	"one-line summary, the source sections it came from, its dependencies on other features, the " +
	"problem it solves, constraints, acceptance criteria, and any open questions. Then submit the " +
	"whole decomposition in one call."

const ingestSourceHint = "You are decomposing an existing spec into features (gummi ingestion). " +
	"The source document is at %s (relative to your working directory); read it first."

const ingestToolHint = "Submit your decomposition by calling the propose_features tool exactly " +
	"once, with every feature and the coverage map. Do not write the features as prose."

const ingestConventionHint = "When your decomposition is ready, emit it as a single fenced block " +
	"tagged `gummi-propose` containing JSON with this shape: " +
	`{"features":[{"title":"…","one_liner":"…","source_refs":["…"],"depends_on":["title of another feature"],` +
	`"skip":{"brainstorm":false,"plan":false},"problem":"…","constraints":"…","acceptance":"…",` +
	`"open_questions":["…"]}],"coverage":[{"requirement":"…","feature":"title","status":"mapped|out-of-scope|unmapped","note":"…"}]}. ` +
	"Emit the block once, at the end, and nothing after it."

// IngestStepKind classifies one step of a running ingest pass.
type IngestStepKind string

const (
	// IngestStepNote is a gummi-side milestone ("architect started").
	IngestStepNote IngestStepKind = "note"
	// IngestStepTool is a tool call the architect made (Text is the
	// adapter's display label, e.g. "read prd.md").
	IngestStepTool IngestStepKind = "tool"
	// IngestStepDelta is an incremental chunk of the architect's
	// streamed commentary (its reply text or its thinking).
	IngestStepDelta IngestStepKind = "delta"
	// IngestStepMessage is a completed commentary message (the
	// authoritative text of what deltas were streaming).
	IngestStepMessage IngestStepKind = "message"
)

// IngestStep is one visible moment of a running ingest pass. The pass is
// transient — it holds no board Session to snapshot — so this stream is
// the only window into what the architect is doing while it decomposes.
type IngestStep struct {
	Kind IngestStepKind
	Text string
}

// Ingest decomposes the document at sourcePath into feature proposals via
// a transient architect pass under the given profile. The source is
// copied into .gummi/ingest/ for provenance and the returned result's
// SourcePath points at that copy. No features are created — that is
// Materialize's job, after the human reviews the proposal.
//
// progress, when non-nil, receives live steps (tool calls, streamed
// commentary) as the pass runs. It is called from the pass's goroutine;
// callers wanting UI updates must hand off to their own loop.
func (e *Engine) Ingest(ctx context.Context, sourcePath, profile string, progress func(IngestStep)) (domain.IngestResult, error) {
	if e.cfg.Agent == nil {
		return domain.IngestResult{}, fmt.Errorf("no agent configured; cannot ingest")
	}
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return domain.IngestResult{}, fmt.Errorf("reading source: %w", err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return domain.IngestResult{}, fmt.Errorf("source %s is empty", sourcePath)
	}
	relPath, absPath, err := e.stashIngestSource(sourcePath, raw)
	if err != nil {
		return domain.IngestResult{}, err
	}

	// one-shot passes dispatch agent turns outside the Session machinery,
	// so they carry their own escape guard (judged when the pass returns).
	// Ingest runs in the main checkout and is bound to no feature yet.
	defer e.armOneShotGuard("", "")()
	model, provider := e.resolveRole(profile, agent.RoleArchitect)
	caps := e.cfg.Agent.Capabilities()
	// point the agent at the absolute stashed path (like Estimate does with
	// the spec) so a backend that resolves relative paths differently than
	// its process CWD still finds it; provenance keeps the repo-relative path.
	hints := []string{fmt.Sprintf(ingestSourceHint, absPath)}
	var tools []agent.ToolDef
	if caps.ClientTools {
		tools = []agent.ToolDef{proposeFeaturesTool()}
		hints = append(hints, ingestToolHint)
	} else {
		hints = append(hints, ingestConventionHint)
	}
	sess, err := e.cfg.Agent.NewSession(ctx, agent.SessionOpts{
		WorkDir:     e.cfg.Worktrees.Root(),
		Role:        agent.RoleArchitect,
		Model:       model,
		Provider:    provider,
		Permission:  e.cfg.Permission,
		SystemHints: hints,
		Tools:       tools,
	})
	if err != nil {
		return domain.IngestResult{}, fmt.Errorf("starting ingest session: %w", err)
	}
	defer func() { _ = sess.Close() }()

	emit := func(k IngestStepKind, text string) {
		if progress != nil {
			progress(IngestStep{Kind: k, Text: text})
		}
	}
	started := "architect reading " + filepath.Base(relPath)
	if model != "" {
		started += " (" + model + ")"
	}
	emit(IngestStepNote, started)

	if err := sess.Send(ctx, ingestPrompt); err != nil {
		return domain.IngestResult{}, err
	}
	res, err := e.collectProposal(ctx, sess, emit)
	if err != nil {
		return domain.IngestResult{}, err
	}
	emit(IngestStepNote, fmt.Sprintf("proposal received — %d feature(s)", len(res.Proposals)))
	res.SourcePath = relPath
	return res, nil
}

// stashIngestSource copies the source into .gummi/ingest/ and returns its
// repo-relative path (the provenance pointer seeded drafts carry) and its
// absolute path (handed to the agent). Two different documents that share
// a basename get distinct names rather than clobbering each other, so a
// prior ingest's provenance never silently resolves to the wrong doc;
// re-stashing an identical document reuses its existing file.
func (e *Engine) stashIngestSource(srcPath string, content []byte) (relPath, absPath string, err error) {
	dir := e.cfg.Workspace.IngestDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", fmt.Errorf("creating ingest dir: %w", err)
	}
	name, err := uniqueIngestName(dir, filepath.Base(srcPath), content)
	if err != nil {
		return "", "", err
	}
	dest := filepath.Join(dir, name)
	if err := atomicfile.Write(dest, content, 0o600); err != nil {
		return "", "", fmt.Errorf("stashing source: %w", err)
	}
	return filepath.Join(".gummi", "ingest", name), dest, nil
}

// uniqueIngestName picks a filename under dir for content: base if free or
// already holding identical content, else base-2, base-3, … so a distinct
// document with the same basename never overwrites an earlier stash.
func uniqueIngestName(dir, base string, content []byte) (string, error) {
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 1; ; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		existing, err := os.ReadFile(filepath.Join(dir, name))
		switch {
		case os.IsNotExist(err):
			return name, nil
		case err != nil:
			return "", err
		case bytes.Equal(existing, content):
			return name, nil // same document already stashed here — reuse it
		}
	}
}

// collectProposal consumes the ingest session until the architect submits
// its decomposition — via the propose_features tool call or, on backends
// without client tools, a gummi-propose block in the reply. Each visible
// step is relayed to emit so the caller can show the pass working.
func (e *Engine) collectProposal(ctx context.Context, sess agent.Session, emit func(IngestStepKind, string)) (domain.IngestResult, error) {
	var text assistantText
	thinking := false // the tail of the streamed feed is reasoning
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return proposalFromText(text.String())
			}
			switch ev.Kind {
			case agent.EventToolCall:
				emit(IngestStepTool, ev.Tool)
			case agent.EventReasoningDelta:
				// thinking is visible progress but never reply text — it
				// must not end up in the proposal parse. A blank line marks
				// the switch between reply text and thinking so the two
				// don't run together in the live feed.
				if !thinking {
					thinking = true
					emit(IngestStepDelta, "\n\n")
				}
				emit(IngestStepDelta, ev.Text)
			case agent.EventClientToolCall:
				if ev.ToolCall == nil {
					continue
				}
				if ev.ToolCall.Name != ingestToolName {
					resolve(ctx, sess, ev.ToolCall.ID, fmt.Sprintf("unknown tool %q", ev.ToolCall.Name))
					continue
				}
				res, err := decodeProposal(ev.ToolCall.Args)
				if err != nil {
					// let the model try again with a valid call
					resolve(ctx, sess, ev.ToolCall.ID, err.Error()+" — call propose_features again with valid JSON")
					continue
				}
				resolve(ctx, sess, ev.ToolCall.ID, fmt.Sprintf("received %d features", len(res.Proposals)))
				return res, nil
			case agent.EventTextDelta:
				if thinking {
					thinking = false
					emit(IngestStepDelta, "\n\n")
				}
				text.delta(ev.Text)
				emit(IngestStepDelta, ev.Text)
			case agent.EventMessage:
				thinking = false
				text.message(ev.Text)
				emit(IngestStepMessage, ev.Text)
			case agent.EventIdle:
				return proposalFromText(text.String())
			case agent.EventError:
				return domain.IngestResult{}, ev.Err
			}
		case <-ctx.Done():
			return domain.IngestResult{}, ctx.Err()
		}
	}
}

// resolve answers a client-tool call on a raw session (best-effort; a
// backend without ToolResolver simply drops it).
func resolve(ctx context.Context, sess agent.Session, callID, result string) {
	if r, ok := sess.(agent.ToolResolver); ok {
		_ = r.Resolve(ctx, callID, result)
	}
}

// proposeFeaturesTool declares the propose_features client tool.
func proposeFeaturesTool() agent.ToolDef {
	feature := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":       map[string]any{"type": "string", "description": "Short feature title."},
			"one_liner":   map[string]any{"type": "string", "description": "One-line summary."},
			"source_refs": strList("Source sections/ranges this feature came from."),
			"depends_on":  strList("Titles of other proposed features this one needs."),
			"skip": map[string]any{
				"type":        "object",
				"description": "Suggested skip flags for a well-specified slice.",
				"properties": map[string]any{
					"brainstorm": map[string]any{"type": "boolean"},
					"plan":       map[string]any{"type": "boolean"},
				},
			},
			"problem":        map[string]any{"type": "string", "description": "What this feature solves (→ spec Problem)."},
			"constraints":    map[string]any{"type": "string", "description": "Constraints/notes (→ Implementation notes)."},
			"acceptance":     map[string]any{"type": "string", "description": "Acceptance criteria (→ Verification plan)."},
			"open_questions": strList("Unresolved questions (→ %% checklist markers)."),
		},
		"required": []any{"title"},
	}
	coverage := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"requirement": map[string]any{"type": "string", "description": "A source requirement or section."},
			"feature":     map[string]any{"type": "string", "description": "Title of the feature covering it (for mapped)."},
			"status":      map[string]any{"type": "string", "enum": []any{"mapped", "out-of-scope", "unmapped"}},
			"note":        map[string]any{"type": "string", "description": "Rationale, esp. for out-of-scope/unmapped."},
		},
		"required": []any{"requirement", "status"},
	}
	return agent.ToolDef{
		Name: ingestToolName,
		Description: "Submit a decomposition of the source document into features. Call once with " +
			"every feature and a coverage map proving the whole document is accounted for.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"features": map[string]any{"type": "array", "description": "The proposed features.", "items": feature},
				"coverage": map[string]any{"type": "array", "description": "Every requirement → feature or out-of-scope.", "items": coverage},
			},
			"required": []any{"features"},
		},
	}
}

func strList(desc string) map[string]any {
	return map[string]any{"type": "array", "description": desc, "items": map[string]any{"type": "string"}}
}

// proposalWire is the JSON the architect emits (tool args or fenced block).
type proposalWire struct {
	Features []struct {
		Title      string   `json:"title"`
		OneLiner   string   `json:"one_liner"`
		SourceRefs []string `json:"source_refs"`
		DependsOn  []string `json:"depends_on"`
		Skip       struct {
			Brainstorm bool `json:"brainstorm"`
			Plan       bool `json:"plan"`
		} `json:"skip"`
		Problem       string   `json:"problem"`
		Constraints   string   `json:"constraints"`
		Acceptance    string   `json:"acceptance"`
		OpenQuestions []string `json:"open_questions"`
	} `json:"features"`
	Coverage []struct {
		Requirement string `json:"requirement"`
		Feature     string `json:"feature"`
		Status      string `json:"status"`
		Note        string `json:"note"`
	} `json:"coverage"`
}

// decodeProposal parses the wire JSON into a domain IngestResult,
// normalizing lists and dropping features without a usable title. At
// least one usable feature is required.
func decodeProposal(data []byte) (domain.IngestResult, error) {
	var w proposalWire
	if err := json.Unmarshal(data, &w); err != nil {
		return domain.IngestResult{}, fmt.Errorf("proposal is not valid JSON: %w", err)
	}
	var res domain.IngestResult
	for _, f := range w.Features {
		title := strings.TrimSpace(f.Title)
		if title == "" {
			continue // unusable: a feature must have a title to be minted
		}
		res.Proposals = append(res.Proposals, domain.FeatureProposal{
			Title:      title,
			OneLiner:   strings.TrimSpace(f.OneLiner),
			SourceRefs: trimNonEmpty(f.SourceRefs),
			DependsOn:  trimNonEmpty(f.DependsOn),
			Skip:       domain.SkipFlags{Brainstorm: f.Skip.Brainstorm, Plan: f.Skip.Plan},
			Draft: domain.DraftSeed{
				Problem:       strings.TrimSpace(f.Problem),
				Constraints:   strings.TrimSpace(f.Constraints),
				Acceptance:    strings.TrimSpace(f.Acceptance),
				OpenQuestions: trimNonEmpty(f.OpenQuestions),
			},
		})
	}
	if len(res.Proposals) == 0 {
		return domain.IngestResult{}, fmt.Errorf("proposal contained no usable features")
	}
	for _, c := range w.Coverage {
		req := strings.TrimSpace(c.Requirement)
		if req == "" {
			continue
		}
		res.Coverage = append(res.Coverage, domain.CoverageEntry{
			Requirement: req,
			Feature:     strings.TrimSpace(c.Feature),
			Status:      coverageStatus(c.Status, c.Feature),
			Note:        strings.TrimSpace(c.Note),
		})
	}
	return res, nil
}

// coverageStatus normalizes the wire status string; an unrecognized value
// falls back to mapped-if-a-feature-is-named, else unmapped, so a fuzzy
// status never silently reads as covered.
func coverageStatus(s, feature string) domain.CoverageStatus {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "mapped":
		return domain.CoverageMapped
	case "out-of-scope", "out_of_scope", "oos", "excluded":
		return domain.CoverageOutOfScope
	case "unmapped":
		return domain.CoverageUnmapped
	case "":
		// no status given: infer from whether a feature was named.
		if strings.TrimSpace(feature) != "" {
			return domain.CoverageMapped
		}
		return domain.CoverageUnmapped
	default:
		// an unrecognized status ("partial", "deferred", …) is treated as
		// unmapped even when a feature is named, so a not-fully-covered
		// requirement fails loud rather than silently reading as covered.
		return domain.CoverageUnmapped
	}
}

// proposeFenceRe matches the ```gummi-propose … ``` convention block. The
// capture is greedy so it extends to the LAST closing fence: the JSON body
// may itself contain ``` (e.g. a code fence quoted in an open question),
// and a non-greedy match would truncate at that inner fence.
var proposeFenceRe = regexp.MustCompile("(?s)```gummi-propose\\s*(.*)```")

// proposalFromText extracts a proposal from an assistant reply's
// gummi-propose block (the convention path). It errors when no
// well-formed block is present, so an idle with no proposal is a clear
// failure rather than an empty result.
func proposalFromText(text string) (domain.IngestResult, error) {
	m := proposeFenceRe.FindStringSubmatch(text)
	if m == nil {
		return domain.IngestResult{}, fmt.Errorf("the agent did not return a proposal")
	}
	return decodeProposal([]byte(strings.TrimSpace(m[1])))
}

func trimNonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
