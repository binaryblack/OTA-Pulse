#!/usr/bin/env python3
"""TODO-157 / TASK-S109-022: i.MX8M Plus FRDM in bootloader mode (build-level exploration, never flashed).

Static checks of the manual U-Boot integration that lives in
meta-otapulse/dynamic-layers/mender-nxp/ (FRDM is not a board of the vendored NXP board support), plus the REAL
bootloader-mode sanity helpers run against the REAL compiled-in environment of the imx8mplus-frdm-bl build
(tests/data/u-boot-initial-env.imx8mplus-frdm-bl, u-boot-imx-initial-env-sd of that build) and the FRDM boot script.

Run: python3 -m pytest -q meta-otapulse/tests/test_frdm_bl_layer.py
"""

from __future__ import annotations

import re
from pathlib import Path

import pytest

# the helper extraction, fake BitBake data and the `ns` fixture of the 012 sanity tests
from test_bl_sanity import FakeD, BBFatal, _script_image, ns  # noqa: F401  (ns is a fixture)

HERE = Path(__file__).resolve().parent
LAYER = HERE.parent
DYN = LAYER / "dynamic-layers" / "mender-nxp"
UBOOT = DYN / "recipes-bsp" / "u-boot"
SCR = DYN / "recipes-bsp" / "u-boot-scr"
PATCH = UBOOT / "files" / "imx8mp-lpddr4-frdm" / "0001-configs-imx8mp-frdm-otapulse-bootloader-mode.patch"
BOOT_CMD = SCR / "files" / "imx8mp-lpddr4-frdm" / "boot.cmd"
ENV = (HERE / "data" / "u-boot-initial-env.imx8mplus-frdm-bl").read_text()
BSP_NXP = LAYER.parent / "meta-otapulse-bsp" / "boards" / "nxp"
OLIMEX_DIR = BSP_NXP / "recipes-bsp" / "u-boot" / "u-boot-imx" / "olimex-imx8mp-evb"


# ---------------------------------------------------------------- the layer wiring

def test_dynamic_layer_is_registered_and_gated():
    conf = (LAYER / "conf" / "layer.conf").read_text()
    # parsed ONLY when the NXP board support layer (collection mender-nxp) is part of the build
    assert re.search(r'^BBFILES_DYNAMIC \+= "mender-nxp:\$\{LAYERDIR\}/dynamic-layers/mender-nxp/\*/\*/\*\.bbappend"', conf, re.M)
    # the bbappends are in the shape that pattern matches (dynamic-layers/<x>/<dir>/<dir>/*.bbappend)
    assert (UBOOT / "u-boot-imx_%.bbappend").is_file()
    assert (SCR / "u-boot-scr.bbappend").is_file()
    # no recipe of the dynamic layer is reachable through the normal BBFILES globs of the layer
    assert not list((LAYER / "recipes-bsp").glob("**/*frdm*"))


def test_the_vendored_nxp_layer_name_matches_the_collection():
    lc = (BSP_NXP / "conf" / "layer.conf").read_text()
    assert 'BBFILE_COLLECTIONS += "mender-nxp"' in lc


def test_uboot_bbappend_is_machine_gated_and_uses_the_vendored_olimex_variant_by_name():
    t = (UBOOT / "u-boot-imx_%.bbappend").read_text()
    code = "\n".join(l for l in t.splitlines() if not l.lstrip().startswith("#"))
    # the extra search path and the patch apply to the FRDM only
    assert re.search(r"^FILESEXTRAPATHS:prepend:imx8mp-lpddr4-frdm = ", code, re.M)
    assert re.search(r'^SRC_URI:append:imx8mp-lpddr4-frdm = " file://0001-configs-imx8mp-frdm-otapulse-bootloader-mode\.patch"', code, re.M)
    assert re.search(r'^BOOTENV_SIZE:imx8mp-lpddr4-frdm = "0x4000"', code, re.M)
    # the core recipe already lists a file named 0002-Integration-of-Mender-boot-code-into-U-Boot.patch; a second entry
    # fails do_patch with "already applied" (found building it)
    assert "0002-Integration" not in code
    # ... and the search path does reach the vendored variant of that name, by a path that exists
    assert "meta-otapulse-bsp/boards/nxp/recipes-bsp/u-boot/u-boot-imx/olimex-imx8mp-evb" in code
    assert (OLIMEX_DIR / "0002-Integration-of-Mender-boot-code-into-U-Boot.patch").is_file()
    assert (UBOOT / "files" / "imx8mp-lpddr4-frdm").is_dir()


