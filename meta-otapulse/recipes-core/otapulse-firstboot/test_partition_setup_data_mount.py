#!/usr/bin/env python3
"""BUG-319: otapulse-partition-setup must leave /data really mounted on the
first boot that creates the data partition, or stay unmarked so it re-runs.

Runs the REAL script (files/otapulse-partition-setup) through a fresh-disk
first boot: a GPT disk holding only boot + rootfs_a, with free space for
rootfs_b + data (the qemux86-64 per-run qcow2 overlay, or any board flashed
from a minimal wic). Every system path is rewritten into a temp dir and every
disk/mount/systemd tool is a stub. PATH holds ONLY the stubs plus a fixed list
of harmless coreutils, so a tool the harness forgot to stub fails with
"command not found" instead of touching a real disk. The fake root device is
/dev/vdz2, which does not exist.

The failure being modelled was seen live on qemux86-64 (BUG-319 handoff round
3): fstab's data.mount fired on the freshly created, still unformatted "data"
partition and failed, and the script's own mount did not leave /data mounted
either, yet the script had already written its "partitions created" marker,
so it never tried again. The stub mount can be told that its first N mounts
"do not stick" (mount exits 0 but /data is not a mountpoint afterwards).

Run: python3 -m pytest test_partition_setup_data_mount.py
"""
from __future__ import annotations

import os
import re
import subprocess
from pathlib import Path

import pytest

HERE = Path(__file__).resolve().parent
SCRIPT = HERE / "files" / "otapulse-partition-setup"

# Real binaries the script may use that cannot touch a disk.
_SAFE_TOOLS = (
    "awk basename cat chmod cp cut date dirname grep head ln ls mkdir mv "
    "readlink rm sed seq sort tail touch tr wc"
).split()

_STUBS = {
    "findmnt": "#!/bin/sh\necho /dev/vdz2\n",
    "blockdev": (
        "#!/bin/sh\n"
        'case "$*" in\n'
        "  *--getsz*) echo 33554432 ;;\n"
        "  *--getss*) echo 512 ;;\n"
        "  *--getsize64*) echo 17179869184 ;;\n"
        "esac\n"
    ),
    "blkid": '#!/bin/sh\ncase "$*" in *PTTYPE*) echo gpt ;; esac\n',
    # Golden layout: p1 boot, p2 rootfs_a (already named, as on the real
    # qemux86-64 wic). `-n` (create) makes the new partlabels appear, which
    # is what udev does once partprobe tells the kernel.
    "sgdisk": (
        "#!/bin/sh\n"
        'echo "$*" >> "$FAKE_ROOT/sgdisk.log"\n'
        'case "$*" in\n'
        '  *-E*) echo 33554398 ;;\n'
        '  "-p "*)\n'
        '    echo "Number  Start (sector)    End (sector)  Size       Code  Name"\n'
        '    echo "   1            2048          133119   64.0 MiB    0700  boot"\n'
        '    echo "   2          133120         9675159   4.5 GiB     8300  rootfs_a" ;;\n'
        '  "-i 2 "*)\n'
        "    echo \"Partition name: 'rootfs_a'\"\n"
        '    echo "Partition size: 9542040 sectors (4.5 GiB)" ;;\n'
        '  "-i 1 "*) echo "Partition name: \'boot\'" ;;\n'
        "  *-n*)\n"
        '    touch "$FAKE_ROOT/dev/vdz3" "$FAKE_ROOT/dev/vdz4"\n'
        '    ln -sf "$FAKE_ROOT/dev/vdz3" "$FAKE_ROOT/bypl/rootfs_b"\n'
        '    ln -sf "$FAKE_ROOT/dev/vdz4" "$FAKE_ROOT/bypl/data" ;;\n'
        "esac\n"
        "exit 0\n"
    ),
    # lsblk -no FSTYPE <dev>: what mkfs.ext4 recorded for that device or label.
    "lsblk": (
        "#!/bin/sh\n"
        'case "$*" in\n'
        '  *"-no FSTYPE"*)\n'
        '    for a; do last="$a"; done\n'
        '    cat "$FAKE_ROOT/fstype/$(basename "$last")" 2>/dev/null ;;\n'
        '  *-lnpo*) echo "/dev/vdz1 vfat" ;;\n'
        "esac\n"
        "exit 0\n"
    ),
    "mkfs.ext4": (
        "#!/bin/sh\n"
        'for a; do last="$a"; done\n'
        'label=""; prev=""\n'
        'for a; do [ "$prev" = "-L" ] && label="$a"; prev="$a"; done\n'
        'echo ext4 > "$FAKE_ROOT/fstype/$(basename "$last")"\n'
        '[ -n "$label" ] && echo ext4 > "$FAKE_ROOT/fstype/$label"\n'
        "exit 0\n"
    ),
    # FAKE_MOUNT_LOST=N: the first N mounts exit 0 but do not stick (/data is
    # not a mountpoint afterwards), as when systemd drops a mount whose device
    # unit it has not seen yet.
    "mount": (
        "#!/bin/sh\n"
        'echo "$*" >> "$FAKE_ROOT/mount.log"\n'
        'n=$(wc -l < "$FAKE_ROOT/mount.log")\n'
        'if [ "$n" -gt "${FAKE_MOUNT_LOST:-0}" ]; then touch "$FAKE_ROOT/data_mounted"; fi\n'
        "exit 0\n"
    ),
    "mountpoint": '#!/bin/sh\n[ -e "$FAKE_ROOT/data_mounted" ]\n',
    "systemctl": '#!/bin/sh\necho "$*" >> "$FAKE_ROOT/systemctl.log"\nexit 0\n',
    "udevadm": '#!/bin/sh\necho "$*" >> "$FAKE_ROOT/udevadm.log"\nexit 0\n',
    "partprobe": "#!/bin/sh\nexit 0\n",
    "partx": "#!/bin/sh\nexit 0\n",
    "sleep": "#!/bin/sh\nexit 0\n",
    "logger": "#!/bin/sh\nexit 0\n",
}


