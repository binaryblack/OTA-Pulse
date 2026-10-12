#!/usr/bin/env bash
#
# mender-sync.sh -- maintain the pinned, vendored upstream board-integration layers
# under meta-otapulse-bsp/ (TODO-157 / S109 TASK-S109-004). INTERNAL developer tool.
#
# The vendored trees are copies of upstream source (see meta-otapulse-bsp/README.md)
# pinned by mender-upstream.lock. This script is the ONLY thing that ever talks to
# the network about them, and only when a human or CI runs --status / --sync /
# --verify --online. No build step fetches upstream; builds use the vendored copy.
#
# Same discipline as scripts/build-otapulse-artifact.sh (pinned upstream ref + a
# CHECKED-IN, reviewable patch series, hard-fail on drift, never an inline sed):
# local changes to a vendored tree live ONLY as ota-pulse/patches/mender/<component>/
# NNNN-name.patch files, re-applied on top of the pristine upstream on every sync.
# A patch that no longer applies is a hard failure -- regenerate the patch, never
# force it and never hand-edit a vendored file.
#
# Usage:
#   mender-sync.sh --status                         pinned vs latest upstream (https, no credentials)
#   mender-sync.sh --sync <component> <ref> [--dry-run]
#                                                   re-vendor <component> at <ref>
#                                                   (tag, branch name, or full 40-hex sha)
#   mender-sync.sh --verify [--online]              offline lock/tree/patch/LICENSE check;
#                                                   --online additionally compares against a
#                                                   fresh clone of the pinned commit
#   mender-sync.sh --upgrade-lock                   one-time: fill lock columns 8-10, manifests,
#                                                   UPSTREAM extras for legacy 7-column rows
#                                                   (offline; refuses unless the tree verifies)
#
# Components (the only five that may ever be synced; anything else is refused):
#   meta-mender-core meta-mender-qemu meta-mender-raspberrypi meta-mender-nxp grub-mender-grubenv
#   meta-mender-commercial is NEVER vendored: refused by --sync, and --verify fails
#   if any path containing "commercial" exists under meta-otapulse-bsp/.
#
# Lock format (mender-upstream.lock; header comment in that file is authoritative):
#   cols 1-7  component url ref commit subpath vendored-dir tree-sha256 (PRISTINE upstream tree)
#   col  8    patched-tree-sha256  hash of the tree on disk after patches/mender/<c>/*.patch
#             ('-' when the component has no patches: the disk tree is then the pristine one)
#   col  9    patches-sha256       hash of the patch series ('-' when none)
#   col 10    license-sha256       sha256 of the added repo-root LICENSE copy ('-' when the
#                                  component ships its own LICENSE)
#   --verify checks the disk tree against col 8 when set, else col 7. Col 7 is reproducible
#   from a fresh clone (--verify --online does exactly that). Per-component manifests in
#   mender-upstream.manifests/ let a mismatch name the differing files.
#
# --sync is atomic: ALL work (fetch, extract, patch --check + apply, hash, LICENSE copy,
# UPSTREAM render) happens in a temp dir. The vendored tree, lock and manifest are touched
# only at the very end, by renames. Any earlier failure leaves them byte-identical.
# Syncing to the already-pinned ref changes nothing.
#
# Environment (for tests / hermetic use):
#   MENDER_SYNC_ROOT                 ota-pulse directory to operate on (default: this script's parent)
#   MENDER_SYNC_URL_<COMPONENT>      fetch from this URL instead of the real upstream, e.g.
#                                    MENDER_SYNC_URL_META_MENDER_CORE=file:///tmp/fake.git
#                                    (the lock still records the real https url)
#   TMPDIR                           where the temp work dir is created
#
# Exit codes: 0 ok; 1 verification failed / sync refused; 2 usage or infrastructure error.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="${MENDER_SYNC_ROOT:-$(cd "$SCRIPT_DIR/.." && pwd)}"
LOCK="$ROOT/mender-upstream.lock"
PATCHES="$ROOT/patches/mender"
MANIFESTS="$ROOT/mender-upstream.manifests"
BSP_REL="meta-otapulse-bsp"
TREEPY="$SCRIPT_DIR/mender_tree_hash.py"
LOCKPY="$SCRIPT_DIR/mender_lock.py"

KNOWN_COMPONENTS=(meta-mender-core meta-mender-qemu meta-mender-raspberrypi meta-mender-nxp grub-mender-grubenv)
declare -A C_URL=(
    [meta-mender-core]="https://github.com/mendersoftware/meta-mender"
    [meta-mender-qemu]="https://github.com/mendersoftware/meta-mender"
    [meta-mender-raspberrypi]="https://github.com/mendersoftware/meta-mender-community"
    [meta-mender-nxp]="https://github.com/mendersoftware/meta-mender-community"
    [grub-mender-grubenv]="https://github.com/mendersoftware/grub-mender-grubenv"
)
declare -A C_SUB=(
    [meta-mender-core]="meta-mender-core"
    [meta-mender-qemu]="meta-mender-qemu"
    [meta-mender-raspberrypi]="meta-mender-raspberrypi"
    [meta-mender-nxp]="meta-mender-nxp"
    [grub-mender-grubenv]="."
)
declare -A C_DIR=(
    [meta-mender-core]="$BSP_REL/core"
    [meta-mender-qemu]="$BSP_REL/qemu"
    [meta-mender-raspberrypi]="$BSP_REL/boards/raspberrypi"
    [meta-mender-nxp]="$BSP_REL/boards/nxp"
    [grub-mender-grubenv]="$BSP_REL/grub-mender-grubenv"
)
# add = the subpath has no LICENSE of its own: copy the upstream repo-root LICENSE in.
# own = the tree carries its own LICENSE.
declare -A C_LIC=(
    [meta-mender-core]="add"
    [meta-mender-qemu]="add"
    [meta-mender-raspberrypi]="add"
    [meta-mender-nxp]="add"
    [grub-mender-grubenv]="own"
)

FAILS=0
WORK=""
ALLOW_PROTO="https"
UP_URL=""
declare -a CLEANUP_PATHS=()
SWAP_ACTIVE=0
SWAP_DST=""; SWAP_OLD=""; SWAP_MAN=""; SWAP_LOCK_OLD=""; SWAP_MAN_OLD=""
SWAP_HAD_OLD=0; SWAP_HAD_LOCK=0; SWAP_HAD_MAN=0

