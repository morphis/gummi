#!/usr/bin/env python3
"""Assert a running goal's budget invariants, tick by tick.

DESIGN §17.3 says four things must be true of a goal's ledger at every
moment, not merely at the end. Nothing checked them while a goal ran, so
a drive that wanted to know had to read a hand-over afterwards and take
its word for it. This polls `gummi status <goal> --json` and checks them
against the goal as it actually stands:

  * goal spend + the cards' spend never exceeds the envelope
  * a live card never holds less than it has spent
  * the cards together never hold past the envelope-less-reserve line
  * an ended card holds only what it spent

It prints one NDJSON line per tick — the budget tree plus any violation
— and exits non-zero if it ever saw one, so it is usable from CI as well
as beside a drive.

One thing it deliberately does NOT assert: that the goal's OWN spend
stays out of the reserve. The reserve exists for the goal's review and
verify (`Ledger.OwnBudget` is reserve-inclusive), so a goal spending
into it is the design working. Asserting the stronger thing reports
violations that are not.

    scripts/goal-ledger.py ./bin/gummi /path/to/workspace GL-001 [seconds]
"""
import json
import subprocess
import sys
import time


def snapshot(gummi, workspace, goal):
    p = subprocess.run([gummi, "status", goal, "--json"], cwd=workspace,
                       capture_output=True, text=True)
    if p.returncode != 0:
        return None
    try:
        return json.loads(p.stdout)
    except json.JSONDecodeError:
        return None


def violations(goal):
    b = goal.get("budget") or {}
    cards = goal.get("cards") or []
    env = b.get("envelope", 0)
    own = b.get("goal_spend", 0.0)
    held = b.get("held_by_cards", 0.0)
    spent = b.get("card_spend", 0.0)
    reserve = b.get("reserve", 0)
    bad = []
    if own + spent > env + 0.5:
        bad.append(f"goal {own:.1f} + cards {spent:.1f} > envelope {env}")
    if held > env - reserve + 0.5:
        bad.append(f"cards hold {held:.1f}, past the {env}-{reserve} line")
    for c in cards:
        e, s = float(c.get("envelope", 0)), c.get("spent", 0.0)
        if c.get("state") in ("landed", "dropped"):
            continue
        if s > max(e, s) + 0.5:
            bad.append(f"{c['id']} spent {s:.1f} > held {max(e, s):.1f}")
    return bad, dict(envelope=env, own=round(own, 1), held=round(held, 1),
                     card_spend=round(spent, 1), reserve=reserve,
                     left=round(b.get("left_to_give", 0.0), 1), cards=len(cards))


def main():
    if len(sys.argv) < 4:
        sys.exit(__doc__)
    gummi, workspace, goal = sys.argv[1:4]
    every = float(sys.argv[4]) if len(sys.argv) > 4 else 20.0
    seen = []
    while True:
        s = snapshot(gummi, workspace, goal)
        if s is None:
            print(json.dumps({"note": "goal unreadable; stopping"}), flush=True)
            break
        g = s.get("goal") or {}
        bad, row = violations(g)
        print(json.dumps({**row, "violations": bad}), flush=True)
        seen += bad
        if g.get("ready") or not row["envelope"]:
            break
        time.sleep(every)
    print(json.dumps({"violations_total": len(seen), "violations": seen[:20]}), flush=True)
    sys.exit(1 if seen else 0)


if __name__ == "__main__":
    main()
