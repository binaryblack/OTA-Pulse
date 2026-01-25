#!/bin/bash
# Migration script from Mender to OTAPulse
# This script helps migrate an existing Mender installation to OTAPulse
# while preserving device identity and configuration.

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
MENDER_CONF_DIR="/etc/mender"
MENDER_DATA_DIR="/usr/share/mender"
MENDER_STATE_DIR="/var/lib/mender"

OTAPULSE_CONF_DIR="/etc/otapulse"
OTAPULSE_DATA_DIR="/usr/share/otapulse"
OTAPULSE_STATE_DIR="/var/lib/otapulse"

DRY_RUN=false
FORCE=false

# Usage function
usage() {
    cat <<EOF
Usage: $0 [OPTIONS]

Migrate from Mender to OTAPulse while preserving device identity.

OPTIONS:
    --dry-run       Show what would be done without making changes
    --force         Force migration even if OTAPulse directories exist
    -h, --help      Show this help message

DESCRIPTION:
    This script migrates an existing Mender installation to OTAPulse by:
    1. Stopping the Mender service
    2. Copying configuration and data to OTAPulse directories
    3. Preserving device identity (authentication keys)
    4. Enabling and starting the OTAPulse service

    The original Mender files are preserved (not deleted).

EXAMPLES:
    # Preview what will be done
    $0 --dry-run

    # Perform the migration
    sudo $0

    # Force migration even if OTAPulse already exists
    sudo $0 --force

EOF
    exit 1
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --force)
            FORCE=true
            shift
            ;;
        -h|--help)
            usage
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            usage
            ;;
    esac
done

# Check if running as root
if [ "$DRY_RUN" = false ] && [ "$(id -u)" -ne 0 ]; then
    echo -e "${RED}Error: This script must be run as root${NC}"
    echo "Try: sudo $0"
    exit 1
fi

# Logging functions
log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Execute or show command
run_cmd() {
    if [ "$DRY_RUN" = true ]; then
        echo "[DRY-RUN] $*"
    else
        eval "$*"
    fi
}

# Main migration function
migrate() {
    echo "======================================"
    echo "  Mender to OTAPulse Migration Tool"
    echo "======================================"
    echo

    if [ "$DRY_RUN" = true ]; then
        log_warn "DRY RUN MODE - No changes will be made"
        echo
    fi

    # Check if Mender is installed
    if [ ! -d "$MENDER_CONF_DIR" ] && [ ! -d "$MENDER_STATE_DIR" ]; then
        log_error "Mender installation not found"
        log_error "No $MENDER_CONF_DIR or $MENDER_STATE_DIR directories found"
        exit 1
    fi

    log_info "Found Mender installation"

    # Check if OTAPulse already exists
    if [ -d "$OTAPULSE_CONF_DIR" ] || [ -d "$OTAPULSE_STATE_DIR" ]; then
        if [ "$FORCE" = false ]; then
            log_error "OTAPulse directories already exist!"
            log_error "Use --force to overwrite, or remove them manually"
            exit 1
        else
            log_warn "Force mode: will overwrite existing OTAPulse directories"
        fi
    fi

    # Step 1: Stop Mender service
    log_info "Step 1: Stopping Mender services..."
    run_cmd "systemctl stop mender-client 2>/dev/null || true"
    run_cmd "systemctl stop mender 2>/dev/null || true"
    run_cmd "systemctl disable mender-client 2>/dev/null || true"
    run_cmd "systemctl disable mender 2>/dev/null || true"

    # Step 2: Create OTAPulse directories
    log_info "Step 2: Creating OTAPulse directories..."
    run_cmd "mkdir -p $OTAPULSE_CONF_DIR"
    run_cmd "mkdir -p $OTAPULSE_STATE_DIR"
    run_cmd "mkdir -p $OTAPULSE_DATA_DIR"

    # Step 3: Copy configuration
    log_info "Step 3: Migrating configuration..."
    if [ -d "$MENDER_CONF_DIR" ]; then
        run_cmd "cp -a $MENDER_CONF_DIR/* $OTAPULSE_CONF_DIR/ 2>/dev/null || true"

        # Rename mender.conf to otapulse.conf
        if [ -f "$MENDER_CONF_DIR/mender.conf" ]; then
            run_cmd "cp $MENDER_CONF_DIR/mender.conf $OTAPULSE_CONF_DIR/otapulse.conf"
            run_cmd "chmod 0600 $OTAPULSE_CONF_DIR/otapulse.conf"
            log_info "  - Copied configuration: mender.conf → otapulse.conf"
        fi

        # Copy device_type
        if [ -f "$MENDER_CONF_DIR/device_type" ]; then
            run_cmd "cp $MENDER_CONF_DIR/device_type $OTAPULSE_CONF_DIR/device_type"
            run_cmd "chmod 0444 $OTAPULSE_CONF_DIR/device_type"
            log_info "  - Copied device_type"
        fi

        # Copy scripts directory
        if [ -d "$MENDER_CONF_DIR/scripts" ]; then
            run_cmd "cp -a $MENDER_CONF_DIR/scripts $OTAPULSE_CONF_DIR/"
            log_info "  - Copied scripts directory"
        fi
    fi

    # Step 4: Copy state data and preserve device identity
    log_info "Step 4: Migrating state data and device identity..."
    if [ -d "$MENDER_STATE_DIR" ]; then
        run_cmd "cp -a $MENDER_STATE_DIR/* $OTAPULSE_STATE_DIR/ 2>/dev/null || true"

        # Preserve authentication key (critical for device identity)
        if [ -f "$MENDER_STATE_DIR/mender-agent.pem" ]; then
            run_cmd "cp $MENDER_STATE_DIR/mender-agent.pem $OTAPULSE_STATE_DIR/mender-agent.pem"
            run_cmd "chmod 0600 $OTAPULSE_STATE_DIR/mender-agent.pem"
            log_info "  - Preserved device authentication key (identity maintained)"
        fi

        # Copy device_type from state directory
        if [ -f "$MENDER_STATE_DIR/device_type" ]; then
            run_cmd "cp $MENDER_STATE_DIR/device_type $OTAPULSE_STATE_DIR/device_type"
            run_cmd "chmod 0444 $OTAPULSE_STATE_DIR/device_type"
        fi
    fi

    # Step 5: Copy data directory (identity, inventory scripts)
    log_info "Step 5: Migrating data directory..."
    if [ -d "$MENDER_DATA_DIR" ]; then
        run_cmd "cp -a $MENDER_DATA_DIR/* $OTAPULSE_DATA_DIR/ 2>/dev/null || true"
        log_info "  - Copied identity and inventory scripts"
    fi

    # Step 6: Enable and start OTAPulse service
    log_info "Step 6: Enabling OTAPulse service..."
    run_cmd "systemctl daemon-reload"
    run_cmd "systemctl enable soc-ota-agent.service"
    run_cmd "systemctl start soc-ota-agent.service"

    echo
    log_info "Migration completed successfully!"
    echo

    if [ "$DRY_RUN" = false ]; then
        # Show service status
        echo "Service status:"
        systemctl status soc-ota-agent.service --no-pager -l || true
        echo
        log_info "Device identity preserved - no re-registration needed"
        log_info "Original Mender files remain in $MENDER_CONF_DIR and $MENDER_STATE_DIR"
        log_info "You can remove them once you verify OTAPulse is working correctly"
        echo
        log_info "Monitor the service with: journalctl -u soc-ota-agent -f"
    else
        echo
        log_info "This was a dry run. Re-run without --dry-run to perform the migration."
    fi
}

# Run migration
migrate

exit 0
