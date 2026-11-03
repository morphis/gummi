package engine

// What a session's runs leave behind when the session is deleted, or a run
// is deleted outright (a quiet end): the hidden cards its steps ran on and
// the run directories. Their rows cascade with the run; these do not.

import (
	"context"
	"os"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/gummidir"
	"github.com/morphis/gummi/internal/rmtree"
)

// ForgetRuns removes the step cards and the run directories of session,
// before the session's own record goes.
func (e *Engine) ForgetRuns(ctx context.Context, session domain.FeatureID) {
	if e.cfg.Store == nil {
		return
	}
	cards, _ := e.cfg.Store.SessionStepCards(ctx, session)
	for _, id := range cards {
		e.dropStepCard(ctx, id)
	}
	if e.pool != nil {
		_ = rmtree.RemoveAll(gummidir.Path(e.pool.Root(), "runs", string(session)))
	}
}

// dropStepCard removes a step's hidden card: its record, its session, its
// live mirror and its scratch files. Best effort: what is left is clutter.
func (e *Engine) dropStepCard(ctx context.Context, id domain.FeatureID) {
	if ff := e.Freeform(id); ff != nil {
		_ = ff.Close()
	}
	e.Drop(id)
	if err := e.cfg.Store.DeleteFeature(ctx, id); err != nil {
		return
	}
	_ = os.Remove(e.cfg.Workspace.LiveFile(id))
	_ = rmtree.RemoveAll(e.cfg.Workspace.ScratchFilesDir(id))
}
