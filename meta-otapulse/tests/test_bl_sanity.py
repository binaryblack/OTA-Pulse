#!/usr/bin/env python3
"""TODO-157 / TASK-S109-012: bootloader-mode sanity checks.

Runs the REAL helper functions of otapulse-full.bbclass / otapulse-full-bootloader.bbclass
(extracted from the class text, so a change of the class is what is tested) against fake
BitBake data, the real compiled-in U-Boot environment of the qemuarm64-bl image
(tests/data/u-boot-initial-env.qemuarm64-bl) and the real state scripts of the agent recipe.

Every message a check can produce must
  * name the OTAPULSE_* variable to fix (the pair returned with it),
  * say nothing about the vendored board support (no "mender").

Run: python3 -m pytest -q meta-otapulse/tests/test_bl_sanity.py
"""

from __future__ import annotations

import re
import shutil
from pathlib import Path

import pytest

HERE = Path(__file__).resolve().parent
LAYER = HERE.parent
CLASSES = LAYER / "classes"
FULL = (CLASSES / "otapulse-full.bbclass").read_text()
BL = (CLASSES / "otapulse-full-bootloader.bbclass").read_text()
SCRIPTS = LAYER / "recipes-core" / "soc-ota-agent" / "files"
REAL_ENV = (HERE / "data" / "u-boot-initial-env.qemuarm64-bl").read_text()


class BBFatal(Exception):
    pass


class _Utils:
    @staticmethod
    def contains(var, item, yes, no, d):
        return yes if item in (d.getVar(var) or "").split() else no

    @staticmethod
    def which(path, name):
        for p in (path or "").split(":"):
            f = Path(p) / name
            if f.is_file():
                return str(f)
        return ""


class _BB:
    utils = _Utils

    def __init__(self):
        self.warnings: list[str] = []
        self.notes: list[str] = []

    def fatal(self, msg):
        raise BBFatal(msg)

    def warn(self, msg):
        self.warnings.append(msg)

    def note(self, msg):
        self.notes.append(msg)


class FakeD:
    def __init__(self, **v):
        self.v = dict(v)

    def getVar(self, name, expand=True):
        return self.v.get(name)

    def setVar(self, name, val):
        self.v[name] = val


def _blocks(text: str) -> str:
    """The module-level `def` blocks and `python name() { }` blocks of a class as Python source."""
    out = []
    lines = text.splitlines()
    i = 0
    while i < len(lines):
        ln = lines[i]
        m = re.match(r"^python (\w+)\(\) \{$", ln)
        if ln.startswith("def "):
            out.append(ln)
            i += 1
            while i < len(lines) and (lines[i] == "" or lines[i][0] in " \t"):
                out.append(lines[i])
                i += 1
            continue
        if m:
            out.append("def %s(d):" % m.group(1))
            i += 1
            while i < len(lines) and lines[i] != "}":
                out.append(lines[i])
                i += 1
        i += 1
    return "\n".join(out)


@pytest.fixture()
def ns():
    bb = _BB()
    g = {"bb": bb, "__name__": "bbclass"}
    exec(compile(_blocks(FULL) + "\n" + _blocks(BL), "bbclass", "exec"), g)
    g["_bb"] = bb
    return g


def _msg_ok(msg: str) -> None:
    low = re.sub(r"grub-mender-grubenv", "", msg).lower()
    assert "mender" not in low, msg


# ---------------------------------------------------------------- numbers / image type


def test_parse_number(ns):
    p = ns["otapulse_full_parse_number"]
    assert p("0x800000") == 0x800000
    assert p(" 4096 ") == 4096
    assert p("") is None and p(None) is None and p("abc") is None and p("-5") is None
    assert p("0x") is None and p("1.5") is None


@pytest.mark.parametrize(
    "bl,it,bad",
    [
        ("grub", "sd", True),
        ("uboot", "uefi", True),
        ("grub", "uefi", False),
        ("grub", "gpt", False),
        ("uboot", "sd", False),
        ("uboot", "gpt", False),
    ],
)
def test_image_type_conflict(ns, bl, it, bad):
    msg = ns["otapulse_full_image_type_conflict"](bl, it)
    if not bad:
        assert msg is None
        return
    assert "OTAPULSE_IMAGE_TYPE" in msg and "OTAPULSE_BOOTLOADER" in msg
    _msg_ok(msg)


def test_validate_rejects_the_conflict_before_the_board_support(ns):
    d = FakeD(OTAPULSE_BOOTLOADER="grub", OTAPULSE_IMAGE_TYPE="sd", OTAPULSE_PANIC_TIMEOUT_S="10")
    with pytest.raises(BBFatal, match="OTAPULSE_IMAGE_TYPE"):
        ns["otapulse_full_validate"](d)
    d = FakeD(OTAPULSE_BOOTLOADER="uboot", OTAPULSE_IMAGE_TYPE="uefi", OTAPULSE_PANIC_TIMEOUT_S="10")
    with pytest.raises(BBFatal, match="OTAPULSE_IMAGE_TYPE"):
        ns["otapulse_full_validate"](d)
    # the defaults and valid explicit choices still pass
    for bl, it in (("uboot", ""), ("grub", ""), ("uboot", "sd"), ("grub", "uefi")):
        d = FakeD(OTAPULSE_BOOTLOADER=bl, OTAPULSE_IMAGE_TYPE=it, OTAPULSE_PANIC_TIMEOUT_S="10")
        ns["otapulse_full_validate"](d)


# ---------------------------------------------------------------- U-Boot variables


def test_uboot_vars_valid_and_unset(ns):
    ns["otapulse_full_check_uboot_vars"](FakeD(OTAPULSE_BOOTLOADER="uboot"))
    ns["otapulse_full_check_uboot_vars"](FakeD(OTAPULSE_BOOTLOADER="grub"))
    ns["otapulse_full_check_uboot_vars"](
        FakeD(
            OTAPULSE_BOOTLOADER="uboot",
            OTAPULSE_UBOOT_ENV_OFFSET="0x800000",
            OTAPULSE_UBOOT_ENV_OFFSET_REDUND="0x1000000",
            OTAPULSE_UBOOT_ENV_SIZE="0x20000",
            OTAPULSE_UBOOT_AUTO_CONFIGURE="1",
        )
    )


@pytest.mark.parametrize(
    "var,val",
    [
        ("OTAPULSE_UBOOT_ENV_OFFSET", "abc"),
        ("OTAPULSE_UBOOT_ENV_OFFSET", "0"),
        ("OTAPULSE_UBOOT_ENV_OFFSET", "0x800001"),
        ("OTAPULSE_UBOOT_ENV_OFFSET_REDUND", "-4096"),
        ("OTAPULSE_UBOOT_ENV_SIZE", "0"),
        ("OTAPULSE_UBOOT_ENV_SIZE", "128K"),
        ("OTAPULSE_UBOOT_AUTO_CONFIGURE", "yes"),
    ],
)
def test_uboot_vars_invalid_name_the_variable(ns, var, val):
    with pytest.raises(BBFatal) as e:
        ns["otapulse_full_check_uboot_vars"](FakeD(OTAPULSE_BOOTLOADER="uboot", **{var: val}))
    assert var in str(e.value)
    _msg_ok(str(e.value))


def test_uboot_vars_refused_with_grub(ns):
    with pytest.raises(BBFatal) as e:
        ns["otapulse_full_check_uboot_vars"](
            FakeD(OTAPULSE_BOOTLOADER="grub", OTAPULSE_UBOOT_ENV_OFFSET="0x800000")
        )
    assert "OTAPULSE_UBOOT_ENV_OFFSET" in str(e.value) and "OTAPULSE_BOOTLOADER" in str(e.value)


def test_uboot_vars_explicit_non_redundant_pair(ns):
    with pytest.raises(BBFatal) as e:
        ns["otapulse_full_check_uboot_vars"](
            FakeD(
                OTAPULSE_BOOTLOADER="uboot",
                OTAPULSE_UBOOT_ENV_OFFSET="0x800000",
                OTAPULSE_UBOOT_ENV_OFFSET_REDUND="0x800000",
            )
        )
    assert "OTAPULSE_UBOOT_ENV_OFFSET_REDUND" in str(e.value) and "not redundant" in str(e.value)


# ---------------------------------------------------------------- environment layout


def test_env_layout(ns):
    f = ns["otapulse_full_env_layout_problem"]
    assert f(0x800000, 0x1000000, 0x20000) is None
    assert f(0x800000, 0x820000, 0x20000) is None  # exactly adjacent
    var, msg = f(0x800000, 0x800000, 0x20000)
    assert var == "OTAPULSE_UBOOT_ENV_OFFSET_REDUND" and "not redundant" in msg
    var, msg = f(0x800000, 0x810000, 0x20000)
    assert var == "OTAPULSE_UBOOT_ENV_OFFSET_REDUND" and "overlap" in msg
    var, msg = f(0x810000, 0x800000, 0x20000)  # reversed order overlaps too
    assert "overlap" in msg
    assert f(0x800000, None, 0x20000)[0] == "OTAPULSE_UBOOT_ENV_OFFSET_REDUND"
    assert f(None, 0x1000000, 0x20000)[0] == "OTAPULSE_UBOOT_ENV_OFFSET"
    assert f(0x800000, 0x1000000, 0)[0] == "OTAPULSE_UBOOT_ENV_SIZE"
    for args in ((0x800000, 0x800000, 0x20000), (0x800000, 0x810000, 0x20000), (1, None, 1), (None, 1, 1), (1, 2, 0)):
        _msg_ok(f(*args)[1])


ALIGN = 0x800000  # 8 MiB, the board support's default partition alignment


def _region(ns, base=0x800000, off1=0x800000, off2=0x1000000, size=0x20000, total=2 * ALIGN, reserved=2 * ALIGN):
    return ns["otapulse_full_env_region_problem"](base, off1, off2, size, total, reserved, ALIGN)


def test_env_region_default_layout_is_fine(ns):
    assert _region(ns) is None


