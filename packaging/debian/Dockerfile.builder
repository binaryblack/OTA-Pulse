# Builder image for the OTA-Pulse Debian packages (TASK-S109-024). See builder.env for the pins.
# Not built directly: ota-pulse/scripts/build-debs.sh builds it with the build args from builder.env.
ARG BUILDER_BASE_IMAGE
FROM ${BUILDER_BASE_IMAGE}
ARG DEBIAN_SNAPSHOT

# Point apt at a fixed snapshot of Debian bookworm (+ security) instead of the moving archive.
RUN set -eux; \
    rm -f /etc/apt/sources.list /etc/apt/sources.list.d/*; \
    printf '%s\n' \
      "deb [check-valid-until=no] http://snapshot.debian.org/archive/debian/${DEBIAN_SNAPSHOT}/ bookworm main" \
      "deb [check-valid-until=no] http://snapshot.debian.org/archive/debian/${DEBIAN_SNAPSHOT}/ bookworm-updates main" \
      "deb [check-valid-until=no] http://snapshot.debian.org/archive/debian-security/${DEBIAN_SNAPSHOT}/ bookworm-security main" \
      > /etc/apt/sources.list; \
    printf 'Acquire::Check-Valid-Until "false";\nAcquire::Retries "5";\nAPT::Install-Recommends "false";\n' \
      > /etc/apt/apt.conf.d/90otapulse-snapshot

# Multiarch + the cross toolchains and the target-arch development libraries the agent's cgo parts
# link against (openssl, liblzma, gio from glib). lmdb is compiled from the vendored Go module, so no
# liblmdb-dev. lintian and fakeroot run inside the same image so the build and the check see the same files.
RUN set -eux; \
    dpkg --add-architecture arm64; dpkg --add-architecture armhf; \
    apt-get update; \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      build-essential debhelper dpkg-dev fakeroot lintian pkg-config xz-utils \
      crossbuild-essential-arm64 crossbuild-essential-armhf \
      libssl-dev libssl-dev:arm64 libssl-dev:armhf \
      liblzma-dev liblzma-dev:arm64 liblzma-dev:armhf \
      libglib2.0-dev libglib2.0-dev:arm64 libglib2.0-dev:armhf; \
    rm -rf /var/lib/apt/lists/*

# The Go toolchain comes from the pinned base image (/usr/local/go); never let go fetch another one.
ENV GOTOOLCHAIN=local GOFLAGS=-mod=vendor GOPROXY=off CGO_ENABLED=1
