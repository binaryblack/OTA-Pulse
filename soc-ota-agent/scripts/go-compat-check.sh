#!/usr/bin/env bash
# go-compat-check.sh -- guard against using Go language/stdlib features newer
# than the OLDEST toolchain any board image builds the agent with.
#
# Background: the Jetson (meta-tegra / poky-kirkstone) image builds the agent
# with Go 1.17.13 (poky-kirkstone/meta/recipes-devtools/go), while other boards
# use Go 1.22.x. go.mod says `go 1.21`, but Go 1.17 does not enforce that
# directive: it only prints "note: module requires Go 1.21" AFTER a compile
# error. So any API newer than 1.17 (e.g. os/exec.Cmd.WaitDelay, Go 1.20)
# breaks ONLY the Jetson build, and only at image-build time.
#
# Strategy (strongest available first):
#   1. REAL oldest toolchain: $GO_COMPAT_GO (path to a go<=1.17 binary), else
#      a go1.17 from ~/sdk/go1.17*/ or the Yocto cache
#      (/mnt/storage/yocto-cache/builds/*/tmp/sysroots-components/x86_64/
#      go-binary-native). Runs `go build ./...` + `go vet` (which also compiles
#      the _test.go files) with -mod=vendor. This is authoritative.
#   2. Fallback when no old toolchain exists: `go build -gcflags=-lang=go1.17`
#      -- LIMIT: only rejects post-1.17 LANGUAGE features (generics, `any`,
#      ...), NOT newer stdlib APIs, because the host stdlib is still the new
#      one.
#   3. ALWAYS: grep for well-known post-1.17 stdlib symbols/builtins in
#      non-vendor .go files. LIMIT: a denylist of known symbols, not a
#      proof; comments are stripped crudely (`//...`), so a symbol inside a
#      string literal can false-positive. Extend DENY below when a new one
#      bites.
#
# Usage: scripts/go-compat-check.sh        (run from soc-ota-agent/, or `make go-compat`)
# Env:   GO_COMPAT_GO=/path/to/go1.17/bin/go   force a specific old toolchain
#        GO_COMPAT_NO_OLD=1                    ignore old toolchains, exercise the fallback
#        GO_COMPAT_REQUIRE_OLD=1               fail (rather than fall back) if no <=1.17 toolchain is found
set -uo pipefail
cd "$(dirname "$0")/.."

MAX_MINOR=17
rc=0

# ---- 1/2. toolchain-based check -------------------------------------------
minor_of() { "$1" version 2>/dev/null | sed -n 's/.*go1\.\([0-9][0-9]*\).*/\1/p'; }

