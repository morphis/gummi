package agent

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ModelCataloger is implemented by agents that can enumerate the models
// their backend offers, on request. The session model picker (DESIGN
// §19.8) offers a backend's own catalog where it can say one — asked
// live, so the list is the agent's, never gummi's guess — and typed entry
// everywhere; a backend that cannot enumerate simply does not implement
// this, and its picker keeps the ids the workspace's profiles already run
// there as suggestions. There is deliberately no baked-in registry behind
// either source: model ids are opaque strings the adapters forward
// verbatim, and a baked-in list would go stale the week a provider ships
// something.
//
// The returned ids are forwarded verbatim by every other path, so they
// are whatever the backend itself spells — copilot's SDK ids, opencode's
// provider/model pairs.
type ModelCataloger interface {
	ModelCatalog(ctx context.Context) ([]string, error)
}

// opencodeModelsTimeout bounds one `opencode models` probe: the
// subprocess answers from opencode's local catalog, and a hung probe must
// not hang a picker open behind it.
const opencodeModelsTimeout = 15 * time.Second

// OpencodeModelCatalog returns the model ids opencode itself offers —
// the full provider/model pairs its own catalog ships, one per line of
// `opencode models` — not a list gummi keeps. bin "" is "opencode".
//
// It needs only the binary on PATH, no adapter and no authentication, so
// a picker can offer opencode's catalog on a board that runs none of it;
// the engine asks it directly when no opencode adapter is started
// (sessionmodel.go). Each call spawns the CLI; callers that repeat the
// ask are expected to cache (engine.SessionModelCatalog does).
func OpencodeModelCatalog(ctx context.Context, bin string) ([]string, error) {
	if bin == "" {
		bin = "opencode"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("opencode binary %q not found: %w", bin, err)
	}
	ctx, cancel := context.WithTimeout(ctx, opencodeModelsTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, resolved, "models").Output()
	if err != nil {
		return nil, fmt.Errorf("opencode models: %w", err)
	}
	var ids []string
	for _, line := range strings.Split(string(out), "\n") {
		if id := strings.TrimSpace(line); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