def test_env_region_second_copy_outside_the_reserved_area(ns):
    # 0x2000000 would be inside the boot partition: the case from the review
    var, msg = _region(ns, off2=0x2000000)
    assert var == "OTAPULSE_UBOOT_ENV_OFFSET_REDUND" and "outside the area reserved" in msg
    _msg_ok(msg)
    # a copy that starts inside but runs past the end
    var, msg = _region(ns, off2=0x17f0000)
    assert var == "OTAPULSE_UBOOT_ENV_OFFSET_REDUND"
    # the area ends exactly at base + total: a copy ending there is fine
    assert _region(ns, off2=0x1000000 + 0x800000 - 0x20000) is None
    # a first copy below the area
    assert _region(ns, off1=0x400000)[0] == "OTAPULSE_UBOOT_ENV_OFFSET"


def test_env_size_larger_than_the_reserved_space(ns):
    # 9 MiB rounds up to 2 alignment units per copy = 4 units total > 2 reserved
    total = 4 * ALIGN
    var, msg = _region(ns, size=0x900000, total=total)
    assert var == "OTAPULSE_UBOOT_ENV_SIZE" and "Reduce OTAPULSE_UBOOT_ENV_SIZE" in msg
    for bad in (msg, ):
        low = bad.lower()
        assert "reserved_space" not in low and "docs." not in low and "mender" not in low


def test_env_offset_must_sit_on_the_alignment_grid(ns):
    var, msg = _region(ns, base=0x810000, off1=0x810000, off2=0x1010000)
    assert var == "OTAPULSE_UBOOT_ENV_OFFSET" and "multiple of the partition alignment" in msg
    _msg_ok(msg)


def test_layout_check_bounds_on_recipe_data(ns):
    d = FakeD(
        MENDER_FEATURES="mender-uboot",
        MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET="0x800000",
        MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_1="0x800000",
        MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_2="0x1000000",
        BOOTENV_SIZE="0x20000",
        MENDER_BOOTENV_TOTAL_ALIGNED_SIZE="16777216",
        MENDER_RESERVED_SPACE_BOOTLOADER_DATA="16777216",
        MENDER_PARTITION_ALIGNMENT="8388608",
        MENDER_UBOOT_CONFIG_SYS_MMC_ENV_PART="0",
    )
    ns["otapulse_full_check_uboot_layout"](d)
    d.v["MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_2"] = "0x2000000"
    with pytest.raises(BBFatal, match="OTAPULSE_UBOOT_ENV_OFFSET_REDUND"):
        ns["otapulse_full_check_uboot_layout"](d)
    d.v["MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_2"] = "0x1000000"
    d.v["BOOTENV_SIZE"] = "0x900000"
    d.v["MENDER_BOOTENV_TOTAL_ALIGNED_SIZE"] = str(4 * ALIGN)
    with pytest.raises(BBFatal, match="OTAPULSE_UBOOT_ENV_SIZE"):
        ns["otapulse_full_check_uboot_layout"](d)
    # an environment in a hardware boot partition is outside the user area: no area to stay inside
    d.v["MENDER_UBOOT_CONFIG_SYS_MMC_ENV_PART"] = "1"
    d.v["MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_2"] = "0x2000000"
    d.v["BOOTENV_SIZE"] = "0x20000"
    ns["otapulse_full_check_uboot_layout"](d)


def test_uboot_config_dirs(ns):
    f = ns["otapulse_full_uboot_config_dirs"]
    assert f("/b", "qemu_arm64_defconfig", "") == ["/b"]
    assert f("/b", "qemu_arm64_defconfig", None) == ["/b"]
    # multi-configuration board: UBOOT_MACHINE is a list, the real configurations are in /b/<entry>
    assert f("/b", "imx8mp_a_defconfig imx8mp_b_defconfig", "sd emmc") == ["/b/imx8mp_a_defconfig", "/b/imx8mp_b_defconfig"]


def test_multi_config_board_ignores_the_unmerged_top_level_config(ns, tmp_path):
    bad = GOOD_CONFIG.replace("CONFIG_BOOTCOUNT_ENV=y\n", "")  # would fail if it were looked at
    (tmp_path / ".config").write_text(bad)
    for cfg in ("a_defconfig", "b_defconfig"):
        (tmp_path / cfg).mkdir()
        (tmp_path / cfg / ".config").write_text(GOOD_CONFIG)
        (tmp_path / cfg / "u-boot.cfg").write_text("")
    (tmp_path / "u-boot.cfg").write_text("")
    d = FakeD(B=str(tmp_path), UBOOT_MACHINE="a_defconfig b_defconfig", UBOOT_CONFIG="sd emmc",
              MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_1="0x800000", MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_2="0x1000000",
              BOOTENV_SIZE="0x20000", MENDER_FEATURES="mender-uboot")
    ns["otapulse_full_check_uboot_config_early"](d)  # nothing: no unmerged configuration to look at
    ns["otapulse_full_check_uboot_config"](d)        # judges the two real ones, not B/.config
    # a real per-configuration problem is still found
    (tmp_path / "b_defconfig" / ".config").write_text(bad)
    with pytest.raises(BBFatal, match="CONFIG_BOOTCOUNT_ENV"):
        ns["otapulse_full_check_uboot_config"](d)
    # single configuration: B/.config is the one
    d2 = FakeD(B=str(tmp_path), UBOOT_MACHINE="a_defconfig", UBOOT_CONFIG="",
               MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_1="0x800000", MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_2="0x1000000",
               BOOTENV_SIZE="0x20000", MENDER_FEATURES="mender-uboot")
    with pytest.raises(BBFatal, match="CONFIG_BOOTCOUNT_ENV"):
        ns["otapulse_full_check_uboot_config"](d2)


def test_config_wrapper_uses_the_preprocessed_configuration(ns, tmp_path):
    (tmp_path / ".config").write_text(NO_COUNTER)
    d = FakeD(B=str(tmp_path), UBOOT_MACHINE="x_defconfig", UBOOT_CONFIG="",
              MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_1="0x800000", MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_2="0x1000000",
              BOOTENV_SIZE="0x20000", MENDER_FEATURES="mender-uboot")
    (tmp_path / "u-boot.cfg").write_text(HDR_BOTH)
    ns["otapulse_full_check_uboot_config"](d)
    (tmp_path / "u-boot.cfg").write_text("#define CONFIG_X 1\n")
    with pytest.raises(BBFatal, match="OTAPULSE_UBOOT_AUTO_CONFIGURE"):
        ns["otapulse_full_check_uboot_config"](d)
    (tmp_path / "u-boot.cfg").unlink()  # not generated: warn, leave it to the bootloader build
    ns["otapulse_full_check_uboot_config"](d)
    assert any("u-boot.cfg" in w for w in ns["_bb"].warnings)


def test_header_text_from_the_preprocessing_result(ns):
    f = ns["otapulse_full_header_text"]
    assert f("#define CONFIG_X 1\n", None) == "#define CONFIG_X 1\n"
    # not generated because the board support's header found no boot counter: definitely not defined
    log = "config_mender.h:32:3: error: #error CONFIG_BOOTCOUNT_LIMIT is required for Mender to work."
    assert f(None, log) == ""
    assert f(None, "error: #error CONFIG_BOOTCOUNT_ENV is required") == ""
    # not generated for any other reason, or no log: not judged
    assert f(None, "make: *** No rule to make target") is None
    assert f(None, None) is None


def test_config_wrapper_reads_the_preprocessing_log(ns, tmp_path):
    (tmp_path / ".config").write_text(NO_COUNTER)
    (tmp_path / "otapulse-u-boot-cfg.log").write_text("error: #error CONFIG_BOOTCOUNT_LIMIT is required for x")
    d = FakeD(B=str(tmp_path), UBOOT_MACHINE="x_defconfig", UBOOT_CONFIG="",
              MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_1="0x800000", MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_2="0x1000000",
              BOOTENV_SIZE="0x20000", MENDER_FEATURES="mender-uboot")
    with pytest.raises(BBFatal, match="CONFIG_BOOTCOUNT_LIMIT and CONFIG_BOOTCOUNT_ENV"):
        ns["otapulse_full_check_uboot_config"](d)


def test_image_wants_agent(ns, tmp_path):
    f = ns["otapulse_full_image_wants_agent"]
    assert f(str(tmp_path), ("foo soc-ota-agent bar", None)) is True
    assert f(str(tmp_path), (None, "x soc-ota-agent")) is True
    assert f(str(tmp_path), ("foo", "bar")) is False  # an initramfs / minimal image
    (tmp_path / "usr/bin").mkdir(parents=True)
    (tmp_path / "usr/bin/soc-ota-agent").write_text("")
    assert f(str(tmp_path), (None, None)) is True


def test_rootfs_wrapper_skips_images_without_the_agent(ns, tmp_path):
    ns["otapulse_bl_rootfs_checks"](FakeD(IMAGE_ROOTFS=str(tmp_path), OTAPULSE_BOOTLOADER="uboot",
                                           IMAGE_INSTALL="busybox", PACKAGE_INSTALL="busybox"))
    assert any("checks skipped" in n for n in ns["_bb"].notes)
    # the same empty root file system fails when the image does install the agent
    with pytest.raises(BBFatal, match="OTAPULSE_BOOT_MODE"):
        ns["otapulse_bl_rootfs_checks"](FakeD(IMAGE_ROOTFS=str(tmp_path), OTAPULSE_BOOTLOADER="uboot",
                                               IMAGE_INSTALL="busybox soc-ota-agent", PACKAGE_INSTALL=""))


def test_uboot_layout_check_on_recipe_data(ns):
    d = FakeD(
        MENDER_FEATURES="mender-uboot",
        MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_1="0x800000",
        MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_2="0x1000000",
        BOOTENV_SIZE="0x20000",
    )
    ns["otapulse_full_check_uboot_layout"](d)
    d.v["MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_2"] = "0x800000"
    with pytest.raises(BBFatal, match="OTAPULSE_UBOOT_ENV_OFFSET_REDUND"):
        ns["otapulse_full_check_uboot_layout"](d)
    d.v["MENDER_FEATURES"] = "mender-uboot mender-ubi"  # UBI keeps its copies in volumes
    ns["otapulse_full_check_uboot_layout"](d)


