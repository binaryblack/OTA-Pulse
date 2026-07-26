# OTAPulse Security Guide

This guide covers security best practices for deploying OTAPulse in production environments.

## Overview

OTAPulse provides multiple layers of security:

1. **Transport Security** - TLS encrypted communications
2. **Artifact Signing** - Cryptographic verification of firmware
3. **Device Authentication** - Mutual TLS and token-based auth
4. **Secure Boot** - Platform integrity verification

## Transport Security

### TLS Configuration

All communications between devices and the OTAPulse server use TLS 1.2+.

**Server Certificate Verification:**

```json
{
  "ServerURL": "https://ota.example.com",
  "ServerCertificate": "/etc/otapulse/ca-certificates.crt"
}
```

For private CAs, include your CA certificate in the device image.

### Certificate Pinning

For enhanced security, pin the server certificate:

```json
{
  "ServerCertificate": "/etc/otapulse/server-ca.pem"
}
```

## Artifact Signing

### Key Generation

Generate signing keys (keep private key secure!):

```bash
# RSA 4096-bit (recommended for compatibility)
openssl genpkey -algorithm RSA -out artifact-signing-private.pem \
  -pkeyopt rsa_keygen_bits:4096
openssl rsa -in artifact-signing-private.pem -pubout \
  -out artifact-signing-public.pem

# ECDSA P-384 (recommended for performance)
openssl ecparam -name secp384r1 -genkey -noout \
  -out artifact-signing-private.pem
openssl ec -in artifact-signing-private.pem -pubout \
  -out artifact-signing-public.pem
```

### Signing Artifacts

Sign your firmware artifacts during the build process:

```bash
mender-artifact write rootfs-image \
  -t your-device-type \
  -n "release-1.0.1" \
  -f your-rootfs.ext4 \
  --key artifact-signing-private.pem \
  -o artifact-signed.mender
```

Or enable automatic signing during Yocto build:
```bash
# In local.conf
SOC_OTA_SIGNING_KEY = "/path/to/artifact-signing-private.pem"
```

### Verification Key Deployment

Include the public key in your device image:

```bash
# In local.conf
SOC_OTA_VERIFICATION_KEYS = "/path/to/artifact-signing-public.pem"
```

Or via the signing-keys recipe:

```
meta-otapulse/
└── recipes-core/signing-keys/files/
    └── production-rsa-public.pem   # Add your public key here
```

### Enabling Verification

Configure the agent to require signed artifacts:

```json
{
  "ArtifactVerifyKey": "/etc/otapulse/artifact-verify-key.pem"
}
```

For key rotation, use the list form:
```json
{
  "ArtifactVerifyKeys": [
    "/etc/soc-monitoring/signing-keys/active/production-rsa-public.pem",
    "/etc/soc-monitoring/signing-keys/active/production-ecdsa-public.pem"
  ]
}
```

Enable build-time verification enforcement:
```bash
# In local.conf
SOC_OTA_SIGNATURE_VERIFICATION = "1"
```

`ArtifactVerifyKey` and `ArtifactVerifyKeys` are mutually exclusive — setting both is a
configuration error. The agent verifies the signature embedded in the `.otapulse` artifact
itself (there is no separate `.sig` download); it tries each configured key in turn and
installs only if one of them verifies.

By default, a *signed* artifact installs with a warning when no verification key is
configured. To make a missing or unreadable key a hard install failure instead of a silent
downgrade to unverified installs, set:

```json
{
  "RequireArtifactVerification": true
}
```

### Key Rotation Procedure

Because multiple verification keys can be active at once, rotation is a rolling operation
with no fleet downtime:

