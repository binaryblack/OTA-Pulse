# OTAPulse Docker Build Environment

Reproducible build environment for the OTAPulse agent using Docker.

## Quick Start

```bash
cd OTA-Pulse

# Build the Docker image
docker/build.sh image

# Build the agent (all architectures)
docker/build.sh all

# Build for a specific architecture
docker/build.sh amd64
docker/build.sh arm64
docker/build.sh armhf
```

## What's Included

The Docker build container includes:

- Go 1.22 toolchain
- Cross-compilers (aarch64, arm, x86_64)
- OpenSSL and liblmdb development headers
- mender-artifact tool
- dpkg-buildpackage (for .deb packaging)
- shellcheck (for script validation)

## Build Outputs

Binaries are placed in `soc-ota-agent/output/`:

```
soc-ota-agent/output/
├── otapulse-linux-amd64
├── otapulse-linux-arm64
└── otapulse-linux-armhf
```

## Using in CI

```yaml
# GitHub Actions example
jobs:
  build:
    runs-on: ubuntu-latest
    container:
      image: ghcr.io/binaryblack/otapulse-build:latest
    steps:
      - uses: actions/checkout@v4
      - run: cd soc-ota-agent && make build
```

## Building the Container Locally

```bash
docker build -t otapulse-build -f docker/Dockerfile.build .
```

## Interactive Development

```bash
docker run -it --rm \
    -v $(pwd):/workspace \
    -w /workspace/soc-ota-agent \
    otapulse-build \
    bash
```

## Dockerfile Details

See `docker/Dockerfile.build` for the full container definition.
