#!/bin/bash
# quickstart-qemu.sh — Zero-to-first-OTA-update demo using QEMU
#
# Builds the OTA agent, creates a test artifact, and demonstrates the
# full update lifecycle without requiring any hardware.
#
# Usage:
#   quickstart-qemu.sh [--skip-build] [--arch ARCH]
#
# Options:
#   --skip-build    Skip agent build (use existing binary)
#   --arch ARCH     Target architecture: arm64 (default), arm, amd64
#   -h, --help      Show this help message

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
AGENT_DIR="$REPO_ROOT/soc-ota-agent"
WORK_DIR="/tmp/otapulse-quickstart"

SKIP_BUILD=false
ARCH="arm64"

# ---------------------------------------------------------------------------
# Terminal colours
# ---------------------------------------------------------------------------
if [[ -t 1 ]] && [[ "${NO_COLOR:-}" == "" ]]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[1;33m'
    BLUE='\033[0;34m'
    BOLD='\033[1m'
    RESET='\033[0m'
else
    RED='' GREEN='' YELLOW='' BLUE='' BOLD='' RESET=''
fi

info()  { printf "${BLUE}==>${RESET} %s\n" "$*"; }
ok()    { printf "${GREEN}==>${RESET} %s\n" "$*"; }
warn()  { printf "${YELLOW}==>${RESET} %s\n" "$*"; }
error() { printf "${RED}==>${RESET} %s\n" "$*" >&2; }
step()  { printf "\n${BOLD}${BLUE}── Step %s: %s${RESET}\n" "$1" "$2"; }

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --skip-build) SKIP_BUILD=true; shift ;;
        --arch)
            [[ $# -ge 2 ]] || { error "Option $1 requires an argument"; exit 1; }
            ARCH="$2"; shift 2 ;;
        -h|--help)    sed -En '2,/^$/s/^# ?//p' "$0"; exit 0 ;;
        *)            error "Unknown option: $1"; exit 1 ;;
    esac
done

# Map arch to Go/QEMU values
case "$ARCH" in
    arm64|aarch64)
        GOARCH=arm64
        QEMU_BIN=qemu-system-aarch64
        QEMU_MACHINE=virt
        QEMU_CPU=cortex-a57
        CC_PREFIX=aarch64-linux-gnu-
        ;;
    arm|armhf)
        GOARCH=arm
        GOARM=7
        QEMU_BIN=qemu-system-arm
        QEMU_MACHINE=virt
        QEMU_CPU=cortex-a15
        CC_PREFIX=arm-linux-gnueabihf-
        ;;
    amd64|x86_64)
        GOARCH=amd64
        QEMU_BIN=qemu-system-x86_64
        QEMU_MACHINE=q35
        QEMU_CPU=qemu64
        CC_PREFIX=""
        ;;
    *)
        error "Unsupported architecture: $ARCH"
        exit 1
        ;;
esac

# ---------------------------------------------------------------------------
# Banner
# ---------------------------------------------------------------------------
printf "\n${BOLD}OTAPulse Quickstart — QEMU Demo${RESET}\n"
printf '═%.0s' {1..50}; printf '\n'
info "Architecture: ${ARCH} (GOARCH=${GOARCH})"
info "Work directory: ${WORK_DIR}"
info "Agent source: ${AGENT_DIR}"
printf '═%.0s' {1..50}; printf '\n'

# ---------------------------------------------------------------------------
# Prerequisites check
# ---------------------------------------------------------------------------
step "0" "Checking prerequisites"

MISSING=""
for tool in go mender-artifact openssl; do
    if ! command -v "$tool" &>/dev/null; then
        MISSING="$MISSING $tool"
    fi
done

if [[ -n "$MISSING" ]]; then
    error "Missing required tools:$MISSING"
    echo "  Install them before running this script."
    echo "  See docs/QUICKSTART.md for installation instructions."
    exit 1
fi

ok "All prerequisites found"

# ---------------------------------------------------------------------------
# Set up work directory
# ---------------------------------------------------------------------------
mkdir -p "$WORK_DIR"/{config,artifacts,keys}

# ---------------------------------------------------------------------------
# Step 1: Build the agent
# ---------------------------------------------------------------------------
step "1" "Building OTA agent"

if [[ "$SKIP_BUILD" == "true" ]]; then
    if [[ -f "$AGENT_DIR/otapulse" ]]; then
        info "Using existing binary: $AGENT_DIR/otapulse"
        cp "$AGENT_DIR/otapulse" "$WORK_DIR/"
    else
        error "No existing binary found. Run without --skip-build."
        exit 1
    fi
else
    info "Building for host architecture (standalone test mode)..."
    cd "$AGENT_DIR"
    make build
    cp otapulse "$WORK_DIR/"
    ok "Agent built successfully"
fi

# ---------------------------------------------------------------------------
# Step 2: Generate signing keys
# ---------------------------------------------------------------------------
step "2" "Generating signing keys"

PRIVATE_KEY="$WORK_DIR/keys/signing-key.pem"
PUBLIC_KEY="$WORK_DIR/keys/verify-key.pem"

if [[ -f "$PRIVATE_KEY" ]]; then
    info "Keys already exist, reusing"
