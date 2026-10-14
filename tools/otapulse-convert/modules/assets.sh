# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 OTA-Pulse
#
# Prebuilt bootloader files (U-Boot for the Raspberry Pi, a GRUB EFI binary, ...) are binary
# inputs of an image. They are never taken from a fixed address and never trusted unchecked:
#
#   1. the file is looked up by name in the 'assets' directory;
#   2. if it is not there and OTAPULSE_ASSETS_BASE_URL is set, it is downloaded from there;
#   3. in both cases its sha256 must equal the entry for that name in configs/assets.sha256.
#
# A name that is not listed, a missing file, or a different checksum stops the conversion.

# Print the path of a verified asset.
#
#  $1 - file name of the asset
function otapulse_asset_path() {
    if [[ $# -ne 1 ]]; then
        log_fatal "otapulse_asset_path() requires 1 argument"
    fi
    local -r name="$1"
    local -r list="configs/assets.sha256"
    local expected

    expected=$(awk -v n="${name}" '!/^#/ && $2 == n {print $1}' "${list}")
    if [ -z "${expected}" ]; then
        log_fatal "The asset '${name}' is not listed in ${list}, so it cannot be verified."
    fi

    if [ ! -f "assets/${name}" ]; then
        if [ -n "${OTAPULSE_ASSETS_BASE_URL}" ]; then
            log_info "Downloading the asset ${name} from ${OTAPULSE_ASSETS_BASE_URL}"
            run_and_log_cmd "wget -q '${OTAPULSE_ASSETS_BASE_URL%/}/${name}' -O 'assets/${name}.part'"
            run_and_log_cmd "mv 'assets/${name}.part' 'assets/${name}'"
        else
            log_error "The asset '${name}' is needed for this target but is not in the assets directory,"
            log_error "and OTAPULSE_ASSETS_BASE_URL is not set."
            log_fatal "Put the file into the 'assets' directory (expected sha256 ${expected})."
        fi
    fi

    local actual
    actual=$(sha256sum "assets/${name}" | cut -d ' ' -f1)
    if [ "${actual}" != "${expected}" ]; then
        log_error "The asset 'assets/${name}' does not match ${list}."
        log_error "  expected sha256 ${expected}"
        log_fatal "  actual   sha256 ${actual}"
    fi
    echo "assets/${name}"
}
