---
name: workflow-recipes
description: "Use when no bundled gummi workflow recipe fits and you write one inline as workflow_run's recipe_yaml, or when a planner step revises a run's plan with a gummi-workflow block: the YAML grammar, roles, templates, loops, joins and worked examples."
scope: general
source: gummi
---

# Writing a gummi workflow recipe

Start from a bundled recipe, not a blank page: `references/` holds
`deliver.yaml`, `fix-until-green.yaml` and `investigate.yaml` as gummi
ships them, and `workflow_recipes` with a `name` shows any recipe,
the workspace's own included. Pass yours to `workflow_run` as
`recipe_yaml`. A recipe that cannot run comes back as problems, with nothing
proposed: fix them and call again.

## The shape

```yaml
name: test-first
description: Write the failing tests, then build until they pass
inputs: {}  # name: hint (required), or {hint: h, default: d} / {hint: h, required: false}
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
            Make the tests from the step below pass without changing them.

            {{tests.output}}

            {{spec}}
  - id: validate
    role: reviewer
    prompt: |
      Read {{run.diff}} against the spec. End with one line per
      done-when item: MET: DW-n or UNMET: DW-n <why>.

      {{spec}}
```

`steps` run in order. Each entry is exactly one of:

- **a step**: `id`, `role`, `prompt`; optional `slot`, `agent`/`model`
  (pin, beats `slot`; opencode: `provider/model`), `artifacts`
  (name → plain file name in the run's dir) and `captureOutput`
  (false hides its message from later steps).
- **`parallel`**: `id`, `join`, and `steps` of plain steps side by side
  in the same tree.
- **`repeat`**: `id`, `max`, one `until`, `onMax`, and `steps` (which may
  hold a `parallel`).

## Roles

| role | does | reads or writes |
|---|---|---|
| `scout` | reads, reproduces, maps | reads |
| `planner` | plans what follows; may rewrite the remaining steps | writes |
| `contractor` | design, interfaces, stubs | writes |
| `implementer` | builds | writes |
| `tester` | writes and extends tests | writes |
| `scribe` | docs and reports | writes |
| `reviewer` | reads the work against the spec | reads |

Every step has a full shell; a reader is told to change nothing, and
one that did is flagged. gummi gives each step its role's preamble: do
not repeat it. A step's model is the profile's choice per role;
`slot: <name>` picks another profile slot (a second reviewer on
`reviewer-alt`). Never use a slot for a bigger or cheaper model.

## Templates

| template | is |
|---|---|
| `{{spec}}` | the frozen spec's text; put it in every prompt needing the goal |
| `{{inputs.<name>}}` | an input, wrapped as data |
| `{{artifacts.<name>}}` | an artifact's absolute path in the run's dir |
| `{{previous.output}}` | the step before's final message |
| `{{<id>.output}}` | an earlier step's; in a `repeat`, its latest round |
| `{{run.diff}}` | a file: the diff from the run's start to now |

A step sees only its filled prompt: name every output and artifact it
needs.

## Lines a step's output may carry

- `FAILED: <why>` as its last line fails the step; outside a
  `parallel`, that ends the run partial.
- `DECISION: …` and `OPEN: …` go into the run's report.
- `MET: DW-n` / `UNMET: DW-n <why>`, in the **run's last step**, judge
  the items without a check. End with a reviewer that writes them, or
  every `judge: true` item stays unjudged.
- A `gummi-done-when` block adds checks to the run.

## Loops: `until` and `onMax`

| `until` | ends the loop when |
|---|---|
| `checks` | every done-when check passes in the tree |
| `{verdict: APPROVED, in: review}` | the artifact `review`'s JSON `verdict` is the word; a step in the loop must declare it |
| `{contains: LGTM}` | the loop's last step's output contains the text |

`onMax`: `pause` (the default) asks the person; `continue` goes on;
`abort` ends the run partial. A loop gummi cannot judge (no verdict
artifact, a check ran no tests) always asks.

Give a verdict step an artifact and the exact JSON to write: a missing
file is captured from the output's last JSON object, but only if there
is one. Tell the implementer to read the last review first:
`If {{artifacts.review}} exists, address its findings first.`

## Joins: `parallel`

| `join` | the block |
|---|---|
| `all` | fails when one branch fails, stopping the rest |
| `allSettled` | waits for all; fails only if all fail |
| `any` | settles on the first success, stopping the rest |

Put readers side by side. Two writers in one tree overwrite each other:
gummi warns; make them steps in order instead.

## Other shapes

A light review loop: a `repeat` of an implementer and one reviewer with
`until: { contains: "VERDICT: PASS" }`, then a validating reviewer.
`contains` matches anywhere in the output: pick a marker written
only when meant, and ask for it on the last line.

A planner who decides the steps: a `planner` step that ends its output
with a fenced `gummi-workflow` block (a YAML list of entries, as
under `steps:`) replaces the run's remaining steps, once they pass
the same checks and the person allows the revised plan.

## Command and dispatch steps

A step may be gummi's own act, not a model: `run:` a command or
`dispatch:` a session (an agent's only). Neither has a role, slot,
agent, model or prompt.

```yaml
name: watch-foo
steps:
  - id: check
    run: ./check-release.sh
    quietExit: 1
  - id: bump
    dispatch:
      repo: myrepo
      title: "Bump foo to {{check.output}}"
      envelope: 5
      recipe: fix-until-green
      spec: "Upstream foo released {{check.output}}. Bump it."
```

- `run` goes through `sh -c` in the run's tree. Exit 0 passes stdout on
  as `{{check.output}}`; `quietExit` (1–255, first step only) ends the
  run with no trace; any other exit fails it. A `{{…}}` is one quoted
  word: never put it in quotes.
- `dispatch`: `repo` is a literal, `envelope` dollars out of the
  agent's own. With a `recipe` give a `spec`, else a `prompt`.

## Open the PR and wait on it

Needs the session's "agent may publish". `pr: {act: push|create|update|ready,
draft: true}` is one publish act (never forced). `watch:` is at most one,
top level (never in a repeat), and waits on the PR with no model, then
runs a handler per event, one at a time; gummi pushes after each pass.

```yaml
name: ship-it
inputs:
  trust: {hint: "who may steer: @login, @org/team, role:maintain", default: "role:maintain"}
steps:
  - id: build
    role: implementer
    prompt: "Build and commit.\n\n{{spec}}\n{{watch.event}}"
  - id: open
    pr: {act: create, draft: true}
  - id: shepherd
    watch:
      until: merged
      trust: "{{inputs.trust}}"
      max: 6
      quiet: 7d
      onClosed: abort
      on:
        checks-failed: {recipe: fix-until-green}
        changes-requested: {goto: build}
        comment:
          - id: answer
            role: implementer
            prompt: "Address this and commit.\n\n{{event}}"
        base-moved: {recipe: merge-base}
```

- `until`: `merged` or `green-approved`. Events: `base-moved`,
  `checks-failed`, `changes-requested`, `comment`. A handler is steps,
  `recipe: name` (no watch or pr inside) or `goto: id` (an earlier
  top-level step: the steps from it to the watch run again); it may add
  `max`. `checks: [ci]` limits whose logs are read.
- `trust` is `@login`, `@org/team`, `role:write|maintain|admin`,
  comma-separated; empty is the publishing login. Others' feedback is
  counted, never read. An unterminated `{{` is refused.
- `{{event}}` (handler steps, and `{{inputs.event}}` of a handler recipe
  that declares `event`) is the event's facts as data; `{{watch.event}}`
  is empty until a `goto` pass. Bundled `ship` is this recipe.

## Before you propose

- Every id is unique across the recipe, nested and handler recipes
  included, of letters, digits, `-`, `_` — never digits alone.
- Every `{{…}}` names an input, an artifact some step declares, or an
  earlier step.
- Each loop has a `max` and one `until`; an `until` verdict's artifact is
  declared inside the loop.
- The last step judges the `judge: true` items.
- If it worked, offer **Save as recipe…** on the run's block.
