#!/usr/bin/env python3
"""BUG-367: otapulse-auto-provision's tunnel self-heal must heal ONLY the
tunnel credential, with the device's own API key, without an admin login.

Runs the REAL script (files/otapulse-auto-provision) on an already-provisioned
device (the /data/otapulse/.auto-provisioned marker exists) whose tunnel
credential is stale, i.e. exactly the "tunnel self-heal only" branch. Every
/data, /etc and /sys path is rewritten into a temp dir; curl, systemctl,
logger and sleep are stubs; PATH holds ONLY the stubs plus a fixed list of
harmless tools, so nothing can touch the host or the network.

The curl stub answers from a per-test route table and records every call
(URL + headers), so the tests can prove which endpoints the script used.

Run: python3 -m pytest test_auto_provision_tunnel_selfheal.py
"""
from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
from pathlib import Path

import pytest

HERE = Path(__file__).resolve().parent
SCRIPT = HERE / "files" / "otapulse-auto-provision"

_SAFE_TOOLS = (
    "cat chmod chown cut date dirname grep head md5sum mkdir python3 rm sed tail tr"
).split()

SERVER = "http://otapulse.test:8000"
DEVICE_ID = "dev-5b5a24ae"
DEVICE_UUID = "6a3e3d4c-1f0e-4f43-9d55-3f2a8a0a7c11"
DEVICE_KEY = "sk_live_device_bound_key_0123456789"
OLD_SOTC = "sotc_" + "0" * 64
NEW_SOTC = "sotc_" + "a" * 64

# curl stub: parse the few flags the script uses, log the call, answer from
# $FAKE_ROOT/routes.json ({"<path>": {"code": 200, "body": {...}}}). Like real
# curl: -f with an HTTP error prints nothing and exits 22; -w appends the
# write-out; an unknown route is a connection failure (exit 7, code 000).
_CURL = r'''#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
url, headers, wfmt, fail, i = None, [], None, False, 0
while i < len(args):
    a = args[i]
    if a in ("-H", "-d", "-X", "-w", "--max-time", "--data-urlencode"):
        v = args[i + 1]
        if a == "-H":
            headers.append(v)
        if a == "-w":
            wfmt = v
        i += 2
        continue
    if a.startswith("-") and not a.startswith("--") and "f" in a:
        fail = True
    if a.startswith("http"):
        url = a
    i += 1
root = os.environ["FAKE_ROOT"]
with open(os.path.join(root, "curl.log"), "a") as f:
    f.write(json.dumps({"url": url, "headers": headers}) + "\n")
routes = json.load(open(os.path.join(root, "routes.json")))
path = url.split("://", 1)[1].split("/", 1)[1] if url else ""
path = "/" + path.split("?", 1)[0]
route = routes.get(path)
if route is None:
    if wfmt:
        sys.stdout.write(wfmt.replace("%{http_code}", "000").replace("\\n", "\n"))
    sys.exit(7)
code, body = route["code"], json.dumps(route.get("body", {}))
if fail and code >= 400:
    sys.exit(22)
sys.stdout.write(body)
if wfmt:
    sys.stdout.write(wfmt.replace("%{http_code}", str(code)).replace("\\n", "\n"))
'''

_STUBS = {
    "curl": _CURL,
    "systemctl": '#!/bin/sh\necho "$*" >> "$FAKE_ROOT/systemctl.log"\n'
                 'case "$*" in *is-active*) exit 0 ;; *is-enabled*) exit 0 ;; esac\nexit 0\n',
    "logger": "#!/bin/sh\nexit 0\n",
    "sleep": "#!/bin/sh\nexit 0\n",
}


def _which(tool: str) -> str:
    p = shutil.which(tool)
    assert p, f"{tool} not found on host"
    return p


