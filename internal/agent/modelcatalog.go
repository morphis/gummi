package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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

// opencodeModelsTimeout bounds one catalog probe: it now asks a server —
// the transient serve a probe spawns for the ask — and a hung probe must
// not hang a picker open behind it. The server answers from opencode's
// local catalog once it is up; this covers the boot, not the answer.
const opencodeModelsTimeout = 30 * time.Second

// OpencodeModelCatalog returns the model ids opencode itself offers — the
// full provider/model pairs its own catalog ships — not a list gummi
// keeps. bin "" is "opencode".
//
// It needs only the binary on PATH, no adapter and no session: the probe
// spawns a transient `opencode serve` (default environment, no session
// config, a throwaway directory), asks its providers endpoint, and kills
// the server as soon as it answers — so a picker can offer opencode's
// catalog on a board that runs none of it. Callers that repeat the ask
// are expected to cache (engine.SessionModelCatalog does), which keeps
// this at one transient serve per cache miss.
//
// It is a variable, the seam opencodeExecPath is: the engine's
// no-adapter probe path rebinds it in tests instead of spawning anything.
var OpencodeModelCatalog = opencodeModelCatalog

func opencodeModelCatalog(ctx context.Context, bin string) ([]string, error) {
	if bin == "" {
		bin = "opencode"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("opencode binary %q not found: %w", bin, err)
	}
	ctx, cancel := context.WithTimeout(ctx, opencodeModelsTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "gummi-opencode-probe-*")
	if err != nil {
		return nil, fmt.Errorf("opencode catalog: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	port, err := freeLoopbackPort()
	if err != nil {
		return nil, fmt.Errorf("opencode catalog: %w", err)
	}
	password, err := randomToken()
	if err != nil {
		return nil, fmt.Errorf("opencode catalog: %w", err)
	}
	// The probe's server runs on the inherited environment minus whatever
	// session config the process itself was started under — a probe is the
	// operator's own catalog, not one session's cage.
	env := envWithout("OPENCODE_CONFIG")
	env = append(env, "OPENCODE_SERVER_PASSWORD="+password)
	proc, err := serveOpencode(ctx, resolved, port, dir, env)
	if err != nil {
		return nil, fmt.Errorf("opencode catalog: starting opencode serve: %w", err)
	}
	defer func() {
		proc.cancel()
		proc.wait()
	}()
	srv := opencodeServer{base: proc.url, password: password}
	if err := srv.waitReady(ctx, opencodeModelsTimeout, proc.exited); err != nil {
		return nil, fmt.Errorf("opencode catalog: %w", err)
	}
	ids, err := srv.providers(ctx)
	if err != nil {
		return nil, fmt.Errorf("opencode catalog: %w", err)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("opencode catalog: the server answered with no models")
	}
	return ids, nil
}
