#!/usr/bin/env python3
"""TODO-157 / TASK-S109-011: otapulse-bl-guard, the userspace half of the bootloader
rollback net in bootloader compatibility mode.

Covers the three jobs of the script with the REAL script against fake tools:

* deadline     reboot when the bootloader environment still says upgrade_available=1
               after 20 minutes of uptime (wedged userspace);
* seed-failed  a failed otapulse-data-seed keeps the agent down; on an uncommitted
               update that must end in a reboot (and so a rollback), on a committed
               slot in a loud failure only;
* clock-wait   bounded NTP wait before the agent can commit (replaces the wait of the
               gated ArtifactCommit_Enter_01).

Nothing here touches the host: the script runs with a PATH of stubs plus a symlink
farm of the few real tools it needs (so a fw_printenv / grub-editenv / timedatectl
installed on the host can never leak in). `systemctl`, `reboot`, `sync`,
`mountpoint`, `logger` and `sleep` are stubs that only record, and the last-resort
sysrq write is pointed at a temp file. The U-Boot tool is a fake `fw_printenv`
reading a scripted sequence of answers (so "the agent committed in the meantime"
is expressible); the GRUB tool is a fake `grub-editenv` over plain-text env files.

Run: python3 -m pytest test_bl_guard.py
"""
from __future__ import annotations

import os
import re
import shutil
import stat
import subprocess
from pathlib import Path

import pytest

HERE = Path(__file__).resolve().parent
FILES = HERE / "files"
GUARD = FILES / "otapulse-bl-guard"
RECIPE = HERE / "otapulse-firstboot_1.0.bb"
UNITS = {
    n: FILES / n
    for n in (
        "otapulse-bl-boot-deadline.service",
        "otapulse-bl-boot-deadline.timer",
        "otapulse-bl-seed-failure.service",
        "otapulse-bl-clock-wait.service",
        "otapulse-data-seed.service",
    )
}

REAL_TOOLS = ("sed", "tr", "cut", "head", "cat", "rm", "mv", "mkdir", "timeout", "cp", "grep", "tail")

_STUBS = {
    "sync": "#!/bin/sh\nexit 0\n",
    "logger": '#!/bin/sh\necho "$*" >> "$FAKE_ROOT/logger.log"\n',
    "mountpoint": '#!/bin/sh\n[ "${FAKE_DATA_MOUNTED:-1}" = 1 ]\n',
    "sleep": '#!/bin/sh\necho "$1" >> "$FAKE_ROOT/sleeps"\n',
    "reboot": '#!/bin/sh\necho "reboot $*" >> "$FAKE_ROOT/actions"\nexit "${FAKE_REBOOT_RC:-0}"\n',
    "systemctl": (
        "#!/bin/sh\n"
        'echo "systemctl $*" >> "$FAKE_ROOT/actions"\n'
        'exit "${FAKE_SYSTEMCTL_RC:-0}"\n'
    ),
}

# fw_printenv: FAKE_UBOOT_UA is a space separated script of answers, one per
# "fw_printenv upgrade_available" call (the last one repeats): 0, 1, FAIL (rc 1),
# EMPTY (rc 0, nothing printed), GARBAGE (rc 0, upgrade_available=maybe).
_FW_PRINTENV = r"""#!/bin/sh
# One name per call.  FAKE_UBOOT_UA / FAKE_BOOTCOUNT / FAKE_SLOT are space separated
# scripts of answers indexed by the number of upgrade_available calls so far (the last one
# repeats).  An unset FAKE_BOOTCOUNT means the variable is undefined (rc 1), like a real
# fw_printenv for an undefined name.
pick_nth() { i=0; pick=""; for v in $1; do i=$((i + 1)); pick=$v; [ "$i" -ge "$2" ] && break; done; }
n=$(cat "$FAKE_ROOT/uboot.calls" 2>/dev/null || echo 0)
case "$1" in
  upgrade_available) n=$((n + 1)); echo "$n" > "$FAKE_ROOT/uboot.calls" ;;
  bootcount|mender_boot_part) ;;
  *) echo "unexpected: $*" >&2; exit 9 ;;
esac
case "$1" in
  bootcount) [ -n "${FAKE_BOOTCOUNT:-}" ] || { echo "## Error: \"bootcount\" not defined" >&2; exit 1; }
             pick_nth "$FAKE_BOOTCOUNT" "$n"; echo "bootcount=$pick"; exit 0 ;;
  mender_boot_part) pick_nth "${FAKE_SLOT:-3}" "$n"; [ "$pick" = NONE ] && pick=""
                    echo "mender_boot_part=$pick"; exit 0 ;;
esac
pick_nth "$FAKE_UBOOT_UA" "$n"
case "$pick" in
  FAIL) echo "Cannot read environment, using default" >&2; exit 1 ;;
  EMPTY) exit 0 ;;
  GARBAGE) echo "upgrade_available=maybe" ;;
  *) echo "upgrade_available=$pick" ;;
esac
"""

# grub-editenv <file> list: prints the file; a file holding CORRUPT fails (rc 1).
# FAKE_GRUB_SWITCH_AFTER=N: after N calls every "<env>.next" replaces its "<env>"
# (the agent committing between two reads).
_GRUB_EDITENV = r"""#!/bin/sh
n=$(cat "$FAKE_ROOT/grub.calls" 2>/dev/null || echo 0)
n=$((n + 1)); echo "$n" > "$FAKE_ROOT/grub.calls"
if [ -n "${FAKE_GRUB_SWITCH_AFTER:-}" ] && [ "$n" -gt "$FAKE_GRUB_SWITCH_AFTER" ]; then
  for nx in "$FAKE_ROOT"/grub/*/*/env.next; do
    [ -f "$nx" ] && cp "$nx" "${nx%.next}"
  done
fi
[ "$2" = list ] || exit 8
grep -q CORRUPT "$1" && exit 1
cat "$1"
"""

# timedatectl show -p NTPSynchronized --value: "no" until the FAKE_SYNC_AFTER-th poll.
_TIMEDATECTL = r"""#!/bin/sh
n=$(cat "$FAKE_ROOT/tdc.calls" 2>/dev/null || echo 0)
n=$((n + 1)); echo "$n" > "$FAKE_ROOT/tdc.calls"
if [ -n "${FAKE_SYNC_AFTER:-}" ] && [ "$n" -ge "$FAKE_SYNC_AFTER" ]; then echo yes; else echo no; fi
"""


DASH = [shutil.which("dash") or shutil.which("sh")]
BUSYBOX_ASH = [shutil.which("busybox"), "ash"] if shutil.which("busybox") else None


