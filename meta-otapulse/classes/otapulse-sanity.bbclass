# OTAPulse Configuration Sanity Check Class
# Validates OTAPulse build-time configuration before do_rootfs assembles the image.
# Customers see clear, actionable errors rather than cryptic build failures later.
#
# Usage — add to your image recipe or global local.conf:
#   inherit otapulse-sanity
#   # or globally:
#   INHERIT += "otapulse-sanity"
#
# ─── Behaviour Controls ───────────────────────────────────────────────────────
#
#   OTAPULSE_SANITY_LEVEL (default: 'error')
#     'error'  — failed checks abort the build with bb.fatal()
#     'warn'   — failed checks emit bb.warn() and the build continues
#     'off'    — all checks are silently skipped
#
#   OTAPULSE_SANITY_SKIP (default: '')
#     Space-separated list of individual check names to bypass.
#     Available names:
#       server  provisioning  mender-artifact  partitions  signing
#       device-type  packages  systemd  image-size  networking
#
#     Example — skip signing and networking checks:
#       OTAPULSE_SANITY_SKIP:append = " signing networking"
#
#   OTAPULSE_PROVISIONING_MODE (defined in otapulse.bbclass, read here)
#     'token'  — a valid credential must exist at build time (default)
#     'manual' — credentials are injected at runtime; skip the provisioning check
#
#   OTAPULSE_ALLOW_UNSIGNED (default: '0')
#     '1' — explicit dev escape hatch: allow the 'signing' check to pass with
#           SOC_OTA_SIGNATURE_VERIFICATION=0 instead of failing the build.
#           Mirrors buildroot-otapulse/package/otapulse/otapulse.mk, which
#           hard-errors on a missing verification key. Never set this for a
#           production image.


# ─── Check behaviour ──────────────────────────────────────────────────────────
OTAPULSE_SANITY_LEVEL   ?= "error"
OTAPULSE_SANITY_SKIP    ?= ""
OTAPULSE_ALLOW_UNSIGNED ?= "0"

# ─── Placeholder detection lists ──────────────────────────────────────────────
# These lists are defined as BitBake variables so projects can extend them:
#   OTAPULSE_PLACEHOLDER_URLS:append = " https://my-internal-placeholder.com"
#   OTAPULSE_PLACEHOLDER_TOKENS:append = " my-example-token"

# Server URLs that are documentation examples and must not reach production
OTAPULSE_PLACEHOLDER_URLS ?= " \
    https://ota.example.com \
    https://your-ota-server.com \
    https://your-server.com \
    https://otapulse.example.com \
    https://monitoring.example.com \
    http://192.168.0.123:8000 \
    https://docker.mender.io \
    CHANGE_ME \
"

# Token / credential values that are documentation examples
# Note: "Paste your ..." style copy-paste instructions are caught in code below
# because they cannot be represented as a single space-separated token.
OTAPULSE_PLACEHOLDER_TOKENS ?= " \
    your-tenant-token \
    your-token-here \
    prov_xxxx \
    YOUR_TOKEN \
    CHANGE_ME \
"


