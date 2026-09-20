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
  one.
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

No usable agent leaves the board static. Creation, specs, worktrees and
gates all still work.

## `.gummi/config.yaml`

Scaffolded on first run. Every key is optional.

| key | meaning |
|---|---|
| `permissions` | `allow-all` (default; gummi assumes it runs in a sandbox) or `guarded` (agent tool calls need approval through the inbox) |
| `sandbox` | workspace default for the tool-coverage refusal: `enforce`, `warn` (built-in default) or `off`. Only `enforce` refuses anything — `warn` and `off` both let a run start. It does not confine writes; the backend's own file-tool policy does that, and no backend confines the shell. See DESIGN §4.4 |
| `autopilot_lanes` | how many autopilot cards drive at once (default 2). The attended pool is sized by `GUMMI_MAX_ACTIVE`, not this key, so an attended card never queues behind autopilot work |
| `repo` | the git repository gummi manages when `.gummi` sits above it, named relative to the workspace root (e.g. `git/lxd`). Empty means the workspace root is the repo |
| `repos` | a map of selectable names to repository paths under the workspace. Every card names one; `--repo` on the headless verbs and `o` on the board pick it |
| `checks.default` | a fixed list of verification checks. When set, check discovery skips the scribe and writes this list into every spec's verification plan. Unset, gummi discovers the repo's build, test and lint commands at plan approval into a `gummi-checks` block you review and edit |
| `env` | environment prerequisites, each `{probe, describe}`. A verification plan cites one with an `[env: <name>]` tag; gummi runs the probe at verify kickoff and in `gummi doctor` |
| `substrates` | the external environments work is proved on — a test cluster, a device farm — each `{describe, probe, provision, reset, ttl, timeout}`; only `probe` is required. A plan cites one exactly as it cites an env prerequisite (`[env: <name>]`), so a name may not be both. Unlike one, a substrate can be brought up (`provision`) and put back to a known state (`reset`), it expires (`ttl`, a Go duration), and **one job holds it at a time** across every gummi process on the workspace. `timeout` bounds one provision or reset (default 45m, at most 6h). `gummi doctor` reports each one's state and holder. See DESIGN §17.7 |
| `experiments` | the orchestrated live runs that prove work on a substrate, each `{describe, substrate, inputs, control, deploy, settle, run, collect, timeout}`; `substrate` and `run` are required. A goal's done-when item names one as its means of proof (`experiment: <name>`, optionally `assertions: [ids]`). Every command runs in the workspace root with `GUMMI_EVIDENCE` (a directory to write into — `results.ndjson` there, one `{"id","ok","detail"}` per line, is how a run reports its assertions), `GUMMI_TREE_<REPO>` and `GUMMI_HEAD_<REPO>` for each input (the unnamed default repo is `HOME`), `GUMMI_SUBSTRATE`, `GUMMI_EXPERIMENT`, `GUMMI_RUN`, `GUMMI_PURPOSE` and `GUMMI_ATTEMPT`. Exit 75 from any phase means *this run could not be judged*. `timeout` bounds each phase (default 30m). Operator configuration on purpose: a goal may change the rig it is tested on, and must not thereby change what counts as passing. See DESIGN §17.8 |
| `instructions` | extra instruction files (absolute paths) appended to the workspace environment card, user then workspace |
| `skills.forward` | workspace skills to forward into card sessions, as bare names (resolved against `.claude/skills`, `.agents/skills`, `.github/skills` at the workspace root, in that order) or absolute paths. A card runs in a worktree under `.gummi/worktrees/`, a sibling of the repository, so a skill kept beside `.gummi` is outside every backend's project scope and reaches nothing without this; a skill the repository itself ships is already in the worktree and needs no forwarding. Honored by the `opencode` and `copilot` backends (`agent.Capabilities.SkillDirs`); on a backend that cannot load skills from outside the worktree gummi says so on the card's activity feed rather than dropping them silently. gummi's own skill is refused — a card must never drive a second gummi |
| `agent` | which installed CLI (`copilot`, `claude`, `codex`, `opencode`, `pi`) hosts the board's **agent tab**. It has nothing to do with the engine's per-role backends. The first-run picker writes this key without disturbing the rest of the file |

## `.gummi/profiles.yaml`

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

## Hooks

