# gummi UX review — round 2, two cards driven on a pty

**Date:** 2026-09-10 (evening) · **Branch:** main @ 1d9478c · **Rig:** a throwaway `tally`
Go CLI (count/top commands), profile `thrifty` = claude-sonnet-5 for architect/implementer/
reviewer, haiku-4.5 for scribe. TUI on a 120×40 tmux pty.

Follows `REVIEW-ux-drive-2026-09-10.md` (same day, earlier). Everything that review fixed
held up: the todo comment no longer wedges the card, the blocker counts stay fresh, prose
at an open ask is the answer, the decision block at the landing gate is excellent. **None of
its findings are re-filed here.** What follows is new.

> **Status: all fixed.** Every finding below is closed in the working tree — see
> "What was changed" at the end for the mechanism-level list, what was verified back on a
> pty, and the three follow-on defects the fixes themselves turned up. `go test ./...`,
> `go vet ./...` and the pinned build are green.

Two cards, driven end to end as a first-time user would:

- **FD-001** "tally top should let me choose how many words to show" — attended, todo → plan
  → implement → verify → landed, including a spec comment at todo, three `ask_user` rounds,
  a diff comment and a rework round.
- **BG-002** "tally count reports one character too many when a file has no trailing newline"
  — created from the same dialog, handed to autopilot, parked at the landing gate, then landed.

Both landed correct work (`tally top -n 3` prints 3 rows; `printf 'abc'` now counts 3 chars),
for ~$5.30 of model spend and ~55 minutes of wall clock.

The findings are ordered by how badly they trap a user, not by how hard they are to fix.

---

## 1. The user gets stuck

### 1.1 Parking a card at the landing gate hides the one decision only a human can make *(worst)*

BG-002 passed verify. The card page showed the good decision block — *land on main / send it
back / stop here*. I chose **"stop here — park it — nothing runs until you come back"**, the
ordinary "I'll deal with this later" action.

Everything about the pending decision then disappeared:

```
   The verify run is paused.
   Before that, autopilot crossed 3 gates and took 3 answers without you. [alt+a]

  gummi  verification stopped here — choose what happens next.
  ▸ 1. pick it back up — the run is paused — a fresh run picks verify back up
```

One row. Not *land on main* — **re-run verify**, at full price, on a stage that already
passed. "Verify passed — the branch is ready to land" is gone from the page entirely.

And the board:

```
   REVIEW / VERIFY
  ▸1 ◐ BG-002 ⚡ tally count reports…  ⏸ [thrifty] ⎇ 253.6cr        ← needs-you line gone

  inbox:   NEEDS YOU
           nothing needs you
```

**"nothing needs you" — while a verified branch sits waiting for the one action gummi
never automates** (DESIGN §16: merge is permanently withheld from automation). A user who
parks a finished card and comes back tomorrow has no surface anywhere telling them the card
is ready to land.

The way out exists but is not offered: `g` from the board opens the merge dialog directly.
On the card page the action inventory has it too — spelled `advance` and `merge`, neither of
which says "land", sitting next to each other so they read as two ways to do the same thing.

**Mechanism**, two independent halves:

- `stageActions` (`ui/nextsteps.go:438`) returns a single row for `engine.StatePaused` and
  short-circuits every stage branch below it. Its comment — *"already stopped by hand:
  picking it back up is the only answer"* — is true of a paused **run** and false of a paused
  **card at a finished gate**: pausing does not un-pass a verify.
- `pauseRun` (`ui/shell.go:3540`) returns `noticeMsg{..., clearInbox: f.ID}`. Parking
  explicitly deletes the needs-you item.

**Suggested shape.** When `in.finished()`, the paused branch should still render the gate's
own answer set, with "pick it back up" demoted to the re-run row. And a gate item should
survive a pause — pausing stops the *agent*, it does not answer the *question*.

### 1.2 An agent's "resolved" silently closes the user's blocking comment

At todo I commented on the spec: *"Name the flag -n, not --limit — short flags fit the rest of
this CLI."* It was filed correctly under **blocks approval (you)**.

By the plan gate it had vanished from the open list — `✎ 2 open`, both of them template
placeholders — and the gate opened. I never answered it and no agent ever addressed it. What
closed it was the architect resolving a *different* thread that happened to share the anchor:

```
- the default is still ten when -n is not given
%% @user(2026-09-10): Name the flag -n, not --limit — …          ← mine, never addressed
%% @reviewer(2026-09-10): blocking — … Add a machine-run `go test ./...` check …
%% @architect: resolved — added `go build ./...` and `go test ./...` …   ← closes both
```

