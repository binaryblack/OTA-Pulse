#!/usr/bin/env bash
#
# build-debs.sh -- build the OTA-Pulse Debian packages (TASK-S109-024):
#   soc-ota-agent_<ver>_<arch>.deb   Go + cgo agent, arm64 / armhf / amd64
#   socmond_<ver>_all.deb            shell monitoring daemon (architecture independent)
#
# Reproducible and containerised, same discipline as scripts/build-otapulse-artifact.sh:
#   * the builder is a Docker image built from packaging/debian/Dockerfile.builder with a base image
#     pinned by digest and apt packages pinned by a snapshot.debian.org date (packaging/debian/builder.env);
#     its tag is the hash of those two files, so any change rebuilds it and nothing else can reach it;
#   * all inputs come from ONE git revision of this repository (git archive), never from the working
#     tree; building HEAD with uncommitted changes in a used path is a hard failure;
#   * the Go module vendor/ tree is generated once from go.mod/go.sum and checked against a pinned
#     hash (packaging/debian/vendor.lock); a different tree is a hard failure;
#   * the package build itself runs with NO network, a fixed SOURCE_DATE_EPOCH (commit time), UTC,
#     C locale, umask 022, -trimpath, no VCS stamp, no build path in the result.
#   * cross compilation (arm64, armhf) uses the Debian cross toolchain and multiarch -dev libraries,
#     not qemu-user emulation; amd64 is native.
#
# Usage:
#   scripts/build-debs.sh [--arch arm64|armhf|amd64|all] [--rev GITREV] [--out DIR] [--cache DIR]
#                         [--repro] [--no-lint] [--keep-work] [--update-vendor-lock]
#     --arch   default: all
#     --rev    default: HEAD of this repository (resolved to a full commit id and recorded)
#     --out    default: <repo>/build/debs
#     --cache  default: <repo>/build/debs-cache (vendor trees, work directories)
#     --repro  build every selected architecture TWICE from scratch and fail unless every .deb is
#              byte-identical
#     --update-vendor-lock  record the vendor tree hash for the current go.mod/go.sum (review first)
#
# Output (in --out): the .deb files, SHA256SUMS, BUILD-MANIFEST.txt, lintian-<arch>.txt.
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PKG_DIR="${ROOT_DIR}/packaging/debian"
ARCHES=()
REV="HEAD"
OUT_DIR="${ROOT_DIR}/build/debs"
CACHE_DIR="${ROOT_DIR}/build/debs-cache"
REPRO=0
LINT=1
KEEP=0
UPDATE_LOCK=0

die() { echo "build-debs: ERROR: $*" >&2; exit 1; }
log() { echo ">>> $*"; }

while [ $# -gt 0 ]; do
    case "$1" in
        --arch)  [ $# -ge 2 ] || die "--arch needs a value"; ARCHES+=("$2"); shift 2 ;;
        --rev)   [ $# -ge 2 ] || die "--rev needs a value"; REV="$2"; shift 2 ;;
        --out)   [ $# -ge 2 ] || die "--out needs a value"; OUT_DIR="$2"; shift 2 ;;
        --cache) [ $# -ge 2 ] || die "--cache needs a value"; CACHE_DIR="$2"; shift 2 ;;
        --repro) REPRO=1; shift ;;
        --no-lint) LINT=0; shift ;;
        --keep-work) KEEP=1; shift ;;
        --update-vendor-lock) UPDATE_LOCK=1; shift ;;
        -h|--help) sed -n '2,36p' "$0"; exit 0 ;;
        *) die "unknown argument: $1" ;;
    esac
done
[ ${#ARCHES[@]} -gt 0 ] || ARCHES=(all)
EXPANDED=()
for a in "${ARCHES[@]}"; do
    case "$a" in
        all) EXPANDED+=(arm64 armhf amd64) ;;
        arm64|armhf|amd64) EXPANDED+=("$a") ;;
        *) die "unsupported architecture '$a' (arm64, armhf, amd64, all)" ;;
    esac
done
mapfile -t ARCHES < <(printf '%s\n' "${EXPANDED[@]}" | awk '!seen[$0]++')

command -v docker >/dev/null || die "docker is required"
command -v git >/dev/null || die "git is required"
git -C "$ROOT_DIR" rev-parse --git-dir >/dev/null 2>&1 || die "$ROOT_DIR is not a git work tree"

