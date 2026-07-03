package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"github.com/morphia/gummi/internal/domain"
	"github.com/morphia/gummi/internal/spec"
	"github.com/morphia/gummi/internal/state"
	"github.com/morphia/gummi/internal/workflow"
)

// featureRow is one board entry: the stored feature plus the bits of
// derived filesystem state the board displays.
type featureRow struct {
	F           domain.Feature
	HasWorktree bool
	History     []state.TransitionRecord
}

// rowsMsg delivers a fresh load of the board content.
type rowsMsg struct {
	rows []featureRow
	err  error
}

// noticeMsg surfaces a transient outcome (success or failure) in the
// status bar.
type noticeMsg struct {
	text  string
	isErr bool
}

// loadRows reads all features, their histories, and worktree presence.
func (m *Shell) loadRows() tea.Msg {
	ctx := context.Background()
	feats, err := m.store.ListFeatures(ctx)
	if err != nil {
		return rowsMsg{err: err}
	}
	rows := make([]featureRow, 0, len(feats))
	for _, f := range feats {
		row := featureRow{F: f}
		if hist, err := m.store.History(ctx, f.ID); err == nil {
			row.History = hist
		}
		if ok, err := m.wt.Exists(ctx, &f); err == nil {
			row.HasWorktree = ok
		}
		rows = append(rows, row)
	}
	return rowsMsg{rows: rows}
}

// formResult carries the new-feature form's fields.
type formResult struct {
	Title    string
	OneLiner string
	Profile  string
	Skip     domain.SkipFlags
}

// createFeature mints a number and persists a new feature in todo.
func (m *Shell) createFeature(res formResult) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		slug, err := domain.Slugify(res.Title)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		num, err := m.store.MintFeatureNum(ctx, m.ws.SeqFile())
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		id, err := domain.NewFeatureID(num)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		now := m.now()
		f := domain.Feature{
			ID: id, Num: num, Title: res.Title, OneLiner: res.OneLiner,
			Slug: slug, Stage: workflow.Initial(), Skip: res.Skip,
			Profile: res.Profile, CreatedAt: now, UpdatedAt: now,
		}
		if err := m.store.CreateFeature(ctx, &f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s created", id)}
	}
}

// advanceStage moves the feature along its primary forward edge. When
// the feature leaves Spec (spec approval, DESIGN §10.11) its worktree
// and branch are created first.
func (m *Shell) advanceStage(id domain.FeatureID) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		nexts := workflow.Next(f.Stage, f.Skip)
		if len(nexts) == 0 {
			return noticeMsg{text: fmt.Sprintf("%s is done — nothing to advance", id)}
		}
		// prefer the skip edge when the flag opts the feature out of
		// the intermediate stage, otherwise take the primary edge
		next := nexts[len(nexts)-1]
		if next == domain.StageImplement && f.Stage != domain.StageSpec {
			// rerun edges (review/verify → implement) are bounces, not
			// forward moves; g always goes forward
			next = nexts[0]
		}
		if f.Stage == domain.StageSpec {
			if ok, err := m.wt.Exists(ctx, &f); err != nil {
				return noticeMsg{text: err.Error(), isErr: true}
			} else if !ok {
				if _, err := m.wt.Create(ctx, &f); err != nil {
					return noticeMsg{text: err.Error(), isErr: true}
				}
			}
			// spec approval commits the spec to the feature branch and
			// retires the draft (DESIGN §10.11)
			if err := m.migrateDraft(ctx, &f); err != nil {
				return noticeMsg{text: err.Error(), isErr: true}
			}
		}
		if _, err := m.store.Transition(ctx, id, next, "user"); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s → %s", id, next)}
	}
}

// migrateDraft moves the spec draft into the feature's worktree as a
// committed file. A feature that never had a draft gets one from the
// template — the branch always carries a spec. Idempotent, keyed on
// git tracking (a merely-present uncommitted file is not migrated).
func (m *Shell) migrateDraft(ctx context.Context, f *domain.Feature) error {
	draftPath := filepath.Join(m.ws.DraftsDir(), spec.DraftFilename(f))
	if tracked, err := m.wt.FileCommitted(ctx, f, f.SpecPath()); err != nil {
		return err
	} else if tracked {
		return os.RemoveAll(draftPath)
	}
	// content preference: the draft; else a stray uncommitted worktree
	// copy (e.g. a crashed earlier migration); else a fresh template
	wtSpec := filepath.Join(m.wt.Root(), f.WorktreePath(), f.SpecPath())
	content := spec.Template(f)
	if raw, err := os.ReadFile(draftPath); err == nil {
		content = string(raw)
	} else if !os.IsNotExist(err) {
		return err
	} else if raw, err := os.ReadFile(wtSpec); err == nil {
		content = string(raw)
	}
	if err := m.wt.CommitFile(ctx, f, f.SpecPath(), content,
		fmt.Sprintf("docs(spec): %s %s", f.ID, f.Title)); err != nil {
		return err
	}
	return os.RemoveAll(draftPath)
}

// bounceStage sends a review/verify feature back to implement (the
// rerun edge). Only those two stages bounce: from anywhere else the
// edge into Implement is a forward move and belongs to g.
func (m *Shell) bounceStage(id domain.FeatureID) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if f.Stage != domain.StageReview && f.Stage != domain.StageVerify {
			return noticeMsg{text: fmt.Sprintf("%s is in %s — only review/verify can bounce back", id, f.Stage), isErr: true}
		}
		if _, err := m.store.Transition(ctx, id, domain.StageImplement, "user"); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s bounced back to implement", id)}
	}
}

// deleteFeature removes worktree, branch, and record.
func (m *Shell) deleteFeature(id domain.FeatureID) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		if ok, err := m.wt.Exists(ctx, &f); err == nil && ok {
			if err := m.wt.Remove(ctx, &f, true); err != nil {
				return noticeMsg{text: err.Error(), isErr: true}
			}
		}
		// a feature that never left Spec has no branch — only delete
		// one that exists
		if ok, err := m.wt.BranchExists(ctx, &f); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		} else if ok {
			if err := m.wt.DeleteBranch(ctx, &f, true); err != nil {
				return noticeMsg{text: err.Error(), isErr: true}
			}
		}
		if err := m.store.DeleteFeature(ctx, id); err != nil {
			return noticeMsg{text: err.Error(), isErr: true}
		}
		return noticeMsg{text: fmt.Sprintf("%s deleted", id)}
	}
}