`Doc.Threads` (`internal/spec/spec.go:134`) scans bottom-up: *"a resolution closes only the
markers ABOVE it in the run, plus itself"*. Every marker on one anchor line is one thread, so
any agent's `resolved —` closes every human comment above it.

It gets worse, or rather stranger: at the landing gate my comment came **back** as open,
because the verifier had inserted a plain `  RESULT: PASS…` line between the markers, splitting
the run. So a user comment's open/closed state — the thing that blocks a gate — flips based on
what unrelated text later lands near it.

**Suggested shape.** A `@user` marker is only closed by an `@user` resolution or by an explicit
reply that names it. A model resolving its own reviewer thread must not be able to close a
human's.

### 1.3 Blockers are revealed one at a time

At FD-001's landing gate, with one open spec comment *and* one open diff comment:

```
   1 open comment in the spec is holding the gate shut.
  ▸ 1. resolve open comments — 1 open in the spec blocks the gate — R requests changes
```

I resolved the spec one. The page immediately became:

```
   1 unresolved diff comment is holding the gate shut.
  ▸ 1. resolve diff comments — 1 open blocks the gate — R requests changes, x resolves
```

`whyItStopped` (`ui/narration.go:255`) returns on the *first* non-zero of
`openSpecQs` → `openDiffComments` → `undrafted`. The page always understates what stands
between the user and the gate. Say all of it: *"1 comment in the spec and 1 on the diff are
holding the gate shut."*

### 1.4 A failed run gives no diagnosis and one action that will fail identically

My first run died because opencode was broken on this machine. The whole of what gummi showed:

```
    opencode · openrouter/z-ai/glm-5.3-flash
    ✗ opencode run failed: exit status 1
   The plan session errored before it finished.

  gummi  plan failed — choose what happens next.
  ▸ 1. pick it back up — the run is paused — a fresh run picks plan back up
```

`alt+o` (outputs) added nothing. The backend's own stderr — which said exactly what was
wrong — was not shown, and the single offered action re-runs the same broken command.
This is the *first thing a new user hits* when their CLI is not set up, and it is a loop.

**Suggested shape.** Keep the last N lines of the backend's stderr on the failure event and
render them under the ✗. Offer `gummi doctor` as a second row when a run fails before its
first turn.

---

## 2. Keys and controls that are advertised and do nothing

### 2.1 `?` never opens help from the spec or diff surface

Both surfaces list it in their own key table with `bar: true` (`ui/specview.go:220`), so it
shows in the footer:

```
 gummi   g approve · R request changes · c comment · x resolve · n/p markers · ? help · esc back
```

Pressing `?` does nothing. `handleKey` gates the help overlay on `!m.textEntry()`
(`ui/shell.go:2449`), and `textEntry()` (`ui/shell.go:2650`) is true whenever the card is open
and the thread composer has focus — which it is, underneath the spec surface. Every *other*
key that table lists (`c`, `x`, `R`, `n`, `p`) works, because the surface answers those itself.

### 2.2 `alt+a` — "open cited" — is dead

The narration prints the citation mark and the status bar advertises the chord:

```
   Before that, autopilot crossed 3 gates and took 3 answers without you. [alt+a]
 gummi   ✉ 1 need you   ↑↓ choose · … · alt+o outputs · alt+a open cited · esc backlog
```

Pressed from the top of the thread and again after four page-downs: nothing moves. `alt+o` on
the same screen toggles fine, so the chord is arriving. The event branch of `openAnchor`
(`ui/citations.go:395`) sets `anchorTo/anchorFrom` and returns; `thread.go:503` consumes the
anchor only if the render found a matching row (`anchorIdx >= 0`) and **clears it either way** —
so when the anchored period is drawn folded, the jump is silently dropped.

### 2.3 "press c to clean up" — on the one screen where `c` types a letter

After merging, the status bar says:

```
 gummi   FD-001 squash-merged into main → done — press c to clean up
```

The user is on the card page. Pressing `c` there puts `c` in the composer — and enter would
send it to an agent. `c` only works on the board. The notice advertises a key the surface it
is displayed on does not accept.

### 2.4 "Choose the agent tab's CLI" configures nothing

The space menu offers it; it opens a real dialog, accepts a choice, reports
`agent tab: codex chosen`, and **writes `agent: codex` into `.gummi/config.yaml`**. The agent
tab is unchanged — it is a board session (`claude · claude-sonnet-5 · allow-all`), and
`BoardSession` takes its backend from the profile's role config, never from `Config.Agent`.