@pytest.fixture
def shell() -> list[str]:
    """The POSIX shell under test. The on-device /bin/sh is busybox ash or dash; dash is
    the strict reference. busybox ash prefers its built-in applets over the PATH stubs
    (sleep, sync, logger, reboot ...), so it only runs the decisions that never get as far
    as rebooting or sleeping (TestBusyboxAsh), never the rest."""
    return DASH


class Board:
    """A fake bootloader-mode device: env tools, /data, /run, uptime, clock."""

    def __init__(self, tmp: Path, shell: list[str]):
        self.shell = shell
        self.root = tmp
        self.stubs = tmp / "stubs"
        self.real = tmp / "real"
        self.data_otapulse = tmp / "data/otapulse"
        self.run_dir = tmp / "run/otapulse"
        self.uptime = tmp / "uptime"
        for d in (self.stubs, self.real, self.data_otapulse):
            d.mkdir(parents=True, exist_ok=True)
        for name, body in _STUBS.items():
            self._stub(name, body)
        for tool in REAL_TOOLS:
            src = shutil.which(tool)
            assert src, f"host lacks {tool}"
            (self.real / tool).symlink_to(src)
        self.set_uptime(2000)
        self.env = {
            "FAKE_ROOT": str(tmp),
            "OTAPULSE_BL_STATE_DIR": str(self.data_otapulse),
            "OTAPULSE_BL_RUN_DIR": str(self.run_dir),
            "OTAPULSE_BL_UPTIME_FILE": str(self.uptime),
            "OTAPULSE_BL_SYSRQ": str(tmp / "sysrq"),
            "OTAPULSE_BL_READ_DELAY_S": "0",
            "OTAPULSE_BL_GRUBENV_DIRS": str(tmp / "grub/efi"),
            "OTAPULSE_BL_BOOT_ID_FILE": str(tmp / "boot_id"),
        }
        self._boot_n = 0
        self._trial_slot: str | None = None
        self._new_boot_id()

    # -- construction helpers
    def _new_boot_id(self) -> None:
        self._boot_n += 1
        (self.root / "boot_id").write_text(f"0a1b2c3d-0000-4000-8000-{self._boot_n:012d}\n")

    def arm_trial(self, slot: str = "3", boot_id: str | None = None) -> None:
        """What otapulse-bl-clock-wait leaves in /run on a trial boot."""
        self._trial_slot = slot
        self.run_dir.mkdir(parents=True, exist_ok=True)
        bid = boot_id or (self.root / "boot_id").read_text().strip()
        (self.run_dir / "bl-trial-boot").write_text(f"boot_id={bid}\nslot={slot}\n")

    def _stub(self, name: str, body: str) -> None:
        p = self.stubs / name
        p.write_text(body)
        p.chmod(0o755)

    def set_uptime(self, seconds: int) -> None:
        self.uptime.write_text(f"{seconds}.42 {seconds * 3}.00\n")

    def uboot(self, *answers: str, bootcount: int | str | None = None, slot: str = "3",
              trial: bool = False) -> None:
        """Fake U-Boot environment. bootcount None = the variable is undefined (the safe
        default: nothing may reboot on it); trial=True = a trial boot in progress
        (bootcount 1, clock-wait marker for *slot*)."""
        self._stub("fw_printenv", _FW_PRINTENV)
        self.env["FAKE_UBOOT_UA"] = " ".join(answers)
        self.env["FAKE_SLOT"] = slot
        if trial and bootcount is None:
            bootcount = 1
        if bootcount is None:
            self.env.pop("FAKE_BOOTCOUNT", None)
        else:
            self.env["FAKE_BOOTCOUNT"] = str(bootcount)
        if trial:
            self.arm_trial(slot)

    def grub(self, ua1: str | None, ua2: str | None, edit1: str | None = None,
             edit2: str | None = None, bootcount: int | None = None, slot: str = "3",
             trial: bool = False) -> None:
        """Two env copies; ua None = CORRUPT, edit None = no editcount line, bootcount
        None = no bootcount line (undefined), trial=True = trial boot in progress."""
        self._stub("grub-editenv", _GRUB_EDITENV)
        if trial and bootcount is None:
            bootcount = 1
        if trial:
            self.arm_trial(slot)
        for i, (ua, edit) in enumerate(((ua1, edit1), (ua2, edit2)), start=1):
            d = self.root / "grub/efi" / f"mender_grubenv{i}"
            d.mkdir(parents=True, exist_ok=True)
            lines = ["# GRUB Environment Block"]
            if ua is None:
                lines.append("CORRUPT")
            else:
                if bootcount is not None:
                    lines.append(f"bootcount={bootcount}")
                lines += [f"mender_boot_part={slot}", f"upgrade_available={ua}"]
                if edit is not None:
                    lines.append(f"editcount={edit}")
            (d / "env").write_text("\n".join(lines) + "\n")

    def grub_next(self, ua: str) -> None:
        """What both copies hold after the (n-th) read: the agent committed."""
        for i in (1, 2):
            d = self.root / "grub/efi" / f"mender_grubenv{i}"
            (d / "env.next").write_text(
                f"bootcount=0\nmender_boot_part=3\nupgrade_available={ua}\n"
            )

    def grub_set_all(self, **kv: str) -> None:
        """Rewrite one variable in both copies (the agent / bootloader changing the env)."""
        for i in (1, 2):
            f = self.root / "grub/efi" / f"mender_grubenv{i}" / "env"
            rows = dict(l.split("=", 1) for l in f.read_text().splitlines() if "=" in l)
            rows.update(kv)
            f.write_text("# GRUB Environment Block\n" + "".join(f"{k}={v}\n" for k, v in rows.items()))

    def timedatectl(self) -> None:
        self._stub("timedatectl", _TIMEDATECTL)

    # -- observation
    @property
    def actions(self) -> list[str]:
        p = self.root / "actions"
        return p.read_text().splitlines() if p.exists() else []

    @property
    def rebooted(self) -> bool:
        return any(a.startswith("systemctl reboot") for a in self.actions)

    @property
    def counter(self) -> str | None:
        p = self.data_otapulse / "bl-guard-reboots"
        return p.read_text().strip() if p.exists() else None

    @property
    def syslog(self) -> str:
        p = self.root / "logger.log"
        return p.read_text() if p.exists() else ""

    @property
    def sleeps(self) -> list[str]:
        p = self.root / "sleeps"
        return p.read_text().splitlines() if p.exists() else []

    def run(self, cmd: str, **extra: str) -> subprocess.CompletedProcess:
        env = dict(self.env)
        env.update(extra)
        env["PATH"] = f"{self.stubs}:{self.real}"
        return subprocess.run(
            self.shell + [str(GUARD), cmd], env=env, capture_output=True, text=True, timeout=60
        )

    def new_boot(self) -> None:
        """A reboot happened: /run is wiped, uptime restarts, /data and the env persist."""
        shutil.rmtree(self.root / "run", ignore_errors=True)
        (self.root / "actions").unlink(missing_ok=True)
        for f in ("uboot.calls", "grub.calls"):
            (self.root / f).unlink(missing_ok=True)
        self.set_uptime(2000)
        self._new_boot_id()
        if self._trial_slot is not None:  # clock-wait of the new boot saw upgrade_available=1
            self.arm_trial(self._trial_slot)


