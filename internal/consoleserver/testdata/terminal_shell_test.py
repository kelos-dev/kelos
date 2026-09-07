import errno
import os
import pty
import select
import signal
import sys
import tempfile
import time
from pathlib import Path


def read_prompt(fd):
    output = b""
    deadline = time.monotonic() + 5
    while b"kelos-test> " not in output:
        remaining = deadline - time.monotonic()
        if remaining <= 0 or not select.select([fd], [], [], remaining)[0]:
            raise AssertionError(f"Shell did not return to its prompt: {output!r}")
        try:
            chunk = os.read(fd, 65536)
        except OSError as error:
            if error.errno != errno.EIO:
                raise
            chunk = b""
        if not chunk:
            raise AssertionError(f"Shell exited before its prompt: {output!r}")
        output += chunk
    return output


with tempfile.TemporaryDirectory() as directory:
    Path(directory, ".bashrc").write_text("PS1='kelos-test> '\n")
    Path(directory, ".hushlogin").touch()
    Path(directory, "completion-file").write_text("file-completed\n")
    command = Path(directory, "kelos-completion-command")
    command.write_text("#!/bin/sh\nprintf 'command-completed\\n'\n")
    command.chmod(0o755)
    pid, fd = pty.fork()
    if pid == 0:
        os.chdir(directory)
        os.execve(sys.argv[1], sys.argv[1:], {
            "PATH": f"{directory}:/usr/bin:/bin",
            "HOME": directory,
            "INPUTRC": "/dev/null",
            "HISTFILE": "/dev/null",
        })
    try:
        read_prompt(fd)
        for line, expected in [
            (b"cat completion-fi\t\r", b"file-completed\r\n"),
            (b"kelos-completion-com\t\r", b"command-completed\r\n"),
        ]:
            os.write(fd, line)
            output = read_prompt(fd)
            assert expected in output, f"Tab completion failed: {output!r}"
    finally:
        os.close(fd)
        try:
            os.kill(pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        os.waitpid(pid, 0)
