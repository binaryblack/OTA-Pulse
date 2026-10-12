# meta-otapulse-bsp (internal)

Vendored upstream board-integration layers for OTA-Pulse "bootloader compat mode"
(TODO-157 / sprint S109). Internal developer doc, not customer facing.

## What is here

Copies (not submodules) of upstream trees, each with an `UPSTREAM` file
(url, ref, commit, subpath) and a `LICENSE`:

| Directory | Upstream | Pin |
|-----------|----------|-----|
| `core/` | meta-mender `meta-mender-core` | tag `scarthgap-v2026.09`, `d996cfd2e6a7` |
| `qemu/` | meta-mender `meta-mender-qemu` | tag `scarthgap-v2026.09`, `d996cfd2e6a7` |
| `boards/raspberrypi/` | meta-mender-community `meta-mender-raspberrypi` | branch `scarthgap`, SHA `57f10880a166` (no tags exist) |
| `boards/nxp/` | meta-mender-community `meta-mender-nxp` | branch `scarthgap`, SHA `57f10880a166` (no tags exist) |
| `grub-mender-grubenv/` | standalone repo `mendersoftware/grub-mender-grubenv` | `6eabae13042a` (the default `GRUB_MENDER_GRUBENV_REV` of the core pin) |

Full SHAs and tree hashes: `ota-pulse/mender-upstream.lock`. Recompute a tree hash with
`python3 ota-pulse/scripts/mender_tree_hash.py <dir>`.

Each `core/`, `qemu/`, `boards/*` directory is itself a Yocto layer (own `conf/layer.conf`). This
directory is not a layer. Layers are added to a bl-flavor `bblayers.conf` only, never to a native one.

## Rules

- NEVER edit a vendored file. Local changes go in `ota-pulse/patches/mender/` (TASK-S109-004),
  applied on top of the vendored tree.
- `meta-mender-commercial` is NEVER vendored. Nor are `meta-mender-demo` / `meta-mender-extended`
  or the other meta-mender-community sublayers.
- `LICENSE` in `core/`, `qemu/`, `boards/*` is a copy of the upstream repo-root LICENSE (those
  sublayers have none of their own); it is listed as `added-file:` in the tree's `UPSTREAM` and
  excluded from the tree hash. Everything else is byte-identical to upstream.
- Re-pinning goes through the sync tooling (TASK-S109-004), which rewrites the lock.
