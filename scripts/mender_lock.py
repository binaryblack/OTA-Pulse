#!/usr/bin/env python3
"""Text helpers for ``mender-upstream.lock`` and ``UPSTREAM`` files (TASK-S109-004).

Internal developer tool, called only by ``scripts/mender-sync.sh``. It does no
network access and runs no git commands; it only reads and rewrites the small
text files that pin the vendored upstream board-integration layers.

Lock row format (TAB separated, one record per line, ``#`` lines are comments)::

    component url ref commit subpath vendored-dir tree-sha256   (cols 1-7, S109-003)
    patched-tree-sha256 patches-sha256 license-sha256           (cols 8-10, S109-004)

Columns 8-10 are optional so that a legacy 7-column row still parses; a column
whose value does not apply is written as ``-``. ``row`` prints ``?`` for a
column that is absent from a legacy row.

Subcommands::

    row LOCK COMPONENT                 print the 10 fields TAB separated
    components LOCK                    print every component named in LOCK
    set LOCK COMPONENT FIELD x10       rewrite / append the row, in place
    patches-hash PATCH_DIR             print the patch-series hash, or "-"
    sha256 FILE                        print the sha256 of a file
    render-upstream ...                print a regenerated UPSTREAM file
"""

from __future__ import annotations

import argparse
import hashlib
import re
import sys
from pathlib import Path

FIELDS = (
    "component",
    "url",
    "ref",
    "commit",
    "subpath",
    "vendored-dir",
    "tree-sha256",
    "patched-tree-sha256",
    "patches-sha256",
    "license-sha256",
)
DEFAULT_HEADER = (
    "# Vendored upstream source. DO NOT EDIT any file in this directory.\n"
    "# Local changes go in ota-pulse/patches/mender/ (TASK-S109-004).\n"
    "# Verify with: ota-pulse/scripts/mender-sync.sh --verify\n"
    "# (compares with ota-pulse/mender-upstream.lock).\n"
)
UPSTREAM_KEY_ORDER = (
    "component",
    "url",
    "ref",
    "commit",
    "subpath",
    "license",
    "added-file",
    "added-file-sha256",
    "patches",
    "note",
)
DEFAULT_ADDED_FILE = "LICENSE (copy of upstream repo-root LICENSE at the same commit)"


def sha256_file(path: Path) -> str:
    """Return the hex sha256 of a file's bytes."""
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def patches_hash(patch_dir: Path) -> str:
    """Hash the ``*.patch`` series in ``patch_dir`` (``-`` when there is none).

    The hash is the sha256 of one ``"<sha256-of-file>  <name>\\n"`` line per
    patch, in ascending (byte-wise) file-name order, so renaming, reordering,
    adding, removing or editing a patch all change it.
    """
    if not patch_dir.is_dir():
        return "-"
    names = sorted(
        (p.name for p in patch_dir.iterdir() if p.suffix == ".patch"),
        key=lambda n: n.encode("utf-8"),
    )
    if not names:
        return "-"
    for name in names:
        path = patch_dir / name
        if path.is_symlink() or not path.is_file():
            raise ValueError(
                f"patch {path} must be a regular file (symlinks are refused)"
            )
    payload = "".join(f"{sha256_file(patch_dir / n)}  {n}\n" for n in names)
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()


def read_rows(lock: Path) -> dict[str, list[str]]:
    """Return component -> list of fields (1..10 entries) for every lock row."""
    rows: dict[str, list[str]] = {}
    if not lock.is_file():
        return rows
    for raw in lock.read_text(encoding="utf-8").splitlines():
        if not raw.strip() or raw.startswith("#"):
            continue
        fields = raw.split("\t")
        if len(fields) < 7 or len(fields) > len(FIELDS):
            raise ValueError(f"malformed lock row ({len(fields)} columns): {raw!r}")
        if fields[0] in rows:
            raise ValueError(f"duplicate lock row for {fields[0]}")
        rows[fields[0]] = fields
    return rows


def set_row(lock: Path, values: list[str]) -> None:
    """Replace the row for ``values[0]`` in ``lock`` (append when absent)."""
    if len(values) != len(FIELDS):
        raise ValueError(f"need {len(FIELDS)} fields, got {len(values)}")
    for value in values:
        if not value or "\t" in value or "\n" in value:
            raise ValueError(f"illegal lock field value: {value!r}")
    new_row = "\t".join(values)
    lines = lock.read_text(encoding="utf-8").splitlines() if lock.is_file() else []
    out: list[str] = []
    replaced = False
    for raw in lines:
        if raw.strip() and not raw.startswith("#") and raw.split("\t")[0] == values[0]:
            out.append(new_row)
            replaced = True
        else:
            out.append(raw)
    if not replaced:
        out.append(new_row)
    lock.write_text("\n".join(out) + "\n", encoding="utf-8")


def parse_upstream(text: str) -> tuple[list[str], dict[str, list[str]]]:
    """Split an UPSTREAM file into (leading ``#`` header lines, key -> values)."""
    header: list[str] = []
    keys: dict[str, list[str]] = {}
    in_header = True
    for raw in text.splitlines():
        if in_header and raw.startswith("#"):
            header.append(raw)
            continue
        in_header = False
        if not raw.strip() or raw.startswith("#"):
            continue
        key, _, value = raw.partition(":")
        keys.setdefault(key.strip(), []).append(value.strip())
    return header, keys


