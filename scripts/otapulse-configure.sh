#!/bin/bash
# otapulse-configure.sh — Interactive configuration wizard for OTAPulse
#
# Generates a valid otapulse.conf JSON configuration file by prompting the user
# for required values. Optionally generates signing key pairs and emits
# Yocto local.conf snippets or Buildroot .config fragments.
#
# Usage:
#   otapulse-configure.sh [OPTIONS]
#
# Options:
#   -o, --output FILE     Output config file (default: otapulse.conf)
#   -n, --non-interactive Use defaults and environment variables only
#   --yocto               Also emit Yocto local.conf snippet
#   --buildroot           Also emit Buildroot .config fragment
#   --generate-keys       Generate signing key pair
#   -h, --help            Show this help message
#
# Environment variables (used in non-interactive mode):
#   OTAPULSE_SERVER_URL        Server URL
#   OTAPULSE_TENANT_TOKEN      Tenant token
#   OTAPULSE_DEVICE_TYPE       Device type
#   OTAPULSE_UPDATE_POLL       Update poll interval (seconds)
#   OTAPULSE_INVENTORY_POLL    Inventory poll interval (seconds)
#   OTAPULSE_RETRY_POLL        Retry poll interval (seconds)
#   OTAPULSE_ROOTFS_PART_A     Rootfs partition A device path
#   OTAPULSE_ROOTFS_PART_B     Rootfs partition B device path
#   OTAPULSE_VERIFY_KEY        Path to artifact verification key
#   OTAPULSE_USE_FILE_BOOTENV  Use file-based boot environment (true/false)

set -euo pipefail

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

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------
OUTPUT_FILE="otapulse.conf"
NON_INTERACTIVE=false
EMIT_YOCTO=false
EMIT_BUILDROOT=false
GENERATE_KEYS=false

# Config defaults
DEFAULT_SERVER_URL=""
DEFAULT_TENANT_TOKEN=""
DEFAULT_DEVICE_TYPE=""
DEFAULT_UPDATE_POLL=1800
DEFAULT_INVENTORY_POLL=28800
DEFAULT_RETRY_POLL=300
DEFAULT_ROOTFS_A="/dev/mmcblk0p3"
DEFAULT_ROOTFS_B="/dev/mmcblk0p4"
DEFAULT_VERIFY_KEY=""
DEFAULT_USE_FILE_BOOTENV=true

# Placeholder patterns (must match otapulse-verify.sh)
PLACEHOLDER_PATTERNS=(
    "CHANGE_ME"
    "example.com"
    "your-ota-server"
    "your-server"
    "192.168.0.122"
    "docker.mender.io"
    "localhost"
)

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
info()  { printf "${BLUE}[INFO]${RESET} %s\n" "$*"; }
warn()  { printf "${YELLOW}[WARN]${RESET} %s\n" "$*"; }
error() { printf "${RED}[ERROR]${RESET} %s\n" "$*" >&2; }
ok()    { printf "${GREEN}[OK]${RESET} %s\n" "$*"; }

prompt() {
    local var_name="$1" prompt_text="$2" default="$3"
    local value

    if [[ "$NON_INTERACTIVE" == "true" ]]; then
        value="$default"
    else
        if [[ -n "$default" ]]; then
            printf "${BOLD}%s${RESET} [%s]: " "$prompt_text" "$default"
        else
            printf "${BOLD}%s${RESET}: " "$prompt_text"
        fi
        read -r value
        value="${value:-$default}"
    fi

    printf -v "$var_name" '%s' "$value"
}

prompt_yesno() {
    local prompt_text="$1" default="${2:-n}"
    local response

    if [[ "$NON_INTERACTIVE" == "true" ]]; then
        [[ "$default" == "y" ]] && return 0 || return 1
    fi

    if [[ "$default" == "y" ]]; then
        printf "${BOLD}%s${RESET} [Y/n]: " "$prompt_text"
    else
        printf "${BOLD}%s${RESET} [y/N]: " "$prompt_text"
    fi
    read -r response
    response="${response:-$default}"
    [[ "$response" =~ ^[Yy] ]]
}

