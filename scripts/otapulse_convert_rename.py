#!/usr/bin/env python3
"""Mechanical white-label rename of the vendored image conversion tool (TASK-S109-025).

INTERNAL developer tool. It is the generator of the patch
``patches/mender/mender-convert/0100-rename-generated.patch``; it is not run by any build.

The tool that ``otapulse-convert`` is made from is an upstream project. Its hand written changes
are the patches ``0001`` .. ``0099`` of ``patches/mender/mender-convert/``; this script renames
what is left over, so that no upstream product name reaches a customer:

* configuration variables ``MENDER_*``      -> ``OTAPULSE_*``
* file names and commands ``mender-convert*``, ``mender_convert*``, ``mender-artifact``
* the words ``Mender`` and ``mender``       -> ``OTA-Pulse`` / ``otapulse``

and it leaves untouched the identifiers that are part of the on-device bootloader integration
(``PROTECTED`` below): they are read by the bootloader scripts of the vendored bootloader
environment and by the agent, so renaming them would break a converted device.

Usage::

    otapulse_convert_rename.py DIR            rename DIR in place (idempotent)
    otapulse_convert_rename.py --check DIR    exit 1 and list every change it would make

``--check`` is the consistency test of the checked in patch series: on a tree that has the whole
series applied the generator must find nothing to change.

``--residual DIR`` prints every remaining line that contains "mender" (any case), for the
documented allow list (docs/mender-compat/OTAPULSE-CONVERT.md).

``--audit ALLOWLIST DIR`` is the white-label gate of the finished tool: every remaining occurrence
of the upstream name (in a file name or a line of a text file) must be covered by an entry of the
allow list, and every entry must cover at least one occurrence (a stale entry is an error too).
Allow list format: one entry per line, three TAB separated columns ``path-glob``, ``regex``,
``justification``; ``#`` starts a comment line.
"""

from __future__ import annotations

import argparse
import fnmatch
import os
import re
import sys
from pathlib import Path
from typing import Iterator, List, Tuple

# Identifiers of the bootloader integration that stay as they are. Each entry is a regular
# expression; a match is hidden from the renaming rules.
PROTECTED: Tuple[str, ...] = (
    # directory on the boot partition, make targets and the defines file of the bootloader scripts
    r"grub-mender-grubenv",
    r"mender_grubenv_defines",
    # variables of the GRUB / U-Boot boot scripts, set in the defines file or the cmdline template
    r"\bmender_(?:kernel_root_base|kernel_root|rootfs[ab]_part|rootfs[ab]_uuid|boot_uuid|boot_part)\b",
    # state folders of other updaters: detection of a tainted input image (modules/probe.sh)
    r"var/lib/mender-(?:monitor|configure)",
)

# Rules applied in order to the text left after hiding the protected identifiers.
RULES: Tuple[Tuple[str, str], ...] = (
    (r"MENDER_", "OTAPULSE_"),
    (r"mender-convert", "otapulse-convert"),
    (r"mender_convert", "otapulse_convert"),
    (r"mender-artifact", "otapulse-artifact"),
    (r"mender-grow-data", "otapulse-grow-data"),
    (r"Mender", "OTA-Pulse"),
    (r"mender", "otapulse"),
)

SKIP_FILES = ("LICENSE", "UPSTREAM")
SKIP_DIRS = (".git",)

_PROT_RE = re.compile("|".join("(?:%s)" % p for p in PROTECTED))


def rename_text(text: str) -> str:
    """Apply the renaming rules to a string (protected identifiers excluded)."""
    hidden: List[str] = []

    def hide(match: "re.Match[str]") -> str:
        hidden.append(match.group(0))
        return "\x00%d\x00" % (len(hidden) - 1)

    out = _PROT_RE.sub(hide, text)
    for pattern, replacement in RULES:
        out = re.sub(pattern, replacement, out)
    return re.sub("\x00(\\d+)\x00", lambda m: hidden[int(m.group(1))], out)


def iter_files(root: Path) -> Iterator[Path]:
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = sorted(d for d in dirnames if d not in SKIP_DIRS)
        for name in sorted(filenames):
            yield Path(dirpath) / name