@pytest.fixture
def board(tmp_path: Path, shell: list[str]) -> Board:
    return Board(tmp_path, shell)


def _loud(r: subprocess.CompletedProcess) -> bool:
    """Logged at journald error priority (<3>) on stderr."""
    return "<3>otapulse-bl-guard: ERROR:" in r.stderr


# ======================= deadline: the decision table ==================================

@pytest.mark.parametrize("kind", ["uboot", "grub"])
class TestDeadline:
    def _pending(self, board: Board, kind: str, ua: str = "1") -> None:
        if kind == "uboot":
            board.uboot(ua, trial=True)
        else:
            board.grub(ua, ua, trial=True)

    def test_committed_is_a_noop(self, board: Board, kind: str) -> None:
        self._pending(board, kind, "0")
        r = board.run("deadline")
        assert r.returncode == 0, r.stderr
        assert not board.rebooted and board.actions == []
        assert "no-op" in r.stdout

    def test_pending_below_the_deadline_is_a_noop(self, board: Board, kind: str) -> None:
        self._pending(board, kind)
        board.set_uptime(1199)
        r = board.run("deadline")
        assert r.returncode == 0, r.stderr
        assert board.actions == []
        assert "not yet" in r.stdout

    def test_pending_at_and_after_the_deadline_reboots(self, board: Board, kind: str) -> None:
        self._pending(board, kind)
        board.set_uptime(1200)  # boundary: >= deadline
        r = board.run("deadline")
        assert r.returncode == 0, r.stderr
        assert board.rebooted
        assert board.actions == ["systemctl reboot"]  # graceful first, nothing forced
        assert "rebooting so the bootloader can roll the update back" in r.stderr
        assert board.counter == "1"
        assert (board.run_dir / "bl-guard-rebooted").exists()

    def test_a_committed_agent_in_the_meantime_prevents_the_reboot(
        self, board: Board, kind: str
    ) -> None:
        """upgrade_available flips to 0 between the first read and the final check."""
        if kind == "uboot":
            board.uboot("1", "0", trial=True)
        else:
            board.grub("1", "1", trial=True)
            board.grub_next("0")
            board.env["FAKE_GRUB_SWITCH_AFTER"] = "2"  # both copies read once, then flip
        r = board.run("deadline")
        assert r.returncode == 0, r.stderr
        assert not board.rebooted and board.actions == []
        assert "committed in the meantime" in r.stdout
        assert board.counter is None

    def test_uptime_unreadable_does_not_reboot(self, board: Board, kind: str) -> None:
        self._pending(board, kind)
        board.uptime.write_text("garbage\n")
        r = board.run("deadline")
        assert r.returncode == 3 and _loud(r) and board.actions == []


class TestDeadlineUbootEnvironment:
    @pytest.mark.parametrize("answer", ["FAIL", "EMPTY", "GARBAGE"])
    def test_unreadable_or_odd_value_never_reboots_and_is_loud(
        self, board: Board, answer: str
    ) -> None:
        board.uboot(answer)
        r = board.run("deadline")
        assert r.returncode == 3
        assert board.actions == [] and board.counter is None
        assert _loud(r) and "NOT rebooting" in r.stderr
        # retried, not given up on the first transient failure
        assert (board.root / "uboot.calls").read_text().strip() == "3"

    def test_errors_also_reach_syslog(self, board: Board) -> None:
        board.uboot("FAIL")
        board.run("deadline")
        assert "-p user.err" in board.syslog and "NOT rebooting" in board.syslog

    def test_a_transient_failure_is_retried(self, board: Board) -> None:
        board.uboot("FAIL", "1", trial=True)
        r = board.run("deadline")
        assert r.returncode == 0, r.stderr
        assert board.rebooted

    def test_no_environment_tool_at_all(self, board: Board) -> None:
        r = board.run("deadline")
        assert r.returncode == 3 and _loud(r)
        assert "neither fw_printenv" in r.stderr
        assert board.actions == []

    def test_logs_the_bootcount_for_the_support_engineer(self, board: Board) -> None:
        board.uboot("1", bootcount=2, trial=True)
        r = board.run("deadline")
        assert "bootcount=2" in r.stderr


