#!/usr/bin/env python3
"""TODO-157 / TASK-S109-009: otapulse-data-seed, the one thing of
otapulse-partition-setup that survives in bootloader compatibility mode.

Runs the REAL script against a temp directory standing in for /data. The only
stub is `mountpoint` (nothing here mounts anything). Covers: first-boot seeding,
idempotence, the loud device-type mismatch failure, refusing to seed onto an
unmounted /data, a power-cut-safe write, and that nothing else on /data (the
bootloader environment configuration in /data/u-boot, the credentials in
/data/otapulse) is ever touched.

It also pins the unit wiring that makes the check effective: the seed unit is
ordered before everything that reads /data/otapulse, and the agent drop-in
Requires= it.

Run: python3 -m pytest test_data_seed.py
"""
from __future__ import annotations

import hashlib
import os
import re
import subprocess
from pathlib import Path

import pytest

HERE = Path(__file__).resolve().parent
FILES = HERE / "files"
SEED = FILES / "otapulse-data-seed"
UNIT = FILES / "otapulse-data-seed.service"
RECIPE = HERE / "otapulse-firstboot_1.0.bb"
AGENT_DIR = HERE.parent / "soc-ota-agent"


def _tree(root: Path) -> dict[str, str]:
    """relative path -> sha256 (or 'dir'/'link') of everything under root."""
    out: dict[str, str] = {}
    for p in sorted(root.rglob("*")):
        rel = str(p.relative_to(root))
        if p.is_symlink():
            out[rel] = "link:" + os.readlink(p)
        elif p.is_dir():
            out[rel] = "dir"
        else:
            out[rel] = hashlib.sha256(p.read_bytes()).hexdigest()
    return out


class Env:
    def __init__(self, tmp: Path, mounted: bool = True):
        self.root = tmp
        self.data = tmp / "data"
        self.etc = tmp / "etc-otapulse"
        self.bin = tmp / "bin"
        for d in (self.data, self.etc, self.bin):
            d.mkdir()
        # /data/u-boot as the board support leaves it: must never change
        (self.data / "u-boot").mkdir()
        (self.data / "u-boot" / "fw_env.config").write_text("/dev/mmcblk0 0x800000 0x20000\n")
        (self.bin / "mountpoint").write_text("#!/bin/sh\n" + ("exit 0\n" if mounted else "exit 1\n"))
        (self.bin / "mountpoint").chmod(0o755)

    def image_device_type(self, value: str | None) -> Path:
        src = self.etc / "device_type"
        if value is not None:
            src.write_text(f"device_type={value}\n")
        return src

    def run(self, skip_mountcheck: bool = False) -> subprocess.CompletedProcess:
        env = {
            "PATH": f"{self.bin}:{os.environ['PATH']}",
            "OTAPULSE_DATA_SEED_DIR": str(self.data / "otapulse"),
            "OTAPULSE_DATA_SEED_SRC": str(self.etc / "device_type"),
        }
        if skip_mountcheck:
            env["OTAPULSE_DATA_SEED_SKIP_MOUNTCHECK"] = "1"
        return subprocess.run(["sh", str(SEED)], env=env, capture_output=True, text=True)


@pytest.fixture
def env(tmp_path: Path) -> Env:
    return Env(tmp_path)


def test_seeds_on_first_boot(env: Env) -> None:
    env.image_device_type("qemuarm64")
    r = env.run()
    assert r.returncode == 0, r.stderr
    dst = env.data / "otapulse" / "device_type"
    assert dst.read_text() == "device_type=qemuarm64\n"
    # byte-identical to the image's file: what the agent recipe writes is what is seeded
    assert dst.read_bytes() == (env.etc / "device_type").read_bytes()
    assert "<6>otapulse-data-seed: seeded" in r.stdout
    # the atomic-write temp file is gone
    assert [p.name for p in (env.data / "otapulse").iterdir()] == ["device_type"]


def test_idempotent_and_leaves_everything_else_alone(env: Env) -> None:
    env.image_device_type("qemuarm64")
    assert env.run().returncode == 0
    # the agent's / auto-provision's own state on /data
    (env.data / "otapulse" / "otapulse.conf").write_text('{"SoCAPIKey": "k"}\n')
    (env.data / "etc").mkdir()
    (env.data / "etc" / "machine-id").write_text("0" * 32 + "\n")
    before = _tree(env.data)
    r = env.run()
    assert r.returncode == 0, r.stderr
    assert "already matches" in r.stdout
    assert _tree(env.data) == before, "a second run must not change anything on /data"


def test_never_touches_the_bootloader_environment_config(env: Env) -> None:
    env.image_device_type("qemuarm64")
    before = _tree(env.data / "u-boot")
    assert env.run().returncode == 0
    assert _tree(env.data / "u-boot") == before


def test_whitespace_differences_are_not_a_mismatch(env: Env) -> None:
    env.image_device_type("qemuarm64")
    (env.data / "otapulse").mkdir()
    (env.data / "otapulse" / "device_type").write_text("device_type=qemuarm64")  # no newline
    assert env.run().returncode == 0


