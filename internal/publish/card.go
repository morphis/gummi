package publish

import (
	"context"

	"github.com/morphis/gummi/internal/domain"
)

// Tree is what InputFor reads off the card's repository: a worktree.Manager.
type Tree interface {
	Dirty(ctx context.Context, f *domain.Feature) (bool, error)
	RebaseInProgress(ctx context.Context, f *domain.Feature) (bool, error)
	Landed(ctx context.Context, f *domain.Feature) (bool, error)
	BaseBranch(ctx context.Context) string
}

// InputFor builds an act's Input the same way for every face: the card's
// tree state, its base, and its unresolved diff comments. busy is whether
// an agent session holds the card, which only the face knows.
// openSpec counts the open spec threads holding its gate, which the face
// reads the way its landing does.
func InputFor(ctx context.Context, t Tree, f *domain.Feature, busy bool, anns []domain.DiffAnnotation, openSpec int) Input {
	c := Card{F: f, Busy: busy}
	// a tree that cannot be read is not known clean: refuse rather than
	// publish past a state nobody saw
	var err error
	if c.Dirty, err = t.Dirty(ctx, f); err != nil {
		c.Dirty = true
	}
	if c.Rebasing, err = t.RebaseInProgress(ctx, f); err != nil {
		c.Rebasing = true
	}
	if f.LandedSHA != "" {
		c.Landed = true
	} else if f.Stage == domain.StageDone && !f.HandedOff() {
		c.Landed, _ = t.Landed(ctx, f)
	}
	base := f.Base
	if base == "" {
		base = t.BaseBranch(ctx)
	}
	open := 0
	for _, a := range anns {
		if !a.Resolved {
			open++
		}
	}
	return Input{Card: c, Base: base, OpenComments: open, OpenSpec: openSpec}
}