def plan(root: Path) -> List[Tuple[Path, Path, str, bool]]:
    """Return (old path, new path, new text, text changed) for every file that changes."""
    changes: List[Tuple[Path, Path, str, bool]] = []
    for path in iter_files(root):
        rel = path.relative_to(root)
        if rel.name in SKIP_FILES and len(rel.parts) == 1:
            continue
        new_rel = Path(*(rename_text(part) for part in rel.parts))
        if path.is_symlink():
            if new_rel != rel:
                changes.append((path, root / new_rel, "", False))
            continue
        try:
            text = path.read_text(encoding="utf-8")
            new_text = rename_text(text)
        except UnicodeDecodeError:
            text = new_text = ""
        if new_rel != rel or new_text != text:
            changes.append((path, root / new_rel, new_text, new_text != text))
    return changes


def apply(root: Path, changes: List[Tuple[Path, Path, str, bool]]) -> None:
    for old, new, new_text, text_changed in changes:
        if old != new:
            new.parent.mkdir(parents=True, exist_ok=True)
            if new.exists():
                raise SystemExit("refusing to overwrite %s" % new)
            old.rename(new)
        if text_changed:
            mode = new.stat().st_mode
            new.write_text(new_text, encoding="utf-8")
            new.chmod(mode)
    # remove directories that became empty
    for dirpath, dirnames, filenames in os.walk(root, topdown=False):
        if Path(dirpath) != root and not dirnames and not filenames and ".git" not in Path(dirpath).parts:
            Path(dirpath).rmdir()


def occurrences(root: Path) -> List[Tuple[str, int, str]]:
    """Every remaining occurrence of the upstream name: (relative path, line number or 0 for a
    file name, text of the line or the file name)."""
    found: List[Tuple[str, int, str]] = []
    for path in iter_files(root):
        rel = path.relative_to(root)
        if rel.name in SKIP_FILES and len(rel.parts) == 1:
            continue
        if re.search("mender", str(rel), re.I):
            found.append((str(rel), 0, rel.name))
        try:
            text = path.read_text(encoding="utf-8")
        except (UnicodeDecodeError, OSError):
            continue
        for number, line in enumerate(text.splitlines(), 1):
            if re.search("mender", line, re.I):
                found.append((str(rel), number, line.strip()))
    return found


def residual(root: Path) -> List[str]:
    return [
        "%s: <file name>" % rel if number == 0 else "%s:%d: %s" % (rel, number, text)
        for rel, number, text in occurrences(root)
    ]


def load_allowlist(path: Path) -> List[Tuple[str, "re.Pattern[str]", str]]:
    entries: List[Tuple[str, "re.Pattern[str]", str]] = []
    for number, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        cols = raw.split("\t")
        if len(cols) != 3 or not all(c.strip() for c in cols):
            raise SystemExit("%s:%d: expected 3 TAB separated columns (path-glob, regex, justification)" % (path, number))
        entries.append((cols[0].strip(), re.compile(cols[1].strip(), re.I), cols[2].strip()))
    return entries


def audit(root: Path, allowlist: Path) -> List[str]:
    """Return the problems: an occurrence nobody allowed, or an allow list entry that matches nothing."""
    entries = load_allowlist(allowlist)
    used = [0] * len(entries)
    problems: List[str] = []
    for rel, number, text in occurrences(root):
        for index, (glob, regex, _why) in enumerate(entries):
            if fnmatch.fnmatch(rel, glob) and regex.search(text):
                used[index] += 1
                break
        else:
            problems.append("not allowed: %s" % ("%s: <file name>" % rel if number == 0 else "%s:%d: %s" % (rel, number, text)))
    for index, (glob, regex, _why) in enumerate(entries):
        if not used[index]:
            problems.append("stale allow list entry (matches nothing): %s  %s" % (glob, regex.pattern))
    return problems


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("directory", type=Path)
    parser.add_argument("--check", action="store_true", help="do not change anything; exit 1 if the tree is not renamed")
    parser.add_argument("--residual", action="store_true", help="print the lines that still contain the upstream name")
    parser.add_argument("--audit", type=Path, metavar="ALLOWLIST", help="check every remaining occurrence against the allow list")
    args = parser.parse_args()
    root = args.directory.resolve()
    if not root.is_dir():
        print("not a directory: %s" % root, file=sys.stderr)
        return 2
    if args.residual:
        for line in residual(root):
            print(line)
        return 0
    if args.audit:
        problems = audit(root, args.audit)
        for problem in problems:
            print(problem)
        return 1 if problems else 0
    changes = plan(root)
    if args.check:
        for old, new, _text, _changed in changes:
            print("would change %s%s" % (old.relative_to(root), "" if old == new else " -> %s" % new.relative_to(root)))
        return 1 if changes else 0
    apply(root, changes)
    print("renamed %d file(s)" % len(changes))
    return 0


if __name__ == "__main__":
    sys.exit(main())