log()  { printf 'mender-sync: %s\n' "$*"; }
warn() { printf 'mender-sync: WARNING: %s\n' "$*" >&2; }
fail() { printf 'mender-sync: FAIL: %s\n' "$*" >&2; FAILS=$((FAILS + 1)); }
die()  { printf 'mender-sync: error: %s\n' "$*" >&2; exit 1; }
infra_die() { printf 'mender-sync: error: %s\n' "$*" >&2; exit 2; }

# Undo an interrupted --sync swap (see cmd_sync): put the previous tree, lock and manifest
# back. Runs from the EXIT trap, so Ctrl-C / SIGTERM mid-swap cannot leave the vendored
# directory deleted or the lock describing a tree that is not on disk.
rollback_swap() {
    [ "$SWAP_ACTIVE" = 1 ] || return 0
    SWAP_ACTIVE=0
    if [ -d "$SWAP_OLD" ]; then
        rm -rf -- "$SWAP_DST"
        mv -- "$SWAP_OLD" "$SWAP_DST"
    elif [ "$SWAP_HAD_OLD" = 0 ]; then
        rm -rf -- "$SWAP_DST"
    fi
    if [ -f "$SWAP_LOCK_OLD" ]; then cp -p "$SWAP_LOCK_OLD" "$LOCK"; elif [ "$SWAP_HAD_LOCK" = 0 ]; then rm -f -- "$LOCK"; fi
    if [ -f "$SWAP_MAN_OLD" ]; then cp -p "$SWAP_MAN_OLD" "$SWAP_MAN"; elif [ "$SWAP_HAD_MAN" = 0 ]; then rm -f -- "$SWAP_MAN"; fi
    printf 'mender-sync: interrupted or failed mid-swap: previous tree, lock and manifest restored\n' >&2
}

