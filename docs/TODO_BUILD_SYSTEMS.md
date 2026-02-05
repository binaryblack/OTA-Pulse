# OTA-Pulse Build System Integration Roadmap

This document tracks planned and potential build system integrations for OTA-Pulse.

## Current Status

| Build System | Status | Directory | Notes |
|--------------|--------|-----------|-------|
| Yocto/OpenEmbedded | ✅ Complete | `meta-otapulse/` | Production ready |
| Buildroot | ✅ Complete | `buildroot-otapulse/` | Production ready |

---

## Planned Integrations

### 1. OpenWrt Package
**Priority:** High
**Target Market:** Routers, IoT gateways, networking devices
**Effort:** Medium (2-3 days)

**Why:**
- Massive community (thousands of supported devices)
- Standard for networking/router products
- Strong demand for secure OTA in network infrastructure

**Required Files:**
```
openwrt-otapulse/
├── Makefile                    # OpenWrt package Makefile
├── files/
│   ├── otapulse.init           # Procd init script
│   ├── otapulse.config         # UCI configuration
│   └── otapulse.defaults       # Default settings
└── patches/                    # Any needed patches
```

**Key Considerations:**
- OpenWrt uses procd init system (not systemd)
- UCI configuration system integration
- LuCI web interface for configuration (optional)
- musl libc compatibility (test Go binary)
- Smaller flash storage constraints

---

### 2. Debian/Ubuntu Packages (.deb)
**Priority:** High
**Target Market:** Industrial PCs, SBCs, any Debian-based system
**Effort:** Low (1-2 days)

**Why:**
- Easy onboarding for customers already using Debian/Ubuntu
- Works on Raspberry Pi OS, Armbian, etc.
- No custom build system needed - just `apt install`

**Required Files:**
```
debian-otapulse/
├── debian/
│   ├── control                 # Package metadata
│   ├── rules                   # Build rules
│   ├── changelog               # Version history
│   ├── copyright               # License info
│   ├── otapulse.service        # Systemd service
│   ├── otapulse.postinst       # Post-install script
│   ├── otapulse.prerm          # Pre-remove script
│   └── conffiles               # Config file list
└── README.md
```

**Key Considerations:**
- Multi-architecture support (amd64, arm64, armhf)
- APT repository hosting (GitHub releases or dedicated)
- Automatic updates via apt
- Configuration via /etc/otapulse/
- Integration with existing partition schemes

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
│   └── otapulse.make           # PTXdist rules file
├── rules/
│   └── otapulse.in             # Kconfig menu
└── projectroot/
    └── etc/otapulse/           # Default configs
```

---

### 5. Generic Tarball Installer
**Priority:** Medium
**Target Market:** Any Linux system
**Effort:** Low (1 day)

**Why:**
- Works on ANY Linux distribution
- Useful for custom/proprietary build systems
- Good for evaluation and testing

**Required Files:**
```
generic-installer/
├── install.sh                  # Interactive installer
├── uninstall.sh                # Clean removal
├── otapulse-<version>-<arch>.tar.gz
└── README.md
```

**Features:**
- Detect init system (systemd, sysvinit, openrc)
- Install binary and scripts
- Generate initial configuration
- Create systemd/init service
- Validate dependencies

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

| Integration | Effort | Files | Priority |
|-------------|--------|-------|----------|
| OpenWrt | 2-3 days | ~5 | High |
| Debian (.deb) | 1-2 days | ~8 | High |
| Alpine (apk) | 1-2 days | ~4 | Medium |
| Generic tarball | 1 day | ~3 | Medium |
| PTXdist | 2-3 days | ~4 | Low |
| NixOS | 2-3 days | ~3 | Low |
| Fedora (.rpm) | 1-2 days | ~2 | Low |

---

## Architecture Notes

The core OTA agent is designed for easy porting:

```
soc-ota-agent/
├── Makefile              # Standard GNU Make - works anywhere
├── *.go                  # Pure Go code
└── support/
    ├── otapulse-*.service    # Systemd service (reusable)
    └── scripts/              # Shell scripts (portable)
```

**Cross-compilation:**
```bash
# Build for any target
GOOS=linux GOARCH=arm64 make build
GOOS=linux GOARCH=arm GOARM=7 make build
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
