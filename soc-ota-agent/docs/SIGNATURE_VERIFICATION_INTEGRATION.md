# Firmware Signature Verification - OTA Agent Integration Guide

## Overview

This document describes how to integrate firmware signature verification into the SoC OTA Agent.

## Integration Points

### 1. Update Client Integration

The signature verification is integrated into the `UpdateClient` through the `UpdateClientWithVerification` wrapper.

#### Before (Standard Update):
```go
upclient := client.NewUpdate()
image, imageSize, err := upclient.FetchUpdate(ac, updateURI, 0)
```

#### After (With Verification):
```go
upclient, err := client.NewUpdateWithVerification(
    "/etc/soc-monitoring/signing-keys/active",
    true, // enable verification
)
if err != nil {
    return errors.Wrap(err, "failed to initialize update client with verification")
}

image, imageSize, err := upclient.FetchUpdateWithVerification(ac, updateURI, 0)
// Firmware is now verified before installation
```

### 2. Required Changes in app/standalone.go

```go
// In DoStandaloneInstall function, around line 56

func DoStandaloneInstall(device *dev.DeviceManager, updateURI string,
	clientConfig client.Config,
	stateExec statescript.Executor, rebootExitCode bool) error {

	var image io.ReadCloser
	var imageSize int64
	var err error
	var upclient client.Updater

	log.Debug("Starting device update.")

	if strings.HasPrefix(updateURI, "http:") ||
		strings.HasPrefix(updateURI, "https:") {
		log.Infof("Performing remote update from: [%s].", updateURI)

		var ac *client.ApiClient
		ac, err = client.NewApiClient(clientConfig)
		if err != nil {
			return errors.New("Can not initialize client for performing network update.")
		}

		// CHANGED: Use UpdateClientWithVerification instead of NewUpdate()
		upclient, err = client.NewUpdateWithVerification(
			"/etc/soc-monitoring/signing-keys/active",
			true, // enable verification
		)
		if err != nil {
			return errors.Wrap(err, "failed to initialize update client with verification")
		}

		log.Debug("Client initialized with signature verification. Start downloading image.")

		// CHANGED: Use FetchUpdateWithVerification
		upClientVerified, ok := upclient.(*client.UpdateClientWithVerification)
		if ok {
			image, imageSize, err = upClientVerified.FetchUpdateWithVerification(ac, updateURI, 0)
		} else {
			// Fallback to standard fetch
			image, imageSize, err = upclient.FetchUpdate(ac, updateURI, 0)
		}

		log.Debugf("Image downloaded: %d [%v] [%v]", imageSize, image, err)
	} else {
		// perform update from local file
		log.Infof("Start updating from local image file: [%s]", updateURI)
		image, imageSize, err = installer.FetchUpdateFromFile(updateURI)

		log.Debugf("Fetching update from file results: [%v], %d, %v", image, imageSize, err)
	}

	// Rest of the function remains the same...
}
```

### 3. Required Changes in app/mender.go

```go
// Add verifier field to Mender struct
type Mender struct {
	// ... existing fields ...
	
	// Signature verifier for firmware validation
	signatureVerifier *client.SignatureVerifier
}

// In NewMender function, initialize verifier
func NewMender(config *conf.MenderConfig, deviceManager *dev.DeviceManager) (*Mender, error) {
	// ... existing code ...

	// Initialize signature verifier if enabled
	var verifier *client.SignatureVerifier
	if config.Security != nil && config.Security.VerifySignatures {
		verifier, err = client.NewSignatureVerifier(
			config.Security.KeysDirectory,
			true,
		)
		if err != nil {
			log.Errorf("Failed to initialize signature verifier: %v", err)
			// Decide whether to fail or continue without verification
			if config.Security.RejectUnsigned {
				return nil, err
			}
		}
	}

	m := &Mender{
		// ... existing fields ...
		signatureVerifier: verifier,
	}

	return m, nil
}

// In FetchUpdate method
func (m *Mender) FetchUpdate(url string) (io.ReadCloser, int64, error) {
	if m.signatureVerifier != nil && m.signatureVerifier.verificationEnabled {
		// Use verified fetch
		upclient, err := client.NewUpdateWithVerification(
			m.signatureVerifier.keysDirectory,
			true,
		)
		if err != nil {
			return nil, -1, err
		}
		return upclient.FetchUpdateWithVerification(
			m.download,
			url,
			m.GetRetryPollInterval(),
		)
	}

	// Standard fetch without verification
	return m.updater.FetchUpdate(m.download, url, m.GetRetryPollInterval())
}
```

### 4. Configuration File Changes

Add to `conf/mender.conf`:

```json
{
  "ServerURL": "https://your-server.com",
  "Security": {
    "VerifySignatures": true,
    "RejectUnsigned": true,
    "KeysDirectory": "/etc/soc-monitoring/signing-keys/active",
    "MaxVerificationFailures": 5,
    "AlertOnFailure": true,
    "DeleteOnFailure": true,
    "LockdownDisableAutoUpdate": true
  }
}
```

## Verification Flow

