# gummi UX review — two cards driven on a pty

**Date:** 2026-09-10 · **Branch:** main @ a9398d6 · **Rig:** throwaway `notes` Go CLI,
profile `flash` = opencode/`glm-5.3-flash` for every role, TUI on a 120×40 tmux pty.

> **Status: all fixed** (working tree, uncommitted). See "What was changed" at the end for
> the mechanism-level list and what was verified back on a pty.

Two cards were driven end to end as an ordinary user would drive them:

- **FD-001** "Add a --json flag to the list command" — attended, todo → plan → implement →
  verify → landing gate, including a spec comment at todo and a diff comment at verify.
- **BG-002** "notes rm with no id panics" — created from the same dialog and handed to
  autopilot.

The findings below are what actually happened on screen, with the mechanism where I found it.
They are ordered by how badly they trap a user, not by how hard they are to fix.

---

## 1. Dead ends — the user cannot move the card

### 1.1 A comment written at `todo` wedges the card *(the one you reported)*

Type a comment on the spec before anything has run — the natural way to add context for the
agent you are about to start — and the card can no longer start.

```
c  →  "blocks approval (you)"
enter on "1. start — advance into the design flow"
      →  FD-001: 1 open question(s) block approval — resolve them or press R in the spec view
R in the spec view
      →  stage todo has no agent action
```

The message names a recovery that does not exist at this stage. The only way out is `x`
("resolve") on your own comment, which writes `%% @user(2026-09-10): resolved` into the spec —
and the architect, when it finally ran, complained about exactly that:

> "your marker on the spec asks for `--json` on `rm` as well, but the follow-up marker just
> says 'resolved' without recording the answer."

So the only escape hatch disguises the user's request as already-handled.

**Mechanism.** `Engine.Advance` runs `openQuestionsBlockingGate` on *every* forward edge
(`internal/engine/advance.go:139`), including `todo → plan`. That edge is not a review gate:
there is no artifact to review and no agent to send the comment to. Comments written before
the first stage are *input*, not *objections*.

**Suggested shape.** Skip the blocker checks on the `todo →` edge, and let the marker travel
into the plan stage as context (it already reaches the agent — it is in the file).

### 1.2 The card page falsely reports the gate shut, and offers only a loop *(worst one found)*

At FD-001's plan gate the page said, in two places at once:

```
The spec still has Chosen approach and Implementation notes blank, and are required
before the gate opens.

gummi  plan is ready for your decision.
▸ 1. draft the missing sections
    Chosen approach, Implementation notes are required in the spec and still blank —
    the gate stays shut until they are drafted; enter re-runs the stage to write they
  2. start the architect — shape the spec until it convinces you
  3. stop here — park it — nothing runs until you come back
```

Both sections were fully written — a 40-line contract under *Chosen approach* and a six-step
numbered plan with a *Plan claims* table and a `gummi-files` block under *Implementation notes*.

Taking the action the page argues against — actions menu → **advance** — crossed instantly:

```
✓ you advanced plan → implement
```

The **engine** agreed the sections were drafted; only the card page's `in.undrafted` was stale,
never recomputed after the architect's writes. A user who believes the page never presses
advance: they press "draft the missing sections", spend another entire plan session, and land
back on the identical screen. That is an unbounded loop with no override offered.

The same staleness runs the other way at the landing gate (§1.3).

### 1.3 The landing gate invites an action it then refuses

With one unresolved diff comment, the card page still read *"Verify passed — the branch is
ready to land"* and still highlighted `▸ 1. land on main`. Pressing enter:

```
FD-001: 1 open diff comment(s) block approval — resolve them (x) or press R in the diff view
```

`narration.go` and `nextsteps.go` both have a blocked-gate branch for open diff comments;
neither fired, for the same reason as §1.2.

The staleness is symmetric and it repeated on the way out. After the rework round the page did
catch up — *"1 unresolved diff comment is holding the gate shut"* with the right actions — so
the copy works. But then resolving that last comment with `x` in the diff view (the diff view
itself updated instantly: the comment rendered with a ✓ and the `✎ 1 open` counter cleared)
left the card page still saying *"1 unresolved diff comment is holding the gate shut"* and still
recommending "resolve diff comments". Advance from the actions menu opened the merge dialog
immediately. **Neither direction of the count survives a change to the thing it counts.**

