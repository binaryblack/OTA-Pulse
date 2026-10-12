#!/usr/bin/env python3
"""Deterministic content hash of a vendored upstream tree (TASK-S109-003).

Used to generate and verify ``mender-upstream.lock`` (TASK-S109-004's
``mender-sync.sh --verify`` reuses this module).

Tree-hash algorithm (version 1, ``tree-sha256``)
-----------------------------------------------
1. Walk the tree rooted at ``DIR``. Skip every directory named ``.git``, the
   top-level file ``UPSTREAM``, and every top-level path named on an
   ``added-file:`` line in ``UPSTREAM`` (files OTA-Pulse added that do not
   exist upstream, e.g. a copied repo-root LICENSE). Nothing else is skipped.
2. Visit entries in ascending order of their ``/``-separated relative path,
   compared as UTF-8 bytes. Directories are not listed (git does not track
   empty directories); only files and symlinks are.
3. For each entry emit exactly one line (``\\n`` terminated, UTF-8):

   * regular file: ``F <mode> <sha256-of-content> <relpath>`` where ``<mode>``
     is ``755`` if the owner-execute bit is set, otherwise ``644`` (the same
     normalisation git applies).
   * symlink: ``L 120000 <sha256-of-link-target-string> <relpath>``.
4. The tree hash is the lowercase hex SHA-256 of the concatenation of all
   lines. An empty tree hashes the empty string.

The same algorithm applied to ``git archive <commit> <subpath>`` of the
upstream repo (extracted without the leading ``<subpath>/``) must give the
same value as the vendored tree, minus the ``added-file:`` entries.

Pristine versus patched trees (TASK-S109-004)
---------------------------------------------
The algorithm is applied to two different trees and the lock records both:

* ``tree-sha256`` (lock column 7) is the hash of the PRISTINE upstream
  ``<subpath>`` at ``<commit>``. Anyone can reproduce it from a fresh clone.
* ``patched-tree-sha256`` (lock column 8) is the hash of the tree that is
  actually vendored on disk after the checked-in ``patches/mender/<component>/
  *.patch`` series was applied to the pristine tree. It is ``-`` when the
  component has no patches, in which case the on-disk tree IS the pristine
  tree.

``mender-sync.sh --verify`` checks the on-disk tree against column 8 when it is
set, otherwise against column 7. Because the hash covers file contents, a
manifest (the ``--list`` output, whose own sha256 equals the tree hash) is kept
per component in ``mender-upstream.manifests/`` so a mismatch can be reported
file by file (``--diff``).

Usage::

    mender_tree_hash.py DIR                  # print the tree hash
    mender_tree_hash.py --list DIR           # print the manifest lines
    mender_tree_hash.py --diff MANIFEST DIR  # print "M|A|D <path>" differences,
                                             # exit 1 if there are any
    mender_tree_hash.py --added-files DIR    # print each UPSTREAM added-file path
                                             # (duplicates kept), one per line

A path that contains a control character (TAB and LF included), NEL / U+2028 / U+2029,
or bytes that are not valid UTF-8 is refused (exit 2) by every action, since the
unescaped manifest line could otherwise stand in for several others. UPSTREAM must not
contain NUL bytes or duplicate ``key:`` lines.

UPSTREAM is parsed as raw bytes, split on ``\\n`` only, and rejected (exit 2) if it
contains any other line-break character (CR, VT, FF, FS/GS/RS, NEL, U+2028/2029).
"""

from __future__ import annotations

import argparse
import hashlib
import os
import stat
import sys
from pathlib import Path

UPSTREAM_FILE = "UPSTREAM"


# Characters that str.splitlines() (and some other parsers) treat as line breaks but a
# plain "\n" split does not. UPSTREAM is read by more than one parser (this module, and
# shell tools in mender-sync.sh's hooks); a line break that only one of them honours would
# let an "added-file:" line hide from the other. So they are refused outright.
FORBIDDEN_UPSTREAM_CHARS = (
    "\r",
    "\x0b",
    "\x0c",
    "\x1c",
    "\x1d",
    "\x1e",
    "\x85",
    "\u2028",
    "\u2029",
)


def added_file_entries(root: Path) -> list[str]:
    """Return, in file order, the path on every ``added-file:`` line of UPSTREAM.

    The path is the first space-separated token after the colon (anything after it
    is a comment). Duplicates are kept. This is the ONE parser of that line: the hash
    excludes exactly these paths, and ``mender-sync.sh --verify`` validates this same
    list. UPSTREAM is read as raw bytes and split on ``\\n`` only; a file containing any
    of ``FORBIDDEN_UPSTREAM_CHARS`` raises ``ValueError`` (fail closed).
    """
    upstream = root / UPSTREAM_FILE
    if not upstream.is_file():
        return []
    text = upstream.read_bytes().decode("utf-8")
    if "\x00" in text:
        raise ValueError(f"{upstream}: contains a NUL byte; refusing")
    seen: set[str] = set()
    for raw in text.split("\n"):
        if not raw.strip() or raw.startswith("#") or ":" not in raw:
            continue
        key = raw.split(":", 1)[0].strip()
        if key in seen:
            raise ValueError(
                f"{upstream}: duplicate key {key!r} (line {raw!r}); each UPSTREAM "
                "key may appear once"
            )
        seen.add(key)
    for char in FORBIDDEN_UPSTREAM_CHARS:
        if char in text:
            raise ValueError(
                f"{upstream}: contains the line-break character {char!r}, which "
                "different parsers disagree on; refusing (UPSTREAM must use \\n only)"
            )
    entries: list[str] = []
    for raw in text.split("\n"):
        if raw.startswith("added-file:"):
            value = raw.split(":", 1)[1].strip()
            if value:
                # Allow a trailing "(comment)" after the path.
                entries.append(value.split(" ", 1)[0])
    return entries