# ---------------------------------------------------------------------------------------------
# Inputs: one git revision
# ---------------------------------------------------------------------------------------------
COMMIT="$(git -C "$ROOT_DIR" rev-parse --verify "${REV}^{commit}")" || die "cannot resolve revision '$REV'"
HEAD_COMMIT="$(git -C "$ROOT_DIR" rev-parse HEAD)"
SDE="$(git -C "$ROOT_DIR" show -s --format=%ct "$COMMIT")"
DESCRIBE="$(git -C "$ROOT_DIR" describe --tags --always --match 'v[0-9]*' "$COMMIT")"

# Paths of the repository that end up in the packages.
USED_PATHS=(
    soc-ota-agent
    meta-otapulse/recipes-core/soc-ota-agent/files
    meta-otapulse/recipes-core/socmond/files
    meta-otapulse/recipes-core/otapulse-firstboot/files/otapulse-data-seed
    meta-otapulse/recipes-core/otapulse-firstboot/files/otapulse-bl-guard
    meta-otapulse/recipes-core/otapulse-firstboot/files/otapulse-bl-boot-deadline.service
    meta-otapulse/recipes-core/otapulse-firstboot/files/otapulse-bl-boot-deadline.timer
    meta-otapulse/recipes-core/otapulse-firstboot/files/otapulse-bl-seed-failure.service
    meta-otapulse/recipes-core/otapulse-firstboot/files/otapulse-bl-clock-wait.service
    NOTICE
)
if [ "$COMMIT" = "$HEAD_COMMIT" ]; then
    if ! git -C "$ROOT_DIR" diff --quiet HEAD -- "${USED_PATHS[@]}"; then
        git -C "$ROOT_DIR" status --short -- "${USED_PATHS[@]}" >&2
        die "tracked files in the package inputs differ from HEAD; commit them or build an explicit --rev"
    fi
    if [ -n "$(git -C "$ROOT_DIR" ls-files --others --exclude-standard -- "${USED_PATHS[@]}")" ]; then
        git -C "$ROOT_DIR" ls-files --others --exclude-standard -- "${USED_PATHS[@]}" >&2
        die "untracked files in the package inputs; commit or remove them (or build an explicit --rev)"
    fi
else
    log "building explicit revision $COMMIT (HEAD is $HEAD_COMMIT); working tree is not consulted"
fi

# Debian version from the git description: v0.1.7 -> 0.1.7 ; v0.1.7-88-g202af1a -> 0.1.7+88.g202af1a ;
# bare commit id (no tag) -> 0~git<sha>.
case "$DESCRIBE" in
    v[0-9]*-[0-9]*-g[0-9a-f]*) DEB_VERSION="$(echo "${DESCRIBE#v}" | sed -E 's/^(.*)-([0-9]+)-g([0-9a-f]+)$/\1+\2.g\3/')" ;;
    v[0-9]*) DEB_VERSION="${DESCRIBE#v}" ;;
    *) DEB_VERSION="0~git${DESCRIBE}" ;;
esac
case "$DEB_VERSION" in
    *[!A-Za-z0-9.+~]*) die "derived Debian version '$DEB_VERSION' contains unexpected characters" ;;
esac

# ---------------------------------------------------------------------------------------------
# Builder image (content addressed)
# ---------------------------------------------------------------------------------------------
[ -f "$PKG_DIR/builder.env" ] && [ -f "$PKG_DIR/Dockerfile.builder" ] || die "missing builder.env / Dockerfile.builder in $PKG_DIR"
# shellcheck disable=SC1091
. "$PKG_DIR/builder.env"
case "$BUILDER_BASE_IMAGE" in *@sha256:????????????????????????????????????????????????????????????????) ;; *) die "BUILDER_BASE_IMAGE must be pinned by sha256 digest" ;; esac
case "$DEBIAN_SNAPSHOT" in [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]T[0-9][0-9][0-9][0-9][0-9][0-9]Z) ;; *) die "DEBIAN_SNAPSHOT must look like 20260901T000000Z" ;; esac
BUILDER_TAG="otapulse-debs-builder:$(cat "$PKG_DIR/builder.env" "$PKG_DIR/Dockerfile.builder" | sha256sum | cut -c1-12)"
if ! docker image inspect "$BUILDER_TAG" >/dev/null 2>&1; then
    log "building builder image $BUILDER_TAG (network needed: base image and snapshot.debian.org)"
    docker build -f "$PKG_DIR/Dockerfile.builder" \
        --build-arg "BUILDER_BASE_IMAGE=$BUILDER_BASE_IMAGE" --build-arg "DEBIAN_SNAPSHOT=$DEBIAN_SNAPSHOT" \
        -t "$BUILDER_TAG" "$PKG_DIR" || die "builder image build failed"