def test_uboot_scr_bbappend_adds_only_the_frdm():
    t = (SCR / "u-boot-scr.bbappend").read_text()
    m = re.search(r'^COMPATIBLE_MACHINE = "\(([^)]*)\)"', t, re.M)
    assert m
    assert set(m.group(1).split("|")) == {"olimex-imx8mp-evb", "imx93-voipac", "imx8mp-lpddr4-frdm"}
    # the vendored recipe's own list is the first two
    vend = (BSP_NXP / "recipes-bsp" / "u-boot-scr" / "u-boot-scr.bb").read_text()
    assert 'COMPATIBLE_MACHINE = "(olimex-imx8mp-evb|imx93-voipac)"' in vend


# ---------------------------------------------------------------- the defconfig patch

def _patched_lines():
    """The lines of the three hunks after the patch (context + added), and the removed ones."""
    new, old = [], []
    for l in PATCH.read_text().splitlines():
        if l.startswith(("diff ", "--- ", "+++ ", "@@")):
            continue
        if l.startswith("+"):
            new.append(l[1:])
        elif l.startswith("-"):
            old.append(l[1:])
        elif l.startswith(" "):
            new.append(l[1:])
            old.append(l[1:])
    return new, old


def test_patch_touches_only_the_frdm_defconfig():
    files = re.findall(r"^diff --git a/(\S+) b/\S+$", PATCH.read_text(), re.M)
    assert files == ["configs/imx8mp_frdm_defconfig"]


def test_patch_moves_the_environment_to_a_redundant_pair_on_emmc_user_area():
    new, old = _patched_lines()
    # the stock FRDM environment: SD slot (device 1), one copy, SPI flash + nowhere also enabled
    assert "CONFIG_SYS_MMC_ENV_DEV=1" in old and "CONFIG_ENV_OFFSET=0x700000" in old
    assert "CONFIG_ENV_IS_NOWHERE=y" in old and "CONFIG_ENV_IS_IN_SPI_FLASH=y" in old
    # ... whose boot command falls back to the NXP bsp_bootcmd (kernel from the FAT partition with a FIXED root=, or netboot)
    assert 'CONFIG_BOOTCOMMAND="run sr_ir_v2_cmd;run distro_bootcmd;run bsp_bootcmd"' in old
    # the bootloader flow: eMMC (U-Boot device 2 == Linux mmcblk2), user area, two copies at 8 / 16 MiB, 16 KiB each
    for want in ("CONFIG_ENV_IS_IN_MMC=y", "CONFIG_SYS_MMC_ENV_DEV=2", "CONFIG_SYS_MMC_ENV_PART=0",
                 "CONFIG_ENV_OFFSET=0x800000", "CONFIG_ENV_OFFSET_REDUND=0x1000000",
                 "CONFIG_SYS_REDUNDAND_ENVIRONMENT=y", "CONFIG_ENV_SIZE=0x4000",
                 "CONFIG_BOOTCOUNT_LIMIT=y", "CONFIG_BOOTCOUNT_ENV=y",
                 'CONFIG_BOOTCOMMAND="run distro_bootcmd; reset"'):
        assert want in new, want
    for gone in ("CONFIG_ENV_IS_NOWHERE=y", "CONFIG_ENV_IS_IN_SPI_FLASH=y", "CONFIG_ENV_OFFSET=0x700000", "CONFIG_SYS_MMC_ENV_DEV=1"):
        assert gone not in new, gone


def test_patch_layout_matches_what_the_board_support_computes():
    new, _ = _patched_lines()
    cfg = dict(l.split("=", 1) for l in new if l.startswith("CONFIG_") and "=" in l)
    off1, off2, size = (int(cfg[k], 16) for k in ("CONFIG_ENV_OFFSET", "CONFIG_ENV_OFFSET_REDUND", "CONFIG_ENV_SIZE"))
    # MENDER_PARTITION_ALIGNMENT of the "sd" image type is 8 MiB: first copy at one alignment, second one alignment later
    assert off1 == 8 * 1024 * 1024 and off2 == 16 * 1024 * 1024
    assert off1 + size <= off2
    # imx-boot (2.1 MB, written raw at 32 KiB) must end below the first copy: a 4 MiB limit is generous and checked at build time
    assert 32 * 1024 + 4 * 1024 * 1024 <= off1
    assert size == 0x4000


# ---------------------------------------------------------------- the boot script

def _script_words():
    return [l.strip() for l in BOOT_CMD.read_text().splitlines() if l.strip() and not l.startswith("#")]


