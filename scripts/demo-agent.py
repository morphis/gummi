#!/usr/bin/env python3
"""A scripted agent for the gummi demo recording.

Speaks gummi's headless backend protocol (internal/agent/headless.go) on
stdio, so the engine, the worktrees, the spec files and the diff are all
real -- only the model's tokens are pre-authored. That makes the demo
deterministic and offline: every take is identical, and a re-record after
a UI change costs one command.

Wire in with:

    GUMMI_AGENT=headless GUMMI_AGENT_CMD=scripts/demo-agent.py gummi

The content is keyed by (card id, stage). Cards with no entry fall back
to a generic pass, which is what the board's background cards use.
"""

import json
import os
import re
import sys
import time

# Typing rhythm. The recording wants the thread to fill at a readable
# pace, not instantly; a test harness wants neither.
FAST = os.environ.get("GUMMI_DEMO_FAST") == "1"


def beat(seconds):
    if not FAST:
        time.sleep(seconds)


DEBUG = os.environ.get("GUMMI_DEMO_DEBUG")


def log(msg):
    if not DEBUG:
        return
    with open(DEBUG, "a", encoding="utf-8") as fh:
        fh.write(msg.rstrip("\n") + "\n")


def emit(obj):
    log("OUT " + json.dumps(obj)[:300])
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()


def say(text, pace=0.5):
    """A finished assistant message in the card thread."""
    beat(pace)
    emit({"type": "message", "text": text})


def think(text, pace=0.35):
    beat(pace)
    emit({"type": "reasoning", "text": text})


def tool(name, detail, pace=0.35):
    beat(pace)
    emit({"type": "tool", "name": name, "detail": detail})


def usage(credits, inp=0, out=0, model="demo"):
    emit({"type": "usage", "credits": credits, "input": inp, "output": out, "model": model})


def idle():
    emit({"type": "idle"})


# --------------------------------------------------------------------------
# spec editing helpers
# --------------------------------------------------------------------------

def read_spec(path):
    try:
        with open(path, encoding="utf-8") as fh:
            return fh.read()
    except OSError:
        return ""


def write_spec(path, text):
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text)


def set_section(doc, title, body):
    """Replace the body of '## <title>' with body, keeping section order."""
    lines = doc.split("\n")
    head = "## " + title
    try:
        start = next(i for i, ln in enumerate(lines) if ln.strip() == head)
    except StopIteration:
        # Section missing entirely: append it.
        return doc.rstrip("\n") + "\n\n" + head + "\n\n" + body.strip() + "\n"
    end = len(lines)
    for i in range(start + 1, len(lines)):
        if lines[i].startswith("## "):
            end = i
            break
    new = [head, ""] + body.strip().split("\n") + [""]
    return "\n".join(lines[:start] + new + lines[end:])


def append_section(doc, title, extra):
    lines = doc.split("\n")
    head = "## " + title
    try:
        start = next(i for i, ln in enumerate(lines) if ln.strip() == head)
    except StopIteration:
        return doc.rstrip("\n") + "\n\n" + head + "\n\n" + extra.strip() + "\n"
    end = len(lines)
    for i in range(start + 1, len(lines)):
        if lines[i].startswith("## "):
            end = i
            break
    body = "\n".join(lines[start + 1:end]).strip("\n")
    # Drop the seeded %% @gummi: placeholder once real content arrives.
    body = "\n".join(ln for ln in body.split("\n") if not ln.startswith("%% @gummi:"))
    merged = (body.strip() + "\n" + extra.strip()).strip()
    return "\n".join(lines[:start] + [head, "", merged, ""] + lines[end:])


def edit_spec(path, fn):
    doc = read_spec(path)
    if not doc:
        return
    write_spec(path, fn(doc))
    tool("edit", os.path.basename(path))


# --------------------------------------------------------------------------
# FD-001 -- the hero card: lxc list gains a DISK USAGE% column
# --------------------------------------------------------------------------

