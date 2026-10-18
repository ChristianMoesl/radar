#!/usr/bin/env python3
"""Opt-in native WSL filesystem qualification. Never use the real vault or SBX HOME.

Prepare/authenticate a dedicated HOME first, then see --help. The caller retains
that installation and credentials. Only this probe's sandbox/fixtures are removed.
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import uuid

CACHE = "DOCKER_SANDBOXES_ENABLE_VIRTIOFS_CACHE"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cli", required=True, help="Absolute native SBX 0.48.x executable")
    parser.add_argument("--isolated-home", required=True, help="Dedicated authenticated test HOME, never your real HOME")
    parser.add_argument("--windows-temp", required=True, help="Existing disposable Windows-backed parent directory under /mnt/c")
    parser.add_argument("--kit", default="shell", help="Kit with python3 and git; qualify radar-kit separately too")
    args = parser.parse_args()
    if os.environ.get("RADAR_SBX_NATIVE_WSL_E2E") != "1":
        parser.error("explicitly set RADAR_SBX_NATIVE_WSL_E2E=1")
    if sys.platform != "linux" or "microsoft" not in Path("/proc/sys/kernel/osrelease").read_text().lower():
        parser.error("this probe requires native Linux SBX inside WSL2")
    cli, home, windows = map(lambda p: Path(p).expanduser().resolve(), (args.cli, args.isolated_home, args.windows_temp))
    if not cli.is_file() or cli.suffix == ".exe":
        parser.error("--cli must be an installed native executable")
    if not home.is_dir() or home == Path.home().resolve() or home in (Path("/"), Path("/home")):
        parser.error("provide a pre-existing dedicated test HOME (never the normal HOME)")
    if not windows.is_dir() or not windows.is_relative_to("/mnt/c"):
        parser.error("--windows-temp must be an existing parent under /mnt/c, never the vault")
    # Check access without allocating a VM or changing permissions.
    with open("/dev/kvm", "rb+"):
        pass
    safe_keys = {"PATH", "LANG", "LC_ALL", "TERM", "USER", "LOGNAME", "WSL_DISTRO_NAME", "WSL_INTEROP", "XDG_RUNTIME_DIR", "LD_LIBRARY_PATH", "SSL_CERT_FILE", "SSL_CERT_DIR"}
    env = dict({key: value for key, value in os.environ.items() if key in safe_keys}, HOME=str(home), DOCKER_CONFIG=str(home / "docker"),
               GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL="/dev/null", XDG_CONFIG_HOME=str(home / "config"),
               XDG_DATA_HOME=str(home / "data"), XDG_CACHE_HOME=str(home / "cache"),
               XDG_STATE_HOME=str(home / "state"), **{CACHE: "0"})

    def run(argv, cwd=None, stdin=None, timeout=180):
        result = subprocess.run(list(map(str, argv)), cwd=cwd, env=env, input=stdin,
                                text=True, capture_output=True, timeout=timeout)
        if result.returncode:
            # CLI/kit output may contain credentials. Inspect isolated logs explicitly.
            raise RuntimeError(f"{Path(str(argv[0])).name} {argv[1]} failed (exit {result.returncode}); inspect isolated diagnostics under {home}")
        return result.stdout.strip()

    def sbx(*argv, **kwargs):
        return run([cli, *argv], **kwargs)

    version = json.loads(sbx("version", "--json"))
    if not version["client"]["version"].startswith("v0.48."):
        parser.error("this matrix qualifies SBX 0.48.x only")
    status = json.loads(sbx("daemon", "status", "--json"))
    running_before = status.get("status") == "running"
    if running_before:
        pid = (Path(status["socket"]).parent / "sandboxd.pid").read_text().strip()
        environ = Path(f"/proc/{int(pid)}/environ").read_bytes().split(b"\0")
        if f"{CACHE}=0".encode() not in environ:
            parser.error("isolated daemon lacks cache opt-out; deliberately restart that test daemon first")
    root = note_dir = None
    created = False
    name = "radar-native-wsl-" + uuid.uuid4().hex[:16]
    guest_checks = host_checks = 0
    try:
        inventory = json.loads(sbx("ls", "--json"))
        if inventory != []:
            parser.error("dedicated test HOME must have an empty sandbox inventory; nothing removed")
        root = Path(tempfile.mkdtemp(prefix="radar-native-wsl-"))
        note_dir = Path(tempfile.mkdtemp(prefix="radar-native-note-", dir=windows))
        anchor, repository, shared, readonly = [root / p for p in ("workspace with spaces", "external repository", "shared files", "readonly files")]
        for directory in (anchor, repository, shared, readonly):
            directory.mkdir()
        note = note_dir / "task-note.md"
        note.write_text("synthetic note\n")
        (anchor / "plain.txt").write_text("plain\n")
        (anchor / "plain.txt").chmod(0o644)
        (anchor / "script.sh").write_text("#!/bin/sh\nprintf executable")
        (anchor / "script.sh").chmod(0o755)
        (anchor / "relative-link").symlink_to("plain.txt")
        (anchor / "absolute-link").symlink_to(anchor / "plain.txt")
        (anchor / "notes.md").symlink_to(note)
        (readonly / "fixture").write_text("read only\n")
        member = anchor / "member"
        run(["git", "init", "-b", "main", repository])
        for key, value in (("user.name", "Example Developer"), ("user.email", "developer@example.com")):
            run(["git", "-C", repository, "config", key, value])
        (repository / "tracked").write_text("initial\n")
        run(["git", "-C", repository, "add", "tracked"])
        run(["git", "-C", repository, "commit", "-m", "Initial fixture"])
        run(["git", "-C", repository, "worktree", "add", "-b", "probe", member])
        mounts = [str(anchor), str(repository / ".git"), str(shared), str(readonly) + ":ro", str(note_dir)]
        create = ["create", "--name", name, "--skills", "off", args.kit, *mounts]
        # The operator initializes deny-all only in this dedicated test HOME.
        for phase in range(4):
            if phase == 0:
                created = True  # Also clean up a partially failed create.
                sbx(*create, timeout=600)
            elif phase == 1:
                temp = note.with_suffix(".tmp")
                temp.write_text("host atomic update\n")
                os.replace(temp, note)
            elif phase == 2:
                sbx("stop", name)
                sbx("exec", "--workdir", anchor, name, "true")  # exec starts a stopped sandbox.
            else:
                sbx("rm", "--force", name)
                sbx(*create, timeout=600)
            payload = dict(anchor=str(anchor), note=str(note), shared=str(shared), readonly=str(readonly), member=str(member), phase=phase, expected=note.read_text())
            script = GUEST.replace("__PAYLOAD__", repr(json.dumps(payload)))
            output = sbx("exec", "--workdir", anchor, name, "python3", "-c", script)
            result = json.loads(output)
            assert result["checks"] == 24, result
            guest_checks += result["checks"]
            marker = f"roundtrip-{phase}"
            checks = [
                note.read_text() == result["note"],
                (anchor / "notes.md").is_symlink() and os.readlink(anchor / "notes.md") == str(note),
                (anchor / "roundtrip").read_text() == marker,
                (note_dir / "roundtrip").read_text() == marker,
                (shared / "roundtrip").read_text() == marker,
                sorted(p.name for p in readonly.iterdir()) == ["fixture"] and (readonly / "fixture").read_text() == "read only\n",
                run(["git", "-C", member, "status", "--porcelain"]) == "",
                "error" not in run(["git", "-C", repository, "fsck"]).lower(),
            ]
            assert all(checks), f"phase {phase}: failed host checks {checks}"
            host_checks += len(checks)
            print(json.dumps(dict(phase=phase, guest=24, host=8)), flush=True)
        print(json.dumps(dict(result="passed", guest_checks=guest_checks, host_checks=host_checks,
                              version=version["client"], kernel=os.uname().release, cache=0, kit=args.kit, mounts=mounts)))
    finally:
        # Keep fixtures if cleanup fails: deleting mounts under a live VM is unsafe.
        if created:
            try:
                sbx("rm", "--force", name)
                remaining = json.loads(sbx("ls", "--json"))
                if any(row.get("name") == name for row in remaining):
                    raise RuntimeError("sandbox still listed")
            except Exception as error:
                raise RuntimeError(f"cleanup incomplete: inspect/remove {name}; fixtures retained at {root} and {note_dir}") from error
        if not running_before:
            sbx("daemon", "stop")
        if root is not None:
            shutil.rmtree(root)
        if note_dir is not None:
            shutil.rmtree(note_dir)


GUEST = r'''
import errno, json, os, stat, subprocess
from pathlib import Path
p=json.loads(__PAYLOAD__)
a,n,s,ro,m=[Path(p[k]) for k in ('anchor','note','shared','readonly','member')]
checks=0
def check(value):
 global checks
 assert value, f"check {checks+1} failed in phase {p['phase']}"
 checks+=1
def git(*args):
 return subprocess.check_output(['git','-C',str(m),*args],stderr=subprocess.PIPE,text=True).strip()
check(os.getcwd()==str(a))
check((a/'plain.txt').read_text()=='plain\n')
check(stat.S_IMODE((a/'plain.txt').stat().st_mode)==0o644)
check(os.readlink(a/'relative-link')=='plain.txt')
check((a/'relative-link').read_text()=='plain\n')
check(os.readlink(a/'absolute-link')==str(a/'plain.txt'))
check((a/'absolute-link').read_text()=='plain\n')
check(os.readlink(a/'notes.md')==str(n))
check((a/'notes.md').read_text()==p['expected'])
check(n.read_text()==p['expected'])
with (a/'notes.md').open('a') as f: f.write('guest append\n')
check(n.read_text()==p['expected']+'guest append\n')
target=(a/'notes.md').resolve(strict=True)
tmp=target.with_suffix('.tmp')
with tmp.open('w') as f:
 f.write('atomic save\n'); f.flush(); os.fsync(f.fileno())
os.replace(tmp,target)
check(n.read_text()=='atomic save\n' and n.name in os.listdir(n.parent) and (a/'notes.md').is_symlink())
link=a/'new-link'
if link.is_symlink(): link.unlink()
link.symlink_to('plain.txt')
check(link.read_text()=='plain\n')
(a/'script.sh').chmod(0o755)
check(subprocess.check_output([str(a/'script.sh')],text=True)=='executable')
marker=f"roundtrip-{p['phase']}"
for directory in (a,n.parent,s):
 (directory/'roundtrip').write_text(marker); check((directory/'roundtrip').read_text()==marker)
check((ro/'fixture').read_text()=='read only\n')
try:
 (ro/'forbidden').write_text('forbidden')
except OSError as error:
 check(error.errno in (errno.EACCES,errno.EROFS,errno.EPERM))
else:
 raise AssertionError('read-only mount accepted a write')
# Stress listing coherence with several pre-existing files, not only a lone target.
for i in range(50):
 temp=n.parent/'rename.tmp'; final=n.parent/'rename.txt'
 temp.write_text(str(i)); os.replace(temp,final)
 assert final.read_text()==str(i) and final.name in os.listdir(n.parent), f'rename/listing {i}'
check(True)
check(git('status','--porcelain')=='')
git('config','probe.phase',str(p['phase']))
check(git('config','--get','probe.phase')==str(p['phase']))
(m/'tracked').write_text(f"phase {p['phase']}\n")
git('add','tracked'); git('commit','-m','Fixture phase')
check(git('status','--porcelain')=='')
extra=a/'extra'
git('worktree','add','-b','extra-probe',str(extra),'HEAD')
git('worktree','remove',str(extra)); git('branch','-D','extra-probe')
check(not extra.exists())
print(json.dumps(dict(checks=checks,note=n.read_text())))
'''

if __name__ == "__main__":
    main()
