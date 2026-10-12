#!/usr/bin/env python3
"""TODO-157 / TASK-S109-013: bootloader-mode provisioning token handling.

Runs the REAL image post-process function from otapulse-full-bootloader.bbclass in a
POSIX shell against a temp directory standing in for IMAGE_ROOTFS (bitbake variables are
substituted textually, `chown` is recorded instead of run because the tests are not root,
`bbfatal` is a stub). Covers:

  * baked + token: /etc/otapulse/provision.env, mode 0600, that ONE variable, root-owned
  * baked + no token: nothing written
  * manual: nothing written and a stale file is removed
  * a token with shell/format-unsafe characters fails the build
  * the token never reaches a package: the recipe's own payload (files/) holds no token
    and the drop-in is the only provisioning file the package ships, and only in manual mode
  * the native part of the recipe still does not mention the new variable/drop-in

Run: python3 -m pytest test_provision_env.py
"""

from __future__ import annotations

import re
import stat
import subprocess
from pathlib import Path

import pytest

HERE = Path(__file__).resolve().parent
LAYER = HERE.parent.parent
CLASS = (LAYER / "classes" / "otapulse-full-bootloader.bbclass").read_text()
FULL = (LAYER / "classes" / "otapulse-full.bbclass").read_text()
RECIPE = (HERE / "otapulse-firstboot_1.0.bb").read_text()
DROPIN = HERE / "files" / "10-otapulse-provision-data.conf"

TOKEN = "spt_AbCdEf0123456789-_.xyz"


def _function_body() -> str:
    m = re.search(r"^otapulse_bl_provision_env\(\) \{\n(.*?)^\}\n", CLASS, re.S | re.M)
    assert m, "otapulse_bl_provision_env() not found in the class"
    return m.group(1)


def _run(
    tmp: Path, mode: str, token: str, preexisting: str | None = None
) -> subprocess.CompletedProcess:
    rootfs = tmp / "rootfs"
    (rootfs / "etc" / "otapulse").mkdir(parents=True)
    if preexisting is not None:
        (rootfs / "etc" / "otapulse" / "provision.env").write_text(preexisting)
    stubs = tmp / "stubs"
    stubs.mkdir()
    (stubs / "chown").write_text('#!/bin/sh\necho "$@" >> "$CHOWN_LOG"\n')
    (stubs / "chown").chmod(0o755)
    body = (
        _function_body()
        .replace("${IMAGE_ROOTFS}", str(rootfs))
        .replace("${sysconfdir}", "/etc")
        .replace("${OTAPULSE_PROVISIONING_MODE}", mode)
        .replace("${OTAPULSE_PROVISIONING_TOKEN}", token)
    )
    script = 'bbfatal() { echo "FATAL: $*" >&2; exit 1; }\nrun() {\n' + body + "\n}\nrun\n"
    return subprocess.run(
        ["sh", "-c", script],
        capture_output=True,
        text=True,
        env={"PATH": f"{stubs}:/usr/bin:/bin", "CHOWN_LOG": str(tmp / "chown.log")},
    )


def _env_file(tmp: Path) -> Path:
    return tmp / "rootfs" / "etc" / "otapulse" / "provision.env"


def test_baked_with_token_writes_0600_token_only(tmp_path: Path) -> None:
    r = _run(tmp_path, "baked", TOKEN)
    assert r.returncode == 0, r.stderr
    f = _env_file(tmp_path)
    assert f.read_text() == f"OTAPULSE_PROVISIONING_TOKEN={TOKEN}\n"
    assert stat.S_IMODE(f.stat().st_mode) == 0o600
    assert (tmp_path / "chown.log").read_text().split() == ["0:0", str(f)]


def test_baked_without_token_writes_nothing(tmp_path: Path) -> None:
    r = _run(tmp_path, "baked", "")
    assert r.returncode == 0, r.stderr
    assert not _env_file(tmp_path).exists()


def test_manual_ships_no_token_and_removes_a_stale_file(tmp_path: Path) -> None:
    r = _run(tmp_path, "manual", TOKEN, preexisting="OTAPULSE_PROVISIONING_TOKEN=old\n")
    assert r.returncode == 0, r.stderr
    assert not _env_file(tmp_path).exists()
    assert TOKEN not in "".join(
        p.read_text() for p in (tmp_path / "rootfs").rglob("*") if p.is_file()
    )


BAD_TOKENS = [
    "spt_a b",
    "spt_a;rm",
    "spt_a$(x)",
    "spt_a'b",
    'spt_a"b',
    "spt_a\nb",
    "spt_a\\b",
    "spt_a`b`",
]


