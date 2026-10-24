# Backends and configuration

This page holds every backend's specifics, every key in the two config
files, and the full environment table. Design rationale lives in `DESIGN.md` §4.4 and §5.

## Agent backends

Sessions and a workflow run's steps are run by a pluggable agent layer.
`GUMMI_AGENT` selects the default backend, and a role's `backend:` field in
`profiles.yaml` overrides it, so one profile can mix providers: copilot for
a run's writing steps, claude for its planner and reviews.

- **copilot** *(default)*: the official Copilot SDK for Go, driving the
  `copilot` CLI in server mode. Full duplex: streaming, tool-call
  visibility, client tools, session resume — including across gummi
  processes, through the SDK's own `ResumeSession`, which keeps the
  conversation history a restored question would otherwise have to
  rediscover. A conversation the server no longer has falls back to a
  fresh session.
- **claude**: the Claude Code CLI in streaming print mode
  (`GUMMI_CLAUDE_BIN` overrides the binary). Requires `permissions:
  allow-all`; guarded mode is rejected because the CLI's default permission
  mode silently auto-denies tools. Claude Code manages its own endpoint
  routing through its native config (`ANTHROPIC_BASE_URL`, login).
  Two things gummi asks of the CLI that are worth knowing: it narrows the
  built-in **tool roster** (`--tools`) to the surface the session's
  allowlist actually permits — measured on CLI 2.1.x this removes ~35% of
  every request's prompt and the extra turn the CLI otherwise spends
  looking up gummi's deferred MCP tools — and, when a session continues a
  conversation the CLI still holds (a restored question, a reattached
  chat), it passes `--resume` so that session keeps what it had already
  read. Both degrade silently: a CLI whose `--help` does not advertise
  `--tools` keeps the full roster, and a conversation the CLI no longer
  holds is simply not resumed.
- **opencode**: the opencode CLI (`GUMMI_OPENCODE_BIN` overrides the
  binary). Provider and model config is owned by opencode itself
  (`opencode auth`, `opencode.json`). Conversations carry across gummi
  processes through `run --session`; a session opencode cannot find is
  dropped and the turn re-runs on a fresh one. Under `permissions: guarded`, edits and writes inside the card's
  worktree are not asked for: the worktree cage allows them outright, and
  guarded asks only for what the cage does not name (the shell, web
  fetches, everything outside the worktree). opencode's own `question`
  tool is always denied; agents ask through gummi's `ask_user`.
- **headless**: a generic subprocess adapter for any agent binary speaking
  a small stdio JSON protocol. It is what gummi's own demo and browser
  tests drive, not a supported agent. `GUMMI_AGENT_CMD` is its command line. The
  child inherits gummi's environment and reads its own provider config from
  there. `GUMMI_HEADLESS_CREDITS_PER_1K` prices a local endpoint's token
  spend into credits so it meters against the same envelope.

Each backend keeps its own discovery of skills, plugins and extensions,
at user and at repo scope, exactly as when you run that CLI yourself; no
adapter disables `AGENTS.md`, `CLAUDE.md`, project skills or the
operator's own skills and plugins. Beyond its skill pack (DESIGN §23),
gummi adds none: a
skill reaches a card when the card's backend finds it — in the
repository, or in your user directory for that agent. Which directories
those are is each CLI's own business. See DESIGN §4.1a.

Your MCP servers load too: claude's card sessions add gummi's server
beside yours. Only a read-only session on claude runs with
`--strict-mcp-config`, since its tool roster is its cage. The session
env is scrubbed.

A card's **consult** (a question asked beside its session) runs in the
main checkout, not the card's worktree. On claude and opencode it runs
read-only: the backend's own write tools are stripped. copilot cannot strip them, so a consult
there still answers but can write to the checkout; the consult says so
where it opens (TUI and web), and `gummi doctor` reports it per backend as
`consult:<backend>`. Route the profile's `consult` role (it falls back to
the architect's) to a confining backend to avoid that.

No usable agent leaves the board static. Creating sessions, their
worktrees and specs all still work.

## Workflows and their runs

