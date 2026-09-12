#!/usr/bin/env python3
"""TODO-049/S55 static verification for otapulse-boot-script_1.0.bb's
do_configure splice step (no bitbake/sandbox U-Boot build required -- see
TASK-S55-002's completion note for why this is the fallback, not the
primary, verification vehicle).

Mirrors the recipe's Python splice logic BY HAND (kept in sync manually --
there are only ~60 lines of real logic) so the marker-splice and its three
bbfatal contract checks can be exercised without invoking bitbake. Run from
this directory: python3 test_bootcount_net_splice.py

2026-09-12: strengthened per Fable's TASK-S55-002 review -- the original
substring-only checks could be fooled by a comment mentioning "bootpart" or
a second bootargs line clobbering the first. Test cases below cover both
counterexamples Fable gave.
"""
import os
import re
import sys

MARKER = "@@OTAPULSE_BOOTCOUNT_NET@@"
RECOVERYARGS_REF = "$" + "{recoveryargs}"
HERE = os.path.dirname(os.path.abspath(__file__))
FILES = os.path.join(HERE, "files")


class ContractError(Exception):
    pass


def is_code(line):
    return not line.strip().startswith("#")


def assigns(line, varname):
    return re.search(r"\b(setenv|setexpr\.b)\s+" + re.escape(varname) + r"\b", line) is not None


def splice(lines, fragment_lines):
    marker_idx = next((i for i, l in enumerate(lines) if MARKER in l), None)
    if marker_idx is None:
        return list(lines), "no-marker-copy"

    before_lines = [l for l in lines[:marker_idx] if is_code(l)]
    after_lines = [l for l in lines[marker_idx + 1 :] if is_code(l)]

    missing_before = [
        v for v in ("bootpart", "mmcdev", "mmcpart", "scriptaddr")
        if not any(assigns(l, v) for l in before_lines)
    ]
    if missing_before:
        raise ContractError("missing required assignment(s) before marker: " + ", ".join(missing_before))

    reassigns_after = [l for l in after_lines if assigns(l, "bootpart")]
    if reassigns_after:
        raise ContractError("bootpart re-assigned after marker: " + reassigns_after[0].strip())

    bootargs_lines = [l for l in after_lines if assigns(l, "bootargs")]
    if not bootargs_lines or RECOVERYARGS_REF not in bootargs_lines[-1]:
        raise ContractError("last bootargs assignment after marker does not consume recoveryargs")

    return lines[:marker_idx] + fragment_lines + lines[marker_idx + 1 :], "spliced"


def check_balance(text):
    ifs = len(re.findall(r"(?m)^\s*if\b", text))
    fis = len(re.findall(r"(?m)^\s*fi\b", text))
    elifs = len(re.findall(r"\belif\b", text))
    thens = len(re.findall(r";\s*then\b", text))
    return ifs == fis and thens == (ifs + elifs), (ifs, fis, elifs, thens)