is_placeholder_url() {
    local url="$1"
    for pat in "${PLACEHOLDER_PATTERNS[@]}"; do
        [[ "$url" == *"$pat"* ]] && return 0
    done
    return 1
}

json_escape() {
    local val="$1"
    # Escape backslashes first, then double quotes
    val="${val//\\/\\\\}"
    val="${val//\"/\\\"}"
    printf '%s' "$val"
}

validate_url() {
    local url="$1"
    if [[ -z "$url" ]]; then
        error "Server URL cannot be empty"
        return 1
    fi
    if [[ "$url" != http://* ]] && [[ "$url" != https://* ]]; then
        error "Server URL must start with http:// or https://"
        return 1
    fi
    if is_placeholder_url "$url"; then
        warn "Server URL looks like a placeholder: $url"
        return 1
    fi
    return 0
}

validate_pem_key() {
    local keyfile="$1"
    if [[ ! -f "$keyfile" ]]; then
        error "Key file not found: $keyfile"
        return 1
    fi
    if ! grep -q "BEGIN.*KEY" "$keyfile" 2>/dev/null; then
        error "File does not appear to be a valid PEM key: $keyfile"
        return 1
    fi
    return 0
}

validate_integer() {
    local value="$1" name="$2"
    if ! [[ "$value" =~ ^[0-9]+$ ]]; then
        error "$name must be a positive integer, got: $value"
        return 1
    fi
    return 0
}

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        -o|--output)
            [[ $# -ge 2 ]] || { error "Option $1 requires an argument"; exit 1; }
            OUTPUT_FILE="$2"; shift 2 ;;
        -n|--non-interactive) NON_INTERACTIVE=true; shift ;;
        --yocto)        EMIT_YOCTO=true; shift ;;
        --buildroot)    EMIT_BUILDROOT=true; shift ;;
        --generate-keys) GENERATE_KEYS=true; shift ;;
        -h|--help)
            sed -En '2,/^$/s/^# ?//p' "$0"
            exit 0
            ;;
        *) error "Unknown option: $1"; exit 1 ;;
    esac
done

# ---------------------------------------------------------------------------
# Apply environment variable overrides
# ---------------------------------------------------------------------------
DEFAULT_SERVER_URL="${OTAPULSE_SERVER_URL:-$DEFAULT_SERVER_URL}"
DEFAULT_TENANT_TOKEN="${OTAPULSE_TENANT_TOKEN:-$DEFAULT_TENANT_TOKEN}"
DEFAULT_DEVICE_TYPE="${OTAPULSE_DEVICE_TYPE:-$DEFAULT_DEVICE_TYPE}"
DEFAULT_UPDATE_POLL="${OTAPULSE_UPDATE_POLL:-$DEFAULT_UPDATE_POLL}"
DEFAULT_INVENTORY_POLL="${OTAPULSE_INVENTORY_POLL:-$DEFAULT_INVENTORY_POLL}"
DEFAULT_RETRY_POLL="${OTAPULSE_RETRY_POLL:-$DEFAULT_RETRY_POLL}"
DEFAULT_ROOTFS_A="${OTAPULSE_ROOTFS_PART_A:-$DEFAULT_ROOTFS_A}"
DEFAULT_ROOTFS_B="${OTAPULSE_ROOTFS_PART_B:-$DEFAULT_ROOTFS_B}"
DEFAULT_VERIFY_KEY="${OTAPULSE_VERIFY_KEY:-$DEFAULT_VERIFY_KEY}"
if [[ -n "${OTAPULSE_USE_FILE_BOOTENV:-}" ]]; then
    DEFAULT_USE_FILE_BOOTENV="$OTAPULSE_USE_FILE_BOOTENV"
fi

