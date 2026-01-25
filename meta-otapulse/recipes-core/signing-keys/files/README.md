# Firmware Signing Public Keys

This directory contains the public keys used to verify firmware signatures on the device.

## IMPORTANT: Example Keys Included

This directory includes **example placeholder keys** (`example-rsa-public.pem` and
`example-ecdsa-public.pem`) for development and testing purposes only.

**DO NOT USE THESE KEYS IN PRODUCTION!**

For production builds, you MUST either:
1. Replace the example keys with your actual production public keys
2. Set `SOC_OTA_VERIFICATION_KEYS` in your `local.conf` to point to your production keys

Example in `local.conf`:
```bitbake
SOC_OTA_VERIFICATION_KEYS = "/path/to/your/production-rsa-public.pem /path/to/your/production-ecdsa-public.pem"
```

## Directory Structure

```
/etc/soc-monitoring/signing-keys/
├── README.md (this file)
├── active/
│   ├── production-rsa-public.pem
│   └── production-ecdsa-public.pem
└── revoked/
    └── (revoked keys moved here)
```

## Key Management

### Active Keys
Keys in the `active/` directory are used for signature verification.
The OTA agent will attempt verification with all active keys.

### Revoked Keys
When a key is compromised or rotated, move it to `revoked/` directory.
The OTA agent will not use keys from this directory.

## Security

- All keys are read-only (permissions: 0444)
- Keys are embedded in read-only filesystem partition
- Multiple keys supported for key rotation
- Private keys NEVER stored on device

## Key Rotation Procedure

1. Generate new key pair on secure signing server
2. Add new public key to `active/` directory
3. Build and deploy new system image
4. After all devices updated, start signing with new key
5. Move old key to `revoked/` after transition period

## Verification Process

The OTA agent performs these steps:
1. Download firmware and signature from server
2. Load all public keys from `active/` directory
3. Attempt verification with each key
4. Install firmware only if signature is valid
5. Reject and log if verification fails

## Support

For questions about firmware signing:
- Documentation: /docs/FIRMWARE_SIGNING_IMPLEMENTATION.md
- Security: /docs/security/OTA_SECURITY_ARCHITECTURE.md