# ---------------------------------------------------------------- U-Boot .config

GOOD_CONFIG = """\
CONFIG_ENV_IS_IN_MMC=y
CONFIG_BOOTCOUNT_LIMIT=y
CONFIG_BOOTCOUNT_ENV=y
CONFIG_SYS_REDUNDAND_ENVIRONMENT=y
CONFIG_ENV_OFFSET=0x800000
CONFIG_ENV_OFFSET_REDUND=0x1000000
CONFIG_ENV_SIZE=0x20000
"""


def _cfg(ns, text, off1=0x800000, off2=0x1000000, size=0x20000, ubi=False):
    return ns["otapulse_full_dotconfig_problems"](text, off1, off2, size, ubi)


def test_dotconfig_good(ns):
    assert _cfg(ns, GOOD_CONFIG) == []


def test_dotconfig_bootcount_missing(ns):
    for broken in (
        GOOD_CONFIG.replace("CONFIG_BOOTCOUNT_LIMIT=y\n", "# CONFIG_BOOTCOUNT_LIMIT is not set\n"),
        GOOD_CONFIG.replace("CONFIG_BOOTCOUNT_ENV=y\n", ""),
    ):
        probs = _cfg(ns, broken)
        assert [p[0] for p in probs] == ["OTAPULSE_UBOOT_AUTO_CONFIGURE"]
        assert "CONFIG_BOOTCOUNT" in probs[0][1]
        _msg_ok(probs[0][1])


def test_dotconfig_not_redundant(ns):
    for broken in (
        GOOD_CONFIG.replace("CONFIG_SYS_REDUNDAND_ENVIRONMENT=y\n", ""),
        GOOD_CONFIG.replace("CONFIG_ENV_OFFSET_REDUND=0x1000000\n", ""),
    ):
        probs = _cfg(ns, broken)
        assert probs and probs[0][0] == "OTAPULSE_UBOOT_ENV_OFFSET_REDUND"
        _msg_ok(probs[0][1])


def test_dotconfig_offset_and_size_mismatch(ns):
    probs = _cfg(ns, GOOD_CONFIG.replace("CONFIG_ENV_OFFSET=0x800000", "CONFIG_ENV_OFFSET=0x400000"))
    assert [p[0] for p in probs] == ["OTAPULSE_UBOOT_ENV_OFFSET"]
    assert "0x400000" in probs[0][1] and "0x800000" in probs[0][1]
    probs = _cfg(ns, GOOD_CONFIG.replace("OFFSET_REDUND=0x1000000", "OFFSET_REDUND=0xc00000"))
    assert [p[0] for p in probs] == ["OTAPULSE_UBOOT_ENV_OFFSET_REDUND"]
    probs = _cfg(ns, GOOD_CONFIG.replace("ENV_SIZE=0x20000", "ENV_SIZE=0x4000"))
    assert [p[0] for p in probs] == ["OTAPULSE_UBOOT_ENV_SIZE"]
    for p in probs:
        _msg_ok(p[1])


def test_dotconfig_env_not_on_mmc(ns):
    probs = _cfg(ns, GOOD_CONFIG.replace("CONFIG_ENV_IS_IN_MMC=y", "CONFIG_ENV_IS_IN_FAT=y"))
    assert probs and probs[0][0] == "OTAPULSE_UBOOT_AUTO_CONFIGURE"
    _msg_ok(probs[0][1])


HDR_BOTH = "#define CONFIG_BOOTCOUNT_ENV 1\n#define CONFIG_BOOTCOUNT_LIMIT 1\n"
NO_COUNTER = GOOD_CONFIG.replace("CONFIG_BOOTCOUNT_LIMIT=y\n", "").replace("CONFIG_BOOTCOUNT_ENV=y\n", "")


def _cfgh(ns, text, header):
    return ns["otapulse_full_dotconfig_problems"](text, 0x800000, 0x1000000, 0x20000, False, True, header)


def test_boot_counter_enabled_in_a_board_header_is_accepted(ns):
    """RPi / olimex style: Kconfig has no CONFIG_BOOTCOUNT_*, the board header #defines them."""
    assert _cfgh(ns, NO_COUNTER, HDR_BOTH) == []
    assert _cfgh(ns, NO_COUNTER, "#define CONFIG_BOOTCOUNT_ENV\n#define CONFIG_BOOTCOUNT_LIMIT\n") == []
    # the form the preprocessor really writes (u-boot.cfg of the live qemuarm64-bl run): empty value, trailing blank
    assert _cfgh(ns, NO_COUNTER, "#define CONFIG_BOOTCOUNT_LIMIT \n#define CONFIG_BOOTCOUNT_ENV \n") == []
    # one in Kconfig, the other in the header
    assert _cfgh(ns, NO_COUNTER + "CONFIG_BOOTCOUNT_LIMIT=y\n", "#define CONFIG_BOOTCOUNT_ENV 1\n") == []


BSP = LAYER.parent / "meta-otapulse-bsp"


@pytest.mark.parametrize(
    "patch",
    [
        "boards/raspberrypi/recipes-bsp/u-boot/u-boot-2024.01/0001-configs-rpi-enable-mender-requirements.patch",
        "boards/nxp/recipes-bsp/u-boot/u-boot-imx/olimex-imx8mp-evb/0001-configs-olimex-imx8mp-evb-mender-integration.patch",
    ],
)
def test_real_board_patches_enable_the_boot_counter_in_a_header(ns, patch):
    """The real RPi and olimex board-support patches turn the boot counter on with #defines in a C header
    (MENDER_UBOOT_AUTO_CONFIGURE = 0 boards): the lines they add must satisfy the check, and the Kconfig-only
    configuration of those boards must not."""
    text = (BSP / patch).read_text()
    added = "\n".join(l[1:] for l in text.splitlines() if l.startswith("+") and not l.startswith("+++"))
    assert "#define CONFIG_BOOTCOUNT_ENV" in added and "#define CONFIG_BOOTCOUNT_LIMIT" in added
    assert _cfgh(ns, NO_COUNTER, added) == []
    assert _cfgh(ns, NO_COUNTER, "") != []


def test_boot_counter_missing_everywhere_is_reported(ns):
    probs = _cfgh(ns, NO_COUNTER, "#define CONFIG_SOMETHING_ELSE 1\n")
    assert [p[0] for p in probs] == ["OTAPULSE_UBOOT_AUTO_CONFIGURE"]
    assert "CONFIG_BOOTCOUNT_LIMIT and CONFIG_BOOTCOUNT_ENV" in probs[0][1]
    assert "board header" in probs[0][1]
    _msg_ok(probs[0][1])
    # only one of the two in the header
    probs = _cfgh(ns, NO_COUNTER, "#define CONFIG_BOOTCOUNT_ENV 1\n")
    assert probs and "CONFIG_BOOTCOUNT_LIMIT" in probs[0][1] and "CONFIG_BOOTCOUNT_ENV " not in probs[0][1]
    # an explicit zero or a commented-out define is not "on"
    assert _cfgh(ns, NO_COUNTER, "#define CONFIG_BOOTCOUNT_ENV 0\n#define CONFIG_BOOTCOUNT_LIMIT 1\n")
    assert _cfgh(ns, NO_COUNTER, "// #define CONFIG_BOOTCOUNT_ENV\n#define CONFIG_BOOTCOUNT_LIMIT\n")


def test_boot_counter_not_judged_when_the_preprocessed_configuration_is_unavailable(ns):
    assert _cfgh(ns, NO_COUNTER, None) == []


def test_dotconfig_mmc_device_and_partition(ns):
    cfg = GOOD_CONFIG + "CONFIG_SYS_MMC_ENV_DEV=0\nCONFIG_SYS_MMC_ENV_PART=0\n"
    f = lambda text, mmc, final=True: ns["otapulse_full_dotconfig_problems"](
        text, 0x800000, 0x1000000, 0x20000, False, final, "#define CONFIG_BOOTCOUNT_LIMIT\n#define CONFIG_BOOTCOUNT_ENV\n", mmc)
    assert f(cfg, (0, 0)) == []
    assert f(cfg, (None, None)) == []
    probs = f(cfg.replace("ENV_DEV=0", "ENV_DEV=1"), (0, 0))
    assert [p[0] for p in probs] == ["OTAPULSE_STORAGE_DEVICE"] and "MMC device 1" in probs[0][1] and "/dev/mmcblk1" in probs[0][1]
    probs = f(cfg.replace("ENV_PART=0", "ENV_PART=1"), (0, 0))
    assert [p[0] for p in probs] == ["OTAPULSE_STORAGE_DEVICE"] and "hardware partition" in probs[0][1]
    for p in probs:
        _msg_ok(p[1])
    # also in the early look, before the board support compares and says "Mender expects"
    assert f(cfg.replace("ENV_DEV=0", "ENV_DEV=1"), (0, 0), final=False)


def test_expected_mmc_from_the_storage_variables(ns):
    f = ns["otapulse_full_expected_mmc"]
    assert f(FakeD(MENDER_STORAGE_DEVICE="/dev/mmcblk0", MENDER_UBOOT_STORAGE_DEVICE="", MENDER_UBOOT_CONFIG_SYS_MMC_ENV_PART="0")) == (0, 0)
    assert f(FakeD(MENDER_STORAGE_DEVICE="/dev/mmcblk2", MENDER_UBOOT_CONFIG_SYS_MMC_ENV_PART="1")) == (2, 1)
    assert f(FakeD(MENDER_STORAGE_DEVICE="/dev/sda", MENDER_UBOOT_STORAGE_DEVICE="3")) == (3, 0)  # explicit override wins
    assert f(FakeD(MENDER_STORAGE_DEVICE="/dev/vda")) == (None, 0)