def render_upstream(
    old_text: str,
    *,
    component: str,
    url: str,
    ref: str,
    commit: str,
    subpath: str,
    added_license_sha: str,
    patches: list[str],
    tag_object: str,
) -> str:
    """Regenerate an UPSTREAM file, keeping prose (license/notes) from the old one.

    ``added_license_sha`` is ``-`` when no LICENSE was added. ``tag_object`` is
    the annotated-tag object id to record in the note, ``-`` to drop the
    "the tag object is ..." sentence (branch pin / lightweight tag) or ``keep`` to
    leave the note alone. The output is deterministic, so regenerating
    for an unchanged pin reproduces the old file byte for byte.
    """
    # NUL bytes are dropped; planted/duplicate keys are collapsed below (one value per key,
    # notes merged), so a regenerated UPSTREAM always passes the strict parser.
    header, keys = parse_upstream(old_text.replace("\x00", ""))
    if not header:
        header = DEFAULT_HEADER.splitlines()
    keys["component"] = [component]
    keys["url"] = [url]
    keys["ref"] = [ref]
    keys["commit"] = [commit]
    keys["subpath"] = [subpath]
    keys.setdefault("license", ["see the vendored LICENSE"])
    if added_license_sha != "-":
        # Exactly one added-file line, and its path is LICENSE. Any other (planted)
        # added-file line is dropped: the tree hash excludes added files, so each one
        # is a place to hide content from --verify.
        kept = [
            v for v in keys.get("added-file", []) if v.split(" ", 1)[0] == "LICENSE"
        ]
        keys["added-file"] = kept[:1] or [DEFAULT_ADDED_FILE]
        keys["added-file-sha256"] = [f"LICENSE {added_license_sha}"]
    else:
        keys.pop("added-file", None)
        keys.pop("added-file-sha256", None)
    keys["patches"] = [", ".join(patches) if patches else "none"]
    notes = keys.get("note", [])
    fixed: list[str] = []
    for note in notes:
        if tag_object == "keep":
            pass
        elif tag_object != "-":
            note = re.sub(
                r"the tag object is [0-9a-f]{40}",
                f"the tag object is {tag_object}",
                note,
            )
        else:
            note = re.sub(r"the tag object is [0-9a-f]{40};? ?", "", note)
        fixed.append(note)
    if fixed:
        keys["note"] = fixed
    lines = list(header)
    ordered = [k for k in UPSTREAM_KEY_ORDER if k in keys]
    ordered += [k for k in keys if k not in UPSTREAM_KEY_ORDER]
    for key in ordered:
        values = [v for v in keys[key] if v] or [""]
        value = "; ".join(values) if key == "note" else values[0]
        lines.append(f"{key}: {value}")
    return "\n".join(lines) + "\n"


def main(argv: list[str] | None = None) -> int:
    """CLI entry point."""
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    sub = parser.add_subparsers(dest="cmd", required=True)

    p_row = sub.add_parser("row")
    p_row.add_argument("lock", type=Path)
    p_row.add_argument("component")

    p_comp = sub.add_parser("components")
    p_comp.add_argument("lock", type=Path)

    p_set = sub.add_parser("set")
    p_set.add_argument("lock", type=Path)
    p_set.add_argument("fields", nargs=len(FIELDS))

    p_ph = sub.add_parser("patches-hash")
    p_ph.add_argument("patch_dir", type=Path)

    p_sha = sub.add_parser("sha256")
    p_sha.add_argument("file", type=Path)

    p_up = sub.add_parser("render-upstream")
    p_up.add_argument("old", type=Path, help="existing UPSTREAM (may not exist)")
    p_up.add_argument("--component", required=True)
    p_up.add_argument("--url", required=True)
    p_up.add_argument("--ref", required=True)
    p_up.add_argument("--commit", required=True)
    p_up.add_argument("--subpath", required=True)
    p_up.add_argument("--license-sha", default="-")
    p_up.add_argument("--tag-object", default="keep")
    p_up.add_argument("--patch", action="append", default=[])

    args = parser.parse_args(argv)
    try:
        if args.cmd == "row":
            rows = read_rows(args.lock)
            if args.component not in rows:
                return 1
            fields = rows[args.component] + ["?"] * (
                len(FIELDS) - len(rows[args.component])
            )
            print("\t".join(fields))
        elif args.cmd == "components":
            for name in read_rows(args.lock):
                print(name)
        elif args.cmd == "set":
            set_row(args.lock, list(args.fields))
        elif args.cmd == "patches-hash":
            print(patches_hash(args.patch_dir))
        elif args.cmd == "sha256":
            print(sha256_file(args.file))
        else:
            old = args.old.read_text(encoding="utf-8") if args.old.is_file() else ""
            sys.stdout.write(
                render_upstream(
                    old,
                    component=args.component,
                    url=args.url,
                    ref=args.ref,
                    commit=args.commit,
                    subpath=args.subpath,
                    added_license_sha=args.license_sha,
                    patches=list(args.patch),
                    tag_object=args.tag_object,
                )
            )
    except (OSError, ValueError) as exc:
        print(f"mender_lock.py: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
