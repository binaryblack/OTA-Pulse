#!/usr/bin/env python3
"""TASK-S109-018: bootloader-mode drop-in that lets otapulse-machine-id restore the persistent id
into the read-only transient /etc/machine-id systemd bind-mounts on the first boot of a freshly
written A/B slot.

  * the drop-in's ExecStartPre unmounts /etc/machine-id ONLY when it is a mount point
    (run for real in /bin/sh with a fake mountinfo and a recording umount)
  * its failure is tolerated ("-" prefix): a slot with a committed machine-id has nothing to unmount
  * it is installed in bootloader mode only; the native part of the recipe never mentions it, so every
    native task signature and native image stays exactly as it was (native-unchanged gate)

Run: python3 -m pytest test_bl_machine_id_dropin.py
"""

from __future__ import annotations

import re
import shlex
import subprocess
from pathlib import Path

HERE = Path(__file__).resolve().parent
RECIPE = (HERE / "otapulse-firstboot_1.0.bb").read_text()
DROPIN = (HERE / "files" / "10-otapulse-bl-machine-id.conf").read_text()
SCRIPT = (HERE / "files" / "otapulse-machine-id").read_text()


def _pre_command() -> str:
    m = re.search(r"^ExecStartPre=-/bin/sh -c (\".*\")$", DROPIN, re.M)
    assert m, "ExecStartPre=-/bin/sh -c \"...\" not found in the drop-in"
    return shlex.split(m.group(1))[0]


def _run(tmp: Path, mountinfo: str) -> list[str]:
    (tmp / "mountinfo").write_text(mountinfo)
    stubs = tmp / "bin"
    stubs.mkdir()
    (stubs / "umount").write_text('#!/bin/sh\necho "$@" >> "$UMOUNT_LOG"\n')
    (stubs / "umount").chmod(0o755)
    cmd = _pre_command().replace("/proc/self/mountinfo", str(tmp / "mountinfo"))
    log = tmp / "umount.log"
    subprocess.run(
        ["/bin/sh", "-c", cmd],
        env={"PATH": f"{stubs}:/usr/bin:/bin", "UMOUNT_LOG": str(log)},
        check=False,
    )
    return log.read_text().split() if log.exists() else []


TRANSIENT = (
    "27 25 0:23 / /run rw,nosuid - tmpfs tmpfs rw\n"
    "40 1 0:25 /machine-id /etc/machine-id ro,nosuid,nodev,noexec - tmpfs tmpfs rw,size=1M\n"
)
COMMITTED = "27 25 0:23 / /run rw,nosuid - tmpfs tmpfs rw\n28 1 179:3 / / rw - ext4 /dev/root rw\n"


def test_unmounts_the_transient_machine_id(tmp_path):
    assert _run(tmp_path, TRANSIENT) == ["/etc/machine-id"]


def test_leaves_a_committed_machine_id_alone(tmp_path):
    assert _run(tmp_path, COMMITTED) == []


def test_a_path_that_merely_contains_the_name_is_not_matched(tmp_path):
    assert _run(tmp_path, "50 1 0:30 / /etc/machine-id.d rw - tmpfs t rw\n") == []


def test_failure_of_the_pre_step_never_fails_the_unit():
    assert re.search(r"^ExecStartPre=-", DROPIN, re.M), "the '-' prefix must tolerate 'nothing to unmount'"


def test_the_restore_script_is_unchanged_in_shape():
    # the script itself is untouched (native-unchanged); the fix lives in the bootloader-only drop-in
    assert 'cp "$PERSIST" /etc/machine-id' in SCRIPT


def test_installed_in_bootloader_mode_only():
    head = RECIPE.split(":otapulse-bootloader", 1)[0]
    assert "10-otapulse-bl-machine-id" not in head
    assert "otapulse-machine-id.service.d" not in head
    assert re.search(r"SRC_URI:append:otapulse-bootloader = \" \\\n(?:.*\n)*?\s+file://10-otapulse-bl-machine-id\.conf", RECIPE)
    assert re.search(r"FILES:\$\{PN\}:append:otapulse-bootloader = \" \\\n(?:.*\n)*?\s+\$\{systemd_system_unitdir\}/otapulse-machine-id\.service\.d", RECIPE)
    body = RECIPE.split("do_install:append:otapulse-bootloader()", 1)[1]
    assert "10-otapulse-bl-machine-id.conf" in body