A workflow recipe (DESIGN §25.4) is a YAML file. gummi reads
`.gummi/workflows/<name>.yaml` first, then its bundled recipes
(`deliver`, `investigate` and `fix-until-green`), so a workspace file of
the same name overrides a bundled one. A session agent may also hand a
recipe over inline. `.gummi/workflows/` is never created for you; make it
when you keep a recipe of your own, or save one: a run whose recipe the
session wrote inline offers **Save as recipe…** on its block (`s` in the
terminal), which writes it to `.gummi/workflows/<name>.yaml` under a name no
workspace or bundled recipe has. Only a person saves one; the session agent
cannot write there.

Each run keeps a directory of its own, `.gummi/runs/<session>/<n>/`: the
recipe as allowed, the artifacts its steps write, the diff files a step
reads as `{{run.diff}}`, each step's output and the run's report. It sits
in the workspace, not the session's tree, so none of it shows in the
session's diff.

A run's **baseline** needs no setting. When a run is proposed, gummi runs
its spec's done-when check commands in the session's tree as it is, and
again before the first step if the tree or the spec has moved since. A
check that already passes, or a `go test -run` that matches no test, is
named on the proposal and told to the session; the run's report then
reads each checked item before and after (`DW-1 failed before the run, now passes`). The
checks run as they do at the run's end, so a slow check makes the first
step wait for it; nothing is refused or paused over what they find.

## `.gummi/config.yaml`

Scaffolded on first run. Every key is optional.

| key | meaning |
|---|---|
| `permissions` | `allow-all` (default; gummi assumes it runs in a sandbox) or `guarded` (agent tool calls need approval through the inbox) |
| `sandbox` | workspace default for the tool-coverage refusal: `enforce`, `warn` (built-in default) or `off`. Only `enforce` refuses anything — `warn` and `off` both let a run start. It does not confine writes; the backend's own file-tool policy does that, and no backend confines the shell. See DESIGN §4.4 |
| `triage` | `on` (default) or `off`: whether a session's first request is read by a cheap one-shot, before its agent hears it, for whether it is work for a workflow recipe; the agent is told the recipe it names, and still proposes the run for the person to allow. Runs on the profile's `triage` slot, else `scout`, else `scribe`. See DESIGN §25.5 |
| `coauthors` | a list of people, each written `Name <email>`, named in a `Co-authored-by:` trailer on every commit made in the workspace: gummi appends them to the commits it makes (checkpoints, `gummi commit`, squashes, landings, a message written in a rewrite), and a `commit-msg` hook adds them to every other commit made under it — a card's agent's, a person's in the web terminal. The hook lives under `.gummi/state/hooks` and is named to git through gummi's own environment (`core.hooksPath`), so nothing is written to the repository or its git configuration and no session is asked to go against the repository's own instructions; it runs the repository's own hooks first, names co-authors only in checkouts under the workspace root, and is skipped by `git commit --no-verify`. Unset means no trailer. A workspace list replaces a user-level one. The settings in the web page and the TUI write the workspace's. A configured co-author is the one `Co-authored-by:` line gummi's attribution checks let through |
| `repo` | the git repository gummi manages when `.gummi` sits above it, named relative to the workspace root (e.g. `git/lxd`). Empty means the workspace root is the repo |
| `repos` | a map of selectable names to repository paths under the workspace. Every card names one; `o` on the board picks it |
| `checks.default` | a fixed list of checks, each `{name, cmd}`, validated at load |
| `env` | environment prerequisites, each `{probe, describe}`; `gummi doctor` runs each probe and reports it |
| `instructions` | extra instruction files (absolute paths) appended to the workspace environment card, user then workspace |

## `.gummi/profiles.yaml`

The first run seeds this file. Its default `thrifty` profile leaves
`backend:` out, so the default backend drives it, and its model ids are
written for that backend: the CLI's aliases for claude and
`provider/model` for opencode. An existing file is never rewritten.

