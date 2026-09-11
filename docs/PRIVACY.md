# Privacy & Telemetry Documentation

**Community-Scripts Telemetry Service**

---

## Overview

The [Community-Scripts](https://github.com/community-scripts/ProxmoxVE) project includes an **opt-in** telemetry system that collects anonymous technical data when users install applications via Proxmox VE Helper-Scripts. This document explains what is collected, how it is processed, and how users can control it.

**Dashboard:** [https://telemetry.community-scripts.org](https://telemetry.community-scripts.org)

---

## Consent & Opt-In

Telemetry is **strictly opt-in**. On first use, a dialog asks users whether they want to share anonymous data:

- **No option is pre-selected** — users must actively choose
- Pressing Exit/Cancel defaults to **opt-out**
- The choice is persisted in `/usr/local/community-scripts/diagnostics`
- Users can change their preference at any time via the **Settings menu** during installation or by editing the config file directly

### Changing Your Preference

**Via Settings menu:** During any script installation, navigate to the Settings menu and select "Telemetry".

**Via command line (on PVE host):**
```bash
# Opt out
sed -i 's/^DIAGNOSTICS=.*/DIAGNOSTICS=no/' /usr/local/community-scripts/diagnostics

# Opt in
sed -i 's/^DIAGNOSTICS=.*/DIAGNOSTICS=yes/' /usr/local/community-scripts/diagnostics
```

**Inside a container (for addon scripts):**
```bash
# The same file exists inside containers created after opting in
sed -i 's/^DIAGNOSTICS=.*/DIAGNOSTICS=no/' /usr/local/community-scripts/diagnostics
```

> **Note:** Changing the setting on the host affects all **new** containers. Existing containers retain the preference set at creation time.

---

## What We Collect

All data is **purely technical** and **anonymous**. No field can identify a natural person.

### Container & VM Installations (type: `lxc`, `vm`)

| Field | Example | Purpose |
|-------|---------|---------|
| `nsapp` | `docker`, `homeassistant` | Which application was installed |
| `status` | `success`, `failed` | Whether the installation succeeded |
| `exit_code` | `0`, `1`, `130` | Exit code for error categorization |
| `error` | `dpkg: error ...` | Truncated error message (failed installs only) |
| `ct_type` | `1` (unprivileged) | Container privilege type |
| `disk_size` | `8` | Disk size in GB |
| `core_count` | `2` | Number of CPU cores |
| `ram_size` | `2048` | RAM in MiB |
| `os_type` | `debian` | Operating system |
| `os_version` | `12` | OS version |
| `pve_version` | `8.3` | Proxmox VE version |
| `method` | `default`, `advanced` | Installation method used |
| `install_duration` | `45` | Duration in seconds |
| `random_id` | `a1b2c3d4-...` | Random UUID (not linked to user/system) |
| `execution_id` | `a1b2c3d4-...` | Unique execution identifier |

### PVE Tools (type: `pve`)

| Field | Example | Purpose |
|-------|---------|---------|
| `nsapp` | `post-pve-install`, `microcode` | Which tool was executed |
| `status` | `success`, `failed` | Execution result |
| `exit_code` | `0` | Exit code |
| `pve_version` | `8.3` | Proxmox VE version |
| `install_duration` | `12` | Duration in seconds |

### Addon Scripts (type: `addon`)

| Field | Example | Purpose |
|-------|---------|---------|
| `nsapp` | `filebrowser`, `netdata` | Which addon was installed |
| `status` | `success`, `failed` | Installation result |
| `exit_code` | `0` | Exit code |
| `os_type` | `debian` | Container OS |
| `os_version` | `12` | Container OS version |
| `install_duration` | `30` | Duration in seconds |

---

## What We Do NOT Collect

- **No IP addresses** — not logged, not stored, not forwarded
- **No hostnames** or domain names
- **No MAC addresses** or hardware serial numbers
- **No user credentials**, passwords, or API tokens
- **No network configuration** or internal IP addresses
- **No file paths**, directory listings, or file contents
- **No personal data** of any kind (GDPR Art. 4 Nr. 1)

---

## How Data Is Used

The collected data serves the following purposes **exclusively**:

1. **Script Quality** — Identify scripts with high failure rates and fix them
2. **Popularity Ranking** — Understand which scripts are most used to prioritize maintenance
3. **Resource Trends** — Analyze typical CPU, RAM, and disk allocations
4. **OS Compatibility** — Track which OS versions are in use
5. **Error Analysis** — Categorize common failure patterns for faster debugging

All aggregated statistics are publicly visible on the [dashboard](https://telemetry.community-scripts.org).

---

## Data Processing & Storage

| Aspect | Details |
|--------|---------|
| **Processor** | Self-hosted on IONOS VPS in Germany (Frankfurt/Berlin) |
| **Database** | PocketBase (SQLite-based, self-hosted) |
| **Retention** | Data is stored indefinitely for trend analysis |
| **Encryption** | TLS 1.3 in transit, encrypted storage at rest |
| **Access** | Write: telemetry service only. Read: aggregated dashboard (public) |
| **Third parties** | None — no data is shared with or sold to third parties |
| **Backups** | Automated, encrypted, same data center |

---

## Legal Basis (GDPR)

**Art. 6(1)(f) GDPR — Legitimate Interest**

The legitimate interest lies in improving open-source software for the community. Since:
- No personal data is collected (Art. 4 Nr. 1 GDPR)
- Data collection is opt-in with active consent
- Users can withdraw at any time
- All data is anonymous and cannot identify individuals

the processing is GDPR-compliant. For detailed documentation, see:
- [Records of Processing Activities (ROPA)](ROPA.md) — Art. 30 GDPR
- [Technical & Organizational Measures (TOMS)](TOMS.md) — Art. 32 GDPR

---

## Contact & Issues

- **Questions:** Open an issue at [services](https://github.com/community-scripts/services/issues)
- **Privacy concerns:** Open an issue with the `privacy` label
- **Source code:** Fully open source — review the [service implementation](../service.go)
- **Discussion:** [ProxmoxVE Discussions](https://github.com/community-scripts/ProxmoxVE/discussions)
