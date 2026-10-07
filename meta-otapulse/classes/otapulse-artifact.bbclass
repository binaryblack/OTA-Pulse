# OTA-Pulse Artifact Generation Class
# Generates .otapulse artifact files from rootfs images for OTA updates
# Supports optional artifact signing with RSA or ECDSA keys
#
# Usage: inherit otapulse-artifact in your image recipe
# (the old name, mender-artifact, is a one-line shim that inherits this class;
# it is kept for one release and then removed)
#
# Configuration variables (set in local.conf):
#   OTAPULSE_DEVICE_TYPE         - Device type identifier
#                                  (default: ${MENDER_DEVICE_TYPE}, which itself
#                                  defaults to ${MACHINE})
#   OTAPULSE_ARTIFACT_COMPRESSION - Compression: "gzip", "lzma", "none"
#                                  (default: ${MENDER_ARTIFACT_COMPRESSION}, i.e. gzip)
#   SOC_OTA_SIGNING_KEY          - Path to private key for signing (optional)
#   SOC_OTA_SIGNING_CERT         - Path to signing certificate (optional)
#
# The OTAPULSE_* variables are the primary names. The legacy MENDER_DEVICE_TYPE /
# MENDER_ARTIFACT_COMPRESSION names are still honoured as the fallback values, so
# existing board configs that set them keep working unchanged.
#
# Tasks: do_generate_otapulse_artifact (signed, or unsigned when no key is
# configured) and do_generate_otapulse_artifact_unsigned (an extra unsigned copy
# for testing when signing is enabled).
#
# Requires: the otapulse-artifact tool installed on the build host
#   (scripts/install-otapulse-artifact.sh in the project repo; the legacy
#   mender-artifact binary is still accepted as a fallback).
# Artifact format id: when the tool found supports `write --format`, this class
# passes `--format otapulse` so the artifact's `version` member reads
# {"format":"otapulse","version":3}. An older tool without that flag is run with
# its default format (probed at task time, never assumed).

MENDER_DEVICE_TYPE ?= "${MACHINE}"
MENDER_ARTIFACT_COMPRESSION ?= "gzip"
OTAPULSE_DEVICE_TYPE ??= "${MENDER_DEVICE_TYPE}"
OTAPULSE_ARTIFACT_COMPRESSION ??= "${MENDER_ARTIFACT_COMPRESSION}"
SOC_OTA_SIGNING_KEY ?= ""
SOC_OTA_SIGNING_CERT ?= ""

# Stage OTA state scripts out of the rootfs into a stable WORKDIR location
# WHILE the rootfs still exists (end of do_rootfs). do_generate_otapulse_artifact
# runs much later (after do_image_complete) and builds the artifact from the
# .ext4 file — by then ${IMAGE_ROOTFS} may be emptied/removed (rm_work, image
# postprocessing), so reading state scripts directly from it there is unreliable.
# Providing recipes install Artifact* transition scripts (ArtifactReboot_Enter/_Leave,
# ArtifactCommit_*, ...) to ${datadir}/otapulse/state-scripts in the rootfs.
MENDER_STATE_SCRIPTS_STAGE = "${WORKDIR}/otapulse-mender-scripts"
OTAPULSE_STATE_SCRIPTS_STAGE ??= "${MENDER_STATE_SCRIPTS_STAGE}"

otapulse_stage_state_scripts() {
    src="${IMAGE_ROOTFS}/usr/share/otapulse/state-scripts"
    dst="${OTAPULSE_STATE_SCRIPTS_STAGE}"
    rm -rf "$dst"
    if [ -d "$src" ]; then
        install -d "$dst"
        cp -a "$src"/. "$dst"/ 2>/dev/null || true
    fi
}
ROOTFS_POSTPROCESS_COMMAND:append = " otapulse_stage_state_scripts;"