def parse_added_files(root: Path) -> set[str]:
    """Return the relative paths named on ``added-file:`` lines of UPSTREAM."""
    return set(added_file_entries(root))


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


# Unicode line separators that are not ASCII control characters.
_UNICODE_LINE_SEPARATORS = ("\x85", "\u2028", "\u2029")


def check_relpath(rel: str) -> bytes:
    """Return ``rel`` as UTF-8 bytes, or raise ``ValueError`` if it is unsafe to hash.

    A manifest line is ``F <mode> <sha> <relpath>\\n`` with the path unescaped, so a path
    that contains a line break (or any control character) could stand in for several
    manifest lines and leave the tree hash unchanged while real files are removed.
    Refused: ASCII control characters (< 0x20, which includes TAB and LF, and 0x7f),
    NEL / U+2028 / U+2029, and names that are not valid UTF-8 (os.walk reports such
    bytes as lone surrogates). The hash encoding itself is unchanged.
    """
    try:
        encoded = rel.encode("utf-8")
    except UnicodeEncodeError:
        raise ValueError(f"unsafe path {rel!r}: not valid UTF-8") from None
    for char in rel:
        if ord(char) < 0x20 or ord(char) == 0x7F or char in _UNICODE_LINE_SEPARATORS:
            raise ValueError(
                f"unsafe path {rel!r}: contains the control or line-break "
                f"character {char!r}"
            )
    return encoded


def manifest(root: Path, extra_excludes: set[str] | None = None) -> list[str]:
    """Return the sorted manifest lines for ``root`` (see module docstring)."""
    root = Path(root)
    excludes = parse_added_files(root) | (extra_excludes or set())
    excludes.add(UPSTREAM_FILE)
    entries: list[tuple[bytes, str]] = []
    for dirpath, dirnames, filenames in os.walk(root, followlinks=False):
        dirnames[:] = [d for d in dirnames if d != ".git"]
        base = Path(dirpath)
        # os.walk lists symlinks to directories under dirnames; handle them
        # as symlink entries and do not descend.
        for name in list(dirnames):
            if (base / name).is_symlink():
                dirnames.remove(name)
                filenames.append(name)
        for name in filenames:
            path = base / name
            rel = path.relative_to(root).as_posix()
            rel_bytes = check_relpath(rel)
            if rel in excludes:
                continue
            if path.is_symlink():
                target = os.readlink(path)
                sha = hashlib.sha256(os.fsencode(target)).hexdigest()
                line = f"L 120000 {sha} {rel}\n"
            else:
                mode = "755" if path.stat().st_mode & stat.S_IXUSR else "644"
                line = f"F {mode} {_sha256_file(path)} {rel}\n"
            entries.append((rel_bytes, line))
    entries.sort(key=lambda item: item[0])
    return [line for _, line in entries]


def tree_hash(root: Path, extra_excludes: set[str] | None = None) -> str:
    """Return the lowercase hex tree hash of ``root``."""
    payload = "".join(manifest(root, extra_excludes)).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def parse_manifest(text: str) -> dict[str, str]:
    """Map relpath -> manifest line for the given manifest text."""
    result: dict[str, str] = {}
    for line in text.splitlines():
        if not line.strip():
            continue
        # "F <mode> <sha> <relpath>" / "L 120000 <sha> <relpath>"; the path
        # is everything after the third space (it may itself contain spaces).
        parts = line.split(" ", 3)
        if len(parts) != 4:
            raise ValueError(f"malformed manifest line: {line!r}")
        result[parts[3]] = line
    return result


def diff_manifest(expected_text: str, root: Path) -> list[str]:
    """Return ``"<flag> <path>"`` lines describing how ``root`` differs.

    Flags: ``M`` content/mode/type changed, ``A`` extra file on disk,
    ``D`` file missing on disk. Output is sorted by path.
    """
    expected = parse_manifest(expected_text)
    actual = parse_manifest("".join(manifest(root)))
    out: list[tuple[str, str]] = []
    for path, line in expected.items():
        if path not in actual:
            out.append((path, "D"))
        elif actual[path] != line:
            out.append((path, "M"))
    for path in actual:
        if path not in expected:
            out.append((path, "A"))
    out.sort(key=lambda item: item[0].encode("utf-8"))
    return [f"{flag} {path}" for path, flag in out]


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    parser.add_argument("directory", type=Path)
    parser.add_argument(
        "--list", action="store_true", help="print manifest lines, not the hash"
    )
    parser.add_argument(
        "--diff",
        type=Path,
        metavar="MANIFEST",
        help="compare DIR against a stored manifest, print M/A/D lines",
    )
    parser.add_argument(
        "--added-files",
        action="store_true",
        help="print the UPSTREAM added-file paths, one per line",
    )
    args = parser.parse_args(argv)
    if not args.directory.is_dir():
        print(f"not a directory: {args.directory}", file=sys.stderr)
        return 2
    try:
        return _run(args)
    except (ValueError, OSError) as exc:
        print(f"mender_tree_hash.py: {exc}", file=sys.stderr)
        return 2


def _run(args: argparse.Namespace) -> int:
    """Execute the selected action; ``ValueError``/``OSError`` propagate to main()."""
    if args.added_files:
        for entry in added_file_entries(args.directory):
            print(entry)
        return 0
    if args.diff is not None:
        if not args.diff.is_file():
            print(f"no such manifest: {args.diff}", file=sys.stderr)
            return 2
        lines = diff_manifest(args.diff.read_text(encoding="utf-8"), args.directory)
        for line in lines:
            print(line)
        return 1 if lines else 0
    if args.list:
        sys.stdout.write("".join(manifest(args.directory)))
    else:
        print(tree_hash(args.directory))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
