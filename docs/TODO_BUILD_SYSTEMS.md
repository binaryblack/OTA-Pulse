# OTA-Pulse Build System Integration Roadmap

This document tracks planned and potential build system integrations for OTA-Pulse.

## Current Status

| Build System | Status | Directory | Notes |
|--------------|--------|-----------|-------|
| Yocto/OpenEmbedded | ✅ Complete | `meta-otapulse/` | Production ready |
| Buildroot | ✅ Complete | `buildroot-otapulse/` | Production ready |
| Debian/Ubuntu (.deb) | ✅ Complete | `debian-otapulse/` | Multi-arch: amd64, arm64, armhf |
| OpenWrt | ✅ Complete | `openwrt-otapulse/` | Procd + UCI integration |
| Generic tarball | ✅ Complete | `generic-installer/` | Any Linux, auto-detects init system |

---

## Planned Integrations

### ~~1. OpenWrt Package~~ ✅ COMPLETED
See `openwrt-otapulse/` — Procd init + UCI config integration.

---

### ~~2. Debian/Ubuntu Packages (.deb)~~ ✅ COMPLETED
See `debian-otapulse/` — Multi-arch (amd64, arm64, armhf).

---

### 3. Alpine Linux Package (apk)
**Priority:** Medium
**Target Market:** Containers, minimal embedded systems
**Effort:** Low (1-2 days)

**Why:**
- Popular for containerized deployments
- Extremely lightweight
- Growing embedded Linux adoption

**Required Files:**
```
alpine-otapulse/
├── APKBUILD                    # Alpine package build script
├── otapulse.initd              # OpenRC init script
├── otapulse.confd              # OpenRC configuration
└── otapulse.pre-install        # Pre-install hook
```

**Key Considerations:**
- musl libc (verify Go binary compatibility)
- OpenRC init system (not systemd)
- Minimal dependencies

---

### 4. PTXdist Integration
**Priority:** Low
**Target Market:** German industrial market
**Effort:** Medium (2-3 days)

**Why:**
- Popular in German industrial sector
- Strong in automation and manufacturing

**Required Files:**
```
ptxdist-otapulse/
├── rules/
│   ├── otapulse.make           # PTXdist rules file
│   └── otapulse.in             # Kconfig menu
└── projectroot/
    └── etc/otapulse/           # Default configs
```

---

### ~~5. Generic Tarball Installer~~ ✅ COMPLETED
See `generic-installer/` — Auto-detects init system (systemd/OpenRC/sysvinit).

---

### 6. NixOS / Nix Package
**Priority:** Low
**Target Market:** Reproducible builds, DevOps-focused teams
**Effort:** Medium (2-3 days)

**Required Files:**
```
nix-otapulse/
├── default.nix                 # Package definition
├── module.nix                  # NixOS module
└── flake.nix                   # Nix flakes support
```

---

### 7. Fedora/RHEL Packages (.rpm)
**Priority:** Low
**Target Market:** Enterprise Linux, CentOS Stream
**Effort:** Low (1-2 days)

**Required Files:**
```
rpm-otapulse/
├── otapulse.spec               # RPM spec file
└── sources/                    # Source references
```

---

## Integration Effort Summary

| Integration | Status | Effort | Priority |
|-------------|--------|--------|----------|
| OpenWrt | ✅ Done | 2-3 days | High |
| Debian (.deb) | ✅ Done | 1-2 days | High |
| Generic tarball | ✅ Done | 1 day | Medium |
| Alpine (apk) | Planned | 1-2 days | Medium |
| PTXdist | Planned | 2-3 days | Low |
| NixOS | Planned | 2-3 days | Low |
| Fedora (.rpm) | Planned | 1-2 days | Low |

---

## Architecture Notes

The core OTA agent is designed for easy porting:

```
soc-ota-agent/
├── Makefile              # Standard GNU Make - works anywhere
├── *.go                  # Go code (CGO_ENABLED=1 required)
└── support/
    ├── otapulse-*.service    # Systemd service (reusable)
    ├── otapulse-device-identity  # Identity script
    ├── otapulse-inventory-*  # Inventory scripts
    ├── dbus/                 # D-Bus policy files
    ├── modules/              # Update modules (deb, docker, rpm, etc.)
    └── modules-artifact-gen/ # Artifact generator scripts
```

**Cross-compilation (requires C cross-compiler since CGO is enabled):**
```bash
# Build for any target (set CC to your cross-compiler)
CC=aarch64-linux-gnu-gcc GOOS=linux GOARCH=arm64 make build
CC=arm-linux-gnueabihf-gcc GOOS=linux GOARCH=arm GOARM=7 make build
GOOS=linux GOARCH=amd64 make build
```

**Dependencies (minimal):**
- OpenSSL (for signature verification)
- liblmdb (embedded database)
- Standard C library (glibc or musl)

---

## Contributing

To add support for a new build system:

1. Create directory: `<buildsystem>-otapulse/`
2. Add package/build files for that system
3. Ensure mandatory signature verification
4. Add documentation in `docs/`
5. Update main `docs/INTEGRATION.md` table
6. Submit PR

---

## Notes

- All integrations MUST enforce mandatory signature verification
- Configuration options should be consistent across build systems
- Test on real hardware before marking complete
- Provide example configurations for popular boards
