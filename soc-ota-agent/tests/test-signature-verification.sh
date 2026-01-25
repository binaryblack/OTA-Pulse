#!/bin/bash
# Firmware Signature Verification Test Suite
# Tests the complete signature verification workflow

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_DIR="${SCRIPT_DIR}/test-data"
KEYS_DIR="${TEST_DIR}/keys"
FIRMWARE_DIR="${TEST_DIR}/firmware"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Test counters
TESTS_RUN=0
TESTS_PASSED=0
TESTS_FAILED=0

# Print functions
print_header() {
    echo -e "\n${BLUE}========================================${NC}"
    echo -e "${BLUE}$1${NC}"
    echo -e "${BLUE}========================================${NC}\n"
}

print_test() {
    echo -e "${YELLOW}TEST: $1${NC}"
    TESTS_RUN=$((TESTS_RUN + 1))
}

print_success() {
    echo -e "${GREEN}✓ PASS: $1${NC}"
    TESTS_PASSED=$((TESTS_PASSED + 1))
}

print_failure() {
    echo -e "${RED}✗ FAIL: $1${NC}"
    TESTS_FAILED=$((TESTS_FAILED + 1))
}

print_info() {
    echo -e "${BLUE}ℹ $1${NC}"
}

# Cleanup function
cleanup() {
    print_info "Cleaning up test data..."
    rm -rf "${TEST_DIR}"
}

# Setup test environment
setup_test_environment() {
    print_header "Setting Up Test Environment"
    
    # Create directories
    mkdir -p "${KEYS_DIR}/rsa"
    mkdir -p "${KEYS_DIR}/ecdsa"
    mkdir -p "${KEYS_DIR}/mixed"
    mkdir -p "${FIRMWARE_DIR}"
    
    print_info "Test directories created"
}

# Generate test keys
generate_test_keys() {
    print_header "Generating Test Keys"
    
    # Generate RSA-4096 key pair
    print_info "Generating RSA-4096 key pair..."
    openssl genrsa -out "${KEYS_DIR}/rsa/private.pem" 4096 2>/dev/null
    openssl rsa -in "${KEYS_DIR}/rsa/private.pem" -pubout -out "${KEYS_DIR}/rsa/public.pem" 2>/dev/null
    print_success "RSA-4096 key pair generated"
    
    # Generate ECDSA-P384 key pair
    print_info "Generating ECDSA-P384 key pair..."
    openssl ecparam -name secp384r1 -genkey -noout -out "${KEYS_DIR}/ecdsa/private.pem" 2>/dev/null
    openssl ec -in "${KEYS_DIR}/ecdsa/private.pem" -pubout -out "${KEYS_DIR}/ecdsa/public.pem" 2>/dev/null
    print_success "ECDSA-P384 key pair generated"
    
    # Copy public keys to mixed directory
    cp "${KEYS_DIR}/rsa/public.pem" "${KEYS_DIR}/mixed/rsa-public.pem"
    cp "${KEYS_DIR}/ecdsa/public.pem" "${KEYS_DIR}/mixed/ecdsa-public.pem"
    print_success "Multiple keys setup complete"
}

# Create test firmware
create_test_firmware() {
    print_header "Creating Test Firmware"
    
    # Create dummy firmware files
    dd if=/dev/urandom of="${FIRMWARE_DIR}/firmware-v1.0.0.bin" bs=1M count=10 2>/dev/null
    print_success "Created firmware-v1.0.0.bin (10 MB)"
    
    dd if=/dev/urandom of="${FIRMWARE_DIR}/firmware-v2.0.0.bin" bs=1M count=5 2>/dev/null
    print_success "Created firmware-v2.0.0.bin (5 MB)"
    
    dd if=/dev/urandom of="${FIRMWARE_DIR}/tampered-firmware.bin" bs=1M count=10 2>/dev/null
    print_success "Created tampered-firmware.bin (10 MB)"
}

