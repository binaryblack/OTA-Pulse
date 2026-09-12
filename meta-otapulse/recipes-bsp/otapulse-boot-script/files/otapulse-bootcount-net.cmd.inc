# OTAPulse generic pre-kernel boot-count self-revert net (TODO-049 / S55)
#
# Spliced verbatim into any boot-${MACHINE}.cmd that contains the literal
# marker line "@@OTAPULSE_BOOTCOUNT_NET@@" (see otapulse-boot-script_1.0.bb's
# do_compile). Generalizes the block proven live on orange-pi-zero2w
# (BUG-310, BUG-354) into one shared, board-agnostic fragment.
#
# CALLER CONTRACT — the including board script MUST, before the marker line:
#   - setenv mmcdev / mmcpart to the real boot device/partition
#   - already have resolved `bootpart` to 2 or 3 (this fragment may revert it)
#   - already have loaded the mender_boot_part byte into `scriptaddr` (this
#     fragment reuses that same scratch address for mender_boot_part_prev)
# and MUST, after the marker line, append "${recoveryargs}" to its bootargs
# line — otherwise the panic-on-pending-boot safety net (below) never
# actually reaches the kernel command line and this fragment degrades to
# counting-only with no fail-fast benefit. otapulse-boot-script_1.0.bb's
# splice step enforces both preconditions at build time (bbfatal otherwise).
#
# Optional caller inputs (defaulted below if unset): bootiface (mmc), and
# scriptaddr2 (a second scratch address, ${scriptaddr} + 0x10000).
#
# WHY this exists as a U-Boot-level net, separate from otapulse-boot-health:
# otapulse-boot-health (meta-otapulse/recipes-core/otapulse-firstboot/files/
# otapulse-boot-health, a systemd oneshot ordered After=local-fs.target)
# already reverts a bad slot after 3 failed Linux boots — but it depends on
# the kernel successfully mounting root and starting systemd. It cannot run
# at all if the new slot's rootfs never gets that far (the imx8mp-frdm
# incident this sprint traces to: a corrupted rootfs superblock panics the
# kernel in mount_root before ANY userspace code executes — nothing below
# the kernel is watching). This fragment is a LAST-RESORT backstop that
# runs entirely inside U-Boot, before Linux is ever loaded, so it survives
# that case too.
#
# mender_boot_part_prev is written by the on-device agent BEFORE it
# switches mender_boot_part (file_bootenv.go), and neutralised (to the
# sentinel byte 'X', 0x58 — never deleted, since fatwrite has no delete) by
# whichever of {this fragment, Linux-side commit/hygiene cleanup} resolves
# it first. Its presence means "there is an unconfirmed pending switch"; its
# absence, or the sentinel, means this whole block is a strict no-op.
#
# Threshold is 5, deliberately HIGHER than otapulse-boot-health's own
# MAX_BOOT_RETRIES (3): whenever Linux DOES boot far enough to run systemd
# services, that existing mechanism reverts first (at attempt 3) and this
# counter never reaches 5. It only becomes the deciding layer when Linux
# never runs at all. Both constants are cross-referenced from each other's
# implementation — change one, check the other.
#
# No arithmetic on loaded byte values anywhere below (BUG-052 lesson: a
# loaded byte's value can compare as EITHER its decimal value or its
# hex-digit-string depending on context — e.g. ASCII '2' (0x32) can read
# back as 50 or 32 — so every comparison below tests both forms rather than
# doing math on an ambiguously-typed value). Every value WRITTEN back to the
# FAT is an explicit 0x-prefixed hex literal, so there is no write-side
# ambiguity either.
#
# Fable review correction (2026-09-12): U-Boot's fatwrite DOES create a
# file that does not yet exist (verified: this is how uboot_boot_count
# first appears on a board that has never had a pending switch before).
# The on-device agent (file_bootenv.go) does NOT pre-create
# uboot_boot_count today either -- its cycle-start and commit-time writers
# both gate on os.Stat succeeding first (i.e. "reset the counter IF it
# already exists"), never create it. TASK-S55-004 is the task that changes
# this to unconditional pre-creation (so U-Boot only ever needs to
# overwrite, matching the original intent below) -- until that lands, the
# very first arm on a given board relies on THIS fragment's own fatwrite
# create-on-write behavior for uboot_boot_count, same as it always has for
# mender_boot_part_prev's sentinel write.
#
# Power-cut-safe write ordering in the revert branch below: mender_boot_part
# first, then the mender_boot_part_prev sentinel, then the counter reset. A
# cut after step 1 leaves _prev armed with what is now the CURRENT slot's own
# digit — the next boot's revert-branch re-write is then a no-op that
# converges to the same state. A cut after step 2 leaves a stale counter
# next to an already-written sentinel — harmless, since the sentinel gate
# above skips the whole block regardless of the counter's value, and the
# agent's own cycle-start write resets the counter unconditionally on its
# next OTA anyway.

echo "OTAPulse: OTAPULSE-BOOTCOUNT-NET/1 threshold=5"
setenv recoveryargs ""
test -n "${bootiface}"   || setenv bootiface mmc
test -n "${scriptaddr2}" || setexpr scriptaddr2 ${scriptaddr} + 0x10000

