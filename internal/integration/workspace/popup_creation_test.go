package workspace

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"
)

// The documented dashboard entrypoint is a popup. Switching must expose the
// target session to input without killing the still-provisioning popup process.
// Otherwise reordering Create alone would not make conversation available.
func TestEarlySwitchFromPopupKeepsCreatorAliveAndTargetInteractive(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for the isolated PTY fixture")
	}
	runner := newIsolatedPaneTmux(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	const script = `import errno, fcntl, json, os, pathlib, pty, select, shlex, struct, subprocess, sys, termios, time
socket, directory = sys.argv[1:]
root = pathlib.Path(directory)
def tmux(*args):
    return subprocess.run(['tmux', '-S', socket, *args], capture_output=True, text=True, check=True)
def drain():
    if select.select([master], [], [], 0.02)[0]:
        try: os.read(master, 65536)
        except OSError as error:
            if error.errno != errno.EIO: raise
master, slave = pty.openpty()
fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 40, 120, 0, 0))
client = None
popup_client = None
try:
    target = 'read -r value; printf "%s" "$value" > ' + shlex.quote(str(root/'target-input')) + '; sleep 30'
    tmux('new-session', '-d', '-s', 'target', 'bash -c ' + shlex.quote(target))
    env = dict(os.environ, TERM='xterm-256color')
    client = subprocess.Popen(['tmux', '-S', socket, 'attach-session', '-t', 'fixture'], stdin=slave, stdout=slave, stderr=slave, env=env, start_new_session=True)
    os.close(slave)
    deadline = time.monotonic() + 4
    while not tmux('list-clients', '-F', '#{client_name}').stdout.strip():
        if time.monotonic() > deadline: raise RuntimeError('client did not attach')
        drain()
    popup = root/'popup.sh'
    # read represents the ongoing creation operation. If the popup retained
    # keyboard focus it would consume the input intended for the early agent.
    popup.write_text('trap \'\' HUP TERM\ntmux -S ' + shlex.quote(socket) + ' switch-client -t target\ntmux -S ' + shlex.quote(socket) + ' display-popup -C\nprintf switched > ' + shlex.quote(str(root/'switched')) + '\nprintf "" > ' + shlex.quote(str(root/'popup-input')) + '\nwhile [ ! -e ' + shlex.quote(str(root/'release')) + ' ]; do\n  if read -t 1 -r value; then printf "%s" "$value" > ' + shlex.quote(str(root/'popup-input')) + '; fi\n  sleep 0.01\ndone\nprintf completed > ' + shlex.quote(str(root/'completed')) + '\n')
    popup_client = subprocess.Popen(['tmux', '-S', socket, 'display-popup', '-E', 'bash ' + shlex.quote(str(popup))], stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    deadline = time.monotonic() + 4
    while not (root/'switched').exists():
        if time.monotonic() > deadline: raise RuntimeError('popup did not switch client')
        drain()
    os.write(master, b'conversation-input\r')
    deadline = time.monotonic() + 4
    while not (root/'target-input').exists():
        if time.monotonic() > deadline: raise RuntimeError('target not interactive during creation')
        drain()
    if (root/'completed').exists(): raise RuntimeError('creator completed before release')
    (root/'release').touch()
    while not (root/'completed').exists():
        if time.monotonic() > deadline: raise RuntimeError('creator did not survive switch')
        drain()
    print(json.dumps({name:(root/name).read_text() for name in ['target-input', 'popup-input', 'completed']}))
finally:
    if client:
        client.terminate()
        try: client.wait(timeout=2)
        except subprocess.TimeoutExpired: client.kill(); client.wait()
    if popup_client:
        popup_client.terminate()
        popup_client.wait(timeout=2)
    os.close(master)
`
	output, err := exec.CommandContext(ctx, "python3", "-c", script, runner.socket, t.TempDir()).CombinedOutput()
	if err != nil {
		t.Fatalf("isolated popup fixture: %v\n%s", err, output)
	}
	var result map[string]string
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("fixture output: %v\n%s", err, output)
	}
	if result["target-input"] != "conversation-input" || result["popup-input"] != "" || result["completed"] != "completed" {
		t.Fatalf("early target/creator behavior: %#v", result)
	}
}
