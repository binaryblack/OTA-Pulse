# Firmware Signature Verification - Testing Guide

## Overview

This document explains how to test the firmware signature verification implementation for the SoC OTA Agent.

## Prerequisites

### 1. Install Go (if not already installed)

```bash
# Option 1: Using snap (recommended)
sudo snap install go --classic

# Option 2: Using apt
sudo apt install golang-go

# Option 3: Manual installation
wget https://go.dev/dl/go1.21.5.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.21.5.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
```

### 2. Verify Go Installation

```bash
go version
# Expected: go version go1.21.x linux/amd64
```

## Test Structure

```
soc-ota-agent/
├── signature/
│   ├── verify.go           # Core verification logic
│   └── verify_test.go      # Unit tests
└── tests/
    ├── test-signature-verification.sh   # Comprehensive test suite
    └── test-integration.sh              # Integration tests
```

## Running Tests

### Step 1: Unit Tests

Tests the Go signature verification package in isolation:

```bash
cd soc-ota-agent/signature
go test -v
```

**Expected Output:**
```
=== RUN   TestLoadPublicKey_RSA
--- PASS: TestLoadPublicKey_RSA (0.01s)
=== RUN   TestLoadPublicKey_ECDSA
--- PASS: TestLoadPublicKey_ECDSA (0.01s)
...
PASS
ok      signature       0.234s
```

**Test Coverage:**
- ✅ Load RSA public keys (PEM format)
- ✅ Load ECDSA public keys (PEM format)
- ✅ Load keys from directory
- ✅ Verify valid RSA signatures
- ✅ Verify valid ECDSA signatures
- ✅ Detect tampered firmware
- ✅ Detect invalid signatures
- ✅ Handle multiple keys
- ✅ Handle missing keys
- ✅ Calculate key fingerprints
- ✅ Base64 signature encoding/decoding
- ✅ Large firmware file handling
- ✅ Error handling for corrupt keys
- ✅ Error handling for invalid algorithms

### Step 2: Signature Verification Tests

Comprehensive bash test suite that generates keys, signs firmware, and verifies:

```bash
cd soc-ota-agent/tests
./test-signature-verification.sh
```

**What This Tests:**
1. Generates RSA-4096 and ECDSA-P384 key pairs
2. Creates test firmware files
3. Signs firmware with both algorithms
4. Verifies valid signatures
5. Detects tampered firmware
6. Tests invalid signatures
7. Tests multiple key support
8. Verifies fingerprint calculation

**Expected Output:**
```
========================================
Firmware Signature Verification Test
========================================

Test 1: Generate RSA-4096 Key Pair
✓ RSA key pair generated

Test 2: Generate ECDSA-P384 Key Pair
✓ ECDSA key pair generated

Test 3: Create Test Firmware
✓ Firmware created (size: 10485760 bytes)

Test 4: Sign Firmware with RSA
✓ Firmware signed with RSA

Test 5: Sign Firmware with ECDSA
✓ Firmware signed with ECDSA

Test 6: Verify RSA Signature (Valid)
✓ RSA signature verified

Test 7: Verify ECDSA Signature (Valid)
✓ ECDSA signature verified

Test 8: Verify Tampered Firmware (RSA)
✓ Tampered firmware detected (RSA)

Test 9: Verify Tampered Firmware (ECDSA)
✓ Tampered firmware detected (ECDSA)

Test 10: Verify Invalid Signature
✓ Invalid signature detected

Test 11: Verify with Multiple Keys
✓ Multiple keys verified

========================================
All 11 tests passed!
========================================
```

### Step 3: Integration Tests

Full end-to-end integration testing:

```bash
cd soc-ota-agent/tests
./test-integration.sh
```

**What This Tests:**
1. Runs all unit tests
2. Runs signature verification test suite
3. Validates end-to-end workflow
4. Checks Yocto recipe structure
5. Performs security checks (no private keys in repo)

## Test Data

All test data is generated in `soc-ota-agent/tests/test-data/`:

```
test-data/
├── keys/
│   ├── rsa-private.pem         # Test RSA private key
│   ├── rsa-public.pem          # Test RSA public key
│   ├── ecdsa-private.pem       # Test ECDSA private key
│   └── ecdsa-public.pem        # Test ECDSA public key
└── firmware/
    ├── firmware-v1.0.0.bin     # Test firmware (RSA)
    ├── firmware-v1.0.0.rsa.sig # RSA signature
    ├── firmware-v2.0.0.bin     # Test firmware (ECDSA)
    └── firmware-v2.0.0.ecdsa.sig # ECDSA signature
```

**⚠️ IMPORTANT:** Test data contains private keys and should NOT be used in production!

## Manual Testing

### Test 1: Generate Keys on Server

1. Open SoC Monitoring Server web interface
2. Navigate to "Signing Keys" page
3. Click "Generate New Key"
4. Select algorithm: RSA-4096 or ECDSA-P384
5. Enter key name: "Test Key 2024-01"
6. Click "Generate"
7. **IMMEDIATELY** download private key (shown only once!)
8. Download public key
9. Save both securely

### Test 2: Sign Firmware on Server

1. Navigate to "Firmware" page
2. Upload test firmware file
3. Click "Sign" button
4. Select signing key from dropdown
5. Click "Sign Firmware"
6. Verify signature appears in firmware details

### Test 3: Verify on Device (Simulated)

