# gummi UX review — round 3, two cards driven on a pty

**Date:** 2026-09-11 · **Branch:** `ux-drive-round2-fixes` @ 30c57cb · **Rig:** a throwaway
`ledger` Go CLI (list/sum over a plain-text expense file), profile `thrifty` =
claude-sonnet-5 for architect/implementer/reviewer, haiku-4.5 for scribe. TUI on a 120×40
tmux pty. Base branch deliberately `master`, not `main`.

Follows `REVIEW-ux-drive-2026-09-10.md` and `-round2.md`. Everything those two fixed held
up and **none of their findings are re-filed here** — the paused-gate rule, the blocker
counts, the `whyItStopped` narration, approve-first at a finished plan gate, the
commit-message drafting spinner, the two-step ctrl+s merge guard and the autopilot stretch
summary are all genuinely good now. What follows is new.

Two cards, driven end to end as a first-time user would:

- **BG-001** "ledger sum -category food reports nothing when the file says Food" — attended,
  todo → plan → implement → verify → landed, including three `ask_user` rounds, a spec
  comment that the reviewer picked up and acted on, two plan critique rounds and one
  implement critique round.
- **FD-002** "Sort ledger list output by amount" — created from the same dialog with a
  deliberately small 150-credit budget, handed to autopilot, stopped on budget inside verify,
  topped up, then landed.

Both landed correct work (`sum -category food` now reports `3 entries, total 53.45`;
`list -sort amount` sorts descending and `-sort bogus` exits 2), for ~506 credits of model
spend and ~50 minutes of wall clock. A third card was created and deleted to probe dialogs.

The findings are ordered by how badly they trap a user, not by how hard they are to fix.

---

## 1. The user gets stuck

### 1.1 The only forward action on a budget-stopped card is a silent no-op *(worst)*

FD-002 ran out of budget inside verify. The card page offered exactly two rows:

```
   The verify stage reached its envelope and stopped.
   Before that, autopilot crossed 3 gates and took 5 answers without you. [alt+a]

  gummi  verify reached its envelope.
  ▸ 1. top up and go on — raise the envelope — verify picks up where it stopped
    2. stop here — park it — nothing runs until you come back
```

Pressing enter on row 1 does **nothing**. No dialog, no notice, no state change. I pressed
it twice to be sure. The card page has no way forward at all: the only other row parks it.

**Mechanism.** `stageActions` (`ui/nextsteps.go:536`) builds the row as
`nextStep("topup", "", …)` — id `topup`, key `""`. `openDecisionBlock` (`ui/decision.go:904`)
hands it to `runCardAction` (`ui/boardactions.go:284`), which short-circuits on
`a.key != ""` and otherwise switches on `a.id` over
`expand/duplicate/profile/ask/changes/gate/prlink/prunlink/prpull`. There is no `topup`
case, and the key is empty, so control falls out of the switch to `return nil`.

`boardVerb` (`ui/boardactions.go:236`) *does* handle `topup` — that is the board-key and
typed-verb path. Only the card page's own decision row misses it, and the card page is
where the stop is reported.

The escape exists and is never named on that page: the inbox's `u`. The transcript line
right above the decision block does name it — "verify reached its budget with work
committed — advance it, or top it up from the inbox" — which is the only reason I found it.

### 1.2 Visiting a card from the inbox deletes the needs-you item even when you did not answer

The architect asked a question. I opened the inbox, pressed enter on the row to go read it,
looked at the question, and pressed esc to think about it. Then:

```
   IN PROGRESS
  ▸1 ● BG-001 ledger sum -category food reports nothing when the file… [thrifty] ~10.6 credits

   NEEDS YOU
   nothing needs you

 gummi   ⬤ 1 running
```

The board row carries no marker, the inbox says "nothing needs you", and the status bar says
"running" — while the agent sits blocked on a human. The question is still open; nothing
answered it.

**Mechanism.** `inboxJump` (`ui/inboxview.go:67`) calls `m.inbox.remove(id)`
unconditionally, on the reasoning in its own doc comment: "opening the page is opening the
card at its decision". True while you are on the page; false the moment you leave it.

This is the exact state `shell.go:1240`'s comment says must never happen — verbatim: *"the
board says 'in progress', the status bar says 'running', and the inbox says 'nothing needs
you', while the agent sits blocked on a human."* That fix closed the **raise** path. The
**clear** path reopens the same hole one key later.

