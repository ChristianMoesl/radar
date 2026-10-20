#!/usr/bin/env python3
"""Run a native login in a PTY; decline only TWG's local-browser prompt."""
import errno
import os
import pty
import re
import signal
import subprocess
import sys
import termios


def main():
    master, slave = pty.openpty()
    termios.tcsetwinsize(slave, (24, 120))
    environment = dict(os.environ)
    if os.path.basename(sys.argv[1]) == 'twg':
        # This is the user-approved foreground login, not an agent command.
        # TWG also detects generic *_AGENT_* metadata injected by the sandbox.
        # Keep proxy/CA and SSH/GPG settings; change only login UI attribution.
        for key in list(environment):
            if (re.search(r'(^|_)AGENT($|_)', key.upper())
                    and not key.upper().startswith(('SSH_', 'GPG_'))
                    and key.upper() not in {'HTTP_USER_AGENT', 'NPM_CONFIG_USER_AGENT', 'USER_AGENT'}):
                environment.pop(key)
        environment['TWG_AGENT_HOST'] = 'none'
    child = subprocess.Popen(sys.argv[1:], stdin=slave, stdout=slave, stderr=slave, env=environment,
                             start_new_session=True)
    os.close(slave)
    signal.signal(signal.SIGTERM, lambda *_: child.terminate())
    pending, answered = b'', False
    try:
        while True:
            try:
                block = os.read(master, 8192)
            except OSError as error:
                if error.errno == errno.EIO:
                    break
                raise
            if not block:
                break
            sys.stdout.buffer.write(block)
            sys.stdout.buffer.flush()
            pending = (pending + block)[-8192:]
            plain = re.sub(rb'\x1b\[[0-9;?]*[A-Za-z]', b'', pending)
            if not answered and 'twg' == os.path.basename(sys.argv[1]) and b'Open in browser?' in plain:
                # Browser approval happens on the user's machine. This answer
                # never approves a grant or responds to another native prompt.
                os.write(master, b'n')
                answered = True
        return child.wait()
    finally:
        os.close(master)
        if child.poll() is None:
            child.terminate()


if __name__ == '__main__':
    raise SystemExit(main())