PROBLEM = """\
`lxc list` can show memory usage both absolutely (`-c m`, MEMORY USAGE)
and as a percentage of the instance's limit (`-c M`, MEMORY USAGE%).
Disk has only the absolute form: `-c D` renders DISK USAGE and there is
no percentage counterpart.

That asymmetry costs operators the one number they actually page on.
"How full is this instance's root disk?" is the question behind most
capacity alerts, and today it takes `lxc list -c D` plus a separate
`lxc config device get` per instance to answer it. The numerator is
already fetched: `diskUsageColumnData` reads
`State.Disk[rootDisk].Usage` (lxc/list.go). Only the denominator and a
column entry are missing."""

OUT_OF_SCOPE = """\
- Sorting or filtering by the new column (`lxc list` sorts by name; the
  column framework has no comparator seam and adding one is its own FD).
- A percentage column for any other resource (CPU, network).
- Server-side changes. This is a client-side column over fields the API
  already returns."""

CONSIDERED = """\
1. **Denominator from `State.Disk[rootDisk].Total`** -- the pool-reported
   total the `instances_state_total` API extension added. It is fetched
   on the same request that already carries `Usage`, so the column costs
   no extra round trip and reflects what the storage driver actually
   sees. Its weakness is that the field is documented to be `0` both when
   the driver cannot report a total and when the instance has access to
   the entire pool, so those two cases are indistinguishable.
%% @architect: does an older server without instances_state_total need a
   distinct rendering, or is blank enough?
2. **Denominator from the root device's configured `size`** -- read
   `size` off the root disk device in `ExpandedDevices`, mirroring how
   `memoryUsagePercentColumnData` reads `limits.memory` from
   `ExpandedConfig`. Structurally this is the sibling of the memory
   column and needs no API extension, but it reports usage against the
   *quota* rather than against real capacity, and an unset `size` (the
   common default) leaves the column blank on exactly the instances an
   operator most wants to see."""

CHOSEN = """\
Approach 1: `State.Disk[rootDisk].Total` as the denominator.

The column answers a capacity question, so it must report against real
capacity, not against a quota that is unset on most instances. Render
the empty string whenever the percentage cannot be computed honestly --
`Total == 0` (driver cannot report, or the instance sees the whole pool)
and `Usage == 0` (the existing `-c D` guard) both fall back to blank,
matching what `memoryUsagePercentColumnData` already does for an unset
`limits.memory`. A blank cell is the established idiom in this table for
"not applicable here"; a `0.0%` would read as a measurement.

Shorthand char: `U`. Both `d` (Description) and `D` (DISK USAGE) are
taken, so the disk pair cannot mirror the `m`/`M` casing. `U` is free
and reads as "usage percent"."""

NOTES = """\
1. Add `diskUsagePercentColumnData(cInfo api.InstanceFull) string` to
   `lxc/list.go`, directly below the existing `diskUsageColumnData` so
   the disk pair reads together. Resolve the root disk with
   `api.GetRootDiskDevice(cInfo.ExpandedDevices)`, exactly as
   `diskUsageColumnData` does; guard on `cInfo.State != nil`,
   `cInfo.State.Disk != nil`, `Usage > 0` and `Total > 0`; return
   `fmt.Sprintf("%.1f%%", float64(Usage)/float64(Total)*100)`.
2. Register `'U': {"DISK USAGE%", c.diskUsagePercentColumnData, true,
   false, true, false}` in `columnsShorthandMap` (lxc/list.go:576). The
   4th..7th fields copy `'D'`'s: it needs instance state, not snapshots,
   and it is a per-instance column.
3. Add `  U - Disk usage (%)` to the shorthand list in the long help
   text, immediately after the `D - disk usage` line, so `lxc list
   --help` documents the pair together.
4. Extend `TestColumns` in `lxc/list_test.go` with a `-c U` case
   asserting the header renders as `DISK USAGE%`.

### Plan claims

- `helper diskUsagePercentColumnData: keyed by the root disk device name
  from api.GetRootDiskDevice, returns string`
- `golden "50.0%" = Usage 512MiB over Total 1GiB because step 1 divides
  Usage by Total and formats with %.1f%%`
- `Total == 0 renders "" and never divides` -- the guard in step 1 is
  what makes the whole-pool and cannot-report cases safe.
- `column ordering is unaffected` -- 'U' is a new key in
  columnsShorthandMap; defaultColumns ("ns46tSL") is not touched, so no
  existing invocation changes output."""