# ---------------------------------------------------------------------------
# Banner
# ---------------------------------------------------------------------------
printf "\n${BOLD}OTAPulse Configuration Wizard${RESET}\n"
printf '═%.0s' {1..50}; printf '\n\n'

# ---------------------------------------------------------------------------
# Gather configuration
# ---------------------------------------------------------------------------
# Server URL
while true; do
    prompt SERVER_URL "OTAPulse server URL" "$DEFAULT_SERVER_URL"
    if validate_url "$SERVER_URL"; then
        break
    fi
    [[ "$NON_INTERACTIVE" == "true" ]] && exit 1
done

# Tenant Token
prompt TENANT_TOKEN "Tenant token (from server dashboard)" "$DEFAULT_TENANT_TOKEN"
if [[ -z "$TENANT_TOKEN" ]]; then
    warn "No tenant token set. Device will require manual authorization."
fi

# Device Type
prompt DEVICE_TYPE "Device type (e.g., raspberrypi4, imx8mp-evk)" "$DEFAULT_DEVICE_TYPE"
if [[ -z "$DEVICE_TYPE" ]]; then
    error "Device type is required"
    exit 1
fi

# Poll Intervals
printf "\n${BOLD}Poll Intervals${RESET}\n"
prompt UPDATE_POLL "Update poll interval (seconds)" "$DEFAULT_UPDATE_POLL"
validate_integer "$UPDATE_POLL" "Update poll interval" || exit 1

prompt INVENTORY_POLL "Inventory poll interval (seconds)" "$DEFAULT_INVENTORY_POLL"
validate_integer "$INVENTORY_POLL" "Inventory poll interval" || exit 1

prompt RETRY_POLL "Retry poll interval (seconds)" "$DEFAULT_RETRY_POLL"
validate_integer "$RETRY_POLL" "Retry poll interval" || exit 1

# Partition Configuration
printf "\n${BOLD}Partition Configuration${RESET}\n"
prompt ROOTFS_A "Rootfs partition A" "$DEFAULT_ROOTFS_A"
prompt ROOTFS_B "Rootfs partition B" "$DEFAULT_ROOTFS_B"

# Boot Environment
USE_FILE_BOOTENV="$DEFAULT_USE_FILE_BOOTENV"
if [[ "$NON_INTERACTIVE" == "false" ]]; then
    if prompt_yesno "Use file-based boot environment (recommended if no U-Boot env tools)?" "y"; then
        USE_FILE_BOOTENV=true
    else
        USE_FILE_BOOTENV=false
    fi
fi

# Verification Key
printf "\n${BOLD}Security${RESET}\n"
VERIFY_KEY="$DEFAULT_VERIFY_KEY"

if [[ "$GENERATE_KEYS" == "true" ]] || { [[ "$NON_INTERACTIVE" == "false" ]] && [[ -z "$VERIFY_KEY" ]] && prompt_yesno "Generate a new signing key pair?" "n"; }; then
    GENERATE_KEYS=true
    KEY_DIR="$(dirname "$OUTPUT_FILE")"
    PRIVATE_KEY="${KEY_DIR}/otapulse-signing-key.pem"
    PUBLIC_KEY="${KEY_DIR}/otapulse-verify-key.pem"

    if [[ -f "$PRIVATE_KEY" ]]; then
        warn "Private key already exists: $PRIVATE_KEY"
        if [[ "$NON_INTERACTIVE" == "false" ]] && ! prompt_yesno "Overwrite?" "n"; then
            GENERATE_KEYS=false
        fi
    fi

    if [[ "$GENERATE_KEYS" == "true" ]]; then
        if ! command -v openssl &>/dev/null; then
            error "openssl not found. Install it to generate keys."
            exit 1
        fi
        info "Generating RSA 3072-bit signing key pair..."
        openssl genpkey -algorithm RSA -out "$PRIVATE_KEY" -pkeyopt rsa_keygen_bits:3072 2>/dev/null
        openssl rsa -in "$PRIVATE_KEY" -pubout -out "$PUBLIC_KEY" 2>/dev/null
        chmod 600 "$PRIVATE_KEY"
        chmod 644 "$PUBLIC_KEY"
        ok "Private key: $PRIVATE_KEY"
        ok "Public key:  $PUBLIC_KEY"
        VERIFY_KEY="$PUBLIC_KEY"
    fi