def main():
    failures = []

    with open(os.path.join(FILES, "otapulse-bootcount-net.cmd.inc")) as f:
        fragment_lines = f.readlines()

    # Real templates must splice cleanly and balance.
    for fname in ("boot-generic.cmd", "boot-imx8mp.cmd"):
        path = os.path.join(FILES, fname)
        with open(path) as f:
            lines = f.readlines()
        try:
            expanded, mode = splice(lines, fragment_lines)
        except ContractError as e:
            failures.append(f"{fname}: unexpected ContractError: {e}")
            continue
        if mode != "spliced":
            failures.append(f"{fname}: expected 'spliced', got '{mode}'")
        ok, counts = check_balance("".join(expanded))
        if not ok:
            failures.append(f"{fname}: if/fi/then imbalance {counts}")
        print(f"PASS {fname}: {mode}, {len(expanded)} lines, if/fi/then={counts}")

    # Orange Pi's real deployed script has NO marker -- must pass through
    # byte-identical (mode == no-marker-copy, content unchanged). meta-custom
    # lives in the SEPARATE multiboard_yocto repo -- no portable relative
    # path connects the two checkouts; skip gracefully if not found.
    op_path = (
        "/home/krishna/Projects/multiboard_yocto/sources/meta-custom/"
        "recipes-bsp/otapulse-boot-script/files/boot-orange-pi-zero2w.cmd"
    )
    if os.path.exists(op_path):
        with open(op_path) as f:
            op_lines = f.readlines()
        expanded, mode = splice(op_lines, fragment_lines)
        if mode != "no-marker-copy" or expanded != op_lines:
            failures.append(
                "boot-orange-pi-zero2w.cmd: must be an untouched no-marker "
                f"copy (its own inline BUG-310/354 block is pre-existing, "
                f"not yet migrated to the marker in this task) -- got mode={mode}"
            )
        else:
            print(f"PASS boot-orange-pi-zero2w.cmd: no-marker-copy, byte-identical, {len(op_lines)} lines")
    else:
        print("SKIP boot-orange-pi-zero2w.cmd: not found at expected path")

    # Synthetic bad cases -- one per contract check, including the two
    # Fable's review specifically called out as passing the OLD naive
    # substring checks while still being wrong.
    bad_cases = [
        (
            "marker-before-bootpart",
            [f"{MARKER}\n", "setenv bootargs foo" + RECOVERYARGS_REF + "\n"],
        ),
        (
            "bootpart-only-in-comment",
            [
                "# setenv bootpart 2 (placeholder, not real)\n",
                "setenv mmcdev 0\nsetenv mmcpart 1\nsetenv scriptaddr 0x1000\n",
                f"{MARKER}\n",
                "setenv bootargs foo" + RECOVERYARGS_REF + "\n",
            ],
        ),
        (
            "missing-mmcdev-mmcpart-scriptaddr",
            ["setenv bootpart 2\n", f"{MARKER}\n", "setenv bootargs foo" + RECOVERYARGS_REF + "\n"],
        ),
        (
            "bootpart-reassigned-after-marker",
            [
                "setenv bootpart 2\nsetenv mmcdev 0\nsetenv mmcpart 1\nsetenv scriptaddr 0x1000\n",
                f"{MARKER}\n",
                "setenv bootpart 2\n",  # clobbers the fragment's revert
                "setenv bootargs foo" + RECOVERYARGS_REF + "\n",
            ],
        ),
        (
            "no-recoveryargs-consumer",
            [
                "setenv bootpart 2\nsetenv mmcdev 0\nsetenv mmcpart 1\nsetenv scriptaddr 0x1000\n",
                f"{MARKER}\n",
                "setenv bootargs foo\n",
            ],
        ),
        (
            "second-bootargs-clobbers-first",
            [
                "setenv bootpart 2\nsetenv mmcdev 0\nsetenv mmcpart 1\nsetenv scriptaddr 0x1000\n",
                f"{MARKER}\n",
                "setenv bootargs foo" + RECOVERYARGS_REF + "\n",
                "setenv bootargs bar\n",  # second write drops recoveryargs -- must still fail
            ],
        ),
    ]
    for label, lines in bad_cases:
        try:
            splice(lines, fragment_lines)
            failures.append(f"{label}: expected ContractError, none raised")
        except ContractError as e:
            print(f"PASS {label}: correctly rejected ({e})")

    # Good synthetic cases: must NOT raise.
    good_cases = [
        (
            "good-single-bootargs",
            [
                "setenv bootpart 2\nsetenv mmcdev 0\nsetenv mmcpart 1\nsetenv scriptaddr 0x1000\n",
                f"{MARKER}\n",
                "setenv bootargs foo" + RECOVERYARGS_REF + "\n",
            ],
        ),
        (
            "good-test-dash-n-idiom",
            [
                'test -n "${bootpart}" || setenv bootpart 2\n'
                'test -n "${mmcdev}" || setenv mmcdev 0\n'
                'test -n "${mmcpart}" || setenv mmcpart 1\n'
                'test -n "${scriptaddr}" || setenv scriptaddr 0x1000\n',
                f"{MARKER}\n",
                "setenv bootargs foo" + RECOVERYARGS_REF + "\n",
            ],
        ),
        (
            "good-second-bootargs-still-consumes",
            [
                "setenv bootpart 2\nsetenv mmcdev 0\nsetenv mmcpart 1\nsetenv scriptaddr 0x1000\n",
                f"{MARKER}\n",
                "setenv bootargs foo\n",
                "setenv bootargs bar" + RECOVERYARGS_REF + "\n",  # last one wins, and it's fine
            ],
        ),
    ]
    for label, lines in good_cases:
        try:
            _, mode = splice(lines, fragment_lines)
            assert mode == "spliced"
            print(f"PASS {label}: spliced without error")
        except Exception as e:
            failures.append(f"{label}: unexpected failure: {e}")

    if failures:
        print("\nFAILURES:")
        for f in failures:
            print(" -", f)
        sys.exit(1)
    print("\nAll checks passed.")


if __name__ == "__main__":
    main()
