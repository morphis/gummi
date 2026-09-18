package engine

// Substrates, seen from the engine. internal/substrate knows what state a
// substrate is in and who may use it; this file is where the engine asks —
// for a verify kickoff that cites one as an [env:] prerequisite, and for a
// goal deciding whether a card that stopped for want of one is worth
// another verify.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/envprobe"
	"github.com/morphis/gummi/internal/substrate"
)

// layeredConfig reads the user and workspace config the way every probe
// path does: a user path that cannot be resolved is a warning, not a stop.
func (e *Engine) layeredConfig() (config.Config, error) {
	userPath, err := config.UserConfigPath()
	if err != nil {
		if e.envWarn != nil {
			e.envWarn(fmt.Sprintf("user config path could not be resolved: %v", err))
		}
		userPath = ""
	}
	cfg, _, err := config.LoadLayered(userPath, e.cfg.Workspace.ConfigFile())
	return cfg, err
}

// Substrates returns the workspace's substrate manager, read from the
// config as it is now: an operator who adds a substrate while a goal runs
// should not have to restart anything.
func (e *Engine) Substrates() (*substrate.Manager, error) {
	cfg, err := e.layeredConfig()
	if err != nil {
		return nil, err
	}
	m := substrate.New(e.cfg.Workspace.StateDir(), e.cfg.Workspace.Root, cfg.Substrates)
	m.Now = e.now
	return m, nil
}

// probeEnvironment answers every name a verification plan may cite with
// [env: <name>]: the env prerequisites, probed in workDir, and the
// substrates, asked for their state. A substrate someone holds is reported
// as errored rather than absent — only a clean absence licenses skipping a
// live step, and "in use" is not "not there".
func probeEnvironment(ctx context.Context, cfg config.Config, m *substrate.Manager, workDir string) []envprobe.Result {
	results := envprobe.Run(ctx, workDir, cfg.Env)
	for _, name := range m.Names() {
		st, err := m.Status(ctx, name)
		r := envprobe.Result{Name: name, Describe: st.Describe, Output: st.Detail}
		switch {
		case err != nil:
			r.Err = err
		case st.State == substrate.Ready:
			r.Present = true
		case st.State == substrate.Absent:
		default:
			r.Err = errors.New(st.State.String())
		}
		results = append(results, r)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	return results
}

// substrateProbeEvery is how stale a goal lets its reading of a substrate
// get. A probe reaches out to real machines, a goal ticks every few
// seconds, and nothing a goal decides from the answer is worth asking more
// often than this.
const substrateProbeEvery = 2 * time.Minute

type substrateCache struct {
	mu sync.Mutex
	at map[string]substrate.Status
}

// substrateStatus is Status with a memory: the same answer for
// substrateProbeEvery, except that "held" is never remembered — it costs
// nothing to ask and changes the moment a job ends.
func (e *Engine) substrateStatus(ctx context.Context, m *substrate.Manager, name string) (substrate.Status, error) {
	e.substrates.mu.Lock()
	defer e.substrates.mu.Unlock()
	if st, ok := e.substrates.at[name]; ok && st.State != substrate.Held && e.now().Sub(st.CheckedAt) < substrateProbeEvery {
		return st, nil
	}
	st, err := m.Status(ctx, name)
	if err != nil {
		return st, err
	}
	if e.substrates.at == nil {
		e.substrates.at = map[string]substrate.Status{}
	}
	e.substrates.at[name] = st
	return st, nil
}

// forgetSubstrate drops the remembered reading of name: something just
// happened to it that the next reader should see.
func (e *Engine) forgetSubstrate(name string) {
	e.substrates.mu.Lock()
	delete(e.substrates.at, name)
	e.substrates.mu.Unlock()
}
