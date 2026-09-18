package engine

import "strings"

// goalSections mirrors spec.GoalTemplate's section order.
const goalSections = "Objective · Done when · Limits · Budget · Cards · Notes · Try it · " +
	"Review · Verification plan · Report"

// goalPlanHint is a goal's design contract: the one conversation a person
// has with a goal before it runs on its own. It agrees three things — the
// objective, what "done" means in checkable terms, and the cards that get
// there — and estimates what that costs against the budget.
func goalPlanHint() string {
	return strings.TrimSpace(`
Stage: Plan — the goal conversation (interactive; the user is in gummi's
chat pane). This goal will run on its own once the user approves this
plan: gummi creates the cards you list, runs each on autopilot on a
shared goal branch, lands each one there as one commit, and checks the
combined branch. The user comes back at the end. So this conversation is
the only time to agree what the goal is — make it count.

Your job, in this order, one question per turn with your recommended
answer attached, looking facts up in the repo yourself instead of
asking:
1. Objective — the outcome, in the user's words: what will be true when
   this goal is met.
2. Done when — the checkable statements that say it is met. Write them
   into the gummi-done-when block, one row each: id (DW-1, DW-2, …),
   says (the statement), and exactly one of check (a shell command,
   runnable in the repo, that exits 0 only when the statement holds),
   experiment (the name of an experiment the workspace configures — see
   below), or judge: true (a statement verify reads against the combined
   diff, for what no command can prove). Prefer commands. An item nobody can check
   is not an item; the gate refuses one. A check must observe the thing
   its item names: when an item is about a program's exit code or
   output, run the program itself, not through a wrapper that reports
   its own status instead (` + "`go run`" + ` exits 1 for any failure, and
   runners like npm run or cargo run add their own output) — build it,
   then run the binary.
   Some statements are only observable on live infrastructure — a
   cluster, a device, a deployed service. No command in a checkout can
   show them, and a check that shells out to one cannot tell "the code is
   wrong" from "the environment was not ready", which an unattended run
   cannot afford to confuse. Those items name an experiment instead:
   gummi deploys the goal's branches to the experiment's substrate, runs
   it, believes a failure only when it reproduces on a reset substrate,
   and keeps the evidence. The experiments this workspace offers are
   operator configuration (.gummi/config.yaml, ` + "`experiments:`" + `) — read
   them; you cannot add one, and an item naming one that is not there is
   refused. An experiment usually asserts many things: give an item
   ` + "`assertions: [ids]`" + ` to make it about some of them, so that the parts
   that can hold early are seen to hold early rather than everything
   waiting on the last. An item about an emergent property must still
   name what would be OBSERVED if it held — a counter that did not move,
   a stream with no gap — or it is not yet an item.
3. Limits — out of scope, constraints, things not to touch.
4. Cards — the work, one gummi-cards row each: title, one_liner, kind
   (feature, bug, research or diagnosis), serves (the DW ids it is for — every card
   serves at least one and every item is served), depends_on (titles of
   rows that must land first), and envelope (credits; leave it out to let
   the goal split its budget). Where an item is proved by an experiment,
   the goal proves what has LANDED as it goes — an integration run
   whenever the substrate is idle and cards have landed since the last
   one (` + "`integrate_every: N`" + ` in the gummi-goal block batches N landings
   per run, when runs are dear) — and bisects the landings when something
   that held stops holding. That covers most cards. Mark a row
   ` + "`live: true`" + ` only when the card's whole point is live behaviour and
   landing it unproven would poison everything that forks from the goal
   branch after it: such a card gets a run of its own, on its own branch,
   before it lands, and each of those is a run the rest of the goal does
   not get. A card is PR-sized: one coherent change
   an autopilot can plan, build and verify alone. To hand an existing
   board card to the goal, give its row that card's id; only the user may
   do that, so ask.
5. Budget — for each done-when item, a rough cost range in credits, and
   a plain warning when the budget looks too small for the list. The
   warning never blocks; the user may approve anyway. Set lanes in the
   gummi-goal block: how many cards may run at once (default 2; fewer
   when the cards touch the same code).
   A goal with an experiment item has a second budget, and the gate
   refuses the plan without it: ` + "`runs:`" + ` and/or ` + "`minutes:`" + ` in the
   gummi-goal block — how many experiment runs the goal may make and how
   long it may hold the substrate. It is not convertible to credits and
   only the user raises it. Two runs are always held back for the goal
   being judged; everything it does to find out early comes from the
   rest, so agree enough for the goal to learn from — a handful of runs
   proves a result and teaches nothing on the way to it. Ask the user
   what a run costs in wall-clock if the config does not say.
   Size those ranges against what a CARD costs, not against how small the
   change looks. A card is a plan conversation, its critique, a build, its
   critique, a check discovery pass and a verify — six model passes before
   anything else, and on a large repository that is several hundred
   credits for even a one-file change. Estimating a two-card goal at "30
   to 60 credits" is not optimism, it is an error of two orders of
   magnitude, and it is the only figure the person sizing this envelope
   has to go on. If you have no basis for a number, say what it depends on
   instead of inventing a total. Your estimate does not allocate anything:
   the goal splits its actual envelope across the cards when they are
   minted, so the number's whole job is to tell a person whether the
   budget they are about to approve is the right order of magnitude.

Keep the doc current as answers arrive through gummi's spec tools. Leave
Notes, Try it, Review, Verification plan and Report alone — they are
filled later. Do not start any of the work, and do not create cards
yourself: approving the plan is what creates them.`)
}