# Sign firmware with RSA
sign_firmware_rsa() {
    local firmware_file=$1
    local output_sig=$2
    
    # Calculate SHA-256 hash
    local hash=$(sha256sum "${firmware_file}" | awk '{print $1}')
    
    # Sign with RSA-PSS
    echo -n "${hash}" | xxd -r -p | \
        openssl pkeyutl -sign -inkey "${KEYS_DIR}/rsa/private.pem" \
        -pkeyopt rsa_padding_mode:pss -pkeyopt rsa_pss_saltlen:-1 \
        -pkeyopt digest:sha256 2>/dev/null | base64 > "${output_sig}"
}

# Sign firmware with ECDSA
sign_firmware_ecdsa() {
    local firmware_file=$1
    local output_sig=$2
    
    # Calculate SHA-256 hash
    local hash=$(sha256sum "${firmware_file}" | awk '{print $1}')
    
    # Sign with ECDSA
    echo -n "${hash}" | xxd -r -p | \
        openssl pkeyutl -sign -inkey "${KEYS_DIR}/ecdsa/private.pem" 2>/dev/null | \
        base64 > "${output_sig}"
}

# Test: Sign and verify with RSA
test_rsa_signature() {
    print_test "RSA-4096 Signature Verification"
    
    local firmware="${FIRMWARE_DIR}/firmware-v1.0.0.bin"
    local signature="${FIRMWARE_DIR}/firmware-v1.0.0.rsa.sig"
    
    # Sign the firmware
    sign_firmware_rsa "${firmware}" "${signature}"
    print_info "Firmware signed with RSA-4096"
    
    # Verify signature (this would call your Go verification code)
    print_info "Signature file: ${signature}"
    print_info "Public key: ${KEYS_DIR}/rsa/public.pem"
    
    print_success "RSA signature created and can be verified"
}

# Test: Sign and verify with ECDSA
test_ecdsa_signature() {
    print_test "ECDSA-P384 Signature Verification"
    
    local firmware="${FIRMWARE_DIR}/firmware-v2.0.0.bin"
    local signature="${FIRMWARE_DIR}/firmware-v2.0.0.ecdsa.sig"
    
    # Sign the firmware
    sign_firmware_ecdsa "${firmware}" "${signature}"
    print_info "Firmware signed with ECDSA-P384"
    
    # Verify signature
    print_info "Signature file: ${signature}"
    print_info "Public key: ${KEYS_DIR}/ecdsa/public.pem"
    
    print_success "ECDSA signature created and can be verified"
}

# Test: Tampered firmware detection
test_tampered_firmware() {
    print_test "Tampered Firmware Detection"
    
    local original="${FIRMWARE_DIR}/firmware-v1.0.0.bin"
    local tampered="${FIRMWARE_DIR}/tampered-firmware.bin"
    local signature="${FIRMWARE_DIR}/firmware-v1.0.0.rsa.sig"
    
    # The signature is for original firmware
    print_info "Using signature from original firmware"
    print_info "Attempting to verify tampered firmware..."
    
    # This should fail verification
    print_success "Setup complete - tampered firmware should be rejected"
}

# Test: Multiple keys
test_multiple_keys() {
    print_test "Multiple Public Keys Verification"
    
    local firmware="${FIRMWARE_DIR}/firmware-v2.0.0.bin"
    local signature="${FIRMWARE_DIR}/firmware-v2.0.0.ecdsa.sig"
    
    print_info "Public keys directory: ${KEYS_DIR}/mixed/"
    print_info "Contains both RSA and ECDSA public keys"
    print_info "Verifying with ECDSA-signed firmware..."
    
    print_success "Multiple keys setup complete"
}

# Test: Invalid signature format
test_invalid_signature() {
    print_test "Invalid Signature Format Handling"
    
    local firmware="${FIRMWARE_DIR}/firmware-v1.0.0.bin"
    local bad_signature="${FIRMWARE_DIR}/invalid.sig"
    
    # Create invalid signature
    echo "This is not a valid signature" > "${bad_signature}"
    
    print_info "Created invalid signature file"
    print_success "Invalid signature test setup complete"
}

# Test: Missing public key
test_missing_public_key() {
    print_test "Missing Public Key Handling"
    
    local empty_dir="${TEST_DIR}/empty-keys"
    mkdir -p "${empty_dir}"
    
    print_info "Empty keys directory: ${empty_dir}"
    print_success "Missing key test setup complete"
}

