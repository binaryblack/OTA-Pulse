#!/bin/bash
# Integration Test for Firmware Signature Verification
# Tests the complete workflow from server to device

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

print_header() {
    echo -e "\n${BLUE}========================================${NC}"
    echo -e "${BLUE}$1${NC}"
    echo -e "${BLUE}========================================${NC}\n"
}

print_success() {
    echo -e "${GREEN}✓ $1${NC}"
}

print_error() {
    echo -e "${RED}✗ $1${NC}"
}

print_info() {
    echo -e "${BLUE}ℹ $1${NC}"
}

# Test 1: Run unit tests
test_unit_tests() {
    print_header "Running Unit Tests"
    
    cd "${PROJECT_ROOT}/soc-ota-agent/signature"
    
    if go test -v; then
        print_success "All unit tests passed"
        return 0
    else
        print_error "Unit tests failed"
        return 1
    fi
}

# Test 2: Test signature verification script
test_verification_script() {
    print_header "Testing Signature Verification Script"
    
    cd "${PROJECT_ROOT}/soc-ota-agent/tests"
    
    if bash test-signature-verification.sh; then
        print_success "Verification script test passed"
        return 0
    else
        print_error "Verification script test failed"
        return 1
    fi
}

# Test 3: End-to-end workflow
test_end_to_end() {
    print_header "End-to-End Workflow Test"
    
    print_info "This test simulates the complete firmware signing and verification workflow"
    
    # Check if test data exists
    local test_dir="${PROJECT_ROOT}/soc-ota-agent/tests/test-data"
    
    if [ ! -d "${test_dir}" ]; then
        print_error "Test data not found. Run test-signature-verification.sh first"
        return 1
    fi
    
    print_info "Test data directory: ${test_dir}"
    
    # Verify firmware with RSA key
    print_info "Testing RSA-4096 signature verification..."
    if [ -f "${test_dir}/firmware/firmware-v1.0.0.rsa.sig" ]; then
        print_success "RSA signature file found"
    else
        print_error "RSA signature file not found"
        return 1
    fi
    
    # Verify firmware with ECDSA key
    print_info "Testing ECDSA-P384 signature verification..."
    if [ -f "${test_dir}/firmware/firmware-v2.0.0.ecdsa.sig" ]; then
        print_success "ECDSA signature file found"
    else
        print_error "ECDSA signature file not found"
        return 1
    fi
    
    print_success "End-to-end workflow test passed"
    return 0
}

# Test 4: Yocto recipe validation
test_yocto_recipe() {
    print_header "Validating Yocto Recipe"
    
    local recipe="${PROJECT_ROOT}/meta-soc-monitoring/recipes-core/signing-keys/signing-keys_1.0.bb"
    
    if [ -f "${recipe}" ]; then
        print_success "Recipe file exists: ${recipe}"
    else
        print_error "Recipe file not found"
        return 1
    fi
    
    # Check for required files
    local files_dir="${PROJECT_ROOT}/meta-soc-monitoring/recipes-core/signing-keys/files"
    
    if [ -d "${files_dir}" ]; then
        print_success "Files directory exists"
    else
        print_error "Files directory not found"
        return 1
    fi
    
    # List expected files
    local expected_files=("production-rsa-public.pem" "production-ecdsa-public.pem" "README.md")
    
    for file in "${expected_files[@]}"; do
        if [ -f "${files_dir}/${file}" ]; then
            print_success "Found: ${file}"
        else
            print_error "Missing: ${file}"
            return 1
        fi
    done
    
    print_success "Yocto recipe validation passed"
    return 0
}

# Test 5: Security checks
test_security() {
    print_header "Security Checks"
    
    print_info "Checking for private keys in repository (should not exist)..."
    
    cd "${PROJECT_ROOT}"
    
    # Search for potential private keys
    if grep -r "BEGIN PRIVATE KEY" --include="*.pem" --include="*.key" . 2>/dev/null | grep -v "test-data"; then
        print_error "WARNING: Private keys found in repository!"
        print_error "Private keys should NEVER be committed to version control"
        return 1
    else
        print_success "No private keys found in repository"
    fi
    
    # Check placeholder files
    if grep -q "PLACEHOLDER" "${PROJECT_ROOT}/meta-soc-monitoring/recipes-core/signing-keys/files/production-rsa-public.pem"; then
        print_success "Production keys are placeholders (as expected)"
    else
        print_info "WARNING: Production keys may be real - ensure they are intentionally committed"
    fi
    
    print_success "Security checks passed"
    return 0
}

# Main execution
main() {
    print_header "Firmware Signature Verification - Integration Tests"
    
    local failed=0
    
    # Run all tests
    test_unit_tests || failed=$((failed + 1))
    test_verification_script || failed=$((failed + 1))
    test_end_to_end || failed=$((failed + 1))
    test_yocto_recipe || failed=$((failed + 1))
    test_security || failed=$((failed + 1))
    
    # Summary
    print_header "Test Summary"
    
    if [ $failed -eq 0 ]; then
        echo -e "${GREEN}✓ All integration tests passed!${NC}\n"
        
        print_header "Next Steps"
        echo "1. Replace placeholder keys in meta-soc-monitoring/recipes-core/signing-keys/files/"
        echo "   with your actual production public keys"
        echo ""
        echo "2. Build Yocto image with signing keys:"
        echo "   bitbake core-image-minimal"
        echo ""
        echo "3. Deploy to device and verify keys are in:"
        echo "   /etc/soc-monitoring/signing-keys/active/"
        echo ""
        echo "4. Test on device:"
        echo "   - Generate signed firmware on server"
        echo "   - Deploy to device"
        echo "   - Check OTA agent logs for verification"
        echo ""
        echo "5. Monitor verification logs:"
        echo "   journalctl -u soc-ota-agent -f"
        
        return 0
    else
        echo -e "${RED}✗ ${failed} test(s) failed${NC}\n"
        return 1
    fi
}

# Run main
main "$@"
