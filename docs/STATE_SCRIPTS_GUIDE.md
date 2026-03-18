# OTAPulse State Scripts Guide

State scripts allow you to run custom logic at specific points during the OTA update lifecycle. Use them to stop applications before updates, run database migrations, validate hardware state, and more.

## Lifecycle Overview

An OTA update goes through these states:

```
Idle → Download → ArtifactInstall → ArtifactReboot → ArtifactCommit → Idle
                                                   ↓ (failure)
                                            ArtifactRollback → ArtifactFailure → Idle
```

At each state transition, scripts can run at `_Enter` (before) and `_Leave` (after) points.

## Script Locations

| Location | Scope | Deployed via |
|----------|-------|--------------|
| `/etc/otapulse/scripts/` | Root filesystem scripts (persist across updates) | Build system |
| Embedded in `.mender` artifact | Artifact scripts (bundled with update) | mender-artifact |

## Naming Convention

```
<State>_<Transition>_<Priority>
```

- **State:** `Download`, `ArtifactInstall`, `ArtifactReboot`, `ArtifactCommit`, `ArtifactRollback`, `ArtifactFailure`
- **Transition:** `Enter` (before state), `Leave` (after state), `Error` (on failure)
- **Priority:** `00`-`99` (lower runs first)

Examples:
```
ArtifactInstall_Enter_00    # First script before install
ArtifactInstall_Enter_10    # Second script before install
ArtifactReboot_Leave_00     # After reboot (on new partition)
ArtifactCommit_Enter_00     # Before committing (validate new firmware)
ArtifactRollback_Enter_00   # Before rolling back
```

## Common Use Cases

### Stop Application Before Update

```bash
#!/bin/sh
# /etc/otapulse/scripts/ArtifactInstall_Enter_00
# Stop the main application before writing to disk
systemctl stop myapp
exit 0
```

### Validate After Reboot

```bash
#!/bin/sh
# /etc/otapulse/scripts/ArtifactCommit_Enter_00
# Run health checks before committing the update.
# Return non-zero to trigger automatic rollback.

# Check that critical services are running
if ! systemctl is-active --quiet myapp; then
    echo "ERROR: myapp failed to start on new firmware" >&2
    exit 1
fi

# Check hardware connectivity
if ! ping -c 1 -W 5 8.8.8.8 >/dev/null 2>&1; then
    echo "ERROR: Network connectivity lost after update" >&2
    exit 1
fi

echo "Health checks passed"
exit 0
```

### Database Migration

```bash
#!/bin/sh
# /etc/otapulse/scripts/ArtifactInstall_Leave_00
# Run database migration after new files are installed

if [ -f /opt/myapp/migrate.sh ]; then
    /opt/myapp/migrate.sh || {
        echo "ERROR: Database migration failed" >&2
        exit 1
    }
fi
exit 0
```

### Notify External Service

```bash
#!/bin/sh
# /etc/otapulse/scripts/ArtifactCommit_Leave_00
# Notify monitoring system that update succeeded

DEVICE_ID=$(cat /etc/otapulse/device_type 2>/dev/null | cut -d= -f2)
ARTIFACT=$(otapulse show-artifact 2>/dev/null | grep artifact_name | awk '{print $2}')

curl -s -X POST "https://monitoring.example.com/webhook/ota-complete" \
    -H "Content-Type: application/json" \
    -d "{\"device\": \"$DEVICE_ID\", \"artifact\": \"$ARTIFACT\"}" \
    || true  # Don't fail the update if notification fails

exit 0
```

### Cleanup on Rollback

```bash
#!/bin/sh
# /etc/otapulse/scripts/ArtifactRollback_Enter_00
# Clean up any partial state before rolling back

rm -f /tmp/update-in-progress
systemctl restart myapp || true

echo "Rollback cleanup complete"
exit 0
```

## Error Handling

- **Exit code 0:** Success — continue the update
- **Exit code 1:** Retry — script will run again after `StateScriptRetryIntervalSeconds`
- **Exit code 2+:** Fatal — abort the update and trigger rollback

### Retry Behavior

Configure retry behavior in `otapulse.conf`:

```json
{
    "StateScriptTimeoutSeconds": 3600,
    "StateScriptRetryTimeoutSeconds": 1800,
    "StateScriptRetryIntervalSeconds": 60
}
```

- `StateScriptTimeoutSeconds` — Maximum time a single script execution can take
- `StateScriptRetryTimeoutSeconds` — Maximum total time for retries
- `StateScriptRetryIntervalSeconds` — Delay between retry attempts

## Timeouts

Scripts that exceed `StateScriptTimeoutSeconds` are killed with SIGTERM. If the script doesn't exit within 10 seconds after SIGTERM, it's killed with SIGKILL.

For long-running operations (e.g., large database migrations), increase the timeout:

```json
{
    "StateScriptTimeoutSeconds": 7200
}
```

## Embedding Scripts in Artifacts

To include state scripts in a `.mender` artifact:

```bash
# Create artifact with state scripts
mender-artifact write rootfs-image \
    -t my-device \
    -n "release-2.0" \
    -f rootfs.ext4 \
    -s ArtifactInstall_Enter_00:/path/to/pre-install.sh \
    -s ArtifactCommit_Enter_00:/path/to/validate.sh \
    -o release-2.0.mender
```

Artifact scripts take precedence over rootfs scripts with the same name.

## Debugging

### View script execution logs

```bash
journalctl -u otapulse-client | grep -i "state script"
```

### Test scripts manually

```bash
# Make script executable
chmod +x /etc/otapulse/scripts/ArtifactCommit_Enter_00

# Run it
/etc/otapulse/scripts/ArtifactCommit_Enter_00
echo "Exit code: $?"
```

### List installed scripts

```bash
ls -la /etc/otapulse/scripts/
```

## Best Practices

1. **Keep scripts idempotent** — they may run multiple times on retry
2. **Always set exit codes** — unclear exit behavior causes unexpected rollbacks
3. **Log actions** — use `echo` or `logger` for debugging
4. **Don't block indefinitely** — respect the timeout
5. **Test rollback scenarios** — verify your `ArtifactRollback_Enter` scripts work
6. **Use `ArtifactCommit_Enter` for validation** — this is your last chance to trigger rollback
7. **Don't modify boot state** — let the agent handle partition switching
