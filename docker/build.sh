#!/bin/bash
# build.sh — Build OTAPulse using Docker for reproducible builds
#
# Usage:
#   build.sh image         Build the Docker image
#   build.sh amd64         Build for x86_64
#   build.sh arm64         Build for ARM64
#   build.sh armhf         Build for ARMv7
#   build.sh all           Build for all architectures
#   build.sh test          Run tests in container
#   build.sh shell         Open interactive shell in build container

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
IMAGE_NAME="otapulse-build"
OUTPUT_DIR="$REPO_ROOT/soc-ota-agent/output"

build_image() {
    echo "Building Docker image: ${IMAGE_NAME}"
    docker build -t "$IMAGE_NAME" -f "$SCRIPT_DIR/Dockerfile.build" "$REPO_ROOT"
}

ensure_image() {
    if ! docker image inspect "$IMAGE_NAME" &>/dev/null; then
        build_image
    fi
}

build_arch() {
    local arch="$1"
    local goos="linux"
    local goarch cc suffix goarm=""

    case "$arch" in
        amd64|x86_64)
            goarch=amd64; cc=gcc; suffix=linux-amd64 ;;
        arm64|aarch64)
            goarch=arm64; cc=aarch64-linux-gnu-gcc; suffix=linux-arm64 ;;
        armhf|arm)
            goarch=arm; goarm=7; cc=arm-linux-gnueabihf-gcc; suffix=linux-armhf ;;
        *)
            echo "Unknown architecture: $arch" >&2; exit 1 ;;
    esac

    ensure_image
    mkdir -p "$OUTPUT_DIR"

    echo "Building for ${arch} (GOARCH=${goarch})..."
    docker run --rm \
        -v "$REPO_ROOT:/workspace" \
        -w /workspace/soc-ota-agent \
        -e "GOOS=$goos" \
        -e "GOARCH=$goarch" \
        -e "GOARM=$goarm" \
        -e "CC=$cc" \
        -e "CGO_ENABLED=1" \
        "$IMAGE_NAME" \
        sh -c "make build && cp otapulse /workspace/soc-ota-agent/output/otapulse-${suffix}"

    echo "Built: output/otapulse-${suffix}"
}

case "${1:-help}" in
    image)
        build_image
        ;;
    amd64|x86_64)
        build_arch amd64
        ;;
    arm64|aarch64)
        build_arch arm64
        ;;
    armhf|arm)
        build_arch armhf
        ;;
    all)
        for arch in amd64 arm64 armhf; do
            build_arch "$arch"
        done
        echo ""
        echo "All builds complete. Binaries in: soc-ota-agent/output/"
        ls -lh "$OUTPUT_DIR"/otapulse-*
        ;;
    test)
        ensure_image
        docker run --rm \
            -v "$REPO_ROOT:/workspace" \
            -w /workspace/soc-ota-agent \
            "$IMAGE_NAME" \
            make test
        ;;
    shell)
        ensure_image
        docker run -it --rm \
            -v "$REPO_ROOT:/workspace" \
            -w /workspace/soc-ota-agent \
            "$IMAGE_NAME" \
            bash
        ;;
    help|*)
        echo "Usage: $0 {image|amd64|arm64|armhf|all|test|shell}"
        echo ""
        echo "Commands:"
        echo "  image   Build the Docker build image"
        echo "  amd64   Build for x86_64"
        echo "  arm64   Build for ARM64"
        echo "  armhf   Build for ARMv7"
        echo "  all     Build for all architectures"
        echo "  test    Run Go tests"
        echo "  shell   Open interactive shell"
        ;;
esac
