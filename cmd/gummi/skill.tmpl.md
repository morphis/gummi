# Using gummi to ship a feature or bug

gummi is a meta-harness that drives coding agents through a **fixed, spec-driven
workflow** — spec → review → verify — with each work item on its own git
worktree and branch. You (the calling agent) invoke `gummi run`, answer the
questions it delegates to you, and report the **verified branch** it produces.
gummi orchestrates the coding work; it never merges and never skips review or
verify.

## When to use gummi (vs. editing directly)

Use gummi for **PR-sized work that deserves a real quality bar** — a feature or
bug fix that warrants a written spec, an independent code review, a verify pass
(build/test/lint + acceptance checks), and an isolated branch a human can review
before it lands.

Edit files directly for **trivia**: a typo, a one-line config tweak, a rename —
anything where a spec-and-review cycle is pure overhead. If the change is big
enough that you'd open a PR for it, it's big enough for gummi.

**Oversized asks:** if the request spans several independently shippable pieces,
don't push it through one run. Decompose it into PR-sized features yourself and
loop `gummi run` once per feature, sequentially — one verified branch each.

## First run: check readiness with `gummi doctor`

Before the first `gummi run` in a repo, run:

```
gummi doctor --json
```

It returns a structured checklist — repo, workspace, backend, profile, auth,
envelope, lock. Repair each item that isn't `ok`:

- **backend** — set gummi's backend to the **same agent that is driving it**:
  if you are Claude Code, export `GUMMI_AGENT=claude` so gummi drives Claude with
  Claude. Match the backend to yourself by default — it keeps the whole pipeline
  on one vendor's auth, and it sidesteps the cross-model trap where the claude
  backend forwards each role's model to the Claude CLI as `--model` and the CLI
  rejects any non-`claude-*` id (a mismatched run passes spec, then dies at
  implement).
- **profile** — **do not rely on the auto-seeded default** (its implementer/scribe
  roles are OpenAI ids the claude backend cannot drive). Instead **write the
  `default` profile in `.gummi/profiles.yaml` yourself, and ask the human their
  model preferences first.** Offer a cost-tiered shape — a frontier model for the
  architect/reviewer roles, a cheaper one for implementer/scribe — with every
  role on a model your backend can drive (for the claude backend, every role a
  `claude-*` id, e.g. architect/reviewer `claude-sonnet-5`, implementer
  `claude-sonnet-5`, scribe `claude-haiku-4-5`). If the human has no preference,
  seed those tiered defaults. Tier down rather than pointing every role at the
  top frontier model — you don't need the biggest model on the scribe.
- **auth** — if a check reports auth is needed, gummi hands you the **exact
  command**. Give it to the **human** to run (e.g. surface it with the `!`
  prefix). You never handle secrets: API keys are referenced by environment
  variable **name**, never written as literal values.
- **envelope** — every run needs a credit envelope. Pass `--envelope N` per run,
  or set `GUMMI_ENVELOPE`. A run refuses to start without one.

Re-run `gummi doctor` until it reports `ready: true`, then proceed.

## Command grammar

```
{{.Grammar}}
```

A run drives one feature via the **quick route** by default (spec → implement →
review → verify). `--full` opts into the brainstorm + plan stages for larger
work. The design gates auto-cross under the default `--gate-approval=auto`; pass
`--gate-approval=caller` to approve them yourself.

`status`, `spec`, and `diff` are **read-only** — they take no lock, so you can
inspect a feature while a run is live.

## The decision loop

Each `gummi run` / `gummi resume` streams milestone + decision NDJSON on stdout
and exits with a **typed status**. Branch on the exit code:

{{.ExitTable}}

**Capture that exit code directly — never through a pipe.** `gummi run … | tee
run.log` (or `| head`, `| grep`) reports the *filter's* exit status, not
gummi's, so a `question`/`exhausted`/`error` looks like success. Redirect to a
file and read `$?` on the next line:

```
gummi run --envelope N "<description>" > run.log; ec=$?
```

(or `set -o pipefail` if you must pipe). Equivalently, the exit code is fully
redundant with the stream: the **last NDJSON line's `event`** (`done`,
`question`, `blocked`, `escalation`, `exhausted`, `timeout`, `stopped`,
`error`) is the terminal status — parse that if you'd rather not depend on the
shell.

The loop you run:

1. `gummi run --envelope N "<description>"` (add `--ref <id>` to correlate with
   your own tracker, `--acceptance <file|->` to seed the verification plan).
2. Read the exit code:
   - **done (0)** — parse the final `done` line for the branch; report it to your
     caller/human and stop. The branch is verified, not merged.
   - **question (2)** — a design decision was delegated to you. Answer from your
     **task context first**; if unsure, read the spec (`gummi spec <id>`) or diff
     (`gummi diff <id>`); use the `recommended` option only as a tiebreaker. Then
     `gummi resume <id> --answer "<text-or-option>"`. A caller gate (under
     `--gate-approval=caller`) is the same shape: `resume <id> --approve` or
     `--request-changes "<note>"`.
   - **blocked (3)** — open `%%` spec threads or diff comments block a gate.
     Resolve them, or `gummi resume <id> --request-changes "<note>"` to send it
     back; if you can't, escalate to the human.
   - **escalation (4) / exhausted (5) / timeout (6)** — stop and report to the
     human. These are durable and resumable: the card stays on the board.
     `exhausted` resumes with `gummi resume <id> --envelope N`, where N is
     larger than the envelope it ran dry on (the `envelope` field on the
     `exhausted` event) — the raise is a floor, so it can never shrink the
     budget. `escalation` and `timeout` resume once the human weighs in.
   - **error (1)** — a setup/agent failure. Check `gummi status <id>`: a
     pre-id setup failure landed nothing (report it), but a mid-run failure
     can leave a durable, non-terminal card that a `gummi resume <id>` may
     finish — the error event's `resumable`/`stage` fields say which.
3. After a `resume`, read the new exit code and repeat until `done`.

For a **small or well-specified** feature whose delegated questions you'd answer
with the `recommended` option anyway, start with `--autonomous`: it auto-takes
the recommended answer instead of stopping at `question`, shipping the feature to
a verified branch hands-free. Reserve the checkpointing loop above for genuinely
ambiguous forks where the recommended answer might be wrong.

## Stop early for a human design review

For work where a human should sign off on the design before implementation burns
tokens, add `--until spec` (or `--until plan` on `--full`). The run stops cleanly
at that stage — event `stopped`, exit 0 — with the feature parked and resumable.
Hand the spec to the human (`gummi spec <id>`); once approved, `gummi resume <id>
--approve` continues to the verified branch.

## What gummi guarantees

- **Review and verify always run.** There is no flag, profile, or config that
  skips them — the quality floor is invariant.
- **gummi stops at a verified branch. It never merges.** Landing (a PR, a push, a
  squash-merge) stays with the human.
- **It fails loud.** Anything a human would resolve — a design question, a
  rerun/critique cap, an exhausted envelope, a stalled stage — becomes a typed
  non-zero exit and a durable, resumable card, never a silent auto-proceed.

Your job: drive the loop, answer what's delegated to you, and report the verified
branch upward.
