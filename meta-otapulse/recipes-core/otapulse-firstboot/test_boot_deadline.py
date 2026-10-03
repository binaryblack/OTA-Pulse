#!/usr/bin/env python3
"""BUG-356: a pending-upgrade boot that wedges in userspace (no panic, no
reboot) must be turned into a reboot by otapulse-boot-deadline, so that the
EXISTING boot-attempt counters revert it -- and must never reboot anything
else.

Runs the REAL otapulse-boot-health and otapulse-boot-deadline scripts against
a fake FAT boot partition, a fake /data/ota and a fake /run, with every
system path rewritten into a temp dir. mount/umount/mountpoint/blkid/findmnt/
logger/sync/reboot/systemctl are stubs that only record what they were asked
to do; nothing touches a real disk or reboots anything. The agent's own
writes of upgrade_available are modelled exactly as file_bootenv.go's
writeFileAtomic does them (temp file + rename).

"Timer fired" == running otapulse-boot-deadline (the timer's OnBootSec only
decides WHEN it runs; the unit-file test below pins its value and wiring).

Run: python3 -m pytest test_boot_deadline.py
"""
from __future__ import annotations

import os
import re
import subprocess
import time
import uuid
from pathlib import Path

import pytest

HERE = Path(__file__).resolve().parent
FILES = HERE / "files"
HEALTH = FILES / "otapulse-boot-health"
DEADLINE = FILES / "otapulse-boot-deadline"

_STUBS = {
    # mount <dev> <dir>: make <dir> a symlink to the fake FAT.
    "mount": '#!/bin/sh\nrmdir "$2" 2>/dev/null; ln -s "$FAKE_FAT" "$2"\n',
    "umount": '#!/bin/sh\n[ -L "$1" ] && rm -f "$1"\nexit 0\n',
    "mountpoint": '#!/bin/sh\n[ "${FAKE_DATA_MOUNTED:-1}" = 1 ]\n',
    "blkid": (
        "#!/bin/sh\n"
        'case "$*" in\n'
        '  *"-s TYPE"*) echo vfat ;;\n'
        '  *"-s LABEL"*) echo boot ;;\n'
        "esac\n"
    ),
    "findmnt": "#!/bin/sh\necho /dev/fake-root\n",
    "logger": "#!/bin/sh\nexit 0\n",
    "sync": "#!/bin/sh\nexit 0\n",
    "reboot": '#!/bin/sh\necho "reboot $*" >> "$FAKE_ROOT/actions"\n',
    "systemctl": (
        "#!/bin/sh\n"
        'echo "systemctl $*" >> "$FAKE_ROOT/actions"\n'
        'exit "${FAKE_SYSTEMCTL_RC:-0}"\n'
    ),
}