def test_early_wrapper_reports_the_storage_device(ns, tmp_path):
    (tmp_path / ".config").write_text(GOOD_CONFIG + "CONFIG_SYS_MMC_ENV_DEV=1\nCONFIG_SYS_MMC_ENV_PART=0\n")
    d = FakeD(B=str(tmp_path), MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_1="0x800000", MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_2="0x1000000",
              BOOTENV_SIZE="0x20000", MENDER_FEATURES="mender-uboot", MENDER_STORAGE_DEVICE="/dev/mmcblk0")
    with pytest.raises(BBFatal, match="OTAPULSE_STORAGE_DEVICE"):
        ns["otapulse_full_check_uboot_config_early"](d)


def test_dotconfig_early_look_only_reports_contradictions(ns):
    # before configuration fragments are merged a missing option is not yet a problem ...
    bare = "CONFIG_ENV_IS_IN_FAT=y\n"
    assert ns["otapulse_full_dotconfig_problems"](bare, 0x800000, 0x1000000, 0x20000, False, False) == []
    # ... but a value that contradicts the agent's layout already is
    probs = ns["otapulse_full_dotconfig_problems"](
        GOOD_CONFIG.replace("ENV_SIZE=0x20000", "ENV_SIZE=0x40000"), 0x800000, 0x1000000, 0x20000, False, False)
    assert [p[0] for p in probs] == ["OTAPULSE_UBOOT_ENV_SIZE"]
    assert "CONFIG_ENV_SIZE = 0x40000" in probs[0][1]
    assert len(ns["otapulse_full_dotconfig_problems"](bare, 0x800000, 0x1000000, 0x20000, False, True)) >= 2


def test_dotconfig_ubi_only_checks_bootcount(ns):
    assert _cfg(ns, "CONFIG_BOOTCOUNT_LIMIT=y\nCONFIG_BOOTCOUNT_ENV=y\n", ubi=True) == []


# ---------------------------------------------------------------- compiled-in environment


ORANGE_ENV = (HERE / "data" / "u-boot-initial-env.orange-pi-zero2w").read_text()
RPI_ENV = (HERE / "data" / "u-boot-initial-env.raspberrypi4-64-bl").read_text()


def _boots(ns, env, entry="bootcmd"):
    return ns["otapulse_full_uboot_boot_trace"](env, entry)["boots"]


def test_real_environment_is_good(ns):
    env = ns["otapulse_full_uboot_env_dict"](REAL_ENV)
    assert ns["otapulse_full_uboot_env_problems"](env) == []
    for entry in ("bootcmd", "altbootcmd"):
        boots = _boots(ns, env, entry)
        assert len(boots) == 1, boots
        args = boots[0]
        assert re.search(r"root=/dev/mmcblk0p[23]\b", args), args
        assert ns["otapulse_full_last_panic"](args) == 10


def test_environment_panic_follows_the_setup_commands(ns):
    env = ns["otapulse_full_uboot_env_dict"](REAL_ENV)
    env["mender_post_setup_commands"] = "setenv bootargs ${bootargs} panic=7; setenv fdt_addr_r ${fdtcontroladdr}"
    assert ns["otapulse_full_last_panic"](_boots(ns, env)[0]) == 7
    assert ns["otapulse_full_uboot_env_problems"](env) == []


def test_environment_later_panic_wins_like_the_kernel(ns):
    env = ns["otapulse_full_uboot_env_dict"](REAL_ENV)
    env["bootargs"] = "console=ttyAMA0 panic=0"  # board default, then ours appended last
    assert ns["otapulse_full_last_panic"](_boots(ns, env)[0]) == 10
    env["mender_post_setup_commands"] = "setenv bootargs ${bootargs} panic=0"
    probs = ns["otapulse_full_uboot_env_problems"](env)
    assert probs and probs[0][0] == "OTAPULSE_PANIC_TIMEOUT_S" and "panic=0" in probs[0][1]


@pytest.mark.parametrize(
    "post",
    [
        "setenv bootargs ${bootargs} panic=10; setenv bootargs quiet",  # replaced afterwards
        "setenv bootargs ${bootargs} panic=10; setenv bootargs",  # unset afterwards
        "setenv bootargs ${bootargs} panic=10; setenv bootargs console=ttyS0 root=/dev/x",
        "setenv fdt_addr_r ${fdtcontroladdr}",  # no panic at all
        "",
    ],
)
def test_environment_panic_dropped_is_caught(ns, post):
    env = ns["otapulse_full_uboot_env_dict"](REAL_ENV)
    env["mender_post_setup_commands"] = post
    probs = ns["otapulse_full_uboot_env_problems"](env)
    assert probs, post
    assert probs[0][0] == "OTAPULSE_PANIC_TIMEOUT_S"
    _msg_ok(probs[0][1])


def test_environment_boot_command_replacing_bootargs_is_caught(ns):
    env = ns["otapulse_full_uboot_env_dict"](REAL_ENV)
    env["bootcmd"] = env["bootcmd"].replace("setenv bootargs root=${mender_kernel_root} ${bootargs}", "setenv bootargs root=${mender_kernel_root}")
    probs = ns["otapulse_full_uboot_env_problems"](env)
    assert probs and probs[0][0] == "OTAPULSE_PANIC_TIMEOUT_S"


def test_environment_without_a_boot_command_is_caught(ns):
    env = ns["otapulse_full_uboot_env_dict"](REAL_ENV)
    env["bootcmd"] = "run mender_setup"
    probs = ns["otapulse_full_uboot_env_problems"](env)
    assert probs and "never reaches a boot command" in probs[0][1]
    _msg_ok(probs[0][1])


def test_environment_rollback_path_is_checked_too(ns):
    env = ns["otapulse_full_uboot_env_dict"](REAL_ENV)
    env["altbootcmd"] = "setenv bootargs quiet; setenv upgrade_available 0; run bootcmd; booti ${kernel_addr_r}"
    # (the first boot command is reached through `run bootcmd`, which is fine; a direct one is not)
    env["altbootcmd"] = "setenv bootargs quiet; booti ${kernel_addr_r} - ${fdt_addr_r}"
    probs = ns["otapulse_full_uboot_env_problems"](env)
    assert probs and "altbootcmd" in probs[0][1]


def test_environment_bootcount_pieces(ns):
    env = ns["otapulse_full_uboot_env_dict"](REAL_ENV)
    env["bootlimit"] = "0"
    probs = ns["otapulse_full_uboot_env_problems"](env)
    assert probs[0][0] == "OTAPULSE_UBOOT_AUTO_CONFIGURE" and "bootlimit" in probs[0][1]
    env = ns["otapulse_full_uboot_env_dict"](REAL_ENV)
    del env["altbootcmd"]
    probs = ns["otapulse_full_uboot_env_problems"](env)
    assert any("altbootcmd" in p[1] for p in probs)
    for p in probs:
        _msg_ok(p[1])


# ---- finding 2/6/7: boot flows that leave the environment, empty bootcmd, every boot command ----


def test_distro_boot_flow_is_external_and_native_environment_is_refused(ns):
    """The real orange-pi-zero2w environment (a NATIVE image's U-Boot, which this class never builds): bootcmd=run
    distro_bootcmd leaves the environment, so the panic= trace is skipped, but a bootloader-mode environment
    always carries the board integration's boot counter and rollback commands, and this one has none."""
    env = ns["otapulse_full_uboot_env_dict"](ORANGE_ENV)
    assert env["bootcmd"] == "run distro_bootcmd"
    tr = ns["otapulse_full_uboot_boot_trace"](env, "bootcmd")
    assert tr["external"] is True and tr["boots"] == []
    probs = ns["otapulse_full_uboot_env_problems"](env)
    assert probs and probs[0][0] == "OTAPULSE_UBOOT_AUTO_CONFIGURE" and "bootlimit" in probs[0][1]
    assert not any("panic" in p[1] for p in probs)  # (the panic trace itself is skipped)
    _msg_ok(probs[0][1])


@pytest.mark.parametrize("missing", ["bootlimit", "upgrade_available", "altbootcmd"])
def test_external_flow_without_the_integration_fails(ns, missing):
    """A distro-boot board must not build green without a rollback: bootlimit, upgrade_available and altbootcmd
    are required whether or not the boot flow leaves the environment, and with or without mender_setup."""
    env = ns["otapulse_full_uboot_env_dict"](ORANGE_ENV)
    real = ns["otapulse_full_uboot_env_dict"](REAL_ENV)
    for k in ("bootlimit", "upgrade_available", "altbootcmd"):
        env[k] = real[k]
    assert "mender_setup" not in env
    assert ns["otapulse_full_uboot_env_problems"](env) == []  # the three variables are enough
    del env[missing]
    probs = ns["otapulse_full_uboot_env_problems"](env)
    assert probs and probs[0][0] == "OTAPULSE_UBOOT_AUTO_CONFIGURE"
    _msg_ok(probs[0][1])


def test_distro_boot_with_board_integration_still_needs_the_boot_counter(ns):
    """An RPi/NXP-style environment: distro boot + the board integration (mender_setup): bootlimit etc. are
    required, the panic timeout is not judged from the environment."""
    env = ns["otapulse_full_uboot_env_dict"](ORANGE_ENV)
    real = ns["otapulse_full_uboot_env_dict"](REAL_ENV)
    for k in ("mender_setup", "mender_altbootcmd", "mender_post_setup_commands", "mender_pre_setup_commands",
              "altbootcmd", "bootlimit", "upgrade_available", "bootcount", "mender_boot_part", "mender_boot_part_hex"):
        env[k] = real[k]
    assert env["bootcmd"] == "run distro_bootcmd"
    assert ns["otapulse_full_uboot_env_problems"](env) == []
    del env["bootlimit"]
    probs = ns["otapulse_full_uboot_env_problems"](env)
    assert probs and "bootlimit" in probs[0][1]
    _msg_ok(probs[0][1])
    for entry in ("source", "sysboot", "bootefi"):
        env2 = dict(env, bootlimit="1", bootcmd="%s ${scriptaddr}" % entry)
        assert ns["otapulse_full_uboot_boot_trace"](env2, "bootcmd")["external"] is True
        assert ns["otapulse_full_uboot_env_problems"](env2) == []