cleanup() {
    local p
    rollback_swap
    for p in "${CLEANUP_PATHS[@]+"${CLEANUP_PATHS[@]}"}"; do
        rm -rf -- "$p"
    done
    if [ -n "$WORK" ] && [ -d "$WORK" ]; then
        rm -rf -- "$WORK"
    fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

usage() {
    sed -n '/^# Usage:/,/^# Components/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'
    exit 2
}

ensure_work() {
    if [ -z "$WORK" ]; then
        WORK="$(mktemp -d "${TMPDIR:-/tmp}/mender-sync.XXXXXX")"
    fi
}

sha256_of() { sha256sum -- "$1" | cut -d' ' -f1; }
tree_hash() { python3 "$TREEPY" "$1"; }
is_hex40() { [[ "$1" =~ ^[0-9a-f]{40}$ ]]; }
is_hex64() { [[ "$1" =~ ^[0-9a-f]{64}$ ]]; }

# ---------------------------------------------------------------------------
# component table / validation
# ---------------------------------------------------------------------------
validate_component() {
    local comp="$1"
    if [[ "${comp,,}" == *commercial* ]]; then
        die "refusing '$comp': meta-mender-commercial is NEVER vendored"
    fi
    if [ -z "${C_URL[$comp]+x}" ]; then
        die "unknown component '$comp' (known: ${KNOWN_COMPONENTS[*]})"
    fi
}

# Sets UP_URL (the URL to fetch from). Only MENDER_SYNC_URL_<COMPONENT> may override the
# real upstream (tests); that also (and only then) allows the file:// protocol. Must not be
# called in a subshell: it sets ALLOW_PROTO.
resolve_url() {
    local comp="$1" var
    var="MENDER_SYNC_URL_$(printf '%s' "$comp" | tr 'a-z-' 'A-Z_')"
    if [ -n "${!var:-}" ]; then
        ALLOW_PROTO="https:file"
        warn "$var is set: fetching $comp from ${!var} instead of ${C_URL[$comp]}"
        UP_URL="${!var}"
    else
        UP_URL="${C_URL[$comp]}"
    fi
}

# git for network operations: https only, no credential helpers, no askpass, no user
# or system git config (so no insteadOf rewrite to ssh, no stored tokens), never prompts.
git_net() {
    env -u GIT_ASKPASS -u SSH_ASKPASS \
        GIT_TERMINAL_PROMPT=0 GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null \
        GIT_ALLOW_PROTOCOL="$ALLOW_PROTO" \
        git -c credential.helper= -c core.autocrlf=false -c protocol.version=2 "$@"
}

load_row() {
    local row
    row="$(python3 "$LOCKPY" row "$LOCK" "$1")" || return 1
    # shellcheck disable=SC2034  # L_comp is part of the row layout; callers do not read it
    IFS=$'\t' read -r L_comp L_url L_ref L_commit L_sub L_dir L_tree L_patched L_patches L_lic <<<"$row"
}

up_get() { # <UPSTREAM file> <key>
    sed -n "s/^$2: *//p" "$1" | head -n 1
}

# ---------------------------------------------------------------------------
# --status
# ---------------------------------------------------------------------------
cmd_status() {
    local comp url kind name prefix out line sha ref latest latest_commit state rc=0
    [ -f "$LOCK" ] || infra_die "no lock file at $LOCK"
    printf '%-26s %-26s %-13s %-26s %s\n' COMPONENT PINNED PIN-COMMIT LATEST-UPSTREAM STATE
    for comp in "${KNOWN_COMPONENTS[@]}"; do
        if ! load_row "$comp"; then
            printf '%-26s %s\n' "$comp" "(no lock row)"
            continue
        fi
        resolve_url "$comp"
        url="$UP_URL"
        kind="${L_ref%%:*}"
        name="${L_ref#*:}"
        if [ "$kind" = "tag" ]; then
            prefix="$(printf '%s' "$name" | sed 's/[0-9].*//')"
            if ! out="$(git_net ls-remote --tags "$url" "${prefix}*" 2>&1)"; then
                warn "$comp: cannot list tags of $url: $out"
                rc=2
                continue
            fi
            declare -A tag_commit=()
            while read -r sha ref; do
                [ -n "${ref:-}" ] || continue
                ref="${ref#refs/tags/}"
                if [[ "$ref" == *'^{}' ]]; then
                    tag_commit["${ref%'^{}'}"]="$sha"
                elif [ -z "${tag_commit[$ref]+x}" ]; then
                    tag_commit["$ref"]="$sha"
                fi
            done <<<"$out"
            latest="$(printf '%s\n' "${!tag_commit[@]}" | grep -E "^${prefix}[0-9]+(\.[0-9]+)*$" | sort -V | tail -n 1 || true)"
            if [ -z "$latest" ]; then
                printf '%-26s %-26s %-13s %-26s %s\n' "$comp" "$L_ref" "${L_commit:0:12}" "(none found)" "?"
                unset tag_commit
                continue
            fi
            latest_commit="${tag_commit[$latest]}"
            if [ "$latest" = "$name" ]; then
                if [ "$latest_commit" = "$L_commit" ]; then
                    state="up-to-date"
                else
                    state="TAG MOVED upstream (now ${latest_commit:0:12}) -- investigate"
                fi
            elif [ -z "${tag_commit[$name]+x}" ]; then
                state="PINNED TAG NOT FOUND upstream; newest is $latest"
            else
                state="UPDATE AVAILABLE"
            fi
            printf '%-26s %-26s %-13s %-26s %s\n' "$comp" "$L_ref" "${L_commit:0:12}" "tag:$latest ${latest_commit:0:12}" "$state"
            unset tag_commit
        else
            if ! out="$(git_net ls-remote "$url" "refs/heads/$name" 2>&1)"; then
                warn "$comp: cannot list branch of $url: $out"
                rc=2
                continue
            fi
            line="$(printf '%s\n' "$out" | awk -v r="refs/heads/$name" '$2 == r {print $1}')"
            if [ -z "$line" ]; then
                printf '%-26s %-26s %-13s %-26s %s\n' "$comp" "$L_ref" "${L_commit:0:12}" "(branch gone)" "?"
            elif [ "$line" = "$L_commit" ]; then
                printf '%-26s %-26s %-13s %-26s %s\n' "$comp" "$L_ref" "${L_commit:0:12}" "branch:$name ${line:0:12}" "up-to-date"
            else
                printf '%-26s %-26s %-13s %-26s %s\n' "$comp" "$L_ref" "${L_commit:0:12}" "branch:$name ${line:0:12}" "branch head moved (may be unrelated sublayers; review upstream log)"
            fi
        fi
    done
    return "$rc"
}

# ---------------------------------------------------------------------------
# fetching + building the candidate tree
# ---------------------------------------------------------------------------
# fetch_checkout <dir> <url> sha|tag|branch <value> [<expected-commit>]
# Leaves <dir> a git work tree checked out (detached) at the verified commit, and sets
# FETCHED_COMMIT. For sha mode it fetches all heads+tags so reachability can be proven.
fetch_checkout() {
    local dir="$1" url="$2" mode="$3" value="$4" expect="${5:-}" got
    git init -q "$dir"
    git -C "$dir" remote add origin "$url"
    case "$mode" in
        tag)
            git_net -C "$dir" fetch -q --depth 1 origin "refs/tags/$value:refs/tags/$value" \
                || infra_die "fetching tag $value from $url failed"
            got="$(git -C "$dir" rev-parse "refs/tags/$value^{commit}")"
            ;;
        branch)
            git_net -C "$dir" fetch -q --depth 1 origin "refs/heads/$value:refs/remotes/origin/$value" \
                || infra_die "fetching branch $value from $url failed"
            got="$(git -C "$dir" rev-parse "refs/remotes/origin/$value^{commit}")"
            ;;
        sha)
            if ! git_net -C "$dir" fetch -q --no-tags --filter=blob:none origin \
                '+refs/heads/*:refs/remotes/origin/*' '+refs/tags/*:refs/tags/*' 2>/dev/null; then
                git_net -C "$dir" fetch -q --no-tags origin \
                    '+refs/heads/*:refs/remotes/origin/*' '+refs/tags/*:refs/tags/*' \
                    || infra_die "fetching $url failed"
            fi
            git -C "$dir" cat-file -e "$value^{commit}" 2>/dev/null \
                || die "commit $value does not exist in $url"
            if [ -z "$(git -C "$dir" for-each-ref --contains "$value" --format='%(refname)' refs/remotes/origin refs/tags | head -n 1)" ]; then
                die "commit $value is not reachable from any branch or tag of $url"
            fi
            got="$(git -C "$dir" rev-parse "$value^{commit}")"
            ;;
        *) infra_die "internal: bad fetch mode $mode" ;;
    esac
    if [ -n "$expect" ] && [ "$got" != "$expect" ]; then
        die "refusing: $mode '$value' resolved to $got after fetch but the remote advertised $expect (upstream changed mid-sync or the ref is not what was listed); re-run"
    fi
    is_hex40 "$got" || infra_die "unexpected commit id '$got'"
    git_net -C "$dir" -c advice.detachedHead=false checkout -q --detach "$got" \
        || infra_die "checkout of $got failed"
    FETCHED_COMMIT="$got"
}

# fetch an exact commit (for --verify --online): shallow by sha, else full fetch.
fetch_exact_commit() {
    local dir="$1" url="$2" sha="$3"
    git init -q "$dir"
    git -C "$dir" remote add origin "$url"
    if ! git_net -C "$dir" fetch -q --depth 1 origin "$sha" 2>/dev/null; then
        git_net -C "$dir" fetch -q --no-tags origin \
            '+refs/heads/*:refs/remotes/origin/*' '+refs/tags/*:refs/tags/*' \
            || infra_die "fetching $url failed"
    fi
    git -C "$dir" cat-file -e "$sha^{commit}" 2>/dev/null \
        || die "pinned commit $sha not found in $url"
    git_net -C "$dir" -c advice.detachedHead=false checkout -q --detach "$sha" \
        || infra_die "checkout of $sha failed"
    FETCHED_COMMIT="$sha"
}

list_patches() { # <component> -> NUL separated sorted absolute paths on stdout
    local d="$PATCHES/$1"
    [ -d "$d" ] || return 0
    find "$d" -maxdepth 1 \( -type f -o -type l \) -name '*.patch' -print0 | LC_ALL=C sort -z
}