The hosted-pty path the setting drives is unreachable: `ensureAgent` (`ui/agenttab.go:99`) has
exactly one non-test caller — the `agentExitedMsg` respawn (`ui/shell.go:1462`) — which can
only fire after a pty that nothing spawns. The earlier review removed the *first-run modal*
for this picker; the menu entry, the dialog, the config key and the persistence survived it.

### 2.5 Declining the attach CLI's trust prompt is reported as an error

`a` (attach) launches the coding CLI raw inside the worktree. It opened Claude Code's
*"Do you trust the files in this folder?"* prompt with no gummi chrome and no hint of how to get
back. Pressing esc — the prompt's own cancel — returned to gummi with:

```
 gummi   raw agent exited: exit status 1
```

A normal cancel reported as a failure, in a phrase ("raw agent") that appears nowhere else.

---

## 3. Promises the UI does not keep

### 3.1 "start" does not start anything

At todo the only action is:

```
  ▸ 1. start — opens the plan stage — the agent reads the card, and any comments on it
```

Pressing enter advances the stage marker and stops. No agent runs. The next screen offers
*"start the architect"* — the real start. The row promised the agent would read the card;
nothing did until a second keypress. (`ui/nextsteps.go:469` — the row's arm is `advance`.)

### 3.2 "approve — hands the card to the agent stages" hands it to a stage that waits

Same shape one stage later (`ui/nextsteps.go:482`). After approving the plan:

```
  gummi  nothing is running — choose what happens next.
  ▸ 1. run implement — no active run — start (or restart) the stage
```

Nothing was handed anywhere. (And "(or restart)" is offered for a stage that has never run.)
The inconsistency is the trap: *within* a run, implement chains into verify by itself, so the
user reasonably expects approve to start implement.

### 3.3 At the plan gate, the pre-selected action is the expensive one

```
   The plan stage finished and wrote the spec — the gate is waiting on you.
  gummi  plan is ready for your decision.
  ▸ 1. start the architect — shape the spec until it convinces you
    2. approve — hands the card to the agent stages
    3. stop here — park it
 gummi   …  enter start the architect  …
```

The page says *ready for your decision* and then puts the cursor on "re-run the architect" —
a ~$2 session in this rig. The landing gate gets this right (`▸ 1. land on main`). Both are
gates; only one leads with the decision. (`ui/nextsteps.go:474-482`: `talkAction` is appended
before `advance` unconditionally, with no `finished()` distinction.)

### 3.4 "land on main" when the branch is `master`

The rig's trunk is `master`. gummi merged into `master` correctly — and said, in the merge
dialog header, the action row, the clean-up row, the autopilot dialog and the help table:

```
  gummi/FD-001-tally-top-should-let-me-choose-how-many → main
  ▸ 1. land on main — verify passed — squash-merge the branch and mark the feature done
```

`"main"` is a hardcoded literal in ~10 user-facing strings (`ui/chip.go:223`,
`ui/commitmsg.go:256`, `ui/nextsteps.go:420,566`, `ui/cardactions.go:232,361,529`,
`ui/keymap.go:169`, `ui/autopilot.go:302,335`, `ui/msgs.go:739`). Any repo not on `main` is
told the wrong branch name at the single most consequential moment in the product.

### 3.5 The commit-message draft has no progress signal and takes ~90–120s

```
  drafting a suggested message… (edit below to keep yours)
```

Static text, no spinner, no elapsed time, no timeout. It resolved after ~90s the first time
and ~2min the second; for that whole time the dialog is indistinguishable from a hang. The
parenthetical also reads backwards — there is nothing "below" yet, and "keep yours" describes
a race the user cannot see.

### 3.6 A hand-typed bug still carries a provenance line

```
# BG-002: tally count reports one character too many when a file has…
> tally count reports one character too many when a file has no trailing newline
> _Reported via manual_
```

The earlier review removed this for hand-typed features — `renderProvenance` skips
`Source == "manual"` (`internal/spec/spec.go:404`) — but `renderBugProvenance`
(`spec.go:512`) has no such guard. I typed it into the dialog; it tells me it was reported
via "manual".

---

## 4. The new-card dialog

The first screen a new user meets after the splash.

```
    kind     ▸ feature    bug    research
  ┃ Describe it. The first line is the title.
    becomes  BG · tally-count-reports-one-character-too-ma
    runs as  2400 credits · thrifty   alt+o edit
    [ Cancel ]  [ Create ]  [ Create & autopilot ]
```

- **`becomes` shows the slug, not the title — and the title is silently truncated.**
  I typed a 78-character first line. The stored title is 59 characters:
  *"tally count reports one character too many when a file has…"* — cut exactly where the
  meaning lives (`when a file has no trailing newline`). `DeriveTitle` caps at
  `maxTitleLen = 60` (`internal/domain/feature.go:505`) and keeps the full text in `OneLiner`,
  so nothing is *lost* — but the truncated form is what the board, the card header, the
  artifact's H1 and every notice show, and the dialog never previews it. Show the derived
  title on the `becomes` line; the slug is the least interesting of the two.
- **`becomes  FD · …` / `BG · …`** — "FD" and "BG" are never expanded anywhere in the UI.
- **`after     —   tab to add`** is unguessable when collapsed. It is the dependency picker
  ("run this after FD-001"); nothing says so until you tab onto it. Call it `runs after`.
- **`envelope  > 2400   credits · 0 = uncapped`** — see §5.
- The hint block shows the bug headings *and* the feature `## Acceptance` line regardless of
  which kind is selected.

---

## 5. Words the user will not know

These are the ones I had to already know gummi to read. Ordered roughly by how often they
appear.

| word | where | what it means | what would read |
|---|---|---|---|
| **envelope** | new-card options, `u` key, autopilot dialog, budget stop | the spend cap for this card | **budget** / **spend cap** |
| **credits** / `cr` / `~324cr` | everywhere; and `$2.06` on the same screen | 100 credits ≈ $1 | pick one unit; if credits stay, say the rate once |
| **corrective rounds** (`⟲ 2 of 5`) · "corrections" (quitresume.go) · "corrective rounds" (autopilot dialog) | card header, two dialogs | automatic retry rounds after a failed critique/verify | one name; and say what happens at 5 |
| **park / parked / parks to the inbox** | decision block, event log, autopilot dialog | stop and leave it for later | **stop and leave it** |
| **bounce / bounce back** | `b` key, actions menu | send it back a stage | the decision block already says **send it back** — use that |
| **advance** | `g`, help, actions menu | cross the gate — approve, or land | the gate's own verb ("approve" / "land on main") |
| **gate** | narration, help, config | the point where a human decides | it is only ever used *about* a decision — say the decision |
| **ingest / Ingest a spec into features** | `I`, space menu, board help | split one big document into several cards | **split a document into cards** |
| **attach / raw-attach / raw agent** | `a`, actions menu, error notice | run your coding CLI by hand in this card's worktree | **open a terminal agent in this card's worktree** |
| **take back the gates** | actions menu | turn autopilot off for this card | **stop autopilot — I'll approve the gates** |
| **attended 1/1 · autopilot 0/2** | status bar, most-read line on screen | lanes busy / lanes available | **1 of 1 cards you're watching · 0 of 2 on autopilot** |
| **1 open %%** | collapsed spec bar, `R`'s help text | open comment threads in the artifact | **1 open comment** — `%%` is the file's syntax, not a word |
| **markers** (spec view) vs **annotations** (diff view) vs **comments** (both dialogs) | three footers | the same thing | **comments** |
| **spec** vs **bug report** vs **artifact** | tab label by kind, help text, narration | the card's design document | tab already adapts by kind; the help and narration should too |
| **needs-you inbox** (space menu) vs **needs-attention inbox** (board help) vs **NEEDS YOU** (the tab) | three places | one queue | **NEEDS YOU** |
| **BACKLOG** (page title) vs **board** (tab label) vs **backlog** (help title) | one screen | the card list | **board** |
| **feature** (`0 features`, `1..9 jump to feature`, `D delete feature`) vs **card** (everywhere else) | status bar, help | any card | **card** |
| **meta-harness for coding agents** | the splash, first thing ever seen | — | say what it does |
| **accelerators** (`y/n accelerators`) | confirm dialogs | shortcut keys | **y / n** |

Two more, on screens I hit repeatedly:

- **"blocks approval (you)"** is the group my comment lands in at `todo`, where there is no
  approval to block. (The earlier review correctly stopped the *gate* from blocking there; the
  *label* still claims it does.)
- **"Create & autopilot"** now names autopilot, which is right — but the dialog it opens still
  explains itself in the vocabulary above: *"it parks to the inbox if it can't finish"*,
  *"up to 5 corrective rounds, inside a 2400 credit envelope"*. Three unknown words in two lines.

---

## 6. Grammar and agreement

Verbatim from screen:

| where | text |
|---|---|
| `ui/backlog.go:360` | `BACKLOG  1 cards  ·  sort: creation order` |
| `ui/verify.go:328` | `baseline — all 3 repo check(s) pass on the fresh branch` |
| `ui/diffrender.go:258` | `sent 1 diff comment(s) to the implementer` |
| `ui/shell.go:1819` | `discovered N repo check(s) into the …` |
| inbox header | `1 open decision` — shown for a *run failure*, which is not a decision |
| card header | `~324 / 2400 credits · 2076 left` — the same fact twice, one of them subtracted |

---

## 7. What the screen shows at the wrong moment

- **The card title is the first thing sacrificed.** On a 120-column terminal, at the plan gate:
  `FD-001 · tally to…   [thrifty]   autopilot: off   206.3 / 2400 credits · 2193.7 left   ⟲ 2 of 5 corrective rounds`.
  Eight characters of title, and later seven. The credits are stated twice and the round
  counter — which the user cannot act on — outranks the name of the thing they are looking at.
- **The busy label names the stage while the transcript names the role.** During implement's
  review pass: the section rule reads `implement · reviewer · claude-sonnet-5 · fresh context`
  and the spinner three lines below reads `⣻ implementing · 3m50s`. The user is told code is
  being written while it is being reviewed.
- **The status bar's left slot is a global notice feed.** At FD-001's landing gate — the moment
  of decision — it read `BG-002: critique passed → verify`. The other card's progress displaces
  this card's state, including the `✉ N need you` pill.
- **A stale notice outlives its truth.** `FD-001: critique requested changes → reworking (round 1)`
  stayed in the bar while the same card had an open question on screen waiting for an answer.
- **Elapsed time counts from the stage, not the run.** After a failed plan run at 20:35 and a
  fresh one at 20:37, the spinner read `writing plan · 2m38s` at 20:38 — including two minutes
  in which the TUI was closed and nothing was running.
- **The board's per-card spend does not move during a run** (`~317.4cr` for seven minutes while
  the turn line counted up to 24.7 credits).
