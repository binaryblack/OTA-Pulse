"""Static and behavioural tests for the OTA-Pulse Debian packages (TASK-S109-024).

Run:  python3 -m pytest -q ota-pulse/packaging/debian/test_debian_packaging.py

Two groups:
  * source tests: read packaging/debian/ and scripts/build-debs.sh, no build needed;
  * built-package tests: inspect the .deb files in $OTAPULSE_DEBS_DIR (default <ota-pulse>/build/debs);
    skipped when no packages were built. They need dpkg-deb.
"""

from __future__ import annotations

import json
import os
import re
import shutil
import struct
import subprocess
import tempfile
from pathlib import Path

import pytest

PKG = Path(__file__).resolve().parent
DEB = PKG / "debian"
OTAPULSE = PKG.parent.parent
BUILD_SCRIPT = OTAPULSE / "scripts" / "build-debs.sh"
RECIPE_FILES = OTAPULSE / "meta-otapulse" / "recipes-core" / "soc-ota-agent" / "files"
DEBS_DIR = Path(os.environ.get("OTAPULSE_DEBS_DIR", OTAPULSE / "build" / "debs"))

# Customer-facing text: nothing here may name the upstream project. Attribution lives only in
# NOTICE and in the copyright file, which are deliberately not in this list.
CUSTOMER_FACING = [
    DEB / "control",
    DEB / "soc-ota-agent.service",
    DEB / "otapulse-data-seed.service",
    DEB / "otapulse.conf",
    DEB / "boot_mode",
    DEB / "soc-ota-agent.postinst",
    DEB / "soc-ota-agent.postrm",
    DEB / "socmond.postinst",
    DEB / "socmond.postrm",
    DEB / "soc-ota-agent.tmpfiles",
    DEB / "socmond.tmpfiles",
    DEB / "socmond.sysctl",
    DEB / "soc-ota-agent.lintian-overrides",
    DEB / "socmond.lintian-overrides",
] + sorted((DEB / "man").glob("*.1"))

SECRET_PATTERNS = [
    re.compile(r"\bsmk_[A-Za-z0-9]"),
    re.compile(r"\bspt_[A-Za-z0-9]"),
    re.compile(r"\bsotc_[A-Za-z0-9]"),
    re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----"),
    re.compile(r"\"(?:SoCAPIKey|api_key|provisioning_token|TenantToken|project_key)\"\s*:\s*\"[^\"$][^\"]*\""),
]


def read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def control_stanzas() -> dict[str, dict[str, str]]:
    stanzas: dict[str, dict[str, str]] = {}
    for block in read(DEB / "control").strip().split("\n\n"):
        fields: dict[str, str] = {}
        key = None
        for line in block.splitlines():
            if line.startswith((" ", "\t")):
                assert key is not None
                fields[key] += "\n" + line
            else:
                key, _, value = line.partition(":")
                fields[key] = value.strip()
        name = fields.get("Package") or "Source:" + fields["Source"]
        stanzas[name] = fields
    return stanzas


# ----------------------------------------------------------------------------- source tree


def test_control_packages_and_architectures():
    st = control_stanzas()
    assert set(st) == {"Source:otapulse-agent", "soc-ota-agent", "socmond"}
    assert st["soc-ota-agent"]["Architecture"].split() == ["arm64", "armhf", "amd64"]
    # socmond is shell only: one architecture independent package, byte-identical for every arch.
    assert st["socmond"]["Architecture"] == "all"
    assert st["Source:otapulse-agent"]["Rules-Requires-Root"] == "no"
    assert "debhelper-compat (= 13)" in st["Source:otapulse-agent"]["Build-Depends"]


def test_control_dependencies():
    st = control_stanzas()
    agent = st["soc-ota-agent"]["Depends"]
    for dep in ("${shlibs:Depends}", "${misc:Depends}", "ca-certificates", "curl", "jq", "systemd"):
        assert dep in agent
    assert "libubootenv-tool" in st["soc-ota-agent"]["Recommends"]
    # Essential packages must not be listed (lintian error depends-on-essential-package-without-using-version).
    for pkg in ("soc-ota-agent", "socmond"):
        deps = {d.strip().split(" ")[0] for d in st[pkg]["Depends"].replace("\n", " ").split(",")}
        assert not deps & {"coreutils", "gzip", "util-linux", "bash", "dash", "sed", "grep", "tar"}


