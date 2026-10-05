# gummi web: test ids

Every region and control a test targets carries `data-testid` (Playwright's
`page.getByTestId`). Ids with `<…>` are filled per item. The page builds them
in `internal/web/assets/*.js`; keep this list in step when adding one.

## Shell

| id | what |
|---|---|
| `boot` | the "Opening the board…" placeholder |
| `app` | the board (hidden while pairing) |
| `topbar` | the header bar |
| `workspace`, `ws-repo`, `ws-head` | repo and head branch in the header |
| `pill-needs` | "N need you" — click opens the next card that needs you (`n`) |
| `pill-running`, `pill-today` | running count, today's spend |
| `conn` | connection pill; `data-state` = `connecting` \| `live` \| `reconnecting` (a dot alone at ≤ 1100px and on a phone, its words in its label) |
| `presence`, `viewer` | who is viewing; one `viewer` per person |
| `btn-palette`, `btn-keys`, `btn-theme` | ⌘K palette, `?` sheet, theme toggle (`data-theme` = current) |
| `rail-toggle` | compact/full rail (`[`), `aria-pressed` = full |
| `toasts`, `toast` | the toast stack and each toast |
| `toast-show`, `toast-cmds`, `toast-copy`, `toast-close` | on a toast that carries commands to run (a stack replay's pushes): unfold the commands, the commands themselves, copy them, dismiss it (it stays a minute, and while pointed at or focused) |

## Pairing

`pair`, `pair-form`, `pair-name` (`pair-name-hint` under it), `pair-code`, `pair-submit`, `pair-error`
(wrong code, with "N tries left"), `pair-note`, `pair-new` (print a new code),
`pair-refused` (the form shown again after a request was rejected or lapsed).

A browser paired while another has the board, with a code other than the one
printed at start, waits to be let in: `pending` (the waiting screen),
`pending-text`, `pending-expires` (m:ss left), `pending-error`,
`pending-cancel` (withdraw the request).

On a page at the board: `approvals-slot` (always present, hidden when nobody
waits), `approvals` (the request banner), `approval-<deviceId>` (one request),
and inside it `approval-person`, `approval-device`, `approval-source`,
`approval-via`, `approval-time`, `approval-ua`, `approval-approve`,
`approval-reject`; `approvals-live` (screen-reader line that says a new
request once).

## Rail

| id | what |
|---|---|
| `rail` | the rail landmark |
| `rail-filter` | filter box |
| `rail-kinds`, `rail-kind-<TAG>` | kind chips (`all`, `FD`, `BG`, `RS`, `FF`, `GL`) |
| `rail-repos`, `rail-repo-<name>` | repo chips (`all`, then each repo; `default` for the default repo); hidden unless the board spans more than one repo |
| `rail-row-repo-<ID>` | a card's repo chip; shown only when the board spans repos and the card is not in the default repo |
| `rail-cards` | the scrolling list |
| `rail-group-<status>` | `needs`, `running`, `paused`, `idle`, `todo`, `done` |
| `rail-row-<ID>` | a card row; `data-status`, `aria-current="true"` when open |
| `rail-more-done`, `rail-empty` | "Show all N" landed ("+N" on the compact rail); no cards / no match |
| `rail-filtered` | compact rail only: a filter still applies; opens the full rail |
| `rail-foot`, `rail-new-session`, `rail-new`, `rail-fleet`, `rail-more` | foot buttons (`rail-new-session` opens a session draft, `rail-new` the new-card form) |
| `rail-more-menu`, `menu-goals`, `menu-stacks`, `menu-ingest`, `menu-bugs`, `menu-doctor`, `menu-push`, `menu-unpair` | the More menu |

## Resume offer

`resume-slot` (always present), `resume-banner` (the quit-resume question),
`resume-text`, `resume-ago` (how long ago the quit was, kept current),
`resume-all`, `resume-choose`, `resume-card-<ID>` (a checkbox once
choosing), `resume-picked`, `resume-none` ("Not now").

## Notifications

`push-dialog`, `push-body`, `push-state` (`data-state` = `on` | `off` |
`denied` | `unsupported` | `insecure` | `unavailable` | `unknown`),
`push-on`, `push-off`, `push-error`.

## Card head

`card-head`, `card-kind`, `card-id`, `card-title`, `card-stages`,
`stage-<stage>` (a past stage jumps to it in the thread), `card-branch`, `card-scratch` (a research card: scratch tree, no branch),
`card-spend`, `card-context` (context-window meter, shown once the running
session reports a limit), `card-error`, `toggle-panel` (`]`), `card-actions` (menu),
`card-actions-menu`, `action-<id>` (menu item), `action-btn-<id>` (pause/resume
shown as a button), `action-dialog`, `action-question` (what it asks, or the
server's question (a 202) — verbatim, line breaks kept; an action that asks a
yes is sent bare first and never pre-confirmed, and its yes is the token the
question came with), `action-input` (the field its `needs` asks for,
prefilled with its default), `action-hint` (under a landing's or a squash's
message: where it came from and what it becomes, or — with a spinner, the box read-only and
`action-confirm` saying "Drafting…" — that gummi is drafting it), `action-card-<ID>` (a card in the dependency
picker), `action-cards-filter`, `action-error` (the board's refusal, in the
dialog), `action-confirm`, `action-cancel`, `land-method` (the squash ⇄ merge-commit
choice beside a landing's message, in the menu's dialog and in the verify
answer's landing dialog, where a card may keep its commits; its radios are
`land-method-squash` and `land-method-merge`).

## Thread

| id | what |
|---|---|
| `conversation` | the centre landmark |
| `thread` | the scroller |
| `thread-items` | the polite live region holding items |
| `stage-group-<stage>` | a stage segment (`<details>`; `open` when unfolded; the current one has class `cur`) |
| `thread-item` | an item without a more specific id; `data-key`, `data-type` on every item |
| `receipt`, `verify` (`verify-avatar` its avatar), `check-<name>`, `activity` (a run of tool calls, folded), `stretch`, `thread-decision`, `thread-consult` (a consult question or answer, where it was asked) | item kinds |
| `verify-no-checks` | a verify item's head when gummi had no gummi-checks to run (never "all passed") |
| `thread-loading`, `thread-empty`, `thread-unavailable`, `thread-error` | empty states |
| `thread-live` | the live block's container |
| `live`, `live-streaming` (its `live-interrupted` mark when the session stopped mid-message), `live-elsewhere`, `live-error`, `live-consult`, `live-consult-head`, `live-consult-notice` (the consult runs on a backend that cannot confine it), `live-freeform`, `live-freeform-head` | live block parts |
| `thinking`, `tasks` | the agent's reasoning, folded; its pinned checklist |

## Decision and composer

| id | what |
|---|---|
| `decision-slot`, `decision` | the pinned decision (`data-kind`, `data-ref`) |
| `decision-question`, `decision-against`, `decision-more`, `decision-jump` | its parts |
| `decision-option-<optionId>` | a numbered answer (`aria-pressed` = highlighted; on a multi-pick question, picked) |
| `decision-note` and its kinds `decision-answered`, `decision-moved`, `decision-needs`, `decision-busy`, `decision-newcard`, `decision-error`, `decision-pick` | what happened to the last answer (a 409 said who got there first or that the card moved; a 202 question what it still needs; enter with nothing highlighted) |
| `decision-confirm`, `decision-confirm-question`, `decision-confirm-yes`, `decision-confirm-no` | a confirmation an answer's flow asked for (a 202 `confirm` question), drawn beside the decision (never inside its capped box) and focused when it appears; the question is the server's, verbatim (line breaks kept); yes sends the answer again with the token it came with as `confirm` |
| `decision-carry` | "+ N diff comments" on an answer that takes them |
| `nextup-slot`, `nextup` | "X also needs you" chip after an answer (on a phone's document tab, in the docked bar) |
| `dock`, `composer`, `composer-input`, `composer-says`, `composer-send` | composer; `composer-says` is the enter line (the server's reading of the line, or the decision's answer) |
| `composer-model`, `model-picker-btn` | a session's model beside Send (a draft's, or an open session's); absent on a card in the workflow |
| `model-picker`, `model-search`, `model-<agent>-<model>`, `model-typed-<agent>`, `model-recent-<agent>-<model>` | the picker: search or type an id; a suggested model (`default` for the agent's own), a typed id, a recent pair |
| `composer-draft`, `draft-repo`, `draft-base`, `draft-budget`, `draft-budget-pop`, `draft-budget-<n>`, `draft-budget-input`, `draft-budget-set`, `draft-budget-error` | a session draft's row above the line: repository, base, budget (`0` is uncapped) |
| `draft-hero`, `draft-starter`, `draft-cancel` | a session draft's empty conversation, its starter lines, and leaving the draft |
| `composer-note` | a line about the last send: handed back mid-turn, or refused |

## Panel

| id | what |
|---|---|
| `panel` | the right landmark |
| `panel-tabs`, `tab-memory`, `tab-spec`, `tab-diff`, `tab-log`, `tab-pr`, `tab-stats` | tabs (`aria-selected`) |
| `panel-close` | hide the panel |
| `panel-pane` | the tab body; `data-tab` = open tab |
| `panel-loading`, `panel-unavailable`, `panel-error` | tab states (unavailable = the route answers 501) |
| `panel-draft` | the pane under a new session's draft (no tabs: nothing has documents yet) |
| `resizer` | panel resizer (drag, ←/→, double-click resets) |

Spec: `spec`, `spec-toc`, `spec-toc-<i>`, `spec-doc`, `spec-src`, `spec-title`,
`spec-section-<i>`, `spec-comment-<i>`, `spec-note-draft`, `spec-note-input`,
`spec-note-save`, `spec-note`, `spec-prompt` (gummi's own `%%` prompts),
`spec-note-hint` (a note is one line: line breaks become spaces),
`spec-closed` (a done or landed card's spec takes no more notes, and offers
no Comment, Resolve or Request changes),
`spec-note-resolve`, `spec-note-resolution` (a resolution marker, folded onto
the note above it: "resolved by" when it closes it, "answered by" when an
agent answered a person's note that only a person can close), `spec-checks`, `spec-check-<name>`, `spec-pending` (how many notes hold the
gate), `spec-request-changes` (sends them to the stage that owns them: the TUI's R), `spec-none`.

Memory: `memory`, `memory-src` (where the files live), `memory-global`,
`memory-plan`, `memory-dead-ends` (a document's title and its rendered
markdown; nothing written yet reads as a placeholder), `memory-none`
(why a card has none — the workflow-card answer).

Request changes that sends a card back to an earlier stage asks first:
`changes-confirm` (the dialog), `changes-question`, `changes-go`,
`changes-cancel`, `changes-error`.

Diff: `diff-head`, `diff-rev`, `diff-add`, `diff-del` (totals of the files
listed: all, or only those changed since), `diff-since`, `diff-since-all`, `diff-since-new`,
`diff-fresh` (branch moved banner), `diff-fresh-show`, `diff-pending`,
`diff-request-changes` (sends the open comments to the implementer, or with an
earlier stage's spec note back to it: the TUI's R),
`diff-files`, `diff-file-<i>`, `diff-filebox-<i>`, `diff-viewed-<i>`,
`diff-line-<idx>` (idx = raw diff line index, the annotation coordinate; click
`.o`/`.n` to comment), `diff-unfold-<i>` ("Show N lines" on a file folded
because it is large, or the diff already drew many lines; a file with an open
comment is never folded, and its lines have no `diff-line-*` until unfolded),
`diff-orphans` (a file's comments whose line is gone; each says so in
`annotation-gone-<id>`), `diff-other` (comments on files the diff does not
show — a pulled PR thread on an untouched file, a deleted file's — above the
files, and under `diff-none` too; `annotation-where-<id>` names the file),
`annotation-<id>`, `annotation-edit-<id>` (not on a pulled PR thread),
`annotation-edit-input-<id>`, `annotation-edit-save-<id>`,
`annotation-resolve-<id>`, `annotation-delete-<id>`, `annotation-draft`,
`annotation-input`, `annotation-save`, `annotation-adrift` (an unsent comment
whose line the moved diff no longer has), `diff-none`. A done or landed card's
line numbers open no comment box.

PR: `pr`, `pr-state`, `pr-open` (open threads), `pr-fetched`, `pr-refresh`,
`pr-pull`, `pr-thread-<i>`, `pr-thread-show-<i>` (opens the diff at the
thread's file and line), `pr-push` (what to push and when, or why there is
nothing to push), `pr-push-cmd`, `pr-push-copy`, `pr-none`, `pr-link` (link a pull
request, when the card's menu offers it).

Log: `log`, `log-head`, `log-why` (why the history is read-only),
`log-none`, `log-commit-<i>` (oldest first), `log-show-<i>` (a commit's
changes, in `log-patch`; a large file there folds behind `log-unfold-<i>`),
`log-reword-<i>`, `log-editor-<i>`,
`log-editor-save-<i>`, `log-squash-<i>` (`aria-pressed`), `log-plan` (the
draft's bar), `log-plan-line` (what the dry run says), `log-confirm`
(rewriting pushed commits), `log-confirm-cmd`, `log-confirm-copy`,
`log-reset`, `log-apply`, `log-push`, `log-push-cmd` (the force push a
rewrite of pushed commits leaves), `log-push-copy`.

Stats: `stats`, `stats-spent`, `stats-left` (a session's), `stats-passes`,
`stats-rework`, `stats-models` (a session's), `stats-table`, `stats-none`,
`stats-bars` (`-stage`, `-role`, `-model`), `stats-redo`, `stats-clock`
(`-agent`, `-you`, `-idle`, its segments), `stats-hands`, `stats-tools`,
`stats-no-tools` (the backend reports no tool calls), `stats-checks`,
`stats-judgment` (`stats-gates`, `stats-asks`), `stats-envelope`.

## Phone (≤ 760px)

`mobile-card` (a card's screen bar: hidden over the cards), `card-back` (back
to the cards), `card-back-needs` (its count of cards that need you),
`tab-thread` (the card's thread, first in its tab row; `aria-selected` on the
shown one), `mdec-card` (the card the docked decision bar is for, over the
cards), `mobile-decision` (docked decision bar), `mdec-toggle`,
`mdec-option-<optionId>`, `mdec-note`, `mdec-confirm`, `mdec-confirm-question`,
`mdec-confirm-yes`, `mdec-confirm-no` (the bar's own copy of the confirmation).

## Overlays and views

| id | what |
|---|---|
| `scrim` | any overlay's backdrop |
| `palette`, `palette-input`, `palette-results`, `palette-row-<ID>`, `palette-view-<name>` (a view, or `newsession`, `push`) | ⌘K |
| `keys-help` | the `?` sheet |
| `menu` | a popup menu opened with `openMenu` (its `testid` option overrides `menu`, e.g. `rail-more-menu`, `card-actions-menu`) |
| `modal`, `modal-close` | a dialog opened with `openModal` (its `testid` option overrides `modal`) |
| `view-<name>`, `view-<name>-body` | a registered view (`newcard`, `fleet`, `goals`, `goal`, `stacks`, `ingest`, `bugs`, `doctor`) |
| `not-available` | body of a view nobody registered yet |
| `unpair-dialog`, `unpair-confirm` | unpairing |

## Goals (`views/goals.js`)
List: `goals-list`, `goals-count`, `goals-new` (toggles the form), `goals-empty`,
`goals-error`, `goal-row-<ID>` (`data-state` = the goal's one word: `todo`,
`agreeing the plan`, `running`, `wrapping up`, `ready for you`, `done`),
`goal-row-met`, `goal-row-landed`, `goal-row-spent`.
Form: `goal-form-slot`, `goal-form`, `goal-form-desc`, `goal-form-budget`,
`goal-form-profile`, `goal-form-after` (continues another goal),
`goal-form-refs` (one path per line), `goal-form-autopilot`,
`goal-form-error`, `goal-form-cancel`, `goal-form-submit`.
Page: `goal-head`, `goal-back`, `goal-id`, `goal-title`, `goal-state`,
`goal-met`, `goal-after`, `goal-open-card`, `goal-partial`; callouts
`goal-plan`, `goal-plan-open`, `goal-needs-budget`, `goal-needs-substrate`,
`goal-needs-owner`, `goal-waiting-on`, `goal-unread`; `goal-gone` (the goal was deleted while its page was open), `goal-error` (the goal
page did not load, with why).
Verbs: `goal-actions`, `goal-action-<id>` (`note`, `budget`, `substrate`,
`topup`, `stop`, `sendback`, `reverse`, `land`, `abandon`), `goal-action-slot`,
`goal-panel-<id>` (the inline confirmation), `goal-action-input`,
`goal-action-runs`, `goal-action-minutes`, `goal-action-ref`,
`goal-action-why`, `goal-action-error`, `goal-action-cancel`,
`goal-action-confirm`.
Ledger: `goal-ledger`, `ledger-envelope`, `ledger-total`,
`ledger-seg-<own|held|reserve|left>`, `ledger-goal-spend`, `ledger-held`,
`ledger-card-spend`, `ledger-reserve`, `ledger-left`, `ledger-substrate`.
Sections: `goal-done-when`, `done-when-<DW-N>` (`data-status`), `goal-cards`,
`goal-card-<ID>` (`data-state`; click selects the card and closes the view),
`goal-decisions`, `goal-decision-<D-N>`, `goal-reverse-<D-N>`,
`goal-declined`, `goal-found`, `goal-notebook`, `goal-reference`,
`goal-finding-<ref>`, `goal-try-it`, `goal-log`, `goal-log-entry`
(`data-action`), `goal-log-more`.

## Stacks (`views/stacks.js`)
`stacks-list`, `stacks-count`, `stacks-new`, `stacks-empty`, `stacks-error`;
new stack form `stack-form`, `stack-form-card`, `stack-form-name`,
`stack-form-error`, `stack-form-cancel`, `stack-form-submit`.
One stack: `stack-<stackId>` (class `focus` when opened on it), `stack-name`,
`stack-restack`, `stack-add`, `stack-rename`, `stack-delete` (empty stacks
only); `stack-add-form`, `stack-add-card`, `stack-add-pos`, `stack-add-submit`;
`stack-rename-form`, `stack-rename-input`, `stack-rename-save`;
`stack-remove-dialog`/`-cancel`/`-confirm`, `stack-delete-dialog`/`-cancel`/`-confirm`.
Chain: `stack-chain`, `stack-base`, `stack-member-<ID>` (`data-pos`, and
`data-stale`, `data-landed`, `data-running`, `data-dirty` when set),
`stack-open-<ID>`, `member-branch`, `member-blocker`, `marker-landed`, `marker-handedoff`,
`marker-stale`, `marker-running`, `marker-dirty`, `marker-notree`,
`marker-needs`, `stack-up-<ID>` (toward the base), `stack-down-<ID>`,
`stack-remove-<ID>`.
Restack answer: `stack-result`, `stack-result-close`, `stack-replayed`,
`stack-conflict`, `stack-waiting`, `stack-push`, `stack-push-line`,
`stack-push-copy`, `stack-push-copy-all`.
Card head: `card-goal` (opens the goal page — the card's goal, or on a goal's
own card its page), `card-stack` (opens the stacks view on the card's stack).

## Schedules (`views/schedules.js`)
`schedules-list`, `schedules-count`, `schedules-new`, `schedules-empty`,
`schedules-notice`; form `schedule-form` (create and edit — the same one),
`schedule-form-name`, `schedule-form-kind-mint`/`-heartbeat`,
`schedule-form-kind-fixed` (an edit's read-only kind),
`schedule-form-target`, `schedule-form-repo`, `schedule-form-backend`,
`schedule-form-model`, `schedule-form-envelope`, `schedule-form-every`,
`schedule-form-cron`, `schedule-form-tz`, `schedule-form-prompt`,
`schedule-form-preview` (`-cron`, `-fires`, `-error` and
`schedule-form-envelope-hint` inside it), `schedule-form-error`,
`schedule-form-error-detail`, `schedule-form-cancel`, `schedule-form-submit`.
One row: `schedule-<id>` (`data-enabled`), `schedule-<id>-state`,
`schedule-<id>-next`, `schedule-<id>-status`, `schedule-<id>-orphan`,
`schedule-<id>-toggle`, `schedule-<id>-edit`, `schedule-<id>-run`,
`schedule-<id>-rm`,
`schedule-<id>-confirm` (`-yes`/`-no` via the shared strip),
`schedule-<id>-result`.

## Tool views
Shared by the views below: `view-error` (a refusal, as the server said it),
`<prefix>-confirm`, `<prefix>-confirm-yes`, `<prefix>-confirm-no` (an inline
confirm strip), `<prefix>-<ID>` on each card link a write made — the list itself
is `<prefix>` (`ingest-created`, `bugs-created`), `created` / `created-<ID>` when
a view names no prefix.
Import spec (`ingest`): `ingest-form`, `ingest-source` (the paste/path
switch), `ingest-source-paste`,
`ingest-source-path`, `ingest-name`, `ingest-markdown`, `ingest-path`,
`ingest-profile`, `ingest-repo` (only with more than one repository),
`ingest-envelope`, `ingest-start`; running: `ingest-running`, `ingest-steps`,
`ingest-step`, `ingest-busy`; review: `ingest-review`, `ingest-kept` ("N
kept"), `ingest-coverage`, `ingest-unmapped-count`, `ingest-unmapped`,
`ingest-proposals`, `ingest-proposal-<i>` (`data-dropped`), `ingest-title`,
`ingest-rename-<i>`, `ingest-oneliner-<i>`, `ingest-merge-<i>` (not on the
first), `ingest-drop-<i>`, `ingest-undrop-<i>`, `ingest-edit-rename`,
`ingest-edit-oneLiner`, `ingest-edit-save`, `ingest-approve`,
`ingest-confirm` (+ `-yes`/`-no`), `ingest-confirm-unmapped`,
`ingest-discard`, `ingest-discard-confirm`; after: `ingest-done`
(`data-state`), `ingest-failed`, `ingest-created`, `ingest-created-<ID>`,
`ingest-again`.
Import bugs (`bugs`): `bugs-filters`, `bugs-repo`, `bugs-label`,
`bugs-state`, `bugs-limit`, `bugs-fetch`, `bugs-loading`, `bugs-gh-error`
(gh's own failure), `bugs-empty`, `bugs-all`, `bugs-list`,
`bugs-issue-<number>`, `bugs-pick-<number>`, `bug-author` (who opened it),
`bugs-onboard-<number>` (already on the board), `bugs-import`, `bugs-result`, `bugs-created`,
`bugs-created-<ID>`, `bugs-missing`.
Doctor (`doctor`): `doctor-ready` (`data-ready`), `doctor-rerun`,
`doctor-deep`, `doctor-deep-confirm` (+ `-yes`/`-no`), `doctor-checks`,
`doctor-check-<name>` (`data-status` = `ok` | `warn` | `fail` | `unknown`),
`doctor-loading`.
Fleet stats (`fleet`): `fleet`, `fleet-window` (the window switch),
`fleet-window-24h` / `-7d` / `-30d` / `-all`
(`aria-pressed`), `fleet-span`, `fleet-headline`, `fleet-spent`, `fleet-tokens`, `fleet-busiest`,
`fleet-estimated`, `fleet-alltime`, `fleet-rework`, `fleet-lanes`,
`fleet-clock`, `fleet-clock-agent` / `-you` / `-idle`, `fleet-by-stage`,
`fleet-stage-bars`, `fleet-by-model`, `fleet-model-bars`, `fleet-timeline`,
`fleet-timeline-scroll`, `fleet-legend`, `fleet-lane-<ID>` (marks inside:
`.blk.st-<stage>` a session, `.open` still running; `.wait` / `.wait.open`
on you; `.gate`; `.land`), `fleet-tip`, `fleet-empty`, `fleet-error`.
New card (`view-newcard`): `newcard-loading`, `newcard-form`, `newcard-kinds`,
`newcard-kind-<value>` (`feature`, `bug`, `research`, `research-diagnosis`,
`goal`, `freeform`), `newcard-about`, `newcard-title`, `newcard-desc`,
`newcard-severity`, `newcard-repro`, `newcard-expected`, `newcard-actual`,
`newcard-profile`, `newcard-envelope`, `newcard-repo`, `newcard-base`
(`newcard-base-hint` under it),
`newcard-adopt`, `newcard-stack`, `newcard-after`, `newcard-after-<ID>`,
`newcard-error` (a refusal no field owns), `newcard-error-<field>` (beside
its field: `title`, `desc`, `envelope`, `profile`, `severity`, `repo`,
`base`, `adopt`, `stack`, `after`), `newcard-create`, `newcard-autopilot`,
`newcard-cancel`.