fi
BUILDER_ID="$(docker image inspect --format '{{.Id}}' "$BUILDER_TAG")"

# ---------------------------------------------------------------------------------------------
# Safety gates on the inputs
# ---------------------------------------------------------------------------------------------
mkdir -p "$CACHE_DIR" "$OUT_DIR"
CACHE_DIR="$(cd "$CACHE_DIR" && pwd)"; OUT_DIR="$(cd "$OUT_DIR" && pwd)"
RUN_ID="$$-$(date +%s)"
WORK_ROOT="${CACHE_DIR}/work/${RUN_ID}"
mkdir -p "$WORK_ROOT"
cleanup() { [ "$KEEP" = 1 ] || rm -rf "$WORK_ROOT"; }
trap cleanup EXIT

SRC_TREE="${WORK_ROOT}/src"
mkdir -p "$SRC_TREE"
git -C "$ROOT_DIR" archive "$COMMIT" -- "${USED_PATHS[@]}" | tar -x -C "$SRC_TREE" \
    || die "git archive of the package inputs failed"

AGENT_FILES="$SRC_TREE/meta-otapulse/recipes-core/soc-ota-agent/files"
SOCMOND_FILES="$SRC_TREE/meta-otapulse/recipes-core/socmond/files"
for s in ArtifactReboot_Enter_01 ArtifactRollbackReboot_Enter_01 ArtifactCommit_Enter_01; do
    [ -f "$AGENT_FILES/$s" ] || die "state script $s missing from the inputs"
    grep -q 'OTAPULSE-BOOT-MODE-GATE BEGIN' "$AGENT_FILES/$s" \
        || die "state script $s carries no OTAPULSE-BOOT-MODE-GATE block; refusing to package it (it would rewrite native boot media on a bootloader-mode device)"
done
[ "$(cat "$PKG_DIR/debian/boot_mode")" = "bootloader" ] || die "debian/boot_mode must contain exactly 'bootloader'"
grep -q '"BootEnvMode": "bootloader"' "$PKG_DIR/debian/otapulse.conf" || die "debian/otapulse.conf lacks BootEnvMode=bootloader"
grep -q '"UseFileBasedBootEnv": false' "$PKG_DIR/debian/otapulse.conf" || die "debian/otapulse.conf must set UseFileBasedBootEnv=false"
for f in soc-ota-provision; do [ -f "$AGENT_FILES/$f" ] || die "$f missing from the inputs"; done
DATA_SEED="$SRC_TREE/meta-otapulse/recipes-core/otapulse-firstboot/files/otapulse-data-seed"
[ -f "$DATA_SEED" ] || die "otapulse-data-seed missing from the inputs"
BL_FILES="$SRC_TREE/meta-otapulse/recipes-core/otapulse-firstboot/files"
for f in otapulse-bl-guard otapulse-bl-boot-deadline.service otapulse-bl-boot-deadline.timer otapulse-bl-seed-failure.service otapulse-bl-clock-wait.service; do
    [ -f "$BL_FILES/$f" ] || die "bootloader-mode guard input $f missing"
done
for f in socmond socmond-watchdog core-handler.sh soc-ctl soc-deregister config.json custom_metrics.conf.example socmond.service socmond-watchdog.service; do
    [ -f "$SOCMOND_FILES/$f" ] || die "socmond input $f missing"
done
[ -f "$SRC_TREE/soc-ota-agent/go.mod" ] && [ -f "$SRC_TREE/soc-ota-agent/go.sum" ] || die "soc-ota-agent/go.mod or go.sum missing"

# ---------------------------------------------------------------------------------------------
# Vendored Go modules: generated once per go.mod/go.sum, pinned by hash
# ---------------------------------------------------------------------------------------------
GOMOD_HASH="$(cat "$SRC_TREE/soc-ota-agent/go.mod" "$SRC_TREE/soc-ota-agent/go.sum" | sha256sum | cut -d' ' -f1)"
VENDOR_CACHE="${CACHE_DIR}/vendor/${GOMOD_HASH}"
tree_hash() { (cd "$1" && find . -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1); }
if [ ! -f "$VENDOR_CACHE/vendor/modules.txt" ]; then
    log "generating the Go vendor tree for go.mod/go.sum $GOMOD_HASH (network: Go module proxy)"
    rm -rf "$VENDOR_CACHE"; mkdir -p "$VENDOR_CACHE/mod"
    # go mod vendor needs the packages' source, not only go.mod: use a copy of the agent tree.
    cp -a "$SRC_TREE/soc-ota-agent/." "$VENDOR_CACHE/mod/"
    docker run --rm --user "$(id -u):$(id -g)" \
        -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOMODCACHE=/tmp/gomod \
        -e GOFLAGS=-mod=readonly -e GOPROXY=https://proxy.golang.org -e GOSUMDB=sum.golang.org \
        -e GOTOOLCHAIN=local \
        -v "$VENDOR_CACHE/mod:/src" -w /src "$BUILDER_TAG" \
        go mod vendor -o /src/vendor-out \
        || { rm -rf "$VENDOR_CACHE"; die "go mod vendor failed (a go.sum that needs updating is drift: fix it in the repository)"; }
    mv "$VENDOR_CACHE/mod/vendor-out" "$VENDOR_CACHE/vendor"
    rm -rf "$VENDOR_CACHE/mod"