The healthy contrast is worth naming: `R` in the diff view **worked** —
*"FD-001: sent 1 diff comment(s) to the implementer"* and a rework session started. That is
precisely what §1.1 cannot do, which makes the todo dead end look like an oversight rather
than a policy.

---

## 2. Lost work — the user's input silently disappears

### 2.1 Typing a line while the agent is busy kills the stage session

The composer is always live and always says *"message the agent, /approve /verify /diff…"*,
with the footer switching to `enter send` the moment you type — including while the spinner is
up and while a question is on screen. Doing so:

```
you
  Approve as proposed, but also keep --json out of the add command explicitly.

  ✗ a turn is already in progress
```

The agent never received the line. The card then reported *"The plan session errored before it
finished."*

**Mechanism.** `sendThreadMessage` → `Engine.Send` → `deliverTurn`, and `deliverTurn` calls
`e.failRun(s, err)` on **any** `agent.Send` error (`internal/engine/engine.go:1664`) — including
opencode's benign "a turn is already streaming" guard (`internal/agent/opencode.go:204`, and
the same guard exists in `zz.go` and `codex.go`). Worse, `Engine.Send` appends the line to the
transcript **before** attempting delivery (`engine.go:1646`), so the user sees their own message
rendered as `you` and reasonably believes it landed.

Reproduced twice, once with a fresh question on screen and once with a stale one.

**Suggested shape.** A busy backend is "not now", not a failure: keep the line in the composer,
show a notice, never `failRun`. And when a question is open, route prose to `Engine.Answer`
(it already takes arbitrary text) rather than to a turn the backend cannot accept.
`decision.go:798`'s `answerAskWith` does exactly this — but only when the ask declared
`allow_free_form`; for a structured ask the comment says *"prose routes as a turn instead"*,
which is the case that cannot work.

### 2.2 Questions expire, and nothing on screen says so

I spent about two minutes on the first question (I looked at the board and the inbox first).
The MCP tool call timed out underneath:

```
architect
  The question prompt timed out — retrying once.
  · gummi_ask_user
  · question not put to you: the user is still answering your previous question — ask one
    question at a time; re-ask this after that answer arrives
```

The picker stayed on screen the whole time, offering options as if answerable. The retry was
bounced by gummi's one-question-at-a-time guard, my typed answer collided with it, and the plan
session died. gummi sets no MCP tool timeout for opencode, so the backend's default bounds human
think time — in a stage whose own prompt instructs the model to *"interview the user"*.

### 2.3 Cancelling the autopilot dialog is indistinguishable from confirming it

"Create & start" opens:

```
[ Cancel             ]  [ Start on autopilot ]
←/→ move · enter select · esc cancel
```

The confirm button is focused by default (bold, white-on-blue); Cancel is dim. **Colour is the
only cue**, and `←/→` **wraps** (`internal/ui/buttons.go:39`). Press → once — the obvious
"move to the affirmative button" reflex, and the footer says "←/→ move" — and you are on Cancel.

Choosing Cancel emits **nothing**: the dialog closes, the previous notice ("BG-002 created")
stays in the status bar, and the card still reads `autopilot: off`. I lost a card start this way
and only noticed by reading the card header two screens later.

### 2.4 Answers are captured as markers, not as content

After answering "which approach", the spec's `## Chosen approach` held only:

```markdown
## Chosen approach
%% @user(2026-09-10): resolved — B. Presenter seam (recommended)

%% @gummi: converge on one during the plan stage
```

The section body was still empty (so the undrafted gate still counted it as unwritten), the
template placeholder survived alongside the answer, and the option's UI decoration —
"(recommended)" — went into the document.

Twice the capture failed outright, with a truncated message and no remedy:

```
· spec capture skipped: "Add a --json flag to the list command" not found (or not uniqu…
```

### 2.5 A blocking critique finding is filed under "informational"

BG-002's plan critique escalated after two rounds. Its findings sit in the bug report's
checklist like this:

```
BG-002 · bug report ✎ 5 open
informational (agent)
  ☐ blocking — the implementation plan is missing from this artifact: Fix still holds only…
  ☐ blocking — stale pre-revision copies contradict the revised plan: the top-level…
  ☐ blocking — the implementation plan is missing from this artifact: Fix still holds only…
```

