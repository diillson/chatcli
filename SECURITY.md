# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 1.95.x  | Yes       |
| < 1.95  | No        |

## Reporting a Vulnerability

If you discover a security vulnerability in ChatCLI, please report it responsibly.

**Do NOT open a public GitHub issue for security vulnerabilities.**

### How to Report

1. **Email**: Send details to the maintainer via the email listed in the [GitHub profile](https://github.com/diillson).
2. **GitHub Security Advisories**: Use [GitHub's private vulnerability reporting](https://github.com/diillson/chatcli/security/advisories/new) to submit a confidential report.

### What to Include

- Description of the vulnerability
- Steps to reproduce
- Affected versions
- Potential impact
- Suggested fix (if any)

### Response Timeline

- **Acknowledgment**: Within 48 hours
- **Initial assessment**: Within 5 business days
- **Fix timeline**: Depends on severity
  - Critical: Patch within 7 days
  - High: Patch within 14 days
  - Medium: Next release cycle
  - Low: Best effort

### Security Measures

ChatCLI implements defense-in-depth security across all components:

- **Authentication**: JWT with RBAC roles, OAuth PKCE, constant-time token comparison
- **Encryption**: AES-256-GCM at rest, TLS 1.3 in transit
- **Server**: SSRF prevention, per-client rate limiting, field validation, audit logging
- **Agent**: Command allowlist (150+ approved commands), read path blocking, output sanitization
- **Plugins**: Ed25519 signature verification, quarantine system
- **Operator**: Fail-closed auth, resource allowlist, log scrubbing, RBAC least-privilege
- **CI/CD**: govulncheck, gosec, Dependabot, Cosign image signing

### Automated Scanning

Every pull request runs:
- `govulncheck ./...` — Go vulnerability database check
- `gosec ./...` — Static security analysis
- Dependency review for known CVEs

Full security documentation: [chatcli.edilsonfreitas.com](https://chatcli.edilsonfreitas.com)