def test_boot_script_runs_the_slot_setup_loads_from_the_chosen_rootfs_and_recovers():
    lines = _script_words()
    i_args = next(i for i, l in enumerate(lines) if l.startswith("setenv bootargs"))
    i_setup = lines.index("run mender_setup")
    i_boot = next(i for i, l in enumerate(lines) if l.startswith("booti "))
    assert i_args < i_setup < i_boot                       # the post-setup panic=N command appends to these bootargs
    assert "root=${mender_kernel_root}" in lines[i_args]
    assert not re.search(r"root=/dev/", BOOT_CMD.read_text())
    loads = [l for l in lines if l.startswith("load ")]
    assert len(loads) == 2 and all("${mender_uboot_root}" in l and " /boot/" in l for l in loads)
    assert lines[-1] == "run mender_try_to_recover"


def test_boot_script_uses_variables_the_frdm_environment_defines():
    env = {l.split("=", 1)[0] for l in ENV.splitlines() if "=" in l}
    used = set(re.findall(r"\$\{([A-Za-z0-9_]+)\}", BOOT_CMD.read_text()))
    runtime = {"mender_kernel_root", "mender_uboot_root", "mender_uboot_dev"}      # set by `run mender_setup`
    assert used - runtime - env == set(), used - runtime - env
    assert "mender_setup" in env and "mender_try_to_recover" in env


# ---------------------------------------------------------------- the real compiled-in environment through the 012 helpers

def test_real_frdm_environment_is_judged_good(ns):
    env = ns["otapulse_full_uboot_env_dict"](ENV)
    assert env["bootlimit"] == "1" and env["upgrade_available"] == "0" and env["mmcdev"] == "2"
    assert env["mender_uboot_dev"] == "2" and env["mender_uboot_if"] == "mmc"
    assert ns["otapulse_full_last_panic"](env["mender_post_setup_commands"]) == 7
    assert ns["otapulse_full_uboot_env_problems"](env) == []


def test_frdm_bootcmd_can_only_end_in_the_boot_script_or_a_reset(ns):
    """bootcmd = "run distro_bootcmd; reset": distro boot leaves the environment (boot.scr on the FAT partition) and the
    only thing after it is a reset. No kernel command line of the board's own is reachable from the environment."""
    env = ns["otapulse_full_uboot_env_dict"](ENV)
    assert env["bootcmd"] == "run distro_bootcmd; reset"
    tr = ns["otapulse_full_uboot_boot_trace"](env, "bootcmd")
    assert tr["external"] is True and tr["boots"] == []


def test_the_stock_frdm_bootcmd_has_an_unrollbackable_fallback(ns):
    """Why the patch replaces CONFIG_BOOTCOMMAND: with the stock bootcmd the trace finds kernel command lines built by the
    NXP bsp_bootcmd fallback (kernel from the FAT partition, fixed root=/dev/mmcblk1p2, no panic=, or NFS root)."""
    env = ns["otapulse_full_uboot_env_dict"](ENV)
    env["bootcmd"] = "run sr_ir_v2_cmd;run distro_bootcmd;run bsp_bootcmd"
    tr = ns["otapulse_full_uboot_boot_trace"](env, "bootcmd")
    assert tr["boots"], "the stock fallback must produce boot command lines"
    assert any("root=/dev/mmcblk1p2" in b for b in tr["boots"])
    assert any(ns["otapulse_full_last_panic"](b) is None for b in tr["boots"])


def test_frdm_env_check_wrapper_passes_and_notes_the_boot_script(ns, tmp_path):
    (tmp_path / "u-boot-initial-env").write_text(ENV)
    ns["otapulse_full_check_uboot_env"](FakeD(B=str(tmp_path), OTAPULSE_PANIC_TIMEOUT_S="7"))
    assert any("boots through a boot script" in n for n in ns["_bb"].notes)


def test_frdm_boot_partition_check_refuses_panic_zero(ns, tmp_path):
    """(for a boot-script board the compile-time environment check only notes the boot script; the finished boot
    partition is where a panic=0 / dropped timeout is caught)"""
    w = tmp_path / "work"
    (w / "bootfs").mkdir(parents=True)
    (w / "bootfs" / "boot.scr").write_bytes(_script_image(BOOT_CMD.read_text()))
    dep = tmp_path / "deploy"
    dep.mkdir()
    (dep / "u-boot-initial-env").write_text(ENV.replace("panic=7", "panic=0"))
    with pytest.raises(BBFatal, match="OTAPULSE_PANIC_TIMEOUT_S"):
        ns["otapulse_bl_bootfs_checks"](FakeD(WORKDIR=str(w), DEPLOY_DIR_IMAGE=str(dep)))


