# The base image is pinned by digest (ubuntu:24.04). The apt packages installed below come from the
# package repositories at build time and are not pinned.
FROM ubuntu@sha256:534baea6a22c03a63003dbc8dbe78fe34bc0d7e595d9a9dc9834884ff530eb55

RUN apt-get update && env DEBIAN_FRONTEND=noninteractive apt-get install --assume-yes \
    sudo \
# to extract binaries from .deb
    zstd \
# to be able to detect file system types of extracted images
    file \
# to copy files between rootfs directories
    rsync \
# to generate partition table and alter partitions
    parted \
    gdisk \
# mkfs.ext4 and family
    e2fsprogs \
# mkfs.xfs and family
    xfsprogs \
# mkfs.btrfs and family
    btrfs-progs \
# parallel gzip compression
    pigz \
# mkfs.vfat (required for boot partition)
    dosfstools \
# to download assets and to be able to reach the package repositories of the image
    wget \
    ca-certificates \
# to build the bootloader environment scripts
    make \
# to get rid of 'sh: 1: udevadm: not found' errors triggered by parted
    udev \
# to create bmap index file (OTAPULSE_USE_BMAP)
    bmap-tools \
# to regenerate the U-Boot boot.scr on platforms that need customization
    u-boot-tools \
# libraries of the artifact tool
    libssl3t64 \
    liblzma5 \
# artifact compression
    zip  \
    unzip \
    xz-utils \
# manipulate binary and hex
    xxd \
# JSON power tool
    jq \
# GRUB command line tools, primarily grub-probe
    grub-common \
# to be able to run package installations on foreign architectures
    binfmt-support \
    qemu-user-static

# allow us to keep original PATH variables when sudoing
RUN echo "Defaults        secure_path=\"/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/snap/bin:$PATH\"" > /etc/sudoers.d/secure_path_override
RUN chmod 0440 /etc/sudoers.d/secure_path_override

# The artifact tool, built by scripts/build-otapulse-artifact.sh and placed into the build context
COPY bin/otapulse-artifact /usr/local/bin/otapulse-artifact
RUN chmod 0755 /usr/local/bin/otapulse-artifact && otapulse-artifact --version

WORKDIR /

COPY . /otapulse-convert

RUN mkdir -p /otapulse-convert/work
RUN mkdir -p /otapulse-convert/input
RUN mkdir -p /otapulse-convert/deploy
RUN mkdir -p /otapulse-convert/logs

# Nobody but the owner may write to the tool.
RUN chmod -R go-w /otapulse-convert

VOLUME ["/otapulse-convert/configs"]
VOLUME ["/otapulse-convert/input"]
VOLUME ["/otapulse-convert/deploy"]
VOLUME ["/otapulse-convert/logs"]
VOLUME ["/otapulse-convert/work"]

ENTRYPOINT ["/otapulse-convert/docker-entrypoint.sh"]
