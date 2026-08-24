package engine

import (
	"context"
	"fmt"
	"os"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// discoverPrompt asks the scribe for exactly one machine-readable block.
const discoverPrompt = `Survey this repository and determine the fixed commands that build, test,
and lint it — what a CI pipeline would run. Prefer what the repo already
wires up (Makefile targets, package-manifest scripts, CI workflow files,
lint configs) over guesses. Then reply with ONLY this fenced block (a
YAML list, 1-5 entries) and nothing else:

` + "```gummi-checks" + `
- name: build
  cmd: go build ./...
- name: test
  cmd: go test ./...
` + "```" + `

Every cmd must run non-interactively from the repo root, exit non-zero
on failure, and stay offline — no dependency installs, no watch modes.`

// containerLocalRule is appended to the discovery prompt when a workspace
// environment card is present. It reminds the scribe that the checks block
// is only for fast, container-local commands, even though the environment
// card may describe slow or external machinery.
const containerLocalRule = `Only fast, container-local commands belong in the checks block. Any command this environment describes that is slow, or that needs machinery outside this container (a remote host, hardware, or a network service), must NOT go in the block — the block is only for commands that build, test, and lint the repository locally.`

// discoverPromptWith prepends the environment card to the discovery prompt
// when the card is non-empty, and appends the container-local rule so the
// scribe does not fold slow/external commands into the checks block.
func discoverPromptWith(card string) string {
	if card == "" {
		return discoverPrompt
	}
	return card + "\n\n" + discoverPrompt + "\n\n" + containerLocalRule
}

// DiscoverChecks runs a one-shot scribe pass over the feature's fresh
// worktree to learn the repo's build/test/lint commands and writes them
// into the artifact's Verification section as a gummi-checks block —
// the commands Verify later runs (DESIGN §3, decision 7). Fired at the
// approval gate that creates the worktree; the block then rides the
// plan gate like the rest of the spec, so the commands are human-gated
// before Verify auto-runs them. A profile can map the scribe role to a
// cheap model — discovery is deliberately small.
//
// It is a no-op when the artifact already carries a block (hand-authored
// during spec, or from an earlier approval), so re-entry never clobbers
// edits. Best-effort like Estimate: an unusable reply returns (nil, nil)
// and the Verify agent falls back to discovering the commands itself.
func (e *Engine) DiscoverChecks(ctx context.Context, f domain.Feature) ([]domain.Check, error) {
	rc, backend := e.resolveRole(f.Profile, agent.RoleScribe)
	ag := e.agentFor(backend)
	if ag == nil {
		return nil, nil
	}
	workDir, specPath, err := e.locate(ctx, f)
	if err != nil {
		return nil, err
	}
	if raw, err := os.ReadFile(specPath); err == nil {
		if _, found, _ := spec.ParseChecks(string(raw)); found {
			return nil, nil
		}
	}

	userPath, err := config.UserConfigPath()
	if err != nil {
		if e.envWarn != nil {
			e.envWarn(fmt.Sprintf("user config path could not be resolved: %v", err))
		}
		userPath = ""
	}
	cfg, _, err := config.LoadLayered(userPath, e.cfg.Workspace.ConfigFile())
	if err != nil {
		return nil, err
	}
	if len(cfg.Checks.Default) > 0 {
		return e.recordChecks(specPath, spec.RenderChecks(cfg.Checks.Default))
	}

	sess, err := ag.NewSession(ctx, agent.SessionOpts{
		WorkDir:         workDir,
		ArtifactPath:    specPath,
		Role:            agent.RoleScribe,
		Model:           rc.Model,
		Provider:        rc.Provider,
		Permission:      e.cfg.Permission,
		SystemHints:     []string{"You are surveying the repository read-only; do not modify any files."},
		ExtraReadAllows: []string{specPath},
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()
	if err := sess.Send(ctx, discoverPromptWith(e.environmentCard())); err != nil {
		return nil, err
	}
	var text assistantText
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return e.recordChecks(specPath, text.String())
			}
			switch ev.Kind {
			case agent.EventTextDelta:
				text.delta(ev.Text)
			case agent.EventMessage:
				text.message(ev.Text)
			case agent.EventIdle:
				return e.recordChecks(specPath, text.String())
			case agent.EventError:
				return nil, ev.Err
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// recordChecks parses the scribe's reply and upserts the block into the
// artifact's Verification section, re-checking under the file lock that
// no block landed meanwhile (an agent session writes the same file).
func (e *Engine) recordChecks(specPath, reply string) ([]domain.Check, error) {
	checks, _, _ := spec.ParseChecks(reply)
	if len(checks) == 0 {
		return nil, nil
	}
	unlock := spec.LockFile(specPath)
	defer unlock()
	raw, err := os.ReadFile(specPath)
	if err != nil {
		return nil, err
	}
	if _, found, _ := spec.ParseChecks(string(raw)); found {
		return nil, nil
	}
	out, err := spec.UpsertChecks(string(raw), checks)
	if err != nil {
		return nil, err
	}
	if err := atomicfile.Write(specPath, []byte(out), 0o600); err != nil {
		return nil, err
	}
	return checks, nil
}
