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
lint configs) over guesses.

Each command covers the WHOLE repository, not the part a change happens to
touch: a test command scoped to one package cannot fail on what the change
broke in another, which is the only thing this block exists to catch. If a
whole-repo command cannot run here — it needs a service, a device, or a
toolchain this container lacks — widen it as far as it does run (naming
the parts that build, rather than the one package under edit) instead of
narrowing it to a directory.

Then reply with ONLY this fenced block (a
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
	// Discovery is skipped only over its OWN block: re-running it there
	// would re-add commands a reviewer removed on purpose. A block
	// someone else wrote is merged into instead of honoured as the final
	// word — see spec.ChecksDiscoveryNote for why the two differ.
	if raw, err := os.ReadFile(specPath); err == nil {
		if _, found, _ := spec.ParseChecks(string(raw)); found && spec.ChecksAreDiscovered(string(raw)) {
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
		return e.recordChecks(specPath, spec.RenderDiscoveredChecks(cfg.Checks.Default))
	}

	// What builds this repository is a property of the repository, not of
	// the card. A survey whose build-defining files have not moved since
	// the last one is the same survey, so the workspace remembers it: the
	// second card in a repo starts from the first card's answer instead of
	// paying minutes of scribe time to re-derive it — and, more to the
	// point, gets the SAME answer, rather than a second defensible command
	// set that quietly means a different quality floor.
	repoRoot := e.repoRootFor(ctx, f)
	if cached, ok := e.cachedChecks(repoRoot); ok {
		return e.recordChecks(specPath, spec.RenderDiscoveredChecks(cached))
	}

	sess, err := ag.NewSession(ctx, agent.SessionOpts{
		WorkDir:         workDir,
		ArtifactPath:    specPath,
		Role:            agent.RoleScribe,
		Model:           rc.Model,
		Provider:        rc.Provider,
		Think:           rc.Think,
		Permission:      e.cfg.Permission,
		SystemHints:     []string{"You are surveying the repository read-only; do not modify any files."},
		ExtraReadAllows: []string{specPath},
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()
	// The repo's own instructions go in ahead of the survey: a
	// repository that documents its build ("make client builds the
	// CGO-free half"; "these four files are generated, never edit them")
	// answers in one line what the scribe otherwise spends a dozen tool
	// calls and several minutes proving from the outside.
	prompt := discoverPromptWith(e.environmentCard())
	if card := e.repoInstructionsCardFor(ctx, f); card != "" {
		prompt = card + "\n\n" + prompt
	}
	if err := sess.Send(ctx, prompt); err != nil {
		return nil, err
	}
	// Booked against the stage the pass started in, like oneShot: a
	// discovery that outlives the gate it was fired at is still that
	// stage's spend, and a pass nobody books is spend the envelope
	// cannot bound.
	stage := f.Stage
	var text assistantText
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return e.finishDiscovery(repoRoot, specPath, text.String())
			}
			switch ev.Kind {
			case agent.EventTextDelta:
				text.delta(ev.Text)
			case agent.EventMessage:
				text.message(ev.Text)
			case agent.EventUsage:
				e.recordOneShotUsage(f.ID, stage, ev.Usage)
			case agent.EventIdle:
				return e.finishDiscovery(repoRoot, specPath, text.String())
			case agent.EventError:
				return nil, ev.Err
			case agent.EventBudgetExhausted:
				// soft stop, same as the main engine loop's handling: the
				// in-flight response is done and no more turns will run, so
				// stop waiting rather than block forever for an idle that
				// isn't coming.
				return e.finishDiscovery(repoRoot, specPath, text.String())
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// repoInstructionsCardFor resolves f's repository root and returns its
// instruction card, or "" when the repo cannot be resolved or states
// none. It exists so the one-shot passes, which hold a feature rather
// than a session, can reach the same card stage sessions get.
func (e *Engine) repoInstructionsCardFor(ctx context.Context, f domain.Feature) string {
	return e.repoInstructionsCard(e.repoRootFor(ctx, f))
}

// finishDiscovery records a completed survey on the card and remembers it
// for the repository.
//
// What is cached is what the scribe FOUND, not what the card ends up
// carrying: recordChecks merges the survey into whatever the artifact
// already held, and those hand-authored entries are the card's own
// feature-specific checks. Caching the merge would leak one card's checks
// into every later card in the repo.
func (e *Engine) finishDiscovery(repoRoot, specPath, reply string) ([]domain.Check, error) {
	if discovered, _, _ := spec.ParseChecks(reply); len(discovered) > 0 {
		e.rememberChecks(repoRoot, discovered)
	}
	return e.recordChecks(specPath, reply)
}

// repoRootFor resolves f's repository root, or "" when it cannot be
// resolved.
func (e *Engine) repoRootFor(ctx context.Context, f domain.Feature) string {
	mgr, err := e.mgr(ctx, &f)
	if err != nil || mgr == nil {
		return ""
	}
	return mgr.RepoRoot()
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
	existing, found, _ := spec.ParseChecks(string(raw))
	if found && spec.ChecksAreDiscovered(string(raw)) {
		// discovery's own block landed while this pass was running
		return nil, nil
	}
	checks = spec.MergeChecks(existing, checks)
	out, err := spec.UpsertDiscoveredChecks(string(raw), checks)
	if err != nil {
		return nil, err
	}
	if err := atomicfile.Write(specPath, []byte(out), 0o600); err != nil {
		return nil, err
	}
	return checks, nil
}