A `default:` name plus a `profiles:` map. Each profile maps roles to
`{backend, model}`. A session runs on `implementer`. A workflow run's step
runs on its role's slot — `scout`, `planner`, `contractor`, `implementer`,
`tester`, `scribe` or `reviewer` — or on the slot its recipe names
(`slot:`, as `deliver`'s second reviewer names `reviewer-alt`), falling
back to `architect` for a planner or contractor and then to `implementer`.
`consult` falls back to `architect`, `scribe` also drafts landing
messages, and `auditor` falls back to `scribe`. A recipe has no tiers: an
older profile's `step-<tier>` and `crew-<tier>` slots are no longer read.

```yaml
default: thrifty
profiles:
  thrifty:
    architect:   { backend: claude,  model: claude-opus-5 }
    implementer: { backend: copilot, model: gpt-5 }
    reviewer:    { backend: claude,  model: claude-sonnet-5 }
    reviewer-alt: { backend: copilot, model: gpt-5 } # deliver's second review
    scribe:      { backend: copilot, model: gpt-5-mini }
  open: # everything on opencode
    architect:   { backend: opencode, model: anthropic/claude-sonnet-5-5 }
    implementer: { backend: opencode, model: openai/gpt-5 }
    reviewer:    { backend: opencode, model: anthropic/claude-sonnet-5-5 }
    scribe:      { backend: opencode, model: anthropic/claude-haiku-4-5 }
```

A recipe's planner step reads the work and may revise the run's
remaining steps; its turns are paid from the session's envelope like
every step's, and it plans without regard to money: a run estimates
nothing, and goes on until the session's envelope is spent. A mid-size model is usually enough for it:
`planner: { backend: claude, model: claude-sonnet-5-5 }` under an Opus
architect, for example.

A person may edit the file from the board instead: the web page's
**Profiles** view (the More menu) and the terminal's (`P`) add, edit and
delete a profile and make one the default. Each role is checked the way a
session's agent and model are — a claude role given a gpt model is
refused — a backend the board has not started is started, and the file is
validated as a whole before it is written, so a save never leaves one the
board refuses. Both list a file that does not load beside why, so the
profile that breaks it can be fixed or deleted there. A misspelt key
(`Default:`, a role's `adapter:`) is refused rather than ignored.

`backend:` is optional; a role without one uses `GUMMI_AGENT`.
`output_token_max` raises a role's cap on output tokens per turn; only
the opencode backend reads it. Provider config
(endpoints, keys, credit rates) stays in each backend's native store, so
this file is safe to commit.

A running board (TUI or `gummi web`) re-reads the file when it changes,
and its **next** session resolves against the edit; a session already
running keeps the model it started with. An edit that does not parse, or
that routes a role to a backend the board did not start, is refused — the
board keeps the profiles it had and says why (the web Doctor view's
`profiles:live` line) — and a hand edit naming a backend the board did
not start needs a restart; the same edit saved from the board does not.

Sessions (freeform cards) are the one exception to "the profiles decide
which backends start". A session picks its own agent and model in the web
face, and the board starts an installed agent CLI the first time a
session asks for it, even if no profile names it. The engine closes the
agents it started. `headless` needs `GUMMI_AGENT_CMD` for this, as it does
everywhere. A session's workflow steps still take their agents from the session's
profile.

Model ids are forwarded verbatim, so their spelling is the backend's: the
claude CLI takes `claude-haiku-4-5`, while Copilot's spells the same
model `claude-haiku-4.5`. `gummi doctor` fails a claude-backed role
spelled the dotted way without needing `--deep`, and a scribe the backend
refuses says so on the first card it fails, since landing drafts run on
it.

## Hooks

`hooks:` in either config file runs a script when the board changes —
the script surface beside `GUMMI_NOTIFY`'s bell/desktop toast. Each
entry is a shell **command line**, executed via `sh -c` in the workspace
root, with:

- a **JSON payload** on stdin (flat, `event`/`id`/`stage`/`branch`/
  `repo`/`title`/… — every field but `event` is omitempty),
- `GUMMI_EVENT`, `GUMMI_CARD` and `GUMMI_WORKSPACE` in the environment,
- the event name as the **shell's** `"$1"`.

```yaml
hooks:
  - run: ~/bin/gummi-notify "$1"       # every event
  - run: page-oncall.sh "$1"
    events: [question.waiting, budget.exhausted]
```

`"$1"` is the shell's first argument, not the script's: `run:` is a
command line, so a line naming a script and nothing else hands that
script **no arguments**. Forward it, as above, if the script wants it as
`argv[1]` — or just read `GUMMI_EVENT`, which needs no forwarding.

An entry without `events:` fires on every event; with it, only on the
events named. User-level and workspace entries **both** run, in that
order — a personal pager beside the workspace's own pipeline.

The event vocabulary (closed; a config typo is rejected at load):

| event | fires when |
|---|---|
| `card.created` | a card was minted (the new-card form, a schedule) |
| `card.parked` | the card stopped and waits (`reason`: needs-you, gave-up, blocked, quit) |
| `card.merged` | the branch was squash-merged onto its base (`commit` is the sha) |
| `question.waiting` | the card's agent asked and awaits an answer |
| `budget.exhausted` | the card's envelope is spent |
| `card.failed` | a rebase conflict, a session that could not run, or an idle stop (`decision_kind` distinguishes) |
| `stage.enter`, `card.verified`, `gate.waiting` | raised by nothing since the workflow was retired (DESIGN §24); still accepted in `events:` so an existing config loads |

A stop raises **two** events, general first: `card.parked` (the card
stopped) then the decision that says how — `question.waiting`,
`budget.exhausted` or `card.failed`. Key a pager on the decision, not on
the park.

The contract is advisory end to end: hooks run detached from the caller's
path (a full queue drops the event, a hung script is killed after 15
seconds), a hook's exit status is its own business, stdout goes nowhere,
and no hook failure can fail the run that raised the event. Exiting is
bounded too: whatever is still queued when the process closes gets 15
seconds in total to run, and the rest is abandoned rather than held
against a process that is done. Advisory is not silent, though — failed
runs and undelivered events are counted and printed in one line at exit:

```
gummi: hooks: 1 script run failed (last: page-oncall.sh "$1" on budget.exhausted:
exit status 127: sh: page-oncall.sh: not found); 3 queued events abandoned at
exit (15s drain budget)
```

`gummi doctor` reports each configured hook and stats the script when the
line begins with a path or a bare command name, so a typo is visible
before an event needs it.

Events fire where they are committed — the store reports parks,
decisions and creations; the worktree layer reports squash-merges — so a hook fires once per committed row, from
whichever process drove it, and the store's dedupe keys apply (a
re-raised decision that deduped to a no-op raises nothing).

## Environment variables

| variable | effect |
|---|---|
| `GUMMI_AGENT` | default backend: `copilot` (default), `claude`, `opencode` (or `headless`, for tests and demos) |
| `GUMMI_AGENT_CMD` | the headless adapter's command line |
| `GUMMI_CLAUDE_BIN`, `GUMMI_OPENCODE_BIN` | a backend's binary, when it is not the default name on PATH |
| `GUMMI_HEADLESS_CREDITS_PER_1K` | token→credit rate for a local endpoint; 0 uses the engine default |
| `GUMMI_MODEL` | fallback model when a role isn't covered by a profile (unset: the backend's own default) |
| `GUMMI_ENVELOPE` | default budget for new cards, in dollars (`20`, `$12.50`). Unset, the board prefills $20. The envelope is checked between sessions, so a card stops a little over it — one session's worth |
| `GUMMI_STAGE_BUDGET` | flat credit cap for a card's agent session when the card has no envelope |
| `GUMMI_TURN_RESERVE` | one turn's credits: the floor under envelope-derived session budgets, and what a planner or curator run must have left to start |
| `GUMMI_COPILOT_HINT` | `off` hides the status-bar Copilot quota pill (needs an authenticated `gh`) |
| `GUMMI_THEME` | `dark` (default), `light`, `neon` |
| `GUMMI_NOTIFY` | needs-attention hook: `bell` (default), `desktop`, `off`; `gummi web` defaults to `off`, since its terminal is the server's log |
| `GUMMI_WEB_ADDR` | where `gummi web` listens when `--addr` is not given (default `127.0.0.1:7878`) |
| `TS_AUTHKEY` | a Tailscale auth key for `gummi web --tailscale` when `--ts-authkey` is not given; skips the browser login on the node's first run |
| `GUMMI_MOTION` | `off` freezes every activity glyph and stops the clock tick |
| `GUMMI_ATTACH_CMD` | command for the board's raw-attach (`a`) |
| `GUMMI_EVENT`, `GUMMI_CARD`, `GUMMI_WORKSPACE` | exported to `hooks:` scripts: the event name, the card id, the workspace root |