def test_no_client_package_is_installed_or_required():
    text = read(DEB / "control").lower()
    assert "mender" not in text


def test_customer_facing_files_name_no_upstream_project():
    offenders = [str(p.relative_to(PKG)) for p in CUSTOMER_FACING if re.search("mender", read(p), re.I)]
    assert offenders == []


def test_config_template_is_bootloader_mode_and_secret_free():
    conf = json.loads(read(DEB / "otapulse.conf"))
    assert conf["BootEnvMode"] == "bootloader"
    # The agent rejects bootloader mode combined with the file based boot environment.
    assert conf["UseFileBasedBootEnv"] is False
    assert conf["SkipVerify"] is False
    assert conf["TenantToken"] == ""
    assert conf["RequireArtifactVerification"] is True
    assert conf["ArtifactVerifyKeys"]
    assert "AllowUnsignedArtifacts" not in conf
    # Per-image values are left blank/placeholder on purpose (set by the image build or the operator).
    assert conf["RootfsPartA"] == "" and conf["RootfsPartB"] == ""
    assert conf["ServerURL"].startswith("https://") and "example" in conf["ServerURL"]


def test_boot_mode_marker_is_exactly_bootloader():
    # The state scripts treat anything but the word "bootloader" as native mode (and would then
    # rewrite native boot media); the package must therefore ship exactly that word.
    assert read(DEB / "boot_mode") == "bootloader\n"


@pytest.mark.parametrize("path", [p for p in CUSTOMER_FACING + [DEB / "copyright"] if p.exists()], ids=lambda p: p.name)
def test_no_secrets_in_packaged_text(path):
    text = read(path)
    for pat in SECRET_PATTERNS:
        assert not pat.search(text), f"{path.name} matches secret pattern {pat.pattern}"


def test_socmond_default_config_has_no_credentials():
    conf = json.loads(read(RECIPE_FILES.parent.parent / "socmond" / "files" / "config.json"))
    assert conf["auth"]["api_key"] == "" and conf["auth"]["provisioning_token"] == ""
    assert conf["auth"]["project_key"] == ""


def test_socmond_package_does_not_point_at_the_development_backend():
    rules = read(DEB / "rules")
    assert "https://ota.example.com" in rules and "192.168.0.123" in rules  # rewrite, guarded by grep -q
    for p in CUSTOMER_FACING:
        assert "192.168.0." not in read(p), p


def test_agent_unit_invariants():
    unit = read(DEB / "soc-ota-agent.service")
    assert "ExecStart=/usr/bin/soc-ota-agent daemon" in unit
    assert "LimitCORE=infinity" in unit and "GOTRACEBACK=crash" in unit
    assert "ProtectSystem=false" in unit  # the agent writes /boot and the boot environment
    assert "RequiresMountsFor=/data" in unit
    assert "OTAPULSE_DATASTORE_DIR=/data/otapulse" in unit
    assert "WatchdogSec=0" in unit
    assert "WantedBy=multi-user.target" in unit
    # No dependency on units that only exist in the native image layout.
    assert "otapulse-partition-setup" not in unit
    # The device type check of the data seed gates the agent (fail closed), as on the bootloader images.
    assert "Requires=otapulse-data-seed.service" in unit and "After=otapulse-data-seed.service" in unit
    # A missing ReadWritePaths= target would fail the whole namespace setup (status 226).
    for line in unit.splitlines():
        if line.startswith("ReadWritePaths="):
            assert line.startswith("ReadWritePaths=-"), line


def test_agent_unit_keeps_the_recipe_hardening_and_ordering():
    recipe = read(RECIPE_FILES / "soc-ota-agent.service")
    unit = read(DEB / "soc-ota-agent.service")
    for needle in ("LimitCORE=infinity", "ProtectSystem=false", "ProtectHome=yes", "PrivateTmp=yes",
                   "NoNewPrivileges=no", "Restart=on-failure", "RestartSec=30", "WatchdogSec=0",
                   "time-sync.target"):
        assert needle in recipe, f"recipe unit lost {needle}: re-review debian/soc-ota-agent.service"
        assert needle in unit