# prepare_tree <component> <repo-checkout> <outdir>
# Builds <outdir>/pristine (upstream subpath), <outdir>/patched, sets P_PRISTINE, P_PATCHED
# ('-' when no patches), P_LIC_ADDED (0/1), P_LIC_SHA ('-' when none), P_PATCH_NAMES.
prepare_tree() {
    local comp="$1" repo="$2" out="$3" sub="${C_SUB[$1]}" src p name
    P_PATCH_NAMES=()
    mkdir -p "$out/pristine"
    if [ "$sub" = "." ]; then src="$repo"; else src="$repo/$sub"; fi
    [ -d "$src" ] || die "subpath '$sub' does not exist at the fetched commit"
    tar -C "$src" --exclude=.git -cf - . | tar -C "$out/pristine" -xpf -
    if [ -z "$(find "$out/pristine" -mindepth 1 -print -quit)" ]; then
        die "upstream subpath '$sub' is empty"
    fi
    if [ -e "$out/pristine/UPSTREAM" ]; then
        die "upstream tree contains a top-level file named UPSTREAM; the tree-hash algorithm cannot represent that"
    fi
    if [ -n "$(find "$out/pristine" -ipath '*commercial*' -print -quit)" ]; then
        die "refusing: upstream tree contains a path with 'commercial' (meta-mender-commercial is NEVER vendored)"
    fi
    # a hostile upstream tree (newline / control char / non-UTF-8 in a path, ...) is refused here
    P_PRISTINE="$(tree_hash "$out/pristine")" \
        || die "refusing the upstream tree: it contains a path or file the tree-hash algorithm rejects (see above)"

    cp -a "$out/pristine" "$out/patched"
    P_PATCHED="-"
    while IFS= read -r -d '' p; do
        name="$(basename "$p")"
        [[ "$name" =~ ^[0-9]{4}-[A-Za-z0-9._+-]+\.patch$ ]] \
            || die "patch '$name' must be named NNNN-description.patch (4 digits, no spaces)"
        [ ! -L "$p" ] && [ -f "$p" ] || die "patch '$name' must be a regular file (symlinks are refused)"
        # header paths may be followed by a tab/timestamp, so match the path then whitespace or EOL
        if grep -Eq '^(diff --git a/(UPSTREAM|LICENSE) b/|diff --git a/.* b/(UPSTREAM|LICENSE)([[:space:]]|$)|(---|\+\+\+) [ab]/(UPSTREAM|LICENSE)([[:space:]]|$)|(rename|copy) (from|to) (UPSTREAM|LICENSE)([[:space:]]|$))' "$p"; then
            die "patch '$name' touches UPSTREAM or LICENSE, which are not part of the hashed tree"
        fi
        if ! git -C "$out/patched" apply --check --whitespace=nowarn "$p" 2>"$out/apply.err"; then
            cat "$out/apply.err" >&2
            die "patch '$comp/$name' does not apply to the upstream tree at the requested ref -- regenerate the patch, never force (see patches/mender/README.md)"
        fi
        git -C "$out/patched" apply --whitespace=nowarn "$p" \
            || die "patch '$comp/$name' failed to apply after a successful --check -- regenerate the patch, never force"
        P_PATCH_NAMES+=("$name")
    done < <(list_patches "$comp")
    if [ "${#P_PATCH_NAMES[@]}" -gt 0 ]; then
        # authoritative check, independent of how the patch headers are spelled
        [ ! -e "$out/patched/UPSTREAM" ] || die "the patch series created a top-level UPSTREAM file"
        if [ -e "$out/pristine/LICENSE" ]; then
            cmp -s "$out/pristine/LICENSE" "$out/patched/LICENSE" || die "the patch series changed the upstream LICENSE"
        else
            [ ! -e "$out/patched/LICENSE" ] || die "the patch series created a top-level LICENSE"
        fi
        P_PATCHED="$(tree_hash "$out/patched")" \
            || die "refusing the patched tree: it contains a path the tree-hash algorithm rejects (see above)"
    fi
    if [ -n "$(find "$out/patched" -ipath '*commercial*' -print -quit)" ]; then
        die "refusing: patched tree contains a path with 'commercial'"
    fi

    P_LIC_ADDED=0
    P_LIC_SHA="-"
    if [ "${C_LIC[$comp]}" = "add" ]; then
        if [ -e "$out/patched/LICENSE" ]; then
            printf 'mender-sync: %s: upstream subpath ships its own LICENSE; not adding the repo-root copy\n' "$comp" >&2
        else
            [ -f "$repo/LICENSE" ] || die "upstream repo root has no LICENSE to copy for $comp"
            P_LIC_ADDED=1
            P_LIC_SHA="$(sha256_of "$repo/LICENSE")"
        fi
    elif [ ! -f "$out/patched/LICENSE" ]; then
        warn "$comp: expected the tree to carry its own LICENSE but none was found"
    fi
}

lock_row_string() { # echoes the TAB-separated 10-field row to write
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s' "$@"
}