class Board:
    """A fake boot-health board: FAT + /data/ota + /run + boot_id."""

    def __init__(self, tmp: Path, cls: str):
        self.root = tmp
        self.fat = tmp / "fat"
        self.ota = tmp / "data/ota"
        self.run_dir = tmp / "run"
        self.fat.mkdir()
        self.ota.mkdir(parents=True)
        self.run_dir.mkdir()
        bypl = tmp / "bypl"
        bypl.mkdir()
        dev = tmp / "dev"
        dev.mkdir()
        for label, num in (("boot", 1), ("rootfs_a", 2), ("rootfs_b", 3)):
            (dev / f"mmcblk0p{num}").write_text("")
            (bypl / label).symlink_to(dev / f"mmcblk0p{num}")
        if cls == "B":
            # Orange Pi / BeaglePlay / imx8mp-frdm: boot.scr + mender_boot_part.
            # Just installed p3 from p2.
            (self.fat / "boot.scr").write_text("numeric boot.scr")
            (self.fat / "mender_boot_part").write_text("3")
            (self.fat / "mender_boot_part_prev").write_text("2")
            (self.fat / "boot_count").write_text("0")
            (self.fat / "uboot_boot_count").write_text("0")
        else:
            # RPi4 VideoCore: config.txt + cmdline.txt.
            (self.fat / "config.txt").write_text("arm_64bit=1\n")
            (self.fat / "cmdline.txt").write_text("root=/dev/mmcblk0p2 rootwait\n")
        bindir = tmp / "bin"
        bindir.mkdir()
        for name, body in _STUBS.items():
            p = bindir / name
            p.write_text(body)
            p.chmod(0o755)
        self.bindir = bindir
        self.health = self._rewrite(HEALTH, "otapulse-boot-health")
        self.deadline = self._rewrite(DEADLINE, "otapulse-boot-deadline")
        self.data_mounted = True
        self.systemctl_rc = 0
        self.new_boot()

    def _rewrite(self, src: Path, name: str) -> Path:
        s = (
            src.read_text()
            .replace("/data/ota", str(self.ota))
            .replace("/run/otapulse", str(self.run_dir / "otapulse"))
            .replace("/proc/sys/kernel/random/boot_id", str(self.root / "boot_id"))
            .replace("/proc/sysrq-trigger", str(self.root / "sysrq-trigger"))
            .replace("/proc/cmdline", str(self.root / "cmdline"))
            .replace("/dev/disk/by-partlabel/", f"{self.root / 'bypl'}/")
            .replace('[ -b "$dev" ]', '[ -e "$dev" ]')
            .replace("/tmp/.otapulse_health_", f"{self.root}/mnt_")
        )
        # Nothing may still point at a real system path.
        prefix = re.escape(str(self.root))
        for real in ("/data/ota", "/run/otapulse", "/proc/sys", "/proc/sysrq"):
            stray = re.search(rf"(?<!{prefix}){re.escape(real)}", s)
            assert stray is None, f"{name}: unrewritten {real}"
        out = self.root / name
        out.write_text(s)
        out.chmod(0o755)
        return out

    # ── what the rest of the system does ──────────────────────────────────
    def new_boot(self) -> None:
        """A (re)boot: fresh boot_id, empty tmpfs /run, nothing recorded."""
        (self.root / "boot_id").write_text(str(uuid.uuid4()) + "\n")
        for p in self.run_dir.rglob("*"):
            if p.is_file():
                p.unlink()
        (self.root / "actions").write_text("")

    def agent_write_ua(self, value: str, mtime: float | None = None) -> None:
        """file_bootenv.go writeFileAtomic: temp file + rename."""
        tmp = self.ota / ".upgrade_available.tmp"
        tmp.write_text(value + "\n")
        os.replace(tmp, self.ota / "upgrade_available")
        if mtime is not None:
            os.utime(self.ota / "upgrade_available", (mtime, mtime))

    def install_and_reboot(self) -> None:
        """InstallUpdate (ua=1, written BEFORE the reboot), then the reboot."""
        self.agent_write_ua("1", mtime=time.time() - 3600)
        self.new_boot()

    def ua(self) -> str:
        return (self.ota / "upgrade_available").read_text().strip()

    # ── the two scripts ───────────────────────────────────────────────────
    def _env(self) -> dict:
        return dict(
            os.environ,
            PATH=f"{self.bindir}:{os.environ['PATH']}",
            FAKE_FAT=str(self.fat),
            FAKE_ROOT=str(self.root),
            FAKE_DATA_MOUNTED="1" if self.data_mounted else "0",
            FAKE_SYSTEMCTL_RC=str(self.systemctl_rc),
        )

    def boot_health(self) -> str:
        (self.root / "cmdline").write_text("console=ttyS0 root=/dev/mmcblk0p3 rootwait\n")
        r = subprocess.run(
            ["sh", str(self.health)], env=self._env(), capture_output=True, text=True, timeout=30
        )
        assert r.returncode == 0, r.stdout + r.stderr
        return r.stdout

    def deadline_fires(self) -> str:
        r = subprocess.run(
            ["sh", str(self.deadline)], env=self._env(), capture_output=True, text=True, timeout=30
        )
        return r.stdout + r.stderr

    @property
    def marker(self) -> Path:
        return self.run_dir / "otapulse/pending-boot"

    def actions(self) -> list[str]:
        return (self.root / "actions").read_text().split("\n")[:-1]

    def rebooted(self) -> bool:
        return any(a.startswith(("systemctl reboot", "reboot")) for a in self.actions())


