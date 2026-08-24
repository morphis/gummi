package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// The decompose gate (FD-081) hangs off the RS card's verify→done edge: a
// re-runnable side effect that reads the approved doc's `## Slices` rows
// still lacking a minted FD id, runs one transient architect pass to
// expand them into full feature proposals, and — once a caller approves —
// mints the FDs and back-annotates their ids into the source rows. It
// rides the same transient-session shape as Ingest, registers no board
// Session, and never un-approves the doc: every exit, success or failure,
// leaves the RS card at done.

// ErrDecomposeExhausted is returned when the RS card's remaining credit
// envelope admits no further decompose session.
var ErrDecomposeExhausted = errors.New("decompose: RS card's credit envelope is exhausted")

// ErrDecomposeProposalCountMismatch is returned by MintProposals when the
// proposal count no longer matches the doc's unsettled row count — a doc
// edit landed between the decompose pass and the mint.
var ErrDecomposeProposalCountMismatch = errors.New("decompose: proposal count does not match the doc's unsettled slice rows")

const decomposeSourceHint = "You are decomposing an approved research card's `## Slices` rows into full " +
	"feature proposals (gummi decomposition). The source document is at %s (relative to your working " +
	"directory); read it first for full context (Findings, Direction, Constraints)."

const decomposeNoteHint = "Re-decomposing after operator feedback: %s. Address it in the new proposal."

// decomposeRowPrompt is the go-ahead: it enumerates the unsettled rows by
// index and demands exactly one proposal per row, in the same order, so
// MintProposals can bind proposals[i] to the i-th unsettled row.
func decomposeRowPrompt(rows []sliceRow) string {
	var b strings.Builder
	b.WriteString("Expand each of the following unsettled `## Slices` rows into a full feature " +
		"proposal — a PR-sized vertical slice with a title, one-line summary, problem, constraints, " +
		"acceptance criteria, and any open questions. Preserve each row's dependencies (depends-on) " +
		"and cover its listed requirements. Submit exactly one proposal per row below, in the same " +
		"order, via a single propose_features call:\n\n")
	for i, r := range rows {
		fmt.Fprintf(&b, "row %d: %s · %s · depends-on: %v · requirements: %v\n",
			i+1, r.Title, r.OneLiner, r.DependsOn, r.Requirements)
	}
	return b.String()
}

// DecomposeForCard runs one decompose pass for an RS card: it reads the
// card's approved doc, filters `## Slices` for rows without a minted FD
// id, and — when there is at least one — spawns a transient architect
// session that expands each into a full feature proposal. A doc with no
// unsettled rows is a legitimate terminal (a zero-slice RS, or an
// all-settled one) and returns a zero IngestResult without spawning a
// session, so re-running against a settled doc is a cheap no-op.
func (e *Engine) DecomposeForCard(ctx context.Context, cardID domain.FeatureID, note string) (domain.IngestResult, error) {
	rsCard, err := e.cfg.Store.GetFeature(ctx, cardID)
	if err != nil {
		return domain.IngestResult{}, err
	}
	path := e.artifactFile(&rsCard)
	if path == "" {
		return domain.IngestResult{}, fmt.Errorf("%s: no artifact found", cardID)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return domain.IngestResult{}, fmt.Errorf("reading %s: %w", path, err)
	}
	artifact := string(raw)
	rows, err := unsettledSliceRows(artifact)
	if err != nil {
		return domain.IngestResult{}, fmt.Errorf("%s: %w", cardID, err)
	}
	if len(rows) == 0 {
		return domain.IngestResult{}, nil
	}

	rc, backend := e.resolveRole(rsCard.Profile, agent.RoleArchitect)
	ag := e.agentFor(backend)
	if ag == nil {
		return domain.IngestResult{}, fmt.Errorf("no agent configured; cannot decompose")
	}

	// The decompose pass draws down the RS card's own protected reserve
	// (domain.DecomposeReserveCredits, floored off from non-decompose
	// stages by stageBudget's KindResearch branch), not the general
	// stage-budget ceiling: remaining shrinks by every credit (hosted or
	// BYOK-equivalent) a prior decompose session actually spent, so
	// re-runs admit a smaller cap each time and eventually admit none.
	var maxCredits float64
	if rsCard.Budget.Envelope > 0 {
		rate := ag.CreditRate(rc.Model)
		remaining := float64(rsCard.Budget.Envelope) - rsCard.Spend.CreditEquivalentAt(rate)
		if remaining <= 0 {
			return domain.IngestResult{}, ErrDecomposeExhausted
		}
		maxCredits = remaining
	}

	absPath := path
	if !filepath.IsAbs(absPath) {
		if wd, werr := os.Getwd(); werr == nil {
			absPath = filepath.Join(wd, absPath)
		}
	}
	caps := ag.Capabilities()
	hints := []string{fmt.Sprintf(decomposeSourceHint, absPath)}
	var tools []agent.ToolDef
	if caps.ClientTools {
		tools = []agent.ToolDef{proposeFeaturesTool()}
		hints = append(hints, ingestToolHint)
	} else {
		hints = append(hints, ingestConventionHint)
	}
	if note != "" {
		hints = append(hints, fmt.Sprintf(decomposeNoteHint, note))
	}
	wt, err := e.mgr(ctx, &rsCard)
	if err != nil {
		return domain.IngestResult{}, fmt.Errorf("resolving decompose repository: %w", err)
	}
	sess, err := ag.NewSession(ctx, agent.SessionOpts{
		WorkDir:         wt.RepoRoot(),
		Role:            agent.RoleArchitect,
		Model:           rc.Model,
		Provider:        rc.Provider,
		Think:           rc.Think,
		Permission:      e.cfg.Permission,
		SystemHints:     hints,
		Tools:           tools,
		ExtraReadAllows: []string{absPath},
		MaxCredits:      maxCredits,
	})
	if err != nil {
		return domain.IngestResult{}, fmt.Errorf("starting decompose session: %w", err)
	}
	defer func() { _ = sess.Close() }()

	if err := sess.Send(ctx, decomposeRowPrompt(rows)); err != nil {
		return domain.IngestResult{}, err
	}
	res, err := e.collectDecomposeProposal(ctx, cardID, sess)
	if err != nil {
		return domain.IngestResult{}, err
	}
	res.SourcePath = string(rsCard.ID) + " " + rsCard.Slug
	return res, nil
}