def test_maintainer_scripts_use_debhelper_and_are_posix():
    for name in ("soc-ota-agent.postinst", "soc-ota-agent.postrm", "socmond.postinst", "socmond.postrm"):
        text = read(DEB / name)
        assert text.startswith("#!/bin/sh\n")
        assert "#DEBHELPER#" in text
        assert "set -e" in text
        subprocess.run(["sh", "-n", str(DEB / name)], check=True)
    # Device identity and credentials on /data are never removed by the package, not even on purge.
    code = "\n".join(l for l in read(DEB / "soc-ota-agent.postrm").splitlines() if not l.lstrip().startswith("#"))
    assert "rm " not in code and "/data" not in code


def test_rules_build_flags_are_reproducible():
    rules = read(DEB / "rules")
    for needle in ("-trimpath", "-buildvcs=false", "-mod=vendor", "GOPROXY := off", "GOTOOLCHAIN := local",
                   "-buildmode=pie", "-linkmode=external", "-s -w", "hardening=+all"):
        assert needle in rules, needle
    assert "agent/vendor" in rules
    assert "$(DEB_HOST_GNU_TYPE)-gcc" in rules and "$(DEB_HOST_GNU_TYPE)-pkg-config" in rules
    # All three architectures have an explicit GOARCH mapping, armhf is ARMv7.
    for needle in ("GOARCH := amd64", "GOARCH := arm64", "GOARCH := arm", "GOARM := 7"):
        assert needle in rules


def test_rules_ships_the_three_state_scripts_and_not_a_client():
    rules = read(DEB / "rules")
    for s in ("ArtifactReboot_Enter_01", "ArtifactRollbackReboot_Enter_01", "ArtifactCommit_Enter_01"):
        assert s in rules
    # The only mention of the compat client name is the build-time rewrite of the inventory helper.
    assert "install -D -m 0755 otapulse $(CURDIR)/debian/soc-ota-agent/usr/bin/soc-ota-agent" in rules
    assert "usr/bin/mender\n" not in rules and "/usr/bin/mender " not in rules.replace("/usr/bin/mender show-provides", "").replace("'/usr/bin/mender'", "")


def test_state_scripts_in_the_recipe_carry_the_boot_mode_gate():
    for s in ("ArtifactReboot_Enter_01", "ArtifactRollbackReboot_Enter_01", "ArtifactCommit_Enter_01"):
        text = read(RECIPE_FILES / s)
        assert "OTAPULSE-BOOT-MODE-GATE BEGIN" in text, s
    # The gate reads this exact file, written by the package.
    assert "/etc/otapulse/boot_mode" in read(RECIPE_FILES / "ArtifactReboot_Enter_01")


def test_state_scripts_exit_zero_under_dash_in_bootloader_mode(tmp_path):
    """Debian's /bin/sh is dash: the gate must work there, and must run before any side effect."""
    dash = shutil.which("dash")
    if not dash:
        pytest.skip("dash not installed")
    for s in ("ArtifactReboot_Enter_01", "ArtifactRollbackReboot_Enter_01", "ArtifactCommit_Enter_01"):
        script = read(RECIPE_FILES / s)
        script = script.replace("/etc/otapulse/boot_mode", str(tmp_path / "boot_mode"))
        (tmp_path / "boot_mode").write_text("bootloader\n")
        f = tmp_path / s
        f.write_text(script)
        r = subprocess.run([dash, str(f)], capture_output=True, text=True, timeout=30, cwd=tmp_path)
        assert r.returncode == 0, (s, r.stderr)
        assert "boot_mode=bootloader" in r.stderr


def test_builder_is_pinned():
    env = dict(
        line.split("=", 1) for line in read(PKG / "builder.env").splitlines() if line and not line.startswith("#")
    )
    assert re.fullmatch(r"golang@sha256:[0-9a-f]{64}", env["BUILDER_BASE_IMAGE"])
    assert re.fullmatch(r"\d{8}T\d{6}Z", env["DEBIAN_SNAPSHOT"])
    docker = read(PKG / "Dockerfile.builder")
    assert "snapshot.debian.org/archive/debian/${DEBIAN_SNAPSHOT}" in docker
    assert "snapshot.debian.org/archive/debian-security/${DEBIAN_SNAPSHOT}" in docker
    assert "deb.debian.org" not in docker
    assert not docker.lstrip().startswith("# syntax=")  # no unpinned Dockerfile frontend
    for tool in ("crossbuild-essential-arm64", "crossbuild-essential-armhf", "lintian", "debhelper"):
        assert tool in docker


