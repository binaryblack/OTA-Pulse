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

Usage::

    mender_tree_hash.py DIR            # print the tree hash
    mender_tree_hash.py --list DIR     # print the manifest lines
"""

from __future__ import annotations

import argparse
import hashlib
import os
import stat
import sys
from pathlib import Path

UPSTREAM_FILE = "UPSTREAM"


def parse_added_files(root: Path) -> set[str]:
    """Return the relative paths named on ``added-file:`` lines of UPSTREAM."""
    upstream = root / UPSTREAM_FILE
    added: set[str] = set()
    if not upstream.is_file():
        return added
    for raw in upstream.read_text(encoding="utf-8").splitlines():
        if raw.startswith("added-file:"):
            value = raw.split(":", 1)[1].strip()
            if value:
                # Allow a trailing "(comment)" after the path.
                added.add(value.split(" ", 1)[0])
    return added


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


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
            if rel in excludes:
                continue
            if path.is_symlink():
                target = os.readlink(path)
                sha = hashlib.sha256(target.encode("utf-8")).hexdigest()
                line = f"L 120000 {sha} {rel}\n"
            else:
                mode = "755" if path.stat().st_mode & stat.S_IXUSR else "644"
                line = f"F {mode} {_sha256_file(path)} {rel}\n"
            entries.append((rel.encode("utf-8"), line))
    entries.sort(key=lambda item: item[0])
    return [line for _, line in entries]


def tree_hash(root: Path, extra_excludes: set[str] | None = None) -> str:
    """Return the lowercase hex tree hash of ``root``."""
    payload = "".join(manifest(root, extra_excludes)).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    parser.add_argument("directory", type=Path)
    parser.add_argument(
        "--list", action="store_true", help="print manifest lines, not the hash"
    )
    args = parser.parse_args(argv)
    if not args.directory.is_dir():
        print(f"not a directory: {args.directory}", file=sys.stderr)
        return 2
    if args.list:
        sys.stdout.write("".join(manifest(args.directory)))
    else:
        print(tree_hash(args.directory))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