```bash
cd soc-ota-agent/tests

# Create test firmware
dd if=/dev/urandom of=test-firmware.bin bs=1M count=10

# Sign with server's private key (using OpenSSL)
openssl dgst -sha256 -sign /path/to/private-key.pem \
    -out test-firmware.sig test-firmware.bin

# Base64 encode signature
base64 test-firmware.sig > test-firmware.sig.b64

# Verify using Go package
cat > test_verify.go << 'EOF'
package main

import (
    "encoding/base64"
    "fmt"
    "io/ioutil"
    "log"
    
    "signature"
)

func main() {
    // Load public key
    pubKey, err := signature.LoadPublicKey("/path/to/public-key.pem")
    if err != nil {
        log.Fatal(err)
    }
    
    // Read firmware
    firmware, err := ioutil.ReadFile("test-firmware.bin")
    if err != nil {
        log.Fatal(err)
    }
    
    // Read signature (base64 encoded)
    sigB64, err := ioutil.ReadFile("test-firmware.sig.b64")
    if err != nil {
        log.Fatal(err)
    }
    
    sig, err := base64.StdEncoding.DecodeString(string(sigB64))
    if err != nil {
        log.Fatal(err)
    }
    
    // Verify
    verifier := signature.NewVerifier([]*signature.PublicKey{pubKey})
    result := verifier.VerifyFirmware(firmware, sig)
    
    if result.Valid {
        fmt.Println("✓ Signature verified successfully!")
        fmt.Printf("Algorithm: %s\n", result.Algorithm)
        fmt.Printf("Fingerprint: %s\n", result.KeyFingerprint)
    } else {
        fmt.Println("✗ Signature verification failed!")
        fmt.Printf("Error: %s\n", result.Error)
    }
}
EOF

go run test_verify.go
```

## CI/CD Integration

Add to your CI pipeline:

```yaml
# .github/workflows/test.yml or similar
- name: Test Signature Verification
  run: |
    cd soc-ota-agent/tests
    ./test-integration.sh
```

## Troubleshooting

### Error: "invalid signature"

**Possible Causes:**
1. Firmware file was modified after signing
2. Wrong public key used for verification
3. Signature encoding issue (ensure base64)
4. Algorithm mismatch

**Solution:**
```bash
# Re-sign firmware with correct key
openssl dgst -sha256 -sign private-key.pem -out firmware.sig firmware.bin

# Verify key pair matches
openssl rsa -in private-key.pem -pubout | diff - public-key.pem
```

### Error: "failed to load public key"

**Possible Causes:**
1. File doesn't exist
2. Wrong file format (must be PEM)
3. Corrupt key file
4. Wrong permissions

**Solution:**
```bash
# Check file exists
ls -la /etc/soc-monitoring/signing-keys/active/

# Check file format
head -n 1 public-key.pem
# Should output: -----BEGIN PUBLIC KEY-----

# Fix permissions
chmod 644 public-key.pem
```

### Error: "no public keys loaded"

**Possible Causes:**
1. Keys directory is empty
2. Keys are in wrong subdirectory (active/ vs revoked/)
3. File permissions prevent reading

**Solution:**
```bash
# Check keys directory
find /etc/soc-monitoring/signing-keys -name "*.pem" -ls

# Verify keys are in active/ subdirectory
ls -la /etc/soc-monitoring/signing-keys/active/
```

### Test Failures

If unit tests fail:
```bash
# Run with verbose output
go test -v

# Run specific test
go test -v -run TestLoadPublicKey_RSA

# Check for missing dependencies
go mod download
go mod verify
```

If integration tests fail:
```bash
# Clean test data
rm -rf soc-ota-agent/tests/test-data

# Re-run with detailed output
cd soc-ota-agent/tests
bash -x ./test-signature-verification.sh
```

## Performance Benchmarks

Expected performance on typical embedded device:

```
Operation               | Time      | Notes
------------------------|-----------|---------------------------
Load RSA-4096 key       | ~1-2ms    | Once at startup
Load ECDSA-P384 key     | ~1ms      | Once at startup
Verify RSA signature    | ~5-10ms   | Per firmware download
Verify ECDSA signature  | ~2-5ms    | Per firmware download
Hash 10MB firmware      | ~50-100ms | SHA-256, varies by CPU
```

To benchmark:
```bash
cd soc-ota-agent/signature
go test -bench=. -benchmem
```

## Security Notes

1. **Never commit private keys** to version control
2. **Test keys are NOT secure** - generate new keys for production
3. **Private keys** should be stored in HSM or secure key vault
4. **Public keys** on device are read-only, embedded in firmware
5. **Key rotation** requires firmware update (by design)
6. **Signature verification** happens BEFORE installation
7. **Failed verification** rejects firmware update

## Next Steps

After testing succeeds:

1. ✅ Replace placeholder keys in `meta-soc-monitoring/recipes-core/signing-keys/files/`
2. ✅ Build Yocto image with signing keys
3. ✅ Deploy to test device
4. ✅ Test on actual hardware
5. ✅ Monitor logs: `journalctl -u soc-ota-agent -f`
6. ✅ Test OTA update with signed firmware
7. ✅ Test with intentionally invalid signature (should reject)
8. ✅ Document key rotation procedure
9. ✅ Set up production key management

## Reference Documentation

- [FIRMWARE_SIGNING_IMPLEMENTATION_TRACKER.md](../../docs/security/FIRMWARE_SIGNING_IMPLEMENTATION_TRACKER.md)
- [OTA_SECURITY_ARCHITECTURE.md](../../docs/security/OTA_SECURITY_ARCHITECTURE.md)
- [FIRMWARE_SIGNING_FRONTEND.md](../../docs/FIRMWARE_SIGNING_FRONTEND.md)

## Support

For issues or questions:
1. Check logs: `journalctl -u soc-ota-agent`
2. Review test output for specific errors
3. Verify key files are valid PEM format
4. Ensure Go version >= 1.19
5. Check file permissions on /etc/soc-monitoring/
