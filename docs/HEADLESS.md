# Driving gummi headlessly

The board is one way in. The other is the non-interactive driver: the same
engine and the same quality floor, with nobody at the keyboard. This page is
the full reference for that path. The README carries the short version, and
`gummi skill show` prints the loop as a calling agent reads it.

Headless verbs change *who approves a gate*, never *whether* review and
verify run. `run`, `research` and `resume` never merge. They stop at a
verified branch, and landing is the separate `merge` verb.

## Starting a card

```sh
gummi run --envelope 500 "Add a --format=json flag to the export command"
gummi run --envelope 500 --gate-approval autopilot "..."
gummi research --envelope 300 "Where does the exporter buffer, and why?"
```

Two things must be true before any work begins, and both fail loud:

- an envelope is named (`--envelope N`, or `GUMMI_ENVELOPE`). The board's
  creation form prefills 2000 credits you can edit; an unattended run has
  no one to read a default, so it must name one.
- an agent backend is configured. `gummi doctor` tells you which.

`--gate-approval` takes one of two modes. `attended` (the default) stops at
the design gate and hands the decision back through `resume`. `autopilot`
crosses its own gates and runs to a verified branch. The mode is saved on
the card; `resume` keeps it unless you pass the flag again. The retired
spellings `off`, `gates`, `caller`, `auto` and `full` are still accepted
and mapped.

Other `run` flags:

| flag | purpose |
|---|---|
| `--until plan` | stop cleanly before implementation, for a human design review. `plan` is the only stop |
| `--autonomous` | take the agent's recommended answer instead of stopping on a question. The card's history marks the answer as unattended |
| `--acceptance <file\|->` | seed the spec's verification plan |
| `--ref <id>` | your own tracker id, echoed in the stream. `status` and `resume` accept it in place of the card id |
| `--repo <name>` | which managed repository to create the card in. Required when `repos:` is configured |
| `--profile <name>` | the profile mapping roles to models |
| `--stage-timeout <dur>` | per-stage inactivity timeout, 0 disables |
| `--verbose` | add per-tool-call lines to the stream |

## Verbs

| command | purpose |
|---|---|
| `gummi run [flags] "<description>"` | create and drive one feature to a verified branch |
| `gummi research [flags] "<question>"` | create and drive one research card |
| `gummi resume <id\|ref> [decision]` | apply a decision and drive on |
| `gummi resume <id\|ref> --say "<line>"` | read a line the way the card page would and report what it would do, as a `say` event, without acting |
| `gummi status <id\|ref> [--json]` | stage, blockers, spend, branch state |
| `gummi watch <id\|ref> [--json] [--wait] [--once]` | follow the live agent stream of a card another gummi is driving |
| `gummi spec <id\|ref>` | the current spec or report markdown |
| `gummi diff <id\|ref>` | the worktree diff against main |
| `gummi verify <id\|ref>` | re-run the checks on a verified branch and finalize its card |
| `gummi merge <id\|ref> -m <message\|->` | land a verified branch as one squash commit |
| `gummi squash <id\|ref> -m <message\|->` | collapse a card's branch to one commit in place |
| `gummi commit <id\|ref> -m <message\|->` | commit a card's own uncommitted worktree changes onto its branch |
| `gummi clean <id\|ref>` | remove a landed card's worktree and branch |
| `gummi pr link\|unlink\|status\|comments <id> [flags]` | link a card to a PR you opened, or read its status and review comments |
| `gummi deps add\|rm <dependent> <depends-on>`, `gummi deps list <id>` | dependency edges between cards |
| `gummi ingest [flags] <spec-file>` | decompose a spec into feature proposals and materialize them |
| `gummi bugs ingest [flags]`, `gummi bugs new [flags]` | import bugs from GitHub issues, or add one by hand |
| `gummi init` | create and seed the `.gummi` workspace without opening the board |
| `gummi doctor [--json] [--deep]` | readiness: repo, backend, auth, profile, envelope, lock, per-role reach |
| `gummi skill show\|install\|list` | the calling-agent skill |

`status`, `watch`, `spec` and `diff` take no lock, so you can inspect a card
while a run is live. Everything that mutates the workspace (`run`,
`resume`, `merge`, `squash`, `commit`, `clean`) holds an exclusive `.gummi`
lock, so a headless run and the board never touch the same workspace at
once.

## Exit statuses

Every `run`, `research` and `resume` ends on a typed exit the caller
branches on:

| exit | status | caller action |
|---|---|---|
| `0` | `done` | verified branch ready. Report it and stop |
| `0` | `stopped` | `--until` reached its stop. `resume --approve` to continue |
| `0` | `said` | `--say` reported a reading and acted on nothing |
| `2` | `question` | a delegated question or a design gate. `resume --answer`, `--approve` or `--request-changes` |
| `3` | `blocked` | open `%%` or diff threads block a gate (resolve them, or `resume --request-changes`), or an unmet dependency blocks the coding stage (`blocking_deps` on the event: wait for it to land, or `gummi deps rm`) |
| `4` | `escalation` | a rerun or critique cap, or an unclear verdict. Report to a human; resumable |
| `5` | `exhausted` | envelope dry. `resume --envelope N` with a higher number |
| `6` | `timeout` | a stage went quiet. Report; resumable |
| `1` | `error` | setup or agent failure. Nothing partial landed |