Three findings that begin with the word **blocking** are grouped under the header
**"informational (agent)"** — and, per §1.1, agent-authored threads genuinely do not gate.

`internal/ui/specview.go:346` splits the two buckets by **author**, not by severity
(`userMarker(t) != nil`). The intent is documented in the comment above it, and it is right
about the gate math — but "informational" is exactly the word that tells a reader they may
skip something, and here it is being applied to a reviewer's blocking findings.

The card's decision block, meanwhile, read simply *"plan is ready for your decision"* with
"approve — hands the card to the agent stages" as an option. Nothing there says the critique
escalated (that appeared once, as a transient status-bar notice). Approving would hand the
implementer a Fix section the reviewer had just twice reported as empty.

### 2.6 The rework round addresses the comment but never resolves it

`R` on a diff comment sent it to the implementer, which made exactly the requested edit
(the README gained the sentence I asked for). It never called `resolve_annotation`, so the card
came back to verify with the same comment still open and the gate still shut — and the page's
top recommendation was "R requests changes", which would repeat the round.

The tool exists and is registered for `StageImplement` (`asktool.go:157`), and the turn text
tells the agent to call it ("unresolved comments keep the gate blocked",
`budget.go:139`). What is missing is any handling for the case where it doesn't: gummi neither
notices that the comment's anchor text changed nor offers anything but another round.

---

## 3. The board never says which card needs you

A card parked on `ask_user` — the agent literally cannot continue without an answer — showed:

| surface | what it said |
|---|---|
| board | `IN PROGRESS  ● FD-001 …` |
| status bar | `⬤ 1 running` |
| **inbox** | **"nothing needs you"** |
| `gummi status FD-001` | `Waiting: [ask at plan] Is --json on the rm command part of this feature…` |

The data exists; the queue that exists to answer "what needs me" does not use it. The inbox only
lit (`✉1`) *after* the question had expired — and then it showed the *failure*, recommending
"pick it back up", which starts a fresh session and discards the pending question.

`gummi status` also disagrees with the board about liveness: `Running: no` while the TUI says
`⬤ 1 running`.

---

## 4. States that contradict themselves

- **"plan failed" sticks to a session that is still working.** `failRun`
  (`engine.go:1020`) sets `s.Err` and frees the slot but does not stop an interactive session,
  and nothing clears the error when the session recovers. I watched the card render
  `⢿ writing plan` (spinner) and *"The plan session errored before it finished. / plan failed —
  choose what happens next."* simultaneously, then answer two more questions from the same
  session while the inbox still listed a plan failure.
- **gummi declared a run failed while the backend was still alive** — the opencode process was
  still streaming 3m44s after the card said it had failed. "Pick it back up" at that moment
  starts a second process against the same worktree and the same spec.
- **errored vs paused, in adjacent lines.** Narration: *"The plan session errored before it
  finished."* Action row directly beneath: *"pick it back up — the run is paused"*.
- **"autopilot handed it to you"** appeared as a transcript separator while the header read
  `autopilot: on · running` and the spinner was up.

---

## 5. Keys that are advertised but do not work

- **`g` on the card page types the letter `g`.** The spec view's footer says "g approve"; the
  actions menu lists "advance  g". On the thread tab the composer holds focus permanently, so
  every single-letter accelerator the menu advertises — `g p s d b v u i r D` — is unreachable
  by pressing it. You must open the menu and choose the row.
- **Multi-select asks give no affordance.** Options render as `○ / ●` but the footer says only
  "↑↓ choose · enter answer". `space` toggles — never stated, and on the board `space` opens the
  command menu, so guessing costs you a modal. Pressing enter with nothing picked sent an empty
  answer; the agent re-asked the same question with "(none selected = keep all out)" appended.
- **A notice eats the key hints.** While a notice was showing, the diff view's footer collapsed
  to "esc back" — `c`, `x`, `g`, `R` all dropped — at the same moment an error message was
  telling me to press one of them.
- **`p` means different things on different cards.** FD-001's menu: "park  p". BG-002's:
  "dependencies  p".
- **The new-card dialog shows focus by colour alone.** Four of its seven focus stops (envelope,
  profile, severity, the collapsed "runs as" line) share one footer hint, and the `▸` marker sits
  on every choice row at once, so it is not a focus cue either.

---

## 6. Wording