fi
VENDOR_HASH="$(tree_hash "$VENDOR_CACHE/vendor")"
LOCK_FILE="$PKG_DIR/vendor.lock"
if [ "$UPDATE_LOCK" = 1 ]; then
    { grep -v "^${GOMOD_HASH} " "$LOCK_FILE" 2>/dev/null || true; echo "${GOMOD_HASH} ${VENDOR_HASH}"; } | LC_ALL=C sort > "${LOCK_FILE}.new"
    mv "${LOCK_FILE}.new" "$LOCK_FILE"
    log "recorded vendor.lock entry: go.mod+go.sum ${GOMOD_HASH} -> vendor tree ${VENDOR_HASH}"
fi
LOCKED="$(awk -v k="$GOMOD_HASH" '$1==k {print $2}' "$LOCK_FILE" 2>/dev/null || true)"
[ -n "$LOCKED" ] || die "go.mod/go.sum ${GOMOD_HASH} is not in packaging/debian/vendor.lock; review the dependency change, then run with --update-vendor-lock"
[ "$LOCKED" = "$VENDOR_HASH" ] || die "vendor tree hash $VENDOR_HASH differs from the pinned $LOCKED for go.mod/go.sum $GOMOD_HASH (dependency drift)"

PKG_TREE_HASH="$(cd "$PKG_DIR" && find . -type f -not -path '*/__pycache__/*' -not -name '*.pyc' -print0 | LC_ALL=C sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1)"