## The web host on a tailnet

`gummi web --tailscale` puts the board on your tailnet as a node of its
own, next to the loopback listener. It embeds Tailscale (`tsnet`), so the
host needs no `tailscaled`, no port forwarding and no public address.

| flag | effect |
|---|---|
| `--tailscale` | join the tailnet and serve the board there too |
| `--ts-hostname NAME` | the node's name on the tailnet (default `gummi`) |
| `--ts-authkey KEY` | authenticate with an auth key instead of a browser login; falls back to `TS_AUTHKEY`. A flag shows in `ps` and shell history, so prefer the variable |
| `--ts-tls` | serve HTTPS on port 443 with a tailnet certificate; without it the node serves plain HTTP on the loopback listener's port |
| `--verbose` | pass the node's own log through to the server's log |
| `--allow-host a,b` | further names a request's `Host` may carry (see below) |

On the first run without a key, `gummi web` prints one line on stdout and
waits for it, however long that takes:

```
tailscale: open https://login.tailscale.com/a/… to add this board to your tailnet
```

Open it on any device logged into the tailnet. Once the node is up it
prints where the board is, and records that address, so a second board
host started on this workspace names it:

```
gummi web: serving https://gummi.<tailnet>.ts.net
```

The node's identity lives in `.gummi/state/web/tsnet/` (0700). Keep it,
and the board keeps its name and login across restarts; delete it to
start as a new node.