def test_empty_bootcmd_is_an_error_naming_the_variable(ns):
    env = ns["otapulse_full_uboot_env_dict"](REAL_ENV)
    env["bootcmd"] = ""
    probs = ns["otapulse_full_uboot_env_problems"](env)
    assert probs and probs[0][0] == "OTAPULSE_PANIC_TIMEOUT_S" and "no bootcmd" in probs[0][1]
    _msg_ok(probs[0][1])


def test_env_check_wrapper_empty_bootcmd_is_a_fatal_not_a_traceback(ns, tmp_path):
    env = REAL_ENV.replace(
        [l for l in REAL_ENV.splitlines() if l.startswith("bootcmd=")][0], "bootcmd=")
    (tmp_path / "u-boot-initial-env").write_text(env)
    with pytest.raises(BBFatal, match="OTAPULSE_PANIC_TIMEOUT_S"):
        ns["otapulse_full_check_uboot_env"](FakeD(B=str(tmp_path), OTAPULSE_PANIC_TIMEOUT_S="10"))


def test_env_check_wrapper_distro_boot_with_integration_passes(ns, tmp_path):
    env = ns["otapulse_full_uboot_env_dict"](ORANGE_ENV)
    real = ns["otapulse_full_uboot_env_dict"](REAL_ENV)
    for k in ("mender_setup", "mender_altbootcmd", "mender_post_setup_commands", "mender_pre_setup_commands",
              "altbootcmd", "bootlimit", "upgrade_available", "bootcount"):
        env[k] = real[k]
    (tmp_path / "u-boot-initial-env").write_text("\n".join("%s=%s" % kv for kv in env.items()) + "\n")
    ns["otapulse_full_check_uboot_env"](FakeD(B=str(tmp_path), OTAPULSE_PANIC_TIMEOUT_S="10"))
    assert any("boots through a boot script" in n for n in ns["_bb"].notes)
    (tmp_path / "u-boot-initial-env").write_text(ORANGE_ENV)  # the native environment: no rollback
    with pytest.raises(BBFatal, match="OTAPULSE_UBOOT_AUTO_CONFIGURE"):
        ns["otapulse_full_check_uboot_env"](FakeD(B=str(tmp_path), OTAPULSE_PANIC_TIMEOUT_S="10"))


def test_every_boot_command_is_judged(ns):
    """A later branch that drops panic= is seen: the boot commands after the first count too."""
    env = ns["otapulse_full_uboot_env_dict"](REAL_ENV)
    env["bootcmd"] = ("run mender_setup; booti ${kernel_addr_r} - ${fdt_addr_r}; "
                      "setenv bootargs quiet; booti ${kernel_addr_r} - ${fdt_addr_r}")
    boots = _boots(ns, env)
    assert len(boots) == 2 and "panic=10" in boots[0] and "panic" not in boots[1]
    probs = ns["otapulse_full_uboot_env_problems"](env)
    assert probs and "boot command 2" in probs[0][1]
    # an if/else whose else-branch replaces the command line
    env["bootcmd"] = ("run mender_setup; if test -e x; then booti a; else setenv bootargs quiet; booti b; fi")
    assert len(_boots(ns, env)) == 2
    assert ns["otapulse_full_uboot_env_problems"](env)


def test_tokenizer_list_operators(ns):
    stm = ns["otapulse_full_uboot_statements"]
    got = list(stm("a b && c d || e;f", lambda n: ""))
    assert got == [["a", "b"], ["c", "d"], ["e"], ["f"]]
    # quoted and escaped operators are data, a single & or | is not an operator
    got = list(stm('echo "a && b" x\\&&y p&q r|s', lambda n: ""))
    assert got == [["echo", "a && b", "x&&y", "p&q", "r|s"]]


@pytest.mark.parametrize(
    "post",
    [
        "setenv bootargs ${bootargs} panic=10 && setenv bootargs quiet",
        "setenv bootargs ${bootargs} panic=10 || setenv bootargs quiet",
        "test -n x && setenv bootargs quiet",
        "env set bootargs quiet",
        "setenv bootargs ${bootargs} panic=10; env set bootargs quiet",
        "setenv bootargs ${bootargs} panic=10; env set -f bootargs quiet",
        "setenv bootargs ${bootargs} panic=10; setenv -f bootargs quiet",
    ],
)
def test_operators_and_env_set_that_drop_panic_are_seen(ns, post):
    env = ns["otapulse_full_uboot_env_dict"](REAL_ENV)
    env["mender_post_setup_commands"] = post
    probs = ns["otapulse_full_uboot_env_problems"](env)
    assert probs and probs[0][0] == "OTAPULSE_PANIC_TIMEOUT_S", post


def test_operators_and_env_set_that_keep_panic_pass(ns):
    env = ns["otapulse_full_uboot_env_dict"](REAL_ENV)
    for post in ("test -n x && setenv bootargs ${bootargs} panic=9",
                 "env set bootargs ${bootargs} panic=9",
                 "setenv foo 1 && env set bootargs ${bootargs} panic=9 || echo no"):
        env["mender_post_setup_commands"] = post
        assert ns["otapulse_full_uboot_env_problems"](env) == [], post
        assert ns["otapulse_full_last_panic"](_boots(ns, env)[0]) == 9


def test_trace_follows_run_inside_a_chain(ns):
    env = {"bootcmd": "run a && run b", "a": "setenv bootargs x panic=3", "b": "setenv bootargs ${bootargs} y; booti k"}
    assert ns["otapulse_full_uboot_boot_trace"](env, "bootcmd")["boots"] == ["x panic=3 y"]


def test_tokenizer_basics(ns):
    stm = ns["otapulse_full_uboot_statements"]
    got = list(stm('a "b c" d;e  f ; ; g\\;h \'x;y\'', lambda n: ""))
    assert got == [["a", "b c", "d"], ["e", "f"], ["g;h", "x;y"]]
    got = list(stm("setenv x ${v}$w", lambda n: {"v": "1", "w": "2"}.get(n, "")))
    assert got == [["setenv", "x", "12"]]


# ---------------------------------------------------------------- GRUB debug options


@pytest.mark.parametrize("opt", ["force-grub-prompt", "debug-pause"])
def test_grub_debug_options_refused(ns, opt):
    d = FakeD(PACKAGECONFIG="debug-log %s" % opt, OTAPULSE_ALLOW_GRUB_DEBUG="0")
    with pytest.raises(BBFatal) as e:
        ns["otapulse_full_check_grub_debug"](d)
    assert opt in str(e.value) and "OTAPULSE_ALLOW_GRUB_DEBUG" in str(e.value) and "PACKAGECONFIG" in str(e.value)
    _msg_ok(str(e.value))


def test_grub_debug_opt_out_warns(ns):
    d = FakeD(PACKAGECONFIG="force-grub-prompt debug-pause", OTAPULSE_ALLOW_GRUB_DEBUG="1")
    ns["otapulse_full_check_grub_debug"](d)
    w = ns["_bb"].warnings
    assert len(w) == 1 and "force-grub-prompt and debug-pause" in w[0] and "NEVER" in w[0]


def test_grub_debug_clean_config_passes(ns):
    ns["otapulse_full_check_grub_debug"](FakeD(PACKAGECONFIG="debug-log", OTAPULSE_ALLOW_GRUB_DEBUG="0"))
    ns["otapulse_full_check_grub_debug"](FakeD(PACKAGECONFIG=None, OTAPULSE_ALLOW_GRUB_DEBUG="0"))


# ---------------------------------------------------------------- state scripts, boot_mode


def test_real_state_scripts_carry_the_gate(ns):
    for name in ("ArtifactReboot_Enter_01", "ArtifactRollbackReboot_Enter_01", "ArtifactCommit_Enter_01"):
        assert ns["otapulse_full_script_gate_problem"](str(SCRIPTS / name)) is None, name


def test_gate_problems(ns, tmp_path):
    f = ns["otapulse_full_script_gate_problem"]
    src = (SCRIPTS / "ArtifactReboot_Enter_01").read_text()
    p = tmp_path / "s"
    p.write_text(src.replace("OTAPULSE-BOOT-MODE-GATE BEGIN", "REMOVED"))
    assert "does not carry" in f(str(p))
    p.write_text(src.replace("OTAPULSE-BOOT-MODE-GATE END", "REMOVED"))
    assert "does not carry" in f(str(p))
    p.write_text(src.replace("/etc/otapulse/boot_mode", "/etc/other"))
    assert "does not read /etc/otapulse/boot_mode" in f(str(p))
    assert "cannot be read" in f(str(tmp_path / "missing"))


def _agent_d(filespath, names=("ArtifactReboot_Enter_01", "ArtifactRollbackReboot_Enter_01", "ArtifactCommit_Enter_01")):
    return FakeD(SRC_URI=" ".join("file://%s" % n for n in names) + " file://device_type", FILESPATH=str(filespath))


def test_agent_recipe_gate_ok(ns):
    ns["otapulse_full_check_agent_recipe"](_agent_d(SCRIPTS))


def test_agent_recipe_src_uri_of_the_real_recipe(ns):
    recipe = (SCRIPTS.parent / "soc-ota-agent_1.0.0.bb").read_text()
    src_uri = re.search(r'^SRC_URI = " \\\n(.*?)^"', recipe, re.S | re.M).group(1)
    d = FakeD(SRC_URI=src_uri, FILESPATH=str(SCRIPTS))
    ns["otapulse_full_check_agent_recipe"](d)
    # all three state scripts of the recipe are the ones that get checked
    seen = []
    orig = ns["otapulse_full_script_gate_problem"]
    ns["otapulse_full_script_gate_problem"] = lambda p: (seen.append(Path(p).name), orig(p))[1]
    ns["otapulse_full_check_agent_recipe"](d)
    assert sorted(seen) == ["ArtifactCommit_Enter_01", "ArtifactReboot_Enter_01", "ArtifactRollbackReboot_Enter_01"]


