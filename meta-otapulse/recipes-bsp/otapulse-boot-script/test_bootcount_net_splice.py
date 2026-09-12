#!/usr/bin/env python3
"""TODO-049/S55 static verification for otapulse-boot-script_1.0.bb's
do_configure splice step (no bitbake/sandbox U-Boot build required -- see
TASK-S55-002's completion note for why this is the fallback, not the
primary, verification vehicle).

Mirrors the recipe's Python splice logic exactly (kept in sync by hand --
there are only ~25 lines of real logic) so the marker-splice and its two
bbfatal contract checks can be exercised without invoking bitbake. Run from
this directory: python3 test_bootcount_net_splice.py
"""
import os
import re
import sys

MARKER = "@@OTAPULSE_BOOTCOUNT_NET@@"
HERE = os.path.dirname(os.path.abspath(__file__))
FILES = os.path.join(HERE, "files")


class ContractError(Exception):
    pass


def splice(lines, fragment_lines):
    marker_idx = next((i for i, l in enumerate(lines) if MARKER in l), None)
    if marker_idx is None:
        return list(lines), "no-marker-copy"
    before_marker = "".join(lines[:marker_idx])
    if "bootpart" not in before_marker:
        raise ContractError("marker precedes any 'bootpart' reference")
    after_marker = "".join(lines[marker_idx + 1 :])
    if "${recoveryargs}" not in after_marker:
        raise ContractError("no ${recoveryargs} consumer after marker")
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
    # byte-identical (mode == no-marker-copy, content unchanged).
    # meta-custom lives in the SEPARATE multiboard_yocto repo, not under
    # ota-pulse -- no portable relative path connects the two checkouts.
    # This dev-machine absolute path matches this project's documented real
    # layout (CLAUDE.md); if it's not found (a different checkout root, or
    # this script run outside that layout), skip rather than fail.
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
        print("SKIP boot-orange-pi-zero2w.cmd: not found at expected relative path")

    # Synthetic bad cases: both bbfatal contract checks must actually fire.
    bad_cases = [
        (
            "marker-before-bootpart",
            [f"{MARKER}\n", "setenv bootargs foo${recoveryargs}\n"],
        ),
        (
            "no-recoveryargs-consumer",
            ["setenv bootpart 2\n", f"{MARKER}\n", "setenv bootargs foo\n"],
        ),
    ]
    for label, lines in bad_cases:
        try:
            splice(lines, fragment_lines)
            failures.append(f"{label}: expected ContractError, none raised")
        except ContractError as e:
            print(f"PASS {label}: correctly rejected ({e})")

    # Good synthetic case: must NOT raise.
    good = ["setenv bootpart 2\n", f"{MARKER}\n", "setenv bootargs foo${recoveryargs}\n"]
    try:
        _, mode = splice(good, fragment_lines)
        assert mode == "spliced"
        print("PASS good-synthetic-case: spliced without error")
    except Exception as e:
        failures.append(f"good-synthetic-case: unexpected failure: {e}")

    if failures:
        print("\nFAILURES:")
        for f in failures:
            print(" -", f)
        sys.exit(1)
    print("\nAll checks passed.")


if __name__ == "__main__":
    main()
