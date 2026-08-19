package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/workflow"
)

// Bug ingestion (DESIGN §11, bug variant) mirrors spec ingestion's
// pipeline — source → proposals → human gate → materialize — but the
// source yields discrete bugs rather than one document to decompose, so
// there is no architect pass and no coverage map. IngestBugs fetches and
// dedupes; the caller gates; MaterializeBugs mints and seeds.

// SkippedBug pairs a dedup-skipped proposal with the local bug it already
// photographs (the feature whose ExternalRef matched), so a re-ingest can
// say not just "already on the board" but which BG- card to look at.
type SkippedBug struct {
	Proposal domain.BugProposal
	LocalID  domain.FeatureID
}

// BugIngestResult is one ingest pass: the fresh proposals to review, and
// the ones skipped because their external ref is already on the board.
// Skipped is surfaced (not silently dropped) so a re-ingest reports what
// it already had rather than looking like it found nothing.
type BugIngestResult struct {
	Source    string
	Proposals []domain.BugProposal
	Skipped   []SkippedBug
}

// IngestBugs pulls candidate bugs from a source and partitions them into
// fresh proposals and already-imported ones (matched by external ref).
// It creates nothing — materialization is a separate, gated step.
func (e *Engine) IngestBugs(ctx context.Context, src BugSource) (BugIngestResult, error) {
	fetched, err := src.Fetch(ctx)
	if err != nil {
		return BugIngestResult{}, fmt.Errorf("%s source: %w", src.Name(), err)
	}
	res := BugIngestResult{Source: src.Name()}
	seen := map[string]bool{} // dedup within this batch, too
	for _, p := range fetched {
		if p.ExternalRef != "" {
			if seen[p.ExternalRef] {
				continue
			}
			seen[p.ExternalRef] = true
			if f, err := e.cfg.Store.FeatureByExternalRef(ctx, p.ExternalRef); err == nil {
				res.Skipped = append(res.Skipped, SkippedBug{Proposal: p, LocalID: f.ID})
				continue
			} else if !errors.Is(err, state.ErrNotFound) {
				return BugIngestResult{}, err
			}
		}
		res.Proposals = append(res.Proposals, p)
	}
	return res, nil
}

// MaterializeBugs turns approved bug proposals into real bugs: each is
// minted into the todo backlog with a seeded bug report (symptoms,
// provenance, severity). It mirrors Materialize but needs no dependency
// pre-pass — bugs carry no depends_on. Best-effort on failure: bugs
// created before an error are returned alongside it.
func (e *Engine) MaterializeBugs(ctx context.Context, props []domain.BugProposal, opts MaterializeOpts) ([]domain.Feature, error) {
	// Pre-flight: the target repository must be configured, so an unknown
	// name fails the whole batch before any number is consumed.
	if err := e.requireRepo(opts.Repo); err != nil {
		return nil, err
	}
	// Pre-flight: every title must slugify before we mint anything, so a
	// bad title fails the batch cleanly instead of after consuming numbers.
	slugs := make([]string, len(props))
	for i, p := range props {
		s, err := p.Slug()
		if err != nil {
			return nil, fmt.Errorf("bug %q: %w", p.Title, err)
		}
		slugs[i] = s
	}

	var created []domain.Feature
	now := e.now()
	for i, p := range props {
		num, err := e.cfg.Store.MintFeatureNum(ctx, e.cfg.Workspace.SeqFile())
		if err != nil {
			return created, err
		}
		id, err := domain.NewID(domain.KindBug, num)
		if err != nil {
			return created, err
		}
		f := domain.Feature{
			ID: id, Num: num, Kind: domain.KindBug, Title: p.Title, OneLiner: p.OneLiner,
			Slug: slugs[i], Stage: workflow.Initial(domain.KindBug), Skip: p.Skip,
			Profile: opts.Profile, Budget: domain.Budget{Envelope: opts.Envelope},
			ExternalRef: p.ExternalRef, Severity: p.Severity, Repo: opts.Repo, CreatedAt: now, UpdatedAt: now,
		}
		// Draft first so a write failure aborts before the bug exists — a
		// persisted bug with no draft would be reseeded blank on first open.
		if err := e.writeSeededBugDraft(f, p); err != nil {
			return created, err
		}
		if err := e.cfg.Store.CreateFeature(ctx, &f); err != nil {
			return created, err
		}
		created = append(created, f)
	}
	return created, nil
}

// writeSeededBugDraft materializes a bug's pre-populated report under
// .gummi/state/drafts/ (where drafts live until the diagnosis is approved
// and a worktree is created).
func (e *Engine) writeSeededBugDraft(f domain.Feature, p domain.BugProposal) error {
	dir := e.cfg.Workspace.DraftsDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	path := filepath.Join(dir, spec.DraftFilename(&f))
	content := spec.SeededBugTemplate(&f, p.Report, p.Provenance(), p.Severity)
	return atomicfile.Write(path, []byte(content), 0o600)
}
