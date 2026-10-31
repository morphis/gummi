# Workflows

A **workflow** is a recipe of steps that a session runs in its own
worktree. Each step is a fresh agent with one job — design, build,
test, review, report — and nothing but what the recipe hands it. Steps
run one after another, side by side, or round a loop until a condition
holds. When the last step is done, gummi runs the spec's checks and
sends the session a report. The changes stay in the tree, uncommitted,
for the session or you to read and commit.

This page is for people who run workflows and write their own recipes.
The first half is a guide; the second half is a reference of every
element a recipe may hold.

- [How a run works](#how-a-run-works)
- [Running a workflow](#running-a-workflow)
- [The bundled recipes](#the-bundled-recipes)
- [Writing your own recipe](#writing-your-own-recipe)
- [Reference](#reference)
- [Common mistakes](#common-mistakes)

## How a run works

```
session ──writes──▶ spec ──proposes──▶ run ──you allow──▶ steps work in the tree ──▶ report
   ▲                                                                                  │
   └──────────── the session reads the report and the diff, commits what it keeps ────┘
```

- **A run is judged against a spec.** The session writes the spec first
  (Problem, Approach, Done when, Limits). Its *done-when* items are the
  run's goal: each one is a check command, or `judge: true` for
  something only a reader can decide. The run freezes the spec revision
  it was proposed on.
- **You allow every run.** The session proposes; you answer **Allow**,
  **Always allow** (this session's later runs too), **Deny**, or type
  feedback. Only the option itself sets the standing allowance: feedback
  that merely starts with "Always allow" is feedback. No agent allows its
  own run.
- **Steps share the session's tree.** gummi snapshots the tree at every
  step boundary, so each step has a diff of its own and you can
  **Revert run** (or revert to before any step) from the run's block.
- **Steps see only what the recipe gives them.** A step never reads the
  session's conversation or another step's transcript — only its filled
  prompt: the spec, inputs, artifacts and the outputs of steps the
  prompt names.
- **Money is the session's budget.** Nothing is estimated. A run spends
  from the session's envelope until it is spent, then pauses until you
  top it up. Steps at work side by side share what the pool has left,
  so their caps at the time each starts stay within it. While the pool
  is far from spent, gummi trusts a step's own headroom for a few
  seconds between looks, so side-by-side steps can overshoot it by that
  much spend before the pause.
- **Questions come to you.** A step's `ask_user`, a loop at its `max`,
  and a loop that cannot judge its own condition all wait on you.
- **gummi never commits.** At the end it runs the done-when checks,
  writes the report into the spec and sends it to the session. A report
  lost to a crash between the run's end and its delivery is delivered
  again when gummi next starts. **Retry** runs a failed or stopped step
  again and keeps the earlier attempt beside it; an earlier attempt is
  never run again itself.

## Running a workflow

**From a session.** Type `/workflow` in the composer, optionally with a
recipe, a spec and a reason:

```
/workflow deliver on spec 2 the new export command
```

The session writes or updates the spec and proposes the run; you allow
it. A spec's **Propose a run** (the web page's Specs tab, `w` in the
terminal's spec view) does the same. A session may also propose a run on
its own when the work calls for one, and gummi's triage may suggest a
recipe for a session's first request — both still wait on your answer.

**Unattended.** `gummi run` runs one workflow with nobody at the
keyboard. On a terminal it prints readable lines; piped, it streams
NDJSON (`--format text|jsonl` overrides either), and `--jsonl <file>` also
writes every event, `--verbose` activity included, to a file:

```sh
gummi run --envelope 2 --workflow investigate "Why is startup slow?"
gummi run --envelope 5 --workflow deliver --spec spec.md
gummi run --envelope 3 --workflow fix-until-green --check 'make test' "Make the tests pass"
gummi run --envelope 2 --workflow triage --input report=@bug.md   # a workspace recipe with an input
```

`--input name=value` (or `name=@file`, `name=-` for stdin, or an
`--inputs` YAML/JSON file) fills the recipe's inputs; `--spec` gives the
spec, or gummi writes one from the prompt, the inputs and each
`--check`. `--workflow-file` runs a recipe file that is not in the
workspace.

**On a schedule.** An agent (a session with a home of its own) may
propose a schedule that runs a recipe on a cadence; you enable it in the
schedules view. That is where `run:` and `dispatch` steps are used.

### Where recipes live

gummi looks for a recipe by name in this order:

1. `.gummi/workflows/<name>.yaml` in the workspace — your own;
2. the recipes bundled with gummi.

A workspace recipe with a bundled recipe's name overrides it. A session
can also hand over a recipe it wrote inline for one piece of work; if
that run went well, the run's block offers **Save as recipe…**, which
writes it to `.gummi/workflows/` for every session to use. A recipe's
file name is its name: letters, digits, `-` and `_`, starting with a
letter or digit.

The recipe is copied into the run when it is proposed. What you allow is
that copy: editing the file later changes the next run, not this one.

## The bundled recipes

| recipe | what it does |
|---|---|
| `deliver` | A contractor writes a design; then an implementer builds and two reviewers on two models review, round a loop until a merged verdict says `APPROVED` (at most 3 rounds); then a reviewer judges every done-when item. For features and changes across packages. |
| `fix-until-green` | A scout first maps the cause, every place it lives and the behaviour that must keep working, and adds a check for each; then one implementer, round a loop until every done-when check passes (at most 5 rounds). For checks that fail and a fix that needs no design. |
| `investigate` | A scout reads and reproduces; a scribe writes `report.md`. Changes no code. For a question that needs a written answer. |
| `ship` | Builds, opens a draft PR, then waits on it without a model and fixes red checks, requested changes and trusted comments until a person merges. Needs the session's "agent may publish". |
| `merge-base` | Merges a PR's moved base into the branch and resolves the conflicts. Meant as a `ship` handler. |

Their YAML is in `internal/workflowrecipe/bundled/`, and a session's
`workflow_recipes` tool shows any recipe's text. Copy one into
`.gummi/workflows/` and change it rather than starting from nothing.

## Writing your own recipe

### A first recipe

A recipe is a YAML file with a `name`, a `description`, optional
`inputs` and a list of `steps`. Here is a test-first recipe: a tester
writes failing tests, an implementer loops until the checks pass, and a
reviewer has the last word.

```yaml
name: test-first
description: Write the failing tests, then build until they pass
steps:
  - id: tests
    role: tester
    prompt: |
      Write the tests the spec's done-when items name. They must fail
      now. Do not implement anything.

      {{spec}}

  - id: build
    repeat:
      max: 4
      until: checks
      onMax: pause
      steps:
        - id: implement
          role: implementer
          prompt: |
            Make the tests described below pass without changing them.

            {{tests.output}}

            {{spec}}

  - id: validate
    role: reviewer
    prompt: |
      Read the diff in {{run.diff}} against the spec. End with one line
      per done-when item: MET: DW-n, or UNMET: DW-n and why.

      {{spec}}
```

Save it as `.gummi/workflows/test-first.yaml` and run it with
`/workflow test-first`.

### Think in hand-offs

Each step starts with an empty head. Write every prompt as a brief to a
capable stranger:

- **Put `{{spec}}` in every prompt that needs the goal.** It is the
  frozen spec's full text.
- **Name what came before.** `{{tests.output}}` is the `tests` step's
  final message; `{{previous.output}}` is the step just before. A step
  that is not named is not seen.
- **Use artifacts for anything long or structured.** Declare
  `artifacts: { design: design.md }` on the step that writes it, and
  refer to `{{artifacts.design}}` — the file's absolute path — in the
  steps that read it. Artifacts live in the run's directory under
  `.gummi/runs/`, never in your diff.
- **Point reviewers at `{{run.diff}}`.** It is a file holding the diff
  from the run's start to now, written just before the step starts.
- **Do not repeat the role.** gummi already gives each step its role's
  preamble (a reviewer is told to change nothing, nobody is told to
  commit). Say what this step must do, and what it must not.

### Pick the roles

The role says what kind of work a step does and which model runs it:
each role has its own slot in your profile (`P` in the terminal,
Profiles on the web page). `scout` and `reviewer` read; the others
write. Put readers side by side freely; keep writers in sequence, since
two writers in one tree overwrite each other.

To run two steps of one role on different models — two independent
reviewers, say — give one a `slot:` such as `reviewer-alt` and add that
slot to your profile. A slot the profile does not have falls back to the
role's own. Do not use slots to ask for a bigger or cheaper model;
that is what the profile decides.

To pin a step to one agent and model whatever the profile says, give it
`agent:` (a backend such as `claude`, `copilot` or `opencode`), `model:`
(a model id), or both. They win over `slot:` and the role. `agent:` alone
runs that backend's default model — except `opencode`, which has none and
needs `model: provider/model`; `model:` alone runs on the backend the
profile gives the step. A recipe that pins a model only works where that
backend has it, so prefer roles and slots for recipes you share. An
unknown `agent:`, or a model its backend refuses, is refused when the run
is proposed. A step takes only the backend and model from its slot,
pinned or not: a slot's other settings, such as `output_token_max`, do
not reach a workflow step.

### Loops

A `repeat` runs its steps again until its `until` holds, at most `max`
times:

- `until: checks` — every done-when check of the spec passes. The
  simplest loop, for a spec with good checks.
- `until: { verdict: APPROVED, in: review }` — a step in the loop writes
  the artifact `review` as JSON with a `verdict` field, and the loop
  ends when it is the word. Tell that step the exact JSON to write, and
  tell the implementer to read the last review first: `If
  {{artifacts.review}} exists, address its findings first.`
- `until: { contains: "VERDICT: PASS" }` — the loop's last step's output
  contains the text. Ask for it on the last line, and pick a marker the
  step writes only when it means it.

When `max` is reached without the condition, `onMax` decides: `pause`
asks you, `continue` moves on, `abort` ends the run partial. A loop
gummi cannot judge — the verdict file was never written, a check ran no
tests — always asks you, whatever `onMax` says.

### Side by side

A `parallel` runs plain steps at once, in the same tree. `join` says
when the block is done: `all` (fail on the first failure), `allSettled`
(wait for all, fail only if all fail) or `any` (done on the first
success). The step after the block may read each branch by id.

### End with a judge

At the end gummi runs each done-when item's check. Items marked
`judge: true` have no check: they are judged by `MET: DW-n` and
`UNMET: DW-n <why>` lines in the **last step's** output. If your recipe
does not end with a step that writes them, those items stay unjudged and
the spec stays `partial`.

### Let a planner decide the steps

A `planner` step may end its output with a fenced `gummi-workflow` block
holding a YAML list of entries, exactly as under `steps:`. Those entries
replace the run's remaining steps once they pass the recipe checks and
you allow the revised plan. A step already running is never rewritten. A
revised plan may not add `pr:`, `watch:`, `run:` or `dispatch:` steps,
and the question you are asked lists every command, dispatch, PR act,
trust filter and handler it holds, as a proposal does. A fence inside the
block must be balanced, and a block that is never closed is refused
rather than cut short (fences inside an indented YAML block scalar are
just text).

````
```gummi-workflow
- id: implement
  role: implementer
  prompt: "Build the parser described in {{artifacts.plan}}.\n\n{{spec}}"
- id: review
  role: reviewer
  prompt: "Review {{run.diff}} against the spec.\n\n{{spec}}"
```
````

### Try it

A recipe that cannot run is refused when it is proposed, with every
problem listed, and nothing starts. Warnings (two writers side by side,
an ignored field) are shown on the proposal. Read the proposal before
you allow it: it shows each step, the model it runs on, every check
command, and which checks already pass before the run (a check that
already passes proves nothing about the run).

## Reference

### The recipe

```yaml
name: my-recipe          # required
description: One line    # shown when picking a recipe, and to triage
inputs:                  # optional
  report: "the bug report as filed"            # required input: name: hint
  area:   { hint: "where to look", default: "" }
  extra:  { hint: "anything else", required: false }
steps:                   # required, at least one entry; they run in order
  - …
```

| field | required | is |
|---|---|---|
| `name` | yes | the recipe's name |
| `description` | no | one line saying what it is for |
| `inputs` | no | named values a run is given, read as `{{inputs.<name>}}` |
| `steps` | yes | the entries, run in order |

**Inputs.** Each input is either a hint alone (`name: "hint"`), which
makes it required, or a mapping with `hint` and one of `default: <text>`
(optional, filled with the default) or `required: false` (optional,
filled empty). An input with a default cannot also be `required: true`.
A run given an input the recipe does not declare, or missing a required
one, is refused. An input reaches a step wrapped in delimiters naming
it, as data, never as instructions.

### Entries

Each entry of `steps` is exactly one of a **step**, a **`parallel`** or
a **`repeat`**. Every entry has an `id`.

**Ids** are unique across the whole recipe, nested ones included. They
hold only letters, digits, `-` and `_`, and are never digits alone.

#### Agent step

```yaml
- id: implement
  role: implementer
  slot: implementer-alt        # optional
  prompt: |
    …
  artifacts: { notes: notes.md }   # optional
  captureOutput: false             # optional
```

| field | required | is |
|---|---|---|
| `id` | yes | the step's id |
| `role` | yes | one of the roles below |
| `prompt` | yes | the step's brief; may hold [templates](#templates) |
| `slot` | no | the profile slot whose model runs it, instead of the role's: lower-case letters, digits and `-`, starting with a letter |
| `agent` | no | the backend the step runs on, instead of the profile's: lower-case letters, digits and `-`; wins over `slot` |
| `model` | no | the model id the step runs on, instead of the profile's; no spaces; wins over `slot` |
| `artifacts` | no | name → plain file name in the run's directory (no path) |
| `captureOutput` | no | `false` keeps the step's final message from later steps; default `true` |

`envelope` and `tier` on a step are ignored, with a warning: a run
spends from the session's budget, and a step's model comes from its
role.

#### Roles

| role | does | reads or writes | model slot fallback |
|---|---|---|---|
| `scout` | reads, reproduces, maps | reads | `scout` → `implementer` |
| `planner` | plans; may rewrite the remaining steps | writes | `planner` → `architect` → `implementer` |
| `contractor` | design, interfaces, stubs | writes | `contractor` → `architect` → `implementer` |
| `implementer` | builds | writes | `implementer` |
| `tester` | writes and extends tests | writes | `tester` → `implementer` |
| `scribe` | docs and reports | writes | `scribe` → `implementer` |
| `reviewer` | reads the work against the spec | reads | `reviewer` → `implementer` |

A step's `slot:`, when given, is looked up first. Reading roles are told
to change nothing; one whose snapshot changed anyway is flagged in the
run's report, as evidence.

#### `parallel`

```yaml
- id: reviews
  parallel:
    join: allSettled
    steps:
      - { id: review-a, role: reviewer, prompt: "…" }
      - { id: review-b, role: reviewer, slot: reviewer-alt, prompt: "…" }
```

| field | required | is |
|---|---|---|
| `join` | yes | `all`, `allSettled` or `any` |
| `steps` | yes | plain steps only — no `parallel`, `repeat`, `pr:` or `watch:` inside |

| `join` | the block |
|---|---|
| `all` | fails as soon as one branch fails, and stops the others |
| `allSettled` | waits for every branch; fails only if all fail |
| `any` | settles when one branch succeeds, and stops the rest |

More than one writing role in one `parallel` is a warning.

#### `repeat`

```yaml
- id: build
  repeat:
    max: 3
    until: { verdict: APPROVED, in: review }
    onMax: pause
    steps:
      - …
```

| field | required | is |
|---|---|---|
| `max` | yes | the most iterations, 1 or more |
| `until` | yes | exactly one stop condition, below |
| `onMax` | no | `pause` (default), `continue` or `abort` |
| `steps` | yes | entries; may hold steps, a `parallel` or another `repeat` |

| `until` | the loop ends when |
|---|---|
| `checks` | every done-when check of the frozen spec passes in the tree |
| `{ verdict: <word>, in: <artifact> }` | the artifact's JSON field `verdict` equals the word; a step inside the loop must declare the artifact |
| `{ contains: <text> }` | the loop's last step's output contains the text |

| `onMax` | at `max` without the condition |
|---|---|
| `pause` | asks you: one more iteration, continue, or stop |
| `continue` | goes on to the next entry |
| `abort` | ends the run partial |

A condition that cannot be judged (no checks, a check that ran no tests
or timed out, a verdict artifact never written) asks you, whatever
`onMax` says. One more iteration given there does not count against
`max`.

#### `run:` — a command step

```yaml
- id: check
  run: ./check-release.sh
  quietExit: 1
```

Runs a command through `sh -c` in the run's tree, with no model and no
cost. It has no role, slot, agent, model, prompt or artifacts.

- **Exit 0** passes; the command's standard output is the step's output.
- **`quietExit`** (1–255) ends the run quietly, leaving no trace — for a
  scheduled check that finds nothing to do. Only the recipe's first step
  may have it.
- **Any other exit** fails the step.
- **Stop** kills the command. A **pause** (yours, or a spent pool) does
  not: a command spends nothing, so it runs to its end, and the run
  carries on from there when it resumes.
- A template in a command is filled as one single-quoted shell word:
  write it as a word of its own, never inside quotes. `{{run.diff}}` is
  not allowed in a command.

#### `dispatch` — mint a session in another repository

Only an agent's runs may dispatch.

```yaml
- id: bump
  dispatch:
    repo: myrepo
    repos: [docs-site]          # optional: more repositories to attach
    title: "Bump foo to {{check.output}}"
    envelope: 5
    recipe: fix-until-green
    spec: "Upstream foo released {{check.output}}. Bump it."
```

| field | required | is |
|---|---|---|
| `repo` | yes | a configured repository's name — a literal, never a template or a path |
| `repos` | no | more configured repositories the session attaches; literals, no repeats |
| `title` | yes | the new session's title |
| `envelope` | yes | its budget in dollars (`5`, `2.50`), carved from the agent's own |
| `recipe` | no | a recipe the new session runs at once |
| `spec` | with `recipe` | the new session's spec 1, which that run is judged against |
| `prompt` | without `recipe` | the new session's opening turn |

`title`, `spec` and `prompt` may hold templates, filled as plain text. A
dispatched recipe cannot dispatch in turn.

#### `pr:` — a publishing act

Needs the session's "agent may publish" switch; a run with a `pr:` or
`watch:` step is refused at the proposal without it.

```yaml
- id: open
  pr: { act: create, draft: true }
```

| field | is |
|---|---|
| `act` | `push`, `create`, `update` or `ready` |
| `draft` | `true` opens a draft; only with `create` |

Never forced, and refused as a session's own publishing would be (a
dirty tree, a stacked or landed card, a moved target). A refusal fails
the step. Not inside a `parallel` or a handler.

#### `watch:` — wait on the PR

```yaml
- id: shepherd
  watch:
    until: merged
    trust: "{{inputs.trust}}"
    max: 6
    quiet: 7d
    onClosed: abort
    checks: [ci, lint]
    on:
      checks-failed: { recipe: fix-until-green }
      changes-requested: { goto: build }
      comment:
        - id: answer
          role: implementer
          prompt: "Address this and commit. Do not reply on GitHub.\n\n{{event}}"
      base-moved: { recipe: merge-base, max: 2 }
```

Waits on the session's PR without a model and spends nothing while it
waits. At most one per recipe, at the top level — never in a `parallel`,
a `repeat` or a handler — after a step that pushes.

| field | required | is |
|---|---|---|
| `until` | no | `merged` (default): a person merged it; `green-approved`: every check passed and the review is APPROVED |
| `trust` | no | whose feedback is read, below; a literal or one `{{inputs.<name>}}` |
| `max` | no | handler passes of every kind together before it asks you; `0` (default) is no limit |
| `quiet` | no | ask you after nothing happened this long: `7d`, `12h`, `30m`; empty never asks |
| `onClosed` | no | the PR closed unmerged: `abort` (default) ends the run partial, `continue` goes on |
| `checks` | no | the checks whose logs a handler is given; others are left out |
| `on` | yes | at least one event and its handler |

**Events**, one handler at a time, in this order:

| event | when |
|---|---|
| `base-moved` | the PR cannot merge cleanly against its base, or the base moved under a required up-to-date check |
| `checks-failed` | every check on the head has settled and one failed |
| `changes-requested` | a trusted review asks for changes, or a trusted unresolved thread is new |
| `comment` | a trusted top-level comment is new |

**Handlers** are exactly one of:

| form | runs |
|---|---|
| a list of steps | those plain agent steps; they may read `{{event}}` |
| `{ recipe: <name> }` | that recipe as a nested block, on the same tree, budget and spec; it gets the event as its input `event` if it declares one. It may not hold a `watch:`, `pr:` or `dispatch` step |
| `{ goto: <id> }` | the top-level steps from `<id>` up to the watch again; those steps read the event as `{{watch.event}}` |

A step id in a handler's recipe may not be one the recipe's own steps use
(`Check` refuses it): a step is found by its id.

A mapping handler may add `max:` to bound that event's passes. After
each pass gummi pushes (never forced) and the watch waits again. The
same event on the same head after a pass, or a pass that changed
nothing, asks you instead of looping.

**`trust`** is a comma-separated list of `@login`, `@org/team` and one
of `role:write`, `role:maintain`, `role:admin`. Empty means the login
gummi publishes as. Feedback from anyone else is counted on the watch's
strip, never read; you may forward it by hand. A team or permission
gummi cannot read makes the watch ask you rather than guess.

### Templates

Templates are filled when a step starts. Each must name something that
exists at that point, or the recipe is refused.

| template | is | where |
|---|---|---|
| `{{spec}}` | the frozen spec revision's text | anywhere |
| `{{inputs.<name>}}` | a declared input, wrapped as data | anywhere |
| `{{artifacts.<name>}}` | the absolute path of an artifact some step declares | anywhere |
| `{{previous.output}}` | the final message of the step just before | not the first step |
| `{{<id>.output}}` | an earlier step's final message; in a `repeat`, its latest iteration | after that step |
| `{{run.diff}}` | the path of a file holding the diff from the run's start to now | anywhere but a `run:` command |
| `{{event}}` | the event that started this pass, as data | a watch handler's steps |
| `{{watch.event}}` | the event that sent a `goto` pass back; empty on the first pass | top-level steps before the watch |

A step's output is wrapped in delimiters naming its step, so text from
one step cannot pose as instructions to the next. After a `parallel`,
`{{previous.output}}` is its last branch; name branches by id instead.

### Lines a step may write

gummi reads these lines in a step's final message:

| line | effect |
|---|---|
| `FAILED: <why>` | as the last line, fails the step; outside a `parallel`, a failed step ends the run partial |
| `MET: DW-n` / `UNMET: DW-n <why>` | in the run's last step, judge items marked `judge: true` |
| `DECISION: <what>` | a choice the step made that nobody asked it to; goes into the report |
| `OPEN: <what>` | something left open; goes into the report |
| a fenced `gummi-done-when` block | adds its checks to the run, after the spec's items; a step that maps a bug's reach uses it to make the run prove each place it found |
| a fenced `gummi-workflow` block | from a `planner`, replaces the remaining steps once you allow it |

A step also fails when its agent errors.

### Artifacts

An artifact is a file in the run's directory
(`.gummi/runs/<session>/<run>/`), named by the step that declares it.
If a step ends without writing an artifact it declared, gummi captures
it from the step's output — the last non-empty JSON object for a `.json` file,
otherwise the last fenced block — and notes that it did. Artifacts are
shown on the run's block once written.

### What a recipe is checked for

A recipe is refused, with every problem listed, when it has:

- no steps, or an entry that is more than one kind, or an unknown field
  (an inline watch handler's steps included);
- an id missing, used twice, or not made of letters, digits, `-` and `_`;
- an unknown role, a malformed slot, or an agent step without a prompt;
- a malformed `agent`, a `model` with spaces, or either on a `run:`,
  `dispatch`, `pr:` or `watch:` step; an `agent` gummi has no adapter
  for, or a `model` that agent cannot run — `opencode` without a
  `provider/model`, a claude model misspelt or from another provider —
  wherever the step sits, a dispatched recipe included; a `model` alone
  the backend the profile gives the step cannot run (a dispatched
  recipe's is checked when its step starts);
- an artifact file name that is not a plain file name;
- a template naming no input, no declared artifact or no earlier step;
- a `repeat` without `max` or without exactly one `until`, an unknown
  `onMax`, or a verdict `until` whose artifact no step in the loop
  declares;
- a `parallel` without a valid `join`, or holding anything but plain
  steps;
- a `run:` with `quietExit` that is not the first step, a template
  inside quotes, or `{{run.diff}}`;
- a `dispatch` missing `repo`, `title`, `envelope`, or the right one of
  `spec`/`prompt`; a templated or path-like `repo`;
- a `pr:` with an unknown act, `draft` without `create`, or inside a
  `parallel` or handler;
- a second `watch:`, one not at the top level (a `repeat`'s body at any
  depth included), an unknown `until`, `onClosed` or event, a `trust`
  that does not parse or holds an unterminated `{{`, a handler with no
  form or more than one, or a `goto` that names no earlier top-level
  step, or a handler recipe whose step id repeats one of the recipe's own.

It is warned about, but may run, when a `parallel` holds more than one
writing role, a step names a `slot` beside an `agent` or `model` (the
slot is ignored), or a recipe or step still names `tier` or `envelope`.

## Common mistakes

- **A check that already passes.** It proves nothing about the run.
  Every done-when check should fail before the run, unless it is a
  guard that must keep passing.
- **`go test -run Name` with no matching test passes.** Grep the
  output for the test's own `--- PASS:` line.
- **A prompt without `{{spec}}`.** The step does not know the goal.
- **A reviewer that cannot see the work.** Give it `{{run.diff}}`.
- **Two writers in a `parallel`.** They share one tree; put them in
  sequence.
- **A `judge: true` item and no judging last step.** It stays unjudged.
- **A verdict loop whose step is not told the JSON.** Spell out the
  exact shape and the artifact path, or the loop cannot judge itself.
- **A `{{template}}` inside quotes in a `run:` command.** Write it as a
  word of its own; gummi quotes it.
