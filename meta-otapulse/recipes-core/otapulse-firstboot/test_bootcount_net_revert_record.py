#!/usr/bin/env python3
"""BUG-468: otapulse-boot-health's HOLE-A branch (a U-Boot bootcount-net
revert already happened before this boot) must leave a DURABLE record,
/data/ota/bootcount_net_last_revert, before it clears the FAT sentinel and
counters -- the only post-hoc, suite-readable proof that the U-Boot
FRAGMENT (not a human, not this script's own 3-attempt revert) acted.

The record is a pure additional write: these tests also pin that the
branch's revert/cleanup outcome is identical whether the record can be
written or not, and that no other path writes it.

Runs the REAL otapulse-boot-health against the same stubbed fake FAT,
/data/ota, /run and boot_id as test_boot_deadline.py (its Board class is
reused as-is); nothing touches a real disk or reboots anything.

Run: python3 -m pytest test_bootcount_net_revert_record.py
"""
from __future__ import annotations

import re

import pytest

from test_boot_deadline import HEALTH, Board

RECORD = "bootcount_net_last_revert"


@pytest.fixture
def b(tmp_path):
    assert HEALTH.is_file(), f"missing {HEALTH}"
    return Board(tmp_path, "B")


def _fragment_reverted(b: Board, *, fat_boot_count: str = "0") -> None:
    """State after the U-Boot fragment reverted p3 -> p2 at its threshold:
    mender_boot_part rewritten to the old slot, _prev neutralised to 'X',
    uboot_boot_count reset to '0'; the agent's ua=1 is still set."""
    b.install_and_reboot()  # ua=1, fresh boot_id
    (b.fat / "mender_boot_part").write_text("2")
    (b.fat / "mender_boot_part_prev").write_text("X")
    (b.fat / "uboot_boot_count").write_text("0")
    (b.fat / "boot_count").write_text(fat_boot_count)


def _boot_id(b: Board) -> str:
    return (b.root / "boot_id").read_text().strip()


def _record(b: Board) -> dict:
    text = (b.ota / RECORD).read_text()
    lines = text.splitlines()
    assert len(lines) == 1, f"exactly one line, got {text!r}"
    return dict(kv.split("=", 1) for kv in lines[0].split())


def _hole_a_outcome(b: Board) -> tuple:
    """Everything the HOLE-A branch decides, minus the record itself."""
    return (
        b.ua(),
        (b.fat / "mender_boot_part").read_text(),
        (b.fat / "mender_boot_part_prev").exists(),
        (b.fat / "boot_count").read_text(),
        (b.fat / "uboot_boot_count").read_text(),
        (b.ota / "mender_boot_part").read_text().strip(),
        b.rebooted(),
        b.marker.exists(),
    )


_EXPECTED_OUTCOME = ("0", "2", False, "0", "0", "2", False, False)


def test_hole_a_writes_durable_record(b):
    _fragment_reverted(b, fat_boot_count="2")
    out = b.boot_health()
    assert "BOOTCOUNT-NET revert detected" in out
    rec = _record(b)
    assert rec["revert_seq"] == "1"
    assert rec["boot_id"] == _boot_id(b)
    assert rec["reverted_to"] == "2"
    # Counter values are captured BEFORE the branch zeroes them.
    assert rec["boot_count"] == "2"
    assert rec["uboot_boot_count"] == "0"
    assert re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ", rec["recorded_at"])
    assert _hole_a_outcome(b) == _EXPECTED_OUTCOME
    assert not (b.ota / f"{RECORD}.tmp").exists()


def test_each_revert_increments_seq_and_overwrites(b):
    _fragment_reverted(b)
    b.boot_health()
    first = _record(b)
    # A later cycle: a fresh install, reverted again by the fragment.
    (b.fat / "mender_boot_part").write_text("3")
    _fragment_reverted(b)
    b.boot_health()
    second = _record(b)
    assert first["revert_seq"] == "1"
    assert second["revert_seq"] == "2"
    assert second["boot_id"] == _boot_id(b) != first["boot_id"]


@pytest.mark.parametrize(
    "garbage, expected_seq",
    [
        ("not a record\n", "1"),
        ("revert_seq=08 boot_id=x\n", "9"),  # leading zero is not octal
        ("revert_seq=" + "9" * 40 + "\n", "1"),  # absurd length resets
        ("revert_seq=12abc boot_id=x\n", "13"),
        ("", "1"),
    ],
)
def test_corrupt_previous_record_never_breaks_the_branch(b, garbage, expected_seq):
    (b.ota / RECORD).write_text(garbage)
    _fragment_reverted(b)
    b.boot_health()
    assert _record(b)["revert_seq"] == expected_seq
    assert _hole_a_outcome(b) == _EXPECTED_OUTCOME


def test_unwritable_record_changes_nothing_else(b):
    # The temp file cannot be created (a directory in the way, even as root)
    # -- the same failure a full /data produces.
    (b.ota / f"{RECORD}.tmp").mkdir()
    _fragment_reverted(b)
    out = b.boot_health()
    assert "BOOTCOUNT-NET revert detected" in out
    assert "WARNING: failed to write the bootcount-net revert record" in out
    assert _hole_a_outcome(b) == _EXPECTED_OUTCOME
    assert not (b.ota / RECORD).exists()


def test_idle_boot_never_writes_record(b):
    b.agent_write_ua("0")
    b.new_boot()
    b.boot_health()
    assert not (b.ota / RECORD).exists()


def test_ordinary_pending_attempt_never_writes_record(b):
    b.install_and_reboot()
    b.boot_health()
    assert not (b.ota / RECORD).exists()


def test_linux_side_revert_never_writes_record(b):
    """boot-health's OWN MAX_BOOT_RETRIES revert is not the fragment acting."""
    b.install_and_reboot()
    for _ in range(3):
        b.boot_health()
        b.new_boot()
    assert b.ua() == "0"
    assert (b.fat / "mender_boot_part").read_text() == "2"
    assert not (b.ota / RECORD).exists()


def test_record_survives_later_idle_boots(b):
    _fragment_reverted(b)
    b.boot_health()
    rec = _record(b)
    b.new_boot()  # the agent's RollbackReboot, then an ordinary boot
    b.boot_health()
    assert _record(b) == rec