- **No legend for the board's glyphs.** A row can carry `○ ● ◐ ✔ ⚡ ⎇ ✗ ⏸ ✉ ⟲ ~` and the help
  overlay explains keys only.
- **The board help is 28 keys in one flat alphabet-soup list**, mixing `enter`/`j`/`k` with
  `z` (squash in place) and `r` (rebase). Nothing marks which three a new user needs.
- **The comment dialog never says what it is commenting on.** `c` opens a box titled
  `comment` with the placeholder `your comment`; on the diff it opens *over* the line it will
  anchor to. Show the anchor line in the dialog.
- **The actions menu on a landed, cleaned-up card still offers** `hand to autopilot`, `verify`,
  `rebase`, `ask`, `bounce` and `merge`.
- **Answers to `ask_user` are labelled inconsistently**: a typed line renders as
  `you · recorded in the spec`, a picked option as bare `you`. A reader concludes their picked
  answer was recorded nowhere.
- **Internal tool failures render as conversation.**
  `· spec capture skipped: "Chosen approach was never filled in" not found (or not unique) — note it in the spec …`
  is a gummi-internal anchor miss, shown to the user in the transcript with no action attached.
- **Raw MCP identifiers in the transcript**: `· ToolSearch  select:mcp__gummi__spec_view,mcp__gummi__ask_user,…`
  and `· mcp__gummi__spec_replace_section`. `spec_view` / `replace section` would read.
