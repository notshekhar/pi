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
import re
import os
import pty
import select
import struct
import sys
import termios
import time

try:
    import pyte
    import pyte.screens
except ImportError:
    sys.exit("scripts/screenshot.py needs pyte: pip install pyte")

# pyte 0.8.2 cannot parse a Device Status Report (\x1b[?6n), which loop sends;
# it raises TypeError because report_device_status takes no `private` kwarg.
# The DSR response is irrelevant to what we render, so drop it.
pyte.screens.Screen.report_device_status = lambda self, mode, **kw: None

COLS = int(os.environ.get("COLS", "100"))
ROWS = int(os.environ.get("ROWS", "40"))

# Escape sequences must be written in ONE call. Split across writes with a
# delay between them, the app's decoder times out the ESC and reports a bare
# Esc — a harness bug that looks exactly like a broken key binding, and cost
# an afternoon of hunting a working arrow-key handler.
SEQ_FINAL = "ABCDRmZ~"


def run(script, cmd, history=False):
    cap = capture(script, cmd, history)
    if history:
        print("=== scrollback (%d lines) ===" % len(cap["history"]))
        for line in cap["history"]:
            print("|" + line)
        print("=== screen ===")
    for line in cap["text"]:
        print(line)


def capture(script, cmd, history=False):
    """Run cmd in a pty, play script, and return the rendered screen.

    Returns {"text": [row...], "cells": [[(fg, bg, bold)...]...],
    "history": [row...]}. The child is killed with its whole process group,
    so nothing it spawned (MCP servers, LSPs) outlives the capture.
    """
    pid, fd = pty.fork()
    if pid == 0:
        os.environ["TERM"] = "xterm-256color"
        # CHILD_HOME isolates the app's config without moving python's own
        # HOME (which is where pyte is installed).
        if os.environ.get("CHILD_HOME"):
            os.environ["HOME"] = os.environ["CHILD_HOME"]
        os.execvp(cmd[0], cmd)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", ROWS, COLS, 0, 0))

    out = bytearray()
    answered = [0]

    # Answer the queries a real terminal answers, opt-in: TERM_BG=#rrggbb
    # replies to OSC 11 (background colour), TERM_SCHEME=dark|light to
    # CSI ? 996 n, and DA1 is always answered (every terminal does).
    def answer():
        data = bytes(out[answered[0]:])
        answered[0] = len(out)
        bg = os.environ.get("TERM_BG")
        replies = b""
        for m in re.finditer(rb"\x1b\]11;\?(\x07|\x1b\\)|\x1b\[\?996n|\x1b\[0?c", data):
            q = m.group(0)
            if q.startswith(b"\x1b]11") and bg:
                h = bg.lstrip("#")
                replies += ("\x1b]11;rgb:%s%s/%s%s/%s%s\x1b\\" % (h[0:2], h[0:2], h[2:4], h[2:4], h[4:6], h[4:6])).encode()
            elif q == b"\x1b[?996n" and os.environ.get("TERM_SCHEME"):
                replies += b"\x1b[?997;%dn" % (2 if os.environ["TERM_SCHEME"] == "light" else 1)
            elif q.endswith(b"c"):
                replies += b"\x1b[?62;22c"
        if replies:
            os.write(fd, replies)

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
            answer()
        return True

    drain(float(os.environ.get("SETTLE", "6")))  # masthead, probes, first paint
    for step in script:
        # Only `w<number>` waits; "write a story" is text to type.
        if re.fullmatch(r"w[0-9.]+", step):
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

    top = [
        "".join(line[i].data for i in range(COLS)).rstrip()
        for line in screen.history.top
    ] if history else []
    text = [line.rstrip() for line in screen.display]
    cells = [
        [(screen.buffer[y][x].fg, screen.buffer[y][x].bg, screen.buffer[y][x].bold)
         for x in range(COLS)]
        for y in range(ROWS)
    ]

    # pty.fork makes the child a session leader, so its pid is its pgid:
    # killing the group takes the MCP servers and LSPs it started with it.
    for sig in (15, 9):
        try:
            os.killpg(pid, sig)
        except OSError:
            pass
        time.sleep(0.2)
    # Close the pty BEFORE reaping: a child killed mid-write sits in the
    # exiting state until its terminal is gone, and a blocking waitpid on it
    # hangs the harness forever.
    os.close(fd)
    for _ in range(50):
        try:
            if os.waitpid(pid, os.WNOHANG)[0] != 0:
                break
        except OSError:
            break
        time.sleep(0.1)
    return {"text": text, "cells": cells, "history": top}


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