### 1.3 Answering a question can file a blocking comment against yourself

At the plan gate BG-001 showed two open comments. I had written one. The second was this,
appended to the end of the bug report by gummi itself:

```
%% @user(2026-09-11): answered "strings.EqualFold(e.Category, category)" — Yes, move on
(appended: the spec no longer has one line matching that anchor)
```

That is `captureAnswer`'s record of **my own answer to gummi's own question**. It is
authored `@user`, it is not a resolution, so `spec.Parse` reads it as an open user thread —
and an open user thread shuts the approval gate. Only a human can close it (round 2's rule,
which is correct), so gummi filed a blocker against the user for answering a question, and
the user has to find it and resolve it by hand before the card can move.

The reviewer noticed and could not do anything about it either: *"the stray marker on the
last line of the report (referencing an anchor that no longer matches any spec line) is
already resolved by the user and doesn't indicate a plan defect — just leftover bookkeeping."*

**Mechanism.** `captureAnswer` (`engine/asktool.go:1160-1173`) writes
`"resolved — " + answer` on the happy path — which `resolvedRe` (`spec/spec.go:88`) matches,
closing the thread cleanly. On the anchor-miss fallback it writes
`answered %q — %s (appended: …)`, which matches nothing, so the note opens a thread instead
of closing one. The fallback is deliberate and right; its *spelling* is what gates the card.

### 1.4 An agent's resolution under a user comment does not close it, and the one offered action cannot either

I commented on the gummi-checks block. The reviewer read it, quoted it, and the architect
fixed the checks and wrote `%% @architect: resolved — …` directly beneath my comment. The
screen still showed:

```
  blocks approval (you)
    ☐ The gummi-checks commands only build and run the binary, so they pass whether…  L76
```

The rule is right — an agent must not close a human's comment. But nothing on screen says
that the resolution sitting one line below is not a closure, and the decision block's only
offered action makes it worse:

```
  ▸ 1. resolve open comments — 2 open in the bug report block the gate — R requests changes
```

`R` sends the comments *back to the agent*, which is what already happened and which can
never close a `@user` marker. The action that works is `x`, on the spec surface, and the row
never names it.

### 1.5 A malformed gummi-checks block fails the baseline, shows a Go error, and blocks nothing

Because my comment made the architect **rewrite** the checks block, it wrote a plain YAML
string list instead of the `- name: / cmd:` mapping. At approval:

```
  BG-001: gummi-checks baseline failed — gummi-checks block does not parse: yaml: unmarshal errors:
  line 1: cannot unmarshal !!str `go buil...` into domain.Check
  line 2: cannot unmarshal !!str `go buil...` into domain.Check
  line 3: cannot unmarshal !!str `go buil...` into domain.Check
  line 4: cannot unmarshal !!str `go test...` into domain.Check
```

Four things wrong at once:

- It names `domain.Check`, an internal Go type, at a user who never agreed to know about it.
- It never says what the correct shape is, so it cannot be acted on.
- It is a **transient notice**. No inbox item, no blocker row, gone on the next keypress.
- **Nothing blocked.** The card advanced into implement, `check_baseline` stayed empty, and
  the decision block said "run implement — no active run — start the stage" as if all were
  well. Verify then reported `pass` — the reviewer had run the commands by hand and said so:
  *"kickoff had no gummi-checks results block, so I ran them myself."* The user has no way to
  know gummi's own check runner never ran.

**Root cause is upstream of the parser.** `discoverPrompt` (`engine/discover.go:17`) carries
the schema example, and only the **scribe** sees it. `promptBugVerify` (`spec/spec.go:514`),
which is what the **architect** reads when it authors or rewrites the block, says only "the
discovered gummi-checks commands always run" and shows no shape at all.

---

## 2. Keys and actions that are advertised and do nothing

### 2.1 The inbox row says `g approve`; `g` is dead on the inbox tab

```
  ▸ ✉ BG-001  plan gate  critiqued: clean — review & approve
    ↳ g approve — moves the card into implement — start the implementer there to begin
```

Pressing `g` does nothing — no notice, no movement. `inboxKey` (`ui/inboxview.go:84`)
answers `j/k/i/enter/x/u` only, and the footer lists `enter go · x dismiss`. The row's hint
comes from the shared `nextStep` helper, which names the key that works **on the card page**.
Same for the verify gate's `g land on main`.