class Device:
    """An already-provisioned device whose tunnel credential is stale."""

    def __init__(self, root: Path, *, main_key=DEVICE_KEY, data_key=None, admin=False,
                 provisioning_token=False):
        self.root = root
        for d in ("bin", "data/otapulse", "data/frp", "etc/otapulse", "sys/class/net"):
            (root / d).mkdir(parents=True, exist_ok=True)

        main = {"ServerURL": SERVER, "SoCDeviceID": DEVICE_ID, "UseSoCMonitoring": True}
        if main_key:
            main["SoCAPIKey"] = main_key
        (root / "etc/otapulse/otapulse.conf").write_text(json.dumps(main))
        data = {"SoCDeviceID": DEVICE_ID}
        if data_key:
            data["SoCAPIKey"] = data_key
        (root / "data/otapulse/otapulse.conf").write_text(json.dumps(data))
        (root / "etc/otapulse/tunnel.conf").write_text(json.dumps(
            {"serverAddr": "192.168.0.123", "serverPort": 7000, "proxyName": DEVICE_ID}))
        # Main provisioning done long ago.
        (root / "data/otapulse/.auto-provisioned").write_text(
            f"provisioned=2026-01-01T00:00:00\ndevice_id={DEVICE_ID}\n")
        # Credential minted for an OLD identity -> NEED_TUNNEL_REMINT=1.
        (root / "data/otapulse/.tunnel-provisioned").write_text(
            "device_id=old-identity\ndevice_uuid=old-uuid\nprovisioned_at=x\n")
        (root / "data/frp/credential").write_text(OLD_SOTC)
        (root / "etc/machine-id").write_text("0123456789abcdef0123456789abcdef\n")
        env_lines = []
        if admin:
            env_lines += ["OTAPULSE_ADMIN_EMAIL=admin@example.test",
                          "OTAPULSE_ADMIN_PASSWORD=correct-password"]
        if provisioning_token:
            env_lines.append("OTAPULSE_PROVISIONING_TOKEN=spt_fleet_token_000000")
        if env_lines:
            (root / "etc/otapulse/provision.env").write_text("\n".join(env_lines) + "\n")
        self.routes = {"/health": {"code": 200, "body": {"status": "ok"}}}

        bindir = root / "bin"
        for name, body in _STUBS.items():
            p = bindir / name
            p.write_text(body)
            p.chmod(0o755)
        for tool in _SAFE_TOOLS:
            (bindir / tool).symlink_to(_which(tool))

        text = SCRIPT.read_text()
        system_path = re.compile(r"(?<![\w./}$])/(data|etc|sys)/")
        text = system_path.sub(lambda m: f"{root}/{m.group(1)}/", text)
        assert not system_path.search(text), "unrewritten system path"
        self.script = root / "otapulse-auto-provision"
        self.script.write_text(text)
        self.script.chmod(0o755)

    def run(self) -> subprocess.CompletedProcess:
        (self.root / "routes.json").write_text(json.dumps(self.routes))
        return subprocess.run(
            ["/bin/bash", str(self.script)],
            env={"PATH": str(self.root / "bin"), "FAKE_ROOT": str(self.root), "HOME": str(self.root)},
            capture_output=True, text=True, timeout=60,
        )

    def calls(self) -> list:
        log = self.root / "curl.log"
        if not log.exists():
            return []
        return [json.loads(line) for line in log.read_text().splitlines()]

    def paths_called(self) -> list:
        return ["/" + c["url"].split("://", 1)[1].split("/", 1)[1].split("?", 1)[0] for c in self.calls()]

    @property
    def credential(self) -> str:
        return (self.root / "data/frp/credential").read_text()

    @property
    def marker(self) -> str:
        return (self.root / "data/otapulse/.tunnel-provisioned").read_text()


ROTATE = "/api/v1/tunnel-credential/rotate"
ROTATE_OK = {"code": 200, "body": {
    "tunnel_token": NEW_SOTC, "tunnel_token_prefix": NEW_SOTC[:12],
    "rotated_at": "2026-10-03T00:00:00", "rotation_count": 3, "device_id": DEVICE_UUID,
}}
MFA_CHALLENGE = {"code": 200, "body": {"mfa_challenge_required": True, "mfa_pending_token": "p"}}


@pytest.fixture
def device(tmp_path):
    return lambda **kw: Device(tmp_path, **kw)


