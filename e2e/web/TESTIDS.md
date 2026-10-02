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
| `conn` | connection pill; `data-state` = `connecting` \| `live` \| `reconnecting` |
| `presence`, `viewer` | who is viewing; one `viewer` per person |
| `btn-palette`, `btn-keys`, `btn-theme` | ⌘K palette, `?` sheet, theme toggle (`data-theme` = current) |
| `rail-toggle` | compact/full rail (`[`), `aria-pressed` = full |
| `toasts`, `toast` | the toast stack and each toast |
| `toast-show`, `toast-cmds`, `toast-copy`, `toast-close` | on a toast that carries commands to run (a stack replay's pushes): unfold the commands, the commands themselves, copy them, dismiss it (it stays a minute, and while pointed at or focused) |

## Pairing

`pair`, `pair-form`, `pair-name`, `pair-code`, `pair-submit`, `pair-error`
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
`approval-reject`.

## Rail

| id | what |
|---|---|
| `rail` | the rail landmark |
| `rail-filter` | filter box |
| `rail-kinds`, `rail-kind-<TAG>` | kind chips (`all`, `FD`, `BG`, `RS`, `FF`, `GL`) |
| `rail-cards` | the scrolling list |
| `rail-group-<status>` | `needs`, `running`, `paused`, `idle`, `todo`, `done` |
| `rail-row-<ID>` | a card row; `data-status`, `aria-current="true"` when open |
| `rail-more-done`, `rail-empty` | "Show all N" landed; no cards / no match |
| `rail-foot`, `rail-new-session`, `rail-new`, `rail-agent`, `rail-fleet`, `rail-more` | foot buttons (`rail-new-session` opens a session draft, `rail-new` the new-card form) |
| `rail-more-menu`, `menu-goals`, `menu-stacks`, `menu-ingest`, `menu-bugs`, `menu-doctor`, `menu-push`, `menu-unpair` | the More menu |

## Resume offer

`resume-slot` (always present), `resume-banner` (the quit-resume question),
`resume-text`, `resume-all`, `resume-choose`, `resume-card-<ID>` (a checkbox
once choosing), `resume-picked`, `resume-none` ("Not now").

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
dialog), `action-confirm`, `action-cancel`.

## Thread

| id | what |
|---|---|
| `conversation` | the centre landmark |
| `thread` | the scroller |
| `thread-items` | the polite live region holding items |
| `stage-group-<stage>` | a stage segment (`<details>`; `open` when unfolded; the current one has class `cur`) |
| `thread-item` | an item without a more specific id; `data-key`, `data-type` on every item |
| `receipt`, `verify` (`verify-avatar` its avatar), `check-<name>`, `tool-group`, `stretch`, `thread-decision`, `thread-consult` (a consult question or answer, where it was asked) | item kinds |
| `verify-no-checks` | a verify item's head when gummi had no gummi-checks to run (never "all passed") |
| `thread-loading`, `thread-empty`, `thread-unavailable`, `thread-error` | empty states |
| `thread-live` | the live block's container |
| `live`, `live-streaming`, `live-elsewhere`, `live-error`, `live-consult`, `live-consult-head`, `live-freeform`, `live-freeform-head` | live block parts |

## Decision and composer