## Resuming

`gummi resume` carries one decision flag at a time:

- `--answer "<text>"` resolves a delegated `ask_user` question. The answer
  rides the same round trip the board's picker rides, and the card's
  history records who answered and which option was chosen. When more
  than one decision is open, the newest one is resolved.
- `--approve` / `--request-changes "<note>"` decide a design gate. Passing
  `--answer` at a gate, or `--approve` at a question, is refused with a
  usage error naming the verb this stop actually takes.
- `--bounce [--note "<why>"]` rewinds one rerun edge: a verify failure back
  to implement, an implement-stage card back to plan. It is the board's
  `b` key.
- `--envelope N` raises the envelope before resuming. It never lowers it.
- `--say "<line>"` reads a line the way the card page reads typed text and
  reports the routing as a `say` event without acting. Use it to see what
  a sentence would do before you commit to a verb.

## Landing

A run stops at a verified branch on purpose. The landing commit is a
review decision, so the headless way to make it is explicit:

```sh
gummi merge FD-042 -m "feat(export): add a --format=json flag"
gummi merge FD-042 -m - <<'MSG'     # or read the message from stdin
feat(export): add a --format=json flag

The flag writes NDJSON to stdout instead of the table layout.
MSG
```

`merge` requires the card to be at a verified branch, takes no other
input, and is stricter than the board's dialog: the message must be a
Conventional Commits `type(scope): summary` with no diff dump and no agent
attribution, or the command refuses before touching git. On success it
emits a `merged` event with the landed sha and moves the card to `done`.

`clean <id>` is the board's `c` key: it removes a landed card's worktree
and branch and keeps the card as a done entry. It refuses anything that
has not actually landed, or that carries tracked-dirty rework.

`status --json` carries two distinct terminal signals. `verified:true`
means the verify gate passed and the branch is ready to land; this is
where a headless run stops and what a CI caller polls for. `done:true`
means the branch was squash-merged into main, by the board's `m`, by
`gummi merge`, or by hand. After a headless run expect `verified:true`
with `done:false` until you merge.

## Landing through a PR

Some repos land through a PR on GitHub instead of `gummi merge`. gummi
still never writes to GitHub on that route. It only names and reads the
PR you already opened. The loop is four commands:

```sh
gummi pr link FD-042 --auto                          # or a URL / number instead of --auto
gummi pr comments FD-042 --ingest                    # unresolved review threads become diff annotations
gummi resume FD-042 --bounce --note "address review" # rewinds to fix the annotated lines
git push                                             # push the fix onto the open PR
```

What you do before the first push depends on the repo's merge setting:

| merge method | before you push |
|---|---|
| squash merge | nothing; GitHub collapses the branch to one commit |
| merge commit / rebase merge | `gummi squash <id> -m <message\|->` first, so the branch lands as one commit either way. Later fix rounds may keep their own commits or `squash` again, as long as no review thread is open |

`squash` refuses while the worktree has uncommitted changes, because
folding them silently into the collapsed commit would hide what changed.
Commit them first, then squash:

```sh
gummi commit FD-042 -m "fix(export): tighten the empty-array case"
gummi squash FD-042 -m "feat(export): add a --format=json flag"
```

`commit` commits exactly the card's own uncommitted worktree changes onto
its own branch with your message. It touches no PR, remote or main
checkout, moves no stage, and has no precondition. A clean worktree is a
no-op, reported as such.

## Dependencies

`gummi deps add <dependent> <depends-on>` records that one card needs
another (`rm` and `list` remove and read the edges). A dependency counts
as met only when the target reaches `done`, meaning verified *and*
landed. Anything short of that blocks the dependent card's entry into
implement with a `blocked` exit naming the outstanding cards. The board
shows the same edges through the `p` key's dependency picker, and
`gummi ingest` seeds edges when a decomposed spec says one proposal
depends on another.

## The calling-agent skill

gummi generates its own skill, a `SKILL.md` documenting this loop, and
installs it where Claude Code, GitHub Copilot CLI, Codex and opencode
read it:

```sh
gummi skill install          # project scope: .claude/skills + .agents/skills
gummi doctor                 # then check backend, auth and envelope are ready
```

A project-scope install writes `.claude/skills/gummi/SKILL.md` for Claude,
Copilot and opencode, plus `.agents/skills/gummi/SKILL.md` for Codex.
`--scope user` writes to each detected agent's home instead, `--agent`
targets one, `--dry-run` prints what would be written, and `--check`
fails if any target is absent or drifted. The command grammar and exit
table are generated from the binary's real flags, so they cannot drift.
The frontmatter is version-stamped, so `install` and `list` detect a
stale or edited file and refuse to overwrite it without `--force`.

`gummi doctor` is the readiness check the skill's first-run setup runs
(`--json` for a machine-readable checklist, `--deep` to probe each role's
backend). It reports and never repairs: a backend needing login is
surfaced as the exact command for a human to run. Provider config lives in
each backend's native store, never in `profiles.yaml`.
