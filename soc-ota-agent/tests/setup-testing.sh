#!/bin/bash
# Quick Setup Script for Firmware Signature Verification Testing
# Run this to set up your environment for testing

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

echo "=========================================="
echo "Firmware Signature Verification Setup"
echo "=========================================="
echo ""

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Check if Go is installed
echo -n "Checking for Go installation... "
if command -v go &> /dev/null; then
    GO_VERSION=$(go version | awk '{print $3}')
    echo -e "${GREEN}✓ Found: ${GO_VERSION}${NC}"
else
    echo -e "${RED}✗ Not found${NC}"
    echo ""
    echo "Go is required to run the tests. Install it using:"
    echo ""
    echo "  Option 1 (snap):"
    echo "    sudo snap install go --classic"
    echo ""
    echo "  Option 2 (apt):"
    echo "    sudo apt install golang-go"
    echo ""
    echo "  Option 3 (manual):"
    echo "    wget https://go.dev/dl/go1.21.5.linux-amd64.tar.gz"
    echo "    sudo tar -C /usr/local -xzf go1.21.5.linux-amd64.tar.gz"
    echo "    export PATH=\$PATH:/usr/local/go/bin"
    echo ""
    exit 1
fi

# Check Go version
GO_MAJOR=$(echo $GO_VERSION | sed 's/go\([0-9]*\)\..*/\1/')
GO_MINOR=$(echo $GO_VERSION | sed 's/go[0-9]*\.\([0-9]*\)\..*/\1/')

if [ "$GO_MAJOR" -lt 1 ] || ([ "$GO_MAJOR" -eq 1 ] && [ "$GO_MINOR" -lt 19 ]); then
    echo -e "${YELLOW}⚠ Warning: Go version is ${GO_VERSION}, recommend >= 1.19${NC}"
fi

# Check for OpenSSL
echo -n "Checking for OpenSSL... "
if command -v openssl &> /dev/null; then
    OPENSSL_VERSION=$(openssl version | awk '{print $2}')
    echo -e "${GREEN}✓ Found: ${OPENSSL_VERSION}${NC}"
else
    echo -e "${RED}✗ Not found${NC}"
    echo "OpenSSL is required. Install it using:"
    echo "  sudo apt install openssl"
    exit 1
fi

# Make scripts executable
echo -n "Making test scripts executable... "
chmod +x "${SCRIPT_DIR}/test-signature-verification.sh" 2>/dev/null || true
chmod +x "${SCRIPT_DIR}/test-integration.sh" 2>/dev/null || true
echo -e "${GREEN}✓ Done${NC}"

# Check signature package
echo -n "Checking signature package... "
if [ -f "${PROJECT_ROOT}/soc-ota-agent/signature/verify.go" ]; then
    echo -e "${GREEN}✓ Found${NC}"
else
    echo -e "${RED}✗ Not found${NC}"
    echo "Signature package is missing!"
    exit 1
fi

echo ""
echo -e "${GREEN}=========================================="
echo "Setup Complete!"
echo "==========================================${NC}"
echo ""
echo "Quick Start:"
echo ""
echo "1. Run Unit Tests:"
echo "   cd ${PROJECT_ROOT}/soc-ota-agent/signature"
echo "   go test -v"
echo ""
echo "2. Run Signature Verification Tests:"
echo "   cd ${PROJECT_ROOT}/soc-ota-agent/tests"
echo "   ./test-signature-verification.sh"
echo ""
echo "3. Run Full Integration Tests:"
echo "   cd ${PROJECT_ROOT}/soc-ota-agent/tests"
echo "   ./test-integration.sh"
echo ""
echo "4. Read Documentation:"
echo "   less ${PROJECT_ROOT}/soc-ota-agent/tests/TESTING_GUIDE.md"
echo ""
echo -e "${BLUE}For more information, see TESTING_GUIDE.md${NC}"
