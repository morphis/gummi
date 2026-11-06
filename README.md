# gummi

**Run a fleet of coding agents from one board in your browser.**

![the gummi web board](docs/assets/web.png)

```sh
gummi web        # http://127.0.0.1:7878 — pair your browser, phone or tablet
```

Every piece of work gets its own agent, worktree and branch. You talk to
them, read their diffs and land what you like — from one page, not a pile
of terminals. Each session spends from a dollar budget you set: when it
runs out, the work pauses for your top-up and never overspends.

## Sessions

A session is a coding agent on a git worktree and branch of its own. Run
as many as you like side by side; anything waiting on you lands in one
**Needs you** inbox. Read the diff, comment on lines, then merge or open
a PR — it lands only when you say so.

## Agents

An agent is a session with a home of its own instead of a repository —
a standing assistant for research, triage or ops. Sessions and agents
can come back on a schedule or a heartbeat, and an agent can **watch**:
a schedule you allow runs a cheap command check, and only when it finds
something does a model start work in a repository you named.

## Dynamic workflows

Once workflows are turned on (they are off by default; see
[docs/workflows.md](docs/workflows.md#turning-workflows-on)), a session
whose work deserves more than one agent writes a spec and
proposes a **workflow**: design, build, review and validate, each step a
fresh agent in the session's own tree, in sequence, in parallel or in a
loop until the checks pass. You allow the run, follow every step live,
and revert it in one click. Recipes are plain YAML — use the bundled
`deliver`, `investigate` and `fix-until-green`, or write your own:
[docs/workflows.md](docs/workflows.md) is the guide and the reference.

## And also

- **Your models, per role** — Copilot CLI, Claude Code or opencode; the
  session and each workflow role (planner, implementer, reviewer, …) pick
  a model from a profile you edit on the board: a strong one where it
  earns it, a cheap one elsewhere.
- **Anywhere** — loopback or your tailnet (`gummi web --tailscale`),
  with push notifications.
- **Across repositories** — a session may attach other repositories of
  the workspace: the same branch in each, landed one repository at a
  time, one pull request each.
- **Terminal** — the same board as a TUI (`gummi`).
- **Unattended** — `gummi run --envelope 5 "<prompt>"` runs one session
  with no one at the keyboard, for evals and CI: readable text on a
  terminal, NDJSON when piped (`--format`), and `--jsonl events.jsonl` keeps
  the full NDJSON in a file beside either;
  `gummi run --envelope 2 --workflow triage --input report=@bug.md` runs a
  workflow on its inputs and streams its report and artifacts.

## Get started

```sh
sudo snap install gummi-agent --edge
export PATH=/snap/gummi-agent/current/bin:$PATH
curl -fsSL https://gh.io/copilot-install | bash   # or Claude Code / opencode
cd your-repo && gummi doctor && gummi web
```

Run the binary from `/snap/gummi-agent/current/bin/gummi` directly, not
through the `gummi-agent.gummi` wrapper: the snap is strictly confined,
and gummi needs your repositories, your agents' CLIs and their logins.
Without snap, `go install github.com/morphis/gummi/cmd/gummi@latest`
(Go 1.26+).

To keep the web board running as a systemd service (port 7879), install the unit from the snap:

```sh
gummi-agent.install-script | sudo bash -ex
```

The service runs as the user who ran `sudo` (`SUDO_USER`), with that
user's home as `HOME`, in `/project` (or the home when there is no
`/project`). It asks whether to enable the in-browser terminal and Tailscale, and for
the Tailscale node name and auth key. It writes
`/etc/systemd/system/gummi-web.service` and `/etc/gummi-web.env` (mode
0600, holds `TS_AUTHKEY`), then enables and starts the service. Run it
again to change the answers.

To answer ahead of time instead, for an unattended install, pass a YAML
file of flat `key: value` lines (`terminal`, `tailscale`, `ts_hostname`,
`ts_tls`, `ts_authkey`); nothing is asked then:

```sh
gummi-agent.install-script | sudo bash -ex -s -- --preseed gummi.yaml
```

```yaml
terminal: false
tailscale: true
ts_hostname: gummi
ts_authkey: tskey-auth-…
```

Prefer the terminal? Run `gummi` in the repo; `?` shows every key.

The first time the board starts in a repository it offers a short setup, in
the terminal and on the web page alike: which agent to run (skipped when
only one is usable, with a hint for each that is not), a strong and an
everyday model from that agent's list, and a confirm. Enter on each screen
takes the preselections. It rewrites `.gummi/profiles.yaml` to one `default`
profile; Esc or Skip keeps the starter file. `gummi run` and `gummi doctor`
never show it, and neither does a later start. `P` in the terminal, or
Profiles on the page, edits the profile afterwards.

## Docs

[Configuration](docs/CONFIGURATION.md) ·
[Design](docs/DESIGN.md) ·
[Contributing](AGENTS.md) ·
[Security](SECURITY.md) ·
[MIT License](LICENSE)
