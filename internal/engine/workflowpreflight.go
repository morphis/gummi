package engine

// A recipe's preflight (DESIGN §25.5): one shell command the recipe names,
// run in the session's tree when the run is proposed, before anyone is
// asked to allow it. It is the recipe's own question of whether the tree
// can carry the run at all — a tool its checks need, a service its tests
// reach — answered before a step spends anything. A non-zero exit refuses
// the proposal with the command's output as the reason; a zero exit's
// output is kept in the run's directory, shown on the proposal and filled
// as {{preflight.output}}, so a step knows the baseline it starts from.
// The recipe chooses it: gummi adds no preflight of its own.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/verify"
	"github.com/morphis/gummi/internal/workflowrecipe"
)

// preflightTimeout bounds a recipe's preflight: a proposal waits on it.
const preflightTimeout = 5 * time.Minute

// errNoPreflight is runPreflight's answer for a recipe with none.
var errNoPreflight = errors.New("the recipe has no preflight")

// runPreflight runs recipe's preflight in session's tree and returns what
// it printed. It errs, saying why in the words a refusal gives, when the
// command exits non-zero, times out or cannot be run.
func (e *Engine) runPreflight(ctx context.Context, session domain.FeatureID, recipe domain.Recipe) (string, error) {
	cmd := strings.TrimSpace(recipe.Preflight)
	if cmd == "" {
		return "", errNoPreflight
	}
	dir, err := e.sessionTree(ctx, session)
	if err != nil {
		return "", fmt.Errorf("the recipe's preflight could not be run: %w", err)
	}
	res := verify.RunBounded(ctx, dir, []domain.Check{{Name: "preflight", Cmd: cmd}}, preflightTimeout)[0]
	out := strings.TrimSpace(res.Output)
	switch res.Status {
	case verify.StatusPass:
		return out, nil
	case verify.StatusTimeout:
		return "", fmt.Errorf("the recipe's preflight `%s` did not finish within %s, so the run is not proposed", cmd, preflightTimeout)
	case verify.StatusNotRun:
		return "", fmt.Errorf("the recipe's preflight `%s` never ran, so the run is not proposed", cmd)
	}
	why := fmt.Sprintf("the recipe's preflight `%s` exited %d, so the run is not proposed", cmd, res.ExitCode)
	if out != "" {
		why += ":\n" + tail(out, 2000)
	}
	return "", errors.New(why)
}

// preflightFor runs recipe's preflight, when it has one, for a run about
// to be created; ok is false when it has none.
func (e *Engine) preflightFor(ctx context.Context, session domain.FeatureID, recipe domain.Recipe) (out string, ok bool, err error) {
	out, err = e.runPreflight(ctx, session, recipe)
	if errors.Is(err, errNoPreflight) {
		return "", false, nil
	}
	return out, err == nil, err
}

// keepPreflight writes what run n's preflight printed into its directory.
func (e *Engine) keepPreflight(session domain.FeatureID, n int, out string) {
	_, _ = e.writeRunFile(session, n, workflowrecipe.PreflightFile, out)
}

// preflightOutput is what run's preflight printed when it was proposed,
// and whether its recipe has a preflight.
func (e *Engine) preflightOutput(run *domain.Run) (string, bool) {
	if strings.TrimSpace(run.Recipe.Preflight) == "" {
		return "", false
	}
	raw, err := os.ReadFile(filepath.Join(e.RunDir(run.Session, run.N), workflowrecipe.PreflightFile)) // #nosec G304 -- the run's own directory
	if err != nil {
		return "", true
	}
	return string(raw), true
}