fi

if [[ -z "$VERIFY_KEY" ]]; then
    prompt VERIFY_KEY "Path to artifact verification key (public key PEM)" "$DEFAULT_VERIFY_KEY"
fi

if [[ -n "$VERIFY_KEY" ]] && [[ -f "$VERIFY_KEY" ]]; then
    if validate_pem_key "$VERIFY_KEY"; then
        ok "Verification key validated: $VERIFY_KEY"
    fi
fi

# ---------------------------------------------------------------------------
# Generate otapulse.conf JSON
# ---------------------------------------------------------------------------
printf "\n${BOLD}Generating Configuration${RESET}\n"

# Remove trailing slash from server URL
SERVER_URL="${SERVER_URL%/}"

# Build JSON (escape string values to prevent injection)
JSON_CONTENT="{"
JSON_CONTENT+="\n    \"ServerURL\": \"$(json_escape "$SERVER_URL")\""

if [[ -n "$TENANT_TOKEN" ]]; then
    JSON_CONTENT+=",\n    \"TenantToken\": \"$(json_escape "$TENANT_TOKEN")\""
fi

JSON_CONTENT+=",\n    \"RootfsPartA\": \"$(json_escape "$ROOTFS_A")\""
JSON_CONTENT+=",\n    \"RootfsPartB\": \"$(json_escape "$ROOTFS_B")\""
JSON_CONTENT+=",\n    \"UpdatePollIntervalSeconds\": ${UPDATE_POLL}"
JSON_CONTENT+=",\n    \"InventoryPollIntervalSeconds\": ${INVENTORY_POLL}"
JSON_CONTENT+=",\n    \"RetryPollIntervalSeconds\": ${RETRY_POLL}"

if [[ "$USE_FILE_BOOTENV" == "true" ]]; then
    JSON_CONTENT+=",\n    \"UseFileBasedBootEnv\": true"
fi

if [[ -n "$VERIFY_KEY" ]]; then
    JSON_CONTENT+=",\n    \"ArtifactVerifyKey\": \"$(json_escape "$VERIFY_KEY")\""
fi

JSON_CONTENT+="\n}"

# Write config
printf '%b\n' "$JSON_CONTENT" > "$OUTPUT_FILE"
ok "Configuration written to: $OUTPUT_FILE"

# Also write device_type file next to config
DEVICE_TYPE_FILE="$(dirname "$OUTPUT_FILE")/device_type"
echo "device_type=$DEVICE_TYPE" > "$DEVICE_TYPE_FILE"
ok "Device type written to: $DEVICE_TYPE_FILE"

# ---------------------------------------------------------------------------
# Emit Yocto local.conf snippet
# ---------------------------------------------------------------------------
if [[ "$EMIT_YOCTO" == "true" ]]; then
    YOCTO_SNIPPET="$(dirname "$OUTPUT_FILE")/otapulse-local.conf.snippet"
    cat > "$YOCTO_SNIPPET" <<YOCTO_EOF
# ──────────────────────────────────────────────────────────────────────────
# OTAPulse Configuration — add to your conf/local.conf
# Generated by otapulse-configure.sh on $(date -u +%Y-%m-%d)
# ──────────────────────────────────────────────────────────────────────────
INHERIT += "otapulse"

# Server
OTA_SERVER_URL = "${SERVER_URL}"
OTAPULSE_PROVISIONING_TOKEN = "${TENANT_TOKEN}"

# Device
MENDER_DEVICE_TYPE = "${DEVICE_TYPE}"

# Partitions
MENDER_ROOTFS_PART_A = "${ROOTFS_A}"
MENDER_ROOTFS_PART_B = "${ROOTFS_B}"

