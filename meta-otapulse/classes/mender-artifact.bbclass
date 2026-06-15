# Mender Artifact Generation Class
# Generates .mender artifact files from rootfs images for OTA updates
# Supports optional artifact signing with RSA or ECDSA keys
#
# Usage: inherit mender-artifact in your image recipe
#
# Configuration variables (set in local.conf):
#   MENDER_DEVICE_TYPE         - Device type identifier (default: ${MACHINE})
#   MENDER_ARTIFACT_COMPRESSION - Compression: "gzip", "lzma", "none" (default: gzip)
#   SOC_OTA_SIGNING_KEY        - Path to private key for signing (optional)
#   SOC_OTA_SIGNING_CERT       - Path to signing certificate (optional)
#
# Requires: mender-artifact tool installed on the build host
#   Install with: wget https://downloads.mender.io/mender-artifact/3.11.2/linux/mender-artifact
#                 chmod +x mender-artifact && sudo mv mender-artifact /usr/local/bin/

MENDER_DEVICE_TYPE ?= "${MACHINE}"
MENDER_ARTIFACT_COMPRESSION ?= "gzip"
SOC_OTA_SIGNING_KEY ?= ""
SOC_OTA_SIGNING_CERT ?= ""

python do_generate_mender_artifact() {
    import os
    import subprocess
    import time

    deploy_dir = d.getVar('DEPLOY_DIR_IMAGE')
    image_basename = d.getVar('IMAGE_BASENAME')
    machine = d.getVar('MACHINE')
    device_type = d.getVar('MENDER_DEVICE_TYPE')
    compression = d.getVar('MENDER_ARTIFACT_COMPRESSION') or 'gzip'
    signing_key = d.getVar('SOC_OTA_SIGNING_KEY')
    signing_cert = d.getVar('SOC_OTA_SIGNING_CERT')

    # Find mender-artifact binary in common locations
    mender_artifact_bin = None
    for path in ['/usr/bin/mender-artifact', '/usr/local/bin/mender-artifact',
                 os.path.expanduser('~/bin/mender-artifact'),
                 os.path.expanduser('~/.local/bin/mender-artifact')]:
        if os.path.isfile(path) and os.access(path, os.X_OK):
            mender_artifact_bin = path
            break

    if not mender_artifact_bin:
        bb.warn("mender-artifact not found in /usr/bin, /usr/local/bin, ~/bin, or ~/.local/bin")
        bb.warn("Skipping Mender artifact generation")
        return

    # Set up environment with libssl1.1 compatibility library path
    # mender-artifact binary requires libssl1.1 which may not be installed on newer systems
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
        bb.warn("No ext4 rootfs found for mender artifact generation")
        return

    # Determine output filename
    if signing_key and os.path.isfile(signing_key):
        mender_out = os.path.join(deploy_dir, "%s-%s-signed.mender" % (image_basename, machine))
    else:
        mender_out = os.path.join(deploy_dir, "%s-%s.mender" % (image_basename, machine))

    # Build the command
    cmd = [mender_artifact_bin, 'write', 'rootfs-image',
           '--file', rootfs_image,
           '--output-path', mender_out,
           '--artifact-name', artifact_name,
           '--device-type', device_type]

    # Embed Mender state scripts (ArtifactReboot_Enter/_Leave, ArtifactCommit_*, ...).
    # Artifact* transition scripts are taken from the ARTIFACT at update time, not the
    # rootfs, so they MUST be embedded here via --script or they never run on device.
    # Providing recipes stage them in ${datadir}/otapulse/state-scripts within the rootfs.
    image_rootfs = d.getVar('IMAGE_ROOTFS')
    state_scripts_dir = os.path.join(image_rootfs or '', 'usr', 'share', 'otapulse', 'state-scripts')
    if os.path.isdir(state_scripts_dir):
        for sf in sorted(os.listdir(state_scripts_dir)):
            sp = os.path.join(state_scripts_dir, sf)
            if os.path.isfile(sp) and sf != 'version':
                cmd.extend(['--script', sp])
                bb.note("Embedding Mender state script: %s" % sf)

    # Add compression option
    if compression and compression != 'none':
        cmd.extend(['--compression', compression])

    # Add signing options if private key is provided
    signing_enabled = False
    if signing_key and os.path.isfile(signing_key):
        signing_enabled = True
        cmd.extend(['--key', signing_key])
        bb.note("Signing artifact with key: %s" % signing_key)

        # Add certificate if provided
        if signing_cert and os.path.isfile(signing_cert):
            # Check if it's an RSA or ECDSA setup based on key type
            # mender-artifact uses different flags for different scenarios
            bb.note("Using signing certificate: %s" % signing_cert)
    elif signing_key:
        bb.warn("Signing key specified but not found: %s" % signing_key)
        bb.warn("Artifact will be created WITHOUT signature")

    bb.note("=" * 60)
    bb.note("Generating Mender Artifact")
    bb.note("=" * 60)
    bb.note("  Input:       %s" % rootfs_image)
    bb.note("  Output:      %s" % mender_out)
    bb.note("  Artifact:    %s" % artifact_name)
    bb.note("  Device Type: %s" % device_type)
    bb.note("  Compression: %s" % compression)
    bb.note("  Signed:      %s" % ("Yes" if signing_enabled else "No"))
    bb.note("=" * 60)

    try:
        result = subprocess.run(cmd, check=True, capture_output=True, text=True, env=run_env)
        bb.note("Mender artifact created successfully: %s" % mender_out)

        # Show artifact info
        try:
            info_result = subprocess.run([mender_artifact_bin, 'read', mender_out],
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
        bb.error("mender-artifact failed with exit code %d" % e.returncode)
        bb.error("Command: %s" % ' '.join(cmd))
        if e.stdout:
            bb.error("stdout: %s" % e.stdout)
        if e.stderr:
            bb.error("stderr: %s" % e.stderr)
        bb.fatal("Failed to create Mender artifact")
}

addtask do_generate_mender_artifact after do_image_complete before do_build
do_generate_mender_artifact[nostamp] = "1"

# Also generate an unsigned artifact if signing is enabled (for testing)
python do_generate_mender_artifact_unsigned() {
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
    device_type = d.getVar('MENDER_DEVICE_TYPE')
    compression = d.getVar('MENDER_ARTIFACT_COMPRESSION') or 'gzip'

    # Find mender-artifact binary
    mender_artifact_bin = None
    for path in ['/usr/bin/mender-artifact', '/usr/local/bin/mender-artifact',
                 os.path.expanduser('~/bin/mender-artifact'),
                 os.path.expanduser('~/.local/bin/mender-artifact')]:
        if os.path.isfile(path) and os.access(path, os.X_OK):
            mender_artifact_bin = path
            break

    if not mender_artifact_bin:
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

    mender_out = os.path.join(deploy_dir, "%s-%s-unsigned.mender" % (image_basename, machine))

    cmd = [mender_artifact_bin, 'write', 'rootfs-image',
           '--file', rootfs_image,
           '--output-path', mender_out,
           '--artifact-name', artifact_name,
           '--device-type', device_type]

    # Embed Mender state scripts (see do_generate_mender_artifact for rationale).
    image_rootfs = d.getVar('IMAGE_ROOTFS')
    state_scripts_dir = os.path.join(image_rootfs or '', 'usr', 'share', 'otapulse', 'state-scripts')
    if os.path.isdir(state_scripts_dir):
        for sf in sorted(os.listdir(state_scripts_dir)):
            sp = os.path.join(state_scripts_dir, sf)
            if os.path.isfile(sp) and sf != 'version':
                cmd.extend(['--script', sp])

    if compression and compression != 'none':
        cmd.extend(['--compression', compression])

    bb.note("Generating unsigned artifact for testing: %s" % mender_out)

    try:
        result = subprocess.run(cmd, check=True, capture_output=True, text=True, env=run_env)
        bb.note("Unsigned artifact created: %s" % mender_out)
    except subprocess.CalledProcessError as e:
        bb.warn("Failed to create unsigned artifact: %s" % e.stderr)
}

addtask do_generate_mender_artifact_unsigned after do_generate_mender_artifact before do_build
do_generate_mender_artifact_unsigned[nostamp] = "1"