def test_build_script_discipline():
    sh = read(BUILD_SCRIPT)
    assert sh.startswith("#!/usr/bin/env bash")
    assert "set -euo pipefail" in sh
    subprocess.run(["bash", "-n", str(BUILD_SCRIPT)], check=True)
    for needle in (
        "--network none",          # the package build itself is offline
        "SOURCE_DATE_EPOCH",
        "git -C \"$ROOT_DIR\" archive",  # inputs from one git revision, not the working tree
        "vendor.lock",             # vendor tree pinned by hash
        "OTAPULSE-BOOT-MODE-GATE BEGIN",  # refuses to package ungated state scripts
        "--repro",
        "SHA256SUMS",
        "lintian",
    ):
        assert needle in sh, needle
    assert "bookworm" in sh


def test_build_script_help_runs():
    r = subprocess.run(["bash", str(BUILD_SCRIPT), "--help"], capture_output=True, text=True, timeout=30)
    assert r.returncode == 0 and "--arch" in r.stdout


def test_build_script_rejects_bad_arch():
    r = subprocess.run(["bash", str(BUILD_SCRIPT), "--arch", "mips"], capture_output=True, text=True, timeout=30)
    assert r.returncode != 0 and "unsupported architecture" in r.stderr


def test_vendor_lock_entries_are_well_formed():
    lock = PKG / "vendor.lock"
    assert lock.exists()
    for line in read(lock).splitlines():
        assert re.fullmatch(r"[0-9a-f]{64} [0-9a-f]{64}", line), line


def test_man_pages_exist_for_every_binary():
    expected = {"soc-ota-agent", "otapulse", "soc-ota-provision", "socmond", "socmond-watchdog",
                "socmond-core-handler", "soc-ctl", "soc-deregister"}
    assert {p.stem for p in (DEB / "man").glob("*.1")} == expected


# ------------------------------------------------------------ postinst pre-seeding behaviour


def run_postinst(tmp_path: Path, env_extra: dict[str, str], args=("configure", "")):
    """Run the real soc-ota-agent postinst with /etc and /var/lib redirected into tmp_path."""
    etc = tmp_path / "etc" / "otapulse"
    var = tmp_path / "var" / "lib" / "otapulse"
    etc.mkdir(parents=True)
    var.mkdir(parents=True)
    shutil.copy(DEB / "otapulse.conf", etc / "otapulse.conf")
    script = read(DEB / "soc-ota-agent.postinst")
    script = script.replace("/etc/otapulse", str(etc)).replace("/var/lib/otapulse", str(var))
    f = tmp_path / "postinst"
    f.write_text(script)
    env = {"PATH": os.environ["PATH"], **env_extra}
    r = subprocess.run(["sh", str(f), *args], capture_output=True, text=True, env=env, timeout=30)
    return r, etc, var


def test_postinst_preseeds_first_install_only(tmp_path):
    r, etc, var = run_postinst(
        tmp_path,
        {
            "OTAPULSE_SERVER_URL": "http://192.168.0.123:8000",
            "OTAPULSE_ROOTFS_PART_A": "/dev/mmcblk0p2",
            "OTAPULSE_ROOTFS_PART_B": "/dev/mmcblk0p3",
            "OTAPULSE_DEVICE_TYPE": "raspberrypi4-64",
        },
    )
    assert r.returncode == 0, r.stderr
    conf = json.loads((etc / "otapulse.conf").read_text())
    assert conf["ServerURL"] == "http://192.168.0.123:8000"
    assert conf["RootfsPartA"] == "/dev/mmcblk0p2" and conf["RootfsPartB"] == "/dev/mmcblk0p3"
    assert conf["BootEnvMode"] == "bootloader" and conf["UseFileBasedBootEnv"] is False
    assert (etc / "device_type").read_text() == "device_type=raspberrypi4-64\n"
    assert (var / "device_type").read_text() == "device_type=raspberrypi4-64\n"


def test_postinst_does_not_touch_config_on_upgrade(tmp_path):
    r, etc, _ = run_postinst(tmp_path, {"OTAPULSE_SERVER_URL": "http://10.0.0.1"}, args=("configure", "0.1.6"))
    assert r.returncode == 0
    assert json.loads((etc / "otapulse.conf").read_text())["ServerURL"] == "https://ota.example.com"