- **A fresh card reads as five open threads.** `FD-001 · spec ✎ 5 open` before anything has run,
  all of them `%% @gummi:` template placeholders. They are correctly grouped as
  *agent notes (non-blocking)* now — but the headline count does not distinguish them.

---

## 8. Smaller notes

- `x` (resolve) writes a contentless `%% @user(2026-09-10): resolved` — the same marker the
  architect complained about in the earlier review. Resolving with a reason (prompt for one
  line, default empty) would make the artifact readable later.
- After clean-up, the board row **stops saying `landed`** — the word is derived from the branch,
  and clean-up deletes the branch. A done card that landed and one that was abandoned then look
  identical.
- The merge dialog's *"unreviewed draft — ctrl+s again to land without reviewing"* guard is good.
  It fires even when the whole draft is visible on screen; "unreviewed" means "not scrolled or
  edited", which is not what the word says.
- The reviewer role escaped the rig: `grep -rl "gummi-checks" /` and reads of
  `/project/internal/spec/checks.go` during BG-002's critique. The write cage holds; shell and
  reads roam the filesystem. Worth knowing that "caged" in `doctor`'s output means writes only —
  `doctor` says so, but the one-line summary reads stronger than it is.
- Agents burn turns discovering that the artifact is outside the worktree:
  *"The Edit tool can't write outside the worktree — the bug report is only writable through
  gummi's `spec_annotate`."* Both cards hit it. Worth a line in the stage kickoff.