`hooks:` in either config file runs a script when the board changes —
the script surface beside `GUMMI_NOTIFY`'s bell/desktop toast. Each
entry is a shell command, executed via `sh -c` in the workspace root,
with:

- the **event name** as `$1`,
- a **JSON payload** on stdin (flat, `event`/`id`/`stage`/`branch`/
  `title`/… — every field but `event` is omitempty),
- `GUMMI_EVENT`, `GUMMI_CARD` and `GUMMI_WORKSPACE` in the environment,

```yaml
hooks:
  - run: ~/bin/gummi-notify            # every event
  - run: page-oncall.sh
    events: [gate.waiting, budget.exhausted]
```

An entry without `events:` fires on every event; with it, only on the
events named. User-level and workspace entries **both** run, in that
order — a personal pager beside the workspace's own pipeline.

The event vocabulary (closed; a config typo is rejected at load):

| event | fires when |
|---|---|
| `card.created` | a card was minted (creation, ingest, bugs import) |
| `stage.enter` | a card crossed a stage edge (`from`/`to`/`actor` name it) |
| `card.verified` | the card reached done with a verified branch — the landing moment |
| `card.parked` | the card stopped and waits (`reason`: needs-you, gave-up, blocked, quit) |
| `card.merged` | the branch was squash-merged onto its base (`commit` is the sha) |
| `gate.waiting` | the card blocked on a design-gate approval |
| `question.waiting` | the card's agent asked and awaits an answer |
| `budget.exhausted` | the card's envelope is spent |
| `card.failed` | a failing verify, a rebase conflict, or an idle stop (`decision_kind` distinguishes) |

The contract is advisory end to end: hooks run detached from the caller's
path (a full queue drops the event, a hung script is killed after 15
seconds), a hook's exit status and output are its own business, and no
hook failure can fail the run that raised the event. Events fire where
they are committed — the store reports crossings, parks, decisions and
creations; the worktree layer reports squash-merges — so a hook fires
once per committed row, from whichever process drove it, and the store's
dedupe keys apply (a re-raised decision that deduped to a no-op raises
nothing).

## Environment variables

| variable | effect |
|---|---|
| `GUMMI_AGENT` | default backend: `copilot` (default), `claude`, `codex`, `opencode`, `pi`, `headless` |
| `GUMMI_AGENT_CMD` | the headless adapter's command line |
| `GUMMI_CLAUDE_BIN`, `GUMMI_CODEX_BIN`, `GUMMI_OPENCODE_BIN`, `GUMMI_PI_BIN` | a backend's binary, when it is not the default name on PATH |
| `GUMMI_PI_PROVIDER` | the provider pi routes a session to when its model id does not name one |
| `GUMMI_HEADLESS_CREDITS_PER_1K` | token→credit rate for a local endpoint; 0 uses the engine default |
| `GUMMI_MODEL` | fallback model when a role isn't covered by a profile |
| `GUMMI_MAX_ACTIVE` | the attended lane pool (default 1) |
| `GUMMI_ENVELOPE` | default credit envelope for new cards, and a floor under the estimated one. Unset, the board prefills 2000 and headless runs refuse to start. The envelope is checked between sessions, so a card stops a little over it — one session's worth |
| `GUMMI_STAGE_BUDGET` | flat per-stage credit cap |
| `GUMMI_TURN_RESERVE` | one turn's credits, the floor under envelope-derived stage budgets |
| `GUMMI_REVIEW_DIFF_MAX` | bytes of diff the reviewer is handed inline (default 48 KiB); above it the reviewer gets a stat and fetches what it reads, and 0 forces that shape |
| `GUMMI_COPILOT_HINT` | `off` hides the status-bar Copilot quota pill (needs an authenticated `gh`) |
| `GUMMI_THEME` | `dark` (default), `light`, `neon` |
| `GUMMI_NOTIFY` | needs-attention hook: `bell` (default), `desktop`, `off` |
| `GUMMI_MOTION` | `off` freezes every activity glyph and stops the clock tick |
| `GUMMI_ATTACH_CMD` | command for the board's raw-attach (`a`) and the agent tab, ahead of `GUMMI_AGENT` and the `agent:` key |
| `GUMMI_EVENT`, `GUMMI_CARD`, `GUMMI_WORKSPACE` | exported to `hooks:` scripts: the event name, the card id, the workspace root |