class TestDeadlineGrubEnvironment:
    def test_copies_agree(self, board: Board) -> None:
        board.grub("1", "1", trial=True)
        assert board.run("deadline").returncode == 0 and board.rebooted

    def test_one_corrupt_copy_the_other_decides(self, board: Board) -> None:
        board.grub(None, "1", trial=True)
        r = board.run("deadline")
        assert r.returncode == 0, r.stderr
        assert board.rebooted

    def test_one_corrupt_copy_committed(self, board: Board) -> None:
        board.grub("0", None)
        assert board.run("deadline").returncode == 0
        assert board.actions == []

    def test_disagree_unique_highest_editcount_wins_pending(self, board: Board) -> None:
        board.grub("1", "0", edit1="0000000b", edit2="0000000a", trial=True)
        assert board.run("deadline").returncode == 0 and board.rebooted

    def test_disagree_unique_highest_editcount_wins_committed(self, board: Board) -> None:
        board.grub("1", "0", edit1="0000000a", edit2="0000000b")
        r = board.run("deadline")
        assert r.returncode == 0 and board.actions == []

    def test_disagree_without_editcount_is_undecidable_and_never_reboots(
        self, board: Board
    ) -> None:
        board.grub("1", "0")
        r = board.run("deadline")
        assert r.returncode == 3 and _loud(r) and board.actions == []
        assert "disagree" in r.stderr

    def test_disagree_with_tied_editcount_is_undecidable(self, board: Board) -> None:
        board.grub("1", "0", edit1="0000000a", edit2="0000000a")
        r = board.run("deadline")
        assert r.returncode == 3 and board.actions == []

    def test_a_committing_agent_between_the_two_copy_writes_is_retried(
        self, board: Board
    ) -> None:
        """The agent writes copy 2 then copy 1; a read in between sees a disagreement.
        The retry sees the settled state."""
        board.grub("1", "0")
        board.grub_next("0")
        board.env["FAKE_GRUB_SWITCH_AFTER"] = "2"
        r = board.run("deadline")
        assert r.returncode == 0, r.stderr
        assert board.actions == []

    def test_both_copies_corrupt(self, board: Board) -> None:
        board.grub(None, None)
        r = board.run("deadline")
        assert r.returncode == 3 and _loud(r) and board.actions == []

    def test_boot_partition_not_mounted(self, board: Board) -> None:
        board.grub("1", "1", trial=True)
        shutil.rmtree(board.root / "grub/efi")
        r = board.run("deadline")
        assert r.returncode == 3 and "boot partition" in r.stderr and board.actions == []

    def test_uses_grub_editenv_read_only_never_the_self_healing_printer(
        self, board: Board
    ) -> None:
        board.grub("1", "1", trial=True)
        # a trap: if the script called grub-mender-grubenv-print / fw_setenv it would hit these
        board._stub("grub-mender-grubenv-print", "#!/bin/sh\necho TOUCHED >> \"$FAKE_ROOT/forbidden\"\n")
        board._stub("grub-mender-grubenv-set", "#!/bin/sh\necho TOUCHED >> \"$FAKE_ROOT/forbidden\"\n")
        board._stub("fw_setenv", "#!/bin/sh\necho TOUCHED >> \"$FAKE_ROOT/forbidden\"\n")
        board.run("deadline")
        assert not (board.root / "forbidden").exists()

    def test_unquoted_word_lists_are_never_globbed(self, board: Board) -> None:
        """set -f: a directory list entry with glob characters is a literal name."""
        board.grub("1", "1", trial=True)
        r = board.run("deadline", OTAPULSE_BL_GRUBENV_DIRS=f"{board.root}/grub/e*", OTAPULSE_BL_READ_TRIES="1")
        assert r.returncode == 3 and "no GRUB environment files" in r.stderr and board.actions == []
        assert re.search(r"^set -f$", GUARD.read_text(), re.M)

    def test_picks_the_directory_from_the_grubenv_config(self, board: Board) -> None:
        board.grub("1", "1", trial=True)
        cfg = board.root / "mender_grubenv.config"
        cfg.write_text(f"ENV_DIR = {board.root}/grub/efi\n")
        board.env.pop("OTAPULSE_BL_GRUBENV_DIRS")
        r = board.run("deadline", OTAPULSE_BL_GRUBENV_CONFIG=str(cfg))
        assert r.returncode == 0 and board.rebooted


# ======================= reboot-storm guards ===========================================

class TestRebootStormGuards:
    def test_at_most_one_guard_reboot_per_boot(self, board: Board) -> None:
        board.uboot("1", trial=True)
        assert board.run("deadline").returncode == 0
        assert board.actions == ["systemctl reboot"]
        r = board.run("deadline")  # same boot: the marker in /run is still there
        assert r.returncode == 0 and _loud(r) and "already issued" in r.stderr
        assert board.actions == ["systemctl reboot"]  # no second reboot
        assert board.counter == "1"

    def test_a_device_whose_rollback_is_impossible_stops_after_the_budget(
        self, board: Board
    ) -> None:
        """ua stays 1 across every reboot: bounded, not an endless loop."""
        board.uboot("1", trial=True)
        for n in range(1, 6):
            board.new_boot()
            assert board.run("deadline").returncode == 0
            assert board.rebooted, f"reboot {n} expected"
            assert board.counter == str(n)
        board.new_boot()
        r = board.run("deadline")
        assert r.returncode == 0 and _loud(r) and not board.rebooted
        assert "did not roll the update back" in r.stderr
        assert board.counter == "5"

    def test_budget_is_configurable(self, board: Board) -> None:
        board.uboot("1", trial=True)
        (board.data_otapulse / "bl-guard-reboots").write_text("1\n")
        r = board.run("deadline", OTAPULSE_BL_MAX_REBOOTS="1")
        assert not board.rebooted and "limit 1" in r.stderr

    def test_budget_resets_when_the_update_is_finally_committed(self, board: Board) -> None:
        board.uboot("1", trial=True)
        board.run("deadline")
        assert board.counter == "1"
        board.new_boot()
        board.uboot("0")  # the bootloader rolled back / the agent committed
        r = board.run("deadline")
        assert r.returncode == 0 and board.counter is None
        assert "budget reset" in r.stdout

    def test_a_corrupt_budget_file_fails_closed(self, board: Board) -> None:
        board.uboot("1", trial=True)
        (board.data_otapulse / "bl-guard-reboots").write_text("lots\n")
        r = board.run("deadline")
        assert not board.rebooted and _loud(r) and "corrupt" in r.stderr

    def test_deadline_still_reboots_once_when_data_is_not_writable(self, board: Board) -> None:
        """The deadline fires once per boot (OnBootSec); without a durable budget the
        per-boot marker is the guard."""
        board.grub("1", "1", trial=True)
        r = board.run("deadline", FAKE_DATA_MOUNTED="0")
        assert r.returncode == 0 and board.rebooted
        assert board.counter is None and "cannot be kept" in r.stderr
        assert "already issued" in board.run("deadline", FAKE_DATA_MOUNTED="0").stderr

    def test_budget_survives_in_the_documented_file_only(self, board: Board) -> None:
        board.uboot("1", trial=True)
        board.run("deadline")
        files = sorted(p.name for p in board.data_otapulse.iterdir())
        assert files == ["bl-guard-reboots"]  # atomic write leaves no temp file behind


class TestRebootLadder:
    def test_forced_reboot_when_the_graceful_one_fails(self, board: Board) -> None:
        board.uboot("1", trial=True)
        r = board.run("deadline", FAKE_SYSTEMCTL_RC="1")
        assert board.actions == ["systemctl reboot", "reboot -f"]
        assert "forcing" in r.stderr

    def test_sysrq_is_the_last_resort(self, board: Board) -> None:
        board.uboot("1", trial=True)
        r = board.run("deadline", FAKE_SYSTEMCTL_RC="1", FAKE_REBOOT_RC="1")
        assert board.actions == ["systemctl reboot", "reboot -f"]
        assert (board.root / "sysrq").read_text().strip() == "b"
        assert r.returncode == 1

    def test_never_a_one_shot_reboot_argument(self, board: Board) -> None:
        board.uboot("1", trial=True)
        board.run("deadline")
        assert all("tryboot" not in a and " 0" not in a for a in board.actions)


# ======================= seed-failed ===================================================

