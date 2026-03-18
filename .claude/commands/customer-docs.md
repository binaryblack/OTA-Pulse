# Generate Customer Documentation

You are a technical writer creating **customer-facing** documentation for **OTAPulse** — a production-grade Over-The-Air (OTA) update platform for embedded Linux devices.

## Golden Rules

- **Never expose internal implementation details** (state machine internals, source code logic, internal API endpoints, server-side architecture, or cryptographic internals beyond "we use RSA/ECDSA signing").
- **Never reveal** file paths, Go package names, internal data structures, or debugging internals.
- **Focus on**: what the product does, how to integrate it into a build system, how to configure it, how to use its features, and how to troubleshoot common issues from the device/operator perspective.
- Write for **embedded Linux engineers** integrating OTAPulse into their product — not Anthropic contributors.

---

## Step 1: Gather current state

Read the following files to understand what is currently documented and what the product currently supports:

- `docs/QUICKSTART.md`
- `docs/INTEGRATION.md`
- `docs/CONFIGURATION.md`
- `docs/API.md`
- `docs/SECURITY.md`
- `docs/STATE_SCRIPTS_GUIDE.md`
- `docs/TROUBLESHOOTING.md`
- `docs/KEY_ROTATION.md`
- `soc-ota-agent/OTA_AGENT_USAGE.md`
- `soc-ota-agent/QUICK_REFERENCE.md`
- `README.md`

Also list files under `docs/integration/` using Glob (`docs/integration/**/*.md`) and read any integration guides found there.

---

## Step 2: Check if customer docs already exist

Use Glob to check if `docs/CUSTOMER_GUIDE.md` exists.

- If it **exists**: read it fully — you will **update** it (preserve structure, update outdated sections, add new sections for features not yet covered, fix inaccuracies). Do not rewrite from scratch unless the file is clearly a stub.
- If it **does not exist**: create it from scratch using the structure below.

---

## Step 3: Write or update `docs/CUSTOMER_GUIDE.md`

The markdown document must follow this structure (add/remove subsections based on what is actually supported — do not document features that don't exist):

```
# OTAPulse — Customer Integration Guide

> Version: (derive from README or QUICKSTART if available, otherwise omit)
> Last updated: (today's date)

## Overview

What OTAPulse does and why it exists. 2–4 sentences. No internal details.

## Key Features

Bullet list of user-visible capabilities:
- Atomic A/B updates with automatic rollback
- Secure artifact signing (RSA/ECDSA)
- Bandwidth-efficient updates (delta, compression)
- Telemetry, crash capture, fleet health monitoring
- State scripts for custom update lifecycle hooks
- D-Bus API for programmatic control
- Multi-server failover
- Standalone (offline) update mode
- Hardware watchdog integration

## Supported Platforms

Table: Architecture | Build Systems | Notes
Cover: ARM 32-bit, ARM64, x86_64, RISC-V (experimental)
Build systems: Yocto/OpenEmbedded, Buildroot, Debian/Ubuntu, OpenWrt, Generic Linux

## Getting Started

### Prerequisites
What the customer needs before integrating (hardware, build environment, server).

### Quick Start (QEMU)
Summary of how to do the QEMU quickstart — point to docs/QUICKSTART.md for full details.

## Build System Integration

### Yocto / OpenEmbedded
How to add the meta-otapulse layer. Key variables to set. How to build a .mender artifact.

### Buildroot
How to add buildroot-otapulse as BR2_EXTERNAL. Key config options.

### Debian / Ubuntu
How to install the .deb package. Post-install configuration steps.

### OpenWrt
How to install and configure via UCI.

### Generic Linux
How to use the generic installer script.

## Configuration Reference

Key configuration options in `otapulse.conf`:
- Server URL and polling interval
- Device identity
- Signing/verification keys
- Telemetry settings
- Watchdog settings

Present as a table: Key | Type | Default | Description

Do NOT expose internal-only config keys.

## Artifact Management

How to create and sign update artifacts (.mender format):
- Required fields
- Signing an artifact
- Verifying an artifact before deployment

## Deploying Updates

How an update flows from the operator's perspective:
1. Build and sign artifact
2. Upload to update server
3. Device polls and downloads
4. Atomic installation and reboot
5. Automatic commit or rollback

Keep this at the operator/user level — no internal state machine details.

## State Scripts

What state scripts are, when they run, and how to write one.
Reference the lifecycle stages: Download, ArtifactInstall, ArtifactReboot, ArtifactCommit, ArtifactRollback.
Show a minimal example script.

## D-Bus API

How to use the D-Bus interface to:
- Query update status
- Trigger an update check
- Pause/resume updates via Update Control Maps

Only document the public-facing interface.

## CLI Reference

Table of all `soc-ota-agent` subcommands with brief description and example.

## Security

Best practices for customers:
- Always sign artifacts
- Key management recommendations
- Key rotation procedure (high level)
- Network security (TLS, certificate pinning if applicable)

Do NOT reveal cryptographic implementation details.

## Telemetry & Fleet Monitoring

What metrics and events OTAPulse reports:
- Device health (CPU, memory, disk, temperature)
- Crash reports and coredumps
- Reboot reasons
- Inventory / software versions

How to access this data (via the server dashboard or API — keep generic if server details are not in the docs).

## Troubleshooting

Common issues and resolutions from the device/operator perspective.
Do NOT expose internal log formats or debug internals.

## FAQ

5–8 common customer questions and concise answers.

## Changelog / What's New

(Only include if version history is available in the existing docs.)
```

---

## Step 4: Write or update `docs/customer-guide.html`

After writing the markdown, generate a self-contained HTML file at `docs/customer-guide.html`.

The HTML must:
- Be fully **self-contained** (no external CDN dependencies — inline all CSS and minimal JS)
- Render the full content of `docs/CUSTOMER_GUIDE.md` as structured HTML
- Have a **professional, clean design** suitable for sharing with enterprise customers
- Include a **sidebar navigation** (sticky, links to each `##` section)
- Include a **responsive layout** (works on desktop and mobile)
- Use a **neutral, professional color scheme** (e.g., dark navy sidebar, white content area, accent color for headings and links)
- Include a **header** with the OTAPulse product name and tagline
- Render code blocks with syntax-highlighted styling (use `<pre><code>` with CSS — no JS syntax highlighter needed)
- Include a **table of contents** at the top of the content area
- Include a **footer** with "OTAPulse Documentation" and the last-updated date

**Do not** include any internal implementation details in the HTML that were excluded from the markdown.

The HTML should be generated by converting the markdown content you just wrote into proper HTML elements — headings, paragraphs, tables, lists, code blocks — not by embedding raw markdown.

---

## Step 5: Report

After writing both files, output a brief summary:
- Whether each file was created or updated
- Key sections added or changed
- Any source docs that were missing or incomplete (flag for the user to fill in)