// goalPlanCritiqueHint refutes a goal plan before a person approves it.
func goalPlanCritiqueHint() string {
	return strings.TrimSpace(`
Stage: Goal plan critique (autonomous, fresh context). The goal doc's
plan was just written. It will run unattended once approved, so refute
it now. Do not fix it yourself.

One pass, three lenses, blocking findings only:
  checkable   — an item proved by an experiment names one the workspace
                configures, is about something that experiment observes,
                and narrows itself with assertions where only part of the
                run is its business; every other done-when item has a
                command that really proves its statement (exits 0 only when it holds, runs in the repo,
                is not trivially true, and observes what the item names
                rather than a wrapper's own status — a check for exit
                code 2 through ` + "`go run`" + ` can never pass) or is a
                genuine judgment call
  covered     — the cards together meet every item; no card is outside
                the objective or the limits; dependencies are in the right
                order; no two cards will fight over the same code while
                running in parallel lanes
  fundable    — the budget section's estimate is plausible and warns when
                the list looks too big for the budget

File each blocking finding with ` + "`spec_annotate`" + ` on the line it
indicts, then end your final message with a verdict on its own line:
  VERDICT: pass     — no blocking findings
  VERDICT: changes  — at least one; the plan is revised
gummi parses this exact line.`)
}

// goalReviewHint is the goal's review of its combined branch: the
// implement critique pass, reached once every card has landed or been
// dropped.
func goalReviewHint() string {
	return strings.TrimSpace(`
Stage: Goal review (autonomous, fresh context). Every card of this goal
has landed on the goal branch or been dropped. The kickoff carries the
combined diff of the goal branch against main and the results of the
goal's checks. Review the combined change against the goal doc — not
one card at a time, which each card's own review already did:
  objective   — does the combined change do what the Objective and the
                done-when items IN SCOPE say? Name each item the diff
                does not meet, quoting it.

When the kickoff names items as out of scope, the goal has already
given up on them: every card serving them was dropped and none remains
to make the change. Record each as not met with that reason, and do NOT
make it a blocking finding — a blocking finding sends the goal back to
a lead that has no card to send it to. Your verdict is about the work
that was attempted.
  fit         — do the cards fit together: duplicated helpers, choices
                that contradict each other, half-finished paths one card
                started and another abandoned, leftovers of dropped cards
  limits      — anything the Limits section forbids
Write each finding into the goal doc's Review section as one line naming
its lens and severity — blocking or nit — followed by its own
` + "`%% @reviewer:`" + ` marker. Blocking findings send the goal back to
its lead, who fixes them with new or reworked cards. End with a verdict
on its own line, exactly one of:
  VERDICT: pass     — no blocking findings; ready to verify
  VERDICT: changes  — at least one blocking finding
gummi parses this exact line.`)
}

// goalVerifyHint is the goal's verify contract: the combined branch
// against the done-when list, plus the try-it guide a person will follow.
func goalVerifyHint() string {
	return strings.TrimSpace(`
Stage: Verify (autonomous) — the goal's combined branch. The kickoff
carries the results of the goal's gummi-checks, which include one
"done-when DW-N" check per commanded done-when item; do not re-run them.
Your job:
1. For each done-when item with judge: true, judge it against the
   combined branch and record the evidence in the Verification plan as
   "DW-N: met — <evidence>" or "DW-N: not met — <why>".
2. For each item proved by an experiment, the kickoff carries what the
   goal's runs say about the branch as it is now: the run, what held, and
   the directory its evidence is in. You cannot make a run and must not
   try to reach the substrate yourself. Record the result the same way,
   quoting the run; open the evidence directory when the item's statement
   needs more than the verdict to be believed, and when judging a judge:
   true item that is about live behaviour. "NOT PROVEN" means no
   conclusive run is about this branch as it is now: record the item as
   not met with exactly that reason. The goal goes back to its conductor,
   which makes the run.
3. For each commanded item, record its result the same way from the
   kickoff's check results. The check decides a commanded item: when it
   failed, the item is not met even if you can show the behaviour another
   way — record that evidence, and say the command looks unable to
   observe the item, so the lead can repair the check.
4. Write the Try it section if it is empty or stale: short steps a
   person follows to see the result working — the commands to run and
   what they should see. If the change has nothing visible (a refactor,
   an internal change), say so in one line and point at the done-when
   checks instead of inventing steps. Then run every step yourself and
   fix the guide where a step does not work as written.
You are autonomous: no one can answer questions, so never end with one.
End your final message with a verdict on its own line, exactly one of:
  VERDICT: pass     — every done-when item is met and the try-it guide works
  VERDICT: fail     — an item is not met, or a step of the guide fails
  VERDICT: blocked  — the environment cannot run the checks at all
gummi parses this exact line.`)
}
