# Technical and Organizational Measures (TOM)

**GDPR Art. 32 Compliance Documentation**

---

## 1. Confidentiality (Art. 32(1)(b) GDPR)

### 1.1 Physical Access Control
| Measure | Implementation | Status |
|---------|----------------|--------|
| Data Center | IONOS VPS Germany (Frankfurt/Berlin) | ✅ |
| Physical Access | Secured by IONOS (biometrics, 24/7 monitoring) | ✅ |
| ISO 27001 | Information Security Management System (ISMS) certified | ✅ |
| BSI IT-Grundschutz | ISO 27001 on basis of IT-Grundschutz (BSI-IGZ-0730-2025) | ✅ |
| BSI C5 Testat | Cloud Computing Compliance Controls Catalog (Type 1, Nov 2023) | ✅ |
| ISAE 3000 | International Standard on Assurance Engagements | ✅ |
| ISO 50001 | Energy Management System certified | ✅ |
| GDPR/DSGVO | Full EU data protection compliance | ✅ |
| Gaia-X | Member for sovereign data handling (EU independence from US CLOUD Act) | ✅ |

### 1.2 System Access Control
| Measure | Implementation | Status |
|---------|----------------|--------|
| SSH Access | SSH keys only, password login disabled | ✅ |
| API Authentication | PocketBase admin token required | ✅ |
| Dashboard Access | Read-only without authentication (aggregated data only) | ✅ |
| Admin Access | Via Coolify with 2FA | ✅ |

### 1.3 Data Access Control
| Measure | Implementation | Status |
|---------|----------------|--------|
| Authorization Concept | Least privilege: service only has write access to telemetry collection | ✅ |
| API Endpoints | Telemetry endpoint: POST only, Dashboard API: GET only | ✅ |
| Non-Root Processes | Container runs as non-root user | ✅ |

### 1.4 Separation Control
| Measure | Implementation | Status |
|---------|----------------|--------|
| Data Separation | Separate collections for ProxmoxVE/DevScripts | ✅ |
| Network Separation | Docker network isolation | ✅ |
| Environment Separation | Production separated from development | ✅ |

---

## 2. Integrity (Art. 32(1)(b) GDPR)

### 2.1 Transfer Control
| Measure | Implementation | Status |
|---------|----------------|--------|
| Transport Encryption | TLS 1.3 (HTTPS) | ✅ |
| Internal Communication | Docker internal network | ✅ |
| Data Location | Server located in EU (Germany) | ✅ |

### 2.2 Input Control
| Measure | Implementation | Status |
|---------|----------------|--------|
| Request Validation | Strict JSON schema validation | ✅ |
| Max Body Size | 1024 bytes (prevents oversized payloads) | ✅ |
| Error Messages | Max 120 characters (prevents log injection) | ✅ |
| Audit Logging | Failed requests are logged (without IP) | ✅ |

---

## 3. Availability and Resilience (Art. 32(1)(b)(c) GDPR)

### 3.1 Availability Control
| Measure | Implementation | Status |
|---------|----------------|--------|
| Health Checks | `/health` endpoint with Docker HEALTHCHECK | ✅ |
| Auto-Restart | Coolify restarts container on crash | ✅ |
| Rate Limiting | 60 requests/minute per IP (DDoS protection) | ✅ |
| Timeout Handling | 120s timeout for dashboard queries | ✅ |

### 3.2 Recoverability
| Measure | Implementation | Status |
|---------|----------------|--------|
| Data Backup | PocketBase SQLite backups via Coolify | ✅ |
| Backup Interval | Daily | ✅ |
| Disaster Recovery | Data can be restored from backup | ✅ |

---

## 4. Regular Testing and Evaluation (Art. 32(1)(d) GDPR)

### 4.1 Privacy Management
| Measure | Implementation | Status |
|---------|----------------|--------|
| Processing Records | [docs/ROPA.md](ROPA.md) | ✅ |
| Security Policy | [SECURITY.md](../SECURITY.md) | ✅ |
| Deletion Concept | Automatic deletion after 365 days | ✅ |

### 4.2 Technical Reviews
| Measure | Interval | Status |
|---------|----------|--------|
| Dependency Updates | On every build (Go Modules) | ✅ |
| Container Updates | Alpine base regularly updated | ✅ |
| Code Review | All changes via Pull Request | ✅ |

---

## 5. Privacy by Design / Privacy by Default (Art. 25 GDPR)

### 5.1 Privacy by Design
| Principle | Implementation | Status |
|-----------|----------------|--------|
| Data Minimization | Only technically necessary data is collected | ✅ |
| Anonymity | No personal data, anonymous session IDs | ✅ |
| No IP Storage | `ENABLE_REQUEST_LOGGING=false` | ✅ |

### 5.2 Privacy by Default
| Setting | Default | Status |
|---------|---------|--------|
| Telemetry | Opt-in (user must actively consent) | ✅ |
| Request Logging | Disabled | ✅ |
| Data Sharing | None | ✅ |

---

## 6. Sub-Processors

### 6.1 Service Providers
| Provider | Function | Location | Contract |
|----------|----------|----------|----------|
| Hetzner Cloud | Infrastructure | EU (Germany) | DPA in place |
| Coolify | Container orchestration | Self-hosted | - |
| GitHub | Source code hosting | USA | EU-US DPF certified |

### 6.2 No Third-Party Sharing
Telemetry data is **not** shared with external analytics services, advertising partners, or any other third parties.

---

## 7. Technical Security Measures in Code

```go
// service.go - Security Headers
w.Header().Set("X-Content-Type-Options", "nosniff")
w.Header().Set("X-Frame-Options", "DENY")
w.Header().Set("Referrer-Policy", "no-referrer")

// Rate Limiting
RateLimitRPM: 60       // Max 60 requests per minute
RateBurst:    20       // Burst limit
MaxBodyBytes: 1024     // Max 1KB request body

// No IP Storage
EnableReqLogging: false
```

---

## 8. Data Breach Response

| Step | Responsible | Timeframe |
|------|-------------|-----------|
| Detection | Automatic (monitoring) or via GitHub Issue | - |
| Initial Assessment | Maintainer | 24 hours |
| Supervisory Authority Notification | N/A (no personal data) | - |
| Data Subject Notification | N/A (no personal data) | - |
| Documentation | GitHub Security Advisory | 7 days |

---

## 9. Revision History

| Date | Version | Change | Author |
|------|---------|--------|--------|
| 2025-02-12 | 1.0 | Initial creation | Community Scripts Team |

---

*This documentation is updated when significant changes are made to the service.*