// collectDecomposeProposal drains a decompose session like Ingest's
// collectProposal, additionally metering every usage sample onto the RS
// card's spend — the "bills to the RS card's own envelope" contract — via
// the existing AddSpend side-channel.
func (e *Engine) collectDecomposeProposal(ctx context.Context, cardID domain.FeatureID, sess agent.Session) (domain.IngestResult, error) {
	var text assistantText
	thinking := false
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return proposalFromText(text.String())
			}
			switch ev.Kind {
			case agent.EventUsage:
				if e.cfg.Store != nil {
					_ = e.cfg.Store.AddDecomposeSpend(ctx, cardID, ev.Usage.Credits, ev.Usage.InputTokens, ev.Usage.OutputTokens)
				}
			case agent.EventToolCall:
				// visible progress only; decompose has no live listener.
			case agent.EventReasoningDelta:
				if !thinking {
					thinking = true
				}
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
					resolve(ctx, sess, ev.ToolCall.ID, err.Error()+" — call propose_features again with valid JSON")
					continue
				}
				resolve(ctx, sess, ev.ToolCall.ID, fmt.Sprintf("received %d features", len(res.Proposals)))
				return res, nil
			case agent.EventTextDelta:
				thinking = false
				text.delta(ev.Text)
			case agent.EventMessage:
				thinking = false
				text.message(ev.Text)
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

// MintProposals materializes an approved decompose result into real FDs:
// it re-derives the doc's unsettled rows (the doc, not the caller, is the
// source of truth for row order — the only thing that makes it safe for
// every caller, driver auto-trigger, --approve, and a future TUI re-run
// alike, to share this one entry point without an ephemeral row buffer
// crossing a save/load cycle), asserts the proposal count still matches,
// mints every proposal in one Materialize batch (so cross-row DependsOn
// titles resolve regardless of forward/backward doc order), positionally
// back-annotates the minted ids into the source rows, and wires
// store-level dependency edges over the minted prefix.
//
// Best-effort on failure, mirroring Materialize: features minted before an
// error are returned alongside it, each already back-annotated to its
// source row, so the operator can see exactly which rows settled and
// re-run only the failing tail.
func (e *Engine) MintProposals(ctx context.Context, cardID domain.FeatureID, res domain.IngestResult) ([]domain.Feature, error) {
	rsCard, err := e.cfg.Store.GetFeature(ctx, cardID)
	if err != nil {
		return nil, err
	}
	path := e.artifactFile(&rsCard)
	if path == "" {
		return nil, fmt.Errorf("%s: no artifact found", cardID)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	artifact := string(raw)
	unsettled, err := unsettledSliceRows(artifact)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cardID, err)
	}
	if len(res.Proposals) != len(unsettled) {
		return nil, fmt.Errorf("%s: %w (%d proposals, %d unsettled rows)",
			cardID, ErrDecomposeProposalCountMismatch, len(res.Proposals), len(unsettled))
	}

	env := rsCard.Budget.Envelope
	if env <= 0 {
		env = int(domain.MinEnvelope)
	}
	created, mintErr := e.Materialize(ctx, res, MaterializeOpts{Profile: rsCard.Profile, Envelope: env, Repo: rsCard.Repo})
	if len(created) == 0 {
		return created, mintErr
	}

	ids := make([]domain.FeatureID, len(created))
	for i, f := range created {
		ids[i] = f.ID
	}
	newArtifact, annErr := spec.SetSliceIDsPositional(artifact, ids)
	if annErr == nil {
		annErr = atomicfile.Write(path, []byte(newArtifact), 0o600)
	}
	if annErr != nil {
		// The FDs are already minted; a caller that swallowed this error
		// would report success while the source doc still shows every row
		// as unsettled — the next pass would re-decompose and duplicate-mint
		// them. Surface it instead so the caller escalates and the operator
		// can re-annotate the doc by hand.
		if mintErr == nil {
			mintErr = fmt.Errorf("back-annotating %s: %w", cardID, annErr)
		} else {
			mintErr = fmt.Errorf("%w (also failed to back-annotate: %v)", mintErr, annErr)
		}
	}

	byTitle := make(map[string]domain.Feature, len(created))
	for _, f := range created {
		byTitle[f.Title] = f
	}
	for i, f := range created {
		for _, dep := range res.Proposals[i].DependsOn {
			if target, ok := byTitle[dep]; ok {
				_ = e.cfg.Store.AddDependency(ctx, f.ID, target.ID)
			}
		}
	}
	return created, mintErr
}