### 2.2 `[alt+a]` is printed beside claims whose anchor resolves to nothing

`narration.go:34-37` states the invariant plainly: *"a rendered narration may not contain an
anchor that resolves to nothing (citations.go, invariant 3)."* On screen:

```
   Before that, autopilot crossed 3 gates and took 5 answers without you. [alt+a]
```

`alt+a` → `FD-002: nothing on this page opens at that citation`. This is not an edge case:
it also failed when the status bar itself advertised the key —
`alt+o outputs · alt+a open cited · esc board`. The error text is the only place in the
product that uses the word "citation".

### 2.3 Card-page help is unreachable unless you already know the key

The card page footer never offers help, and `?` types a character into the composer (which
is deliberate). The command menu lists:

```
    Show the keys for this surface  ?
```

— naming a key that does not work on this surface. Running that entry opens a genuinely good
table, whose last row reads:

```
  alt+/      this table — ? types here rather than opening help
```

So the real key is documented **only inside the help you can only reach without it**. On the
busiest screen in the product there is no path to help for someone who has not already found
one.

### 2.4 That help table is wider than a 120-column terminal

The `keys · card` overlay renders with no right border at all and its first row cut
mid-word:

```
╭──────────────────────────────────────────────────────────────────────────────────────────
│  ↑↓         move through the open decision — ↑ off the top opens the action inventory (dependencies, envelope, duplica
```

`helpDialog.View` (`ui/dialogs.go:43`) takes `w` and never uses it — rows are written at
their natural width and the frame grows past the pane. The board's table happens to fit;
the card's longest row is ~150 columns and does not.

---

## 3. Information the user cannot reach

### 3.1 An `ask_user` question is hard-capped at two lines and the rest exists nowhere

Every one of the four questions asked during this drive was cut off, and in three of them
the part that was cut was **the actual question**:

| on screen | in `card_events` |
|---|---|
| `…not e.g. requiring exact-match input but normalizing the file data, or…` | `…or flagging mixed-case data as a load error?` |
| `…environment and severity (Medium) recorded. Ready to move on to diagnosing…` | `Ready to move on to diagnosing the root cause?` |
| `…(live-proof + gummi-checks + regression…` | `…regression test). Ready to move on to implementation?` |

`pickerQuestionLines = 2` (`ui/decision.go:249`), whose comment reads *"Two is enough for the
questions the guide actually poses."* Four for four, it was not. The inbox row truncates to
one line, `pgup` scrolls the transcript and leaves the pinned block alone, and nothing else
on any surface carries the text. The **selected option** expands its detail correctly — the
question is the one thing in the control that cannot.

### 3.2 The blockers panel clips the line number, pointing at a different line

At 120 columns:

```
    ☐ answered "strings.EqualFold(e.Category, category)" — Yes, move on (appended: the spec no longer has one line…  L10
```

At 160 columns the same row reads `L105`. The marker is on line 105; the panel sends the
reader to line 10.

`renderStatus` (`ui/specview.go:650`) truncates the text to `w-8` and *then* appends
`"  L" + line`, so the row is `w+2` wide and the pane clips the tail. The text truncation has
an ellipsis; the number's does not, so it fails silently into a plausible wrong answer.

### 3.3 Card titles are silently cut to 60 characters — into the database, the H1, and the branch

I typed a 64-character first line under a field that says *"Describe it. The first line is the
title."* What was stored:

```
sqlite> select id,title from features;
BG-001|ledger sum -category food reports nothing when the file…
```

`DeriveTitle` (`domain/feature.go:513`, `maxTitleLen = 60`) is lossy and permanent. The
on-disk bug report's own heading carries the ellipsis —

```
# BG-001: ledger sum -category food reports nothing when the file…

> ledger sum -category food reports nothing when the file says Food
```

— with the full title quoted two lines below it. 60 characters is under a git subject line
and well under a GitHub issue title; an ordinary bug title overruns it.

### 3.4 The comment dialog shows no anchor, and covers the line it is anchoring to

`c` opens a box that says only `comment` and `> your comment`. It is drawn **over** the line
it will attach to, and it is a one-line field that scrolls horizontally, so a
three-sentence review comment shows only its tail:

```
   72    %% @architect: re│  >  output, e.g. grep for '3 entries, total 53.45'.   │
```

My comment was about the gummi-checks block at L88; it anchored to `## Review` at L76,
because that is where the cursor happened to be. The `x` resolve dialog *does* show which
comment it will act on — the asymmetry is the tell.

### 3.5 "resolve open comments" drops you in the middle of the document

Enter on that row opened the spec at line 37. The blockers were at L76 and L105. `n/p` walks
**every** marker line — resolutions and `@gummi` template placeholders included — so
reaching the first blocker took seven presses through five markers that were not blocking
anything. The panel at the top lists the blockers with line numbers and cannot be jumped from.

---

## 4. Numbers and state that contradict themselves

### 4.1 Two `⟲` badges of the same shape, one of them unlabelled

```
BG-001 · ledger sum -c…   [thrifty]   autopilot: off   237.6 / 2400 credits   ⟲ 1 of 3   ⟲ 3 of 5 corrective rounds
 gummi   BG-001: critique requested changes → reworking (round 1)
```

Three different round numbers on one screen. `correctiveLabel`'s doc comment
(`ui/thread.go:908`) predicted this exactly — *"two unlabelled badges of the same shape side
by side would be two numbers nobody could tell apart"* — and then labelled only one of them,
which does not tell the reader what the other one is. The first badge's denominator also
changes silently from 2 to 3 crossing plan → implement (`roundLabel` swaps
`maxPlanRounds` for `maxReviewRounds`), and the help legend has one `⟲` entry describing
only one of the two.

### 4.2 `171.1 / 150 credits`

The header of a budget-stopped card. The overshoot is real and expected — a turn in flight
finishes — but nothing says so, and a spend counter reading past its own cap just looks broken.

### 4.3 The card title is still the first thing sacrificed

At its worst this drive, on a 120-column terminal: `BG-001 · ledger sum -c…` — thirteen
characters of title, while two round counters the reader cannot act on and a credit figure
keep their full width beside it.

---

## 5. Words the user will not know, or will read as two different things

### 5.1 "envelope" survives on every surface that reports a budget stop

Round 2 replaced `envelope` with `budget` across ~40 strings. Every surface that *sets* a
budget now says budget. Every surface that reports **running out** still says envelope — and
that is the first time a user meets the word:

| where | text |
|---|---|
| board needs-you line | `The verify stage reached its envelope and stopped.` (`ui/narration.go:249`) |
| decision title | `verify reached its envelope.` (`ui/decision.go:371`) |
| the one forward row | `top up and go on — raise the envelope — verify picks up where it stopped` (`ui/nextsteps.go:537`) |
| inbox footer | `top up — raise the envelope and resume (budget items only)` (`ui/inboxview.go:127`) |
| the dialog's **title** | `envelope · FD-002` (`ui/envelope.go:210,217`) |
| after topping up | `FD-002 topped up — envelope raised to 240 credits, resuming` (`ui/envelope.go:124`) |
| card help table | `…(dependencies, envelope, duplicate, delete, repo…)` (`ui/threadinput.go:1066`) |
| advance | ` · envelope estimated at %d credits from %d metered feature(s)` (`engine/advance.go:452`) |

Six lines apart on one screen, the transcript says the other word: *"budget reached — stage
work committed"*, *"verify reached its budget with work committed"*.

### 5.2 "land on main" on a repository whose branch is `master`

Round 2's §3.4 resolved the base branch once at attach and swept the help and board strings.
The landing gate was missed — which is the one row where the name of the branch matters:

| where | text |
|---|---|
| help overlay | `m  squash-merge branch into master` ✓ |
| merge dialog | `gummi/BG-001-… → master` ✓ |
| decision row | `land on main — verify passed — squash-merge the branch and mark the bug done` ✗ (`ui/nextsteps.go:709`) |
| inbox row | `verify gate  passed — review & land on main` ✗ (`ui/reviewloop.go:513`) |
| after merging | `BG-001 squash-merged into main → done` ✗ (`ui/merge.go:118,121,123`) |
| CLI help | `diff  Dump a feature's worktree diff against main` ✗ |

### 5.3 One key, four names