# =============================================================================
# Helper: otapulse_sanity_msg
# =============================================================================
# Formats and emits a failed-check banner.
#
# Arguments:
#   d          — BitBake datastore
#   check_name — short identifier matching OTAPULSE_SANITY_SKIP (e.g. 'server')
#   message    — one-line problem summary
#   detail     — optional multi-line fix instructions (\n-separated)
#
# Returns True when the check is being skipped or level is not 'error'.
# Calls bb.fatal() when OTAPULSE_SANITY_LEVEL == 'error' (the default).
# =============================================================================
def otapulse_sanity_msg(d, check_name, message, detail=''):
    skip_list = (d.getVar('OTAPULSE_SANITY_SKIP', True) or '').split()
    if check_name in skip_list:
        bb.note("OTAPulse Sanity [%s]: skipped (in OTAPULSE_SANITY_SKIP)" % check_name)
        return True

    level = (d.getVar('OTAPULSE_SANITY_LEVEL', True) or 'error').lower().strip()

    sep  = "=" * 68
    dash = "-" * 68
    msg  = "\n%s\n" % sep
    msg += "  OTAPulse Sanity — CHECK FAILED: %s\n" % check_name.upper()
    msg += "%s\n" % dash
    msg += "  Problem: %s\n" % message
    if detail:
        msg += "%s\n" % dash
        msg += "  Fix:\n"
        for line in detail.strip().split('\n'):
            msg += "    %s\n" % line
    msg += "%s\n" % dash
    msg += "  To skip this check:           OTAPULSE_SANITY_SKIP:append = ' %s'\n" % check_name
    msg += "  To downgrade all to warnings: OTAPULSE_SANITY_LEVEL = 'warn'\n"
    msg += "%s" % sep

    if level == 'error':
        bb.fatal(msg)
        return False
    else:
        bb.warn(msg)
        return True