@pytest.mark.parametrize("kind", ["uboot", "grub"])
class TestSeedFailed:
    def _env(self, board: Board, kind: str, ua: str) -> None:
        if kind == "uboot":
            board.uboot(ua, trial=True)
        else:
            board.grub(ua, ua, trial=True)

    def test_on_an_uncommitted_update_it_reboots_at_once(self, board: Board, kind: str) -> None:
        self._env(board, kind, "1")
        board.set_uptime(40)  # well before any deadline
        r = board.run("seed-failed")
        assert r.returncode == 0, r.stderr
        assert board.rebooted
        assert board.counter == "1"
        assert "seed failure on an uncommitted update" in r.stderr
        assert _loud(r)

    def test_on_a_committed_slot_it_is_loud_but_does_not_reboot(
        self, board: Board, kind: str
    ) -> None:
        self._env(board, kind, "0")
        r = board.run("seed-failed")
        assert r.returncode == 0
        assert board.actions == [] and board.counter is None
        assert _loud(r) and "NOT rebooting" in r.stderr
        assert "committed slot" in r.stderr

    def test_a_committed_slot_never_reboot_loops_across_boots(
        self, board: Board, kind: str
    ) -> None:
        self._env(board, kind, "0")
        for _ in range(4):
            board.new_boot()
            board.run("seed-failed")
            assert board.actions == []


class TestSeedFailedGuards:
    def test_unreadable_environment_does_not_reboot(self, board: Board) -> None:
        board.uboot("FAIL")
        r = board.run("seed-failed")
        assert r.returncode == 3 and board.actions == [] and _loud(r)

    def test_rollback_impossible_is_bounded(self, board: Board) -> None:
        board.uboot("1", trial=True)
        for n in range(1, 6):
            board.new_boot()
            board.set_uptime(30)
            board.run("seed-failed")
            assert board.rebooted and board.counter == str(n)
        board.new_boot()
        r = board.run("seed-failed")
        assert not board.rebooted and "did not roll the update back" in r.stderr

    def test_shares_the_budget_with_the_deadline(self, board: Board) -> None:
        board.uboot("1", trial=True)
        (board.data_otapulse / "bl-guard-reboots").write_text("5\n")
        board.run("seed-failed")
        assert not board.rebooted
        board.new_boot()
        board.run("deadline")
        assert not board.rebooted

    def test_without_a_durable_budget_it_does_not_reboot_immediately(self, board: Board) -> None:
        """An immediate reboot with no memory could loop every few seconds."""
        board.grub("1", "1", trial=True)
        r = board.run("seed-failed", FAKE_DATA_MOUNTED="0")
        assert board.actions == [] and _loud(r) and "not rebooting immediately" in r.stderr

    def test_committed_in_the_meantime(self, board: Board) -> None:
        board.uboot("1", "0", trial=True)
        r = board.run("seed-failed")
        assert board.actions == [] and "committed in the meantime" in r.stdout


# ======================= clock-wait ====================================================

class TestClockWait:
    def test_committed_boot_does_not_wait_and_resets_the_budget(self, board: Board) -> None:
        board.uboot("0")
        board.timedatectl()
        (board.data_otapulse / "bl-guard-reboots").write_text("2\n")
        r = board.run("clock-wait")
        assert r.returncode == 0
        assert not (board.root / "tdc.calls").exists(), "must not poll the clock"
        assert board.sleeps == []
        assert board.counter is None

    def test_pending_boot_waits_until_synced(self, board: Board) -> None:
        board.uboot("1", trial=True)
        board.timedatectl()
        r = board.run("clock-wait", FAKE_SYNC_AFTER="4")
        assert r.returncode == 0
        assert "NTP-synced after 3s" in r.stdout
        assert len(board.sleeps) == 3

    def test_pending_boot_already_synced_does_not_sleep(self, board: Board) -> None:
        board.uboot("1", trial=True)
        board.timedatectl()
        r = board.run("clock-wait", FAKE_SYNC_AFTER="1")
        assert r.returncode == 0 and board.sleeps == [] and "after 0s" in r.stdout

    def test_a_clockless_board_is_bounded_and_still_exits_zero(self, board: Board) -> None:
        board.uboot("1", trial=True)
        board.timedatectl()
        r = board.run("clock-wait")  # never syncs
        assert r.returncode == 0  # never blocks the agent
        assert len(board.sleeps) == 60
        assert "starting the agent anyway" in r.stderr and "<4>" in r.stderr

    def test_bound_is_configurable(self, board: Board) -> None:
        board.uboot("1", trial=True)
        board.timedatectl()
        r = board.run("clock-wait", OTAPULSE_BL_CLOCK_WAIT_S="5")
        assert len(board.sleeps) == 5 and r.returncode == 0

    def test_unreadable_environment_still_waits(self, board: Board) -> None:
        board.uboot("FAIL")
        board.timedatectl()
        r = board.run("clock-wait", FAKE_SYNC_AFTER="3")
        assert r.returncode == 0
        assert "waiting for the clock anyway" in r.stderr
        assert "NTP-synced after 2s" in r.stdout

    def test_grub_pending_waits(self, board: Board) -> None:
        board.grub("1", "1", trial=True)
        board.timedatectl()
        r = board.run("clock-wait", FAKE_SYNC_AFTER="2")
        assert "NTP-synced after 1s" in r.stdout

    def test_no_timedatectl_continues(self, board: Board) -> None:
        board.uboot("1", trial=True)
        r = board.run("clock-wait")
        assert r.returncode == 0 and "timedatectl not installed" in r.stderr
        assert board.sleeps == []

    def test_never_writes_the_budget_or_reboots(self, board: Board) -> None:
        board.uboot("1", trial=True)
        board.timedatectl()
        board.run("clock-wait")
        assert board.actions == [] and board.counter is None


# ======================= misc ==========================================================

def test_status_is_read_only(board: Board) -> None:
    board.uboot("1", bootcount=2, trial=True)
    r = board.run("status")
    assert r.returncode == 0
    assert "kind=uboot upgrade_available=1 bootcount=2" in r.stdout
    assert "reboot_budget=0/5" in r.stdout
    assert board.actions == [] and board.counter is None


def test_unknown_subcommand_is_a_usage_error(board: Board) -> None:
    assert board.run("frobnicate").returncode == 2
    assert board.run("").returncode == 2


def test_a_hung_environment_tool_is_bounded(board: Board) -> None:
    board.uboot("1", trial=True)
    board._stub("fw_printenv", "#!/bin/sh\nexec sleep_real 100\n")
    board._stub("sleep_real", "#!/bin/sh\nexec /bin/sleep \"$1\"\n")
    r = subprocess.run(
        board.shell + [str(GUARD), "deadline"],
        env={**board.env, "PATH": f"{board.stubs}:{board.real}", "OTAPULSE_BL_READ_TRIES": "1"},
        capture_output=True, text=True, timeout=45,
    )
    assert r.returncode == 3 and board.actions == []


# ======================= trial-boot guards (review round 1 blocker) ====================

def _no_reboot(board: Board, r: subprocess.CompletedProcess) -> None:
    assert board.actions == [] and board.counter is None, board.actions
    assert not (board.run_dir / "bl-guard-rebooted").exists()