def test_agent_recipe_ungated_script_refused(ns, tmp_path):
    for n in ("ArtifactReboot_Enter_01", "ArtifactRollbackReboot_Enter_01", "ArtifactCommit_Enter_01"):
        shutil.copy(SCRIPTS / n, tmp_path / n)
    p = tmp_path / "ArtifactCommit_Enter_01"
    p.write_text(p.read_text().replace("OTAPULSE-BOOT-MODE-GATE BEGIN", "X").replace("OTAPULSE-BOOT-MODE-GATE END", "Y"))
    with pytest.raises(BBFatal) as e:
        ns["otapulse_full_check_agent_recipe"](_agent_d(tmp_path))
    assert "ArtifactCommit_Enter_01" in str(e.value) and "OTAPULSE-BOOT-MODE-GATE" in str(e.value)
    assert "OTAPULSE_BOOT_MODE" in str(e.value)
    _msg_ok(str(e.value))


def test_agent_recipe_missing_script_and_override_by_filespath(ns, tmp_path):
    # a script missing from FILESPATH is refused; an override placed FIRST on FILESPATH is the one checked
    with pytest.raises(BBFatal, match="was not found"):
        ns["otapulse_full_check_agent_recipe"](_agent_d(tmp_path))
    shutil.copy(SCRIPTS / "ArtifactReboot_Enter_01", tmp_path / "ArtifactReboot_Enter_01")
    (tmp_path / "ArtifactReboot_Enter_01").write_text("#!/bin/sh\nexit 0\n")
    with pytest.raises(BBFatal, match="ArtifactReboot_Enter_01"):
        ns["otapulse_full_check_agent_recipe"](_agent_d("%s:%s" % (tmp_path, SCRIPTS)))
    with pytest.raises(BBFatal, match="ships no"):
        ns["otapulse_full_check_agent_recipe"](FakeD(SRC_URI="file://device_type", FILESPATH=str(SCRIPTS)))


# ---------------------------------------------------------------- the finished root file system


def _rootfs(tmp_path, *, boot_mode="bootloader\n", gate=True, fw="/dev/mmcblk0 0x800000 0x20000\n/dev/mmcblk0 0x1000000 0x20000\n"):
    r = tmp_path / "rootfs"
    (r / "etc/otapulse").mkdir(parents=True)
    if boot_mode is not None:
        (r / "etc/otapulse/boot_mode").write_text(boot_mode)
    sd = r / "usr/share/otapulse/state-scripts"
    sd.mkdir(parents=True)
    for n in ("ArtifactReboot_Enter_01", "ArtifactRollbackReboot_Enter_01", "ArtifactCommit_Enter_01"):
        shutil.copy(SCRIPTS / n, sd / n)
    if not gate:
        p = sd / "ArtifactRollbackReboot_Enter_01"
        p.write_text(p.read_text().replace("OTAPULSE-BOOT-MODE-GATE BEGIN", "X"))
    if fw is not None:
        (r / "data/u-boot").mkdir(parents=True)
        (r / "data/u-boot/fw_env.config").write_text(fw)
    return r


def test_rootfs_good(ns, tmp_path):
    r = _rootfs(tmp_path)
    assert ns["otapulse_full_rootfs_problems"](str(r), "uboot") == []
    assert ns["otapulse_full_rootfs_problems"](str(r), "grub") == []


@pytest.mark.parametrize("bad", [None, "native\n", ""])
def test_rootfs_boot_mode_missing_or_wrong(ns, tmp_path, bad):
    r = _rootfs(tmp_path, boot_mode=bad)
    probs = ns["otapulse_full_rootfs_problems"](str(r), "uboot")
    assert probs and probs[0][0] == "OTAPULSE_BOOT_MODE" and "/etc/otapulse/boot_mode" in probs[0][1]
    _msg_ok(probs[0][1])


def test_rootfs_ungated_script_and_missing_script(ns, tmp_path):
    r = _rootfs(tmp_path, gate=False)
    probs = ns["otapulse_full_rootfs_problems"](str(r), "uboot")
    assert probs and "ArtifactRollbackReboot_Enter_01" in probs[0][1] and "OTAPULSE-BOOT-MODE-GATE" in probs[0][1]
    r2 = tmp_path / "second"
    r2.mkdir()
    r = _rootfs(r2)
    (r / "usr/share/otapulse/state-scripts/ArtifactCommit_Enter_01").unlink()
    probs = ns["otapulse_full_rootfs_problems"](str(r), "uboot")
    assert probs and "ArtifactCommit_Enter_01" in probs[0][1]


def test_rootfs_fw_env_config(ns, tmp_path):
    r = _rootfs(tmp_path, fw="/dev/mmcblk0 0x800000 0x20000\n")
    probs = ns["otapulse_full_rootfs_problems"](str(r), "uboot")
    assert probs and probs[0][0] == "OTAPULSE_UBOOT_ENV_OFFSET_REDUND"
    (tmp_path / "b").mkdir()
    r = _rootfs(tmp_path / "b", fw="/dev/mmcblk0 0x800000 0x20000\n/dev/mmcblk0 0x800000 0x20000\n")
    probs = ns["otapulse_full_rootfs_problems"](str(r), "uboot")
    assert probs and "not redundant" in probs[0][1]
    (tmp_path / "c").mkdir()
    r = _rootfs(tmp_path / "c", fw=None)
    probs = ns["otapulse_full_rootfs_problems"](str(r), "uboot")
    assert probs and probs[0][0] == "OTAPULSE_UBOOT_ENV_OFFSET"
    assert ns["otapulse_full_rootfs_problems"](str(r), "grub") == []  # GRUB images have no fw_env.config


# ---------------------------------------------------------------- the python wrappers


def test_uboot_config_early_wrapper(ns, tmp_path):
    d = FakeD(B=str(tmp_path), MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_1="0x800000",
              MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_2="0x1000000", BOOTENV_SIZE="0x20000", MENDER_FEATURES="mender-uboot")
    ns["otapulse_full_check_uboot_config_early"](d)  # no .config yet: nothing to say
    (tmp_path / ".config").write_text(GOOD_CONFIG.replace("ENV_OFFSET=0x800000", "ENV_OFFSET=0x400000"))
    with pytest.raises(BBFatal, match="OTAPULSE_UBOOT_ENV_OFFSET"):
        ns["otapulse_full_check_uboot_config_early"](d)


def test_uboot_config_wrapper(ns, tmp_path):
    (tmp_path / ".config").write_text(GOOD_CONFIG)
    (tmp_path / "u-boot.cfg").write_text("")
    d = FakeD(
        B=str(tmp_path),
        MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_1="0x800000",
        MENDER_UBOOT_ENV_STORAGE_DEVICE_OFFSET_2="0x1000000",
        BOOTENV_SIZE="0x20000",
        MENDER_FEATURES="mender-uboot",
    )
    ns["otapulse_full_check_uboot_config"](d)
    (tmp_path / ".config").write_text(GOOD_CONFIG.replace("CONFIG_BOOTCOUNT_ENV=y", ""))
    with pytest.raises(BBFatal, match="OTAPULSE_UBOOT_AUTO_CONFIGURE"):
        ns["otapulse_full_check_uboot_config"](d)
    (tmp_path / ".config").unlink()
    with pytest.raises(BBFatal, match="no U-Boot configuration"):
        ns["otapulse_full_check_uboot_config"](d)


def test_uboot_env_wrapper(ns, tmp_path):
    (tmp_path / "u-boot-initial-env").write_text(REAL_ENV)
    d = FakeD(B=str(tmp_path), OTAPULSE_PANIC_TIMEOUT_S="10")
    ns["otapulse_full_check_uboot_env"](d)
    assert any("panic=10" in n for n in ns["_bb"].notes)
    d.v["OTAPULSE_PANIC_TIMEOUT_S"] = "7"  # the environment still says 10: warn, do not fail
    ns["otapulse_full_check_uboot_env"](d)
    assert any("OTAPULSE_PANIC_TIMEOUT_S = 7" in w for w in ns["_bb"].warnings)
    (tmp_path / "u-boot-initial-env").write_text(REAL_ENV.replace("panic=10", "panic=0"))
    with pytest.raises(BBFatal, match="OTAPULSE_PANIC_TIMEOUT_S"):
        ns["otapulse_full_check_uboot_env"](d)
    (tmp_path / "u-boot-initial-env").unlink()
    with pytest.raises(BBFatal, match="was not generated"):
        ns["otapulse_full_check_uboot_env"](d)


def test_rootfs_wrapper(ns, tmp_path):
    r = _rootfs(tmp_path)
    kw = dict(IMAGE_ROOTFS=str(r), OTAPULSE_BOOTLOADER="uboot", IMAGE_INSTALL="soc-ota-agent", PACKAGE_INSTALL="")
    ns["otapulse_bl_rootfs_checks"](FakeD(**kw))
    (r / "etc/otapulse/boot_mode").unlink()
    with pytest.raises(BBFatal, match="OTAPULSE_BOOT_MODE"):
        ns["otapulse_bl_rootfs_checks"](FakeD(**kw))


# ---------------------------------------------------------------- wiring / native inertness


def test_hooks_are_wired():
    assert "otapulse_full_check_uboot_vars(d)" in BL  # configuration handler
    assert re.search(r"appendVarFlag\('do_configure', 'postfuncs', ' otapulse_full_uboot_cfg "
                     r"otapulse_full_check_uboot_config'\)", BL)
    assert re.search(r"appendVarFlag\('do_compile', 'postfuncs', ' otapulse_full_check_uboot_env'\)", BL)
    assert re.search(r"appendVarFlag\('do_configure', 'prefuncs', ' otapulse_full_uboot_defconfig "
                     r"otapulse_full_check_uboot_config_early'\)", BL)
    assert "otapulse_full_check_uboot_layout(d)" in BL
    assert "otapulse_full_check_grub_debug(d)" in BL
    assert "otapulse_full_check_agent_recipe(d)" in BL
    assert re.search(r'^ROOTFS_POSTPROCESS_COMMAND \+= "otapulse_bl_rootfs_checks;"$', BL, re.M)
    # every customer-facing variable the new checks name is documented in the entry class
    for var in ("OTAPULSE_UBOOT_ENV_OFFSET", "OTAPULSE_UBOOT_ENV_OFFSET_REDUND", "OTAPULSE_UBOOT_ENV_SIZE",
                "OTAPULSE_UBOOT_AUTO_CONFIGURE", "OTAPULSE_ALLOW_GRUB_DEBUG"):
        assert var in FULL, var
        assert var in (LAYER / "conf/samples/local.conf.bootloader.sample").read_text(), var