VERIFICATION = """\
```gummi-checks
- name: build
  cmd: go build ./lxc/...
- name: vet
  cmd: go vet ./lxc/
```

- `lxc list -c U` renders a DISK USAGE% column with one decimal place.
- An instance whose root disk reports `Total == 0` renders an empty
  cell, not `0.0%` and not a divide-by-zero panic.
- `lxc list --help` lists `U - Disk usage (%)` under the shorthand chars.
- `lxc list` with no `-c` produces byte-identical output to before."""

IMPL_GO = '''
func (c *cmdList) diskUsagePercentColumnData(cInfo api.InstanceFull) string {
	rootDisk, _, _ := api.GetRootDiskDevice(cInfo.ExpandedDevices)

	if cInfo.State == nil || cInfo.State.Disk == nil {
		return ""
	}

	disk := cInfo.State.Disk[rootDisk]

	// Total is 0 both when the storage driver cannot report a total and
	// when the instance has access to the whole pool; neither is a
	// percentage we can honestly print, so render an empty cell.
	if disk.Usage <= 0 || disk.Total <= 0 {
		return ""
	}

	return fmt.Sprintf("%.1f%%", (float64(disk.Usage)/float64(disk.Total))*float64(100))
}
'''


def patch_list_go(workdir):
    """Apply the real change to lxc/list.go in the feature's worktree."""
    path = os.path.join(workdir, "lxc", "list.go")
    try:
        with open(path, encoding="utf-8") as fh:
            src = fh.read()
    except OSError:
        return False

    if "diskUsagePercentColumnData" in src:
        return True

    # 1. the column function, right after diskUsageColumnData
    anchor = "func (c *cmdList) typeColumnData(cInfo api.InstanceFull) string {"
    if anchor in src:
        src = src.replace(anchor, IMPL_GO.strip() + "\n\n" + anchor, 1)

    # 2. the shorthand map entry, next to 'D'
    dline = "\t\t'D': {\"DISK USAGE\", c.diskUsageColumnData, true, false, true, false},"
    if dline in src:
        src = src.replace(
            dline,
            dline + "\n\t\t'U': {\"DISK USAGE%\", c.diskUsagePercentColumnData, true, false, true, false},",
            1,
        )

    # 3. the help text
    hline = "  D - disk usage"
    if hline in src:
        src = src.replace(hline, hline + "\n  U - Disk usage (%)", 1)

    with open(path, "w", encoding="utf-8") as fh:
        fh.write(src)
    return True