# Probe whether the artifact tool understands `write --format` (the otapulse
# format-id flag). Returns ['--format', 'otapulse'] when it does, [] otherwise
# (older tool / legacy mender-artifact: default format, nothing is assumed).
def otapulse_artifact_format_args(tool, env):
    import subprocess
    try:
        r = subprocess.run([tool, 'write', 'rootfs-image', '--help'],
                           capture_output=True, text=True, timeout=30, env=env)
        if '--format' in (r.stdout or '') + (r.stderr or ''):
            return ['--format', 'otapulse']
    except Exception as e:
        bb.note("Could not probe %s for --format support: %s" % (tool, e))
    return []

python do_generate_otapulse_artifact() {
    import os
    import subprocess
    import time

    deploy_dir = d.getVar('DEPLOY_DIR_IMAGE')
    image_basename = d.getVar('IMAGE_BASENAME')
    machine = d.getVar('MACHINE')
    device_type = d.getVar('OTAPULSE_DEVICE_TYPE')
    compression = d.getVar('OTAPULSE_ARTIFACT_COMPRESSION') or 'gzip'
    signing_key = d.getVar('SOC_OTA_SIGNING_KEY')
    signing_cert = d.getVar('SOC_OTA_SIGNING_CERT')

    # Find the OTA artifact tool binary in common locations: prefer the
    # otapulse-artifact wrapper (pass-through to the real mender-artifact),
    # fall back to mender-artifact directly.
    artifact_tool_bin = None
    for path in ['/usr/bin/otapulse-artifact', '/usr/local/bin/otapulse-artifact',
                 os.path.expanduser('~/bin/otapulse-artifact'),
                 os.path.expanduser('~/.local/bin/otapulse-artifact'),
                 '/usr/bin/mender-artifact', '/usr/local/bin/mender-artifact',
                 os.path.expanduser('~/bin/mender-artifact'),
                 os.path.expanduser('~/.local/bin/mender-artifact')]:
        if os.path.isfile(path) and os.access(path, os.X_OK):
            artifact_tool_bin = path
            break

    if not artifact_tool_bin:
        bb.warn("OTA artifact tool (otapulse-artifact/mender-artifact) not found in /usr/bin, /usr/local/bin, ~/bin, or ~/.local/bin")
        bb.warn("Skipping OTA artifact generation")
        return

    # Set up environment with libssl1.1 compatibility library path
    # the artifact tool binary requires libssl1.1 which may not be installed on newer systems
    run_env = os.environ.copy()
    libssl_compat_dir = os.path.expanduser('~/.local/lib/mender-artifact-deps')
    if os.path.isdir(libssl_compat_dir):
        current_ld_path = run_env.get('LD_LIBRARY_PATH', '')
        if current_ld_path:
            run_env['LD_LIBRARY_PATH'] = libssl_compat_dir + ':' + current_ld_path
        else:
            run_env['LD_LIBRARY_PATH'] = libssl_compat_dir
        bb.note("Using libssl1.1 compatibility libraries from: %s" % libssl_compat_dir)

    # Build version
    topdir = d.getVar('TOPDIR')
    try:
        with open(os.path.join(topdir, '..', 'BUILD_VERSION'), 'r') as f:
            build_num = f.read().strip()
    except:
        build_num = "0"

    timestamp = time.strftime('%Y%m%d%H%M%S', time.gmtime())
    distro_version = d.getVar('DISTRO_VERSION') or '1.0.0'
    artifact_name = "%s-%s-build%s-%s" % (image_basename, distro_version, build_num, timestamp)

    # Find ext4 image
    rootfs_image = None
    for f in os.listdir(deploy_dir):
        if f.endswith('.ext4') and image_basename in f and not f.endswith('.ext4.gz'):
            rootfs_image = os.path.join(deploy_dir, f)
            break

    if not rootfs_image:
        bb.warn("No ext4 rootfs found for OTA artifact generation")
        return

    # Determine output filename
    if signing_key and os.path.isfile(signing_key):
        artifact_out = os.path.join(deploy_dir, "%s-%s-signed.otapulse" % (image_basename, machine))
    else:
        artifact_out = os.path.join(deploy_dir, "%s-%s.otapulse" % (image_basename, machine))

    # Build the command
    cmd = [artifact_tool_bin, 'write', 'rootfs-image',
           '--file', rootfs_image,
           '--output-path', artifact_out,
           '--artifact-name', artifact_name,
           '--device-type', device_type]

    # Embed OTA state scripts (ArtifactReboot_Enter/_Leave, ArtifactCommit_*, ...).
    # Artifact* transition scripts are taken from the ARTIFACT at update time, not the
    # rootfs, so they MUST be embedded here via --script or they never run on device.
    # They are staged out of the rootfs at do_rootfs time (otapulse_stage_state_scripts)
    # because ${IMAGE_ROOTFS} is unreliable this late in the image build.
    state_scripts_dir = d.getVar('OTAPULSE_STATE_SCRIPTS_STAGE')
    if state_scripts_dir and os.path.isdir(state_scripts_dir):
        for sf in sorted(os.listdir(state_scripts_dir)):
            sp = os.path.join(state_scripts_dir, sf)
            if os.path.isfile(sp) and sf != 'version':
                cmd.extend(['--script', sp])
                bb.note("Embedding OTA state script: %s" % sf)

    # Add compression option
    if compression and compression != 'none':
        cmd.extend(['--compression', compression])

    # Artifact format id: only when the tool found supports it (probed, not assumed)
    fmt_args = otapulse_artifact_format_args(artifact_tool_bin, run_env)
    cmd.extend(fmt_args)

    # Add signing options if private key is provided
    signing_enabled = False
    if signing_key and os.path.isfile(signing_key):
        signing_enabled = True
        cmd.extend(['--key', signing_key])
        bb.note("Signing artifact with key: %s" % signing_key)

        # Add certificate if provided
        if signing_cert and os.path.isfile(signing_cert):
            # Check if it's an RSA or ECDSA setup based on key type
            # the artifact tool uses different flags for different scenarios
            bb.note("Using signing certificate: %s" % signing_cert)
    elif signing_key:
        bb.warn("Signing key specified but not found: %s" % signing_key)
        bb.warn("Artifact will be created WITHOUT signature")

    bb.note("=" * 60)
    bb.note("Generating OTA-Pulse Artifact")
    bb.note("=" * 60)
    bb.note("  Input:       %s" % rootfs_image)
    bb.note("  Output:      %s" % artifact_out)
    bb.note("  Artifact:    %s" % artifact_name)
    bb.note("  Device Type: %s" % device_type)
    bb.note("  Compression: %s" % compression)
    bb.note("  Format:      %s" % (fmt_args[1] if fmt_args else "tool default"))
    bb.note("  Signed:      %s" % ("Yes" if signing_enabled else "No"))
    bb.note("=" * 60)

    try:
        result = subprocess.run(cmd, check=True, capture_output=True, text=True, env=run_env)
        bb.note("OTA artifact created successfully: %s" % artifact_out)

        # Show artifact info
        try:
            info_result = subprocess.run([artifact_tool_bin, 'read', artifact_out],
                                        capture_output=True, text=True, timeout=30, env=run_env)
            if info_result.returncode == 0:
                bb.note("Artifact info:\n%s" % info_result.stdout[:2000])
        except Exception as e:
            bb.note("Could not read artifact info: %s" % str(e))

        # Verify signature if artifact was signed
        if signing_enabled:
            bb.note("Artifact was signed with private key")
            bb.note("To verify on device, ensure corresponding public key is deployed")

    except subprocess.CalledProcessError as e:
        bb.error("OTA artifact tool failed with exit code %d" % e.returncode)
        bb.error("Command: %s" % ' '.join(cmd))
        if e.stdout:
            bb.error("stdout: %s" % e.stdout)
        if e.stderr:
            bb.error("stderr: %s" % e.stderr)
        bb.fatal("Failed to create OTA artifact")
}