`g` is `advance` in the board footer, the actions menu and the slash menu; `approve` in the
spec and diff footers; `land on main` in the decision block; and "advance it" in the
autopilot park line — all for the same key on the same card. The help overlay gets it right
(`g  move the card to its next stage`) and is the only place that does. Round 2 replaced
"advance" with the gate's own verb in the decision block; the four other surfaces kept it.

### 5.4 `backlog` and `board` on the same screen

The card page header says `‹ esc backlog`; the card page **footer** says `esc board`; the
board itself says `BOARD`. Round 2 settled on one name — board — and this pair was missed.

### 5.5 Smaller, all verbatim from screen

| where | text | note |
|---|---|---|
| status bar | `✉ 1 need you` | `ui/shell.go:3706` hardcodes the plural |
| every confirm dialog | `enter select · ←/→ move · y/n accelerators · esc cancel` | §5 asked for `y / n` |
| dependency picker | `dependencies · FD-003   1 deps` | and `keys · deps` in its help |
| actions menu | `read or annotate the bug report (tab toggles annotate)` | §5 settled on **comments** |
| new-card dialog | `runs as  2400 credits · thrifty` | reads as "runs as 2400 credits" |
| new-card dialog | budget in credits, with no rate, ever | the pass footers say `$0.23`; nothing ever connects the two |
| slash menu | Sentence-case board commands, then lowercase card commands, no heading between | one flat list, two conventions |
| space menu | `Switch the board's profile` / `Switch the board's model` | reads as the default for new cards; actually the agent tab's chat session, which it silently jumps to |
| autopilot dialog | `up to 5 corrective rounds` | still never says what happens at 5 |
| `gummi --help` | `Decompose a spec into feature proposals and materialize them` | TUI says "Split a document into cards" |
| `gummi --help` | `Rehydrate a parked feature and drive it on` | |
| `gummi --help` | **feature**, **work item**, **card**, **bug** — four nouns for a card in one screen | |
| `gummi status` | `0 open question(s) · 0 open diff comment(s)` | §6's `(s)` sweep never reached the CLI |
| `.gummi/config.yaml` | `See docs/DESIGN.md.` ×2 | `gummi --help` stopped citing it at people who have no copy; the seeded config still does |

### 5.6 The bug report template's own framing

The panel above a fresh bug report is headed `agent notes (non-blocking)` and lists the
template's unanswered prompts as unchecked boxes — so a brand-new card opens looking like it
has seven unfinished tasks, under a heading that calls the questions "notes". "non-blocking"
is the gate's vocabulary, not the reader's.

---

## 6. The tab order in the new-card dialog

`kind` is the first row on screen and the **last** stop in the tab cycle: composer → budget →
profile → runs after → buttons → kind. A user who types a bug description and sees
`becomes  FD (feature) · …` has to tab five times past everything else to fix it. The
focused kind row is also the only row that does not move the `▸` onto its label, so the
focus is a background colour and a one-character shift.

---

## 7. What worked well

Worth saying, because it is most of the product:

- **The plan → implement → verify loop produced correct, minimal, well-tested work on both
  cards**, including picking up my review comment, quoting it back, fixing the thing it was
  about, and verifying the fix was red before and green after.
- **The gate decision block** at a finished plan gate: approve-first, the blocker sentence
  ("2 open comments in the bug report are holding the gate shut"), and the three-row closed
  answer set. This is the best surface in the product.
- **The autopilot stretch summary** — every gate crossed and every answer taken, listed, with
  the question and the chosen option. Nothing about the run is hidden.
- **The two-step `ctrl+s` merge guard**: *"this is the scribe's draft, untouched — ctrl+s
  again to land it as written."* Exactly the right amount of friction.
- **The board's needs-you second line** under a stopped row, and the `$ ✉ ✗ ?` glyph set.
- **`x resolve` showing which comment it will close**, and taking an optional reason.
- **`gummi doctor`** caught a missing git identity before anything else could fail on it.
- **The pass ledger** (`plan · architect · 9 turns · 21.3 credits ✓ 05:23`) reads beautifully.

---

## 8. What I would fix first

1. §1.1 — wire `topup` into `runCardAction`. A one-line switch case; it is a hard dead end.
2. §1.2 — stop `inboxJump` clearing an unanswered question.
3. §3.1 — let the question wrap as far as it needs, or make it reachable.
4. §1.3 / §1.4 — make `captureAnswer`'s fallback a resolution, and point the blocked gate at
   the key that actually closes a user comment.
