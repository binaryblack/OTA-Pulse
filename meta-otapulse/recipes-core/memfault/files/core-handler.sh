#!/bin/sh
# Memfault Core Dump Handler
# Called by kernel via core_pattern when a process crashes
# Arguments: %p %u %g %s %t %c %h %e
#   %p - PID of dumped process
#   %u - numeric UID of dumped process
#   %g - numeric GID of dumped process
#   %s - signal number causing dump
#   %t - time of dump (seconds since epoch)
#   %c - core file size soft resource limit
#   %h - hostname
#   %e - executable filename

# IMPORTANT: Do NOT use set -e here - we must not fail or we cause crash loops
# The kernel calls this for EVERY crash, including crashes in this handler

# Configuration
COREDUMP_DIR="/var/lib/memfault/coredumps"
MAX_COREDUMP_SIZE=52428800  # 50MB
LOG_FILE="/var/log/memfault/coredump.log"

# Parse arguments
PID="${1:-unknown}"
DUMP_UID="${2:-unknown}"
DUMP_GID="${3:-unknown}"
SIGNAL="${4:-unknown}"
TIMESTAMP="${5:-0}"
LIMIT="${6:-unknown}"
DUMP_HOSTNAME="${7:-unknown}"
EXECUTABLE="${8:-unknown}"

# Use current time if timestamp is 0 or invalid
if [ "${TIMESTAMP}" = "0" ] || [ -z "${TIMESTAMP}" ]; then
    TIMESTAMP=$(date +%s 2>/dev/null || echo "0")
fi

# Ensure directories exist (silently)
mkdir -p "${COREDUMP_DIR}" 2>/dev/null || true
mkdir -p "$(dirname "${LOG_FILE}")" 2>/dev/null || true

# Simple logging function
log() {
    echo "$(date '+%Y-%m-%d %H:%M:%S') $*" >> "${LOG_FILE}" 2>/dev/null || true
}

# Skip coredumps from memfault itself to prevent infinite loops
case "${EXECUTABLE}" in
    memfault*|core-handler*)
        # Drain stdin to prevent blocking
        cat > /dev/null 2>/dev/null || true
        exit 0
        ;;
esac

log "Received coredump: pid=${PID} executable=${EXECUTABLE} signal=${SIGNAL}"

# Generate unique filename
COREDUMP_FILE="${COREDUMP_DIR}/core.${EXECUTABLE}.${PID}.${TIMESTAMP}"

# Read core dump from stdin and compress
# Use dd with count limit as fallback if head is not available
if command -v gzip >/dev/null 2>&1; then
    head -c "${MAX_COREDUMP_SIZE}" 2>/dev/null | gzip -c > "${COREDUMP_FILE}.gz" 2>/dev/null
    COMPRESS_STATUS=$?
else
    # No gzip, save uncompressed (truncated)
    head -c "${MAX_COREDUMP_SIZE}" > "${COREDUMP_FILE}" 2>/dev/null
    COMPRESS_STATUS=$?
fi

if [ ${COMPRESS_STATUS} -ne 0 ]; then
    log "ERROR: Failed to capture coredump for ${EXECUTABLE}"
    exit 0  # Exit 0 to prevent kernel from retrying
fi

# Get actual size
if [ -f "${COREDUMP_FILE}.gz" ]; then
    CORE_SIZE=$(stat -c%s "${COREDUMP_FILE}.gz" 2>/dev/null || echo "0")
    CORE_FILE_PATH="${COREDUMP_FILE}.gz"
else
    CORE_SIZE=$(stat -c%s "${COREDUMP_FILE}" 2>/dev/null || echo "0")
    CORE_FILE_PATH="${COREDUMP_FILE}"
fi

# Create simple metadata file (no jq dependency)
METADATA_FILE="${COREDUMP_FILE}.meta"
cat > "${METADATA_FILE}" 2>/dev/null << EOF
{
    "timestamp": "${TIMESTAMP}",
    "pid": "${PID}",
    "uid": "${DUMP_UID}",
    "gid": "${DUMP_GID}",
    "signal": "${SIGNAL}",
    "executable": "${EXECUTABLE}",
    "hostname": "${DUMP_HOSTNAME}",
    "coredump_file": "${CORE_FILE_PATH}",
    "coredump_size": ${CORE_SIZE},
    "compressed": true,
    "uploaded": false
}
EOF

log "Coredump saved: ${CORE_FILE_PATH} (${CORE_SIZE} bytes)"

# Cleanup old coredumps if directory exceeds size limit (500MB)
# Do this in background to not block
(
    MAX_DIR_SIZE=524288000  # 500MB
    dir_size=$(du -sb "${COREDUMP_DIR}" 2>/dev/null | cut -f1 || echo "0")

    while [ "${dir_size}" -gt "${MAX_DIR_SIZE}" ]; do
        oldest=$(ls -t "${COREDUMP_DIR}"/*.gz "${COREDUMP_DIR}"/*.meta 2>/dev/null | tail -1)
        if [ -n "${oldest}" ] && [ -f "${oldest}" ]; then
            rm -f "${oldest}" 2>/dev/null
            base_name="${oldest%.gz}"
            base_name="${base_name%.meta}"
            rm -f "${base_name}.gz" "${base_name}.meta" 2>/dev/null
            log "Removed old coredump: ${oldest}"
            dir_size=$(du -sb "${COREDUMP_DIR}" 2>/dev/null | cut -f1 || echo "0")
        else
            break
        fi
    done
) &

exit 0
