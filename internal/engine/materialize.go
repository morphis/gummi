package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/spec"
	"github.com/morphia/gummi/internal/workflow"
)

// MaterializeOpts configures how approved proposals become features: the
// profile all of them adopt and the credit envelope each carries (mirrors
// the fields the new-feature form sets).
type MaterializeOpts struct {
	Profile  string
	Envelope int
}

// Materialize turns an approved IngestResult into real features (DESIGN
// §11.4, phase C): each proposal is minted into the todo backlog and its
// draft is seeded with the extracted content, provenance, and
// dependencies. IDs are minted in a first pass so dependency references
// can resolve to FD-IDs; features are then created and their drafts
// written. Returns the created features in proposal order.
//
// Best-effort on failure: features created before an error are returned
// alongside it, so a mid-batch failure leaves a diagnosable partial state
// rather than silently losing work.
func (e *Engine) Materialize(ctx context.Context, res domain.IngestResult, opts MaterializeOpts) ([]domain.Feature, error) {
	// Pre-flight: every title must slugify before we mint anything, so a
	// bad title fails the batch cleanly instead of after consuming feature
	// numbers for the proposals ahead of it.
	slugs := make([]string, len(res.Proposals))
	for i, p := range res.Proposals {
		s, err := p.Slug()
		if err != nil {
			return nil, fmt.Errorf("proposal %q: %w", p.Title, err)
		}
		slugs[i] = s
	}

	// Pass 1: mint an ID/slug for every proposal, and index by title so
	// depends_on can render as "FD-002 slug" instead of a bare title. A
	// duplicate title keeps its first occurrence (deterministic), so a
	// dependency on that title resolves to a stable feature.
	feats := make([]domain.Feature, len(res.Proposals))
	byTitle := make(map[string]domain.Feature, len(res.Proposals))
	now := e.now()
	for i, p := range res.Proposals {
		num, err := e.cfg.Store.MintFeatureNum(ctx, e.cfg.Workspace.SeqFile())
		if err != nil {
			return nil, err
		}
		id, err := domain.NewFeatureID(num)
		if err != nil {
			return nil, err
		}
		f := domain.Feature{
			ID: id, Num: num, Title: p.Title, OneLiner: p.OneLiner, Slug: slugs[i],
			Stage: workflow.Initial(), Skip: p.Skip, Profile: opts.Profile,
			Budget: domain.Budget{Envelope: opts.Envelope}, CreatedAt: now, UpdatedAt: now,
		}
		feats[i] = f
		if _, seen := byTitle[p.Title]; !seen {
			byTitle[p.Title] = f
		}
	}

	// Pass 2: write each seeded draft, then persist the feature. Draft
	// first so a write failure aborts before the feature exists — a
	// persisted feature with no draft would be silently reseeded with the
	// blank template on first open, losing the ingested content.
	var created []domain.Feature
	for i, p := range res.Proposals {
		f := feats[i]
		prov := domain.DraftProvenance{
			Source:    res.SourcePath,
			Refs:      p.SourceRefs,
			DependsOn: resolveDeps(p.DependsOn, byTitle),
		}
		if err := e.writeSeededDraft(f, p.Draft, prov); err != nil {
			return created, err
		}
		if err := e.cfg.Store.CreateFeature(ctx, &f); err != nil {
			return created, err
		}
		created = append(created, f)
	}
	return created, nil
}

// writeSeededDraft materializes a feature's pre-populated draft under
// .gummi/state/drafts/ (where drafts live until spec approval).
func (e *Engine) writeSeededDraft(f domain.Feature, seed domain.DraftSeed, prov domain.DraftProvenance) error {
	dir := e.cfg.Workspace.DraftsDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	path := filepath.Join(dir, spec.DraftFilename(&f))
	return os.WriteFile(path, []byte(spec.SeededTemplate(&f, seed, prov)), 0o600)
}

// resolveDeps maps depends_on titles to "FD-NNN slug" labels where the
// title matches a proposal in this batch, leaving unmatched titles as-is
// (a dependency on something outside the ingest still reads sensibly).
func resolveDeps(titles []string, byTitle map[string]domain.Feature) []string {
	if len(titles) == 0 {
		return nil
	}
	out := make([]string, 0, len(titles))
	for _, t := range titles {
		if f, ok := byTitle[t]; ok {
			out = append(out, string(f.ID)+" "+f.Slug)
		} else {
			out = append(out, t)
		}
	}
	return out
}