5. §1.5 — plain-words error, a durable blocker, and put the schema in front of the role that
   writes the block.
6. §5.1 / §5.2 — finish the two vocabulary sweeps round 2 started, on the surfaces that
   matter most.
7. §2.1 / §2.2 / §2.3 / §2.4 — a key that is named must work, and the help must fit.

---

## Appendix — how to reproduce this rig

```sh
cd $SCRATCH/rig/ledger      # a 6-file Go CLI (main.go, entry.go, entry_test.go,
                            # Makefile, ledger.txt, README.md), git init, one commit
git config user.name "Dev Rig" && git config user.email dev@example.com
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

The rig's planted bug is `Filter`'s case-sensitive `==` (`ledger sum -category food` finds
one of three food lines); the planted feature gap is that `list` has no sort flag. Keep the
base branch as `master` — three of this review's findings only appear there.

Note: `init`'s default profile is `thrifty` with **no `backend:`**, which falls through to
copilot. A first-time user with no copilot login gets a profile that cannot run and nothing
at creation time says so — `gummi doctor` is the only surface that mentions it.

---

## What was changed

Every finding above is fixed. `go build ./...`, `go vet ./...` and `go test ./...` are green,
and the behaviour was re-checked on a 120×40 pty against the rebuilt binary. Where a fix
changed a contract, the test pinning the old one was rewritten with the reason in its doc
comment rather than deleted.

### The four that trapped a user

| finding | change |
|---|---|
| §1.1 the top-up row was a no-op | `runCardAction` (boardactions.go) grew the `topup` case it never had. The row is keyless by construction, so it fell past the `a.key != ""` shortcut and out of the bottom of a switch that knew `expand/duplicate/profile/ask/changes/gate/prlink/prunlink/prpull` — `return nil`, silently. `runCommand` had carried the case all along for the space menu; both now route to the one `topUpBudget` the inbox's `u` performs |
| §1.2 the inbox cleared what you had not answered | `inboxJump` no longer calls `inbox.remove`. Reading a question is not answering it, and every kind already has a real clearing path on the act itself — a gate on `Advance`, a question on the answer, a budget on the top-up, a failure on the retry. `x` is still "not now" |
| §1.3 answering filed a blocker against you | `captureAnswer`'s anchor-miss branch now writes `resolved — <answer> (recorded here: …)`. The fallback POSITION was right; its spelling was what `spec.Parse` read as a fresh open `@user` thread, which is exactly what shuts the approval gate. A new assertion in both ask-tool tests counts open user threads and requires zero |
| §1.4 the offered action could not clear the blocker | both blocked-gate rows now lead with `x resolves one, R sends them back to the agent`. `R` is the one key that provably cannot close a `@user` marker, and it was the only key the spec row named |
| §1.5 malformed gummi-checks | `checksShapeError` replaces the yaml/Go type error with the shape, and names the plain-string-list mistake specifically. The deeper fix is upstream: a new `checksShape` constant puts the `- name:` / `cmd:` schema into **both** section prompts, so the architect that rewrites the block has seen it — previously only `discoverPrompt` carried it, and only the scribe reads that |

### Keys and help

| finding | change |
|---|---|
| §2.1 `g` dead on the inbox | `inboxSuggestedKey` honours whatever the row printed, by construction: the same `suggestFor` the renderer reads decides what the key does, so the hint and the behaviour cannot drift |
| §2.2 `[alt+a]` refused its own mark | `cardCitationKey` applies the `m.cardEvents` cache to the row before resolving. `featureRow.Events` is populated in `threadView` **on a copy**, so a row out of `m.selected()` was always empty and `scrollThreadToEvent` always failed — the mark was generated from one source and resolved against another |
| §2.3 card help unreachable | `withHelpKey`'s row got `bar: true` and is now spliced *above* each table's last row, not appended after it. Appending made help the last row and pushed `esc` into the first slot the bar sheds, which is how a first pass at this replaced "esc board" with "alt+/ help" — second-to-last is where help belongs anyway. `helpKeyFor` also stops the command menu advertising `?` on the surface where `?` types |
| §2.4 help table wider than the terminal | `helpDialog.View` uses the `w` it always received: rows wrap under the text column, and the vertical window counts display lines rather than table rows so a wrapped row cannot push the last one off the bottom |

### Information that was unreachable

| finding | change |
|---|---|
| §3.1 two-line question cap | `pickerQuestionLines` 2 → 4. Four for four of this drive's questions overran two, and three of them lost the actual question |
| §3.2 clipped line reference | `renderStatus` reserves the `"  L<n>"` suffix out of the width *before* truncating the text into it. The row was `w+2` wide and the pane clipped the one part with no ellipsis to admit it |
| §3.3 titles cut at 60 | `maxTitleLen` 60 → 100. `maxSlugLen` is separate, so branch names are unchanged |
| §3.4 comment dialog showed no anchor | `commentDialog` carries and renders one (`on: <line>`), like `resolveDialog` always has; both call sites pass the cursor line. Field widened, limit 200 → 600 |
| §3.5 dropped mid-document | `firstBlockingComment` lands the artifact on the first open `@user` comment when there is one. A citation still outranks it — that is a place the reader asked for by name — and at todo, where a comment blocks nothing, nothing changes |

### Numbers, and the two vocabulary sweeps

- **§4.1** `roundLabel` names its loop (`⟲ 1 of 3 review rounds`), so the two same-shaped badges are
  distinguishable and the silent 2→3 denominator change across stages is legible. The help
  legend's single `⟲` entry says the badge names which loop it counts.
- **§4.2** `budgetSummary` appends `(over)` when spend exceeds the cap. The overshoot is real and
  expected; the masthead simply never said so.
- **§5.1 envelope → budget** on every surface that reports a stop: the narration, the decision
  title, the top-up row, the inbox footer, the dialog's own **title**, the top-up notice, the
  card help table, and the approval-time estimate line. The API keeps its names — `--envelope`,
  `GUMMI_ENVELOPE`, the JSON key, `Budget.Envelope` — because scripts call those.
- **§5.2 the base branch**, resolved rather than assumed, in the three places round 2 missed:
  `nextInput.base` (new field, filled at assembly) for the landing row, `gateReason`/
  `verifyGateReason` for the inbox, and all four `merge.go` notices. `baseBranchOf` is the
  id-keyed variant for callers holding no Feature.
- **§5.3** `g` is `next stage` in the footer and the inventory, matching the help table that
  already said it in words; `advance to verify` → `start verify`.
- **§5.4** `esc board`, both ends of the card page.
- **§5.5** `✉ 1 needs you`; `y / n`; `0 dependencies` / `keys · dependencies`; `comment on` rather
  than `annotate`; `Switch the agent tab's profile/model`; and the `(s)` sweep finished in the
  CLI and the engine (`cardPlural` in each), which round 2 did only in the TUI.
