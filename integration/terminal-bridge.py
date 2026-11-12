"""Minimal local PTY bridge for the isolated official Codex TUI test."""
import os
import pty
import select
import signal
import struct
import subprocess
import sys
import termios
import fcntl

master, slave = pty.openpty()
fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 45, 150, 0, 0))
child = subprocess.Popen(sys.argv[1:], stdin=slave, stdout=slave, stderr=slave, start_new_session=True)
os.close(slave)

def stop(_signum=None, _frame=None):
    if child.poll() is None:
        os.killpg(child.pid, signal.SIGTERM)

signal.signal(signal.SIGTERM, stop)
signal.signal(signal.SIGINT, stop)
try:
    while child.poll() is None:
        readable, _, _ = select.select([master, sys.stdin.buffer], [], [], 0.1)
        for source in readable:
            if source == master:
                try:
                    data = os.read(master, 65536)
                except OSError:
                    break
                if not data:
                    break
                for query, answer in [(b'\x1b[6n', b'\x1b[1;1R'), (b'\x1b[?u', b'\x1b[?0u'), (b'\x1b[c', b'\x1b[?1;2c')]:
                    if query in data:
                        os.write(master, answer)
                sys.stdout.buffer.write(data)
                sys.stdout.buffer.flush()
            else:
                data = os.read(sys.stdin.fileno(), 65536)
                if not data:
                    stop()
                    break
                os.write(master, data)
finally:
    stop()
    try:
        child.wait(timeout=5)
    except subprocess.TimeoutExpired:
        os.killpg(child.pid, signal.SIGKILL)
        child.wait()
    os.close(master)
sys.exit(child.returncode if child.returncode >= 0 else 1)
