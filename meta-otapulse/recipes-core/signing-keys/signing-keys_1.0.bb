# Yocto recipe for deploying firmware signing public keys
# Place in: meta-soc-monitoring/recipes-core/signing-keys/signing-keys_1.0.bb
#
# Configuration:
#   SOC_OTA_VERIFICATION_KEYS - Space-separated list of custom public key paths
#                               If not set, example placeholder keys are used
#
# IMPORTANT: For production builds, you MUST either:
#   1. Set SOC_OTA_VERIFICATION_KEYS in local.conf to point to your production keys
#   2. Replace the example keys in files/ with your actual production keys
#
# Example in local.conf:
#   SOC_OTA_VERIFICATION_KEYS = "/path/to/your/production-rsa-public.pem"

SUMMARY = "Firmware signing public keys for secure OTA updates"
DESCRIPTION = "Deploys cryptographic public keys used to verify firmware signatures on device"
LICENSE = "MIT"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/MIT;md5=0835ade698e0bcf8506ecda2f7b4f302"

# Custom verification keys (space-separated paths, set in local.conf)
# When set, these keys are used instead of the default example keys
SOC_OTA_VERIFICATION_KEYS ?= ""

# Whether the image enables artifact signature verification (see soc-ota-agent_1.0.0.bb
# and otapulse-sanity.bbclass). GAP-OTA-016: this recipe must never let a
# SOC_OTA_SIGNATURE_VERIFICATION=1 build fall back to example keys silently.
SOC_OTA_SIGNATURE_VERIFICATION ?= "0"

# SRC_URI is conditional:
# - If custom keys are specified, only include README (keys come from custom paths)
# - If no custom keys, include example placeholder keys from files/ directory
SRC_URI = "file://README.md"
SRC_URI:append = "${@'' if d.getVar('SOC_OTA_VERIFICATION_KEYS') else ' file://example-rsa-public.pem file://example-ecdsa-public.pem'}"

S = "${WORKDIR}"

# Directory where public keys will be installed
SIGNING_KEYS_DIR = "${sysconfdir}/soc-monitoring/signing-keys"

python do_install() {
    import os
    import shutil

    d_dir = d.getVar('D')
    workdir = d.getVar('WORKDIR')
    signing_keys_dir = d.getVar('SIGNING_KEYS_DIR')
    custom_keys = d.getVar('SOC_OTA_VERIFICATION_KEYS') or ""
    sig_verify = (d.getVar('SOC_OTA_SIGNATURE_VERIFICATION') or '0').strip()

    # GAP-OTA-016: a signature-verifying build must never fall back to the
    # example keypair (anyone can pull it from the project source). Fail the
    # build outright instead of quietly trusting it — this check lives here
    # (not just in otapulse-sanity.bbclass, see TASK-S38-011) so it can't be
    # bypassed by OTAPULSE_SANITY_LEVEL=warn or a sanity 'signing' skip.
    if sig_verify == '1' and not custom_keys.strip():
        bb.fatal(
            "signing-keys: SOC_OTA_SIGNATURE_VERIFICATION=1 but "
            "SOC_OTA_VERIFICATION_KEYS is not set. Refusing to fall back to "
            "the example keypair shipped in this recipe's files/ directory — "
            "it is publicly available in the project source and must never "
            "be trusted as a production verification key.\n"
            "Set the path to your real public verification key:\n"
            "  SOC_OTA_VERIFICATION_KEYS = \"/path/to/production-rsa-public.pem\""
        )

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
        # Install example placeholder keys (for development/testing only).
        # SOC_OTA_SIGNATURE_VERIFICATION=1 already bb.fatal'd above, so
        # reaching this branch means verification is off — these keys are
        # inert. GAP-OTA-016: install them under their real example- name;
        # NEVER alias them to production-*, which is the exact filename
        # soc-ota-agent's default SOC_OTA_VERIFY_KEY_FILES trusts.
        bb.warn("Installing EXAMPLE placeholder keys - NOT FOR PRODUCTION USE!")
        bb.warn("Set SOC_OTA_VERIFICATION_KEYS in local.conf for production builds")
        for key_file in ['example-rsa-public.pem', 'example-ecdsa-public.pem']:
            src_path = os.path.join(workdir, key_file)
            if os.path.exists(src_path):
                dest_path = os.path.join(dest_active, key_file)
                shutil.copy2(src_path, dest_path)
                os.chmod(dest_path, 0o444)
                bb.note("Installed example key as: %s" % key_file)
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