def test_mismatch_fails_loudly_and_keeps_the_old_value(env: Env) -> None:
    env.image_device_type("raspberrypi4-64")
    (env.data / "otapulse").mkdir()
    dst = env.data / "otapulse" / "device_type"
    dst.write_text("device_type=qemuarm64\n")
    r = env.run()
    assert r.returncode == 1
    # error level for journald (-p err) and names both values
    assert r.stderr.startswith("<3>otapulse-data-seed: ERROR:")
    assert "mismatch" in r.stderr
    assert "device_type=qemuarm64" in r.stderr and "device_type=raspberrypi4-64" in r.stderr
    assert dst.read_text() == "device_type=qemuarm64\n", "must not silently overwrite"


@pytest.mark.parametrize("content", [None, "", "garbage\n", "device_type=\n"])
def test_image_without_a_device_type_fails(env: Env, content: str | None) -> None:
    src = env.etc / "device_type"
    if content is not None:
        src.write_text(content)
    r = env.run()
    assert r.returncode == 1
    assert r.stderr.startswith("<3>otapulse-data-seed: ERROR:")
    assert not (env.data / "otapulse" / "device_type").exists()


def test_refuses_to_seed_onto_an_unmounted_data(tmp_path: Path) -> None:
    env = Env(tmp_path, mounted=False)
    env.image_device_type("qemuarm64")
    r = env.run()
    assert r.returncode == 1
    assert "not a mount point" in r.stderr
    assert not (env.data / "otapulse").exists(), "nothing may be created on the root filesystem"


def test_a_failed_write_leaves_no_partial_device_type(env: Env) -> None:
    env.image_device_type("qemuarm64")
    # the directory is read-only: neither device_type nor a temp file may appear
    (env.data / "otapulse").mkdir()
    (env.data / "otapulse").chmod(0o555)
    try:
        if os.geteuid() == 0:
            pytest.skip("root ignores directory permissions")
        r = env.run()
        assert r.returncode == 1
        assert list((env.data / "otapulse").iterdir()) == []
    finally:
        (env.data / "otapulse").chmod(0o755)


# ---- wiring (what makes the check effective on a device) -----------------------------

def test_unit_orders_before_everything_that_uses_data_otapulse() -> None:
    unit = UNIT.read_text()
    assert re.search(r"^RequiresMountsFor=/data$", unit, re.M)
    assert re.search(r"^After=data\.mount$", unit, re.M)
    before = " ".join(re.findall(r"^Before=(.*)$", unit, re.M)).split()
    for needed in ("soc-ota-agent.service", "otapulse-auto-provision.service"):
        assert needed in before
    assert re.search(r"^ExecStart=/usr/bin/otapulse-data-seed$", unit, re.M)
    assert re.search(r"^WantedBy=multi-user\.target$", unit, re.M)
    # a oneshot unit that stays "active": the agent's Requires= keeps seeing it
    assert re.search(r"^Type=oneshot$", unit, re.M) and re.search(r"^RemainAfterExit=yes$", unit, re.M)


def test_agent_dropin_requires_the_seed_unit() -> None:
    dropin = (AGENT_DIR / "files" / "10-bootloader-mode.conf").read_text()
    active = "\n".join(l for l in dropin.splitlines() if not l.lstrip().startswith("#"))
    assert "otapulse-data-seed.service" in " ".join(re.findall(r"^Requires=(.*)$", active, re.M)).split()
    assert "otapulse-data-seed.service" in " ".join(re.findall(r"^After=(.*)$", active, re.M)).split()


def test_recipe_gates_native_only_pieces_by_override_only() -> None:
    """The native task bodies must stay textually untouched (native-unchanged
    gate): every bootloader-mode line is override-conditional."""
    text = RECIPE.read_text()
    marker = "# ── Bootloader compat mode (TODO-157 / sprint S109, TASK-S109-009)"
    assert marker in text
    native, bl = text.split(marker, 1)
    assert "otapulse-bootloader" not in native, "native part of the recipe must not mention the override"
    for name in ("otapulse-partition-setup", "otapulse-boot-health", "otapulse-boot-deadline"):
        # still installed by the unchanged native do_install ...
        assert f"${{WORKDIR}}/{name} ${{D}}${{bindir}}/{name}" in native
        # ... and listed for removal in bootloader mode
        assert name in bl
    # kept in bootloader mode: never removed
    for kept in ("otapulse-auto-provision", "otapulse-machine-id"):
        assert not re.search(rf"rm -f .*{kept}", bl)
        assert kept not in re.search(r"SYSTEMD_SERVICE:\$\{PN\}:remove:otapulse-bootloader = \"([^\"]*)\"", bl).group(1)
    # every assignment/function in the bootloader block carries the override or is a plain helper variable
    for line in bl.splitlines():
        m = re.match(r"^(SRC_URI|RDEPENDS|SYSTEMD_SERVICE|FILES|do_install)\b(\S*)", line)
        if m:
            assert "otapulse-bootloader" in m.group(2) or "otapulse-bootloader" in line.split("=")[0], line
