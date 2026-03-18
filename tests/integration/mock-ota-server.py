#!/usr/bin/env python3
"""
mock-ota-server.py — Minimal OTA server for integration testing

Implements the subset of the OTA API needed for end-to-end testing:
  - Device authentication (POST /api/devices/v1/authentication/auth_requests)
  - Deployment check (GET /api/devices/v1/deployments/device/deployments/next)
  - Artifact download (GET /api/devices/v1/deployments/device/deployments/{id}/artifact)
  - Status reporting (PUT /api/devices/v1/deployments/device/deployments/{id}/status)

Usage:
    python3 mock-ota-server.py [OPTIONS]

Options:
    --port PORT         Listen port (default: 8443)
    --artifact FILE     .mender artifact to serve
    --device-type TYPE  Expected device type (default: any)
    --no-tls            Disable TLS (use HTTP)
"""

import argparse
import hashlib
import http.server
import json
import os
import ssl
import sys
import tempfile
import uuid
from datetime import datetime, timezone


class MockOTAState:
    """Shared state for the mock server."""

    def __init__(self, artifact_path=None, device_type=None, use_tls=False):
        self.artifact_path = artifact_path
        self.device_type = device_type
        self.use_tls = use_tls
        self.auth_tokens = {}  # device_id -> token
        self.deployments = {}  # deployment_id -> state
        self.deployment_id = str(uuid.uuid4())
        self.device_statuses = {}  # device_id -> last status


state = MockOTAState()


class MockOTAHandler(http.server.BaseHTTPRequestHandler):
    """HTTP handler implementing minimal OTA API."""

    def log_message(self, fmt, *args):
        sys.stderr.write(f"[{datetime.now(timezone.utc).isoformat()}] {fmt % args}\n")

    def send_json(self, code, data):
        body = json.dumps(data).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def read_body(self):
        length = int(self.headers.get("Content-Length", 0))
        return self.rfile.read(length) if length > 0 else b""

    def send_no_content(self):
        """Send a proper 204 No Content response (no body)."""
        self.send_response(204)
        self.end_headers()

    def do_POST(self):
        # Authentication endpoint
        if self.path == "/api/devices/v1/authentication/auth_requests":
            body = self.read_body()
            try:
                data = json.loads(body)
            except (json.JSONDecodeError, ValueError):
                self.send_json(400, {"error": "invalid JSON"})
                return

            device_id = data.get("id_data", {}).get("SerialNumber", str(uuid.uuid4()))
            token = f"mock-jwt-{uuid.uuid4()}"
            state.auth_tokens[device_id] = token

            self.log_message("AUTH: device=%s", device_id)
            self.send_response(200)
            self.send_header("Content-Type", "application/jwt")
            body = token.encode()
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return

        # Status reporting
        if "/status" in self.path:
            body = self.read_body()
            try:
                data = json.loads(body)
            except (json.JSONDecodeError, ValueError):
                data = {}
            status = data.get("status", "unknown")
            self.log_message("STATUS: %s -> %s", self.path, status)
            self.send_no_content()
            return

        # Log reporting
        if "/log" in self.path:
            self.read_body()
            self.send_no_content()
            return

        self.send_json(404, {"error": "not found"})

    def do_GET(self):
        # Deployment check
        if "/deployments/next" in self.path or self.path.endswith("/deployments/device/deployments/next"):
            if state.artifact_path and os.path.exists(state.artifact_path):
                proto = "https" if state.use_tls else "http"
                host = self.headers.get("Host", "localhost")
                deployment = {
                    "id": state.deployment_id,
                    "artifact": {
                        "artifact_name": "test-release",
                        "device_types_compatible": [state.device_type or "any"],
                        "source": {
                            "uri": f"{proto}://{host}/api/devices/v1/deployments/device/deployments/{state.deployment_id}/artifact",
                            "expire": "2099-01-01T00:00:00Z",
                        },
                    },
                }
                self.log_message("DEPLOY: serving deployment %s", state.deployment_id)
                self.send_json(200, deployment)
            else:
                # No deployment available
                self.send_response(204)
                self.end_headers()
            return

        # Artifact download
        if "/artifact" in self.path and state.artifact_path:
            if not os.path.exists(state.artifact_path):
                self.send_json(404, {"error": "artifact not found"})
                return

            file_size = os.path.getsize(state.artifact_path)
            self.log_message("DOWNLOAD: serving %s (%d bytes)", state.artifact_path, file_size)

            self.send_response(200)
            self.send_header("Content-Type", "application/vnd.mender-artifact")
            self.send_header("Content-Length", str(file_size))
            self.end_headers()

            with open(state.artifact_path, "rb") as f:
                while True:
                    chunk = f.read(65536)
                    if not chunk:
                        break
                    self.wfile.write(chunk)
            return

        self.send_json(404, {"error": "not found"})

    def do_PUT(self):
        # Status updates
        body = self.read_body()
        self.log_message("PUT %s: %s", self.path, body.decode(errors="replace")[:200])
        self.send_response(204)
        self.end_headers()


def main():
    parser = argparse.ArgumentParser(description="Mock OTA server for integration testing")
    parser.add_argument("--port", type=int, default=8443, help="Listen port")
    parser.add_argument("--artifact", help="Path to .mender artifact to serve")
    parser.add_argument("--device-type", help="Expected device type")
    parser.add_argument("--no-tls", action="store_true", help="Disable TLS")
    args = parser.parse_args()

    state.artifact_path = args.artifact
    state.device_type = args.device_type
    state.use_tls = not args.no_tls

    server = http.server.HTTPServer(("0.0.0.0", args.port), MockOTAHandler)

    if not args.no_tls:
        # Generate self-signed cert for testing
        cert_file = tempfile.NamedTemporaryFile(suffix=".pem", delete=False)
        key_file = tempfile.NamedTemporaryFile(suffix=".pem", delete=False)
        cert_file.close()
        key_file.close()

        os.system(
            f"openssl req -x509 -newkey rsa:2048 -keyout {key_file.name} "
            f"-out {cert_file.name} -days 1 -nodes "
            f'-subj "/CN=localhost" 2>/dev/null'
        )

        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(cert_file.name, key_file.name)
        server.socket = context.wrap_socket(server.socket, server_side=True)

        proto = "https"
    else:
        proto = "http"

    print(f"Mock OTA server listening on {proto}://0.0.0.0:{args.port}")
    if args.artifact:
        print(f"Serving artifact: {args.artifact}")
    else:
        print("No artifact configured (deployments/next will return 204)")

    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nShutting down")
        server.shutdown()


if __name__ == "__main__":
    main()