@pytest.mark.parametrize("kind", ["uboot", "grub"])
class TestInstalledButNotYetRebooted:
    """InstallUpdate writes upgrade_available=1, bootcount=0 (and the NEW slot) into the
    environment while the old slot is still running. A deadline that fires in that window
    would reboot into the new slot before anyone asked."""

    def _env(self, board: Board, kind: str, **kw) -> None:
        if kind == "uboot":
            board.uboot("1", **kw)
        else:
            board.grub("1", "1", **kw)

    def test_the_reviewer_probe_bootcount_zero_never_reboots(self, board: Board, kind: str) -> None:
        self._env(board, kind, bootcount=0)  # no marker at all
        board.set_uptime(5000)
        r = board.run("deadline")
        assert r.returncode == 0, r.stderr
        _no_reboot(board, r)
        assert "waiting for the agent's own reboot" in r.stdout

    def test_bootcount_zero_wins_even_with_a_matching_marker(self, board: Board, kind: str) -> None:
        """Protection (a) on its own: the marker is right, bootcount alone stands down."""
        self._env(board, kind, bootcount=0)
        board.arm_trial("3")
        r = board.run("deadline")
        assert r.returncode == 0
        _no_reboot(board, r)
        assert "waiting for the agent's own reboot" in r.stdout

    def test_matching_marker_alone_is_not_enough_without_bootcount(self, board: Board, kind: str) -> None:
        self._env(board, kind, bootcount=None)  # undefined / absent
        board.arm_trial("3")
        r = board.run("deadline")
        assert r.returncode == 3 and _loud(r) and "bootcount" in r.stderr
        _no_reboot(board, r)

    @pytest.mark.parametrize("bad", ["x", "-1", "1.5", "0x1"])
    def test_unparsable_bootcount_does_not_reboot(self, board: Board, kind: str, bad: str) -> None:
        if kind == "uboot":
            board.uboot("1", bootcount=bad)
        else:
            board.grub("1", "1", bootcount=bad)
        board.arm_trial("3")
        r = board.run("deadline")
        assert r.returncode == 3 and _loud(r)
        _no_reboot(board, r)

    def test_seed_failure_in_the_window_does_not_reboot_either(self, board: Board, kind: str) -> None:
        self._env(board, kind, bootcount=0)
        r = board.run("seed-failed")
        assert r.returncode == 0
        _no_reboot(board, r)
        assert "waiting for the agent's own reboot" in r.stdout

    def test_seed_failure_with_unknown_bootcount_does_not_reboot(self, board: Board, kind: str) -> None:
        self._env(board, kind, bootcount=None)
        r = board.run("seed-failed")
        assert r.returncode == 3
        _no_reboot(board, r)

    def test_bootcount_flipping_to_zero_before_the_final_check_stands_down(
        self, board: Board, kind: str
    ) -> None:
        """A new install lands between the first read and the re-read."""
        if kind == "uboot":
            board.uboot("1", "1", bootcount="1 0", trial=True)
        else:
            board.grub("1", "1", trial=True)
            for i in (1, 2):
                nx = board.root / "grub/efi" / f"mender_grubenv{i}" / "env.next"
                nx.write_text("bootcount=0\nmender_boot_part=3\nupgrade_available=1\n")
            board.env["FAKE_GRUB_SWITCH_AFTER"] = "2"
        r = board.run("deadline")
        assert r.returncode == 0, r.stderr
        _no_reboot(board, r)


@pytest.mark.parametrize("kind", ["uboot", "grub"])
class TestTrialMarker:
    def _env(self, board: Board, kind: str, slot: str = "3", **kw) -> None:
        if kind == "uboot":
            board.uboot("1", bootcount=1, slot=slot, **kw)
        else:
            board.grub("1", "1", bootcount=1, slot=slot, **kw)

    def test_no_marker_no_reboot(self, board: Board, kind: str) -> None:
        self._env(board, kind)
        r = board.run("deadline")
        assert r.returncode == 0
        _no_reboot(board, r)
        assert "no trial-boot marker" in r.stdout and "not acting" in r.stdout

    def test_marker_of_another_boot_no_reboot(self, board: Board, kind: str) -> None:
        self._env(board, kind)
        board.arm_trial("3", boot_id="deadbeef-0000-4000-8000-000000000000")
        r = board.run("deadline")
        assert r.returncode == 0
        _no_reboot(board, r)
        assert "another boot" in r.stdout

    def test_commit_then_new_install_in_the_same_boot_no_reboot(self, board: Board, kind: str) -> None:
        """Boot started as a trial of slot 2 (marker); the agent committed (ua 0) and then
        installed again: ua=1, mender_boot_part=3 (the inactive slot), bootcount=0 - and
        even if bootcount were already 1 the slot differs from the marker's."""
        for bc in (0, 1):
            board.arm_trial("2")
            if kind == "uboot":
                board.uboot("1", bootcount=bc, slot="3")
            else:
                board.grub("1", "1", bootcount=bc, slot="3")
            r = board.run("deadline")
            assert r.returncode == 0, r.stderr
            _no_reboot(board, r)
        assert "active slot changed" in r.stdout

    def test_slot_changing_between_the_first_read_and_the_final_check(self, board: Board, kind: str) -> None:
        if kind == "uboot":
            board.uboot("1", "1", bootcount=1, slot="3 2")
            board.arm_trial("3")
        else:
            board.grub("1", "1", bootcount=1, slot="3", trial=True)
            for i in (1, 2):
                (board.root / "grub/efi" / f"mender_grubenv{i}" / "env.next").write_text(
                    "bootcount=1\nmender_boot_part=2\nupgrade_available=1\n")
            board.env["FAKE_GRUB_SWITCH_AFTER"] = "2"
        r = board.run("deadline")
        assert r.returncode == 0, r.stderr
        _no_reboot(board, r)

    def test_unknown_active_slot_does_not_reboot(self, board: Board, kind: str) -> None:
        self._env(board, kind, slot="NONE" if kind == "uboot" else "")
        board.arm_trial("3")
        r = board.run("deadline")
        assert r.returncode == 3 and _loud(r)
        _no_reboot(board, r)

    def test_unreadable_boot_id_does_not_reboot(self, board: Board, kind: str) -> None:
        self._env(board, kind, trial=True)
        (board.root / "boot_id").unlink()
        r = board.run("deadline")
        assert r.returncode == 3 and _loud(r)
        _no_reboot(board, r)

    def test_matching_marker_and_bootcount_reboots(self, board: Board, kind: str) -> None:
        self._env(board, kind, trial=True)
        r = board.run("deadline")
        assert r.returncode == 0 and board.rebooted

    def test_marker_for_the_new_boot_is_required_after_a_real_reboot(self, board: Board, kind: str) -> None:
        self._env(board, kind, trial=True)
        board.run("deadline")
        assert board.rebooted
        board.new_boot()  # armed again by the next boot's clock-wait
        assert board.run("deadline").returncode == 0 and board.rebooted
        board.new_boot()
        (board.run_dir / "bl-trial-boot").unlink()  # a boot whose clock-wait did not arm
        board.run("deadline")
        assert not board.rebooted