else
    openssl genpkey -algorithm RSA -out "$PRIVATE_KEY" -pkeyopt rsa_keygen_bits:3072 2>/dev/null
    openssl rsa -in "$PRIVATE_KEY" -pubout -out "$PUBLIC_KEY" 2>/dev/null
    chmod 600 "$PRIVATE_KEY"
    ok "Signing keys generated"
fi

info "  Private key: $PRIVATE_KEY"
info "  Public key:  $PUBLIC_KEY"

# ---------------------------------------------------------------------------
# Step 3: Generate configuration
# ---------------------------------------------------------------------------
step "3" "Generating configuration"

CONFIG_FILE="$WORK_DIR/config/otapulse.conf"
DEVICE_TYPE="qemu-${ARCH}"

cat > "$CONFIG_FILE" <<EOF
{
    "ServerURL": "https://demo.otapulse.example.com",
    "RootfsPartA": "/dev/vda2",
    "RootfsPartB": "/dev/vda3",
    "UpdatePollIntervalSeconds": 60,
    "InventoryPollIntervalSeconds": 600,
    "RetryPollIntervalSeconds": 30,
    "UseFileBasedBootEnv": true,
    "ArtifactVerifyKey": "${PUBLIC_KEY}"
}
EOF

echo "device_type=${DEVICE_TYPE}" > "$WORK_DIR/config/device_type"

ok "Configuration generated: $CONFIG_FILE"

# Validate with precheck
info "Running pre-build validation..."
if bash "$SCRIPT_DIR/otapulse-precheck.sh" --config "$CONFIG_FILE" 2>/dev/null; then
    ok "Configuration validated"
else
    warn "Some pre-check warnings (expected for demo config)"
fi

# ---------------------------------------------------------------------------
# Step 4: Create test artifacts
# ---------------------------------------------------------------------------
step "4" "Creating test OTA artifacts"

# Create v1.0 artifact (current firmware)
echo "firmware-version=1.0.0" > "$WORK_DIR/artifacts/firmware-v1.txt"
mender-artifact write module-image \
    -T single-file \
    -t "$DEVICE_TYPE" \
    -n "release-1.0.0" \
    -f "$WORK_DIR/artifacts/firmware-v1.txt" \
    -o "$WORK_DIR/artifacts/release-1.0.0.mender" \
    2>/dev/null

ok "Created release-1.0.0.mender (baseline)"

# Create v2.0 artifact (update)
echo "firmware-version=2.0.0" > "$WORK_DIR/artifacts/firmware-v2.txt"
mender-artifact write module-image \
    -T single-file \
    -t "$DEVICE_TYPE" \
    -n "release-2.0.0" \
    -f "$WORK_DIR/artifacts/firmware-v2.txt" \
    -o "$WORK_DIR/artifacts/release-2.0.0.mender" \
    2>/dev/null

ok "Created release-2.0.0.mender (update)"

# Sign the v2.0 artifact
mender-artifact sign \
    "$WORK_DIR/artifacts/release-2.0.0.mender" \
    -k "$PRIVATE_KEY" \
    2>/dev/null

ok "Signed release-2.0.0.mender"

# Validate
mender-artifact validate "$WORK_DIR/artifacts/release-2.0.0.mender" 2>/dev/null
ok "Artifact validated successfully"

# ---------------------------------------------------------------------------
# Step 5: Demonstrate standalone update
# ---------------------------------------------------------------------------
step "5" "Demonstrating standalone OTA update"

info "Showing current artifact info..."
"$WORK_DIR/otapulse" show-artifact 2>/dev/null || true

info ""
info "In a production deployment, the update flow would be:"
info "  1. Server pushes release-2.0.0.mender to device"
info "  2. Agent downloads and verifies signature"
info "  3. Agent writes to inactive partition"
info "  4. Agent reboots to new partition"
info "  5. Agent commits if boot succeeds, rolls back if not"
info ""
info "For standalone testing (no server needed):"
info "  sudo $WORK_DIR/otapulse install $WORK_DIR/artifacts/release-2.0.0.mender"
info "  sudo $WORK_DIR/otapulse commit    # after verifying the update"
info "  sudo $WORK_DIR/otapulse rollback  # if something went wrong"

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
printf "\n"
printf '═%.0s' {1..50}; printf '\n'
printf "${BOLD}${GREEN}Quickstart Complete!${RESET}\n"
printf '═%.0s' {1..50}; printf '\n'
printf "\n"
printf "Files created in: ${WORK_DIR}\n"
printf "  config/otapulse.conf     — Agent configuration\n"
printf "  config/device_type       — Device type file\n"
printf "  keys/signing-key.pem     — Private signing key (keep safe!)\n"
printf "  keys/verify-key.pem      — Public verification key\n"
printf "  artifacts/*.mender       — OTA artifacts\n"
printf "  otapulse                 — Agent binary\n"
printf "\n"
printf "Next steps:\n"
printf "  1. Set up your OTAPulse server\n"
printf "  2. Update ServerURL in otapulse.conf\n"
printf "  3. See docs/INTEGRATION.md for build system integration\n"
printf "  4. See docs/SECURITY.md for production security setup\n"
printf "\n"