class FreshDisk:
    """A fake first-boot disk + the script rewritten to use it."""

    def __init__(self, root: Path):
        self.root = root
        for d in ("bin", "bypl", "dev", "fstype", "data", "etc/ssh",
                  "etc/otapulse", "usr/sbin", "var/lib/otapulse"):
            (root / d).mkdir(parents=True, exist_ok=True)
        for n in (1, 2):
            (root / f"dev/vdz{n}").write_text("")
        (root / "bypl/boot").symlink_to(root / "dev/vdz1")
        (root / "bypl/rootfs_a").symlink_to(root / "dev/vdz2")
        (root / "etc/otapulse/device_type").write_text("device_type=qemux86-64\n")
        (root / "mount.log").write_text("")

        bindir = root / "bin"
        for name, body in _STUBS.items():
            p = bindir / name
            p.write_text(body)
            p.chmod(0o755)
        for tool in _SAFE_TOOLS:
            (bindir / tool).symlink_to(_which(tool))
        self.path = str(bindir)

        text = SCRIPT.read_text()
        # /data as a path token only (not by-partlabel/data, otapulse-data).
        text = re.sub(r"(?<![\w/.-])/data(?=[/\s\"';)]|$)", f"{root}/data", text,
                      flags=re.M)
        for old, new in (
            ("/dev/disk/by-partlabel/", f"{root}/bypl/"),
            ("/var/lib/otapulse", f"{root}/var/lib/otapulse"),
            ("/var/local/otapulse-data", f"{root}/varlocal"),
            ("/etc/ssh/", f"{root}/etc/ssh/"),
            ("/etc/otapulse/", f"{root}/etc/otapulse/"),
            ("/etc/mender/", f"{root}/etc/mender/"),
            ("/etc/fstab", f"{root}/etc/fstab"),
            ("/proc/device-tree/", f"{root}/dt/"),
            ("/usr/sbin/", f"{root}/usr/sbin/"),
            ("/sys/class/block", f"{root}/sys"),
        ):
            text = text.replace(old, new)
        self.script = root / "otapulse-partition-setup"
        self.script.write_text(text)
        self.script.chmod(0o755)

    @property
    def marker(self) -> Path:
        return self.root / "var/lib/otapulse/.partitions-created"

    @property
    def mounted(self) -> bool:
        return (self.root / "data_mounted").exists()

    def mounts(self) -> list[str]:
        return [l for l in (self.root / "mount.log").read_text().splitlines() if l]

    def log(self, name: str) -> str:
        p = self.root / name
        return p.read_text() if p.exists() else ""

    def run(self, mount_lost: int = 0) -> subprocess.CompletedProcess:
        env = {"PATH": self.path, "FAKE_ROOT": str(self.root),
               "FAKE_MOUNT_LOST": str(mount_lost), "LANG": "C"}
        return subprocess.run(["/bin/bash", str(self.script)], env=env,
                              capture_output=True, text=True, timeout=60)


