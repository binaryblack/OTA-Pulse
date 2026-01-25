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

```bash
# In your Yocto configuration
OTAPULSE_SERVER_CERTIFICATE = "/etc/otapulse/server-ca.pem"
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
mender-artifact sign artifact.mender \
  --key artifact-signing-private.pem \
  --output artifact-signed.mender
```

### Verification Key Deployment

Include the public key in your device image:

```bash
# In local.conf
OTAPULSE_ARTIFACT_VERIFY_KEY = "/path/to/artifact-signing-public.pem"
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
- [ ] Configure artifact signature verification
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

- [Integration Guide](INTEGRATION.md)
- [Configuration Reference](CONFIGURATION.md)
- [Troubleshooting](TROUBLESHOOTING.md)
