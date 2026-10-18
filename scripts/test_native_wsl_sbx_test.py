"""Exercise the probe's guest assertions locally, not as SBX qualification."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("probe", Path(__file__).with_name("test-native-wsl-sbx.py"))
probe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(probe)


class GuestFixtureTest(unittest.TestCase):
    @unittest.skipIf(os.getuid() == 0, "local permission fixture requires an unprivileged user")
    def test_all_24_assertions_across_repeated_phases(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            anchor, repo, shared, readonly, note_dir = [root / name for name in ("workspace with spaces", "external repository", "shared", "readonly", "synthetic windows directory")]
            for directory in (anchor, repo, shared, readonly, note_dir):
                directory.mkdir()
            note = note_dir / "task-note.md"
            note.write_text("synthetic\n")
            (anchor / "plain.txt").write_text("plain\n")
            (anchor / "plain.txt").chmod(0o644)
            (anchor / "script.sh").write_text("#!/bin/sh\nprintf executable")
            (anchor / "script.sh").chmod(0o755)
            (anchor / "relative-link").symlink_to("plain.txt")
            (anchor / "absolute-link").symlink_to(anchor / "plain.txt")
            (anchor / "notes.md").symlink_to(note)
            (readonly / "fixture").write_text("read only\n")
            readonly.chmod(0o555)
            def git(*args):
                subprocess.run(["git", "-C", str(repo), *args], check=True, capture_output=True)
            git("init", "-b", "main")
            git("config", "user.name", "Example Developer")
            git("config", "user.email", "developer@example.com")
            (repo / "tracked").write_text("fixture\n")
            git("add", "tracked")
            git("commit", "-m", "Initial fixture")
            member = anchor / "member"
            git("worktree", "add", "-b", "probe", str(member))
            try:
                for phase in range(4):
                    data = dict(anchor=str(anchor), note=str(note), shared=str(shared), readonly=str(readonly), member=str(member), phase=phase, expected=note.read_text())
                    script = probe.GUEST.replace("__PAYLOAD__", repr(json.dumps(data)))
                    result = subprocess.run(["python3", "-c", script], cwd=anchor, check=True, capture_output=True, text=True)
                    self.assertEqual(json.loads(result.stdout)["checks"], 24)
            finally:
                readonly.chmod(0o755)


if __name__ == "__main__":
    unittest.main()