def test_self_heal_uses_the_device_key_and_never_logs_in(device):
    dev = device(admin=True, provisioning_token=True)
    dev.routes[ROTATE] = ROTATE_OK
    dev.routes["/api/auth/login"] = MFA_CHALLENGE  # would fail; must not be reached

    res = dev.run()

    assert res.returncode == 0, res.stdout + res.stderr
    assert dev.paths_called() == ["/health", ROTATE], dev.paths_called()
    call = dev.calls()[1]
    assert f"X-API-Key: {DEVICE_KEY}" in call["headers"]
    assert f"X-Device-ID: {DEVICE_ID}" in call["headers"]
    assert dev.credential == NEW_SOTC
    assert f"device_id={DEVICE_ID}\n" in dev.marker
    assert f"device_uuid={DEVICE_UUID}\n" in dev.marker
    # The API key / main provisioning state is untouched.
    assert json.loads((dev.root / "etc/otapulse/otapulse.conf").read_text())["SoCAPIKey"] == DEVICE_KEY
    assert "SoCAPIKey" not in json.loads((dev.root / "data/otapulse/otapulse.conf").read_text())
    assert (dev.root / "data/otapulse/.auto-provisioned").exists()
    assert "restart soc-ota-tunneld" in (dev.root / "systemctl.log").read_text()
    assert NEW_SOTC not in res.stdout


def test_self_heal_never_reprovisions_even_with_a_provisioning_token(device):
    """Method 0 (POST /api/v1/provision) rotates every API key of the device;
    the self-heal branch must never take it, whatever the server answers."""
    dev = device(admin=True, provisioning_token=True)
    dev.routes[ROTATE] = {"code": 404, "body": {"detail": "Not Found"}}
    dev.routes["/api/auth/login"] = MFA_CHALLENGE
    dev.routes["/api/v1/provision"] = {"code": 200, "body": {"api_key": "sk_new", "tunnel_token": NEW_SOTC}}

    res = dev.run()

    assert res.returncode == 0, res.stdout + res.stderr
    assert "/api/v1/provision" not in dev.paths_called()


def test_older_backend_falls_back_to_admin_path_unchanged(device):
    dev = device(admin=True)
    dev.routes[ROTATE] = {"code": 404, "body": {"detail": "Not Found"}}
    dev.routes["/api/auth/login"] = MFA_CHALLENGE

    res = dev.run()

    assert res.returncode == 0, res.stdout + res.stderr
    assert dev.paths_called() == ["/health", ROTATE, "/api/auth/login"]
    assert "falling back to admin auth" in res.stdout
    assert "JWT login failed" in res.stdout
    # Nothing was minted: the old credential and marker stay as they were.
    assert dev.credential == OLD_SOTC
    assert "device_uuid=old-uuid" in dev.marker


def test_refused_key_falls_back_and_does_not_touch_the_credential(device):
    dev = device(admin=False)
    dev.routes[ROTATE] = {"code": 403, "body": {"detail": "A device-bound API key is required"}}

    res = dev.run()

    assert res.returncode == 0, res.stdout + res.stderr
    assert dev.paths_called() == ["/health", ROTATE]
    assert "HTTP 403" in res.stdout and "device-bound" not in res.stdout
    assert dev.credential == OLD_SOTC


def test_malformed_token_is_not_written(device):
    dev = device()
    dev.routes[ROTATE] = {"code": 200, "body": {"tunnel_token": "not-a-sotc-token", "device_id": DEVICE_UUID}}

    res = dev.run()

    assert res.returncode == 0, res.stdout + res.stderr
    assert dev.credential == OLD_SOTC
    assert "wrong prefix" in res.stdout


def test_no_api_key_on_device_skips_straight_to_admin_path(device):
    dev = device(main_key=None, admin=True)
    dev.routes["/api/auth/login"] = MFA_CHALLENGE

    res = dev.run()

    assert res.returncode == 0, res.stdout + res.stderr
    assert ROTATE not in dev.paths_called()
    assert "/api/auth/login" in dev.paths_called()


def test_main_config_key_wins_over_data_fallback(device):
    dev = device(main_key=DEVICE_KEY, data_key="sk_stale_data_copy")
    dev.routes[ROTATE] = ROTATE_OK
    dev.run()
    assert f"X-API-Key: {DEVICE_KEY}" in dev.calls()[1]["headers"]


def test_data_fallback_key_used_when_main_has_none(device):
    dev = device(main_key=None, data_key="sk_data_copy_key")
    dev.routes[ROTATE] = ROTATE_OK
    res = dev.run()
    assert "X-API-Key: sk_data_copy_key" in dev.calls()[1]["headers"]
    assert dev.credential == NEW_SOTC, res.stdout