def test_frdm_boot_script_gets_the_slot_root_and_panic_on_the_kernel_command_line(ns):
    env = ns["otapulse_full_uboot_env_dict"](ENV)
    env["__boot_script"] = ns["otapulse_full_boot_script_text"](BOOT_CMD.read_text())
    # the command line U-Boot has before the script runs: the board's own (FRDM has no firmware-provided /chosen/bootargs)
    tr = ns["otapulse_full_uboot_boot_trace"](env, "__boot_script", fdt_bootargs="")
    assert len(tr["boots"]) == 1
    args = tr["boots"][0]
    assert ns["otapulse_full_last_panic"](args) == 7
    assert "root=/dev/mmcblk2p2" in args                  # ${mender_kernel_root} resolved by the slot setup: eMMC, slot A
    assert "console=ttymxc1,115200" in args


def test_frdm_boot_script_image_passes_the_boot_partition_check(ns, tmp_path):
    w = tmp_path / "work"
    (w / "bootfs").mkdir(parents=True)
    (w / "bootfs" / "boot.scr").write_bytes(_script_image(BOOT_CMD.read_text()))
    dep = tmp_path / "deploy"
    dep.mkdir()
    (dep / "u-boot-initial-env").write_text(ENV)
    ns["otapulse_bl_bootfs_checks"](FakeD(WORKDIR=str(w), DEPLOY_DIR_IMAGE=str(dep)))
    assert any("panic=7" in n for n in ns["_bb"].notes)


def test_frdm_stock_boot_script_is_refused(ns, tmp_path):
    """The stock NXP bsp_bootcmd flow (kernel from the FAT partition, fixed root=): no slot choice, no panic=, no rollback."""
    stock = ("setenv bootargs console=${console} root=/dev/mmcblk2p2 rootwait rw\n"
             "load mmc 2:1 ${loadaddr} Image\nload mmc 2:1 ${fdt_addr_r} ${fdtfile}\nbooti ${loadaddr} - ${fdt_addr_r}\n")
    w = tmp_path / "work"
    (w / "bootfs").mkdir(parents=True)
    (w / "bootfs" / "boot.scr").write_bytes(_script_image(stock))
    dep = tmp_path / "deploy"
    dep.mkdir()
    (dep / "u-boot-initial-env").write_text(ENV)
    with pytest.raises(BBFatal, match="OTAPULSE_BOOT_MODE"):
        ns["otapulse_bl_bootfs_checks"](FakeD(WORKDIR=str(w), DEPLOY_DIR_IMAGE=str(dep)))


# ---------------------------------------------------------------- the multi-configuration environment file name (found building the FRDM image)

def test_multi_config_uboot_deploys_the_environment_under_another_name(ns, tmp_path):
    """The first FRDM image build failed in do_image_sdimg: "u-boot-initial-env was not found" - the multi-configuration
    U-Boot (UBOOT_CONFIG) deploys u-boot-imx-initial-env-sd (and stamped copies) instead."""
    find = ns["otapulse_full_find_deployed_uboot_env"]
    assert find(str(tmp_path)) == (None, "was not found")
    for n in ("u-boot-imx-initial-env-sd", "u-boot-imx-initial-env-imx8mp-lpddr4-frdm-sd",
              "u-boot-imx-initial-env-imx8mp-lpddr4-frdm-sd-2024.04-r0"):
        (tmp_path / n).write_text(ENV)
    path, why = find(str(tmp_path))
    assert why is None and Path(path).name.startswith("u-boot-imx-initial-env")
    # copies that disagree: refused, naming them
    (tmp_path / "u-boot-imx-initial-env-sd").write_text(ENV.replace("panic=7", "panic=9"))
    path, why = find(str(tmp_path))
    assert path is None and "2 different versions" in why
    # the single-configuration name wins and is used as before
    (tmp_path / "u-boot-initial-env").write_text(ENV)
    assert find(str(tmp_path)) == (str(tmp_path / "u-boot-initial-env"), None)


def test_boot_partition_check_finds_the_multi_config_environment(ns, tmp_path):
    w = tmp_path / "work"
    (w / "bootfs").mkdir(parents=True)
    (w / "bootfs" / "boot.scr").write_bytes(_script_image(BOOT_CMD.read_text()))
    dep = tmp_path / "deploy"
    dep.mkdir()
    (dep / "u-boot-imx-initial-env-sd").write_text(ENV)
    ns["otapulse_bl_bootfs_checks"](FakeD(WORKDIR=str(w), DEPLOY_DIR_IMAGE=str(dep)))
    assert any("panic=7" in n for n in ns["_bb"].notes)
    # nothing deployed: the message still names the variable and the missing file
    with pytest.raises(BBFatal, match=r"OTAPULSE_PANIC_TIMEOUT_S.*u-boot-initial-env.*was not found"):
        ns["otapulse_bl_bootfs_checks"](FakeD(WORKDIR=str(w), DEPLOY_DIR_IMAGE=str(tmp_path / "empty")))