# =============================================================================
# Main check function — called by do_otapulse_sanity
# =============================================================================
python otapulse_sanity_check() {
    """Validate OTAPulse configuration. Runs before do_rootfs."""
    import os

    # Fast exit when all checks are globally disabled
    if (d.getVar('OTAPULSE_SANITY_LEVEL', True) or 'error').lower().strip() == 'off':
        bb.note("OTAPulse Sanity: disabled (OTAPULSE_SANITY_LEVEL=off)")
        return

    passed  = 0
    failed  = 0
    skipped = 0

    skip_list          = (d.getVar('OTAPULSE_SANITY_SKIP',          True) or '').split()
    placeholder_urls   = (d.getVar('OTAPULSE_PLACEHOLDER_URLS',     True) or '').split()
    placeholder_tokens = (d.getVar('OTAPULSE_PLACEHOLDER_TOKENS',   True) or '').split()

    bb.note("=" * 68)
    bb.note("OTAPulse Sanity Checks")
    bb.note("=" * 68)

    # ------------------------------------------------------------------
    # Helper: is a credential a known placeholder or empty?
    # Also catches "Paste your ..." style copy-paste instruction strings
    # that cannot be represented as a single space-separated list token.
    # ------------------------------------------------------------------
    def is_placeholder_token(cred):
        s = (cred or '').strip()
        if not s:
            return True                       # empty → treat as unconfigured
        if s in placeholder_tokens:
            return True                       # exact match against list
        if s.lower().startswith('paste '):
            return True                       # "Paste your tenant token here" style
        return False

    # ==================================================================
    # CHECK 'server' — OTA_SERVER_URL must be a real, non-placeholder URL
    # ==================================================================
    if 'server' in skip_list:
        bb.note("OTAPulse Sanity [server]: skipped")
        skipped += 1
    else:
        url = (d.getVar('OTA_SERVER_URL', True) or '').strip()

        if not url:
            otapulse_sanity_msg(d, 'server',
                "OTA_SERVER_URL is not set.",
                "Add to local.conf:\n"
                "  OTA_SERVER_URL = \"https://your-actual-ota-server.com\"")
            failed += 1
        elif url in placeholder_urls:
            otapulse_sanity_msg(d, 'server',
                "OTA_SERVER_URL is set to a placeholder value: %s" % url,
                "Replace it with your real server URL in local.conf:\n"
                "  OTA_SERVER_URL = \"https://your-actual-ota-server.com\"")
            failed += 1
        elif not (url.startswith('http://') or url.startswith('https://')):
            otapulse_sanity_msg(d, 'server',
                "OTA_SERVER_URL does not start with http:// or https://: %s" % url,
                "Provide a full URL including the scheme:\n"
                "  OTA_SERVER_URL = \"https://your-actual-ota-server.com\"")
            failed += 1
        else:
            bb.note("OTAPulse Sanity [server]: OK (%s)" % url)
            passed += 1

    # ==================================================================
    # CHECK 'provisioning' — at least one valid credential must be set
    # (skipped entirely when OTAPULSE_PROVISIONING_MODE = 'manual')
    # ==================================================================
    if 'provisioning' in skip_list:
        bb.note("OTAPulse Sanity [provisioning]: skipped")
        skipped += 1
    else:
        prov_mode    = (d.getVar('OTAPULSE_PROVISIONING_MODE',  True) or 'token').strip().lower()
        tenant_token = (d.getVar('OTAPULSE_TENANT_TOKEN',       True) or '').strip()
        prov_token   = (d.getVar('OTAPULSE_PROVISIONING_TOKEN', True) or '').strip()
        api_key      = (d.getVar('SOC_MONITORING_API_KEY',      True) or '').strip()

        if prov_mode == 'manual':
            bb.note("OTAPulse Sanity [provisioning]: OK "
                    "(OTAPULSE_PROVISIONING_MODE=manual — credentials injected at runtime)")
            passed += 1
        elif (not is_placeholder_token(tenant_token) or
              not is_placeholder_token(prov_token)   or
              not is_placeholder_token(api_key)):
            bb.note("OTAPulse Sanity [provisioning]: OK (credential configured)")
            passed += 1
        else:
            otapulse_sanity_msg(d, 'provisioning',
                "No valid provisioning credential found — all values are empty or placeholders.",
                "Set at least ONE of the following in local.conf:\n"
                "\n"
                "  Option A — Mender tenant token:\n"
                "    OTAPULSE_TENANT_TOKEN = \"eyJhbGciOi...<your-real-token>\"\n"
                "\n"
                "  Option B — OTAPulse provisioning token:\n"
                "    OTAPULSE_PROVISIONING_TOKEN = \"spt_<your-real-token>\"\n"
                "\n"
                "  Option C — Monitoring API key:\n"
                "    SOC_MONITORING_API_KEY = \"<your-api-key>\"\n"
                "\n"
                "  Option D — Inject credentials at runtime (factory provisioning):\n"
                "    OTAPULSE_PROVISIONING_MODE = \"manual\"")
            failed += 1

    # ==================================================================
    # CHECK 'mender-artifact' — class must be inherited and ext4 present
    # ==================================================================
    if 'mender-artifact' in skip_list:
        bb.note("OTAPulse Sanity [mender-artifact]: skipped")
        skipped += 1
    else:
        inherited = (d.getVar('INHERITED',     True) or '').split()
        fstypes   = (d.getVar('IMAGE_FSTYPES', True) or '').split()

        if 'mender-artifact' not in inherited:
            otapulse_sanity_msg(d, 'mender-artifact',
                "The 'mender-artifact' class is not inherited — .mender files will not be generated.",
                "Add to your image recipe or local.conf:\n"
                "  inherit mender-artifact\n"
                "  # or globally:\n"
                "  INHERIT += \"mender-artifact\"\n"
                "\n"
                "Inheriting 'otapulse' already includes this automatically.")
            failed += 1
        elif 'ext4' not in fstypes:
            otapulse_sanity_msg(d, 'mender-artifact',
                "'ext4' is not in IMAGE_FSTYPES — mender-artifact requires an ext4 rootfs.",
                "Append ext4 to IMAGE_FSTYPES:\n"
                "  IMAGE_FSTYPES:append = \" ext4\"\n"
                "\n"
                "Inheriting 'otapulse' already does this automatically.")
            failed += 1
        else:
            bb.note("OTAPulse Sanity [mender-artifact]: OK "
                    "(class inherited, ext4 in IMAGE_FSTYPES)")
            passed += 1

    # ==================================================================
    # CHECK 'partitions' — A/B layout configuration must be coherent
    # ==================================================================
    if 'partitions' in skip_list:
        bb.note("OTAPulse Sanity [partitions]: skipped")
        skipped += 1
    else:
        partition_mode     = (d.getVar('OTAPULSE_PARTITION_MODE', True) or 'dynamic').strip()
        wks_file           = (d.getVar('WKS_FILE',                True) or '').strip()
        image_install_pkgs = (d.getVar('IMAGE_INSTALL',           True) or '').split()

        if partition_mode == 'static' and not wks_file:
            otapulse_sanity_msg(d, 'partitions',
                "OTAPULSE_PARTITION_MODE='static' but WKS_FILE is not set.",
                "Specify a .wks partition layout file:\n"
                "  WKS_FILE = \"otapulse-ab-static.wks\"\n"
                "\n"
                "Or switch to dynamic mode (A/B partitions created on first boot):\n"
                "  OTAPULSE_PARTITION_MODE = \"dynamic\"")
            failed += 1
        elif partition_mode == 'dynamic' and 'otapulse-firstboot' not in image_install_pkgs:
            otapulse_sanity_msg(d, 'partitions',
                "OTAPULSE_PARTITION_MODE='dynamic' but 'otapulse-firstboot' is not in IMAGE_INSTALL.",
                "The firstboot package creates A/B partitions on the first boot.\n"
                "Add to your image recipe or local.conf:\n"
                "  IMAGE_INSTALL:append = \" otapulse-firstboot\"\n"
                "\n"
                "Inheriting 'otapulse' already includes this automatically for dynamic mode.\n"
                "\n"
                "Or switch to static mode if partitions are pre-allocated in your .wks file:\n"
                "  OTAPULSE_PARTITION_MODE = \"static\"")
            failed += 1
        else:
            bb.note("OTAPulse Sanity [partitions]: OK (mode=%s)" % partition_mode)
            passed += 1

    # ==================================================================
    # CHECK 'signing' — artifact signing and verification key setup
    # ==================================================================
    if 'signing' in skip_list:
        bb.note("OTAPulse Sanity [signing]: skipped")
        skipped += 1
    else:
        sig_verify  = (d.getVar('SOC_OTA_SIGNATURE_VERIFICATION', True) or '0').strip()
        verify_keys = (d.getVar('SOC_OTA_VERIFICATION_KEYS',       True) or '').strip()
        signing_key = (d.getVar('SOC_OTA_SIGNING_KEY',             True) or '').strip()

        if sig_verify == '1':
            # Verification explicitly enabled — key files must exist on the build host
            if not verify_keys:
                otapulse_sanity_msg(d, 'signing',
                    "SOC_OTA_SIGNATURE_VERIFICATION=1 but SOC_OTA_VERIFICATION_KEYS is not set.",
                    "Set the path to your public verification key:\n"
                    "  SOC_OTA_VERIFICATION_KEYS = \"/path/to/public.key\"\n"
                    "\n"
                    "Generate a key pair with:\n"
                    "  openssl genrsa -out private.key 3072\n"
                    "  openssl rsa -in private.key -pubout -out public.key")
                failed += 1
            elif not os.path.isfile(verify_keys):
                otapulse_sanity_msg(d, 'signing',
                    "Verification key file not found: %s" % verify_keys,
                    "Ensure the file exists at the path specified in SOC_OTA_VERIFICATION_KEYS.")
                failed += 1
            elif signing_key and not os.path.isfile(signing_key):
                otapulse_sanity_msg(d, 'signing',
                    "Signing key file not found: %s" % signing_key,
                    "Ensure the private key exists at the path specified in SOC_OTA_SIGNING_KEY.")
                failed += 1
            else:
                bb.note("OTAPulse Sanity [signing]: OK "
                        "(verification enabled, key: %s)" % verify_keys)
                passed += 1
        elif (d.getVar('OTAPULSE_ALLOW_UNSIGNED', True) or '0').strip() == '1':
            # Verification is off, but the dev escape hatch is explicitly set — allow it
            bb.warn("OTAPulse Sanity [signing]: artifact signature verification is DISABLED "
                    "(SOC_OTA_SIGNATURE_VERIFICATION=0) and OTAPULSE_ALLOW_UNSIGNED=1 — "
                    "proceeding with an UNSIGNED build. Never ship this image to production:\n"
                    "  SOC_OTA_SIGNATURE_VERIFICATION = \"1\"\n"
                    "  SOC_OTA_VERIFICATION_KEYS       = \"/path/to/public.key\"\n"
                    "  SOC_OTA_SIGNING_KEY             = \"/path/to/private.key\"")
            passed += 1
        else:
            # Verification is off and no escape hatch — this must block the build.
            # A stock 'inherit otapulse' image must never silently accept unsigned
            # artifacts (Buildroot already hard-errors the equivalent case).
            otapulse_sanity_msg(d, 'signing',
                "Artifact signature verification is DISABLED "
                "(SOC_OTA_SIGNATURE_VERIFICATION=0). Unsigned artifacts are not "
                "permitted by default.",
                "Enable signature verification:\n"
                "  SOC_OTA_SIGNATURE_VERIFICATION = \"1\"\n"
                "  SOC_OTA_VERIFICATION_KEYS       = \"/path/to/public.key\"\n"
                "  SOC_OTA_SIGNING_KEY             = \"/path/to/private.key\"\n"
                "\n"
                "Or, for an explicit dev-only unsigned build:\n"
                "  OTAPULSE_ALLOW_UNSIGNED = \"1\"")
            failed += 1

    # ==================================================================
    # CHECK 'device-type' — Mender artifact must identify its target hardware
    # ==================================================================
    if 'device-type' in skip_list:
        bb.note("OTAPulse Sanity [device-type]: skipped")
        skipped += 1
    else:
        device_type = (d.getVar('MENDER_DEVICE_TYPE', True) or '').strip()
        machine     = (d.getVar('MACHINE',            True) or '').strip()

        if not device_type and not machine:
            otapulse_sanity_msg(d, 'device-type',
                "Neither MENDER_DEVICE_TYPE nor MACHINE is set.",
                "Set a device type identifier so artifacts target the correct hardware:\n"
                "  MENDER_DEVICE_TYPE = \"my-board-v2\"  # preferred — explicit\n"
                "  # or rely on MACHINE (usually set automatically by your BSP):\n"
                "  MACHINE = \"my-board\"")
            failed += 1
        else:
            effective = device_type if device_type else machine
            bb.note("OTAPulse Sanity [device-type]: OK (%s)" % effective)
            passed += 1

    # ==================================================================
    # CHECK 'packages' — required and recommended packages in IMAGE_INSTALL
    # ==================================================================
    if 'packages' in skip_list:
        bb.note("OTAPulse Sanity [packages]: skipped")
        skipped += 1
    else:
        image_install_pkgs = (d.getVar('IMAGE_INSTALL', True) or '').split()

        # soc-ota-agent is required; 'mender' is accepted as an alias
        OTA_AGENT_NAMES = ['soc-ota-agent', 'mender']
        has_agent = any(p in image_install_pkgs for p in OTA_AGENT_NAMES)

        if not has_agent:
            otapulse_sanity_msg(d, 'packages',
                "'soc-ota-agent' is not in IMAGE_INSTALL — the OTA client will be absent.",
                "Add to your image recipe or local.conf:\n"
                "  IMAGE_INSTALL:append = \" soc-ota-agent\"\n"
                "\n"
                "Inheriting 'otapulse' already includes this automatically.")
            failed += 1
        else:
            # Recommended packages — warn only, never fail
            if 'socmond-bin' not in image_install_pkgs and 'socmond' not in image_install_pkgs:
                bb.warn("OTAPulse Sanity [packages]: 'socmond-bin' not in IMAGE_INSTALL "
                        "— device monitoring and crash reporting will be unavailable. "
                        "Add: IMAGE_INSTALL:append = \" socmond-bin\"")
            if 'u-boot-fw-utils' not in image_install_pkgs:
                bb.warn("OTAPulse Sanity [packages]: 'u-boot-fw-utils' not in IMAGE_INSTALL "
                        "— U-Boot environment variable access may be unavailable on the device. "
                        "Add: IMAGE_INSTALL:append = \" u-boot-fw-utils\"")
            if 'gptfdisk' not in image_install_pkgs:
                bb.warn("OTAPulse Sanity [packages]: 'gptfdisk' not in IMAGE_INSTALL "
                        "— GPT partition management on the device may not work. "
                        "Add: IMAGE_INSTALL:append = \" gptfdisk\"")

            bb.note("OTAPulse Sanity [packages]: OK (soc-ota-agent present)")
            passed += 1

    # ==================================================================
    # CHECK 'systemd' — OTAPulse services require systemd as the init system
    # ==================================================================
    if 'systemd' in skip_list:
        bb.note("OTAPulse Sanity [systemd]: skipped")
        skipped += 1
    else:
        distro_features = (d.getVar('DISTRO_FEATURES', True) or '').split()
        init_manager    = (d.getVar('INIT_MANAGER',    True) or '').strip().lower()

        if 'systemd' not in distro_features and init_manager != 'systemd':
            otapulse_sanity_msg(d, 'systemd',
                "systemd is not the init system — OTAPulse services will not start.",
                "Enable systemd in your distro configuration or local.conf:\n"
                "\n"
                "  Option A — DISTRO_FEATURES (classic Yocto syntax):\n"
                "    DISTRO_FEATURES:append           = \" systemd\"\n"
                "    VIRTUAL-RUNTIME_init_manager     = \"systemd\"\n"
                "    VIRTUAL-RUNTIME_initscripts      = \"\"\n"
                "\n"
                "  Option B — INIT_MANAGER (Kirkstone+ shorthand):\n"
                "    INIT_MANAGER = \"systemd\"")
            failed += 1
        else:
            bb.note("OTAPulse Sanity [systemd]: OK "
                    "(DISTRO_FEATURES systemd=%s, INIT_MANAGER=%s)"
                    % ('yes' if 'systemd' in distro_features else 'no',
                       init_manager if init_manager else 'not-set'))
            passed += 1

    # ==================================================================
    # CHECK 'image-size' — rootfs must fit inside the static partition
    # Only meaningful when OTAPULSE_PARTITION_MODE = 'static'.
    # ==================================================================
    if 'image-size' in skip_list:
        bb.note("OTAPulse Sanity [image-size]: skipped")
        skipped += 1
    else:
        partition_mode = (d.getVar('OTAPULSE_PARTITION_MODE', True) or 'dynamic').strip()

        if partition_mode != 'static':
            bb.note("OTAPulse Sanity [image-size]: skipped "
                    "(OTAPULSE_PARTITION_MODE=%s — partition size is not fixed)" % partition_mode)
            passed += 1
        else:
            wks_size_str  = (d.getVar('SOC_WKS_ROOTFS_SIZE',  True) or '').strip()
            rootfs_kb_str = (d.getVar('IMAGE_ROOTFS_SIZE',     True) or '0').strip()
            overhead_str  = (d.getVar('IMAGE_OVERHEAD_FACTOR', True) or '1.3').strip()

            if not wks_size_str:
                bb.note("OTAPulse Sanity [image-size]: SOC_WKS_ROOTFS_SIZE not set — skipping")
                passed += 1
            else:
                try:
                    # Parse SOC_WKS_ROOTFS_SIZE — supports G/M suffixes or bare KB integer
                    s = wks_size_str.upper()
                    if s.endswith('G'):
                        partition_kb = int(float(s[:-1]) * 1024 * 1024)
                    elif s.endswith('M'):
                        partition_kb = int(float(s[:-1]) * 1024)
                    else:
                        partition_kb = int(float(s))

                    rootfs_kb    = int(rootfs_kb_str)
                    overhead     = float(overhead_str)
                    estimated_kb = int(rootfs_kb * overhead)
                    estimated_mb = estimated_kb // 1024
                    partition_mb = partition_kb // 1024

                    bb.note("OTAPulse Sanity [image-size]: "
                            "partition=%d MB, estimated rootfs=%d MB "
                            "(base=%d KB x %.2f overhead)"
                            % (partition_mb, estimated_mb, rootfs_kb, overhead))

                    if estimated_kb > partition_kb:
                        suggested_mb = int((estimated_kb * 1.1) / 1024) + 1
                        otapulse_sanity_msg(d, 'image-size',
                            "Estimated rootfs (%d MB) exceeds partition size (%d MB)."
                            % (estimated_mb, partition_mb),
                            "Increase SOC_WKS_ROOTFS_SIZE in local.conf:\n"
                            "  Current:   SOC_WKS_ROOTFS_SIZE = \"%s\"\n"
                            "  Suggested: SOC_WKS_ROOTFS_SIZE = \"%dM\"  (+10%% headroom)\n"
                            "\n"
                            "Or reduce the image by removing packages from IMAGE_INSTALL."
                            % (wks_size_str, suggested_mb))
                        failed += 1
                    else:
                        pct = 100.0 * estimated_kb / partition_kb if partition_kb else 0.0
                        bb.note("OTAPulse Sanity [image-size]: OK (%.1f%% of partition used)"
                                % pct)
                        passed += 1

                except ValueError as e:
                    bb.warn("OTAPulse Sanity [image-size]: could not parse size values (%s) "
                            "— skipping size check" % str(e))
                    passed += 1

    # ==================================================================
    # CHECK 'networking' — advisory check for network connectivity
    # OTA updates require network access but networking may be configured
    # outside IMAGE_INSTALL (kernel modules, custom scripts, etc.), so
    # this check only warns and always increments the passed counter.
    # ==================================================================
    if 'networking' in skip_list:
        bb.note("OTAPulse Sanity [networking]: skipped")
        skipped += 1
    else:
        image_install_pkgs = (d.getVar('IMAGE_INSTALL', True) or '').split()
        NETWORK_MANAGERS   = ['networkmanager', 'connman', 'systemd-networkd', 'wpa-supplicant']
        found_nm           = [nm for nm in NETWORK_MANAGERS if nm in image_install_pkgs]

        if not found_nm:
            bb.warn("OTAPulse Sanity [networking]: no recognised network manager found in "
                    "IMAGE_INSTALL — OTA updates require network connectivity. "
                    "Consider adding one of: %s. "
                    "If networking is managed outside IMAGE_INSTALL, silence this with: "
                    "OTAPULSE_SANITY_SKIP:append = ' networking'"
                    % ", ".join(NETWORK_MANAGERS))
        else:
            bb.note("OTAPulse Sanity [networking]: OK (%s)" % ", ".join(found_nm))

        passed += 1  # advisory only — never counted as a failure

    # ==================================================================
    # Summary
    # ==================================================================
    bb.note("=" * 68)
    bb.note("OTAPulse Sanity: %d passed | %d failed | %d skipped"
            % (passed, failed, skipped))
    if failed == 0:
        bb.note("All checks passed!")
    bb.note("=" * 68)
}


# =============================================================================
# Task wiring — follows the same pattern as buildversion.bbclass
# =============================================================================

python do_otapulse_sanity() {
    """Validate OTAPulse configuration before building image"""
    bb.build.exec_func('otapulse_sanity_check', d)
}

addtask do_otapulse_sanity before do_rootfs
do_otapulse_sanity[nostamp] = "1"
do_otapulse_sanity[doc] = "Validate OTAPulse configuration before building image"