mw.b ${scriptaddr} 0x00
if load ${bootiface} ${mmcdev}:${mmcpart} ${scriptaddr} mender_boot_part_prev; then
    # A zero-length/truncated mender_boot_part_prev still reports load
    # success but copies no bytes, which would otherwise leave scriptaddr
    # holding whatever it held before (the mender_boot_part byte read by the
    # caller, at this SAME address) -- silently aliasing "previous" to
    # "current". Zeroing first makes that case read as 0x00, matching
    # neither recognised slot value nor the sentinel, so it falls through as
    # "unrecognised" and this block no-ops safely.
    setexpr.b prevpart *${scriptaddr}

    if test ${prevpart} = 88 -o ${prevpart} = 58; then
        echo "OTAPulse: mender_boot_part_prev already cleared (no pending switch) -- skipping bootcount safety net"
    else
        echo "OTAPulse: pending unconfirmed slot switch detected (mender_boot_part_prev present)"

        # Arm a kernel self-reset on panic (mirrors the RPi4 tryboot scheme's
        # cmdline_tryboot.txt panic=15 exactly) so a repeat of the motivating
        # incident -- init crashes, kernel panics, board hangs forever with
        # no watchdog -- still reboots and lets the counter below advance on
        # its own, instead of requiring 5 manual power cycles to ever reach
        # the revert threshold. init=/sbin/init additionally converts the
        # "init missing/unexecutable -> kernel execs /bin/sh as PID 1 and
        # hangs silently" case into an explicit failure the panic timer can
        # still catch, rather than a silent, uncounted hang.
        setenv recoveryargs " panic=15 init=/sbin/init"

        # Zero scratch B before attempting to load the counter -- same
        # defense as the _prev zeroing above. Without this, a zero-length/
        # truncated uboot_boot_count read would leave scriptaddr2 holding
        # whatever was left in DRAM from a prior boot/session, an
        # unpredictable value this ladder was never designed to compare
        # against. A genuinely missing/zero-length file (e.g. a torn agent
        # write at cycle start, before TASK-S55-004's fail-closed hardening
        # lands) now reads back as 0x00, which the ladder below treats as
        # "unrecognised -> clamp to atmax" -- i.e. it reverts immediately
        # rather than silently never triggering. That is the deliberately
        # accepted fail-safe direction for a garbled/absent counter, the
        # same policy already used for an unrecognised mender_boot_part_prev
        # value: when in doubt, revert to the known-good slot rather than
        # trust an ambiguous pending-switch state.
        mw.b ${scriptaddr2} 0x00
        setenv ubcount 48
        if load ${bootiface} ${mmcdev}:${mmcpart} ${scriptaddr2} uboot_boot_count; then
            setexpr.b ubcount *${scriptaddr2}
        fi

        setenv atmax 0
        if test ${ubcount} = 48 -o ${ubcount} = 30; then
            setenv nextbyte 0x31
        elif test ${ubcount} = 49 -o ${ubcount} = 31; then
            setenv nextbyte 0x32
        elif test ${ubcount} = 50 -o ${ubcount} = 32; then
            setenv nextbyte 0x33
        elif test ${ubcount} = 51 -o ${ubcount} = 33; then
            setenv nextbyte 0x34
        elif test ${ubcount} = 52 -o ${ubcount} = 34; then
            setenv nextbyte 0x35
            setenv atmax 1
        else
            # 53/35 ('5', already at or past threshold) or unrecognised --
            # clamp and treat as "at max" so a garbled counter fails safe
            # (reverts) rather than silently never triggering.
            setenv nextbyte 0x35
            setenv atmax 1
        fi

        if test ${atmax} = 1; then
            echo "OTAPulse: BOOTCOUNT-NET -- too many boot-loader-level attempts on the pending slot, reverting to the previous known-good slot"
            if test ${prevpart} = 32 -o ${prevpart} = 50; then
                setenv bootpart 2
                mw.b ${scriptaddr2} 0x32
            elif test ${prevpart} = 33 -o ${prevpart} = 51; then
                setenv bootpart 3
                mw.b ${scriptaddr2} 0x33
            else
                echo "OTAPulse: mender_boot_part_prev unrecognised, cannot revert -- leaving bootpart as-is"
            fi
            if test ${prevpart} = 32 -o ${prevpart} = 50 -o ${prevpart} = 33 -o ${prevpart} = 51; then
                fatwrite ${bootiface} ${mmcdev}:${mmcpart} ${scriptaddr2} mender_boot_part 1
                echo "OTAPulse: BOOTCOUNT-NET REVERTED to p${bootpart}"
            fi
            # Neutralise the trigger file itself (BUG-354 lesson): once this
            # revert has acted, mender_boot_part_prev must stop gating this
            # block on every future boot. fatwrite can only overwrite, not
            # delete, so write the sentinel byte 'X' (0x58) -- recognised
            # nowhere else as a real slot digit -- instead of leaving the
            # just-reverted slot's own digit in place (which would look
            # identical to a fresh pending switch on the very next ordinary
            # boot and re-arm forever).
            mw.b ${scriptaddr2} 0x58
            fatwrite ${bootiface} ${mmcdev}:${mmcpart} ${scriptaddr2} mender_boot_part_prev 1
            mw.b ${scriptaddr2} 0x30
            fatwrite ${bootiface} ${mmcdev}:${mmcpart} ${scriptaddr2} uboot_boot_count 1
        else
            mw.b ${scriptaddr2} ${nextbyte}
            fatwrite ${bootiface} ${mmcdev}:${mmcpart} ${scriptaddr2} uboot_boot_count 1
            echo "OTAPulse: BOOTCOUNT-NET -- recorded this boot attempt (not yet at threshold)"
        fi
    fi
fi