@pytest.fixture(params=["A", "B"])
def board(request, tmp_path):
    for f in (HEALTH, DEADLINE):
        assert f.is_file(), f"missing {f}"
    return Board(tmp_path, request.param)


@pytest.fixture
def board_b(tmp_path):
    return Board(tmp_path, "B")


# ── fires on a wedged pending-upgrade boot ─────────────────────────────────


def test_fires_on_wedged_pending_upgrade_boot(board):
    board.install_and_reboot()
    out = board.boot_health()
    assert "BOOT-DEADLINE armed for pending boot attempt 1/3" in out
    assert board.marker.is_file()
    # ... the boot wedges: nothing commits, nothing reboots ...
    assert not board.rebooted()
    out = board.deadline_fires()
    assert "BOOT-DEADLINE expired" in out
    assert board.actions() == ["systemctl reboot"], "plain reboot, never '0 tryboot'"
    # It only reboots: the revert decision stays with the existing counters.
    assert board.ua() == "1"
    if (board.fat / "mender_boot_part").exists():
        assert (board.fat / "mender_boot_part").read_text() == "3"
        assert (board.fat / "mender_boot_part_prev").read_text() == "2"


def test_wedge_every_attempt_is_reverted_by_existing_counter(board_b):
    """Attempts 1 and 2 hit the deadline; attempt 3 is reverted by
    otapulse-boot-health's own existing path, which arms nothing."""
    b = board_b
    b.install_and_reboot()
    for attempt in (1, 2):
        out = b.boot_health()
        assert f"boot attempt {attempt}/3" in out
        assert b.marker.is_file()
        b.deadline_fires()
        assert b.actions() == ["systemctl reboot"]
        b.new_boot()
    out = b.boot_health()
    assert "reverting to previous slot" in out
    assert b.actions() == ["reboot "], "boot-health's own revert reboot"
    assert (b.fat / "mender_boot_part").read_text() == "2"
    assert b.ua() == "0"
    assert not b.marker.exists()


def test_escalates_when_systemctl_reboot_fails(board_b):
    board_b.install_and_reboot()
    board_b.boot_health()
    board_b.systemctl_rc = 1
    board_b.deadline_fires()
    assert board_b.actions() == ["systemctl reboot", "reboot -f"]
    assert (board_b.root / "sysrq-trigger").read_text().strip() == "b"


# ── does not fire on an idle boot ──────────────────────────────────────────


def test_does_not_fire_on_idle_boot(board):
    board.agent_write_ua("0", mtime=time.time() - 3600)
    board.new_boot()
    board.boot_health()
    assert not board.marker.exists()
    board.deadline_fires()
    assert not board.rebooted()


def test_no_upgrade_file_at_all_is_idle(board):
    board.new_boot()
    board.boot_health()
    assert not board.marker.exists()
    board.deadline_fires()
    assert not board.rebooted()


# ── does not fire during an install before its reboot ──────────────────────


def test_does_not_fire_mid_install_on_idle_boot(board):
    """InstallUpdate writes ua=1 BEFORE the reboot; uptime may already be
    past the deadline (long-running board)."""
    board.agent_write_ua("0", mtime=time.time() - 3600)
    board.new_boot()
    board.boot_health()
    board.agent_write_ua("1")  # install in progress, not rebooted yet
    board.deadline_fires()
    assert not board.rebooted()