```
┌─────────────────────────────────────────┐
│  1. Client requests update from server  │
└──────────────┬──────────────────────────┘
               │
               ▼
┌─────────────────────────────────────────┐
│  2. Download firmware binary            │
│     GET /api/devices/v1/download        │
└──────────────┬──────────────────────────┘
               │
               ▼
┌─────────────────────────────────────────┐
│  3. Download signature                  │
│     GET /api/devices/v1/download.sig    │
└──────────────┬──────────────────────────┘
               │
               ▼
┌─────────────────────────────────────────┐
│  4. Load public keys from device        │
│     /etc/soc-monitoring/signing-keys/   │
└──────────────┬──────────────────────────┘
               │
               ▼
┌─────────────────────────────────────────┐
│  5. Verify signature                    │
│     - Calculate firmware SHA-256        │
│     - Verify with RSA/ECDSA             │
│     - Check key fingerprint             │
└──────────────┬──────────────────────────┘
               │
        ┌──────┴──────┐
        │             │
     VALID         INVALID
        │             │
        ▼             ▼
┌──────────────┐  ┌──────────────────────────┐
│ 6a. Install  │  │ 6b. REJECT               │
│     Continue │  │  - Delete firmware       │
│     normal   │  │  - Log error             │
│     install  │  │  - Alert server          │
│     flow     │  │  - Increment counter     │
│              │  │  - Check lockdown        │
└──────────────┘  └──────────────────────────┘
```

## Error Handling

### Verification Failure
When signature verification fails:
1. Firmware download is **deleted** immediately
2. Error logged to `/var/log/soc-ota-security.log`
3. Failure counter **incremented**
4. Alert sent to server (if configured)
5. Device **remains on current version**

### Security Lockdown
After 5 consecutive failures (configurable):
1. Automatic updates **disabled**
2. Manual intervention **required**
3. Alert sent to administrators
4. Logged to system syslog

To reset lockdown:
```bash
# Via CLI (requires implementation)
soc-ota-agent reset-lockdown

# Or via API call from server
curl -X POST https://device-ip/api/reset-lockdown \
  -H "Authorization: Bearer <admin-token>"
```

## Testing

### Test Verified Update
```bash
# 1. Generate test keys (on development machine)
cd soc-ota-agent/tests
./test-signature-verification.sh

# 2. Copy public key to device
scp test-data/keys/rsa/public.pem \
    root@device:/etc/soc-monitoring/signing-keys/active/test-rsa-public.pem

# 3. Sign firmware on server
# (Use server UI to sign firmware)

# 4. Deploy to device
# (Trigger OTA update)

# 5. Check logs on device
ssh root@device
journalctl -u soc-ota-agent -f

# Expected output:
# INFO: Downloading firmware from: https://...
# INFO: Downloaded firmware: 10485760 bytes
# INFO: Downloading signature from: https://....sig
# INFO: Downloaded signature: 512 bytes
# INFO: Verifying firmware signature (size: 10485760 bytes)
# INFO: ✓ Signature verified successfully: RSA-4096 (sha256:abc123...)
# INFO: Installing firmware...
```

### Test Rejection of Invalid Signature
```bash
# 1. Modify firmware after signing (simulate tampering)
# 2. Deploy to device
# 3. Check logs

# Expected output:
# ERROR: ✗ Signature verification failed: invalid signature
# WARN: Verification failure count: 1/5
# ERROR: Installation rejected
# INFO: Firmware deleted
```

## Deployment Checklist

- [ ] Build Yocto image with public keys embedded
- [ ] Deploy public keys to `/etc/soc-monitoring/signing-keys/active/`
- [ ] Set proper permissions: `chmod 444 *.pem`
- [ ] Configure `/etc/soc-monitoring/agent.conf`
- [ ] Enable `verify_signatures = true`
- [ ] Test with signed firmware
- [ ] Test with invalid signature (should reject)
- [ ] Test with missing signature (should reject if `reject_unsigned=true`)
- [ ] Monitor `/var/log/soc-ota-security.log`
- [ ] Set up alerting for verification failures

## Troubleshooting

### "No public keys found"
```bash
# Check keys directory exists
ls -la /etc/soc-monitoring/signing-keys/active/

# Check keys are valid PEM format
head -n 1 /etc/soc-monitoring/signing-keys/active/*.pem
# Should output: -----BEGIN PUBLIC KEY-----

# Check permissions
# Should be readable: -r--r--r-- (0444)
```

### "Signature verification failed: invalid signature"
- Firmware was modified after signing
- Wrong public key used
- Signature encoding issue (not base64)
- Algorithm mismatch

### "Device in security lockdown"
- Too many verification failures
- Reset counter: `soc-ota-agent reset-lockdown`
- Check logs: `/var/log/soc-ota-security.log`

## Performance Impact

Expected overhead per OTA update:
- Key loading (first time only): ~1-2ms
- Signature download: ~100-500ms (network dependent)
- Signature verification: ~5-10ms (RSA) or ~2-5ms (ECDSA)
- **Total added time**: <1 second

This is negligible compared to firmware download time (typically minutes).

## Security Notes

✅ **DO**:
- Always enable `verify_signatures = true` in production
- Set `reject_unsigned = true`
- Monitor `/var/log/soc-ota-security.log`
- Alert on verification failures
- Test with invalid signatures before deployment
- Keep failure counter low (5-10 max)

❌ **DON'T**:
- Disable verification in production
- Store private keys on device
- Allow unlimited retry attempts
- Trust unsigned firmware
- Ignore security logs

## References

- [Signature Verification Package](../../soc-ota-agent/signature/verify.go)
- [Testing Guide](../../soc-ota-agent/tests/TESTING_GUIDE.md)
- [Device-Side Implementation Summary](../../soc-ota-agent/tests/DEVICE_SIDE_IMPLEMENTATION_SUMMARY.md)
