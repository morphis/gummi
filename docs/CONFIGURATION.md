# Backends and configuration

The README names the knobs a first user meets. This page holds the rest:
every backend's specifics, every key in the two config files, and the full
environment table. Design rationale lives in `DESIGN.md` §4.4 and §5.

## Agent backends

Stages are run by a pluggable agent layer. `GUMMI_AGENT` selects the
default backend, and a role's `backend:` field in `profiles.yaml` overrides
it, so one profile can mix providers: copilot for implement, claude for
review.

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
- **codex**: the Codex CLI (`GUMMI_CODEX_BIN` overrides the binary), using
  `codex exec --json` JSONL turns and `codex exec resume` — for turns
  within one gummi session, and for a session that continues an earlier
  one (a restored question, a reattached chat), whose thread gummi hands
  back so it keeps what it had already read. A thread the CLI no longer
  holds is dropped and the turn re-runs on a new one, so a stale id costs
  a slower turn rather than the stage. Codex owns authentication (`codex login`) and provider
  config; gummi passes the profile's model through Codex's `-m` flag and
  never copies credentials. Requires `permissions: allow-all`: the exec
  stream cannot service guarded approval callbacks. Messages appear when
  Codex completes each message item; command, file-change and MCP activity
  stays visible as tool events.
- **opencode**: the opencode CLI (`GUMMI_OPENCODE_BIN` overrides the
  binary). Provider and model config is owned by opencode itself
  (`opencode auth`, `opencode.json`). Conversations carry across gummi
  processes through `run --session`, with the same fallback codex has: a
  session opencode cannot find is dropped and the turn re-runs on a fresh
  one. Under `permissions: guarded`, edits and writes inside the card's
  worktree are not asked for: the worktree cage allows them outright, and
  guarded asks only for what the cage does not name (the shell, web
  fetches, everything outside the worktree). opencode's own `question`
  tool is always denied; agents ask through gummi's `ask_user`.
- **pi**: the pi coding agent in RPC mode (`GUMMI_PI_BIN` overrides the
  binary): one `pi --mode rpc` child per session, speaking JSON lines on
  stdio. Provider and model config is owned by pi itself (`pi` →
  `/login`, or the provider's API-key env var such as
  `OPENROUTER_API_KEY`); `GUMMI_PI_PROVIDER` names the provider for model
  ids that do not carry one (`openrouter/z-ai/glm-flash-latest` carries
  its own, a bare `claude-sonnet-4.5` does not and would hit pi's
  built-in default). Conversations carry across gummi processes through
  `--session <id>`, with the same fallback the other CLI adapters have: a
  session pi cannot confirm is dropped and the turn re-runs on a fresh
  one. `--tools` gives pi's read-only research sessions a structural
  allowlist (bash/edit/write are absent, not merely unapproved). pi has
  no native MCP client, so gummi's tools reach it through a generated
  `--extension` (a per-session artifact): the session's tool descriptors
  register at load, and a spawned `gummi __mcp` child serves the actual
  calls, so ask_user and friends block exactly as on the MCP-native
  backends. RPC mode has no approval gate, so guarded collapses to
  allow-all.
