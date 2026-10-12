#!/usr/bin/env python3
"""TODO-157 / TASK-S109-009: otapulse-resize-data-part, step 1 of growing /data
in bootloader compatibility mode (extend the partition; the file system follows
after the mount, by systemd-growfs).

Runs the REAL script against a fake sysfs and fake /etc/fstab and /proc/mounts.
parted, partprobe, fdisk, blkid, udevadm and sleep are stubs that record what
they were asked to do, so nothing here can touch a disk. The parted stub
"grows" the fake partition the way the kernel would report it.

Run: python3 -m pytest test_resize_data_part.py
"""
from __future__ import annotations

import os
import re
import subprocess
from pathlib import Path

import pytest

HERE = Path(__file__).resolve().parent
FILES = HERE / "files"
SCRIPT = FILES / "otapulse-resize-data-part"
RECIPE = HERE / "otapulse-growfs-data_1.0.bb"

DISK = "s009fake0"          # a name that cannot exist as a real block device
DEV = f"/dev/{DISK}p4"
SECTORS_2GIB = 4194304


class Board:
    """A disk with the upstream board support's MBR layout (boot, rootA, rootB, data)."""

    def __init__(self, tmp: Path, total: int = SECTORS_2GIB, pttype: str = "dos") -> None:
        self.root = tmp
        self.sysfs = tmp / "sys"
        self.bin = tmp / "bin"
        self.log = tmp / "actions"
        self.bin.mkdir()
        self.log.write_text("")
        self.fstab = tmp / "fstab"
        self.mounts = tmp / "mounts"
        self.mounts.write_text("")
        self.fstab.write_text(f"{DEV}  /data  auto  defaults  0  2\n")
        self.pttype = pttype
        (self.sysfs / "block" / DISK).mkdir(parents=True)
        (self.sysfs / "class" / "block").mkdir(parents=True)
        (self.sysfs / "block" / DISK / "size").write_text(f"{total}\n")
        # (name, number, start, size) as the sdimg of the spike: data is the last one
        self.parts = [(f"{DISK}p1", 1, 49152, 32768), (f"{DISK}p2", 2, 81920, 868352),
                      (f"{DISK}p3", 3, 950272, 868352), (f"{DISK}p4", 4, 1818624, 262144)]
        for name, num, start, size in self.parts:
            self.add_part(name, num, start, size)
        self._stubs()

    def add_part(self, name: str, num: int, start: int, size: int) -> None:
        d = self.sysfs / "block" / DISK / name
        d.mkdir()
        (d / "partition").write_text(f"{num}\n")
        (d / "start").write_text(f"{start}\n")
        (d / "size").write_text(f"{size}\n")
        link = self.sysfs / "class" / "block" / name
        if not link.is_symlink():
            link.symlink_to(Path("..") / ".." / "block" / DISK / name)

    def data_size(self) -> int:
        return int((self.sysfs / "block" / DISK / f"{DISK}p4" / "size").read_text())

    def _stub(self, name: str, body: str) -> None:
        p = self.bin / name
        p.write_text("#!/bin/sh\n" + body)
        p.chmod(0o755)

    def _stubs(self) -> None:
        data_size_file = self.sysfs / "block" / DISK / f"{DISK}p4" / "size"
        disk_size_file = self.sysfs / "block" / DISK / "size"
        start_file = self.sysfs / "block" / DISK / f"{DISK}p4" / "start"
        # parted -s <disk> resizepart <n> 100%  ==> partition end moves to the end of the disk
        self._stub("parted", f'echo "parted $*" >> "{self.log}"\n'
                             'if [ "${FAKE_PARTED_FAIL:-0}" = 1 ]; then echo "Error: boom" >&2; exit 1; fi\n'
                             'if [ "${FAKE_PARTED_NOOP:-0}" = 1 ]; then exit 0; fi\n'
                             f'echo $(( $(cat "{disk_size_file}") - $(cat "{start_file}") )) > "{data_size_file}"\n')
        for tool in ("partprobe", "udevadm", "fdisk"):
            self._stub(tool, f'echo "{tool} $*" >> "{self.log}"\ncat >/dev/null 2>&1 || true\n')
        self._stub("blkid", f'case "$*" in *PTTYPE*) echo {self.pttype} ;; esac\n')
        self._stub("sleep", "exit 0\n")

    def actions(self) -> list[str]:
        return self.log.read_text().splitlines()

    def run(self, **extra: str) -> subprocess.CompletedProcess:
        env = {
            "PATH": f"{self.bin}:{os.environ['PATH']}",
            "OTAPULSE_RESIZE_FSTAB": str(self.fstab),
            "OTAPULSE_RESIZE_SYSFS": str(self.sysfs),
            "OTAPULSE_RESIZE_MOUNTS": str(self.mounts),
            "OTAPULSE_RESIZE_WAIT_S": "1",
        }
        env.update(extra)
        return subprocess.run(["sh", str(SCRIPT)], env=env, capture_output=True, text=True)


@pytest.fixture
def board(tmp_path: Path) -> Board:
    return Board(tmp_path)


