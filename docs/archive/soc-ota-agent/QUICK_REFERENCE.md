# Firmware Signature Verification - Quick Reference

## 🚀 Quick Start (30 seconds)

```bash
# Setup environment
cd soc-ota-agent/tests
./setup-testing.sh

# Run all tests
./test-integration.sh
```

## 📋 Common Commands

### Run Unit Tests
```bash
cd soc-ota-agent/signature
go test -v
```

### Run Integration Tests
```bash
cd soc-ota-agent/tests
./test-signature-verification.sh
```

### Generate Test Keys
```bash
# RSA-4096
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:4096 \
    -out private-key.pem
openssl rsa -in private-key.pem -pubout -out public-key.pem

# ECDSA-P384
openssl ecparam -name secp384r1 -genkey -out private-key.pem
openssl ec -in private-key.pem -pubout -out public-key.pem
```

### Sign Firmware
```bash
# Sign with RSA
openssl dgst -sha256 -sign private-key.pem \
    -out firmware.sig firmware.bin

# Sign with ECDSA
openssl dgst -sha256 -sign private-key.pem \
    -out firmware.sig firmware.bin

# Base64 encode
base64 firmware.sig > firmware.sig.b64
```

### Verify Signature (Manual)
```go
package main

import (
    "encoding/base64"
    "fmt"
    "io/ioutil"
    "signature"
)

func main() {
    // Load public key
    pubKey, _ := signature.LoadPublicKey("public-key.pem")
    
    // Read firmware and signature
    firmware, _ := ioutil.ReadFile("firmware.bin")
    sigB64, _ := ioutil.ReadFile("firmware.sig.b64")
    sig, _ := base64.StdEncoding.DecodeString(string(sigB64))
    
    // Verify
    verifier := signature.NewVerifier([]*signature.PublicKey{pubKey})
    result := verifier.VerifyFirmware(firmware, sig)
    
    if result.Valid {
        fmt.Printf("✓ Valid: %s (%s)\n", result.Algorithm, result.KeyFingerprint)
    } else {
        fmt.Printf("✗ Invalid: %s\n", result.Error)
    }
}
```

## 📁 Key File Locations

### Development
```
soc-ota-agent/tests/test-data/
├── keys/
│   ├── rsa-private.pem
│   ├── rsa-public.pem
│   ├── ecdsa-private.pem
│   └── ecdsa-public.pem
└── firmware/
    ├── firmware-v1.0.0.bin
    └── firmware-v1.0.0.rsa.sig
```

### Production (on device)
```
/etc/soc-monitoring/signing-keys/
├── active/
│   ├── production-rsa-public.pem
│   └── production-ecdsa-public.pem
└── revoked/
```

### Yocto Recipe
```
meta-soc-monitoring/recipes-core/signing-keys/
├── signing-keys_1.0.bb
└── files/
    ├── production-rsa-public.pem
    ├── production-ecdsa-public.pem
    └── README.md
```

## 🔐 Security Checklist

- [ ] Private keys stored in HSM/vault (NOT in repository)
- [ ] Public keys embedded in device firmware
- [ ] Keys installed as read-only (0444 permissions)
- [ ] Signature verification before firmware installation
- [ ] Reject unsigned firmware
- [ ] Log all verification attempts
- [ ] Monitor verification logs for attacks
- [ ] Plan key rotation procedure

## 🐛 Troubleshooting

### "invalid signature"
```bash
# Check key pair matches
openssl rsa -in private.pem -pubout | diff - public.pem

# Re-sign firmware
openssl dgst -sha256 -sign private.pem -out firmware.sig firmware.bin
```

### "failed to load public key"
```bash
# Check file format
head -n 1 public-key.pem
# Should show: -----BEGIN PUBLIC KEY-----

# Fix permissions
chmod 644 public-key.pem
```

### "no public keys loaded"
```bash
# Check keys directory
ls -la /etc/soc-monitoring/signing-keys/active/

# Verify files are .pem
find /etc/soc-monitoring/signing-keys -name "*.pem"
```

### Test failures
```bash
# Clean and re-run
rm -rf soc-ota-agent/tests/test-data
cd soc-ota-agent/tests
./test-signature-verification.sh
```

## 📊 Performance Benchmarks

| Operation           | Time      |
|--------------------|-----------|
| Load RSA key       | ~1-2ms    |
| Load ECDSA key     | ~1ms      |
| Hash 10MB firmware | ~50-100ms |
| Verify RSA sig     | ~5-10ms   |
| Verify ECDSA sig   | ~2-5ms    |

## 🔗 Key Algorithms

### RSA-4096 (PSS padding)
- **Security:** ~150-bit equivalent
- **Key Size:** 4096 bits
- **Signature:** 512 bytes
- **Speed:** Slower, but widely supported

### ECDSA-P384
- **Security:** ~192-bit equivalent
- **Key Size:** 384 bits
- **Signature:** ~96 bytes
- **Speed:** Faster, smaller signatures

## 📚 Documentation

| Document | Purpose |
|----------|---------|
| [TESTING_GUIDE.md](tests/TESTING_GUIDE.md) | Complete testing instructions |
| [DEVICE_SIDE_IMPLEMENTATION_SUMMARY.md](tests/DEVICE_SIDE_IMPLEMENTATION_SUMMARY.md) | Implementation overview |
| [meta-soc-monitoring/.../README.md](../meta-soc-monitoring/recipes-core/signing-keys/files/README.md) | On-device docs |

## 🎯 Next Steps

1. **Install Go (if needed):**
   ```bash
   sudo snap install go --classic
   ```

2. **Run Tests:**
   ```bash
   cd soc-ota-agent/tests
   ./test-integration.sh
   ```

3. **Generate Production Keys:**
   - Open server web UI
   - Navigate to "Signing Keys"
   - Generate new key
   - Download and secure private key

4. **Deploy to Device:**
   ```bash
   # Copy public key
   cp production-rsa-public.pem \
      meta-soc-monitoring/recipes-core/signing-keys/files/
   
   # Build Yocto image
   bitbake core-image-minimal
   ```

5. **Test OTA Update:**
   - Sign firmware on server
   - Deploy to device
   - Check logs: `journalctl -u soc-ota-agent -f`

---

**Need Help?** See [TESTING_GUIDE.md](tests/TESTING_GUIDE.md) for detailed troubleshooting
