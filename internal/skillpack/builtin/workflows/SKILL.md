---
name: workflows
description: "Use at the start of a request for a feature across packages, a risky refactor, a written investigation or failing checks to fix; when the person sends /workflow or triage names a recipe; before writing done-when checks; and to propose, steer, stop or read a gummi workflow run."
scope: general
source: gummi
---

# Using gummi workflows well

A workflow run is a recipe of fresh-context steps working in this
session's own tree against one of its specs. It costs more than doing the
work yourself, and it buys fresh eyes: a design, independent reviewers,
checks run at the end. Use it when that is worth paying for.

## Decide before your first edit

Decide at the start of each new piece of work whether to do it here or
to propose a run. A run proposed once you have done most of the work
buys nothing, so this is a question for the first turn, not for later.

Propose a run when:

- the person asks for one — in words, or by sending `/workflow`
  (from the composer or a spec's **Propose a run**);
- gummi's triage named a recipe ahead of the request — follow it unless
  the request or what you find says otherwise, and say why not;
- it is a feature or change across several packages, a risky refactor,
  or work with tests and docs to write (`deliver`);
- the person wants a question answered in a written report
  (`investigate`);
- checks already exist and fail, and the fix needs no design
  (`fix-until-green`).

Do it here for a question, a small or local change, a fix whose cause
you know, or a change the person is still shaping with you. When you
propose, say in one sentence why a run beats doing it here, and which
recipe.

## Write the spec first

A run is judged against the spec revision it freezes when you propose
it: finish the spec first, since an edit after the proposal reaches the
run only as a note. Before `workflow_run`:

1. `spec_start`, then fill Problem, Approach, Done when and Limits with
   `spec_replace_section`. Steps read only the spec, the artifacts and
   earlier steps' outputs — never this conversation. What you know and
   the spec does not say, they do not know.
2. Run every check yourself, now. **Each must fail** before the run: a
   check that already passes proves nothing about the run's work. The
   only exception is a guard that must keep passing; say so in its
   `says`.
3. gummi runs the checks again as the run's baseline, before its first
   step, and tells you an item that already passes, ran no tests or
   cannot run (a command not found). Fix that item's check and propose
   again, or say why it is a guard. When the tree may lack what the
   checks need, give the recipe a `preflight:` command that checks for
   it: a failing one refuses the proposal before anything is spent.

## Done-when checks that can fail

```gummi-done-when
- id: DW-1
  says: "a session's spec list shows each spec's state"
  check: "go test ./internal/engine/ -run '^TestSpecListShowsState$' -count=1 -v 2>&1 | grep -q -- '--- PASS: TestSpecListShowsState'"
- id: DW-2
  says: "the list reads clearly at 80 columns"
  judge: true
```

- **The exit status is the verdict.** `sh -c` runs the check in the
  tree, at most two minutes each.
- **`go test -run` passes when nothing matches.** A misspelled or
  not-yet-written test passes vacuously. Grep for the test's own
  `--- PASS:` line, as DW-1 does. gummi marks a `go test` that ran no
  tests at all as unjudged, but a pattern that matched one test of two
  still passes; the grep catches both.
- **One behaviour per item**, named by a test that exercises it. Do not
  grep source code for a symbol: it passes on a stub.
- **Cover the whole bug, and what must keep working.** An item for every
  place the bug lives — a sibling function, another case of the same
  type — not only the one reported, and a guard that runs the whole test
  suite of each package the fix touches: a fix loop goes green on the
  items it is given, however narrow.
- **gummi's environment, not your shell's.** The checks run with the
  environment gummi was started in. Name a tool by its absolute path if
  that `PATH` may not have it.
- **No network, nothing flaky.** A check that fails one time in twenty
  sends a loop round again for nothing.
- **`judge: true`** for what no command can decide: wording, layout, a
  design choice. The run's last step judges it, with `MET: DW-n` or
  `UNMET: DW-n <why>` lines; a recipe that does not end in such a step
  leaves the item unjudged, and the spec stays partial.
- Never weaken a check to make it pass. If a check was wrong, change it
  in the spec (a new revision), and tell the person.

## Pick the recipe

`workflow_recipes` lists them; `workflow_recipes` with a `name` shows
one's YAML.

| recipe | for |
|---|---|
| `deliver` | a feature or change: design, then implement and two reviewers until a verdict says APPROVED (at most 3 rounds), then validate every item |
| `fix-until-green` | checks that fail and a fix that needs no design: one implementer, round until the checks pass (at most 5) |
| `ship` | build, open a draft PR, then wait on it with no model and fix its red checks and trusted reviews until a person merges (the session must be allowed to publish; `trust` input) |
| `merge-base` | a handler recipe: merge a PR's moved base into the branch |
| `investigate` | a question, no change: a scout maps, a scribe writes `report.md` |

If none fits — no design needed, a tester before the implementer, a
different reviewer — write one inline as `recipe_yaml`, or keep it
in a file in your tree and pass `recipe_path` (each proposal re-reads
it, so tune it between runs without pasting it again); the
`workflow-recipes` skill has the grammar. Adapt a bundled one rather
than starting blank.

## While the run is open

- **Do not change files in the tree.** The steps share it. Keep talking
  with the person; read freely.
- `workflow_status` shows the steps, what was spent and open questions.
- `workflow_steer` sends a note every running step gets as a turn and
  every later step reads in its prompt: a constraint, a fact you found,
  a narrowed scope. It is not a restart.
- `workflow_stop` when the run is going the wrong way or the person
  asks. Stopping reverts nothing; the person can Revert run.
- A step's `ask_user` goes to the person, not to you. So does the run's
  own question (a loop at its max, the budget, a run parked when gummi
  stopped): when the person talks it over with you, do not answer it for
  them — only they do.
- **Money:** there is no estimate. The run spends from the session's
  budget and, when that is spent, pauses until the person tops it up.
  Do not promise what a run will cost.

## When the report arrives

The report comes as a turn. Then:

1. Read it: each item's result, the steps that failed, `DECISION:`
   lines (choices a step made that nobody asked it to), `OPEN:` lines,
   and any reading step flagged for changing files.
2. Read the tree yourself: `git status`, `git diff`. Run the checks.
3. Finish small leftovers yourself. For a partial run on real gaps,
   propose a run again on the same spec — after revising it if the gap
   was in the spec.
4. Commit what you keep, with a message about the change, not the run.
   gummi never commits. Leave out what you do not keep, and say so.
5. Tell the person what it means: met or partial, the decisions made
   for them, what is still open.
6. If you wrote the recipe inline and it worked, suggest the person
   save it: the run's block has **Save as recipe…**.
7. A recipe written inline is checked before it is proposed: a refusal
   names the problem (an id repeated across the recipe and its handler
   recipes, a `watch` that is not top level, an unterminated `{{` in
   `trust`, an unknown field in a handler step) — fix it and propose again.
