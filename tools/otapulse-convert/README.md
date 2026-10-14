# otapulse-convert

`otapulse-convert` converts a disk image of a standard Linux distribution (Debian, Raspberry Pi OS)
into an OTA-Pulse image. The converted image

- has a boot partition, two root file system partitions (A and B) and a data partition,
- boots with GRUB (UEFI) or U-Boot, which count the boots of a freshly installed update and revert
  it on their own when it does not come up (bootloader compatibility mode),
- carries the OTA-Pulse agent (`soc-ota-agent`) and the monitoring daemon (`socmond`),
- comes with an OTA-Pulse artifact (`.otapulse`) of its root file system, which is the base for
  updates and can be signed with your key.

## Supported targets

| Configuration | Input image | Bootloader |
|---------------|-------------|------------|
| `configs/debian_arm64_uefi_config` | Debian 12 arm64 "genericcloud" or "nocloud" | GRUB (UEFI), the image's own |
| `configs/raspberrypi/uboot/debian/raspberrypi4_bookworm_64bit_config` | Raspberry Pi OS (Debian 12) arm64 | U-Boot (needs the asset listed in `configs/assets.sha256`) |

## Use

Docker, a Linux host with loop device support, and the OTA-Pulse packages are needed.

```bash
./docker-build                                  # builds the container image once

cp soc-ota-agent_*_arm64.deb socmond_*_all.deb input/packages/
cp debian-12-genericcloud-arm64.raw input/

OTAPULSE_ARTIFACT_NAME="release-1" ./docker-otapulse-convert \
    --disk-image input/debian-12-genericcloud-arm64.raw \
    --config configs/debian_arm64_uefi_config
```

The image (`*.img`) and the artifact (`*.otapulse`) are written to `deploy/`. The options of a
conversion are documented in `configs/otapulse_convert_config`; pass your own configuration file
with `--config` to change them. A root file system overlay (`--overlay DIR`) is copied into the
image last.

Run `./otapulse-convert --help` for the command line.

## Licenses

This tool is derived from open source software. See `NOTICE`.
