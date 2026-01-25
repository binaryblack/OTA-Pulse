# Build Version Management Class
# Automatically increments build version on each image build
# and embeds it into the rootfs for runtime identification

BUILD_VERSION_FILE = "${TOPDIR}/../BUILD_VERSION"

python do_embed_build_version() {
    """Embed build version info into rootfs"""
    import os
    import time
    import subprocess

    rootfs = d.getVar('IMAGE_ROOTFS', True)
    version_file = os.path.join(rootfs, 'etc', 'build-version')
    info_file = os.path.join(rootfs, 'etc', 'build-info')

    # Generate timestamp at execution time (not parse time)
    timestamp = time.strftime('%Y%m%d-%H%M%S', time.gmtime())

    # Get build number at execution time
    build_version_file = d.getVar('BUILD_VERSION_FILE', True)
    try:
        with open(build_version_file, 'r') as f:
            build_number = f.read().strip()
    except:
        build_number = "1"

    # Get git commit at execution time
    try:
        topdir = d.getVar('TOPDIR', True)
        git_commit = subprocess.check_output(
            ['git', 'rev-parse', '--short', 'HEAD'],
            cwd=topdir + '/..',
            stderr=subprocess.DEVNULL,
            universal_newlines=True
        ).strip()
    except:
        git_commit = "unknown"

    build_version = "%s.%s.%s" % (build_number, timestamp, git_commit)

    machine = d.getVar('MACHINE', True)
    distro = d.getVar('DISTRO', True)
    distro_version = d.getVar('DISTRO_VERSION', True) or 'unknown'
    image_basename = d.getVar('IMAGE_BASENAME', True)

    # Write simple version file
    with open(version_file, 'w') as f:
        f.write(build_version + '\n')
    os.chmod(version_file, 0o644)

    # Write detailed build info
    with open(info_file, 'w') as f:
        f.write("BUILD_VERSION=%s\n" % build_version)
        f.write("BUILD_NUMBER=%s\n" % build_number)
        f.write("BUILD_TIMESTAMP=%s\n" % timestamp)
        f.write("GIT_COMMIT=%s\n" % git_commit)
        f.write("MACHINE=%s\n" % machine)
        f.write("DISTRO=%s\n" % distro)
        f.write("DISTRO_VERSION=%s\n" % distro_version)
        f.write("IMAGE_BASENAME=%s\n" % image_basename)
    os.chmod(info_file, 0o644)

    bb.note("Embedded build version: %s" % build_version)
}

python do_increment_build_version() {
    """Task to increment build number - runs once per image build"""
    import os

    version_file = d.getVar('BUILD_VERSION_FILE', True)

    # Read current
    try:
        with open(version_file, 'r') as f:
            build_num = int(f.read().strip())
    except:
        build_num = 0

    # Increment and write
    new_num = build_num + 1
    try:
        with open(version_file, 'w') as f:
            f.write(str(new_num) + '\n')
        bb.note("Build version incremented: %d -> %d" % (build_num, new_num))
    except Exception as e:
        bb.warn("Failed to increment build version: %s" % str(e))
}

# This task runs early, before do_rootfs
addtask do_increment_build_version before do_rootfs
do_increment_build_version[nostamp] = "1"
do_increment_build_version[lockfiles] = "${BUILD_VERSION_FILE}.lock"

# Embed version info during rootfs generation
ROOTFS_POSTPROCESS_COMMAND += "do_embed_build_version;"