# Poll intervals (seconds)
OTAPULSE_UPDATE_POLL_INTERVAL = "${UPDATE_POLL}"
OTAPULSE_INVENTORY_POLL_INTERVAL = "${INVENTORY_POLL}"

# Security — set to "1" and provide signing key for production
SOC_OTA_SIGNATURE_VERIFICATION = "1"
YOCTO_EOF

    if [[ -n "$VERIFY_KEY" ]]; then
        echo "SOC_OTA_ARTIFACT_VERIFY_KEY = \"${VERIFY_KEY}\"" >> "$YOCTO_SNIPPET"
    fi

    ok "Yocto snippet written to: $YOCTO_SNIPPET"
fi

# ---------------------------------------------------------------------------
# Emit Buildroot .config fragment
# ---------------------------------------------------------------------------
if [[ "$EMIT_BUILDROOT" == "true" ]]; then
    BR_FRAGMENT="$(dirname "$OUTPUT_FILE")/otapulse-buildroot.config.fragment"
    cat > "$BR_FRAGMENT" <<BR_EOF
# ──────────────────────────────────────────────────────────────────────────
# OTAPulse Configuration — merge into your Buildroot .config or defconfig
# Generated by otapulse-configure.sh on $(date -u +%Y-%m-%d)
# ──────────────────────────────────────────────────────────────────────────
BR2_PACKAGE_OTAPULSE=y
BR2_PACKAGE_OTAPULSE_SERVER_URL="${SERVER_URL}"
BR2_PACKAGE_OTAPULSE_TENANT_TOKEN="${TENANT_TOKEN}"
BR2_PACKAGE_OTAPULSE_DEVICE_TYPE="${DEVICE_TYPE}"
BR2_PACKAGE_OTAPULSE_UPDATE_POLL_INTERVAL=${UPDATE_POLL}
BR2_PACKAGE_OTAPULSE_INVENTORY_POLL_INTERVAL=${INVENTORY_POLL}
BR2_PACKAGE_OTAPULSE_ROOTFS_PART_A="${ROOTFS_A}"
BR2_PACKAGE_OTAPULSE_ROOTFS_PART_B="${ROOTFS_B}"
BR_EOF

    if [[ -n "$VERIFY_KEY" ]]; then
        echo "BR2_PACKAGE_OTAPULSE_VERIFY_KEY=\"${VERIFY_KEY}\"" >> "$BR_FRAGMENT"
    fi

    ok "Buildroot fragment written to: $BR_FRAGMENT"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
printf "\n"
printf '═%.0s' {1..50}; printf '\n'
printf "${BOLD}Configuration Summary${RESET}\n"
printf '─%.0s' {1..50}; printf '\n'
printf "  Server URL:     %s\n" "$SERVER_URL"
printf "  Device Type:    %s\n" "$DEVICE_TYPE"
printf "  Partitions:     %s / %s\n" "$ROOTFS_A" "$ROOTFS_B"
printf "  Update Poll:    %s seconds\n" "$UPDATE_POLL"
printf "  File Boot Env:  %s\n" "$USE_FILE_BOOTENV"
if [[ -n "$VERIFY_KEY" ]]; then
    printf "  Verify Key:     %s\n" "$VERIFY_KEY"
fi
printf '═%.0s' {1..50}; printf '\n'

printf "\n${GREEN}${BOLD}Configuration complete!${RESET}\n"
printf "  Config file: %s\n" "$OUTPUT_FILE"
printf "\n  Next steps:\n"
printf "  1. Copy %s to /etc/otapulse/ on your device\n" "$OUTPUT_FILE"
printf "  2. Copy %s to /etc/otapulse/ on your device\n" "$DEVICE_TYPE_FILE"
if [[ -n "$VERIFY_KEY" ]]; then
    printf "  3. Copy the verification key to your device\n"
fi
printf "  4. Run 'otapulse-precheck.sh' to validate before building\n"
printf "\n"
