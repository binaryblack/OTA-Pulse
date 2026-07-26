# Firmware Signature Verification

> **Status: Proposed / not yet integrated.** The code below describes a
> designed-but-disabled feature. The Go implementation lives entirely in
> `.disabled` files (`soc-ota-agent/client/signature_verifier.go.disabled`,
> `soc-ota-agent/client/client_update_verified.go.disabled`,
> `soc-ota-agent/conf/signature_config.go.disabled`) and is **not compiled
> into the agent binary today**. `soc-ota-agent/conf/signature-verification.conf.example`
> shows the intended config shape but is not read by the running agent.
> Treat everything here as a design doc / integration plan, not a description
> of shipped behavior. This supersedes and replaces the older
> `QUICK_REFERENCE.md`, `docs/SIGNATURE_VERIFICATION_INTEGRATION.md`, and
> `tests/TESTING_GUIDE.md`, which described the same unshipped feature in three
> separate, partially-inconsistent places.
>
> Note: production signing/key-rotation for **already-shipped** artifact
> signing (`SOC_OTA_SIGNATURE_VERIFICATION`, `SOC_OTA_SIGNING_KEY`,
> `ArtifactVerifyKeys`) is a different, currently-integrated mechanism — see
> [../../docs/SECURITY.md](../../docs/SECURITY.md). This document covers the
> proposed *runtime download-time* firmware signature verification layer,
> which is separate and not yet wired in.

## What This Would Add

A verification layer between "download firmware" and "install firmware":
firmware is downloaded, its detached signature is downloaded, and the agent
verifies the signature against locally-stored public keys before allowing
install. Verification failures delete the firmware, log to
`/var/log/soc-ota-security.log`, and (after a configurable number of
consecutive failures) lock the device out of automatic updates until an
administrator intervenes.

```
1. Client requests update from server
2. Download firmware binary        (GET /api/devices/v1/download)
3. Download detached signature      (GET /api/devices/v1/download.sig)
4. Load public keys from device     (keys_directory, e.g. /etc/otapulse/signing-keys/active)
5. Verify signature (SHA-256 + RSA/ECDSA, check key fingerprint)
     VALID   → continue normal install flow
     INVALID → delete firmware, log error, alert server, increment failure counter, check lockdown
```

## Proposed Integration Points

### Update client wrapper

```go
// Before (standard update, what ships today):
upclient := client.NewUpdate()
image, imageSize, err := upclient.FetchUpdate(ac, updateURI, 0)

// Proposed (with verification):
upclient, err := client.NewUpdateWithVerification(keysDirectory, true /* enable */)
image, imageSize, err := upclient.FetchUpdateWithVerification(ac, updateURI, 0)
```

`NewUpdateWithVerification` / `FetchUpdateWithVerification` are defined in
`client/client_update_verified.go.disabled` — not currently compiled.

### Config shape (proposed)

```json
{
  "Security": {
    "VerifySignatures": true,
    "RejectUnsigned": true,
    "KeysDirectory": "/etc/otapulse/signing-keys/active",
    "MaxVerificationFailures": 5,
    "AlertOnFailure": true,
    "DeleteOnFailure": true,
    "LockdownDisableAutoUpdate": true
  }
}
```

This mirrors `conf/signature_config.go.disabled`'s `SignatureConfig` struct
and `conf/signature-verification.conf.example`. To actually enable this,
`SignatureConfig` would need to be added to the live `MenderConfig` in
`conf/config.go`, and `app/mender.go`/`app/standalone.go` would need the
verifier wired into their fetch paths (both currently call the plain
`client.NewUpdate()` / `FetchUpdate()` path only).

## Quick Reference (for developing/testing this feature)

### Generate test keys

```bash
# RSA-4096
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:4096 -out private-key.pem
openssl rsa -in private-key.pem -pubout -out public-key.pem

# ECDSA-P384
openssl ecparam -name secp384r1 -genkey -out private-key.pem
openssl ec -in private-key.pem -pubout -out public-key.pem
```

### Sign and verify firmware manually (OpenSSL only — doesn't require the disabled Go code)

```bash
# Sign
openssl dgst -sha256 -sign private-key.pem -out firmware.sig firmware.bin
base64 firmware.sig > firmware.sig.b64

# Check a key pair matches
openssl rsa -in private-key.pem -pubout | diff - public-key.pem
```

### Run the existing test scripts

`soc-ota-agent/tests/test-signature-verification.sh` and `setup-testing.sh`
are standalone OpenSSL-based shell scripts (key/sign/verify workflow) and run
today independent of the disabled Go code:

```bash
cd soc-ota-agent/tests
./setup-testing.sh
./test-signature-verification.sh
```

`test-integration.sh`, however, **will fail** as shipped — its first step
(`cd soc-ota-agent/signature && go test -v`) references a `signature/` Go
package that doesn't exist in this repo (the real, disabled code lives under
`client/` and `conf/` as `*.go.disabled`). Skip that step, or point it at the
`.disabled` files (renamed to `.go`) once you're actively re-enabling the
feature.

## Re-enabling This Feature (checklist)

- [ ] Rename `client/signature_verifier.go.disabled` → `client/signature_verifier.go` (and the `client_update_verified.go.disabled` / `conf/signature_config.go.disabled` counterparts)
- [ ] Add `Security SignatureConfig` to the live `conf.MenderConfig` in `conf/config.go`
- [ ] Wire `NewUpdateWithVerification`/`FetchUpdateWithVerification` into `app/standalone.go`'s `DoStandaloneInstall` and `app/mender.go`'s `FetchUpdate`
- [ ] Point `keys_directory` at the real on-device path (see `meta-otapulse/recipes-core/signing-keys/files/README.md` for current key layout — note it does **not** use `active/`/`revoked/` subdirectories, so update the disabled code/config's default path accordingly)
- [ ] Fix `test-integration.sh`'s reference to a nonexistent `signature/` package
- [ ] Build a Yocto image with public keys embedded, deploy, and test both valid and tampered-firmware cases end to end
- [ ] Document the final config keys in [../../docs/CONFIGURATION.md](../../docs/CONFIGURATION.md) once shipped

## Performance (measured against the standalone OpenSSL scripts, not the disabled Go path)

| Operation | Time |
|-----------|------|
| Load RSA-4096 key | ~1-2ms |
| Load ECDSA-P384 key | ~1ms |
| Hash 10MB firmware | ~50-100ms |
| Verify RSA signature | ~5-10ms |
| Verify ECDSA signature | ~2-5ms |

Total added time per update, if enabled, would be well under 1 second —
negligible next to firmware download time.
