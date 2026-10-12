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

## Maintenance (`scripts/mender-sync.sh`)

This script is the only thing that touches the network for these trees, and only when a person
or CI runs it. Builds never fetch upstream.

| Command | What it does |
|---------|--------------|
| `scripts/mender-sync.sh --status` | Pinned vs newest upstream tag (`scarthgap-v*`) or branch head, over https with no credentials. |
| `scripts/mender-sync.sh --sync <component> <tag\|branch\|40-hex sha>` | Fetch, verify the commit, re-vendor, apply `patches/mender/<component>/*.patch`, rewrite the lock, the tree's `UPSTREAM` and its manifest. A patch that no longer applies stops the sync with `regenerate the patch, never force` and changes nothing. Syncing to the pinned ref is a no-op. `--dry-run` writes nothing. |
| `scripts/mender-sync.sh --verify` | Offline. Disk trees vs the lock (patched hash when patches exist), patch series vs lock, `UPSTREAM` vs lock, added `LICENSE` sha256, no path containing `commercial`, no unlocked directory. Names the differing files. |
| `scripts/mender-sync.sh --verify --online` | Also re-derives the pristine and patched hashes from a fresh clone of the pinned commit and `cmp`s each added `LICENSE` with the upstream repo-root `LICENSE`. |

The lock records two hashes per component. `tree-sha256` is the pristine upstream subtree and can
be reproduced from a fresh clone. `patched-tree-sha256` is the tree on disk after the patch series
(`-` when there are no patches). See the header of `mender-upstream.lock`. Per-file manifests in
`mender-upstream.manifests/` let `--verify` say which file differs. `patches/mender/README.md`
explains how to write and regenerate patches.

`--verify` also runs in the pre-commit hook (installed by `./scripts/setup.sh hooks` in the parent
repo) whenever a guarded path is staged: `meta-otapulse-bsp/`, `mender-upstream.lock`,
`mender-upstream.manifests/`, `patches/mender/` or the sync tooling. Source of the hook:
`scripts/hooks/mender-verify-hook.sh`.