# ---------------------------------------------------------------------------
# --sync
# ---------------------------------------------------------------------------
cmd_sync() {
    local comp="${1:-}" ref="${2:-}" dry=0 url mode name expect="" tagobj="-" out kind_existing=""
    shift $(( $# >= 2 ? 2 : $# ))
    while [ $# -gt 0 ]; do
        case "$1" in
            --dry-run) dry=1 ;;
            *) usage ;;
        esac
        shift
    done
    [ -n "$comp" ] && [ -n "$ref" ] || usage
    validate_component "$comp"
    [[ "$ref" =~ ^[A-Za-z0-9._+/-]+$ && "$ref" != -* && "$ref" != /* && "$ref" != *..* ]] \
        || die "illegal ref '$ref'"

    local have_row=0 old_row=""
    if load_row "$comp"; then
        have_row=1
        old_row="$(python3 "$LOCKPY" row "$LOCK" "$comp")"
        kind_existing="${L_ref%%:*}"
        [ "$L_url" = "${C_URL[$comp]}" ] || die "lock row for $comp has url '$L_url', expected '${C_URL[$comp]}'"
    fi

    resolve_url "$comp"
    url="$UP_URL"
    ensure_work
    out="$WORK"

    # --- resolve the ref ---------------------------------------------------
    local tag_sha="" tag_peeled="" head_sha="" sha refname lines
    if [[ "$ref" =~ ^[0-9a-f]{40}$ ]]; then
        mode="sha"
        name="$ref"
        expect="$ref"
    elif [[ "$ref" =~ ^[0-9a-fA-F]{7,39}$ ]]; then
        die "abbreviated commit id '$ref': pass the full 40-character sha"
    else
        lines="$(git_net ls-remote "$url" "refs/tags/$ref" "refs/tags/$ref^{}" "refs/heads/$ref")" \
            || infra_die "ls-remote $url failed"
        while read -r sha refname; do
            [ -n "${refname:-}" ] || continue
            case "$refname" in
                "refs/tags/$ref") tag_sha="$sha" ;;
                "refs/tags/$ref^{}") tag_peeled="$sha" ;;
                "refs/heads/$ref") head_sha="$sha" ;;
            esac
        done <<<"$lines"
        if [ -n "$tag_sha" ] && [ -n "$head_sha" ]; then
            if [ "$kind_existing" = "tag" ]; then head_sha=""; else tag_sha=""; fi
        fi
        if [ -n "$tag_sha" ]; then
            mode="tag"
            name="$ref"
            expect="${tag_peeled:-$tag_sha}"
            if [ -n "$tag_peeled" ]; then tagobj="$tag_sha"; fi
        elif [ -n "$head_sha" ]; then
            mode="branch"
            name="$ref"
            expect="$head_sha"
        else
            die "'$ref' is neither a tag nor a branch of $url"
        fi
    fi

    # --- fetch + verify the commit ----------------------------------------
    log "$comp: fetching $mode '$name' from $url"
    FETCHED_COMMIT=""
    fetch_checkout "$out/repo" "$url" "$mode" "$name" "$expect"
    local commit="$FETCHED_COMMIT" new_ref
    log "$comp: resolved to commit $commit"

    # --- ref label for the lock -------------------------------------------
    case "$mode" in
        tag) new_ref="tag:$name" ;;
        branch) new_ref="branch:$name" ;;
        sha)
            new_ref=""
            if [ "$have_row" = 1 ] && [ "$L_commit" = "$commit" ]; then
                new_ref="$L_ref"
            elif [ "$have_row" = 1 ]; then
                case "$L_ref" in
                    tag:*)
                        if [ "$(git -C "$out/repo" rev-parse -q --verify "refs/tags/${L_ref#tag:}^{commit}" 2>/dev/null || true)" = "$commit" ]; then
                            new_ref="$L_ref"
                        fi
                        ;;
                    branch:*)
                        if git -C "$out/repo" merge-base --is-ancestor "$commit" "refs/remotes/origin/${L_ref#branch:}" 2>/dev/null; then
                            new_ref="$L_ref"
                        fi
                        ;;
                esac
            fi
            if [ -z "$new_ref" ]; then
                local t
                t="$(git -C "$out/repo" tag --points-at "$commit" | LC_ALL=C sort -V | tail -n 1)"
                if [ -n "$t" ]; then
                    new_ref="tag:$t"
                else
                    t="$(git -C "$out/repo" for-each-ref --contains "$commit" --format='%(refname:strip=3)' refs/remotes/origin | grep -v '^HEAD$' | head -n 1 || true)"
                    [ -n "$t" ] || die "cannot label commit $commit (no branch or tag contains it)"
                    new_ref="branch:$t"
                fi
            fi
            if [[ "$new_ref" == tag:* ]]; then
                local ot
                ot="$(git -C "$out/repo" rev-parse -q --verify "refs/tags/${new_ref#tag:}" 2>/dev/null || true)"
                if [ -z "$ot" ]; then tagobj="keep"; elif [ "$ot" != "$commit" ]; then tagobj="$ot"; fi
            fi
            ;;
    esac

    # --- build the candidate tree -----------------------------------------
    prepare_tree "$comp" "$out/repo" "$out"
    local expected_hash="$P_PRISTINE" patches_hash lic_sha="$P_LIC_SHA"
    [ "$P_PATCHED" = "-" ] || expected_hash="$P_PATCHED"
    patches_hash="$(python3 "$LOCKPY" patches-hash "$PATCHES/$comp")"

    mkdir -p "$out/final"
    cp -a "$out/patched/." "$out/final/"
    if [ "$P_LIC_ADDED" = 1 ]; then
        cp -p "$out/repo/LICENSE" "$out/final/LICENSE"
    fi
    local old_upstream="$ROOT/${C_DIR[$comp]}/UPSTREAM" patch_args=() n
    [ -f "$old_upstream" ] || old_upstream="/dev/null"
    for n in "${P_PATCH_NAMES[@]+"${P_PATCH_NAMES[@]}"}"; do patch_args+=(--patch "$n"); done
    python3 "$LOCKPY" render-upstream "$old_upstream" \
        --component "$comp" --url "${C_URL[$comp]}" --ref "$new_ref" --commit "$commit" \
        --subpath "${C_SUB[$comp]}" --license-sha "$lic_sha" --tag-object "$tagobj" \
        "${patch_args[@]+"${patch_args[@]}"}" >"$out/final/UPSTREAM"

    # self-check: the assembled tree must hash to what the lock will say
    local final_hash
    final_hash="$(tree_hash "$out/final")"
    [ "$final_hash" = "$expected_hash" ] \
        || infra_die "internal: assembled tree hash $final_hash != $expected_hash"
    python3 "$TREEPY" --list "$out/final" >"$out/manifest"
    [ "$(sha256_of "$out/manifest")" = "$expected_hash" ] \
        || infra_die "internal: manifest hash != tree hash"

    # --- new lock ----------------------------------------------------------
    local new_row
    new_row="$(lock_row_string "$comp" "${C_URL[$comp]}" "$new_ref" "$commit" "${C_SUB[$comp]}" \
        "${C_DIR[$comp]}" "$P_PRISTINE" "$P_PATCHED" "$patches_hash" "$lic_sha")"
    if [ -f "$LOCK" ]; then cp -p "$LOCK" "$out/lock.new"; else : >"$out/lock.new"; fi
    python3 "$LOCKPY" set "$out/lock.new" "$comp" "${C_URL[$comp]}" "$new_ref" "$commit" \
        "${C_SUB[$comp]}" "${C_DIR[$comp]}" "$P_PRISTINE" "$P_PATCHED" "$patches_hash" "$lic_sha"

    # --- no-op detection ---------------------------------------------------
    local dst="$ROOT/${C_DIR[$comp]}" man="$MANIFESTS/$comp.manifest"
    if [ "$have_row" = 1 ] && [ "$old_row" = "$new_row" ] && [ -d "$dst" ] \
        && [ -f "$man" ] && cmp -s "$man" "$out/manifest" \
        && cmp -s "$dst/UPSTREAM" "$out/final/UPSTREAM" \
        && { [ "$P_LIC_ADDED" = 0 ] || { [ ! -L "$dst/LICENSE" ] && cmp -s "$dst/LICENSE" "$out/final/LICENSE"; }; } \
        && [ "$(tree_hash "$dst")" = "$expected_hash" ]; then
        log "$comp: already at $new_ref ($commit): no change"
        return 0
    fi

    log "$comp: ${L_commit:-<none>} -> $commit ($new_ref); tree $expected_hash; ${#P_PATCH_NAMES[@]} patch(es) applied"
    if [ "$dry" = 1 ]; then
        log "$comp: --dry-run: nothing written"
        return 0
    fi

    # --- atomic-ish swap (renames only; everything was verified above) ----
    local new="$dst.sync-new.$$" old="$dst.sync-old.$$"
    local lock_new="$LOCK.sync-new.$$" man_new="$MANIFESTS/.$comp.manifest.sync-new.$$"
    CLEANUP_PATHS+=("$new" "$lock_new" "$man_new")
    mkdir -p "$(dirname "$dst")" "$MANIFESTS"
    cp -a "$out/final" "$new"
    cp -p "$out/lock.new" "$lock_new"
    cp -p "$out/manifest" "$man_new"
    # Snapshot what rollback_swap() needs. $old is deliberately NOT in CLEANUP_PATHS: it is
    # the only copy of the previous tree while the swap is in flight.
    if [ -f "$LOCK" ]; then cp -p "$LOCK" "$out/lock.old"; SWAP_HAD_LOCK=1; fi
    if [ -f "$man" ]; then cp -p "$man" "$out/man.old"; SWAP_HAD_MAN=1; fi
    if [ -d "$dst" ]; then SWAP_HAD_OLD=1; fi
    SWAP_DST="$dst"; SWAP_OLD="$old"; SWAP_MAN="$man"
    SWAP_LOCK_OLD="$out/lock.old"; SWAP_MAN_OLD="$out/man.old"
    SWAP_ACTIVE=1
    if [ "$SWAP_HAD_OLD" = 1 ]; then mv -- "$dst" "$old"; fi
    # test-only fault injection: simulate an interrupt between the two renames
    if [ "${MENDER_SYNC_FAULT:-}" = "after-old-moved" ]; then kill -TERM $$; fi
    mv -- "$new" "$dst" || infra_die "could not move the new tree into place"
    mv -- "$lock_new" "$LOCK" || infra_die "could not update the lock"
    mv -- "$man_new" "$man" || infra_die "could not update the manifest"
    SWAP_ACTIVE=0
    rm -rf -- "$old"
    log "$comp: synced to $new_ref ($commit). Review with 'git diff --stat', then commit."
}

# ---------------------------------------------------------------------------
# --verify
# ---------------------------------------------------------------------------
verify_component() {
    local comp="$1" online="$2" dir up expected actual man ph lic_actual
    if ! load_row "$comp"; then
        fail "$comp: no row in $LOCK"
        return
    fi
    dir="$ROOT/${C_DIR[$comp]}"

    [ "$L_url" = "${C_URL[$comp]}" ] || fail "$comp: lock url '$L_url' != expected '${C_URL[$comp]}'"
    [ "$L_sub" = "${C_SUB[$comp]}" ] || fail "$comp: lock subpath '$L_sub' != expected '${C_SUB[$comp]}'"
    [ "$L_dir" = "${C_DIR[$comp]}" ] || fail "$comp: lock vendored-dir '$L_dir' != expected '${C_DIR[$comp]}'"
    is_hex40 "$L_commit" || fail "$comp: lock commit '$L_commit' is not a full 40-hex sha"
    [[ "$L_ref" =~ ^(tag|branch):[A-Za-z0-9._+/-]+$ ]] || fail "$comp: lock ref '$L_ref' is not tag:<x> or branch:<x>"
    is_hex64 "$L_tree" || fail "$comp: lock tree-sha256 '$L_tree' is not 64-hex"
    if [ "$L_patched" = "?" ]; then L_patched="-"; fi
    if [ "$L_patches" = "?" ]; then L_patches="-"; fi
    [ "$L_patched" = "-" ] || is_hex64 "$L_patched" || fail "$comp: lock patched-tree-sha256 '$L_patched' invalid"
    [ "$L_patches" = "-" ] || is_hex64 "$L_patches" || fail "$comp: lock patches-sha256 '$L_patches' invalid"

    if [ ! -d "$dir" ]; then
        fail "$comp: vendored directory ${C_DIR[$comp]} is missing"
        return
    fi

    # tree hash
    expected="$L_tree"
    [ "$L_patched" = "-" ] || expected="$L_patched"
    man="$MANIFESTS/$comp.manifest"
    if ! actual="$(python3 "$TREEPY" "$dir" 2>&1)"; then
        fail "$comp: cannot hash ${C_DIR[$comp]}: $actual"
        actual="$expected"   # already reported; skip the mismatch report below
    fi
    if [ "$actual" != "$expected" ]; then
        fail "$comp: vendored tree ${C_DIR[$comp]} differs from the lock (expected $expected, got $actual)"
        if [ -f "$man" ] && [ "$(sha256_of "$man")" = "$expected" ]; then
            local l
            while IFS= read -r l; do
                [ -n "$l" ] || continue
                fail "    ${l%% *} ${C_DIR[$comp]}/${l#* }   (M=modified A=extra file D=missing file)"
            done < <(python3 "$TREEPY" --diff "$man" "$dir" || true)
        else
            fail "    (no usable manifest at $man; cannot name the differing files -- compare with 'git diff')"
        fi
    elif [ -f "$man" ]; then
        [ "$(sha256_of "$man")" = "$expected" ] || fail "$comp: manifest $man does not hash to the lock's tree hash"
    else
        warn "$comp: no manifest at $man (run --upgrade-lock); mismatches cannot name files"
    fi

    # patch series
    if ! ph="$(python3 "$LOCKPY" patches-hash "$PATCHES/$comp")"; then
        fail "$comp: patch series in patches/mender/$comp is invalid (a symlinked or non-regular *.patch?)"
        ph="$L_patches"
    fi
    if [ "$ph" != "$L_patches" ]; then
        fail "$comp: patch series in patches/mender/$comp (hash $ph) differs from the lock ($L_patches); run --sync $comp <ref> to re-apply"
    fi
    if [ "$ph" != "-" ] && [ "$L_patched" = "-" ]; then
        fail "$comp: patches exist but the lock has no patched-tree-sha256"
    fi

    # UPSTREAM consistency
    up="$dir/UPSTREAM"
    if [ ! -f "$up" ]; then
        fail "$comp: $up is missing"
    else
        [ "$(up_get "$up" component)" = "$comp" ] || fail "$comp: UPSTREAM component != lock"
        [ "$(up_get "$up" url)" = "$L_url" ] || fail "$comp: UPSTREAM url != lock"
        [ "$(up_get "$up" ref)" = "$L_ref" ] || fail "$comp: UPSTREAM ref != lock ($(up_get "$up" ref) vs $L_ref)"
        [ "$(up_get "$up" commit)" = "$L_commit" ] || fail "$comp: UPSTREAM commit != lock"
        [ "$(up_get "$up" subpath)" = "$L_sub" ] || fail "$comp: UPSTREAM subpath != lock"
    fi

    # added-file: the tree hash excludes every path named on an "added-file:" line, so the set
    # must be exactly {LICENSE} when this component adds a LICENSE and {} otherwise. A planted
    # line would hide an extra file from the hash.
    # The list comes from mender_tree_hash.py -- the very parser that decides what the hash
    # excludes -- so there is exactly one reading of UPSTREAM (it also fails closed on
    # line-break characters other than LF).
    if [ -f "$up" ]; then
        local af_out af_path want_lic=0 got_lic=0
        if [ "${C_LIC[$comp]}" = "add" ] && { [ "$L_lic" = "?" ] || is_hex64 "$L_lic"; }; then want_lic=1; fi
        if ! af_out="$(python3 "$TREEPY" --added-files "$dir" 2>&1)"; then
            fail "$comp: UPSTREAM cannot be parsed: $af_out"
        else
            while IFS= read -r af_path; do
                [ -n "$af_path" ] || continue
                if [ "$af_path" = "LICENSE" ] && [ "$want_lic" = 1 ] && [ "$got_lic" = 0 ]; then
                    got_lic=1
                else
                    fail "$comp: UPSTREAM has an unexpected 'added-file:' entry '$af_path' (allowed: only LICENSE, only once, only when the lock records a license sha); it would hide that path from the tree hash"
                fi
            done <<<"$af_out"
            if [ "$want_lic" = 1 ] && [ "$got_lic" = 0 ]; then
                fail "$comp: UPSTREAM lacks the 'added-file: LICENSE' line"
            fi
        fi
    fi

    # LICENSE: offline sha256 check against the value recorded in the lock + UPSTREAM
    if [ "${C_LIC[$comp]}" = "add" ]; then
        if [ "$L_lic" = "?" ]; then
            warn "$comp: legacy lock row without license-sha256 (run --upgrade-lock); LICENSE not checked"
        elif ! is_hex64 "$L_lic"; then
            fail "$comp: lock license-sha256 '$L_lic' invalid (expected 64-hex for an added LICENSE)"
        elif [ -L "$dir/LICENSE" ] || [ ! -f "$dir/LICENSE" ]; then
            fail "$comp: ${C_DIR[$comp]}/LICENSE is missing or is not a regular file (symlinks are refused)"
        else
            lic_actual="$(sha256_of "$dir/LICENSE")"
            [ "$lic_actual" = "$L_lic" ] || fail "$comp: ${C_DIR[$comp]}/LICENSE differs from the recorded upstream LICENSE (sha256 $lic_actual != $L_lic)"
            if [ -f "$up" ]; then
                local recorded
                recorded="$(up_get "$up" added-file-sha256)"
                if [ -n "$recorded" ]; then
                    [ "$recorded" = "LICENSE $L_lic" ] || fail "$comp: UPSTREAM added-file-sha256 '$recorded' != lock"
                else
                    fail "$comp: UPSTREAM has no added-file-sha256 line (lock records $L_lic)"
                fi
            fi
        fi
    else
        if [ -L "$dir/LICENSE" ] || [ ! -f "$dir/LICENSE" ]; then
            fail "$comp: ${C_DIR[$comp]}/LICENSE is missing or is not a regular file (symlinks are refused)"
        fi
    fi

    if [ "$online" = 1 ]; then
        verify_online "$comp" "$dir"
    fi
}

declare -A FETCH_CACHE=()
verify_online() {
    local comp="$1" dir="$2" url key repo out
    resolve_url "$comp"
    url="$UP_URL"
    key="$url@$L_commit"
    ensure_work
    if [ -z "${FETCH_CACHE[$key]+x}" ]; then
        repo="$WORK/online-${#FETCH_CACHE[@]}"
        log "$comp: fetching $L_commit from $url for the fresh-clone comparison"
        FETCHED_COMMIT=""
        fetch_exact_commit "$repo" "$url" "$L_commit"
        FETCH_CACHE[$key]="$repo"
    fi
    repo="${FETCH_CACHE[$key]}"
    out="$WORK/online-out-$comp"
    rm -rf -- "$out"
    mkdir -p "$out"
    # prepare_tree dies on a patch conflict; run it in a subshell so we can report and continue
    local res
    if ! res="$( (prepare_tree "$comp" "$repo" "$out"; printf '%s %s %s %s' "$P_PRISTINE" "$P_PATCHED" "$P_LIC_ADDED" "$P_LIC_SHA") 2>"$out/err")"; then
        fail "$comp: online check could not rebuild the tree: $(tr '\n' ' ' <"$out/err")"
        return
    fi
    local o_pristine o_patched o_lic_added
    read -r o_pristine o_patched o_lic_added _ <<<"$res"
    [ "$o_pristine" = "$L_tree" ] || fail "$comp: ONLINE pristine tree hash of $L_commit ($o_pristine) != lock tree-sha256 ($L_tree)"
    if [ "$L_patched" != "-" ] || [ "$o_patched" != "-" ]; then
        [ "$o_patched" = "$L_patched" ] || fail "$comp: ONLINE pristine+patches hash ($o_patched) != lock patched-tree-sha256 ($L_patched)"
    fi
    if [ "$o_lic_added" = 1 ]; then
        if cmp -s "$repo/LICENSE" "$dir/LICENSE"; then
            log "$comp: LICENSE identical to upstream repo-root LICENSE at $L_commit"
        else
            fail "$comp: ${C_DIR[$comp]}/LICENSE is NOT byte-identical to upstream repo-root LICENSE at $L_commit"
        fi
    fi
}

cmd_verify() {
    local online=0 comp d row_comp
    while [ $# -gt 0 ]; do
        case "$1" in
            --online) online=1 ;;
            *) usage ;;
        esac
        shift
    done
    [ -f "$LOCK" ] || infra_die "no lock file at $LOCK"

    # every row in the lock must be a known component (never anything 'commercial')
    while IFS= read -r row_comp; do
        if [[ "${row_comp,,}" == *commercial* ]]; then
            fail "lock names a 'commercial' component: $row_comp (meta-mender-commercial is NEVER vendored)"
        elif [ -z "${C_URL[$row_comp]+x}" ]; then
            fail "lock row for unknown component '$row_comp'"
        fi
    done < <(python3 "$LOCKPY" components "$LOCK")

    for comp in "${KNOWN_COMPONENTS[@]}"; do
        verify_component "$comp" "$online"
    done

    # nothing 'commercial' anywhere under the bsp dir
    if [ -d "$ROOT/$BSP_REL" ]; then
        while IFS= read -r d; do
            [ -n "$d" ] || continue
            fail "forbidden path containing 'commercial': ${d#"$ROOT"/} (meta-mender-commercial is NEVER vendored)"
        done < <(find "$ROOT/$BSP_REL" -ipath '*commercial*' 2>/dev/null)
        # every vendored dir must be accounted for by the lock
        local known=" " c
        for c in "${KNOWN_COMPONENTS[@]}"; do known+="$ROOT/${C_DIR[$c]} "; done
        while IFS= read -r d; do
            if [[ "$known" != *" $d "* && "$d" != "$ROOT/$BSP_REL/boards" ]]; then
                fail "unlocked directory under $BSP_REL: ${d#"$ROOT"/} (not covered by mender-upstream.lock)"
            fi
        done < <(find "$ROOT/$BSP_REL" -mindepth 1 -maxdepth 2 -type d | LC_ALL=C sort | awk -v r="$ROOT/$BSP_REL" '
            { n = $0; sub("^" r "/", "", n); if (index(n, "/") == 0 || n ~ /^boards\//) print $0 }')
    else
        fail "$BSP_REL is missing"
    fi

    if [ "$FAILS" -gt 0 ]; then
        printf 'mender-sync: --verify FAILED (%d problem(s))\n' "$FAILS" >&2
        return 1
    fi
    log "--verify OK: ${#KNOWN_COMPONENTS[@]} vendored trees match mender-upstream.lock$([ "$online" = 1 ] && echo ' and fresh upstream clones')"
}

# ---------------------------------------------------------------------------
# --upgrade-lock (one-time migration of legacy 7-column rows; offline)
# ---------------------------------------------------------------------------
cmd_upgrade_lock() {
    local comp dir expected actual ph lic ref_label cur_patched
    [ -f "$LOCK" ] || infra_die "no lock file at $LOCK"
    ensure_work
    cp -p "$LOCK" "$WORK/lock.up"
    mkdir -p "$MANIFESTS"
    for comp in "${KNOWN_COMPONENTS[@]}"; do
        load_row "$comp" || die "$comp: no lock row"
        dir="$ROOT/${C_DIR[$comp]}"
        cur_patched="$L_patched"
        [ "$cur_patched" != "?" ] || cur_patched="-"
        expected="$L_tree"
        [ "$cur_patched" = "-" ] || expected="$cur_patched"
        actual="$(tree_hash "$dir")"
        [ "$actual" = "$expected" ] || die "$comp: tree does not match the lock; refusing to bless it (run --verify)"
        ph="$(python3 "$LOCKPY" patches-hash "$PATCHES/$comp")"
        if { [ "$ph" = "-" ] && [ "$cur_patched" != "-" ]; } || { [ "$ph" != "-" ] && [ "$cur_patched" = "-" ]; }; then
            die "$comp: patch series and lock disagree; use --sync"
        fi
        if [ "$L_lic" != "?" ]; then
            lic="$L_lic"
        elif [ "${C_LIC[$comp]}" = "add" ]; then
            [ -f "$dir/LICENSE" ] || die "$comp: LICENSE missing"
            lic="$(sha256_of "$dir/LICENSE")"
        else
            lic="-"
        fi
        ref_label="$L_ref"
        python3 "$LOCKPY" set "$WORK/lock.up" "$comp" "$L_url" "$ref_label" "$L_commit" "$L_sub" "$L_dir" \
            "$L_tree" "$cur_patched" "$ph" "$lic"
        local pargs=() n
        while IFS= read -r -d '' n; do pargs+=(--patch "$(basename "$n")"); done < <(list_patches "$comp")
        python3 "$LOCKPY" render-upstream "$dir/UPSTREAM" --component "$comp" --url "$L_url" --ref "$L_ref" \
            --commit "$L_commit" --subpath "$L_sub" --license-sha "$lic" --tag-object keep \
            "${pargs[@]+"${pargs[@]}"}" >"$WORK/upstream.$comp"
        python3 "$TREEPY" --list "$dir" >"$WORK/manifest.$comp"
        [ "$(sha256_of "$WORK/manifest.$comp")" = "$expected" ] || infra_die "internal: manifest hash mismatch for $comp"
    done
    # all components validated: now write
    for comp in "${KNOWN_COMPONENTS[@]}"; do
        cat "$WORK/upstream.$comp" >"$ROOT/${C_DIR[$comp]}/UPSTREAM"
        cp "$WORK/manifest.$comp" "$MANIFESTS/$comp.manifest"
    done
    cp "$WORK/lock.up" "$LOCK"
    log "lock upgraded to 10 columns; manifests written to ${MANIFESTS#"$ROOT"/}/"
}

main() {
    [ $# -ge 1 ] || usage
    command -v python3 >/dev/null 2>&1 || infra_die "python3 is required"
    command -v git >/dev/null 2>&1 || infra_die "git is required"
    case "$1" in
        --status) shift; cmd_status "$@" ;;
        --sync) shift; cmd_sync "$@" ;;
        --verify) shift; cmd_verify "$@" ;;
        --upgrade-lock) shift; cmd_upgrade_lock "$@" ;;
        -h|--help) usage ;;
        *) usage ;;
    esac
}

main "$@"
