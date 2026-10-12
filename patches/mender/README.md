# patches/mender (internal)

Local changes to the vendored upstream board-integration layers under
`meta-otapulse-bsp/`. Internal developer doc, not customer facing.

A vendored tree is never edited by hand. Every local difference from upstream
lives here as a patch, and `scripts/mender-sync.sh --sync` re-applies the series
on top of a fresh, pristine upstream tree. A hand edit is therefore lost on the
next sync, and `scripts/mender-sync.sh --verify` fails on it straight away.

## Layout

```
patches/mender/
  meta-mender-core/            -> meta-otapulse-bsp/core
  meta-mender-qemu/            -> meta-otapulse-bsp/qemu
  meta-mender-raspberrypi/     -> meta-otapulse-bsp/boards/raspberrypi
  meta-mender-nxp/             -> meta-otapulse-bsp/boards/nxp
  grub-mender-grubenv/         -> meta-otapulse-bsp/grub-mender-grubenv
```

The directory name is the component name from `mender-upstream.lock`. Each
directory holds a `.gitkeep` and zero or more `NNNN-description.patch` files.

## Rules

- Name: `NNNN-description.patch`. Four digits, then letters, digits, `.`, `_`, `+` or `-`.
  Patches apply in sorted name order. Any other name is refused.
- Paths: relative to the root of the component tree, `a/` and `b/` prefixes (plain
  `git diff` output, `-p1`). So `a/conf/layer.conf`, not `a/meta-mender-core/conf/layer.conf`.
- A patch must not touch `UPSTREAM` or `LICENSE` at the tree root. Those are managed by the
  sync tool and are not part of the hashed tree.
- One concern per patch. Put the reason in the patch header (the text before the first `diff`).
- Patch files are plain text that describes a change to the upstream file. They carry the
  licence of the file they patch (for example GPL-2.0-or-later for a U-Boot file). Do not
  paste code from anywhere else into a patch. Keep the upstream copyright header.
- Never vendor or patch in anything from `meta-mender-commercial`.

## Create a patch

1. Get a pristine copy of the tree at the pinned commit. The pin is in `mender-upstream.lock`.
   ```
   scripts/mender-sync.sh --sync <component> <pinned-ref> --dry-run      # proves the pin still fetches
   git clone https://github.com/mendersoftware/meta-mender /tmp/up && git -C /tmp/up checkout <commit>
   cp -a /tmp/up/<subpath> /tmp/work                                      # or the whole repo for grub-mender-grubenv
   ```
2. Make a scratch git repo of just that tree, edit, and diff:
   ```
   cd /tmp/work && git init -q && git add -A && git -c user.name=x -c user.email=x@x commit -qm base
   $EDITOR recipes-bsp/u-boot/some.bbappend
   git diff > /path/to/ota-pulse/patches/mender/<component>/0001-short-description.patch
   ```
   `git diff` gives the `a/` and `b/` prefixes the tool expects. Add a header describing the
   change at the top of the file.
3. Apply it to the vendored tree through the tool, never by hand:
   ```
   scripts/mender-sync.sh --sync <component> <pinned-ref>
   scripts/mender-sync.sh --verify
   git diff --stat        # shows the vendored file change, the lock and the manifest
   ```
   The lock then records two hashes: `tree-sha256` (pristine upstream, reproducible from a fresh
   clone) and `patched-tree-sha256` (what is on disk). `--verify` checks the disk tree against
   the second one and `--verify --online` re-derives both from upstream.

## Regenerate a patch after an upstream bump

`--sync <component> <new-ref>` runs `git apply --check` for every patch against the new upstream
tree in a temp copy first. If one no longer applies it stops with
`regenerate the patch, never force`, and the vendored tree, the lock and the manifest stay
exactly as they were. Then:

1. Check out the new upstream commit as in step 1 above.
2. Apply the old patches in order with `git apply --3way` (or by hand) in the scratch repo, fix
   the conflicts, commit the resolved base plus the series, and run `git diff <base> HEAD`
   per patch to write the new `NNNN-*.patch` files.
3. Replace the old files in `patches/mender/<component>/` and run `--sync` again.

Do not loosen the check (`--reject`, `-C0`, `--unidiff-zero`, `patch -F`) to make a stale patch
go through. A fuzzy apply can land the change in the wrong place and nobody would notice.
