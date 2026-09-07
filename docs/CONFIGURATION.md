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
  visibility, client tools, session resume.
- **claude**: the Claude Code CLI in streaming print mode
  (`GUMMI_CLAUDE_BIN` overrides the binary). Requires `permissions:
  allow-all`; guarded mode is rejected because the CLI's default permission
  mode silently auto-denies tools. Claude Code manages its own endpoint
  routing through its native config (`ANTHROPIC_BASE_URL`, login).
- **codex**: the Codex CLI (`GUMMI_CODEX_BIN` overrides the binary), using
  `codex exec --json` JSONL turns and `codex exec resume` while the gummi
  session is live. Codex owns authentication (`codex login`) and provider
  config; gummi passes the profile's model through Codex's `-m` flag and
  never copies credentials. Requires `permissions: allow-all`: the exec
  stream cannot service guarded approval callbacks. Messages appear when
  Codex completes each message item; command, file-change and MCP activity
  stays visible as tool events.
- **opencode**: the opencode CLI (`GUMMI_OPENCODE_BIN` overrides the
  binary). Provider and model config is owned by opencode itself
  (`opencode auth`, `opencode.json`).
- **headless**: a generic subprocess adapter for any agent binary speaking
  a small stdio JSON protocol. `GUMMI_AGENT_CMD` is its command line. The
  child inherits gummi's environment and reads its own provider config from
  there. `GUMMI_HEADLESS_CREDITS_PER_1K` prices a local endpoint's token
  spend into credits so it meters against the same envelope.
- **zz**: the zz CLI (`GUMMI_ZZ_BIN` overrides the binary), a small Rust
  coding agent that fronts any OpenAI-compatible endpoint (local llama.cpp,
  OpenRouter, a self-hosted gateway). zz's `-p ask` mode is
  process-per-turn with no stdin form, so gummi resumes a session through a
  `--session` transcript file rather than an in-process handle. zz owns
  provider selection through its own `~/.config/zz/config.toml`; a role's
  `provider:` field in `profiles.yaml` names one of that file's
  `[providers.<name>]` stanzas and gummi forwards it as `--provider`, so
  different roles under one zz binary can hit different endpoints. A role's
  `think:` field is forwarded as `--think <level>`, an opaque value the
  provider stanza declares. Requires `permissions: allow-all` (zz has no
  approval callback) and cannot run a read-only research session (zz has
  no flag to disable its write, edit and bash tools); point research roles
  at `claude` or `opencode`. Its prompt travels as a positional argv
  string, so a turn is bounded well under Linux's 128 KiB argv limit. Every
  invocation carries `--max-turns` (default 200, `GUMMI_ZZ_MAX_TURNS`) as a
  runaway-loop backstop; the real spend limiter is the credit envelope, so
  this only catches a session that never converges, and hitting it ends
  the turn with an error naming the cap and the knob. `GUMMI_ZZ_CREDITS_PER_1K`
  prices its token spend into credits, as headless does.

gummi suppresses operator-level config that could hijack a stage (codex
gets `--ignore-user-config`, claude gets `--strict-mcp-config` and a
scrubbed session env). It does not suppress repo-level agent instructions:
no adapter disables `AGENTS.md`, `CLAUDE.md` or project skills, because
those are the repository's own guidance for agents working in it. zz
follows the same rule; gummi passes neither `--no-skills` nor
`--no-agents-md`.

No usable agent leaves the board static. Creation, specs, worktrees and
gates all still work.

## `.gummi/config.yaml`

Scaffolded on first run. Every key is optional.

| key | meaning |
|---|---|
| `permissions` | `allow-all` (default; gummi assumes it runs in a sandbox) or `guarded` (agent tool calls need approval through the inbox) |
| `sandbox` | workspace default for the tool-coverage refusal and the main-checkout tripwire: `enforce`, `warn` (built-in default) or `off`. It does not confine writes; the backend's own file-tool policy does that, and no backend confines the shell. See DESIGN §4.4 |
| `autopilot_lanes` | how many autopilot cards drive at once (default 2). The attended pool is sized by `GUMMI_MAX_ACTIVE`, not this key, so an attended card never queues behind autopilot work |
| `repo` | the git repository gummi manages when `.gummi` sits above it, named relative to the workspace root (e.g. `git/lxd`). Empty means the workspace root is the repo |
| `repos` | a map of selectable names to repository paths under the workspace. Every card names one; `--repo` on the headless verbs and `o` on the board pick it |
| `checks.default` | a fixed list of verification checks. When set, check discovery skips the scribe and writes this list into every spec's verification plan. Unset, gummi discovers the repo's build, test and lint commands at plan approval into a `gummi-checks` block you review and edit |
| `env` | environment prerequisites, each `{probe, describe}`. A verification plan cites one with an `[env: <name>]` tag; gummi runs the probe at verify kickoff and in `gummi doctor` |
| `instructions` | extra instruction files (absolute paths) appended to the workspace environment card, user then workspace |
| `agent` | which installed CLI (`copilot`, `claude`, `codex`, `opencode`, `zz`) hosts the board's **agent tab**. It has nothing to do with the engine's per-role backends. The first-run picker writes this key without disturbing the rest of the file |

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
    scribe:      { backend: zz, model: qwen2.5-coder-32b, provider: local-llama-cpp }
```

`backend:` is optional; a role without one uses `GUMMI_AGENT`.
`output_token_max` caps a role's output tokens per turn. `provider:` and
`think:` are zz-only, described above. Provider config (endpoints, keys,
credit rates) stays in each backend's native store, so this file is safe
to commit.

## Environment variables

| variable | effect |
|---|---|
| `GUMMI_AGENT` | default backend: `copilot` (default), `claude`, `codex`, `opencode`, `headless`, `zz` |
| `GUMMI_AGENT_CMD` | the headless adapter's command line |
| `GUMMI_CLAUDE_BIN`, `GUMMI_CODEX_BIN`, `GUMMI_OPENCODE_BIN`, `GUMMI_ZZ_BIN` | a backend's binary, when it is not the default name on PATH |
| `GUMMI_HEADLESS_CREDITS_PER_1K`, `GUMMI_ZZ_CREDITS_PER_1K` | token→credit rate for a local endpoint; 0 uses the engine default |
| `GUMMI_ZZ_MAX_TURNS` | zz's runaway-turn backstop (default 200) |
| `GUMMI_MODEL` | fallback model when a role isn't covered by a profile |
| `GUMMI_MAX_ACTIVE` | the attended lane pool (default 1) |
| `GUMMI_ENVELOPE` | default credit envelope for new cards, and a floor under the estimated one. Unset, the board prefills 2000 and headless runs refuse to start |
| `GUMMI_STAGE_BUDGET` | flat per-stage credit cap |
| `GUMMI_TURN_RESERVE` | one turn's credits, the floor under envelope-derived stage budgets |
| `GUMMI_REVIEW_DIFF_MAX` | bytes of diff the reviewer is handed inline (default 48 KiB); above it the reviewer gets a stat and fetches what it reads, and 0 forces that shape |
| `GUMMI_COPILOT_HINT` | `off` hides the status-bar Copilot quota pill (needs an authenticated `gh`) |
| `GUMMI_THEME` | `dark` (default), `light`, `neon` |
| `GUMMI_NOTIFY` | needs-attention hook: `bell` (default), `desktop`, `off` |
| `GUMMI_MOTION` | `off` freezes every activity glyph and stops the clock tick |
| `GUMMI_ATTACH_CMD` | command for the board's raw-attach (`a`) and the agent tab, ahead of `GUMMI_AGENT` and the `agent:` key |
