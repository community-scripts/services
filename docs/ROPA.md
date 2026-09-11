# Records of Processing Activities (ROPA)

**GDPR Art. 30 Compliance Documentation**

---

## 1. Controller Information

| Field | Value |
|-------|-------|
| **Organization** | Community Scripts (Open Source Project) |
| **Project Name** | Telemetry Service |
| **Repository** | https://github.com/community-scripts/services |
| **Contact** | Via GitHub Issues |

---

## 2. Purpose of Processing

### 2.1 Description
The Telemetry Service collects **anonymous technical usage statistics** from the Proxmox VE Helper-Scripts. This data is used exclusively for:

- **Quality Improvement**: Identification of scripts with high failure rates
- **Prioritization**: Recognition of the most-used applications
- **Trend Analysis**: Understanding of used operating systems and resource configurations

### 2.2 Legal Basis
**Art. 6(1)(f) GDPR** (Legitimate Interest)

The legitimate interest lies in improving open-source software for the community. The processing is minimally invasive because:
- No personal data is collected
- No IP addresses are stored
- Data transmission is opt-in (users must actively consent)

---

## 3. Categories of Data Subjects

| Category | Description |
|----------|-------------|
| Helper-Script Users | Administrators who run Proxmox VE Helper-Scripts and have consented to telemetry |

---

## 4. Categories of Personal Data

### ⚠️ NO Personal Data Is Collected

The collected data is **purely technical** and allows **no identification of natural persons**:

| Data Field | Type | Description | Personal Data |
|------------|------|-------------|---------------|
| `random_id` | UUID | Randomly generated session ID (new per installation) | ❌ No |
| `type` | String | LXC, VM, Tool, Addon | ❌ No |
| `nsapp` | String | Name of installed application (e.g., "jellyfin") | ❌ No |
| `status` | String | Success / Failed / Installing | ❌ No |
| `disk_size` | Integer | Disk size in GB | ❌ No |
| `core_count` | Integer | CPU cores | ❌ No |
| `ram_size` | Integer | RAM in MB | ❌ No |
| `os_type` | String | Operating system (debian, ubuntu, alpine) | ❌ No |
| `os_version` | String | OS version (12, 24.04) | ❌ No |
| `pve_version` | String | Proxmox VE version | ❌ No |
| `method` | String | Installation method | ❌ No |
| `error` | String | Error description (max 120 characters) | ❌ No |
| `exit_code` | Integer | Exit code (0-255) | ❌ No |
| `gpu_vendor` | String | GPU manufacturer | ❌ No |
| `cpu_vendor` | String | CPU manufacturer | ❌ No |
| `install_duration` | Integer | Installation duration in seconds | ❌ No |

### What Is NOT Collected:
- ❌ IP addresses (request logging disabled)
- ❌ Hostnames or domain names
- ❌ MAC addresses or serial numbers
- ❌ Usernames or email addresses
- ❌ Network configuration
- ❌ Location data

---

## 5. Data Recipients

| Recipient | Purpose | Legal Basis |
|-----------|---------|-------------|
| PocketBase (self-hosted) | Storage of telemetry data | Processing on same server |
| GitHub (for public dashboard) | Aggregated statistics | Art. 6(1)(f) (legitimate interest) |

**No sharing with third parties.** The data is used exclusively for improving the Helper-Scripts.

---

## 6. International Data Transfers

| Location | Transfer | Safeguards |
|----------|----------|------------|
| Non-EU Countries | ❌ No | - |

Data processing occurs **exclusively on EU servers** (Hetzner Cloud, Germany).

---

## 7. Retention Periods

| Data Category | Retention Period | Justification |
|---------------|------------------|---------------|
| Telemetry Data | **365 days** | Sufficient for yearly trend analysis |
| Aggregated Statistics | Indefinite | No personal data |
| Logs (if enabled) | 7 days | Technical troubleshooting |

Automatic deletion is implemented by the `cleanup` job in the service.

---

## 8. Technical and Organizational Measures (TOM)

See separate documentation: [TOMS.md](TOMS.md)

**Summary:**
- ✅ Encryption in transit (TLS 1.3)
- ✅ Access control (API token-based)
- ✅ Rate limiting (DDoS protection)
- ✅ No IP storage
- ✅ Privacy by Design (anonymous session IDs)

---

## 9. Data Protection Impact Assessment (DPIA)

A DPIA according to Art. 35 GDPR is **not required** because:

1. No personal data is processed
2. No profiling or automated decision-making occurs
3. No special categories of personal data (Art. 9 GDPR) are affected
4. The processing does not present a high risk to the rights and freedoms of natural persons

---

## 10. Revision History

| Date | Version | Change | Author |
|------|---------|--------|--------|
| 2025-02-12 | 1.0 | Initial creation | Community Scripts Team |

---

*For questions or change requests, please contact us via GitHub Issues.*