def test_does_not_fire_mid_install_after_commit_in_same_boot(board):
    """Pending boot commits, then a NEW deployment installs in the same boot."""
    board.install_and_reboot()
    board.boot_health()
    assert board.marker.is_file()
    board.agent_write_ua("0")  # CommitUpdate
    board.agent_write_ua("1")  # next InstallUpdate, before its reboot
    out = board.deadline_fires()
    assert "rewritten since boot attempt 1 was counted" in out
    assert not board.rebooted()


# ── does not fire after commit ─────────────────────────────────────────────


def test_does_not_fire_after_commit(board):
    board.install_and_reboot()
    board.boot_health()
    assert board.marker.is_file()
    board.agent_write_ua("0")  # CommitUpdate
    out = board.deadline_fires()
    assert "no longer pending" in out
    assert not board.rebooted()
    assert not board.marker.exists()


def test_does_not_fire_after_agent_rollback(board_b):
    board_b.install_and_reboot()
    board_b.boot_health()
    board_b.agent_write_ua("0")  # Rollback() clears ua too
    board_b.deadline_fires()
    assert not board_b.rebooted()


# ── doubt means "do nothing" ───────────────────────────────────────────────


def test_marker_from_another_boot_is_ignored(board_b):
    board_b.install_and_reboot()
    board_b.boot_health()
    (board_b.root / "boot_id").write_text(str(uuid.uuid4()) + "\n")
    board_b.deadline_fires()
    assert not board_b.rebooted()


def test_data_unmounted_at_deadline_does_not_fire(board_b):
    board_b.install_and_reboot()
    board_b.boot_health()
    board_b.data_mounted = False
    board_b.deadline_fires()
    assert not board_b.rebooted()


def test_not_armed_when_data_not_mounted_at_boot(board):
    board.install_and_reboot()
    board.data_mounted = False
    board.boot_health()
    assert not board.marker.exists()


def test_not_armed_when_attempt_cannot_be_persisted(board_b):
    """A deadline reboot the next boot cannot count would loop forever."""
    (board_b.fat / "boot_count").unlink()
    (board_b.fat / "boot_count").mkdir()  # unwritable as a file, even for root
    board_b.install_and_reboot()
    out = board_b.boot_health()
    assert "boot deadline NOT armed" in out
    assert not board_b.marker.exists()


# ── unit files + recipe wiring ─────────────────────────────────────────────


def test_units_and_recipe_wiring():
    timer = (FILES / "otapulse-boot-deadline.timer").read_text()
    service = (FILES / "otapulse-boot-deadline.service").read_text()
    recipe = (HERE / "otapulse-firstboot_1.0.bb").read_text()
    assert re.search(r"^OnBootSec=1200$", timer, re.M)
    assert re.search(r"^Unit=otapulse-boot-deadline\.service$", timer, re.M)
    assert re.search(r"^WantedBy=timers\.target$", timer, re.M)
    assert re.search(r"^ConditionPathExists=/run/otapulse/pending-boot$", service, re.M)
    assert re.search(r"^ExecStart=/usr/bin/otapulse-boot-deadline$", service, re.M)
    assert "[Install]" not in service, "static: started only by the timer"
    for f in (HEALTH, DEADLINE):
        assert 'DEADLINE_MARKER' in f.read_text()
        assert "/run/otapulse/pending-boot" in f.read_text() or '"/run/otapulse"' in f.read_text()
    assert "BOOT-DEADLINE/1" in DEADLINE.read_text()
    for name in (
        "otapulse-boot-deadline",
        "otapulse-boot-deadline.service",
        "otapulse-boot-deadline.timer",
    ):
        assert f"file://{name} " in recipe
        assert f"${{WORKDIR}}/{name} " in recipe
    sysd = re.search(r'^SYSTEMD_SERVICE:\$\{PN\} = "([^"]*)"', recipe, re.M).group(1).split()
    assert "otapulse-boot-deadline.timer" in sysd
    assert "otapulse-boot-deadline.service" not in sysd