`--ts-tls` needs **MagicDNS** and **HTTPS certificates** enabled for the
tailnet, one switch each in the Tailscale admin console (DNS page). It is
also what Web Push needs: notifications and the installed page want a
secure origin, and plain HTTP on a tailnet name is not one. The first
certificate is fetched as soon as the node is up; a failure shows in the
server's log.

Pairing applies on the tailnet exactly as on loopback: being on the
tailnet gets a browser to the pairing screen, not to the board.
`--no-pairing` refuses `--tailscale`. If the machine already runs
`tailscaled`, `tailscale serve` in front of a loopback `gummi web` remains
an alternative; pass `--allow-host <name>.<tailnet>.ts.net`, since the
proxy forwards the tailnet name as the request's `Host`. `--no-pairing`
refuses an `--allow-host` that is not loopback for the same reason it
refuses `--tailscale`: through the proxy, the unpaired board is open.

A paired device's token is honoured only on the host and port it paired
on. A device paired on `127.0.0.1:7878` and opened through the tailnet
name (or the other way round) pairs again there, as a second device —
which, with a code from `gummi web pair` or one the page asked for, waits
until a browser already paired approves it on its page.
Plain HTTP on an address other than loopback carries the pairing code and
the token in clear; the server warns when it starts that way.

`--terminal` gives paired devices a **Terminal** tab on every card that
has a worktree: your own shell (`$SHELL`), started in that worktree. It is
off by default and refused with `--no-pairing`, because it is more than
the board: a paired device can then run any command as you, outside any
agent sandbox. The shell stays running when the page closes and is
replayed when it comes back; it ends on `exit`, with the tab's **End
shell** button (for one that no longer answers), when the card's worktree
is removed, after an hour with no page attached, or when the server
stops. A board runs at most four at once. Opening one is logged by the
server and announced on every other open page. On a touch screen a bar
under the shell has the keys a phone's keyboard lacks: esc, tab, the
arrows, home, end, page up and down, a few symbols, and ctrl and alt,
which hold for the next key (ctrl, then c, interrupts).

Every request's `Host` must be one of the server's own names — loopback,
the address it was reached on, the `--addr` name, the `--tls-cert`
certificate's names, the tailnet node's name and addresses once it is up,
and `--allow-host` — or it is answered 421. That is what stops a web page
elsewhere from reaching the board through a DNS name it points at
127.0.0.1 (DNS rebinding), where the `Origin` check alone cannot.
