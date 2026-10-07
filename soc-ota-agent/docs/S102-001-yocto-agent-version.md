# Proposed Yocto recipe change: real agent version (TASK-S102-001, TODO-011)

The agent Makefile now stamps `conf.Version` from `git describe --tags --always --dirty`
(`Makefile`, `VERSION =`). The Yocto recipe still overrides it on the command line with the
package version `PV`, which is `1.0.0` on every board, so the fleet would keep reporting
`1.0.0`. This repo does not own the recipe; apply the change below in `multiboard_yocto`
(`sources/OTA-Pulse/meta-otapulse/recipes-core/soc-ota-agent/soc-ota-agent_1.0.0.bb`,
`do_compile`, currently `oe_runmake build VERSION="${PV}"`).

```diff
-    # Build using the Makefile
-    oe_runmake build VERSION="${PV}"
+    # Build using the Makefile. The agent version reported in inventory
+    # (otapulse_agent_version / mender_client_version) is the real git
+    # describe of the OTA-Pulse tree, not the fixed recipe PV (1.0.0).
+    # Fall back to PV only when the tree is not a git checkout.
+    AGENT_VERSION="$(git -C ${S} describe --tags --always --dirty 2>/dev/null || echo ${PV})"
+    bbnote "soc-ota-agent version: ${AGENT_VERSION}"
+    oe_runmake build VERSION="${AGENT_VERSION}"
```

Notes:

* `${S}` is the `soc-ota-agent` directory inside the `binaryblack/OTA-Pulse` checkout
  (`EXTERNALSRC`), so `git -C ${S} describe` resolves against that repo's tags (`v0.1.x`).
  Tags must be present in the checkout the build uses (`git fetch --tags`).
* If git refuses the tree for ownership reasons inside the build sandbox, add
  `git config --global --add safe.directory ${S}` (or `GIT_CONFIG_*` env) before the call; the
  `|| echo ${PV}` fallback keeps the build working either way, but then reports `1.0.0` again.
* The `-dirty` suffix appears when tracked files are modified; the untracked `vendor/` does
  not trigger it.
* `soc-ota-tunneld` shares this source tree; no change is needed for it.
* Buildroot (`buildroot-otapulse/package/otapulse/otapulse.mk:170`) passes
  `-X .../conf.Version=$(OTAPULSE_VERSION)` and has the same fixed-version property; out of
  scope for this task, same idea applies if wanted.