- **antigravity**: Google's Antigravity CLI (binary `agy`;
  `GUMMI_ANTIGRAVITY_BIN` overrides it) in its bidirectional stream-json
  mode: one `agy` child per session, turns framed on stdin, events
  streamed off stdout. agy has no config-dir flag, so the adapter spawns
  every child with `HOME` redirected to a per-card directory under the
  workspace state area (`.gummi/state/agent-home/<ID>/agy`, and
  `agy-consult` beside it for the card's consult conversation) — the
  card's config tree (OAuth token copy, `mcp_config.json`,
  conversations) lives there and nowhere in the operator's own config.
  It is kept apart from the card's scratch directory, which the agent is
  told is its own; a home an older gummi kept under
  `.gummi/state/scratch/<ID>/agy-home` is moved on the card's next
  session, conversations and all, so a resume still finds them. The card
  home is seeded from the operator's
  login (re-run `agy` in your real home to refresh it) and removed with
  the card's cleanup. The tools agy runs inherit that `HOME`, so gummi
  points the few settings they need back at your real home, each only
  when you have not set it yourself: `GIT_CONFIG_GLOBAL` (your git
  identity and signing setup), `GNUPGHOME`, and Go's `GOENV`, `GOPATH`,
  `GOCACHE` and `GOLANGCI_LINT_CACHE`, so cards share your module and
  build caches. `XDG_CONFIG_HOME` and `XDG_CACHE_HOME` stay redirected,
  because agy reads them itself. gummi's tools reach the child through the card
  home's `mcp_config.json`, and forwarded skills are symlinked into the
  card home's skill root (`~/.gemini/config/skills` under the card home).
  Usage is metered as per-turn deltas from agy's cumulative token totals;
  `GUMMI_ANTIGRAVITY_CREDITS_PER_1K` prices them into credits, or the
  engine's default token pricing applies. Requires `permissions:
  allow-all` (agy's print mode has no approval surface, so guarded is
  refused); read-only research sessions are refused too (no structural
  write-stripping); a turn cannot be interrupted mid-flight (the stream
  protocol has no cancel event) — the board's stop control waits for the
  turn to end; images are not carried. The model id carries the effort
  dial (`gemini-3.1-pro-high`); an empty model runs agy's own default.
- **headless**: a generic subprocess adapter for any agent binary speaking
  a small stdio JSON protocol. `GUMMI_AGENT_CMD` is its command line. The
  child inherits gummi's environment and reads its own provider config from
  there. `GUMMI_HEADLESS_CREDITS_PER_1K` prices a local endpoint's token
  spend into credits so it meters against the same envelope.

gummi suppresses operator-level config that could hijack a stage (codex
gets `--ignore-user-config`, claude gets `--strict-mcp-config` and a
scrubbed session env). It does not suppress repo-level agent instructions:
no adapter disables `AGENTS.md`, `CLAUDE.md` or project skills, because
those are the repository's own guidance for agents working in it.

A card's **consult** (a question asked beside its stage) runs in the main
checkout, not the card's worktree. On claude, opencode and pi it runs
read-only: the backend's own write tools are stripped, as for a research
session. copilot, codex and antigravity cannot strip them, so a consult
there still answers but can write to the checkout; the consult says so
where it opens (TUI and web), and `gummi doctor` reports it per backend as
`consult:<backend>`. Route the profile's `consult` role (it falls back to
the architect's) to a confining backend to avoid that.

No usable agent leaves the board static. Creation, specs, worktrees and
gates all still work.

## `.gummi/config.yaml`

Scaffolded on first run. Every key is optional.

| key | meaning |
|---|---|
| `permissions` | `allow-all` (default; gummi assumes it runs in a sandbox) or `guarded` (agent tool calls need approval through the inbox) |
| `sandbox` | workspace default for the tool-coverage refusal: `enforce`, `warn` (built-in default) or `off`. Only `enforce` refuses anything — `warn` and `off` both let a run start. It does not confine writes; the backend's own file-tool policy does that, and no backend confines the shell. See DESIGN §4.4 |
| `repo` | the git repository gummi manages when `.gummi` sits above it, named relative to the workspace root (e.g. `git/lxd`). Empty means the workspace root is the repo |
| `repos` | a map of selectable names to repository paths under the workspace. Every card names one; `--repo` on the headless verbs and `o` on the board pick it |
| `checks.default` | a fixed list of verification checks. When set, check discovery skips the scribe and writes this list into every spec's verification plan. Unset, gummi discovers the repo's build, test and lint commands at plan approval into a `gummi-checks` block you review and edit |
| `env` | environment prerequisites, each `{probe, describe}`. A verification plan cites one with an `[env: <name>]` tag; gummi runs the probe at verify kickoff and in `gummi doctor` |
| `substrates` | the external environments work is proved on — a test cluster, a device farm — each `{describe, probe, provision, reset, ttl, timeout}`; only `probe` is required. A plan cites one exactly as it cites an env prerequisite (`[env: <name>]`), so a name may not be both. Unlike one, a substrate can be brought up (`provision`) and put back to a known state (`reset`), it expires (`ttl`, a Go duration), and **one job holds it at a time** across every gummi process on the workspace. `timeout` bounds one provision or reset (default 45m, at most 6h). `gummi doctor` reports each one's state and holder. See DESIGN §17.7 |
| `experiments` | the orchestrated live runs that prove work on a substrate, each `{describe, substrate, inputs, control, deploy, settle, run, collect, timeout}`; `substrate` and `run` are required. A goal's done-when item names one as its means of proof (`experiment: <name>`, optionally `assertions: [ids]`). Every command runs in the workspace root with `GUMMI_EVIDENCE` (a directory to write into — `results.ndjson` there, one `{"id","ok","detail"}` per line, is how a run reports its assertions), `GUMMI_TREE_<REPO>` and `GUMMI_HEAD_<REPO>` for each input (the unnamed default repo is `HOME`), `GUMMI_SUBSTRATE`, `GUMMI_EXPERIMENT`, `GUMMI_RUN`, `GUMMI_PURPOSE` and `GUMMI_ATTEMPT`. Exit 75 from any phase means *this run could not be judged*. `timeout` bounds each phase (default 30m). Operator configuration on purpose: a goal may change the rig it is tested on, and must not thereby change what counts as passing. See DESIGN §17.8 |
| `instructions` | extra instruction files (absolute paths) appended to the workspace environment card, user then workspace |
| `skills.forward` | workspace skills to forward into card sessions, as bare names (resolved against `.claude/skills`, `.agents/skills`, `.github/skills` at the workspace root, in that order) or absolute paths. A card runs in a worktree under `.gummi/worktrees/`, a sibling of the repository, so a skill kept beside `.gummi` is outside every backend's project scope and reaches nothing without this; a skill the repository itself ships is already in the worktree and needs no forwarding. Honored by the `opencode`, `copilot`, `claude` and `antigravity` backends (`agent.Capabilities.SkillDirs`); on a backend that cannot load skills from outside the worktree gummi says so on the card's activity feed rather than dropping them silently. gummi's own skill is refused — a card must never drive a second gummi |

## `.gummi/profiles.yaml`

The first run seeds this file. Its default `thrifty` profile leaves
`backend:` out, so the default backend drives it, and its model ids are
written for that backend: the CLI's aliases for claude, `provider/model`
for opencode and pi, OpenAI ids for codex, and `agy models` ids for
antigravity. An existing file is never rewritten.

A `default:` name plus a `profiles:` map. Each profile maps roles
(`architect`, `implementer`, `reviewer`, `scribe`) to `{backend, model}`:

```yaml
default: thrifty
profiles:
  thrifty:
    architect:   { backend: claude,  model: claude-opus-5 }
    implementer: { backend: copilot, model: gpt-5 }
    reviewer:    { backend: claude,  model: claude-sonnet-5 }
    scribe:      { backend: headless, model: qwen2.5-coder-32b }
  gemini: # everything on Antigravity
    architect:   { backend: antigravity, model: gemini-3.1-pro-high }
    implementer: { backend: antigravity, model: gemini-3.1-pro-high }
    reviewer:    { backend: antigravity, model: gemini-3.1-pro-high }
    scribe:      { backend: antigravity, model: gemini-3.1-pro-low }