@pytest.mark.parametrize("bad", ['http://x"y', "http://x|y", "http://x&y", "http://x\\y", "a b", "x\ny"])
def test_postinst_rejects_unsafe_values(tmp_path, bad):
    r, etc, _ = run_postinst(tmp_path, {"OTAPULSE_SERVER_URL": bad})
    assert r.returncode == 0
    assert "not allowed" in r.stderr
    conf = json.loads((etc / "otapulse.conf").read_text())  # still valid JSON, unchanged
    assert conf["ServerURL"] == "https://ota.example.com"


def test_postinst_never_reads_credentials_from_the_environment(tmp_path):
    r, etc, _ = run_postinst(tmp_path, {"OTAPULSE_TENANT_TOKEN": "smk_should_not_be_used"})
    assert r.returncode == 0
    assert "smk_" not in (etc / "otapulse.conf").read_text()


def test_socmond_postinst_preseeds_server_url_on_first_install_only(tmp_path):
    etc = tmp_path / "etc" / "socmond"
    etc.mkdir(parents=True)
    conf = etc / "config.json"
    conf.write_text('{\n  "server": {\n    "base_url": "https://ota.example.com",\n    "api_version": "v1"\n  }\n}\n')
    script = read(DEB / "socmond.postinst").replace("/etc/socmond", str(etc))
    f = tmp_path / "postinst"
    f.write_text(script)

    def run(url, old):
        env = {"PATH": os.environ["PATH"], "OTAPULSE_SERVER_URL": url}
        return subprocess.run(["sh", str(f), "configure", old], capture_output=True, text=True, env=env, timeout=30)

    assert run("http://10.1.2.3:8000", "0.1.6").returncode == 0  # upgrade: untouched
    assert json.loads(conf.read_text())["server"]["base_url"] == "https://ota.example.com"
    r = run("http://x|y&z", "")  # unsafe: ignored, file still valid JSON
    assert r.returncode == 0 and "not allowed" in r.stderr
    assert json.loads(conf.read_text())["server"]["base_url"] == "https://ota.example.com"
    assert run("http://10.1.2.3:8000", "").returncode == 0  # first install: applied
    assert json.loads(conf.read_text())["server"]["base_url"] == "http://10.1.2.3:8000"


# ------------------------------------------------------------------------- built packages


def debs(pattern: str) -> list[Path]:
    return sorted(DEBS_DIR.glob(pattern)) if DEBS_DIR.is_dir() else []


def dpkg_deb(*args: str) -> str:
    if not shutil.which("dpkg-deb"):
        pytest.skip("dpkg-deb not installed")
    return subprocess.run(["dpkg-deb", *args], capture_output=True, text=True, check=True).stdout


AGENT_DEBS = debs("soc-ota-agent_*.deb")
SOCMOND_DEBS = debs("socmond_*.deb")
needs_debs = pytest.mark.skipif(not AGENT_DEBS, reason=f"no packages in {DEBS_DIR} (run scripts/build-debs.sh)")

ELF_MACHINE = {"amd64": 62, "arm64": 183, "armhf": 40}


