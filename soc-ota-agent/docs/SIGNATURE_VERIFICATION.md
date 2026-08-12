# Firmware Signature Verification

> **Status: shipped.** Artifact signature verification and consecutive-failure
> lockdown run in `installer/installer.go` (`ReadHeaders`) and
> `installer/signature_lockdown.go`, compiled into the agent binary today.
> This document previously described a different, never-integrated design
> (see "Superseded design" below) — that design is gone, not just disabled.

## What actually ships

Every artifact install — daemon-managed or standalone CLI — funnels through
`installer.ReadHeaders`, called from `device.DeviceManager.ReadArtifactHeaders`
and `app.doStandaloneInstallStatesDownload`. When the device is configured
with artifact verification keys (`SOC_OTA_SIGNATURE_VERIFICATION=1` at build
time, see `meta-otapulse/recipes-core/soc-ota-agent/soc-ota-agent_1.0.0.bb`),
`ReadHeaders`:

1. Refuses to even open the artifact if a prior lockdown is active
   (`checkNotLockedDown`).
2. Opens the artifact with `areader.NewReaderSigned`, which requires an
   embedded signature block — an artifact with no signature at all fails
   here with `"expecting signed artifact, but no signature file found"`.
3. If a signature block is present, `VerifySignatureCallback` checks it
   against every configured key via the vendored
   `mender-artifact/artifact.NewPKIVerifier` — the same mechanism that has
   always verified artifacts here (`SOC_OTA_SIGNING_KEY`,
   `SOC_OTA_VERIFY_KEY_FILES`). Nothing about signature cryptography itself
   is new.
4. GAP-SEC-F2 (new): every failure at step 2 or step 3 — missing signature,
   or a signature that doesn't verify against any configured key —
   increments a consecutive-failure counter. `DefaultMaxVerificationFailures`
   (5) failures within `DefaultFailureWindow` (24h) activates lockdown:
   further `ReadHeaders` calls are refused outright until an operator calls
   `installer.ResetSignatureLockdown()`. A single success resets the
   counter to zero.

This closes the gap GAP-SEC-F1 already made fail-closed
(`conf.MenderConfig.GetVerificationKeys`, `conf/config.go:385-421`, which
hard-errors if `RequireArtifactVerification` is set but no keys resolve) —
F1 stops a misconfigured device from silently accepting unsigned installs;
F2 stops a device from being hammered with an unlimited number of forged/
tampered artifacts.

Lockdown state is in-memory and per-process — it resets on agent restart.
Persisting it across restarts was intentionally left out of this pass: the
fail-closed key-loading check in `GetVerificationKeys` still runs on every
start regardless, and restart-persistence would need a real on-disk store
and a threat model for who can trigger agent restarts, which wasn't asked
for here.

## Operating it

- Check whether lockdown is active: `installer.SignatureLockdownActive()` /
  `installer.SignatureFailureCount()`.
- Clear it after investigating: `installer.ResetSignatureLockdown()`.
  Neither is currently exposed over a CLI flag or D-Bus method — wiring an
  operator-facing reset path is a reasonable follow-up but wasn't in scope
  here (this pass added the safety property itself, not an admin UI for it).
- Both `DefaultMaxVerificationFailures` and `DefaultFailureWindow` are Go
  constants, not runtime config — there is no `otapulse.conf` key for them.
  Making them configurable would mean extending the `otapulse.conf.in`
  template + `soc-ota-agent_1.0.0.bb` injection logic the same way
  `RequireArtifactVerification` was added (see `git log` for that commit) —
  straightforward if a real need for per-fleet tuning shows up, not added
  speculatively.

## Superseded design (historical — do not resurrect this)

An earlier design lived in three `.disabled` Go files
(`client/signature_verifier.go.disabled`, `client/client_update_verified.go.disabled`,
`conf/signature_config.go.disabled`, now deleted) plus a config example and
three shell scripts (`conf/signature-verification.conf.example`,
`tests/setup-testing.sh`, `tests/test-signature-verification.sh`,
`tests/test-integration.sh`, also deleted). It modeled firmware as a raw
binary downloaded from a URL plus a **detached signature file** downloaded
from `<url>.sig`, verified via a `github.com/mendersoftware/mender/signature`
package.

That package **does not exist** — not in `go.mod`, not in `go.sum`, not
anywhere in the module cache. `docs/SIGNATURE_VERIFICATION.md`'s own
checklist even flagged `test-integration.sh` as broken for the same reason
before this rewrite. The whole download-and-verify-detached-signature model
is also architecturally foreign to this system: OTA-Pulse artifacts are a
tar-based container with the signature embedded in the manifest, verified as
part of reading that container (`areader`), not a separate file fetched over
plain HTTP. There was no viable path to "enable" that design — it would have
needed a real signature package and a rewritten download protocol, not a
rename from `.disabled` to `.go`.

If a future need arises for something that design was reaching for
(e.g. downloading and verifying firmware payloads that live outside the
OTA-Pulse artifact format entirely), design it against the real
`mender-artifact` primitives and the real update flow from scratch — do not
resurrect these deleted files as a starting point.