---

## 9. What worked well

- **The autopilot receipt is the best screen in the product.** Collapsed per-pass rows with
  turns, credits and clock times; `✓ autopilot answered …` and `✓ autopilot crossed plan →
  implement` inline; and the summary sentence *"Before that, autopilot crossed 3 gates and took
  3 answers without you."* Nothing else I have seen makes delegated work this auditable.
- **The landing decision block**, when its inputs are fresh: *verification passed — decide
  whether this work is ready to land / land on main / send it back — not convinced — your line
  goes back with it / stop here*. Exactly right.
- **Prose at an open ask is the answer** — the title flips to `FD-001 asks · your line is the
  answer` the moment you type. It worked first try, and the answer was recorded in the spec.
- **`R` (request changes) on the diff** did exactly what it said: a rework session, the shorter
  usage line I asked for, and a return to the gate.
- **The blocked-gate rules from the last review hold.** A comment at todo no longer wedges the
  card, and the counts stayed correct through four surface switches.
- **`gummi doctor`** is genuinely good — it caught the git identity, named the profile, and
  told me `auth:opencode` could not be checked offline. It is the only place a misconfigured
  backend is explained; §1.4 is largely "the failure screen should point here".
- **The work itself was correct**, on both cards, with real tests and live checks.

---

## 10. What I would fix first

1. **§1.1** — a parked card at a gate keeps its gate: the decision block and the inbox item.
   This is the only finding that can lose work indefinitely.
2. **§1.2** — a model's `resolved` must not close a human's comment.
3. **§3.4** — stop hardcoding `main`. It is a one-line lookup and it is wrong on every
   `master` repo.
4. **§1.4** — show the backend's stderr on a failed run, and point at `gummi doctor`.
5. **§2.1–2.4** — four advertised controls that do nothing. `?`, `alt+a`, `press c`, and the
   agent-CLI picker (which should be deleted along with the dead `agent:` key).
6. **§3.1–3.3** — make the advance rows honest about what they start, and lead a finished gate
   with its decision.
7. **§5** — the vocabulary table. Half of it is one string each; `envelope → budget`,
   `%% → comment`, and one name for the retry budget would remove most of the confusion I hit.
8. **§6** — the plurals, twenty minutes.

---

## Appendix — how to reproduce this rig

```sh
cd $SCRATCH/rig/tally          # a 5-file Go CLI, git init, one commit
/project/bin/gummi init
cat > .gummi/profiles.yaml <<'EOF'
default: thrifty
profiles:
  thrifty:
    architect:   { backend: claude, model: claude-sonnet-5 }
    implementer: { backend: claude, model: claude-sonnet-5 }
    reviewer:    { backend: claude, model: claude-sonnet-5 }
    scribe:      { backend: claude, model: claude-haiku-4-5-20251001 }
EOF
tmux new-session -d -s ux -x 120 -y 40 -c "$PWD" \
  "GUMMI_ENVELOPE=2400 GUMMI_NOTIFY=off /project/bin/gummi"
```

Note for a later drive: **opencode on this machine is currently broken** — every run dies with
`TypeError: undefined is not an object (evaluating 'a.name')` in `SystemPrompt.environment`,
so any profile routing at `backend: opencode` (including `/project/.gummi/profiles.yaml`'s
`claude` profile, which is opencode/glm-5.3-flash) fails before its first turn. The `claude`
backend works.


---

## What was changed

Every finding above is fixed in the working tree. `go test ./...`, `go vet ./...` and the
pinned build are green. Where a fix changed a contract, the test that pinned the old
contract was rewritten rather than deleted, with the reason in its doc comment.

### The two that could lose work

**§1.1 — a parked card keeps its gate.** Two independent causes, both closed.
`stageActions` (`ui/nextsteps.go`) only returns the bare "pick it back up" row while
`!in.finished()`; a paused card whose stage already finished falls through to that stage's
real answer set, so a verified branch still offers **land on main**. A new `stopOrResume`
substitutes "pick it back up" for "stop here" on an already-stopped session, so the set
never offers to stop what is already stopped. And `pauseRun` (`ui/shell.go`) no longer
clears the inbox unconditionally: `attnGate`, `attnFailure` and `attnBudget` survive a
pause — pausing stops the *agent*, not the *question* — while `attnQuestion` clears,
because the agent that asked it is the thing being stopped.