- **§5.6** the template-prompt group is headed *"prompts the stages will answer — nothing waiting
  on you"*.
- **§5.7** `gummi --help` uses one noun — **card** — and plain verbs: `Split a document into
  cards`, `Pick a parked card back up`, `Dump a card's worktree diff against its base branch`.
- **§6** the new-card footer names `tab/shift+tab`, so the `kind` row directly above the composer
  is one keystroke away instead of five.

### Three regression tests

`internal/ui/round3_repro_test.go` pins the defects whose repro otherwise needs a live stage:
the budget stop must offer a row that actually runs, an inbox row must honour the key it
prints, and a stretch's opening event must resolve from the cache the mark was generated
from. The first two fail on the pre-fix tree; the third asserts the resolver directly,
because `cardCitationKey` answers the chord either way.

### Not done

- **The malformed-checks notice is still transient.** It says the right thing now and names the
  fix, but it is a notice: no inbox item, no blocker row, gone on the next keypress, and the
  card still advances. Raising it as `attnFailure` would hijack the card's decision block into
  the failure arm ("try again — the session errored"), which is worse than the leak. A durable
  surface for "this card's verification apparatus is not wired up" is a design question, not a
  wording one.
- **The comment field is still single-line.** It is wider and holds 600 characters, and it now
  shows what it is attaching to, but `enter` is its submit gesture and a textarea would change
  that for every caller.
- **Credits still never state their rate.** The pass footers say `$0.23` and every other surface
  says credits; nothing on any screen connects the two. Picking one unit is a decision, not a
  sweep.