# ---------------------------------------------------------------------------------------------
# One package build in a fresh stage directory
# ---------------------------------------------------------------------------------------------
# build_one <arch> <result-dir>
build_one() {
    local arch="$1" res="$2" stage tag
    stage="${WORK_ROOT}/stage-${arch}-$(basename "$res")"
    mkdir -p "$stage/otapulse-agent" "$res"
    local top="$stage/otapulse-agent"
    cp -a "$SRC_TREE/soc-ota-agent" "$top/agent"
    cp -a "$VENDOR_CACHE/vendor" "$top/agent/vendor"
    mkdir -p "$top/upstream/agent" "$top/upstream/socmond"
    cp -a "$AGENT_FILES/soc-ota-provision" "$AGENT_FILES"/Artifact*_01 "$DATA_SEED" "$top/upstream/agent/"
    mkdir -p "$top/upstream/bl"
    for f in otapulse-bl-guard otapulse-bl-boot-deadline.service otapulse-bl-boot-deadline.timer otapulse-bl-seed-failure.service otapulse-bl-clock-wait.service; do
        cp -a "$BL_FILES/$f" "$top/upstream/bl/"
    done
    cp -a "$SOCMOND_FILES/." "$top/upstream/socmond/"
    cp -a "$SRC_TREE/NOTICE" "$top/upstream/NOTICE"
    cp -a "$PKG_DIR/debian" "$top/debian"
    printf '%s\n' "$DESCRIBE" > "$top/OTAPULSE_AGENT_VERSION"
    {
        echo "otapulse-agent (${DEB_VERSION}) bookworm; urgency=medium"
        echo
        echo "  * OTA-Pulse commit ${COMMIT}."
        echo
        echo " -- OTA-Pulse Project <support@ota-pulse.com>  $(LC_ALL=C TZ=UTC date -u -d "@${SDE}" -R)"
    } > "$top/debian/changelog"
    log "building ${arch} (${DEB_VERSION}) in a network-less container"
    docker run --rm --network none --user "$(id -u):$(id -g)" \
        -e HOME=/tmp -e TZ=UTC -e LC_ALL=C.UTF-8 -e LANG=C.UTF-8 \
        -e "SOURCE_DATE_EPOCH=${SDE}" \
        -v "$stage:/build" -w /build/otapulse-agent "$BUILDER_TAG" \
        sh -c "umask 022 && dpkg-buildpackage -b -us -uc -a${arch} --no-sign" \
        > "$res/build-${arch}.log" 2>&1 \
        || { tail -40 "$res/build-${arch}.log" >&2; die "dpkg-buildpackage ($arch) failed; log: $res/build-${arch}.log"; }
    cp -a "$stage"/*.deb "$stage"/*.buildinfo "$stage"/*.changes "$res/"
    if [ "$LINT" = 1 ]; then
        docker run --rm --network none --user "$(id -u):$(id -g)" -e HOME=/tmp \
            -v "$stage:/build:ro" -w /build "$BUILDER_TAG" \
            lintian --tag-display-limit 0 --info --display-info --fail-on error \
            "soc-ota-agent_${DEB_VERSION}_${arch}.deb" "socmond_${DEB_VERSION}_all.deb" \
            > "$res/lintian-${arch}.txt" 2>&1 \
            || { cat "$res/lintian-${arch}.txt" >&2; die "lintian reported errors for $arch"; }
    fi
    [ "$KEEP" = 1 ] || rm -rf "$stage"
}

run_set() {
    # run_set <label> -> results in $OUT_DIR/.. ; prints nothing
    local label="$1" arch
    for arch in "${ARCHES[@]}"; do
        build_one "$arch" "${WORK_ROOT}/res-${label}/${arch}"
    done
}

sums_of() { # sums_of <label>  -> lines "sha256  name" sorted, socmond only once (all arches must agree)
    local label="$1" arch f
    for arch in "${ARCHES[@]}"; do
        for f in "${WORK_ROOT}/res-${label}/${arch}"/*.deb; do
            printf '%s  %s\n' "$(sha256sum "$f" | cut -d' ' -f1)" "$(basename "$f")"
        done
    done | LC_ALL=C sort -u
}

run_set A
# socmond is architecture independent: every architecture's build must give the same file.
SOCMOND_SUMS="$(for arch in "${ARCHES[@]}"; do sha256sum "${WORK_ROOT}/res-A/${arch}/socmond_${DEB_VERSION}_all.deb" | cut -d' ' -f1; done | sort -u | wc -l)"
[ "$SOCMOND_SUMS" = 1 ] || die "socmond_${DEB_VERSION}_all.deb differs between architectures; the package is not architecture independent"

if [ "$REPRO" = 1 ]; then
    run_set B
    if ! diff <(sums_of A) <(sums_of B) >/dev/null; then
        diff <(sums_of A) <(sums_of B) >&2 || true
        die "reproducibility check FAILED: the second build differs from the first"
    fi
    log "reproducibility check passed: two independent builds are byte-identical"
    sums_of A | sed 's/^/  build1=build2  /' >&2
fi

# ---------------------------------------------------------------------------------------------
# Publish to the output directory
# ---------------------------------------------------------------------------------------------
for arch in "${ARCHES[@]}"; do
    cp -a "${WORK_ROOT}/res-A/${arch}"/*.deb "$OUT_DIR/"
    cp -a "${WORK_ROOT}/res-A/${arch}"/*.buildinfo "${WORK_ROOT}/res-A/${arch}"/build-"${arch}".log "$OUT_DIR/" 2>/dev/null || true
    [ ! -f "${WORK_ROOT}/res-A/${arch}/lintian-${arch}.txt" ] || cp -a "${WORK_ROOT}/res-A/${arch}/lintian-${arch}.txt" "$OUT_DIR/"
done
( cd "$OUT_DIR" && sha256sum ./*.deb | sed 's| \./| |' | LC_ALL=C sort -k2 > SHA256SUMS )
{
    echo "repository_commit=${COMMIT}"
    echo "git_describe=${DESCRIBE}"
    echo "debian_version=${DEB_VERSION}"
    echo "source_date_epoch=${SDE}"
    echo "architectures=${ARCHES[*]}"
    echo "builder_image=${BUILDER_TAG}"
    echo "builder_image_id=${BUILDER_ID}"
    echo "builder_base_image=${BUILDER_BASE_IMAGE}"
    echo "debian_snapshot=${DEBIAN_SNAPSHOT}"
    echo "gomod_gosum_sha256=${GOMOD_HASH}"
    echo "vendor_tree_sha256=${VENDOR_HASH}"
    echo "packaging_tree_sha256=${PKG_TREE_HASH}"
    echo "reproducibility_checked=${REPRO}"
} > "$OUT_DIR/BUILD-MANIFEST.txt"
log "done. packages in $OUT_DIR"
cat "$OUT_DIR/SHA256SUMS"