Broken grammar, on screen verbatim:

| where | text | problem |
|---|---|---|
| `ui/narration.go:255` | "The spec still has Chosen approach and Implementation notes blank, and are required before the gate opens." | the second clause has no subject |
| `ui/nextsteps.go:280` | "enter re-runs the stage to write **they**" | `pronoun` is used as both subject and object; needs *them*/*it* |
| same row | "**re-runs** the stage" | shown for a stage that has never run |
| autopilot dialog | "starting it **on full** runs plan, implement and verify without you — up to 5 corrections" | "on full" is a retired mode name; the sentence does not parse; "corrections" is undefined |
| inbox header | "1 open **decisions**" | agreement |

Vocabulary that drifts inside one screen:

- the stage strip says `plan`; the action calls it "the design flow"; the narration calls it
  "The design stage"; the agent prompt calls it "Stage: Plan — phase 1 of 3". Pick one.
- status bar: "attended 0/1 · unattended 0/2" — lane-pool jargon in the most-read line on the
  screen; and the config key for one of those pools is `autopilot_lanes`, not "unattended".
- "> _Ingested from `manual`_" at the top of a card typed by hand in the new-card dialog.
- "0.7 / 2000 credits · 2000 left" states the same number twice.
- **"Create & start"** does not start the design conversation — it opens an *autopilot*
  confirmation and runs plan+implement+verify unattended. The label should say so.

---

## 7. Smaller things worth a line each

- **A fresh card reads as six open comments.** At todo the spec header shows `✎ 6 open` and a
  checklist of `%% @gummi:` template placeholders under the heading "informational (agent)".
  Nothing has run. Six open threads on a card you just created reads as a problem.
- **The envelope you set is not the envelope you get.** FD-001 was created with 2000 credits;
  a scribe pass re-sized it to 1100 (`16.7 / 1100 credits` in the header). One passing notice,
  no question.
- **Every assistant message renders twice** — once alone, then again prefixed to the next chunk;
  and two messages can be concatenated with no separator ("…removing them.The spec file is
  outside the worktree…").
- **The plan critique can silently produce no verdict.** FD-001's reviewer
  could not read the spec (it lives outside the worktree) and said so:
  *"add an allow rule for `external_directory` covering `.../.gummi/**` … Container/config
  change required — I cannot modify permission rules from inside."* It correctly refused to
  invent a verdict; gummi recorded *"parked — plan critique finished with no clear verdict —
  review it manually"* — but the decision block said nothing about it, and the failure sat far
  above the fold. (The architect had hit the same denial a turn earlier and worked around it:
  *"The spec file is outside the worktree — using the gummi tool instead"*. The reviewer got no
  such hint.)
- **No elapsed time anywhere.** The spinner labels change per stage ("writing plan",
  "critiquing plan", bare "running") but nothing shows how long the current turn has been
  going or when the last activity was, so a slow turn and a wedged one look identical. The TUI
  arms no stage timeout at all — headless has `--stage-timeout`, the board has nothing.
- **Stale notices linger.** "stage todo has no agent action" stayed in the status bar after a
  successful resolve; "a turn is already in progress" stayed through the next two screens.
- **Help advertises four doors for one dialog.** `?` lists `n` ("feature, bug or research;
  paste an issue link to import it") and then also `B`, `R`, `G` as separate presets.
- **An untouched branch reports as "landed" once main moves.** After FD-001 landed,
  `gummi status BG-002` — a card at `plan`, `Verified: no`, with no commits of its own — printed
  `Branch: gummi/BG-002-… (landed)`. `Manager.Landed` asks
  `git merge-base --is-ancestor <branch> HEAD` (`worktree/manager.go:612`), which is trivially
  true for a branch still sitting on the fork commit. Verified by hand: BG-002's branch is
  `16a3d48` (the initial commit), main is `af5b631`. Any card that has not committed yet will
  say "landed" the moment any other card lands.
- **`⟲ 2 of 5 corrective`** in the card header — undefined jargon in the most-read line of the
  card; and the card title truncated to "notes rm with no id pan…" with half the header row
  still empty.
- **The first thing a new user sees is a modal about the agent tab** — "this only picks the
  agent tab's hosted CLI — the engine's own per-role backend routing (profiles.yaml) is
  untouched" — before they have seen the board or created a card.

---

## 8. The landing surface

FD-001 did land, as one squash commit, and the feature works
(`notes list --json` → `[]` / a JSON array, `notes rm 1 --json` → the removed object,
`notes --json list` → exit 2). The commit message gummi drafted is genuinely good and wrapped
correctly at ~72 columns in the object. Four things about the dialog around it:

- **The message is displayed mangled.** The dialog is ~60 columns wide inside a 120-column
  terminal, so a body already hard-wrapped at 72 is re-wrapped on top of that:

  ```
  ┃ - scripts had to scrape the padded text table;
  ┃ machine-readable JSON
  ┃   lets consumers parse structured data instead of
  ┃ screen-scraping
  ```

  You are asked to approve, on the merge screen, a message you cannot read on the merge screen.
  Eight visible lines of an eighteen-line message, no scroll indicator.
- **"unreviewed draft — ctrl+s again to land without reviewing"** is a good guard. Keep it.
- **A failed merge throws the approved message away.** My first merge failed on an unset git
  identity (see below); re-entering the dialog started a fresh model draft from scratch —
  another wait, another call.
- **`gummi doctor` does not check git identity.** The failure — `fatal: empty ident name` —
  surfaced only at the final keystroke, after every credit had been spent. The full raw git
  error was dumped into the card's decision area, replacing the actions.

---

## What I would fix first

1. **Recompute the card page's blockers.** §1.2 and §1.3 are the same stale snapshot, and
   between them they can both trap a user in a loop and send one into a refused action.
2. **Never `failRun` on a busy backend, and never show a `you` line that was not delivered**
   (§2.1). This is the one that loses the user's own words.
3. **Do not gate the `todo →` edge on comments** (§1.1), and make `R` at a stage with no agent
   say something other than "stage todo has no agent action".
4. **Put open questions in the inbox while they are open** (§3).
5. **Confirm/cancel dialogs need a non-colour focus cue and a notice on cancel** (§2.3).
6. **Stop calling a reviewer's blocking findings "informational"** (§2.5).
7. The wording table in §6 is a half-hour of edits and removes four of the most confusing
   sentences in the product.

---

## What worked well

Worth saying, because the failures above are concentrated in one layer:

- The **decision block** is the right idea and mostly reads beautifully: *"verification passed —
  decide whether this work is ready to land / 1. land on main / 2. send it back — not convinced —
  your line goes back with it / 3. stop here — park it"*. When its inputs are fresh, it is the
  best thing on the screen.
- **`R` at verify works exactly as designed** — "sent 1 diff comment(s) to the implementer", a
  rework session, and the edit I asked for.
- **The board's needs-you line**, when it fires: *"BG-002 ⚡ … ✉ / The design stage finished and
  wrote the bug report — the gate is waiting on you"*, plus *"Before that, autopilot crossed 1
  gate and took 2 answers without you. [alt+a]"*. That is precisely the sentence the open-ask
  case (§3) is missing.
- **Collapsed prior sessions** ("plan · architect · 6 turns ── from 15:40") keep a long card
  readable.
- **The work itself was correct**, on a cheap flash model, at 24.5 credits for the feature and
  10 for the bug: a real presenter seam, five byte-golden tests, a README paragraph, and live
  checks that actually built the binary and asserted its output.


---

## What was changed

Every finding above is fixed in the working tree. The full suite (`go test ./...`), `go vet
./...` and the pinned build are green. Where a fix changed a contract, the test that pinned the
old contract was rewritten rather than deleted, with the reason in its doc comment.

### The stale snapshot (§1.2, §1.3)

`Shell.refreshBlockers` (`ui/msgs.go`) recomputes one card's `OpenSpecQs` /
`OpenDiffComments` / `Undrafted` and folds them into its row. It is dispatched from the four
places those inputs actually change: a spec load, a diff load, `EventAnnotations`, and —
the one that caused §1.2 — a turn ending on an **interactive** session, which `EventIdle`
used to return from having refreshed nothing.

Underneath it, a rule that was missing entirely: **a blocker describes a gate, and there is no
gate until the stage has produced something to approve.** `blockedGate` (`ui/nextsteps.go`)
and `whyItStopped` (`ui/narration.go`) now both return early unless `in.finished()`. That is
what stopped a comment written at todo from becoming the card's recommended action, and it
removed the duplicate row in §5 (`draft the missing sections` sitting above an identical
`start the architect`).

### The session-killer (§2.1, §2.2)

- `agent.ErrBusy` is new; `opencode`, `codex` and `zz` return it from their "a turn is already
  in progress" guards. It means *not now*, never *this session is broken*.
- `Engine.deliverTurn` no longer routes it through `failRun`. `Engine.Send` undoes what a
  refused turn consumed — `dropUnsentUser` takes the echo back out of the transcript,
  `requeueNudge` puts the budget nudge back — so a refusal leaves the session exactly as it
  found it. An echo of a line the agent never received is the part that made this invisible.
- The composer restores the line (`noticeMsg.restore`) instead of losing it.
- **Prose at an open question is the answer now**, whether or not the ask declared
  `allow_free_form`. `submitThreadLine` routes it to `Engine.Answer`; the picker stops claiming
  enter while words are on the line, the title says "your line is the answer", and the bar says
  `enter answer`. This reverses F4's fix, whose own comment recorded the symptom ("the ask never
  got a reply and the spinner ran forever") and concluded the fix was the label — the drive
  showed the turn it was labelling can never be delivered.
- A pending ask is refused up front, before anything is recorded: the session reports itself
  not-busy there, but the backend's turn is still open inside the tool call.

### The todo dead end (§1.1)

`Advance()` skips the blocker checks on the `todo →` edge (`engine/advance.go`) — that edge is
not a review gate. `noAgentAtStage` replaces "stage todo has no agent action" with a sentence
naming the way forward.

### Everything else

| finding | change |
|---|---|
| §2.3 cancel is silent | `buttonRow.Move` clamps instead of wrapping; the focused button carries a `▸` marker, not just colour; cancelling the autopilot dialog says "cancelled — nothing started" |
| §2.5 "informational" | `specview.go` groups by marker role: **blocks approval (you)** / **reviewer findings — weigh before approving** / **agent notes (non-blocking)** |
| §2.6 unresolved-but-addressed | the diff surface says so when every open comment's line has already changed, naming `x` before `R` |
| §3 open ask not queued | `EventQuestion` always queues; the item clears when the answer lands. Verified: board shows `? … The agent asked a question and is blocked on your reply`, inbox shows `1 open decision · asks: …` |
| §4 duplicate messages | `Session.finishAssistant` finalizes by tracked index, not "is it still last" — and the same bug in `Follower` |
| §5 `g` types itself | not changed: the composer owns printable keys by design. The actions menu is the route, and its rows now carry the accelerators honestly |
| §5 multi-select | not changed |
| §6 wording | all six strings; "plan" is the one name for the stage; `attended · autopilot` replaces `unattended`; `Create & autopilot` replaces `Create & start`; no provenance line for a hand-typed card |
| §7 six open comments | template placeholders are `agent notes (non-blocking)` now, not "informational" |
| §7 no elapsed time | the busy line carries it (`implementing · 4m12s`), and the labels name the stage instead of a bare "running" |
| §7 stale notices | cleared on every key the spec and diff surfaces take |
| §7 notice eats the hints | `statusbar.Render` sheds the ambient count pills **before** the hint rows |
| §7 first-run modal | the agent-tab picker is asked for, never raised. Its answer feeds `ensureAgent`, a hosted-pty path the board thread replaced and nothing reaches |
| §7 empty branch "landed" | `Manager.Landed` returns false for a branch still at its fork point |
| §8 `Running: no` | `gummi status` probes the card lock rather than asserting a fact it cannot know |
| §8 git identity | `gummi doctor` checks it, instead of the merge discovering it after every credit is spent |
| §8 merge dialog | sized to the terminal (~78 columns of body) with a "N more lines" indicator |

### Not fixed

- **§5, `g` on the card page.** The composer holds every printable key on purpose (that is the
  composer-router design). The accelerators remain reachable through the actions menu, which is
  what the footer points at. Changing this is a keymap decision, not a bug fix.
- **§5, multi-select `space`.** Left alone for the same reason — the fix is a hint change in a
  file the picker shares with the free-form channel, and it wants its own look.
- **§7, no stage timeout in the TUI.** The board still cannot notice a wedged stage; it can now
  only show you how long it has been wedged. A real timeout is a behaviour change (it stops
  runs) and belongs in its own card.
