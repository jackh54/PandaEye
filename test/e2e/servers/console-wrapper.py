#!/usr/bin/env python3
"""Headless console wrapper for a Minecraft server.

Starts the server process with a pipe as stdin and forwards every line written to the FIFO
<dir>/console.in to it (so `echo "say hi" > console.in` runs a console command). The FIFO is opened
O_RDWR by the wrapper, so writers never block once the wrapper runs and the wrapper never sees EOF.
stdout/stderr go to <dir>/logs/console.out (append). Commands are logged to <dir>/logs/commands.log.
On SIGTERM/SIGINT the wrapper sends "stop", waits up to --stop-timeout seconds, then kills the server.
Exit code = the server's exit code.

usage: console-wrapper.py --dir DIR [--stop-timeout 60] -- java -jar server.jar ...
"""
import argparse
import os
import select
import signal
import subprocess
import sys
import time


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", required=True)
    ap.add_argument("--stop-timeout", type=float, default=60.0)
    ap.add_argument("cmd", nargs=argparse.REMAINDER)
    a = ap.parse_args()
    cmd = a.cmd[1:] if a.cmd and a.cmd[0] == "--" else a.cmd
    if not cmd:
        ap.error("missing server command")
    d = os.path.abspath(a.dir)
    os.makedirs(os.path.join(d, "logs"), exist_ok=True)
    fifo = os.path.join(d, "console.in")
    if os.path.isfile(fifo):  # a stray regular file (e.g. someone echoed into it while no wrapper ran)
        os.unlink(fifo)
    if not os.path.exists(fifo):
        os.mkfifo(fifo, 0o600)
    out = open(os.path.join(d, "logs", "console.out"), "ab", buffering=0)
    cmdlog = open(os.path.join(d, "logs", "commands.log"), "a", buffering=1)
    proc = subprocess.Popen(cmd, cwd=d, stdin=subprocess.PIPE, stdout=out, stderr=subprocess.STDOUT)
    with open(os.path.join(d, "server.pid"), "w") as f:
        f.write(str(proc.pid))
    with open(os.path.join(d, "wrapper.pid"), "w") as f:
        f.write(str(os.getpid()))

    stopping = {"at": None}

    def send(line: str) -> None:
        try:
            proc.stdin.write((line.rstrip("\n") + "\n").encode())
            proc.stdin.flush()
            cmdlog.write(f"{time.strftime('%H:%M:%S')} {line.rstrip()}\n")
        except (BrokenPipeError, ValueError):
            pass

    def on_signal(signum, _frame):
        if stopping["at"] is None:
            stopping["at"] = time.time()
            send("stop")

    signal.signal(signal.SIGTERM, on_signal)
    signal.signal(signal.SIGINT, on_signal)

    fd = os.open(fifo, os.O_RDWR | os.O_NONBLOCK)
    buf = b""
    try:
        while proc.poll() is None:
            if stopping["at"] is not None and time.time() - stopping["at"] > a.stop_timeout:
                proc.kill()
                break
            try:
                r, _, _ = select.select([fd], [], [], 0.25)
            except InterruptedError:
                continue
            if not r:
                continue
            try:
                chunk = os.read(fd, 65536)
            except BlockingIOError:
                continue
            buf += chunk
            while b"\n" in buf:
                line, buf = buf.split(b"\n", 1)
                s = line.decode("utf-8", "replace").strip()
                if s:
                    send(s)
        code = proc.wait()
    finally:
        os.close(fd)
        for f in ("server.pid", "wrapper.pid"):
            try:
                os.unlink(os.path.join(d, f))
            except FileNotFoundError:
                pass
    out.write(f"\n[console-wrapper] server exited with code {code}\n".encode())
    return code


if __name__ == "__main__":
    sys.exit(main())
