# OTAPulse Key Rotation Guide

This guide covers rotating artifact verification keys without bricking devices in the field.

## Overview

OTAPulse supports multiple verification keys via the `ArtifactVerifyKeys` array in `otapulse.conf`. This enables zero-downtime key rotation by deploying the new public key before signing artifacts with the new private key.

## Key Rotation Process

### Phase 1: Deploy New Public Key

1. Generate a new key pair:
   ```bash
   openssl genpkey -algorithm RSA -out new-signing-key.pem -pkeyopt rsa_keygen_bits:3072
   openssl rsa -in new-signing-key.pem -pubout -out new-verify-key.pem
   chmod 600 new-signing-key.pem
   ```

2. Update device config to accept both old and new keys:
   ```json
   {
       "ArtifactVerifyKeys": [
           "/etc/otapulse/verify-key.pem",
           "/etc/otapulse/verify-key-new.pem"
       ]
   }
   ```

3. Deploy the new public key and updated config to all devices via OTA update (signed with the **old** key):
   ```bash
   # Create artifact that installs the new key
   mender-artifact write module-image \
       -T single-file \
       -t your-device \
       -n "deploy-new-verify-key" \
       -f new-verify-key.pem \
       --dest-dir /etc/otapulse/ \
       -o deploy-new-key.mender

   # Sign with OLD key
   mender-artifact sign deploy-new-key.mender -k old-signing-key.pem
   ```

4. Deploy to all devices. Wait until all devices have updated.

### Phase 2: Switch to New Key

5. Start signing new artifacts with the **new** private key:
   ```bash
   mender-artifact sign release-3.0.mender -k new-signing-key.pem
   ```

   Devices will try the old key first (fail), then try the new key (succeed).

### Phase 3: Remove Old Key

6. Once confident all devices are on the new key, deploy a config update removing the old key:
   ```json
   {
       "ArtifactVerifyKeys": [
           "/etc/otapulse/verify-key-new.pem"
       ]
   }
   ```

7. Securely destroy the old private key.

## Key Rotation Timeline

```
Day 0:   Generate new key pair
Day 1:   Deploy new public key to all devices (signed with old key)
Day 7:   Verify all devices received new key
Day 8:   Start signing with new key
Day 14:  Deploy config removing old key
Day 15:  Destroy old private key
```

Adjust the timeline based on your fleet size and update poll intervals.

## How ArtifactVerifyKeys Works

When the agent receives an artifact, it tries each key in order:

1. Try key at index 0 — if signature validates, accept
2. Try key at index 1 — if signature validates, accept
3. ... continue through all keys
4. If no key validates, reject the artifact

This means:
- Artifacts signed with **any** listed key are accepted
- Order doesn't matter for security, but affects verification speed
- Put the most commonly used key first for performance

## Configuration Examples

### Single Key (Default)

```json
{
    "ArtifactVerifyKey": "/etc/otapulse/verify-key.pem"
}
```

### Multiple Keys (During Rotation)

```json
{
    "ArtifactVerifyKeys": [
        "/etc/otapulse/verify-key-current.pem",
        "/etc/otapulse/verify-key-next.pem"
    ]
}
```

### Yocto Configuration

```bash
# In local.conf — primary and secondary keys
SOC_OTA_ARTIFACT_VERIFY_KEY = "/path/to/primary-verify.pem"
SOC_OTA_ARTIFACT_VERIFY_KEY_2 = "/path/to/secondary-verify.pem"
```

### Buildroot Configuration

```bash
BR2_PACKAGE_OTAPULSE_VERIFY_KEY="/path/to/primary-verify.pem"
BR2_PACKAGE_OTAPULSE_VERIFY_KEY_2="/path/to/secondary-verify.pem"
```

## Emergency Key Rotation

If your signing key is compromised:

1. **Immediately** generate a new key pair
2. Sign an emergency update with the **compromised** key that deploys the new public key
3. Deploy to all devices as fast as possible
4. Revoke the compromised key on the server
5. Start signing with the new key
6. Clean up the old key from devices

The window of vulnerability is between compromise and when all devices receive the new key.

## Best Practices

- **Never embed private keys** in firmware images or packages
- **Store private keys** in an HSM or secure key management system
- **Rotate keys annually** or per your security policy
- **Monitor deployment progress** — don't remove old keys until all devices have the new one
- **Test key rotation** on a staging fleet before production
- **Keep a backup** of the current private key until rotation is complete
- **Use different keys** for development and production