def git(workdir, *args):
    import subprocess

    subprocess.run(["git", "-C", workdir] + list(args), check=False,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


# --------------------------------------------------------------------------
# stage scripts
# --------------------------------------------------------------------------

def fd001_brainstorm(ctx, turn):
    """Interactive: one question per turn, recommendation attached."""
    if turn == 0:
        think("reading lxc/list.go to see how the memory pair is built")
        tool("read", "lxc/list.go")
        tool("grep", "columnsShorthandMap")
        tool("read", "shared/api/instance_state.go")
        edit_spec(ctx["spec"], lambda d: set_section(d, "Problem", PROBLEM))
        edit_spec(ctx["spec"], lambda d: set_section(d, "Out of scope", OUT_OF_SCOPE))
        say(
            "Confirmed against the source: `columnsShorthandMap` (lxc/list.go:576)\n"
            "carries `m`/`M` for the memory pair but only `D` for disk, and\n"
            "`diskUsageColumnData` already reads `State.Disk[rootDisk].Usage`.\n"
            "So the numerator exists; this feature is a denominator plus a\n"
            "column entry. I have written that up under Problem.\n\n"
            "First decision -- **which denominator?**\n\n"
            "  a. `State.Disk[rootDisk].Total`, the pool-reported total from\n"
            "     the `instances_state_total` API extension\n"
            "  b. the root device's configured `size`, mirroring how the\n"
            "     memory column reads `limits.memory`\n\n"
            "My recommendation is **(a)**: the column answers a capacity\n"
            "question, and `size` is unset on most instances -- (b) would\n"
            "leave the column blank exactly where operators need it."
        )
        usage(38, 9100, 640, ctx["model"])
    elif turn == 1:
        think("recording the decision and laying out both approaches")
        edit_spec(ctx["spec"], lambda d: set_section(d, "Considered approaches", CONSIDERED))
        say(
            "Recorded. Both approaches are written up under Considered\n"
            "approaches with their tradeoffs -- I have kept (b) on the page\n"
            "rather than deleting it, so the spec shows why it lost.\n\n"
            "Next decision -- **the shorthand char**. This is more awkward\n"
            "than it looks: `d` is Description and `D` is DISK USAGE, so the\n"
            "disk pair cannot mirror the `m`/`M` casing the memory pair uses.\n\n"
            "Free and plausible: `U` (usage percent) or `%`.\n\n"
            "I recommend **`U`**: `%` has to be quoted in most shells, which\n"
            "makes `lxc list -c %` a support question waiting to happen."
        )
        usage(31, 11200, 520, ctx["model"])
    else:
        say(
            "Noted -- `U` it is. I have flagged the remaining open question\n"
            "(the older-server rendering) as its own `%%` thread against the\n"
            "approach it belongs to, so it rides the spec into the gate.\n\n"
            "Problem, scope and both approaches are on the page. Convergence\n"
            "is the Spec stage's job, so I will stop here."
        )
        usage(22, 12400, 300, ctx["model"])


def fd001_spec(ctx, turn):
    think("converging on the denominator and writing the verification plan")
    tool("read", "lxc/list.go")
    tool("read", "lxc/list_test.go")
    edit_spec(ctx["spec"], lambda d: set_section(d, "Chosen approach", CHOSEN))
    edit_spec(ctx["spec"], lambda d: set_section(d, "Verification plan", VERIFICATION))
    edit_spec(
        ctx["spec"],
        lambda d: d.replace(
            "%% @architect: does an older server without instances_state_total need a",
            "%% @architect: resolved -- blank is enough; an older server reports\n"
            "%% Total == 0, which is already the whole-pool case. One rendering,\n"
            "%% one guard. (was: does an older server need a distinct rendering?)\n"
            "%% @gummi-unused: ",
            1,
        ),
    )
    say(
        "Converged on `State.Disk[rootDisk].Total`, and resolved the open\n"
        "thread while I was there: an older server without the\n"
        "`instances_state_total` extension simply reports `Total == 0`, which\n"
        "is already the whole-pool case. One guard covers both -- no version\n"
        "sniffing needed.\n\n"
        "The Verification plan pins the honest-blank rule as an explicit\n"
        "check, because that is the one behaviour a careless implementation\n"
        "gets wrong (`0.0%` reads as a measurement).\n\n"
        "Spec is complete and ready for your gate."
    )
    usage(54, 14800, 1120, ctx["model"])


def fd001_plan(ctx, turn):
    think("deriving tracer-bullet steps from the approved spec")
    tool("read", "lxc/list.go")
    tool("grep", "func TestColumns")
    edit_spec(ctx["spec"], lambda d: set_section(d, "Implementation notes", NOTES))
    say(
        "Four steps, tracer-bullet ordered: step 1 cuts the thin complete\n"
        "path (the column function), steps 2-3 wire it into the map and the\n"
        "help text, step 4 locks the header in a test.\n\n"
        "Plan claims are written out -- including the golden trace for\n"
        "`50.0%` and the `Total == 0` invariant, so the critique can check\n"
        "them without re-deriving anything from the source."
    )
    usage(47, 16100, 980, ctx["model"])


def fd001_critique(ctx, turn):
    think("refuting the plan before the user approves it")
    tool("read", "lxc/list.go")
    tool("read", "lxc/list_test.go")
    edit_spec(
        ctx["spec"],
        lambda d: append_section(
            d,
            "Review",
            "standards / nit: step 2's tuple fields are given positionally.\n"
            "%% @reviewer: nit -- the `column` struct's four bool fields are\n"
            "unnamed at the callsite, so \"copy 'D''s\" is the only thing\n"
            "keeping them right. Not blocking (every other entry in the map\n"
            "is written the same way), but worth a comment on the line.",
        ),
    )
    say(
        "One pass, four lenses. The plan holds up:\n\n"
        "- **completeness** -- all four Verification plan bullets map to a\n"
        "  step. The `Total == 0` bullet maps to step 1's guard.\n"
        "- **correctness** -- traced the `50.0%` golden through step 1:\n"
        "  512MiB/1GiB = 0.5, `%.1f%%` renders `50.0%`. Reaches the value.\n"
        "- **security** -- no new input surface; the divide is guarded.\n"
        "- **executability** -- `go build ./lxc/...` and `go vet ./lxc/`\n"
        "  both resolve in this worktree, and the checks block parses.\n\n"
        "One non-blocking nit filed against step 2.\n\n"
        "VERDICT: pass"
    )
    usage(41, 15900, 870, ctx["model"])


def review_filed(ctx):
    """Has the reviewer already filed its blocking finding into the spec?"""
    return "@reviewer: blocking -- the plan's step 4" in read_spec(ctx["spec"])


def fix_resolved(ctx):
    """Has the implementer already resolved that finding?"""
    return "@implementer: resolved -- added the `-c U` case" in read_spec(ctx["spec"])


def fd001_implement(ctx, turn):
    wd = ctx["workdir"]
    if not ctx.get("asked"):
        think("reading the plan, then the surrounding column functions")
        tool("read", "lxc/list.go")
        tool("grep", "diskUsageColumnData")
        beat(0.6)
        ctx["asked"] = True
        ctx["pending_ask"] = True
        emit({
            "type": "ask",
            "id": "ask-1",
            "ask": {
                "question": (
                    "Step 1 guards on `Total <= 0`. `lxc list -c D` (the "
                    "absolute column) has no such guard -- it prints a size "
                    "whenever `Usage > 0`. Should I leave `-c D` alone?"
                ),
                "options": [
                    "Leave -c D untouched -- out of scope",
                    "Add the same guard to -c D too",
                ],
                "recommendation": "Leave -c D untouched -- out of scope",
                "anchor": "Out of scope",
            },
        })
        return

    think("applying step 1: the column function, below diskUsageColumnData")
    patch_list_go(wd)
    tool("edit", "lxc/list.go")
    think("step 2: registering 'U' in columnsShorthandMap")
    tool("edit", "lxc/list.go")
    think("step 3: the shorthand list in the long help text")
    tool("edit", "lxc/list.go")
    tool("bash", "go build ./lxc/...")
    beat(1.0)
    edit_spec(
        ctx["spec"],
        lambda d: set_section(
            d,
            "Progress",
            "- [x] step 1 -- `diskUsagePercentColumnData`, guarded on\n"
            "      `Usage > 0 && Total > 0`\n"
            "- [x] step 2 -- `'U'` registered in `columnsShorthandMap`\n"
            "- [x] step 3 -- `U - Disk usage (%)` in the long help text\n"
            "- [ ] step 4 -- `-c U` case in `TestColumns`",
        ),
    )
    git(wd, "add", "-A")
    git(wd, "commit", "-m",
        "feat(lxc/list): add a DISK USAGE% column under -c U\n\n"
        "Memory has had both an absolute and a percentage column since the\n"
        "shorthand map was introduced; disk has only ever had the absolute\n"
        "one, so the capacity question most alerts are about took a second\n"
        "command to answer.\n\n"
        "Total is 0 both when the driver cannot report one and when the\n"
        "instance sees the whole pool, so the column renders blank rather\n"
        "than 0.0% -- a blank cell already means \"not applicable\" in this\n"
        "table, and 0.0% would read as a measurement.")
    tool("bash", "git commit")
    say(
        "Steps 1-3 are in and committed. `go build ./lxc/...` is clean.\n\n"
        "The guard is the load-bearing line, so I gave it a comment naming\n"
        "both cases `Total == 0` covers -- a later reader will otherwise\n"
        "\"simplify\" it into a divide-by-zero.\n\n"
        "Step 4 (the `TestColumns` case) is still open; Progress records it."
    )
    usage(96, 24300, 2410, ctx["model"])


def fd001_review(ctx, turn):
    # Each stage is a fresh process, so the round cannot live in memory.
    # The spec is the state, exactly as it is for gummi itself: round two
    # is the one that finds the implementer's resolution already there.
    second_round = fix_resolved(ctx)
    think("fresh context: reading the spec, then the diff")
    tool("read", os.path.basename(ctx["spec"]))
    tool("bash", "git diff main...HEAD")
    if not second_round:
        edit_spec(
            ctx["spec"],
            lambda d: append_section(
                d,
                "Review",
                "\nconformance / blocking: step 4 of the plan is not implemented.\n"
                "%% @reviewer: blocking -- the plan's step 4 adds a `-c U` case to\n"
                "`TestColumns` asserting the `DISK USAGE%` header, and the\n"
                "Verification plan's first bullet depends on it. The diff adds no\n"
                "test at all, so the column's header is unproven.",
            ),
        )
        say(
            "Two lenses, reported separately.\n\n"
            "**conformance** -- one blocking finding. The plan's step 4 (a\n"
            "`-c U` case in `TestColumns`) is absent from the diff, and the\n"
            "Verification plan's first bullet leans on it. The column's\n"
            "header is currently unproven by anything.\n\n"
            "No scope creep: the diff touches only `lxc/list.go`, and `-c D`\n"
            "is untouched as Out of scope requires.\n\n"
            "**standards** -- the guard comment is good; it names both cases\n"
            "`Total == 0` covers, which is the thing a future simplifier\n"
            "would otherwise delete.\n\n"
            "VERDICT: changes"
        )
        usage(58, 19800, 1180, ctx["model"])
    else:
        say(
            "Re-reviewed the resolved thread against the diff first.\n\n"
            "**conformance** -- step 4 is now implemented: `TestColumns`\n"
            "carries a `-c U` case asserting `DISK USAGE%`, which closes the\n"
            "blocking finding and the Verification plan bullet that depended\n"
            "on it. Nothing else changed, so no new scope to judge.\n\n"
            "**standards** -- nothing further.\n\n"
            "VERDICT: pass"
        )
        usage(44, 17600, 760, ctx["model"])


def fd001_fixup(ctx):
    """Implement, second time round: address the review finding."""
    wd = ctx["workdir"]
    think("reading the reviewer's blocking thread")
    tool("read", os.path.basename(ctx["spec"]))
    tool("edit", "lxc/list_test.go")
    tool("bash", "go test ./lxc/ -run TestColumns")
    beat(0.8)
    edit_spec(
        ctx["spec"],
        lambda d: set_section(
            d,
            "Progress",
            "- [x] step 1 -- `diskUsagePercentColumnData`, guarded on\n"
            "      `Usage > 0 && Total > 0`\n"
            "- [x] step 2 -- `'U'` registered in `columnsShorthandMap`\n"
            "- [x] step 3 -- `U - Disk usage (%)` in the long help text\n"
            "- [x] step 4 -- `-c U` case in `TestColumns`",
        ),
    )
    edit_spec(
        ctx["spec"],
        lambda d: d.replace(
            "%% @reviewer: blocking -- the plan's step 4",
            "%% @implementer: resolved -- added the `-c U` case to TestColumns;\n"
            "%% it asserts the DISK USAGE% header. go test ./lxc/ -run TestColumns\n"
            "%% passes.\n"
            "%% @reviewer: blocking -- the plan's step 4",
            1,
        ),
    )
    git(wd, "add", "-A")
    git(wd, "commit", "-m",
        "test(lxc/list): assert the DISK USAGE% header under -c U\n\n"
        "Closes the reviewer's blocking finding: the column's header was\n"
        "unproven, and the Verification plan's first bullet depended on it.")
    tool("bash", "git commit")
    say(
        "Added the `-c U` case to `TestColumns` and resolved the thread with\n"
        "how it was fixed. `go test ./lxc/ -run TestColumns` passes.\n\n"
        "Progress now shows all four steps done."
    )
    usage(37, 15200, 640, ctx["model"])


def fd001_verify(ctx, turn):
    think("running the spec's verification plan")
    tool("bash", "go build ./lxc/...")
    beat(1.2)
    tool("bash", "go vet ./lxc/")
    beat(0.8)
    tool("bash", "go test ./lxc/ -run TestColumns")
    say(
        "Repo checks: `go build ./lxc/...` and `go vet ./lxc/` both clean.\n\n"
        "Verification plan, bullet by bullet:\n"
        "- `-c U` renders DISK USAGE% to one decimal -- PASS (TestColumns)\n"
        "- `Total == 0` renders blank, no panic -- PASS (the guard returns\n"
        "  before the divide; exercised by the zero-total fixture)\n"
        "- `lxc list --help` lists `U - Disk usage (%)` -- PASS\n"
        "- bare `lxc list` output unchanged -- PASS (defaultColumns is\n"
        "  untouched, so no existing invocation changes)\n\n"
        "VERDICT: pass"
    )
    usage(52, 18400, 900, ctx["model"])


FD001 = {
    "brainstorm": fd001_brainstorm,
    "spec": fd001_spec,
    "plan": fd001_plan,
    "critique": fd001_critique,
    "implement": fd001_implement,
    "review": fd001_review,
    "verify": fd001_verify,
}


# --------------------------------------------------------------------------
# generic fallback -- the board's background cards
# --------------------------------------------------------------------------

GENERIC_TOOLS = [("read", "README.md"), ("grep", "func main"), ("read", "lxc/list.go")]


def generic(ctx, turn):
    stage = ctx["stage"]
    think("working the {} stage".format(stage))
    for name, detail in GENERIC_TOOLS[:2]:
        tool(name, detail)
    spec = ctx["spec"]
    if stage in ("brainstorm", "triage"):
        edit_spec(spec, lambda d: set_section(
            d, "Problem",
            "Filed from the demo seed. The card exists to give the board a\n"
            "card at this stage; its content is not the subject of the demo."))
        edit_spec(spec, lambda d: set_section(
            d, "Considered approaches",
            "1. **Narrow fix** -- smallest diff, no new seams.\n"
            "2. **Generalised seam** -- more code, room to grow."))
        say("Wrote up the problem and two candidate approaches.")
    elif stage in ("spec", "diagnose"):
        edit_spec(spec, lambda d: set_section(
            d, "Chosen approach", "Approach 1: the narrow fix."))
        edit_spec(spec, lambda d: set_section(
            d, "Verification plan",
            "```gummi-checks\n- name: vet\n  cmd: go vet ./lxc/\n```\n\n"
            "- the change builds and vets clean"))
        say("Converged on approach 1 and wrote the verification plan.")
    elif stage == "plan":
        edit_spec(spec, lambda d: set_section(
            d, "Implementation notes",
            "1. Make the change in the identified file.\n"
            "2. Add a regression test that fails without step 1.\n\n"
            "### Plan claims\n\n"
            "- `the change is confined to one file`"))
        say("Two steps, tracer-bullet ordered.")
    elif stage in ("implement", "fix"):
        wd = ctx["workdir"]
        with open(os.path.join(wd, "DEMO-NOTE.md"), "w", encoding="utf-8") as fh:
            fh.write("Placeholder change for the gummi demo board.\n")
        tool("edit", "DEMO-NOTE.md")
        git(wd, "add", "-A")
        git(wd, "commit", "-m", "chore: demo board placeholder change")
        say("Implemented and committed.")
    elif stage == "critique":
        say("Walked the plan once through all four lenses. Steps map to the\n"
            "verification plan, the claims trace, and the checks block runs\n"
            "here. Nothing blocking.\n\nVERDICT: pass")
    elif stage == "review":
        say("**conformance** -- nothing blocking.\n"
            "**standards** -- nothing further.\n\nVERDICT: pass")
    elif stage == "verify":
        tool("bash", "go vet ./lxc/")
        say("Repo checks clean; verification plan satisfied.\n\nVERDICT: pass")
    else:
        say("Done.")
    usage(18, 5200, 340, ctx["model"])


# --------------------------------------------------------------------------
# scribe one-shots -- identified by the prompt, not by a stage hint
# --------------------------------------------------------------------------

def scribe(ctx, prompt):
    if "ESTIMATE:" in prompt:
        emit({"type": "text", "text": "ESTIMATE: 180"})
        usage(3, 2100, 20, ctx["model"])
        idle()
        return True
    if "squash-merge landing commit" in prompt:
        # The engine accepts only a reply that is entirely one fenced
        # gummi-commit block (parseGummiCommit); anything around the fence
        # is rejected and the merge dialog opens empty.
        say(
            "```gummi-commit\n"
            "feat(lxc/list): add a DISK USAGE% column under -c U\n"
            "\n"
            "- answer the capacity question lxc list was always missing:\n"
            "  memory had an absolute and a percentage column, disk only\n"
            "  ever had the absolute one\n"
            "- report against the pool-reported total rather than the root\n"
            "  device quota, which is unset on most instances and would\n"
            "  blank the column exactly where operators need it\n"
            "- render a blank cell when no honest percentage exists, since\n"
            "  0.0% would read as a measurement rather than as absence\n"
            "```",
            pace=0.8,
        )
        usage(6, 4300, 210, ctx["model"])
        idle()
        return True
    if "build/test/lint" in prompt or "checks block" in prompt:
        emit({"type": "text", "text": "```gummi-checks\n- name: build\n  cmd: go build ./lxc/...\n"
                                      "- name: vet\n  cmd: go vet ./lxc/\n```"})
        usage(4, 2600, 60, ctx["model"])
        idle()
        return True
    return False


# --------------------------------------------------------------------------
# protocol loop
# --------------------------------------------------------------------------

# The hero card is created live during the recording, so its number
# depends on how many cards the seed made first. Recognise it by title.
HERO_MARKER = "disk usage percent"


def detect(hints):
    """Pull card id, stage and spec path out of the init frame's hints."""
    blob = "\n".join(hints or [])
    card = ""
    m = re.search(r"\b(FD-\d+|BG-\d+|RS-\d+)\b", blob)
    if m:
        card = m.group(1)
    if HERO_MARKER in blob.lower():
        card = "HERO"
    stage = ""
    m = re.search(r"Stage:\s*([A-Za-z ]+)", blob)
    if m:
        stage = m.group(1).strip().lower()
        if stage.startswith("plan critique"):
            stage = "critique"
        else:
            stage = stage.split()[0]
    spec = ""
    m = re.search(r"at (\S+\.md)", blob)
    if m:
        spec = m.group(1).rstrip(".")
    return card, stage, spec


def main():
    ctx = {"card": "", "stage": "", "spec": "", "workdir": os.getcwd(), "model": "demo"}
    turn = 0
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            frame = json.loads(line)
        except ValueError:
            continue

        kind = frame.get("type")
        if kind == "init":
            ctx["workdir"] = frame.get("workdir") or os.getcwd()
            ctx["model"] = frame.get("model") or "demo"
            card, stage, spec = detect(frame.get("hints"))
            ctx["card"], ctx["stage"], ctx["spec"] = card, stage, spec
            print("demo-agent: card={} stage={} spec={}".format(card, stage, spec),
                  file=sys.stderr)
            log("=== INIT card={} stage={} spec={} model={}".format(
                card, stage, spec, ctx["model"]))
            log("HINTS " + repr("\n".join(frame.get("hints") or []))[:1500])
        elif kind == "resolve":
            # the answer to our ask_user; carry straight on with the work
            log("RESOLVE " + json.dumps(frame.get("result"))[:200])
            ctx["pending_ask"] = False
            fd001_implement(ctx, 1)
            idle()
        elif kind == "interrupt":
            idle()
        elif kind == "send":
            prompt = frame.get("text") or ""
            if scribe(ctx, prompt):
                continue
            stage = ctx["stage"]
            handler = None
            if ctx["card"] == "HERO":
                # Implement re-entered after a review bounce is the fixup
                # pass: the blocking thread is in the spec and unresolved.
                if stage == "implement" and review_filed(ctx) and not fix_resolved(ctx):
                    fd001_fixup(ctx)
                    idle()
                    continue
                handler = FD001.get(stage)
            if handler is None:
                generic(ctx, turn)
            else:
                handler(ctx, turn)
            turn += 1
            # An outstanding ask_user is not the end of the turn: gummi
            # answers it with a resolve frame, and the work continues there.
            if not ctx.get("pending_ask"):
                idle()
        else:
            continue


if __name__ == "__main__":
    try:
        main()
    except (BrokenPipeError, KeyboardInterrupt):
        pass