1. Generate the new key pair on the secure signing host (see [Key Generation](#key-generation)).
2. Add the new **public** key alongside the current one under
   `/etc/soc-monitoring/signing-keys/active/` — via the `signing-keys` recipe, or by adding
   its path to `ArtifactVerifyKeys`.
3. Build and deploy a system image carrying both keys, and wait for the whole fleet to take it.
4. Only then start signing new artifacts with the new private key.
5. After the transition period, move the old public key to
   `/etc/soc-monitoring/signing-keys/revoked/` (and drop it from `ArtifactVerifyKeys`) in the
   next image.

Public keys on the device are read-only (`0444`) and embedded in the read-only rootfs, so
rotating them requires a firmware update by design. Private keys are never stored on devices.

## Device Authentication

### Tenant Token

The tenant token identifies your organization:

```json
{
  "TenantToken": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9..."
}
```

Keep tenant tokens confidential. Rotate if compromised.

### Device Identity

Each device has a unique identity derived from hardware attributes:

```bash
# Default identity script
/usr/share/otapulse/identity/otapulse-device-identity
```

Recommended identity attributes:
- CPU serial number
- MAC address (immutable)
- Secure element ID
- TPM-based identity

### Pre-authorized Devices

For controlled deployments, pre-authorize device identities on the server before deployment.

## Secure Boot Integration

### U-Boot Verified Boot

The layer supports U-Boot verified boot:

1. Sign your kernel and device tree
2. Configure U-Boot to verify signatures
3. Lock down U-Boot environment

```bash
# U-Boot configuration
CONFIG_FIT=y
CONFIG_FIT_SIGNATURE=y
CONFIG_FIT_VERBOSE=y
```

### Chain of Trust

```
ROM → U-Boot SPL → U-Boot → Kernel → Rootfs → Application
        ↓            ↓         ↓        ↓
    (signed)    (verified) (verified) (verified)
```

### systemd-boot EFI ABA (Radxa CM5 / RK3588S)

Boards booting via systemd-boot in EFI ABA mode (see
[integration/rockchip-integration.md](integration/rockchip-integration.md#radxa-cm5-rk3588s-slot-switching))
use `switch-boot-slot.sh`'s systemd-boot method rather than a U-Boot
`boot.scr` for slot selection. The chain-of-trust diagram above still applies;
only the slot-selection step at the U-Boot→kernel boundary differs from the
generic `boot.scr` flow.

### Signing-Key Rotation Workflow

The signing-keys recipe (`meta-otapulse/recipes-core/signing-keys/`) supports
multiple simultaneously-active verification keys specifically to allow
rotation without an update gap:

1. Add the new public key alongside the current one via
   `SOC_OTA_VERIFICATION_KEYS` (space-separated list) — both old and new keys
   verify successfully during the transition.
2. Rebuild and deploy an image carrying both keys to your fleet.
3. Once the fleet has the new key, start signing artifacts with the new
   private key only.
4. After a full rollout cycle, drop the old key from
   `SOC_OTA_VERIFICATION_KEYS` and rebuild.

See [signing-keys/files/README.md](../meta-otapulse/recipes-core/signing-keys/files/README.md)
for the on-device `active/`/`revoked/` key layout this produces.

## Partition Layout Security

### A/B Partition Protection

- Mark inactive partition as read-only
- Verify partition integrity before activation
- Automatic rollback on verification failure

### Data Partition

Sensitive data should be stored on a separate encrypted data partition:

```bash
# In WKS file
part /data --ondisk mmcblk0 --size 512M --fstype=ext4 --label data
```

Consider dm-crypt/LUKS for encryption.

## Production Checklist

### Before Production Deployment

- [ ] Generate unique signing keys for production
- [ ] Store private signing key in HSM or secure vault
- [ ] Configure artifact signature verification (`SOC_OTA_SIGNATURE_VERIFICATION = "1"`)
- [ ] Use TLS with certificate verification
- [ ] Implement secure device identity
- [ ] Enable secure boot chain (if supported)
- [ ] Disable debug interfaces (UART, JTAG)
- [ ] Remove development credentials
- [ ] Set appropriate poll intervals
- [ ] Configure logging appropriately

### Key Management

| Key Type | Storage | Rotation |
|----------|---------|----------|
| Signing Private Key | HSM / Secure Vault | Annually |
| Signing Public Key | Device Image | With private key |
| Tenant Token | Build System | On compromise |
| Server TLS Cert | Device Image | Before expiry |

### Monitoring

Monitor for security events:
- Failed authentication attempts
- Signature verification failures
- Unexpected rollbacks
- Unusual update patterns

## Incident Response

### Compromised Signing Key

1. Revoke the compromised key on the server
2. Generate new signing keys
3. Deploy public key update to devices
4. Re-sign and deploy all artifacts

### Compromised Tenant Token

1. Revoke token on server
2. Generate new tenant token
3. Deploy configuration update to devices
4. Monitor for unauthorized access attempts

### Device Compromise

1. Revoke device authentication
2. Investigate root cause
3. Deploy patched firmware
4. Consider fleet-wide update if systemic

## Further Resources

- [Integration Guide](integration/README.md)
- [Configuration Reference](CONFIGURATION.md)
- [Troubleshooting](TROUBLESHOOTING.md)
