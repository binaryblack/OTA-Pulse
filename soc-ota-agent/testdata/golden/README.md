# Golden OTA-Pulse artifact vectors (TODO-011 / TASK-S102-003)

Twelve tiny artifacts that pin the container format: three payload kinds times two
format identifiers times signed/unsigned. Any change to the container (or to the
code that reads it) must keep all of them accepted.

| File | Payload | `version` member | Signed |
|------|---------|------------------|--------|
| `rootfs-{mender,otapulse}-{signed,unsigned}.otapulse` | `rootfs-image` (4 KiB of `R`, not a real filesystem) | `{"format":"mender","version":3}` / `{"format":"otapulse","version":3}` | yes / no |
| `module-...` | `single-file` update module, one 22-byte file | same | same |
| `rootfs-delta-...` | `rootfs-image-delta` (dummy patch bytes, `delta_format`/`target_size` meta-data, `rootfs-image.checksum` depends+provides) | same | same |

Common header values: artifact name `golden-rootfs` / `golden-module` / `golden-delta`,
compatible device type `golden-device`, signature algorithm RSA-2048 (key below).

Keys (**TEST ONLY - committed on purpose, protect nothing, never trust them anywhere**):

* `test-only-signing-key.pem` / `test-public.pem` - sign / verify the `*-signed.otapulse` files.
The private key is committed so that re-running `gen.sh` reuses it and the committed
`test-public.pem` stays valid.

* `wrong-public.pem` - an unrelated public key for negative tests (verification must fail).

## How they were built

`gen.sh` (needs `otapulse-artifact` or `mender-artifact`, `openssl`, `python3`):

1. `write rootfs-image` / `write module-image -T single-file` /
   `write module-image -T rootfs-image-delta` produce the three unsigned `mender`-id artifacts
   (both tools write `{"format":"mender","version":3}` today).
2. The `otapulse`-id variants are made by rewriting the tar's `version` member to
   `{"format":"otapulse","version":3}` and fixing the `version` line of `manifest`
   (the manifest covers `version`, so the signature must be made *after* this step).
   Member names and order are untouched - the container is still the Mender v3 layout.
3. `sign -k test-only-signing-key.pem` produces every `*-signed` file from its `*-unsigned` sibling.

Re-running `gen.sh` regenerates equivalent but not byte-identical files (tar mtimes, signature
randomness). Do not regenerate casually: other test suites read these exact files.

## Consumers

* Go: `installer/golden_vectors_test.go` drives every file through `installer.ReadHeaders`
  (signed path with the verification key, unsigned path with none, wrong key rejected).
* Python (backend `signature_checker` / `read_artifact_info`, `artifact_writer` round trip) and
  TypeScript (`server/src/artifact/inspect.ts`) tests read the same files; paths relative to this
  directory, names exactly as above.
* CLI: `otapulse-artifact validate -k testdata/golden/test-public.pem <signed file>` must succeed,
  and with `wrong-public.pem` must fail with a verification error.