@needs_debs
@pytest.mark.parametrize("deb", AGENT_DEBS, ids=lambda p: p.name)
def test_built_agent_package(deb, tmp_path):
    arch = deb.name.rsplit("_", 1)[1][: -len(".deb")]
    assert arch in ELF_MACHINE
    assert dpkg_deb("-f", str(deb), "Architecture").strip() == arch
    listing = dpkg_deb("-c", str(deb))
    paths = {line.split()[-1] if "->" not in line else line.split()[-3] for line in listing.splitlines()}
    for must in ("./usr/bin/soc-ota-agent", "./usr/bin/soc-ota-provision", "./etc/otapulse/otapulse.conf",
                 "./etc/otapulse/boot_mode", "./lib/systemd/system/soc-ota-agent.service",
                 "./usr/share/otapulse/state-scripts/ArtifactReboot_Enter_01",
                 "./usr/share/otapulse/state-scripts/ArtifactRollbackReboot_Enter_01",
                 "./usr/share/otapulse/state-scripts/ArtifactCommit_Enter_01",
                 "./usr/share/doc/soc-ota-agent/NOTICE.gz", "./usr/share/doc/soc-ota-agent/copyright"):
        assert must in paths, must
    assert "./usr/bin/mender" not in paths
    ctl = tmp_path / "ctl"
    dpkg_deb("-e", str(deb), str(ctl))
    assert set((ctl / "conffiles").read_text().split()) == {"/etc/otapulse/boot_mode", "/etc/otapulse/otapulse.conf"}
    assert "deb-systemd-helper" in (ctl / "postinst").read_text()
    root = tmp_path / "root"
    dpkg_deb("-x", str(deb), str(root))
    # The binary is a stripped ELF of the right machine, without the build directory in it.
    data = (root / "usr/bin/soc-ota-agent").read_bytes()
    assert data[:4] == b"\x7fELF"
    assert struct.unpack_from("<H", data, 18)[0] == ELF_MACHINE[arch]
    assert b"/build/otapulse-agent" not in data and b"/tmp/go-build" not in data
    assert b"BootEnvMode" in data
    # Shipped config: bootloader mode, secret free; marker file exact; modes.
    conf = json.loads((root / "etc/otapulse/otapulse.conf").read_text())
    assert conf["BootEnvMode"] == "bootloader" and conf["UseFileBasedBootEnv"] is False and conf["TenantToken"] == ""
    assert (root / "etc/otapulse/boot_mode").read_text() == "bootloader\n"
    assert oct((root / "etc/otapulse/otapulse.conf").stat().st_mode & 0o777) == "0o600"
    for s in (root / "usr/share/otapulse/state-scripts").iterdir():
        assert "OTAPULSE-BOOT-MODE-GATE BEGIN" in s.read_text()
    prov = (root / "usr/bin/soc-ota-provision").read_text()
    assert prov.startswith("#!/bin/bash\n")
    # Customer facing text of the built package: no upstream name outside the attribution files.
    for rel in ("lib/systemd/system/soc-ota-agent.service", "etc/otapulse/otapulse.conf"):
        assert not re.search("mender", (root / rel).read_text(), re.I), rel
    control = dpkg_deb("-I", str(deb))
    assert not re.search("mender", control, re.I)
    for p in root.rglob("*"):
        if p.is_file() and not p.is_symlink() and p.stat().st_size < 200_000 and "third_party_licenses" not in p.parts:
            try:
                text = p.read_text()
            except UnicodeDecodeError:
                continue
            for pat in SECRET_PATTERNS:
                assert not pat.search(text), f"{p} matches {pat.pattern}"


@needs_debs
@pytest.mark.skipif(not SOCMOND_DEBS, reason="no socmond package")
@pytest.mark.parametrize("deb", SOCMOND_DEBS, ids=lambda p: p.name)
def test_built_socmond_package(deb, tmp_path):
    assert dpkg_deb("-f", str(deb), "Architecture").strip() == "all"
    ctl = tmp_path / "ctl"
    dpkg_deb("-e", str(deb), str(ctl))
    assert (ctl / "conffiles").read_text().split() == ["/etc/socmond/config.json"]
    root = tmp_path / "root"
    dpkg_deb("-x", str(deb), str(root))
    for rel in ("usr/bin/socmond", "usr/bin/socmond-watchdog", "usr/bin/socmond-core-handler", "usr/bin/soc-ctl",
                "usr/bin/soc-deregister", "lib/systemd/system/socmond.service",
                "lib/systemd/system/socmond-watchdog.service", "usr/lib/sysctl.d/90-socmond-coredump.conf",
                "usr/lib/tmpfiles.d/socmond.conf"):
        assert (root / rel).exists(), rel
    conf = json.loads((root / "etc/socmond/config.json").read_text())
    assert conf["auth"]["provisioning_token"] == "" and conf["auth"]["api_key"] == ""
    assert conf["server"]["base_url"] == "https://ota.example.com"
    assert oct((root / "etc/socmond/config.json").stat().st_mode & 0o777) == "0o600"
    assert "192.168." not in (root / "etc/socmond/config.json").read_text()
    for rel in ("usr/bin/soc-ctl", "usr/bin/soc-deregister"):
        assert (root / rel).read_text().startswith("#!/bin/bash\n")
    for rel in ("usr/bin/socmond", "usr/bin/socmond-watchdog", "usr/bin/socmond-core-handler"):
        assert (root / rel).read_text().startswith("#!/bin/sh\n")


@needs_debs
def test_sha256sums_match_the_packages():
    sums = DEBS_DIR / "SHA256SUMS"
    if not sums.exists():
        pytest.skip("no SHA256SUMS")
    with tempfile.TemporaryDirectory():
        r = subprocess.run(["sha256sum", "-c", "SHA256SUMS"], cwd=DEBS_DIR, capture_output=True, text=True)
        assert r.returncode == 0, r.stdout + r.stderr
