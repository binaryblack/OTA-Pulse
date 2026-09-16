#!/usr/bin/env bash
# Copyright 2026 SoC Monitoring
#
#	Licensed under the Apache License, Version 2.0 (the "License");
#	you may not use this file except in compliance with the License.
#	You may obtain a copy of the License at
#
#	    http://www.apache.org/licenses/LICENSE-2.0
#
#	Unless required by applicable law or agreed to in writing, software
#	distributed under the License is distributed on an "AS IS" BASIS,
#	WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#	See the License for the specific language governing permissions and
#	limitations under the License.
#
# BUG-446: regenerates the client-cert-key-pair fixtures consumed by
# client_test.go's TestLoadingTrust ("Test loading client trust" subtest) and
# TestListSystemCertsFound. These four files went missing from testdata/ at
# some point (client-cert.key and wrong.key were never checked in at all;
# client.crt survived but its ORIGINAL matching private key did not, which
# means it can never be recovered — a certificate's public key does not let
# you derive the private key that made it). Regenerating all four together
# from scratch, in one deterministic script, is the only way to make them a
# consistent, working set again:
#
#   client-cert.key   - the "correct" client private key
#   client.crt        - the "correct" client certificate, signed by
#                        client-cert.key (self-signed; loadClientTrust never
#                        validates the chain against a root, it only loads
#                        cert+key into the OpenSSL ctx)
#   chain-cert.crt    - client.crt followed by a second, unrelated self-signed
#                        certificate, i.e. a 2-entry PEM chain, to exercise
#                        loadClientTrust's ctx.AddChainCertificate loop
#   wrong.key         - a private key that does NOT match client.crt, used to
#                        exercise the "Correct certificate, wrong key" case
#                        (expects OpenSSL's real "key values mismatch" error
#                        out of ctx.UsePrivateKey)
#
# Not cryptographically sensitive: these are throwaway, publicly-committed
# test fixtures, never used to protect anything real. Re-run this script
# (from the client/testdata/ directory) any time these fixtures need to be
# regenerated; every run produces a fresh, internally-consistent set (exact
# byte contents differ run to run, since key generation and X.509 serial/
# validity fields are randomized/time-based, but the pass/fail behavior of
# every test that consumes them is deterministic).
set -euo pipefail
cd "$(dirname "$0")"

subj="/C=XX/ST=NA/L=NA/O=BUG-446 test fixture/CN=127.0.0.1: BUG-446 test fixture"

# 1. The "correct" client key + self-signed certificate.
openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 36500 \
	-keyout client-cert.key -out client.crt \
	-subj "$subj" >/dev/null 2>&1

# 2. A second, unrelated self-signed certificate to append after client.crt,
# forming a 2-entry chain file for the "Certificate chain loading" case.
openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 36500 \
	-keyout /tmp/bug446-chain-tail.key -out /tmp/bug446-chain-tail.crt \
	-subj "/C=XX/ST=NA/L=NA/O=BUG-446 test fixture/CN=chain-tail" \
	>/dev/null 2>&1
cat client.crt /tmp/bug446-chain-tail.crt >chain-cert.crt
rm -f /tmp/bug446-chain-tail.key /tmp/bug446-chain-tail.crt

# 3. A private key that does NOT correspond to client.crt's public key, for
# the "Correct certificate, wrong key" mismatch case.
openssl genrsa -out wrong.key 2048 >/dev/null 2>&1

chmod 644 client-cert.key client.crt chain-cert.crt wrong.key
echo "Regenerated client-cert.key, client.crt, chain-cert.crt, wrong.key"