def _which(name: str) -> str:
    for d in ("/usr/bin", "/bin"):
        p = os.path.join(d, name)
        if os.path.exists(p):
            return p
    raise RuntimeError(f"{name} not found on host")


@pytest.fixture
def disk(tmp_path: Path) -> FreshDisk:
    return FreshDisk(tmp_path)


def _out(r: subprocess.CompletedProcess) -> str:
    return f"rc={r.returncode}\nSTDOUT:\n{r.stdout}\nSTDERR:\n{r.stderr}"


def test_harness_never_reaches_a_real_disk(disk):
    """Sanity: the fake root device is not a real block device."""
    assert not os.path.exists("/dev/vdz2")
    assert not os.path.exists("/dev/vdz")


def test_clean_first_boot_creates_partitions_and_mounts_data(disk):
    r = disk.run()
    assert r.returncode == 0, _out(r)
    assert "-n 3:" in disk.log("sgdisk.log") and "-c 4:data" in disk.log("sgdisk.log"), _out(r)
    assert disk.mounted, _out(r)
    assert disk.marker.exists(), _out(r)
    ota = disk.root / "data/ota"
    assert (ota / "current_slot").read_text().strip() == "a"
    assert (ota / "mender_boot_part").read_text().strip() == "2"


def test_first_mount_that_does_not_stick_is_retried(disk):
    """BUG-319 round 3: the first mount is lost; /data must still end mounted."""
    r = disk.run(mount_lost=1)
    assert r.returncode == 0, _out(r)
    assert disk.mounted, "script finished with /data NOT mounted\n" + _out(r)
    assert len(disk.mounts()) == 2, _out(r)
    assert "reset-failed data.mount" in disk.log("systemctl.log"), _out(r)
    assert "settle" in disk.log("udevadm.log"), _out(r)
    assert disk.marker.exists(), _out(r)


def test_data_never_mounting_fails_and_leaves_setup_unmarked(disk):
    """If /data cannot be mounted, the run must fail and NOT write the marker,
    so Restart=on-failure (and the next boot) runs setup again."""
    r = disk.run(mount_lost=99)
    assert r.returncode != 0, "script exited 0 without /data mounted\n" + _out(r)
    assert not disk.mounted
    assert not disk.marker.exists(), "marker written although /data never mounted\n" + _out(r)


def test_rerun_after_failed_mount_recovers_through_existing_partition_path(disk):
    """The systemd retry finds rootfs_b + data already created and formatted,
    takes the pre-existing-partitions path, mounts /data and seeds /data/ota."""
    first = disk.run(mount_lost=99)
    assert first.returncode != 0, _out(first)
    (disk.root / "mount.log").write_text("")
    r = disk.run()
    assert r.returncode == 0, _out(r)
    assert "already exist and are formatted" in r.stdout, _out(r)
    assert disk.mounted, _out(r)
    assert disk.marker.exists(), _out(r)
    assert (disk.root / "data/ota/current_slot").read_text().strip() == "a"
    # Partitions were created exactly once.
    assert disk.log("sgdisk.log").count("-n 3:") == 1, disk.log("sgdisk.log")