@pytest.mark.parametrize("bad", [b for b in BAD_TOKENS if "'" not in b])
def test_unsafe_token_characters_fail_the_image_function(tmp_path: Path, bad: str) -> None:
    """Defence in depth: the image function itself refuses (a single quote cannot get here,
    it would already break the shell syntax; the configuration check below catches it first)."""
    r = _run(tmp_path, "baked", bad)
    assert r.returncode != 0
    assert "OTAPULSE_PROVISIONING_TOKEN contains characters" in r.stderr
    assert not _env_file(tmp_path).exists()


class _Fatal(Exception):
    pass


def _config_check(token: str | None) -> None:
    src = re.search(r"^def otapulse_full_check_token\(d\):\n(?:(?:    .*|)\n)+", FULL, re.M).group(
        0
    )

    class BB:
        @staticmethod
        def fatal(msg: str) -> None:
            raise _Fatal(msg)

    class D:
        @staticmethod
        def getVar(name: str) -> str | None:
            return token if name == "OTAPULSE_PROVISIONING_TOKEN" else None

    ns = {"bb": BB}
    exec(src, ns)  # noqa: S102 - the function under test, extracted from the class
    ns["otapulse_full_check_token"](D)


@pytest.mark.parametrize("bad", BAD_TOKENS)
def test_unsafe_token_is_refused_at_configuration_time_without_echoing_it(bad: str) -> None:
    with pytest.raises(_Fatal) as e:
        _config_check(bad)
    assert "OTAPULSE_PROVISIONING_TOKEN" in str(e.value)
    assert bad not in str(e.value)


@pytest.mark.parametrize("good", [TOKEN, None, ""])
def test_plain_or_absent_token_passes_the_configuration_check(good: str | None) -> None:
    _config_check(good)


def test_the_configuration_check_is_part_of_the_validation() -> None:
    body = re.search(r"^def otapulse_full_validate\(d\):\n((?:    .*|)\n)+", FULL, re.M).group(0)
    assert "otapulse_full_check_token(d)" in body


def test_function_is_registered_and_tracks_its_inputs() -> None:
    assert re.search(r'^ROOTFS_POSTPROCESS_COMMAND \+= "otapulse_bl_provision_env;"$', CLASS, re.M)
    vardeps = re.search(r"^otapulse_bl_provision_env\[vardeps\] \+= \"([^\"]*)\"", CLASS, re.M)
    assert vardeps and set(vardeps.group(1).split()) == {
        "OTAPULSE_PROVISIONING_TOKEN",
        "OTAPULSE_PROVISIONING_MODE",
    }


def test_mode_is_validated_in_the_customer_entry_point() -> None:
    assert "OTAPULSE_PROVISIONING_MODE must be" in FULL


def test_no_token_in_any_package_payload() -> None:
    """The token reaches the image only through the class' ROOTFS_POSTPROCESS function."""
    for p in (HERE / "files").iterdir():
        assert not re.search(r"spt_[A-Za-z0-9]", p.read_text(errors="ignore")), p
    assert "OTAPULSE_PROVISIONING_TOKEN" not in RECIPE


def test_dropin_reads_data_partition_after_mount() -> None:
    text = "\n".join(l for l in DROPIN.read_text().splitlines() if not l.lstrip().startswith("#"))
    assert re.search(r"^\[Unit\]\nRequiresMountsFor=/data\n", text, re.M)
    assert re.search(r"^\[Service\]\nEnvironmentFile=-/data/otapulse/provision\.env$", text, re.M)


def test_dropin_installed_only_for_manual_mode_and_only_in_bootloader_mode() -> None:
    marker = "# ── Bootloader compat mode (TODO-157 / sprint S109, TASK-S109-009)"
    native, bl = RECIPE.split(marker, 1)
    assert "provision" not in native.replace("otapulse-auto-provision", "").replace(
        "auto-provisioning", ""
    )
    assert "10-otapulse-provision-data.conf" not in native
    assert (
        "SRC_URI:append:otapulse-bootloader" in bl
        and "file://10-otapulse-provision-data.conf" in bl
    )
    assert re.search(
        r'if \[ "\$\{OTAPULSE_PROVISIONING_MODE\}" = "manual" \]; then\n\s+install -d \$\{D\}\$\{systemd_system_unitdir\}/otapulse-auto-provision\.service\.d',
        bl,
    )
    assert re.search(
        r'FILES:\$\{PN\}:append:otapulse-bootloader = " \\\n(?:.*\n)*?\s+\$\{systemd_system_unitdir\}/otapulse-auto-provision\.service\.d',
        bl,
    )
