# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| main    | :white_check_mark: |

We only support the latest version on the `main` branch. Please ensure you are running the most recent version before reporting a vulnerability.

## Reporting a Vulnerability

We take security vulnerabilities in the telemetry-service seriously. If you discover a security issue, please report it responsibly.

### How to Report

**DO NOT** open a public GitHub issue for security vulnerabilities.

Instead, please report security issues via one of these methods:

1. **GitHub Security Advisories** (preferred):
   - Go to [Security Advisories](https://github.com/community-scripts/services/security/advisories)
   - Click "Report a vulnerability"
   - Fill out the form with details

2. **Email**: Contact the maintainers through the [ProxmoxVE repository](https://github.com/community-scripts/ProxmoxVE)

### What to Include

Please provide:

- A clear description of the vulnerability
- Steps to reproduce the issue
- Potential impact assessment
- Any suggested fixes (if available)
- Your contact information for follow-up

### Response Timeline

| Action | Timeline |
|--------|----------|
| Initial acknowledgment | 48 hours |
| Status update | 7 days |
| Fix release (critical) | 14 days |
| Fix release (non-critical) | 30 days |

## Security Measures

This service implements the following security controls:

### Data Protection
- **No PII Collection**: No personally identifiable information is collected
- **No IP Logging**: Request logging is disabled by default (`ENABLE_REQUEST_LOGGING=false`)
- **Anonymous Sessions**: Session IDs are randomly generated UUIDs with no user correlation
- **Data Minimization**: Only technical metrics necessary for analytics are collected

### Transport Security
- **TLS 1.3**: All communications encrypted in transit
- **HTTPS Only**: No plaintext HTTP endpoints in production

### Access Control
- **API Token Authentication**: PocketBase API requires authentication tokens
- **Rate Limiting**: Configurable per-IP rate limiting prevents abuse
- **No Public Write Access**: Only the telemetry endpoint accepts writes

### Infrastructure
- **EU Data Residency**: All data stored on EU servers (Hetzner, Germany)
- **Container Isolation**: Runs in isolated Docker containers
- **Minimal Attack Surface**: Alpine-based image with no shell access

## Known Limitations

- **No End-to-End Encryption**: Data is encrypted in transit but stored unencrypted at rest (mitigated by database access controls)
- **No User Authentication**: The telemetry endpoint accepts anonymous submissions (by design)

## Security Headers

The service sets the following security headers on all responses:

```
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Referrer-Policy: no-referrer
```

## Audit Log

Security-relevant actions are logged:
- Authentication attempts to PocketBase
- Rate limit violations
- Failed data validations

Logs do not contain IP addresses or user-identifiable information.

## Compliance

This service is designed to be compliant with:

- **GDPR/DSGVO**: No personal data processing, Privacy by Design
- **CCPA**: No sale of personal information (no personal information collected)

See [docs/VVT.md](docs/VVT.md) for the full Record of Processing Activities.

---

*Last updated: 2026-02-12*
