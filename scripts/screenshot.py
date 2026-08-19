#!/usr/bin/env python3
"""Drive a TUI binary in a real pty and print the screen it rendered.

UI fidelity cannot be checked by reading code. Every parity gap found in this
project — a cursor two columns off, three blanks where loop has two, a masthead
anchored to the wrong edge — was invisible in the source and obvious on screen.
This runs the binary for real, types at it, and prints what a terminal would
show, so a claim about the UI can be verified instead of asserted.

    scripts/screenshot.py 'hello|w1|\r|w20' ./pi -provider deepseek
    ROWS=24 scripts/screenshot.py --history '/|w2' ./pi

The script is a `|`-separated list of steps:

    w<seconds>   wait, letting the app render
    anything else  type it (\r for Enter, \x05 for ctrl+e, \x1b[A for Up)

--history also prints what scrolled off into the terminal's scrollback, which
is the only way to check that finished blocks are actually being handed to the
terminal rather than overwritten in place.

Needs pyte:  pip install pyte
"""

import fcntl
import os
import pty
import select
import struct
import sys
import termios
import time

try:
    import pyte
except ImportError:
    sys.exit("scripts/screenshot.py needs pyte: pip install pyte")

COLS = int(os.environ.get("COLS", "100"))
ROWS = int(os.environ.get("ROWS", "40"))

# Escape sequences must be written in ONE call. Split across writes with a
# delay between them, the app's decoder times out the ESC and reports a bare
# Esc — a harness bug that looks exactly like a broken key binding, and cost
# an afternoon of hunting a working arrow-key handler.
SEQ_FINAL = "ABCDRmZ~"


def run(script, cmd, history=False):
    pid, fd = pty.fork()
    if pid == 0:
        os.environ["TERM"] = "xterm-256color"
        os.execvp(cmd[0], cmd)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))

    out = bytearray()

    def drain(seconds):
        end = time.time() + seconds
        while time.time() < end:
            r, _, _ = select.select([fd], [], [], 0.2)
            if not r:
                continue
            try:
                chunk = os.read(fd, 65536)
            except OSError:
                return False
            if not chunk:
                return False
            out.extend(chunk)
        return True

    drain(float(os.environ.get("SETTLE", "6")))  # masthead, probes, first paint
    for step in script:
        if step.startswith("w"):
            drain(float(step[1:]))
            continue
        text = step.encode().decode("unicode_escape")
        i = 0
        while i < len(text):
            if text[i] == "\x1b":
                j = i + 1
                while j < len(text) and text[j] not in SEQ_FINAL:
                    j += 1
                piece, i = text[i : j + 1], j + 1
            else:
                piece, i = text[i], i + 1
            try:
                os.write(fd, piece.encode())
            except OSError:
                break
            time.sleep(0.08)
    drain(4)

    screen = pyte.HistoryScreen(COLS, ROWS, history=800)
    pyte.Stream(screen).feed(out.decode("utf8", "replace"))

    if history:
        top = [
            "".join(line[i].data for i in range(COLS)).rstrip()
            for line in screen.history.top
        ]
        print("=== scrollback (%d lines) ===" % len(top))
        for line in top:
            print("|" + line)
        print("=== screen ===")
    for line in screen.display:
        print(line.rstrip())

    try:
        os.kill(pid, 9)
    except OSError:
        pass


def main():
    args = sys.argv[1:]
    history = "--history" in args
    if history:
        args.remove("--history")
    if len(args) < 2:
        sys.exit(__doc__)
    run(args[0].split("|"), args[1:], history)


if __name__ == "__main__":
    main()