def test_native_mode_is_untouched():
    """Nothing in the entry class runs for native mode, and the sanity class has no executable change."""
    sel = FULL[FULL.index("def otapulse_full_select"):FULL.index("inherit ${@otapulse_full_select(d)}")]
    native = sel[sel.index("if mode == 'native':"):sel.index("if mode != 'bootloader':")]
    assert "return ''" in native and "setVar" not in native and "fatal" not in native
    # the new class-level variable and hooks exist only in the bootloader-mode class
    assert "OTAPULSE_ALLOW_GRUB_DEBUG ?=" not in FULL
    assert "ROOTFS_POSTPROCESS_COMMAND" not in FULL
    sanity = (CLASSES / "otapulse-sanity.bbclass").read_text()
    code = "\n".join(l for l in sanity.splitlines() if not l.lstrip().startswith("#"))
    assert "otapulse_full" not in code and "OTAPULSE_UBOOT" not in code and "OTAPULSE_BOOT_MODE" not in code


# ---- TASK-S109-019: boards that boot through a boot script (Raspberry Pi 4, "bootflow scan") ----

# the board support's boot script after the recipe's substitutions (rpi-u-boot-scr + the OTA-Pulse bbappend)
RPI_BOOT_CMD = """fdt addr ${fdt_addr} && fdt get value bootargs /chosen bootargs
run mender_setup
mmc dev ${mender_uboot_dev}
load ${mender_uboot_root} ${kernel_addr_r} /boot/Image
booti ${kernel_addr_r} - ${fdt_addr}
run mender_try_to_recover
"""
# the stock meta-raspberrypi script that a bootloader-mode image gets when the board support's own is not used
RPI_STOCK_CMD = """fdt addr ${fdt_addr} && fdt get value bootargs /chosen bootargs
fatload mmc 0:1 ${kernel_addr_r} Image
if test ! -e mmc 0:1 uboot.env; then saveenv; fi;
booti ${kernel_addr_r} - ${fdt_addr}
"""
RPI_CMDLINE_OK = "dwc_otg.lpm_enable=0 console=serial0,115200 root=${mender_kernel_root} rootfstype=ext4 rootwait net.ifnames=0\n"
RPI_CMDLINE_FIXED = "dwc_otg.lpm_enable=0 console=serial0,115200 root=/dev/mmcblk0p2 rootfstype=ext4 rootwait net.ifnames=0\n"


def _script_image(text: str) -> bytes:
    """A legacy uImage of type script, as mkimage -T script writes it."""
    import struct
    body = text.encode()
    data = struct.pack(">II", len(body), 0) + body
    hdr = struct.pack(">IIIIIIIBBBB32s", 0x27051956, 0, 0, len(data), 0, 0, 0, 5, 2, 6, 0, b"Boot script")
    return hdr + data


def _rpi_env(ns, panic=7):
    env = ns["otapulse_full_uboot_env_dict"](RPI_ENV)
    env["mender_post_setup_commands"] = "setenv bootargs ${bootargs} panic=%d; " % panic
    return env


def test_rpi_bootcmd_bootflow_scan_is_an_external_flow(ns):
    """The Pi's U-Boot boots with "bootflow scan" (standard boot): it leaves the environment, so the compiled-in
    environment check must not claim "never reaches a boot command" (found building raspberrypi4-64-bl)."""
    env = ns["otapulse_full_uboot_env_dict"](RPI_ENV)
    assert env["bootcmd"] == "bootflow scan"
    tr = ns["otapulse_full_uboot_boot_trace"](env, "bootcmd")
    assert tr["external"] is True and tr["boots"] == []
    assert ns["otapulse_full_uboot_env_problems"](env) == []


def test_rpi_env_check_wrapper_passes_and_notes_the_boot_script(ns, tmp_path):
    (tmp_path / "u-boot-initial-env").write_text(RPI_ENV)
    ns["otapulse_full_check_uboot_env"](FakeD(B=str(tmp_path), OTAPULSE_PANIC_TIMEOUT_S="7"))
    assert any("boots through a boot script" in n for n in ns["_bb"].notes)


def test_boot_script_text_decoding(ns):
    txt = ns["otapulse_full_boot_script_text"]
    got = txt(_script_image(RPI_BOOT_CMD))
    assert got.startswith("fdt addr ${fdt_addr} && fdt get value bootargs /chosen bootargs; run mender_setup;")
    assert got.endswith("run mender_try_to_recover")
    assert txt(RPI_BOOT_CMD) == got                       # plain boot.cmd text
    assert txt("# a comment\n\nbooti a\n") == "booti a"
    assert txt(b"") == ""


def test_boot_script_good_rpi_image(ns):
    probs = ns["otapulse_full_boot_script_problems"](_script_image(RPI_BOOT_CMD), _rpi_env(ns), RPI_CMDLINE_OK)
    assert probs == []


def test_boot_script_effective_command_line_has_panic_and_the_slot_root(ns):
    env = _rpi_env(ns, panic=7)
    env["__boot_script"] = ns["otapulse_full_boot_script_text"](RPI_BOOT_CMD)
    tr = ns["otapulse_full_uboot_boot_trace"](env, "__boot_script", fdt_bootargs=" ".join(RPI_CMDLINE_OK.split()))
    assert len(tr["boots"]) == 1
    args = tr["boots"][0]
    assert ns["otapulse_full_last_panic"](args) == 7
    assert "root=/dev/mmcblk0p2" in args            # ${mender_kernel_root} resolved by the boot-slot setup
    assert "${" not in args


def test_boot_script_stock_script_without_slot_setup_is_refused(ns):
    """The stock meta-raspberrypi script loads Image from the FAT partition and never runs the boot-slot setup: no
    slot choice, no panic=, no rollback. This is what the image got before the OTA-Pulse boot script was wired."""
    probs = ns["otapulse_full_boot_script_problems"](_script_image(RPI_STOCK_CMD), _rpi_env(ns), RPI_CMDLINE_OK)
    assert probs and probs[0][0] == "OTAPULSE_BOOT_MODE" and "boot-slot setup" in probs[0][1]
    _msg_ok(probs[0][1])


@pytest.mark.parametrize("post", [
    "",                                            # nothing sets panic= at all
    "setenv bootargs ${bootargs} panic=0; ",
    "setenv bootargs quiet; ",                      # replaces the command line, panic= lost
])
def test_boot_script_panic_dropped_is_refused(ns, post):
    env = _rpi_env(ns)
    env["mender_post_setup_commands"] = post
    probs = ns["otapulse_full_boot_script_problems"](_script_image(RPI_BOOT_CMD), env, RPI_CMDLINE_OK)
    assert probs and probs[0][0] == "OTAPULSE_PANIC_TIMEOUT_S" and "panic" in probs[0][1]
    _msg_ok(probs[0][1])


def test_boot_script_panic_from_cmdline_txt_counts(ns):
    env = _rpi_env(ns)
    env["mender_post_setup_commands"] = ""
    cmd = RPI_CMDLINE_OK.strip() + " panic=9\n"
    assert ns["otapulse_full_boot_script_problems"](_script_image(RPI_BOOT_CMD), env, cmd) == []


def test_boot_script_fixed_root_in_cmdline_is_refused(ns):
    probs = ns["otapulse_full_boot_script_problems"](_script_image(RPI_BOOT_CMD), _rpi_env(ns), RPI_CMDLINE_FIXED)
    assert probs and probs[0][0] == "OTAPULSE_BOOT_MODE" and "fixed root=/dev/mmcblk0p2" in probs[0][1]
    _msg_ok(probs[0][1])


def test_boot_script_without_a_boot_command_and_empty_script(ns):
    probs = ns["otapulse_full_boot_script_problems"]("run mender_setup\n", _rpi_env(ns), RPI_CMDLINE_OK)
    assert probs and probs[0][0] == "OTAPULSE_PANIC_TIMEOUT_S" and "never reaches a boot command" in probs[0][1]
    probs = ns["otapulse_full_boot_script_problems"](b"", _rpi_env(ns), RPI_CMDLINE_OK)
    assert probs and probs[0][0] == "OTAPULSE_BOOT_MODE"


def _bootfs(tmp_path, script=RPI_BOOT_CMD, cmdline=RPI_CMDLINE_OK, env=None):
    w = tmp_path / "work"
    (w / "bootfs").mkdir(parents=True)
    dep = tmp_path / "deploy"
    dep.mkdir()
    if script is not None:
        (w / "bootfs" / "boot.scr").write_bytes(_script_image(script))
    if cmdline is not None:
        (w / "bootfs" / "cmdline.txt").write_text(cmdline)
    (dep / "u-boot-initial-env").write_text(env if env is not None else RPI_ENV.replace("panic=7", "panic=7"))
    return FakeD(WORKDIR=str(w), DEPLOY_DIR_IMAGE=str(dep))


def test_bootfs_wrapper_good_and_bad(ns, tmp_path):
    ns["otapulse_bl_bootfs_checks"](_bootfs(tmp_path))
    assert any("runs the boot-slot setup and hands the kernel panic=7" in n for n in ns["_bb"].notes)
    with pytest.raises(BBFatal, match="OTAPULSE_BOOT_MODE"):
        ns["otapulse_bl_bootfs_checks"](_bootfs(tmp_path / "b", script=RPI_STOCK_CMD))
    with pytest.raises(BBFatal, match="fixed root="):
        ns["otapulse_bl_bootfs_checks"](_bootfs(tmp_path / "c", cmdline=RPI_CMDLINE_FIXED))
    with pytest.raises(BBFatal, match="OTAPULSE_PANIC_TIMEOUT_S"):
        ns["otapulse_bl_bootfs_checks"](_bootfs(tmp_path / "d", env=RPI_ENV.replace("panic=7", "panic=0")))