@pytest.mark.parametrize("kind", ["uboot", "grub"])
class TestClockWaitArmsTheDeadline:
    def _env(self, board: Board, kind: str, ua: str = "1", **kw) -> None:
        kw.setdefault("bootcount", 1)
        if kind == "uboot":
            board.uboot(ua, **kw)
        else:
            board.grub(ua, ua, **kw)

    def _marker(self, board: Board) -> dict[str, str]:
        p = board.run_dir / "bl-trial-boot"
        assert p.exists(), "no marker"
        return dict(l.split("=", 1) for l in p.read_text().splitlines())

    def test_trial_boot_writes_boot_id_and_slot(self, board: Board, kind: str) -> None:
        self._env(board, kind, slot="3")
        board.timedatectl()
        r = board.run("clock-wait", FAKE_SYNC_AFTER="1")
        assert r.returncode == 0
        assert self._marker(board) == {"boot_id": (board.root / "boot_id").read_text().strip(), "slot": "3"}
        assert "boot deadline armed" in r.stdout

    def test_committed_boot_writes_no_marker(self, board: Board, kind: str) -> None:
        self._env(board, kind, ua="0")
        board.run("clock-wait")
        assert not (board.run_dir / "bl-trial-boot").exists()

    def test_installed_not_rebooted_is_not_a_trial_boot(self, board: Board, kind: str) -> None:
        self._env(board, kind, bootcount=0)
        board.timedatectl()
        r = board.run("clock-wait", FAKE_SYNC_AFTER="1")
        assert not (board.run_dir / "bl-trial-boot").exists()
        assert "not a trial boot" in r.stderr and r.returncode == 0

    def test_unreadable_environment_writes_no_marker_but_still_waits(self, board: Board, kind: str) -> None:
        if kind == "uboot":
            board.uboot("FAIL")
        else:
            board.grub(None, None)
        board.timedatectl()
        r = board.run("clock-wait", FAKE_SYNC_AFTER="2")
        assert r.returncode == 0 and not (board.run_dir / "bl-trial-boot").exists()
        assert "will not act on this boot" in r.stderr

    def test_marker_is_written_even_if_the_clock_never_syncs(self, board: Board, kind: str) -> None:
        self._env(board, kind)
        board.timedatectl()
        r = board.run("clock-wait")  # bounded 60 s wait times out
        assert r.returncode == 0 and len(board.sleeps) == 60
        self._marker(board)

    def test_marker_is_written_even_without_timedatectl(self, board: Board, kind: str) -> None:
        self._env(board, kind)
        r = board.run("clock-wait")
        assert r.returncode == 0
        self._marker(board)

    def test_marker_survives_an_unwritable_run_dir_without_failing(self, board: Board, kind: str) -> None:
        self._env(board, kind)
        board.timedatectl()
        blocker = board.root / "run-is-a-file"
        blocker.write_text("")
        r = board.run("clock-wait", OTAPULSE_BL_RUN_DIR=str(blocker / "otapulse"), FAKE_SYNC_AFTER="1")
        assert r.returncode == 0 and "cannot write" in r.stderr

    def test_first_writer_wins_within_a_boot(self, board: Board, kind: str) -> None:
        """A restart of the unit after a commit + new install must not re-arm the deadline."""
        self._env(board, kind, slot="2")
        board.timedatectl()
        board.run("clock-wait", FAKE_SYNC_AFTER="1")
        assert self._marker(board)["slot"] == "2"
        self._env(board, kind, slot="3")
        board.run("clock-wait", FAKE_SYNC_AFTER="1")
        assert self._marker(board)["slot"] == "2"

    def test_end_to_end_clock_wait_then_deadline(self, board: Board, kind: str) -> None:
        """No hand-made marker: what clock-wait leaves is what the deadline accepts."""
        self._env(board, kind)
        board.timedatectl()
        board.run("clock-wait", FAKE_SYNC_AFTER="1")
        r = board.run("deadline")
        assert r.returncode == 0 and board.rebooted
        # ... and the same boot after a commit and a new install does not
        board.new_boot()
        board.arm_trial("2")  # marker of this boot says slot 2
        (board.root / "actions").unlink(missing_ok=True)
        (board.run_dir / "bl-guard-rebooted").unlink(missing_ok=True)
        self._env(board, kind, bootcount=0, slot="3")
        assert board.run("deadline").returncode == 0 and board.actions == []


# ======================= busybox ash (no-reboot decisions only) ========================

@pytest.mark.skipif(BUSYBOX_ASH is None, reason="busybox not installed on the host")
class TestBusyboxAsh:
    """Same script under the shell most Yocto images use as /bin/sh. Only paths that cannot
    reach a reboot or a sleep (busybox would run its own applets there, not our stubs)."""

    @pytest.fixture
    def board(self, tmp_path: Path) -> Board:
        return Board(tmp_path, BUSYBOX_ASH)

    def test_uboot_committed_and_pending_below_deadline(self, board: Board) -> None:
        board.uboot("0")
        assert board.run("deadline").returncode == 0
        board.uboot("1", trial=True)
        board.set_uptime(100)
        r = board.run("deadline")
        assert r.returncode == 0 and "not yet" in r.stdout and board.actions == []

    def test_grub_copy_selection(self, board: Board) -> None:
        board.grub("1", "0", edit1="0000000a", edit2="0000000b")  # copy 2 wins: committed
        assert board.run("deadline").returncode == 0 and board.actions == []
        board.grub("1", "0")  # undecidable
        r = board.run("deadline", OTAPULSE_BL_READ_TRIES="1")
        assert r.returncode == 3 and board.actions == []

    def test_unreadable_never_reboots(self, board: Board) -> None:
        board.uboot("FAIL")
        r = board.run("deadline", OTAPULSE_BL_READ_TRIES="1")
        assert r.returncode == 3 and _loud(r) and board.actions == []

    def test_status(self, board: Board) -> None:
        board.uboot("1", bootcount=3, trial=True)
        assert "kind=uboot upgrade_available=1 bootcount=3" in board.run("status").stdout


# ======================= packaging / wiring ============================================

def _active(text: str) -> str:
    return "\n".join(l for l in text.splitlines() if not l.lstrip().startswith("#"))