**§1.2 — an agent's resolution cannot close a human's comment.** `Doc.Threads`
(`internal/spec/spec.go`) now tracks two flags as it scans a thread bottom-up: an
any-author resolution closes agent markers as before, but a `@user` marker closes only
under a `@user` resolution. `UserOpenThreads` — the single source of truth for the
gate-blocking check on both the UI and `Engine.Advance` sides — needed no change once the
per-marker flags were right.

That rule has a consequence, and it is handled on screen rather than in the parser: after
`R` sends comments to the agent and the agent answers, they stay open until the human
presses `x`. The artifact surface now says so — *"every open comment has already been
answered — x resolves one that the answer addressed; R sends them back again"* — mirroring
the sentence the diff surface already had. Without it the fix would have created the
unbounded rework loop the previous review found at the plan gate.

The anchor-robustness half (a plain `RESULT: PASS` line splitting a thread) was
deliberately **not** attempted: `RESULT: PASS` is free text an agent wrote, and telling it
apart from real content inside `Parse` is a broad, semantically-loaded change. It is
recorded here rather than half-done.

### The rest

| finding | change |
|---|---|
| §1.3 blockers one at a time | `whyItStopped` names every kind at once — "1 comment in the spec and 1 on the diff are holding the gate shut" — and `blockedGate`'s single row appends "(plus 1 on the diff)" so the one offered action never understates what is left |
| §1.4 failed run has no diagnosis | new typed `agent.RunFailure{Backend, Diagnostic, FirstTurn, Err}`; opencode/codex/zz switched from an unbounded `strings.Builder` to the existing `capWriter` ring buffer (16 KB tail), claude/headless/copilot already bounded. The engine needed no change — `failRun` stores the error object itself. The narration names `gummi doctor` when the backend never produced a turn; the pointer is a *sentence*, not a fifth action row, because the answer set is a closed four |
| §2.1 `?` dead on spec/diff | `textEntry()` no longer reports "typing" when a review surface is drawn over the composer |
| §2.2 `alt+a` dead | `appendStretchCloses` drew a period's rules but never reported which row the opening one landed on, so `anchorIdx` stayed −1 and the anchor was cleared with nothing moved. It threads the row back now, and an unresolvable citation leaves a notice instead of swallowing the key |
| §2.3 "press c to clean up" | one `cleanUpNudge` constant naming the action, true on both surfaces; `c` was not rebound — the composer owning printable keys is deliberate |
| §2.4 dead agent-CLI picker | removed outright — the picker, the hosted pty, its session file, the `agent:` config key and everything gated on `hostedKeyboard()` (see "Closed afterwards") |
| §2.5 clean cancel reported red | a non-zero exit from an attached CLI is reported, not judged: `FD-001: claude exited without finishing (status 1)`, ordinary style. A CLI that could not start still errors, caught earlier |
| §3.1/§3.2 advance rows over-promise | "open the plan stage — moves the card into plan; start the agent there…", and approve names the implementer. "(or restart)" appears only when something has actually run |
| §3.3 gate leads with the re-run | at a *finished* plan gate the order is approve-first; unfinished is unchanged. No row added, removed or renamed |
| §3.4 hardcoded "main" | `Manager.BaseBranch` → `Pool.BaseBranch` → `Shell.baseBranch(f)`, resolved once at attach so render paths never shell out to git. Verified on a `master` repo: "rebase branch onto master", "squash-merge branch into master" |
| §3.5 silent draft | spinner + elapsed on the merge dialog's drafting line, and the parenthetical says what it means |
| §3.6 hand-typed bug provenance | `renderBugProvenance` skips `Source == "manual"`, like the feature renderer already did |
| §4 new-card dialog | `becomes` shows the derived title and its real truncation (`FD (feature) · notes list should be able to show only the most recent N…`), `runs after` replaces `after`, `budget` replaces `envelope`, hints follow the selected kind, and FD/BG/RS are spelled out |
| §5 vocabulary | ~40 strings; `envelope`→`budget` everywhere including the CLI flag *descriptions* (the `--envelope` flag and `GUMMI_ENVELOPE` keep their names — they are an API scripts call), one name for the retry budget, `bounce`→`send it back`, `take back the gates`→`stop autopilot`, `raw-attach`→`open a terminal agent in this card's worktree`, `Ingest a spec into features`→`Split a document into cards`, `feature`→`card`, `%%`→`comment`, `markers`→`comments`, and one name — **board** — for the screen that was BACKLOG/backlog/board |
| §6 grammar | `1 card`, `all 3 repo checks pass` / `all 1 repo check passes`, `sent 1 diff comment`, and the inbox counts *items* because a run failure is not a decision |
| §7 stale/misleading state | busy label names the pass (`reviewing` during implement's critique, not `implementing`); elapsed counts from the run, not the stage; board spend reads the live session; `mcp__gummi__` prefixes stripped from the transcript; **both** answer kinds carry their outcome, and the two unhappy outcomes now exist at all (see below); the help overlay is grouped with a glyph legend; a landed card still says "landed" after clean-up (`LandedSHA`, which clean-up cannot erase) |
| §8 smaller | `x` takes an optional reason; the "unreviewed draft" guard says what it checks; the splash says what gummi does and names the first step; `gummi --help` stopped citing a design-doc section at people who have no copy of it |

### Three defects the fixes turned up

- **A user's answer could be dropped entirely.** `captureAnswer`'s anchor lookup fails
  closed on both zero and multiple matches, and on failure it wrote *nothing* — the
  decision never reached the artifact, and the only trace was `spec capture skipped: …` in
  the transcript. It now appends at the end of the document (the one position always
  valid) and says so in plain words. Three exported note constants and `IsAnswerNote` let
  the chat surface fold the clean one and keep the two unhappy ones visible.
- **The stage kickoff was lying to half the backends.** `contractHint` told every session
  to "read and edit the artifact in place there" — false for the three backends whose
  write tools are caged to the working directory, which is why both cards in the drive
  burned turns discovering it. It now names the mediated tools, warns that a read
  succeeding does not mean a write will, and — after a test caught it — names only the
  tools the session was actually served (a read-only research critique was being told
  about `spec_replace_section`, which it does not have).
- **`TestAltA…` hung the entire UI suite.** A new test's `agent.Fake` had no `RoleScribe`
  branch, so check discovery's select never returned. It is why several agents reported
  they "could not get a clean run" — the package was not slow, it was blocked.

### Closed afterwards

Two items were held back from the first pass and finished in a follow-up, so nothing on
this list is outstanding.

**§2.4 — the dead agent-tab picker is gone.** The TUI's agent tab hosts an in-process
board session; the pty it used to host, the dialog that chose which CLI to host, and the
`agent:` config key that persisted the answer were all still in the tree, reachable only
through a menu entry that changed nothing. `gotoTab`'s own comment had recorded the state
of affairs — *"nothing calls it any more: m.agent must stay nil"* — and deferred the
removal to "the later phase that retires it outright". That phase is done: the picker, the
pty view, its session file, the `agent:` key and everything gated on `hostedKeyboard()`
(including the ctrl+g keyboard lock, which could only ever engage over a pty) are deleted.
The `a` raw-attach hatch is a different feature and is untouched, so `GUMMI_ATTACH_CMD`
and `GUMMI_AGENT` keep their meaning through `rawattach.go`.

**§1.2's anchor robustness — an indented continuation no longer re-splits a thread.**
This was deferred as "too broad for a narrow fix", and the narrow fix turned out to exist.
`Parse` (internal/spec/spec.go) anchors a marker to the nearest preceding non-marker line;
an *indented* line inside a run of markers is now read as a continuation of the marker
above it instead of as new content to anchor to. Without that, this happened:

```
- the default is still ten when -n is not given      ← one closed thread
%% @user: Name the flag -n, not --limit
%% @user: resolved
```

the verify pass appended its evidence under the first marker, indented —

```
%% @user: Name the flag -n, not --limit
  RESULT: PASS. printed exactly 10 rows.             ← became an anchor
%% @user: resolved                                   ← split away from what it resolved
```

— and the comment **reopened**, shutting the gate again over a decision the human had
already closed, with nothing on any screen to say why. Reproduced as a unit test
(`Parse` reported 1 thread / 0 open before the appended line and 2 threads / 1 open after);
it now reports 1 thread / 0 open either way. Unindented content still anchors, so a marker
on the next paragraph is not swallowed into the previous conversation.

### Withdrawn

- **"The elapsed timer freezes during a long model turn"** was recorded here as an
  observation and is wrong. The clock does advance: a 120ms `spinnerTick` loop runs for as
  long as anything is busy (`spinnerActive`, ui/spinner.go), and `withElapsed` recomputes
  from `now` on every render. What was actually seen across captures minutes apart is the
  *per-pass reset* the §7 fix introduces — elapsed counts from the current run's segment,
  and a design stage runs several architect/reviewer passes, so the clock restarts at each
  one. That is the intended behaviour. Whether a stage-level age belongs beside the
  per-pass one is a design question, not a defect.
