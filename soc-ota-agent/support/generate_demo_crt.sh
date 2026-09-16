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
# BUG-446: regenerates support/demo.crt, consumed by
# cli/setup_test.go's TestInstallDemoCertificateLocalTrust
# (cli.installDemoCertificateLocalTrust splits it on "END CERTIFICATE"
# markers, one file per embedded PEM cert - the test asserts exactly 3
# resulting files). This file never existed in the repo at all (not a
# gitignore casualty like the *.key fixtures elsewhere - just never
# checked in), so the test never ran and passed even once.
#
# Not cryptographically sensitive: 3 throwaway, publicly-committed,
# self-signed placeholder certs standing in for a real Mender/OTA-Pulse
# demo-server trust bundle - installDemoCertificateLocalTrust only cares
# about splitting well-formed PEM blocks, never validates them against a
# live server. Re-run this script (from support/) any time this fixture
# needs regenerating.
set -euo pipefail
cd "$(dirname "$0")"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

: >demo.crt
for i in 1 2 3; do
	openssl req -x509 -newkey rsa:2048 -nodes -sha256 -days 36500 \
		-keyout "$tmp/demo-$i.key" -out "$tmp/demo-$i.crt" \
		-subj "/C=XX/ST=NA/L=NA/O=OTA-Pulse Demo/CN=demo-mender-cert-$i" \
		>/dev/null 2>&1
	cat "$tmp/demo-$i.crt" >>demo.crt
done

chmod 644 demo.crt
echo "Regenerated demo.crt (3 PEM certificates)"
