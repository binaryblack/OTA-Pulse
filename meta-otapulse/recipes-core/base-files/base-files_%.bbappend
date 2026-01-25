# Add custom fstab with data partition for OTA storage
FILESEXTRAPATHS:prepend := "${THISDIR}/${PN}:"

# Use our custom fstab
SRC_URI += "file://fstab"

do_install:append() {
    # Install our custom fstab
    install -m 0644 ${WORKDIR}/fstab ${D}${sysconfdir}/fstab

    # Create /data mount point
    install -d ${D}/data
}

# Ensure /data directory is included in the image
FILES:${PN} += "/data"