```

A fifth role, `lead`, runs a goal's judgment (see the README's goals
section). It is optional: a profile with no `lead:` runs its goals' leads on
the architect's backend and model. A lead needs a backend that reaches
tools — native client tools or MCP — or the goal runs on its rules alone.
Lead turns are many and short — one per card question, plan check and
event worth a judgment — and each is booked to the goal's budget, so on a
goal with chatty cards the lead can cost more than any one card. A
mid-size model is usually enough for it:
`lead: { backend: claude, model: claude-sonnet-5 }` under an Opus
architect, for example.

`backend:` is optional; a role without one uses `GUMMI_AGENT`.
`output_token_max` caps a role's output tokens per turn. Provider config
(endpoints, keys, credit rates) stays in each backend's native store, so
this file is safe to commit.

A running board (TUI or `gummi web`) re-reads the file when it changes,
and its **next** session resolves against the edit; a session already
running keeps the model it started with. An edit that does not parse, or
that routes a role to a backend the board did not start, is refused — the
board keeps the profiles it had and says why (the web Doctor view's
`profiles:live` line) — and backends are started once, so a new backend
needs a restart.

Sessions (freeform cards) are the one exception to "the profiles decide
which backends start". A session picks its own agent and model in the web
face, and the board starts an installed agent CLI the first time a
session asks for it, even if no profile names it. The engine closes the
agents it started. `headless` needs `GUMMI_AGENT_CMD` for this, as it does
everywhere. A spec's stages still take their agents from the card's
profile (DESIGN §19.8).

Model ids are forwarded verbatim, so their spelling is the backend's: the
claude CLI takes `claude-haiku-4-5`, while Copilot's spells the same
model `claude-haiku-4.5`. `gummi doctor` fails a claude-backed role
spelled the dotted way without needing `--deep`, and a scribe the backend
refuses says so on the first card it fails, since discovery, estimates
and landing drafts all run on it.

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
    events: [gate.waiting, budget.exhausted]
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
| `card.created` | a card was minted (creation, ingest, bugs import) |
| `stage.enter` | a card crossed a stage edge (`from`/`to`/`actor` name it) |
| `card.verified` | the verify gate passed and the branch is ready to land — where gummi's job ends, so it fires **there**, not at a later merge that may never come |
| `card.parked` | the card stopped and waits (`reason`: needs-you, gave-up, blocked, quit) |
| `card.merged` | the branch was squash-merged onto its base (`commit` is the sha) |
| `gate.waiting` | the card waits at a gate for a person: a design-gate approval, an `--until` stop, and on a headless run the landing gate every `card.verified` leads to (a goal's own cards excepted — their goal lands them) |
| `question.waiting` | the card's agent asked and awaits an answer |
| `budget.exhausted` | the card's envelope is spent |
| `card.failed` | a failing verify, a rebase conflict, or an idle stop (`decision_kind` distinguishes) |

A stop raises **two** events, general first: `card.parked` (the card
stopped) then the decision that says how — `gate.waiting`,
`question.waiting`, `budget.exhausted` or `card.failed`. Key a pager on
the decision, not on the park. A landing likewise reports `card.merged`
before the `stage.enter` that crosses to done, because the worktree
reports the commit before the store commits the crossing. A run stopped
by `--until <stage>` is a stop like any other: the card waits for a
person to approve it on, so it raises `card.parked` and then
`gate.waiting`. So does a headless run that verifies: `card.verified`,
then `card.parked` and `gate.waiting` for the landing it now waits on.

The contract is advisory end to end: hooks run detached from the caller's
path (a full queue drops the event, a hung script is killed after 15
seconds), a hook's exit status is its own business, stdout goes nowhere,
and no hook failure can fail the run that raised the event. Exiting is
bounded too: whatever is still queued when the process closes gets 15
seconds in total to run, and the rest is abandoned rather than held
against a process that is done. Advisory is not silent, though — failed
runs and undelivered events are counted and printed in one line at exit:

```
gummi: hooks: 1 script run failed (last: page-oncall.sh "$1" on gate.waiting:
exit status 127: sh: page-oncall.sh: not found); 3 queued events abandoned at
exit (15s drain budget)
```

`gummi doctor` reports each configured hook and stats the script when the
line begins with a path or a bare command name, so a typo is visible
before an event needs it.

Events fire where they are committed — the store reports crossings,
parks, decisions, creations and the verify stamp; the worktree layer
reports squash-merges — so a hook fires once per committed row, from
whichever process drove it, and the store's dedupe keys apply (a
re-raised decision that deduped to a no-op raises nothing).

## Environment variables

| variable | effect |
|---|---|
| `GUMMI_AGENT` | default backend: `copilot` (default), `claude`, `codex`, `opencode`, `pi`, `antigravity`, `headless` |
| `GUMMI_AGENT_CMD` | the headless adapter's command line |
| `GUMMI_CLAUDE_BIN`, `GUMMI_CODEX_BIN`, `GUMMI_OPENCODE_BIN`, `GUMMI_PI_BIN`, `GUMMI_ANTIGRAVITY_BIN` | a backend's binary, when it is not the default name on PATH |
| `GUMMI_PI_PROVIDER` | the provider pi routes a session to when its model id does not name one |
| `GUMMI_HEADLESS_CREDITS_PER_1K` | token→credit rate for a local endpoint; 0 uses the engine default |
| `GUMMI_ANTIGRAVITY_CREDITS_PER_1K` | token→credit rate for antigravity sessions (agy reports token counts only); 0 uses the engine default |
| `GUMMI_MODEL` | fallback model when a role isn't covered by a profile |
| `GUMMI_ENVELOPE` | default credit envelope for new cards, and a floor under the estimated one. Unset, the board prefills 2000 and headless runs refuse to start. The envelope is checked between sessions, so a card stops a little over it — one session's worth |
| `GUMMI_STAGE_BUDGET` | flat per-stage credit cap |
| `GUMMI_TURN_RESERVE` | one turn's credits, the floor under envelope-derived stage budgets |
| `GUMMI_REVIEW_DIFF_MAX` | bytes of diff the reviewer is handed inline (default 48 KiB); above it the reviewer gets a stat and fetches what it reads, and 0 forces that shape |
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

Every request's `Host` must be one of the server's own names — loopback,
the address it was reached on, the `--addr` name, the `--tls-cert`
certificate's names, the tailnet node's name and addresses once it is up,
and `--allow-host` — or it is answered 421. That is what stops a web page
elsewhere from reaching the board through a DNS name it points at
127.0.0.1 (DNS rebinding), where the `Origin` check alone cannot.