addtask do_generate_otapulse_artifact after do_image_complete before do_build
do_generate_otapulse_artifact[nostamp] = "1"

# Also generate an unsigned artifact if signing is enabled (for testing)
python do_generate_otapulse_artifact_unsigned() {
    import os
    import subprocess
    import time

    signing_key = d.getVar('SOC_OTA_SIGNING_KEY')

    # Only generate unsigned if signing is enabled
    if not signing_key or not os.path.isfile(signing_key):
        return

    deploy_dir = d.getVar('DEPLOY_DIR_IMAGE')
    image_basename = d.getVar('IMAGE_BASENAME')
    machine = d.getVar('MACHINE')
    device_type = d.getVar('OTAPULSE_DEVICE_TYPE')
    compression = d.getVar('OTAPULSE_ARTIFACT_COMPRESSION') or 'gzip'

    # Find the OTA artifact tool binary: prefer the otapulse-artifact
    # wrapper (pass-through to the real mender-artifact), fall back to
    # mender-artifact directly.
    artifact_tool_bin = None
    for path in ['/usr/bin/otapulse-artifact', '/usr/local/bin/otapulse-artifact',
                 os.path.expanduser('~/bin/otapulse-artifact'),
                 os.path.expanduser('~/.local/bin/otapulse-artifact'),
                 '/usr/bin/mender-artifact', '/usr/local/bin/mender-artifact',
                 os.path.expanduser('~/bin/mender-artifact'),
                 os.path.expanduser('~/.local/bin/mender-artifact')]:
        if os.path.isfile(path) and os.access(path, os.X_OK):
            artifact_tool_bin = path
            break

    if not artifact_tool_bin:
        return

    # Set up environment with libssl1.1 compatibility library path
    run_env = os.environ.copy()
    libssl_compat_dir = os.path.expanduser('~/.local/lib/mender-artifact-deps')
    if os.path.isdir(libssl_compat_dir):
        current_ld_path = run_env.get('LD_LIBRARY_PATH', '')
        if current_ld_path:
            run_env['LD_LIBRARY_PATH'] = libssl_compat_dir + ':' + current_ld_path
        else:
            run_env['LD_LIBRARY_PATH'] = libssl_compat_dir

    # Build version
    topdir = d.getVar('TOPDIR')
    try:
        with open(os.path.join(topdir, '..', 'BUILD_VERSION'), 'r') as f:
            build_num = f.read().strip()
    except:
        build_num = "0"

    timestamp = time.strftime('%Y%m%d%H%M%S', time.gmtime())
    distro_version = d.getVar('DISTRO_VERSION') or '1.0.0'
    artifact_name = "%s-%s-build%s-%s-unsigned" % (image_basename, distro_version, build_num, timestamp)

    # Find ext4 image
    rootfs_image = None
    for f in os.listdir(deploy_dir):
        if f.endswith('.ext4') and image_basename in f and not f.endswith('.ext4.gz'):
            rootfs_image = os.path.join(deploy_dir, f)
            break

    if not rootfs_image:
        return

    artifact_out = os.path.join(deploy_dir, "%s-%s-unsigned.otapulse" % (image_basename, machine))

    cmd = [artifact_tool_bin, 'write', 'rootfs-image',
           '--file', rootfs_image,
           '--output-path', artifact_out,
           '--artifact-name', artifact_name,
           '--device-type', device_type]

    # Embed OTA state scripts (see do_generate_otapulse_artifact for rationale).
    state_scripts_dir = d.getVar('OTAPULSE_STATE_SCRIPTS_STAGE')
    if state_scripts_dir and os.path.isdir(state_scripts_dir):
        for sf in sorted(os.listdir(state_scripts_dir)):
            sp = os.path.join(state_scripts_dir, sf)
            if os.path.isfile(sp) and sf != 'version':
                cmd.extend(['--script', sp])

    if compression and compression != 'none':
        cmd.extend(['--compression', compression])
    cmd.extend(otapulse_artifact_format_args(artifact_tool_bin, run_env))

    bb.note("Generating unsigned artifact for testing: %s" % artifact_out)

    try:
        result = subprocess.run(cmd, check=True, capture_output=True, text=True, env=run_env)
        bb.note("Unsigned artifact created: %s" % artifact_out)
    except subprocess.CalledProcessError as e:
        bb.warn("Failed to create unsigned artifact: %s" % e.stderr)
}

addtask do_generate_otapulse_artifact_unsigned after do_generate_otapulse_artifact before do_build
do_generate_otapulse_artifact_unsigned[nostamp] = "1"