def test_grows_the_last_partition_to_the_end_of_the_medium(board: Board) -> None:
    before = board.data_size()
    r = board.run()
    assert r.returncode == 0, r.stderr
    assert f"parted -s /dev/{DISK} resizepart 4 100%" in board.actions()
    assert board.data_size() > before
    assert board.data_size() == SECTORS_2GIB - 1818624       # exactly to the end
    assert "<6>otapulse-resize-data-part: " in r.stdout
    # an MBR disk has no backup table to move: fdisk is a GPT-only step
    assert not any(a.startswith("fdisk") for a in board.actions())


def test_second_boot_is_a_noop(board: Board) -> None:
    assert board.run().returncode == 0
    board.log.write_text("")
    r = board.run()
    assert r.returncode == 0
    assert "already fills the medium" in r.stdout
    assert board.actions() == [], "once grown, no tool may run again"


def test_gpt_moves_the_backup_table_first(tmp_path: Path) -> None:
    b = Board(tmp_path, pttype="gpt")
    r = b.run()
    assert r.returncode == 0, r.stderr
    acts = b.actions()
    assert any(a.startswith("fdisk") for a in acts)
    assert acts.index(next(a for a in acts if a.startswith("fdisk"))) < \
        acts.index(f"parted -s /dev/{DISK} resizepart 4 100%")


def test_small_slack_is_not_worth_growing(tmp_path: Path) -> None:
    # 4 MiB - 1 sector behind data: below the threshold, left alone
    b = Board(tmp_path, total=1818624 + 262144 + 8191)
    r = b.run()
    assert r.returncode == 0
    assert not any(a.startswith("parted") for a in b.actions())


def test_leaves_data_alone_when_another_partition_follows(tmp_path: Path) -> None:
    b = Board(tmp_path)
    b.add_part(f"{DISK}p5", 5, 1818624 + 262144, 1000)
    r = b.run()
    assert r.returncode == 0
    assert "follows" in r.stdout
    assert not any(a.startswith("parted") for a in b.actions())


def test_never_resizes_a_mounted_partition(board: Board) -> None:
    board.mounts.write_text(f"{DEV} /data ext4 rw 0 0\n")
    r = board.run()
    assert r.returncode == 0
    assert "already mounted" in r.stdout
    assert not any(a.startswith("parted") for a in board.actions())


def test_a_parted_failure_is_an_error_not_a_hang(board: Board) -> None:
    r = board.run(FAKE_PARTED_FAIL="1")
    assert r.returncode == 1
    assert "\n<3>otapulse-resize-data-part: ERROR: parted could not resize" in "\n" + r.stderr


def test_parted_success_without_growth_is_detected(board: Board) -> None:
    r = board.run(FAKE_PARTED_NOOP="1")
    assert r.returncode == 1
    assert "still" in r.stderr


def test_no_data_entry_in_fstab(board: Board) -> None:
    board.fstab.write_text("proc /proc proc defaults 0 0\n# /dev/x /data auto defaults 0 2\n")
    r = board.run()
    assert r.returncode == 0
    assert "no /data entry" in r.stdout
    assert not any(a.startswith("parted") for a in board.actions())


def test_data_not_on_a_partition(board: Board) -> None:
    board.fstab.write_text("/dev/s009fakedm0  /data  auto  defaults  0  2\n")
    r = board.run()
    assert r.returncode == 0
    assert "not a partition" in r.stdout


def test_only_the_data_partition_end_is_ever_requested(board: Board) -> None:
    """The one parted call names partition 4 and nothing else, and no other
    tool that could write (mkfs, sgdisk, dd, fw_setenv) is ever invoked."""
    assert board.run().returncode == 0
    parted_calls = [a for a in board.actions() if a.startswith("parted")]
    assert len(parted_calls) == 1
    assert re.fullmatch(rf"parted -s /dev/{DISK} resizepart 4 100%", parted_calls[0])
    src = SCRIPT.read_text()
    for forbidden in ("mkfs", "sgdisk", "sfdisk", "dd ", "fw_setenv", "/data/u-boot", "mkpart", "rm -"):
        assert forbidden not in re.sub(r"(?m)^\s*#.*$", "", src), forbidden


# ---- wiring ---------------------------------------------------------------------------

def test_partition_unit_runs_before_the_mount() -> None:
    u = (FILES / "otapulse-resize-data-part.service").read_text()
    assert "Before=data.mount" in u
    assert re.search(r"^WantedBy=data\.mount$", u, re.M)          # Wants: a failure never blocks the mount
    assert not re.search(r"^(Requires|RequiredBy)=", u, re.M)


def test_fs_unit_runs_after_the_mount_and_before_the_agent() -> None:
    u = (FILES / "otapulse-growfs-data.service").read_text()
    assert "After=data.mount" in u
    assert "Before=soc-ota-agent.service" in u
    assert "BindsTo=data.mount" in u
    assert re.search(r"^ExecStart=@SYSTEMD_UNITDIR@/systemd-growfs /data$", u, re.M)
    assert not re.search(r"^Requires=", u, re.M)


def test_recipe_is_bootloader_mode_only() -> None:
    r = RECIPE.read_text()
    assert "SkipRecipe" in r and "otapulse-bootloader" in r
