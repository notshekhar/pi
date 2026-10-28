#!/usr/bin/env python3
"""Run the same keystroke script against loop and pi and show both screens.

    scripts/parity.py 'hi|w1|\r|w20'
    scripts/parity.py --color '/|w2'        # also mark cells whose colour differs
    LOOP_ARGS='-m xai/grok-4.7' PI_ARGS='-model xai/grok-4.7' scripts/parity.py ...

The two screens are printed one above the other, then a line diff of the text.
With --color, rows whose text matches but whose colours do not are listed with
the first differing column, since "looks the same in plain text" is how most
of the remaining gaps hid.

Steps use screenshot.py's grammar: `w<seconds>` waits, anything else is typed.
"""

import difflib
import os
import shlex
import sys

sys.path.insert(0, os.path.dirname(__file__))
import screenshot  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))
LOOP = os.environ.get(
    "LOOP_CMD", "bun " + os.path.join(HERE, "../../loop/packages/cli/src/cli.ts")
)
PI = os.environ.get("PI_CMD", os.path.join(HERE, "../pi"))


def main():
    args = sys.argv[1:]
    color = "--color" in args
    history = "--history" in args
    args = [a for a in args if a not in ("--color", "--history")]
    if not args:
        sys.exit(__doc__)
    script = args[0].split("|")
    loop_cmd = shlex.split(LOOP) + shlex.split(os.environ.get("LOOP_ARGS", ""))
    pi_cmd = shlex.split(PI) + shlex.split(os.environ.get("PI_ARGS", ""))

    a = screenshot.capture(script, loop_cmd, history)
    b = screenshot.capture(script, pi_cmd, history)

    for name, cap in (("loop", a), ("pi", b)):
        print("=" * 40, name, "=" * 40)
        for line in cap["text"]:
            print("|" + line)
    print("=" * 40, "diff loop→pi", "=" * 40)
    for line in difflib.unified_diff(a["text"], b["text"], "loop", "pi", n=0, lineterm=""):
        print(line)
    if color:
        # Compare every cell where both screens show the same character, and
        # summarise colour mappings: "loop draws X where pi draws Y".
        print("=" * 40, "colour (same char, different style)", "=" * 40)
        pairs = {}
        for y, (ra, rb) in enumerate(zip(a["cells"], b["cells"])):
            ta, tb = a["text"][y].ljust(len(ra)), b["text"][y].ljust(len(rb))
            for x, (ca, cb) in enumerate(zip(ra, rb)):
                if ta[x] != tb[x] or ta[x] == " " and ca[1] == cb[1]:
                    continue
                if ca != cb:
                    key = (ca, cb)
                    if key not in pairs:
                        pairs[key] = [0, y, x, ta[x]]
                    pairs[key][0] += 1
        for (ca, cb), (n, y, x, ch) in sorted(pairs.items(), key=lambda kv: -kv[1][0]):
            print("%4d cells  loop fg=%s bg=%s bold=%s  pi fg=%s bg=%s bold=%s  e.g. row %d col %d %r"
                  % (n, ca[0], ca[1], ca[2], cb[0], cb[1], cb[2], y, x, ch))
        if not pairs:
            print("identical")

if __name__ == "__main__":
    main()
