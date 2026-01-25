# Yocto recipe for deploying firmware signing public keys
# Place in: meta-soc-monitoring/recipes-core/signing-keys/signing-keys_1.0.bb
#
# Configuration:
#   SOC_OTA_VERIFICATION_KEYS - Space-separated list of custom public key paths
#                               If not set, default production keys are used

SUMMARY = "Firmware signing public keys for secure OTA updates"
DESCRIPTION = "Deploys cryptographic public keys used to verify firmware signatures on device"
LICENSE = "MIT"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/MIT;md5=0835ade698e0bcf8506ecda2f7b4f302"

SRC_URI = " \
    file://production-rsa-public.pem \
    file://production-ecdsa-public.pem \
    file://README.md \
"

S = "${WORKDIR}"

# Custom verification keys (space-separated paths, set in local.conf)
SOC_OTA_VERIFICATION_KEYS ?= ""

# Directory where public keys will be installed
SIGNING_KEYS_DIR = "${sysconfdir}/soc-monitoring/signing-keys"

python do_install() {
    import os
    import shutil

    d_dir = d.getVar('D')
    workdir = d.getVar('WORKDIR')
    signing_keys_dir = d.getVar('SIGNING_KEYS_DIR')
    custom_keys = d.getVar('SOC_OTA_VERIFICATION_KEYS') or ""

    # Full paths
    dest_base = os.path.join(d_dir, signing_keys_dir.lstrip('/'))
    dest_active = os.path.join(dest_base, 'active')
    dest_revoked = os.path.join(dest_base, 'revoked')

    # Create directory structure
    os.makedirs(dest_active, exist_ok=True)
    os.makedirs(dest_revoked, exist_ok=True)

    # Install README
    readme_src = os.path.join(workdir, 'README.md')
    if os.path.exists(readme_src):
        shutil.copy2(readme_src, dest_base)
        os.chmod(os.path.join(dest_base, 'README.md'), 0o444)

    if custom_keys.strip():
        # Install custom verification keys
        bb.note("Installing custom verification keys")
        for key_path in custom_keys.split():
            key_path = key_path.strip()
            if not key_path:
                continue
            if os.path.isfile(key_path):
                key_name = os.path.basename(key_path)
                dest_path = os.path.join(dest_active, key_name)
                shutil.copy2(key_path, dest_path)
                os.chmod(dest_path, 0o444)
                bb.note("Installed custom key: %s" % key_name)
            else:
                bb.warn("Custom verification key not found: %s" % key_path)
    else:
        # Install default production keys
        bb.note("Installing default production keys")
        for key_file in ['production-rsa-public.pem', 'production-ecdsa-public.pem']:
            src_path = os.path.join(workdir, key_file)
            if os.path.exists(src_path):
                dest_path = os.path.join(dest_active, key_file)
                shutil.copy2(src_path, dest_path)
                os.chmod(dest_path, 0o444)
                bb.note("Installed key: %s" % key_file)
}

FILES:${PN} = " \
    ${SIGNING_KEYS_DIR} \
    ${SIGNING_KEYS_DIR}/active \
    ${SIGNING_KEYS_DIR}/revoked \
    ${SIGNING_KEYS_DIR}/README.md \
"

# Mark as read-only filesystem safe
RDEPENDS:${PN} = ""
RRECOMMENDS:${PN} = "soc-ota-agent"

# Security: Ensure keys are not world-writable
pkg_postinst:${PN}() {
    #!/bin/sh
    if [ -d "$D${SIGNING_KEYS_DIR}" ]; then
        chmod -R u=rX,g=rX,o=rX "$D${SIGNING_KEYS_DIR}"
    fi
}