def test_bootfs_wrapper_skips_boards_without_a_boot_script(ns, tmp_path):
    ns["otapulse_bl_bootfs_checks"](_bootfs(tmp_path, script=None))
    assert any("boot script checks skipped" in n for n in ns["_bb"].notes)


def test_bootfs_check_is_hooked_to_the_sd_image_only_in_u_boot_mode():
    assert "d.appendVarFlag('do_image_sdimg', 'postfuncs', ' otapulse_bl_bootfs_checks')" in BL
    hook = BL[BL.index("TASK-S109-019: boards that boot through a boot script"):]
    hook = hook[:hook.index("TASK-S109-012: GRUB debug options")]
    assert "inherits_class('image', d)" in hook and "OTAPULSE_BOOTLOADER') == 'uboot'" in hook


# ---- TASK-S109-019 (found on the bench RPi4): a long kernel command line and U-Boot's argument limit ----

# what the Raspberry Pi firmware really puts into /chosen/bootargs (26 words), taken from the bench board
RPI_FW_BOOTARGS = (
    "coherent_pool=1M 8250.nr_uarts=1 snd_bcm2835.enable_headphones=0 cgroup_disable=memory numa_policy=interleave "
    "snd_bcm2835.enable_hdmi=0 video=HDMI-A-1:640x480M@60D numa=fake=2 system_heap.max_order=0 "
    "smsc95xx.macaddr=00:00:00:00:00:00 vc_mem.mem_base=0x3ec00000 vc_mem.mem_size=0x40000000  dwc_otg.lpm_enable=0 "
    "console=ttyAMA0,115200 rootfstype=ext4 rootwait root=${mender_kernel_root}  net.ifnames=0 ")


def test_unquoted_expansion_is_split_into_words_and_a_long_command_is_refused(ns):
    stm = ns["otapulse_full_uboot_statements"]
    env = {"b": " ".join("w%d" % i for i in range(20)), "short": "x y"}
    got = list(stm("echo ${short} z; setenv bootargs ${b} panic=7; setenv bootargs \"${b} panic=7\"", lambda n: env.get(n, "")))
    assert got[0] == ["echo", "x", "y", "z"]          # an unquoted expansion is split
    assert got[1] == ["__too_many_args__"]            # 2 + 20 + 1 words: refused by U-Boot (CONFIG_SYS_MAXARGS = 16)
    assert got[2][:2] == ["setenv", "bootargs"] and len(got[2]) == 3   # quoted: ONE argument, always accepted
    assert got[2][2].endswith(" panic=7") and got[2][2].count(" ") == 20
    # an empty unquoted expansion makes no word, an empty quoted one makes an empty word
    assert list(stm("a ${e} b ", lambda n: "")) == [["a", "b"]]
    assert list(stm('a "${e}" b', lambda n: "")) == [["a", "", "b"]]


def test_unquoted_post_setup_command_loses_panic_on_the_pi_but_the_quoted_one_keeps_it(ns):
    """The bench board's kernel command line had no panic=7 although the setup commands carried it: the unquoted
    'setenv bootargs ${bootargs} panic=7' has 28 arguments after expansion and U-Boot refuses it. The check must
    see what the board saw, and the shipped (quoted) form must pass."""
    env = _rpi_env(ns)                                   # post commands are the old, unquoted form
    fw = " ".join(RPI_FW_BOOTARGS.split())
    probs = ns["otapulse_full_boot_script_problems"](_script_image(RPI_BOOT_CMD), env, fw)
    assert probs and probs[0][0] == "OTAPULSE_PANIC_TIMEOUT_S" and "does not contain panic=" in probs[0][1]
    _msg_ok(probs[0][1])
    env["mender_post_setup_commands"] = 'setenv bootargs "${bootargs} panic=7"; '
    assert ns["otapulse_full_boot_script_problems"](_script_image(RPI_BOOT_CMD), env, fw) == []
    env["mender_post_setup_commands"] = 'setenv bootargs "${bootargs} panic=0"; '
    probs = ns["otapulse_full_boot_script_problems"](_script_image(RPI_BOOT_CMD), env, fw)
    assert probs and "panic=0" in probs[0][1]


def test_the_shipped_post_setup_command_is_quoted():
    """The class assignment (and the generated C header it ends up in) must carry the quoted form."""
    m = re.search(r'^MENDER_UBOOT_POST_SETUP_COMMANDS:prepend:mender-uboot = "(.*)"$', BL, re.M)
    assert m, "assignment not found"
    val = m.group(1)
    assert val.startswith('setenv bootargs \\\\\\"\\${bootargs} panic=${OTAPULSE_PANIC_TIMEOUT_S}\\\\\\"')
    # simulate the shell that writes the header ("\\${" -> "${", "\\\\\\"" -> "\\\"") and the C compiler
    import subprocess
    shell = 'OTAPULSE_PANIC_TIMEOUT_S=7; X="%s"; printf "%%s" "$X"' % val.replace("${OTAPULSE_PANIC_TIMEOUT_S}", "7")
    c_text = subprocess.run(["bash", "-c", shell], capture_output=True, text=True).stdout
    assert c_text == 'setenv bootargs \\"${bootargs} panic=7\\"; '
    env_text = c_text.encode().decode("unicode_escape")   # what the C string literal contains
    assert env_text == 'setenv bootargs "${bootargs} panic=7"; '


def test_bootfs_wrapper_judges_a_pi_against_the_firmware_command_line(ns, tmp_path):
    """Found on the bench board: cmdline.txt alone is short (7 words); the firmware prepends 12 more, which is what the
    boot script really appends to. A boot partition with config.txt (the Pi firmware) is checked against that."""
    # the stored test env is the old unquoted shape or the new one; normalise both explicitly
    quoted = re.sub(r"^mender_post_setup_commands=.*$", 'mender_post_setup_commands=setenv bootargs "${bootargs} panic=7"; ',
                    RPI_ENV, flags=re.M)
    unquoted = re.sub(r"^mender_post_setup_commands=.*$", "mender_post_setup_commands=setenv bootargs ${bootargs} panic=7; ",
                      RPI_ENV, flags=re.M)
    d = _bootfs(tmp_path / "q", env=quoted)
    (Path(d.getVar("WORKDIR")) / "bootfs" / "config.txt").write_text("enable_uart=1\n")
    ns["otapulse_bl_bootfs_checks"](d)
    d = _bootfs(tmp_path / "u", env=unquoted)
    (Path(d.getVar("WORKDIR")) / "bootfs" / "config.txt").write_text("enable_uart=1\n")
    with pytest.raises(BBFatal, match="OTAPULSE_PANIC_TIMEOUT_S.*does not contain panic="):
        ns["otapulse_bl_bootfs_checks"](d)
    # without config.txt (no Pi firmware) the short command line alone is judged, like before
    d = _bootfs(tmp_path / "n", env=unquoted)
    ns["otapulse_bl_bootfs_checks"](d)


def test_maxargs_is_read_from_the_uboot_configuration_with_a_default_of_16(ns):
    f = ns["otapulse_full_maxargs"]
    assert f("") == 16 and f(None) == 16
    assert f("CONFIG_FOO=y\nCONFIG_SYS_MAXARGS=64\nCONFIG_BAR=1\n") == 64
    assert f("/* x */\n#define CONFIG_SYS_MAXARGS 32\n") == 32
    assert f("# CONFIG_SYS_MAXARGS is not set\n") == 16
    assert f("CONFIG_SYS_MAXARGS=1\n") == 16          # unusable value: default
    assert f("CONFIG_SYS_MAXARGS=abc\n") == 16


def test_a_larger_maxargs_lets_the_unquoted_form_work_and_a_smaller_one_breaks_more(ns):
    stm = ns["otapulse_full_uboot_statements"]
    env = {"b": " ".join("w%d" % i for i in range(20))}
    cmd = "setenv bootargs ${b} panic=7"
    assert list(stm(cmd, lambda n: env.get(n, "")))[0] == ["__too_many_args__"]            # default 16
    assert list(stm(cmd, lambda n: env.get(n, ""), 64))[0][:2] == ["setenv", "bootargs"]    # a U-Boot built with 64
    assert list(stm("a b c d", lambda n: "", 3))[0] == ["__too_many_args__"]
    # the 26-word Pi command line: refused with 16, accepted by a U-Boot built with a larger limit
    fw = " ".join(RPI_FW_BOOTARGS.split())
    envp = _rpi_env(ns)
    envp["mender_post_setup_commands"] = "setenv bootargs ${bootargs} panic=7; "
    bad = ns["otapulse_full_boot_script_problems"](_script_image(RPI_BOOT_CMD), envp, fw)
    ok = ns["otapulse_full_boot_script_problems"](_script_image(RPI_BOOT_CMD), envp, fw, 64)
    assert bad and not ok


def test_env_check_wrapper_reads_maxargs_from_the_build_dir(ns, tmp_path):
    env = RPI_ENV
    env = re.sub(r"^mender_post_setup_commands=.*$", "mender_post_setup_commands=setenv bootargs ${bootargs} panic=7; ",
                 env, flags=re.M)
    (tmp_path / "u-boot-initial-env").write_text(env)
    (tmp_path / ".config").write_text("CONFIG_SYS_MAXARGS=64\n")
    ns["otapulse_full_check_uboot_env"](FakeD(B=str(tmp_path), OTAPULSE_PANIC_TIMEOUT_S="7"))   # must not raise
    assert any("boots through a boot script" in n for n in ns["_bb"].notes)


def test_the_shipped_firmware_bootargs_carry_no_board_specific_mac(ns):
    fw = ns["otapulse_full_rpi_firmware_bootargs"]()
    assert "DC:A6:32" not in fw.upper() and "smsc95xx.macaddr=00:00:00:00:00:00" in fw
    assert len(fw.split()) == 12
    assert "DC:A6:32" not in BL.upper()

