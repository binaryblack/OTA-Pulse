# QEMU/test-only OpenSSH configuration
# DO NOT upstream to OTA-Pulse — these workarounds are specific to emulated ARM64 QEMU.
#
# GAP-SEC-F5: this bbappend used to apply to EVERY machine (openssh_%, no
# override) — every real hardware board shared IDENTICAL baked-in
# ssh_host_*_key across the whole fleet, and (if OTAPULSE_SSH_PUBKEY was ever
# set for a non-test build) would install a fleet-wide root authorized_keys.
# Now scoped to :qemuall — the MACHINEOVERRIDES poky's own
# conf/machine/include/qemu.inc adds for every qemu* MACHINE (qemuarm64,
# qemux86-64 in this project; matches the same pattern poky itself uses in
# recipes-connectivity/connman/connman-conf.bb's do_install:append:qemuall).
# Every other machine gets openssh's stock, unmodified behavior:
# sshdgenkeys.service runs sshd_check_keys at first boot and generates
# unique per-device host keys (RSA + ECDSA + ED25519), and no root
# authorized_keys is installed.
#
# Problems solved (QEMU only):
#   1. 'sshd -G' (used by sshdgenkeys.service) hangs indefinitely on emulated ARM64.
#      sshdgenkeys is overridden to a no-op, and host keys are pre-generated here on
#      the fast x86_64 build host instead.
#   2. inetd-mode sshd (sshd.socket + sshd@.service) loads the sshd binary per-connection;
#      on emulated ARM64 this takes 20+ seconds, exceeding the test SSH timeout.
#      Replaced with daemon-mode sshd.service (starts once at boot, forks per connection).
#   3. RSA key generation on emulated ARM64 takes 20+ minutes.
#      Host keys are restricted to ECDSA and ED25519 (fast to generate and use).

OTAPULSE_SSH_PUBKEY ?= ""

do_install:append:qemuall() {
    # --- Authorized keys for test SSH access ---
    if [ -n "${OTAPULSE_SSH_PUBKEY}" ] && [ -f "${OTAPULSE_SSH_PUBKEY}" ]; then
        install -d -m 0700 ${D}${ROOT_HOME}/.ssh
        install -m 0600 ${OTAPULSE_SSH_PUBKEY} ${D}${ROOT_HOME}/.ssh/authorized_keys
    fi

    # --- Pre-generate host keys on the x86_64 build host ---
    # sshdgenkeys.service is overridden to a no-op below, so keys must exist in
    # the image before first boot. Generating on x86_64 is instantaneous.
    # Only ECDSA and ED25519 — RSA is excluded (slow on emulated ARM64 even with
    # pre-generated keys, and the 50-hostkeys.conf drop-in below disables it anyway).
    install -d -m 0755 ${D}${sysconfdir}/ssh
    for TYPE in ecdsa ed25519; do
        KEY="${D}${sysconfdir}/ssh/ssh_host_${TYPE}_key"
        if [ ! -f "${KEY}" ]; then
            /usr/bin/ssh-keygen -q -f "${KEY}" -N '' -t ${TYPE}
            chmod 600 "${KEY}"
            chmod 644 "${KEY}.pub"
        fi
    done

    # --- Restrict host key types to ECDSA and ED25519 ---
    install -d -m 0755 ${D}${sysconfdir}/ssh/sshd_config.d
    cat > ${D}${sysconfdir}/ssh/sshd_config.d/50-hostkeys.conf << 'EOF'
# QEMU test: restrict to fast key types only (RSA excluded — too slow on emulated ARM64)
HostKey /etc/ssh/ssh_host_ecdsa_key
HostKey /etc/ssh/ssh_host_ed25519_key
EOF

    # --- Override sshdgenkeys.service to be a no-op ---
    # Keys are pre-generated above; runtime generation would re-run 'sshd -G'
    # which hangs on emulated ARM64.
    install -d -m 0755 ${D}${sysconfdir}/systemd/system/sshdgenkeys.service.d
    cat > ${D}${sysconfdir}/systemd/system/sshdgenkeys.service.d/50-pregenerated.conf << 'EOF'
[Service]
ExecStart=
ExecStart=/bin/true
EOF

    # --- Switch to daemon-mode sshd ---
    # inetd mode (sshd.socket + sshd@.service) loads the sshd binary per connection;
    # on emulated ARM64 this takes 20+ seconds each time.
    # Daemon mode starts sshd once at boot and forks per connection — one-time 20s
    # cost, then instant banner for all subsequent connections.
    install -d -m 0755 ${D}${sysconfdir}/systemd/system
    cat > ${D}${sysconfdir}/systemd/system/sshd.service << 'EOF'
[Unit]
Description=OpenSSH Daemon
# Host keys are plain files baked into the image (no /data symlinks), so sshd
# has no dependency on otapulse-partition-setup. Gating on it only risked the
# 900s TimeoutStartSec stall that left beagleplay SSH-dead after reboot (BUG-001).
After=network.target sshdgenkeys.service

[Service]
EnvironmentFile=-/etc/default/ssh
ExecStartPre=/usr/bin/mkdir -p /var/run/sshd
ExecStart=/usr/sbin/sshd -D $SSHD_OPTS
ExecReload=/bin/kill -HUP $MAINPID
KillMode=process
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
    install -d -m 0755 ${D}${sysconfdir}/systemd/system/multi-user.target.wants
    ln -sf /etc/systemd/system/sshd.service \
           ${D}${sysconfdir}/systemd/system/multi-user.target.wants/sshd.service
    # Mask sshd.socket so inetd mode doesn't start alongside daemon mode
    ln -sf /dev/null ${D}${sysconfdir}/systemd/system/sshd.socket
}

FILES:${PN}:append:qemuall = " \
    ${ROOT_HOME}/.ssh \
    ${sysconfdir}/ssh/ssh_host_ecdsa_key \
    ${sysconfdir}/ssh/ssh_host_ecdsa_key.pub \
    ${sysconfdir}/ssh/ssh_host_ed25519_key \
    ${sysconfdir}/ssh/ssh_host_ed25519_key.pub \
    ${sysconfdir}/ssh/sshd_config.d \
    ${sysconfdir}/systemd/system/sshdgenkeys.service.d \
    ${sysconfdir}/systemd/system/sshd.service \
    ${sysconfdir}/systemd/system/multi-user.target.wants/sshd.service \
    ${sysconfdir}/systemd/system/sshd.socket \
    "
