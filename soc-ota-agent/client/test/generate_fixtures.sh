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
# BUG-446: regenerates the real mTLS test-server cert/key pair used by
# app/mender_test.go's TestMutualTLSClientConnection[WithReverseProxy] and
# app/proxy/proxy_ws_test.go's TestProxyWsConnect* (real tls.LoadX509KeyPair
# calls, and real crypto/tls handshakes against 127.0.0.1). server.crt
# survived in git, but its matching private key never did (server.key,
# blocked by the repo's *.key gitignore rule until the BUG-446 exception was
# added for this directory) — and a certificate's public key never lets you
# recover the private key that made it, so the only way to make this pair
# usable again is regenerating both together.
#
# Not cryptographically sensitive: a throwaway, publicly-committed,
# localhost-only test fixture, never used to protect anything real. Re-run
# this script (from client/test/) any time this pair needs regenerating.
set -euo pipefail
cd "$(dirname "$0")"

openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 36500 \
	-keyout server.key -out server.crt \
	-subj "/C=XX/ST=NA/L=NA/O=Self-signed certificate/CN=127.0.0.1: Self-signed certificate" \
	-addext "subjectAltName=IP:127.0.0.1,IP:::1" \
	>/dev/null 2>&1

chmod 644 server.key server.crt
echo "Regenerated server.key, server.crt"
