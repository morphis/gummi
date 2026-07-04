# gummi

> A meta-harness for coding agents. Drive a fleet of agents through a
> spec-driven workflow across git worktrees, from one beautiful TUI.

gummi is under construction. `docs/DESIGN.md` is the design;

## What works today (M0 — walking skeleton)

- `gummi init` — sets up `.gummi/` in a repo: state dir (0700), spec
  drafts, worktrees dir, ignore rules, FD counter.
- `gummi board` — the kanban TUI:
  - create features (`n`): title, one-liner, profile preset, skip
    flags; IDs minted as `FD-NNN` with merge-safe retry.
  - cards grouped by super-state with stage-accent glyphs, `j/k` and
    `1..9` navigation, `?` help.
  - feature dashboard: stage, branch, worktree, budget, full
    transition audit trail.
  - the fixed workflow (`g` advance, `b` bounce from review/verify):
    brainstorm → spec → plan → implement → review → verify → done,
    with skip flags for brainstorm/plan. Review and verify are never
    skippable.
  - worktree lifecycle: `git worktree` + branch created at spec
    approval under `.gummi/worktrees/FD-NNN`; the spec draft is
    committed to the feature branch at the same moment; delete (`x`)
    removes worktree, branch, and record.
  - spec surface (`s`): glamour read view with an open-question
    checklist (`%%` markers), annotate mode with line cursor,
    threaded `%% @user(date):` comments, `n/p` marker jumps, `e`
    opens `$EDITOR`.

**Agent layer (M1 spike landed).** `internal/agent` is the adapter
abstraction (DESIGN §4.1): a `Fake` for tests and a `Copilot` adapter
over the official Go SDK. A BYOK (bring-your-own-key) session against
any OpenAI-compatible endpoint works **without GitHub authentication**;
`internal/agent/fakeopenai`
is a localhost fake provider for tests. Not yet wired into the TUI —
the interactive chat pane and autonomous implement stage are the rest
of M1. Profiles, budgets, the scheduler, and the review loop follow
(see the roadmap in `docs/DESIGN.md` §9).

## Try it

```sh
make demo   # creates a throwaway repo with gummi initialized
make e2e    # scripted TUI drive asserting the full lifecycle (needs tmux)
```

## Development

```sh
make build          # build bin/gummi
make test           # run all tests
make lint           # go vet + golangci-lint
make golden-update  # regenerate UI golden files
make ci             # the full phase gate
```

A [vhs](https://github.com/charmbracelet/vhs) tape for the demo GIF
lives at `demo/gummi.tape` (recording needs ttyd + ffmpeg).