def test_script_is_posix_sh_and_executable() -> None:
    assert GUARD.read_text().startswith("#!/bin/sh\n")
    assert GUARD.stat().st_mode & stat.S_IXUSR
    for sh in (["sh", "-n"], ["dash", "-n"]):
        if shutil.which(sh[0]):
            assert subprocess.run(sh + [str(GUARD)]).returncode == 0
    if shutil.which("shellcheck"):
        assert subprocess.run(["shellcheck", "-s", "sh", str(GUARD)]).returncode == 0


def test_script_never_writes_the_bootloader_environment() -> None:
    code = _active(GUARD.read_text())
    for forbidden in ("fw_setenv", "grub-editenv.* set", "grub-mender-grubenv-set", "grub-mender-grubenv-print", "mender-update", "tryboot"):
        assert not re.search(forbidden, code), forbidden


def test_timer_is_twenty_minutes_from_boot() -> None:
    timer = _active(UNITS["otapulse-bl-boot-deadline.timer"].read_text())
    assert re.search(r"^OnBootSec=1200$", timer, re.M)
    assert re.search(r"^Unit=otapulse-bl-boot-deadline\.service$", timer, re.M)
    assert re.search(r"^WantedBy=timers\.target$", timer, re.M)
    assert "Persistent" not in timer


def test_deadline_service_runs_the_deadline_command() -> None:
    svc = _active(UNITS["otapulse-bl-boot-deadline.service"].read_text())
    assert re.search(r"^ExecStart=/usr/bin/otapulse-bl-guard deadline$", svc, re.M)
    assert re.search(r"^Type=oneshot$", svc, re.M)
    assert "[Install]" not in svc  # started only by the timer
    # no automatic retry: a persistently failing unit must not loop
    assert "Restart=" not in svc


def test_data_seed_failure_triggers_the_reboot_handler() -> None:
    seed = _active(UNITS["otapulse-data-seed.service"].read_text())
    assert re.search(r"^OnFailure=otapulse-bl-seed-failure\.service$", seed, re.M)
    handler = _active(UNITS["otapulse-bl-seed-failure.service"].read_text())
    assert re.search(r"^ExecStart=/usr/bin/otapulse-bl-guard seed-failed$", handler, re.M)
    assert "[Install]" not in handler and "Restart=" not in handler


def test_clock_wait_orders_before_the_agent_without_requiring_it_or_being_required() -> None:
    svc = _active(UNITS["otapulse-bl-clock-wait.service"].read_text())
    assert "soc-ota-agent.service" in " ".join(re.findall(r"^Before=(.*)$", svc, re.M)).split()
    after = " ".join(re.findall(r"^After=(.*)$", svc, re.M)).split()
    for needed in ("data.mount", "network-online.target", "systemd-timesyncd.service"):
        assert needed in after
    # a hard dependency in either direction would let a stuck wait block the agent
    assert not re.search(r"^Requires=", svc, re.M) and not re.search(r"^BindsTo=", svc, re.M)
    assert re.search(r"^WantedBy=multi-user\.target$", svc, re.M)
    assert re.search(r"^ExecStart=/usr/bin/otapulse-bl-guard clock-wait$", svc, re.M)
    timeout = int(re.search(r"^TimeoutStartSec=(\d+)$", svc, re.M).group(1))
    assert timeout > 60  # the script's own bound is 60 s
    # and the agent side (drop-in + base unit) must not Require it
    agent_dir = HERE.parent / "soc-ota-agent" / "files"
    for f in ("10-bootloader-mode.conf", "soc-ota-agent.service"):
        assert "otapulse-bl-clock-wait" not in _active((agent_dir / f).read_text())


def test_recipe_installs_the_guard_in_bootloader_mode_only() -> None:
    text = RECIPE.read_text()
    marker = "# ── Bootloader compat mode (TODO-157 / sprint S109, TASK-S109-009)"
    native, bl = text.split(marker, 1)
    # native part of the recipe: no trace
    assert "otapulse-bl-" not in native and "otapulse-bootloader" not in native
    names = ["otapulse-bl-guard"] + [n for n in UNITS if n.startswith("otapulse-bl-")]
    for name in names:
        assert re.search(rf"SRC_URI:append:otapulse-bootloader = \"[^\"]*file://{re.escape(name)}\s", bl), name
        assert re.search(rf"^FILES:\$\{{PN\}}:append:otapulse-bootloader = \"[^\"]*/{re.escape(name)}\s", bl, re.M | re.S), name
        install_block = bl.split("do_install:append:otapulse-bootloader()", 1)[1]
        assert name in install_block, f"{name} is never installed"
    enabled = re.search(r"^SYSTEMD_SERVICE:\$\{PN\}:append:otapulse-bootloader = \"([^\"]*)\"", bl, re.M).group(1).split()
    assert "otapulse-bl-boot-deadline.timer" in enabled
    assert "otapulse-bl-clock-wait.service" in enabled
    assert "otapulse-data-seed.service" in enabled
    # static units are started by OnFailure= / the timer only
    assert "otapulse-bl-seed-failure.service" not in enabled
    assert "otapulse-bl-boot-deadline.service" not in enabled
    # the native deadline is still removed, never re-enabled
    removed = re.search(r"^SYSTEMD_SERVICE:\$\{PN\}:remove:otapulse-bootloader = \"([^\"]*)\"", bl, re.M).group(1)
    assert "otapulse-boot-deadline.timer" in removed


def test_every_bootloader_assignment_carries_the_override() -> None:
    text = RECIPE.read_text()
    bl = text.split("# ── Bootloader compat mode (TODO-157 / sprint S109, TASK-S109-009)", 1)[1]
    for line in bl.splitlines():
        m = re.match(r"^(SRC_URI|RDEPENDS|SYSTEMD_SERVICE|FILES|do_install)\b(\S*)", line)
        if m:
            assert "otapulse-bootloader" in line.split("=")[0], line


def test_user_facing_text_is_white_labelled() -> None:
    """No upstream product name in unit descriptions or log text. (Variable and
    directory names of the bootloader environment, e.g. mender_grubenv1 or
    grub-mender-grubenv, are internal path literals the board support fixes.)"""
    text = GUARD.read_text()
    msgs = re.findall(r'(?:info|warn|err) "([^"]*)"', text)
    assert len(msgs) > 10
    for msg in msgs:
        assert "mender" not in msg.lower(), msg
    for echo in re.findall(r'echo "(usage[^"]*)"', text):
        assert "mender" not in echo.lower()
    for path in UNITS.values():
        for line in path.read_text().splitlines():
            if line.startswith("Description=") or line.startswith("ExecStart="):
                assert "mender" not in line.lower(), (path.name, line)