old_go=""
cands=()
[ -n "${GO_COMPAT_GO:-}" ] && cands+=("$GO_COMPAT_GO")
for c in "$HOME"/sdk/go1.1[0-7]*/bin/go \
         /mnt/storage/yocto-cache/builds/*/tmp/sysroots-components/x86_64/go-binary-native/usr/bin/go; do
  [ -x "$c" ] && cands+=("$c")
done
[ "${GO_COMPAT_NO_OLD:-0}" = 1 ] && cands=()   # force the fallback path (testing)
for c in "${cands[@]:-}"; do
  [ -n "$c" ] || continue
  m="$(minor_of "$c")"
  if [ -n "$m" ] && [ "$m" -le "$MAX_MINOR" ]; then old_go="$c"; break; fi
done

# vendor/ is gitignored; -mod=vendor is what the Yocto recipe uses. Without
# it the module cache/network is needed.
modflag=""
[ -f vendor/modules.txt ] && modflag="-mod=vendor"

if [ -n "$old_go" ]; then
  echo "-- go-compat: using real old toolchain: $($old_go version) ($old_go)"
  # The Yocto go-binary-native is relocatable via its own GOROOT.
  goroot="$(cd "$(dirname "$old_go")/.." && pwd)/lib/go"
  [ -d "$goroot" ] || goroot="$(cd "$(dirname "$old_go")/.." && pwd)"
  export GOROOT="$goroot"
  export GOCACHE="${GO_COMPAT_GOCACHE:-${TMPDIR:-/tmp}/go-compat-cache-1.$(minor_of "$old_go")}"
  export GOFLAGS="$modflag"
  export CGO_ENABLED=1 CGO_CFLAGS="${CGO_CFLAGS:--Wno-implicit-fallthrough -Wno-stringop-overflow}"
  echo "-- go-compat: go build ./..."
  "$old_go" build ./... 2>&1 | grep -v 'mdb\.c\|^ *[0-9]* |\|^ *|\|^# github.com/bmatsuo/lmdb-go' ; [ "${PIPESTATUS[0]}" -eq 0 ] || { echo "FAIL: go build with $($old_go version)"; rc=1; }
  echo "-- go-compat: go vet ./... (compiles _test.go too)"
  "$old_go" vet -composites=false -unsafeptr=false ./... 2>&1 | grep -v 'mdb\.c\|^ *[0-9]* |\|^ *|\|^# github.com/bmatsuo/lmdb-go' ; [ "${PIPESTATUS[0]}" -eq 0 ] || { echo "FAIL: go vet with $($old_go version)"; rc=1; }
elif [ "${GO_COMPAT_REQUIRE_OLD:-0}" = 1 ]; then
  echo "FAIL: no Go <= 1.$MAX_MINOR toolchain found (set GO_COMPAT_GO) and GO_COMPAT_REQUIRE_OLD=1"
  exit 1
else
  echo "-- go-compat: WARNING no Go <= 1.$MAX_MINOR toolchain found; falling back to -lang=go1.$MAX_MINOR"
  echo "--            (language features only; stdlib APIs are checked by the grep denylist below)"
  GO="${GO:-go}"
  GOFLAGS="$modflag" "$GO" build -gcflags=-lang=go1.$MAX_MINOR ./... || { echo "FAIL: go build -lang=go1.$MAX_MINOR"; rc=1; }
fi

# ---- 3. grep denylist of known post-1.17 symbols ---------------------------
DENY='\.WaitDelay\b|\.Cancel[[:space:]]*=|context\.(WithCancelCause|WithoutCancel|AfterFunc|WithTimeoutCause|WithDeadlineCause|Cause)\b'
DENY+='|errors\.Join\b|"(slices|maps|cmp|iter|log/slog|unique)"'
DENY+='|atomic\.(Int32|Int64|Uint32|Uint64|Uintptr|Bool|Pointer)\b'
DENY+='|sync\.Once(Func|Value|Values)\b'
DENY+='|(strings|bytes)\.(Cut|CutPrefix|CutSuffix|Clone|ContainsFunc)\b'
DENY+='|time\.(DateTime|DateOnly|TimeOnly)\b|\.(UnixMilli|UnixMicro)\(|time\.(UnixMilli|UnixMicro)\('
DENY+='|(^|[^.A-Za-z_0-9])(min|max|clear)\('
DENY+='|http\.NewResponseController|netip\.|fmt\.Append(f|ln)?\b'

echo "-- go-compat: grepping non-vendor sources for known post-1.$MAX_MINOR symbols"
hits=0
while IFS= read -r f; do
  out="$(sed 's://.*$::' "$f" | grep -nE "$DENY" || true)"
  if [ -n "$out" ]; then
    echo "$out" | sed "s|^|$f:|"
    hits=1
  fi
done < <(find . -name '*.go' -not -path './vendor/*' -not -path './third_party_licenses/*')
if [ "$hits" -ne 0 ]; then
  echo "FAIL: post-Go-1.$MAX_MINOR symbol(s) above; the Jetson/meta-tegra (kirkstone) toolchain is Go 1.17.13"
  rc=1
fi

[ "$rc" -eq 0 ] && echo "go-compat: OK (oldest supported Go 1.$MAX_MINOR)"
exit "$rc"