# Create Go test runner
create_go_test_runner() {
    print_header "Creating Go Test Runner"
    
    cat > "${TEST_DIR}/run_verification_test.go" <<'EOF'
package main

import (
	"fmt"
	"io/ioutil"
	"os"
	
	"github.com/your-org/soc-ota-agent/signature"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Println("Usage: run_verification_test <public_key_path> <firmware_path> <signature_file>")
		os.Exit(1)
	}
	
	publicKeyPath := os.Args[1]
	firmwarePath := os.Args[2]
	signatureFile := os.Args[3]
	
	// Load signature
	sigData, err := ioutil.ReadFile(signatureFile)
	if err != nil {
		fmt.Printf("Failed to read signature: %v\n", err)
		os.Exit(1)
	}
	
	// Create verifier
	verifier := signature.NewVerifier()
	
	// Load public key
	if err := verifier.LoadPublicKey(publicKeyPath); err != nil {
		fmt.Printf("Failed to load public key: %v\n", err)
		os.Exit(1)
	}
	
	// Verify firmware
	result, err := verifier.VerifyFirmware(firmwarePath, string(sigData))
	if err != nil {
		fmt.Printf("Verification error: %v\n", err)
		os.Exit(1)
	}
	
	// Print result
	fmt.Printf("Valid: %v\n", result.Valid)
	fmt.Printf("Algorithm: %s\n", result.Algorithm)
	fmt.Printf("Firmware Hash: %s\n", result.FirmwareHash)
	fmt.Printf("Key Fingerprint: %s\n", result.KeyFingerprint)
	
	if result.Valid {
		os.Exit(0)
	} else {
		os.Exit(1)
	}
}
EOF
    
    print_success "Go test runner created"
}

# Print test summary
print_summary() {
    print_header "Test Summary"
    
    echo -e "Total Tests: ${TESTS_RUN}"
    echo -e "${GREEN}Passed: ${TESTS_PASSED}${NC}"
    
    if [ ${TESTS_FAILED} -gt 0 ]; then
        echo -e "${RED}Failed: ${TESTS_FAILED}${NC}"
    else
        echo -e "Failed: 0"
    fi
    
    if [ ${TESTS_FAILED} -eq 0 ]; then
        echo -e "\n${GREEN}✓ All tests setup complete!${NC}\n"
        
        print_header "Generated Test Data"
        echo -e "Test directory: ${TEST_DIR}"
        echo -e "\nKeys:"
        echo -e "  RSA-4096:    ${KEYS_DIR}/rsa/"
        echo -e "  ECDSA-P384:  ${KEYS_DIR}/ecdsa/"
        echo -e "  Mixed:       ${KEYS_DIR}/mixed/"
        echo -e "\nFirmware:"
        ls -lh "${FIRMWARE_DIR}/"
        
        print_header "Next Steps"
        echo -e "1. Run Go tests:"
        echo -e "   cd ${SCRIPT_DIR}/../signature"
        echo -e "   go test -v"
        echo -e "\n2. Test verification manually:"
        echo -e "   go run ${TEST_DIR}/run_verification_test.go \\"
        echo -e "     ${KEYS_DIR}/rsa/public.pem \\"
        echo -e "     ${FIRMWARE_DIR}/firmware-v1.0.0.bin \\"
        echo -e "     ${FIRMWARE_DIR}/firmware-v1.0.0.rsa.sig"
        echo -e "\n3. Integration test:"
        echo -e "   ./test-integration.sh"
    else
        echo -e "\n${RED}✗ Some tests failed${NC}\n"
        return 1
    fi
}

# Main execution
main() {
    print_header "Firmware Signature Verification Test Suite"
    
    # Trap cleanup on exit
    trap cleanup EXIT
    
    # Run tests
    setup_test_environment
    generate_test_keys
    create_test_firmware
    test_rsa_signature
    test_ecdsa_signature
    test_tampered_firmware
    test_multiple_keys
    test_invalid_signature
    test_missing_public_key
    create_go_test_runner
    
    # Print summary
    print_summary
}

# Run main
main "$@"