| id | what |
|---|---|
| `decision-slot`, `decision` | the pinned decision (`data-kind`, `data-ref`) |
| `decision-question`, `decision-against`, `decision-more`, `decision-jump` | its parts |
| `decision-option-<optionId>` | a numbered answer (`aria-pressed` = highlighted; on a multi-pick question, picked) |
| `decision-note` and its kinds `decision-answered`, `decision-moved`, `decision-needs`, `decision-busy`, `decision-newcard`, `decision-error` | what happened to the last answer (a 409 said who got there first or that the card moved; a 202 question what it still needs) |
| `decision-confirm`, `decision-confirm-question`, `decision-confirm-yes`, `decision-confirm-no` | a confirmation an answer's flow asked for (a 202 `confirm` question), drawn beside the decision (never inside its capped box) and focused when it appears; the question is the server's, verbatim (line breaks kept); yes sends the answer again with the token it came with as `confirm` |
| `decision-carry` | "+ N diff comments" on an answer that takes them |
| `nextup-slot`, `nextup` | "X also needs you" chip after an answer |
| `dock`, `composer`, `composer-input`, `composer-says`, `composer-send` | composer; `composer-says` is the enter line (the server's reading of the line, or the decision's answer) |
| `composer-model`, `model-picker-btn` | a session's model beside Send (a draft's, or an open session's); absent on a card in the workflow |
| `model-picker`, `model-search`, `model-<agent>-<model>`, `model-typed-<agent>`, `model-recent-<agent>-<model>` | the picker: search or type an id; a suggested model (`default` for the agent's own), a typed id, a recent pair |
| `composer-draft`, `draft-repo`, `draft-base`, `draft-budget`, `draft-budget-pop`, `draft-budget-<n>`, `draft-budget-input` | a session draft's row above the line: repository, base, budget (`0` is uncapped) |
| `draft-hero`, `draft-starter`, `draft-cancel` | a session draft's empty conversation, its starter lines, and leaving the draft |
| `composer-note` | a line about the last send: handed back mid-turn, or refused |

## Panel

| id | what |
|---|---|
| `panel` | the right landmark |
| `panel-tabs`, `tab-spec`, `tab-diff`, `tab-log`, `tab-pr`, `tab-stats` | tabs (`aria-selected`) |
| `panel-close` | hide the panel |
| `panel-pane` | the tab body; `data-tab` = open tab |
| `panel-loading`, `panel-unavailable`, `panel-error` | tab states (unavailable = the route answers 501) |
| `resizer` | panel resizer (drag, ←/→, double-click resets) |

Spec: `spec`, `spec-toc`, `spec-toc-<i>`, `spec-doc`, `spec-src`, `spec-title`,
`spec-section-<i>`, `spec-comment-<i>`, `spec-note-draft`, `spec-note-input`,
`spec-note-save`, `spec-note`, `spec-prompt` (gummi's own `%%` prompts),
`spec-note-resolve`, `spec-note-resolution` (a resolution marker, folded onto
the note above it: "resolved by" when it closes it, "answered by" when an
agent answered a person's note that only a person can close), `spec-checks`, `spec-check-<name>`, `spec-pending` (how many notes hold the
gate), `spec-request-changes` (sends them to the stage that owns them: the TUI's R), `spec-none`.

Request changes that sends a card back to an earlier stage asks first:
`changes-confirm` (the dialog), `changes-question`, `changes-go`,
`changes-cancel`, `changes-error`.

Diff: `diff-head`, `diff-rev`, `diff-since`, `diff-since-all`, `diff-since-new`,
`diff-fresh` (branch moved banner), `diff-fresh-show`, `diff-pending`,
`diff-request-changes` (sends the open comments to the implementer, or with an
earlier stage's spec note back to it: the TUI's R),
`diff-files`, `diff-file-<i>`, `diff-filebox-<i>`, `diff-viewed-<i>`,
`diff-line-<idx>` (idx = raw diff line index, the annotation coordinate; click
`.o`/`.n` to comment), `diff-unfold-<i>` ("Show N lines" on a file folded
because it is large, or the diff already drew many lines; a file with an open
comment is never folded, and its lines have no `diff-line-*` until unfolded),
`diff-orphans`, `annotation-<id>`,
`annotation-resolve-<id>`, `annotation-delete-<id>`, `annotation-draft`,
`annotation-input`, `annotation-save`, `diff-none`.

PR: `pr`, `pr-state`, `pr-fetched`, `pr-refresh`, `pr-pull`, `pr-thread-<i>`,
`pr-push`, `pr-push-cmd`, `pr-push-copy`, `pr-none`, `pr-link` (link a pull
request, when the card's menu offers it).

Log: `log`, `log-head`, `log-why` (why the history is read-only),
`log-none`, `log-commit-<i>` (oldest first), `log-show-<i>` (a commit's
changes, in `log-patch`), `log-reword-<i>`, `log-editor-<i>`,
`log-editor-save-<i>`, `log-squash-<i>` (`aria-pressed`), `log-plan` (the
draft's bar), `log-plan-line` (what the dry run says), `log-confirm`
(rewriting pushed commits), `log-reset`, `log-apply`, `log-push`,
`log-push-cmd` (the force push a rewrite of pushed commits leaves).

Stats: `stats`, `stats-spent`, `stats-passes`, `stats-rework`, `stats-table`,
`stats-none`.

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
| `palette`, `palette-input`, `palette-results`, `palette-row-<ID>`, `palette-view-<name>` | ⌘K |
| `keys-help` | the `?` sheet |
| `menu` | a popup menu opened with `openMenu` (its `testid` option overrides `menu`, e.g. `rail-more-menu`, `card-actions-menu`) |
| `modal`, `modal-close` | a dialog opened with `openModal` (its `testid` option overrides `modal`) |
| `view-<name>`, `view-<name>-body` | a registered view (`newcard`, `agent`, `fleet`, `goals`, `goal`, `stacks`, `ingest`, `bugs`, `doctor`) |
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
Board agent (`agent`): `agent-head`, `agent-profile`, `agent-model`,
`agent-model-use`, `agent-context`, `agent-spent`, `agent-transcript`,
`agent-item` (`data-type`, `data-key`), `agent-empty`, `agent-live`,
`agent-streaming`, `agent-busy`, `agent-composer`, `agent-input`,
`agent-send`, `agent-interrupt`, `agent-confirm` (+ `-yes`/`-no`: a switch
that would end the conversation), `agent-error`, `agent-open-error`;
not open: `agent-opener`, `agent-open-profile`, `agent-open-model`,
`agent-open`, `agent-opening`.
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
`newcard-profile`, `newcard-envelope`, `newcard-repo`, `newcard-base`,
`newcard-adopt`, `newcard-stack`, `newcard-after`, `newcard-after-<ID>`,
`newcard-error` (a refusal no field owns), `newcard-error-<field>` (beside
its field: `title`, `desc`, `envelope`, `profile`, `severity`, `repo`,
`base`, `adopt`, `stack`, `after`), `newcard-create`, `newcard-autopilot`,
`newcard-cancel`.
