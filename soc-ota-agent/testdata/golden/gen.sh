#!/usr/bin/env bash
# Regenerates the golden artifact vectors (see README.md). TEST-ONLY material:
# the RSA key below is committed on purpose and protects nothing.
#
# Requires: otapulse-artifact (or mender-artifact; same container format),
# openssl, python3. The Go test (installer/golden_vectors_test.go) does not
# need any of them; it only reads the checked-in files.
set -euo pipefail
cd "$(dirname "$0")"
TOOL="${ARTIFACT_TOOL:-$(command -v otapulse-artifact || command -v mender-artifact)}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

if [[ ! -f test-only-signing-key.pem ]]; then
  openssl genrsa -out test-only-signing-key.pem 2048 2>/dev/null
  openssl rsa -in test-only-signing-key.pem -pubout -out test-public.pem 2>/dev/null
  openssl genrsa -out wrong-private.pem 2048 2>/dev/null
  openssl rsa -in wrong-private.pem -pubout -out wrong-public.pem 2>/dev/null
  rm -f wrong-private.pem   # only the wrong PUBLIC key is needed (negative test)
fi

# Deterministic tiny payloads.
head -c 4096 /dev/zero | tr '\0' 'R' > "$WORK/rootfs.img"
printf 'golden module payload\n' > "$WORK/hello.txt"
printf 'golden-xdelta3-patch-bytes' > "$WORK/delta.patch"
cat > "$WORK/delta-meta.json" <<JSON
{"delta_format": "xdelta3", "target_size": 4096}
JSON
BASE_SUM="$(sha256sum "$WORK/rootfs.img" | cut -d' ' -f1)"

DT=golden-device

"$TOOL" write rootfs-image -n golden-rootfs -t "$DT" -f "$WORK/rootfs.img" \
  -o "$WORK/rootfs-mender-unsigned.otapulse" >/dev/null
"$TOOL" write module-image -T single-file -n golden-module -t "$DT" \
  -f "$WORK/hello.txt" --software-name golden \
  -o "$WORK/module-mender-unsigned.otapulse" >/dev/null
"$TOOL" write module-image -T rootfs-image-delta -n golden-delta -t "$DT" \
  -f "$WORK/delta.patch" -m "$WORK/delta-meta.json" \
  -d "rootfs-image.checksum:$BASE_SUM" -p "rootfs-image.checksum:$BASE_SUM" \
  -o "$WORK/rootfs-delta-mender-unsigned.otapulse" >/dev/null

for kind in rootfs module rootfs-delta; do
  # mender-id (stock) variant: unchanged.
  cp "$WORK/$kind-mender-unsigned.otapulse" "$kind-mender-unsigned.otapulse"
  # otapulse-id variant: `version` member -> {"format":"otapulse","version":3}
  # with the manifest line for `version` re-checksummed.
  python3 - "$WORK/$kind-mender-unsigned.otapulse" "$kind-otapulse-unsigned.otapulse" <<'PY'
import hashlib, io, sys, tarfile
tarfile.RECORDSIZE = 512
src, dst = sys.argv[1], sys.argv[2]
new_version = b'{"format":"otapulse","version":3}'
tin = tarfile.open(src)
members = [(m, tin.extractfile(m).read() if m.isfile() else None) for m in tin.getmembers()]
out = io.BytesIO()
with tarfile.open(fileobj=out, mode="w", format=tarfile.USTAR_FORMAT) as tout:
    for m, data in members:
        if m.name == "version":
            data = new_version
        elif m.name == "manifest":
            lines = []
            for line in data.decode().splitlines():
                digest, name = line.split(None, 1)
                if name.strip() == "version":
                    digest = hashlib.sha256(new_version).hexdigest()
                lines.append(f"{digest}  {name.strip()}")
            data = ("\n".join(lines) + "\n").encode()
        m.size = len(data)
        m.mtime = 0
        tout.addfile(m, io.BytesIO(data))
open(dst, "wb").write(out.getvalue())
PY
  for id in mender otapulse; do
    "$TOOL" sign -k test-only-signing-key.pem \
      -o "$kind-$id-signed.otapulse" "$kind-$id-unsigned.otapulse" >/dev/null
  done
done
ls -l *.otapulse
