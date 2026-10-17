# Security Policy

ChatCLI runs commands, reads files, holds provider credentials and can be exposed to other people through its server, gateway and Kubernetes operator. We treat security reports as the highest-priority work in the project.

## Supported versions

Security fixes ship in a new release of the current line. Older releases do not get backports: upgrade to the latest release (`chatcli update`, `brew upgrade diillson/chatcli/chatcli`, or the matching container image and Helm chart).

| Version | Supported |
|---------|-----------|
| 1.221.3 (latest release) and later <!-- x-release-please-version --> | ✅ |
| Any earlier release | ❌ upgrade |

The version above is updated automatically by the release pipeline.

## Reporting a vulnerability

**Do not open a public issue, pull request or discussion for a security problem.**

Report it privately, in either of these ways:

1. **GitHub private vulnerability reporting** (preferred): [open a report](https://github.com/diillson/chatcli/security/advisories/new). It creates a private advisory where we can talk, share a fix and credit you.
2. **Email** the maintainer at the address listed on the [GitHub profile](https://github.com/diillson).

Please include:

- the affected component (CLI, `/coder` agent, server, gateway, operator, Helm chart, container image, plugin system, a provider integration) and version;
- the steps to reproduce, ideally a minimal proof of concept;
- the impact you see, and the configuration it needs (default settings or an opt-in);
- a suggested fix, if you have one.

## What happens next

| Step | Target |
|------|--------|
| Acknowledgment | within 48 hours |
| Initial assessment and severity (CVSS) | within 5 business days |
| Fix released: critical | within 7 days |
| Fix released: high | within 14 days |
| Fix released: medium | in the next release |
| Fix released: low | best effort |

For a confirmed vulnerability:

1. We open a private GitHub Security Advisory, develop the fix in its private fork and request a CVE.
2. We agree the disclosure date with you. We publish within 90 days of the report at the latest, and earlier once a fixed release is out.
3. The fixed release ships, then the advisory is published with the patched version, and the documentation describes any upgrade step.
4. With your consent, you are credited in the advisory and in this file.

## Scope

In scope:

- the `chatcli` binary in every mode: chat, `/coder` and agents, one-shot, the web UI, MCP and ACP servers;
- server mode (gRPC), the chat gateway and its channels, webhooks, the Kubernetes operator;
- the plugin system (signature verification, quarantine, provider plugins), skills, hooks and MCP integration;
- the official release artifacts: binaries, container images on `ghcr.io/diillson`, Helm charts;
- a bypass of a security boundary through model output: a prompt injection that makes the agent run, read or send something the [coder policy](https://chatcli.edilsonfreitas.com/coder/coder-security), workspace trust or an allowlist should have stopped.

Out of scope:

- vulnerabilities in third-party LLM providers, MCP servers or plugins we do not ship;
- model output that is wrong, harmful or manipulated but crosses no security boundary;
- attacks that need the victim to have already granted the access in question (for example, an `allow` rule the user wrote, or `CHATCLI_POLICY_AUTOMODE` turned on);
- denial of service by a local user against their own session;
- reports from automated scanners without a demonstrated impact.

## Safe harbor

We will not pursue legal action against research that:

- follows this policy;
- is done in good faith;
- tests only against your own installation and accounts;
- avoids privacy violations, data destruction and service disruption;
- gives us a reasonable time to fix the problem before you disclose it.

If you are unsure whether something is in scope, ask us first through a private report.

## Verifying what you install

- **Release binaries**:
  - Each release's `checksums.txt` is signed with Ed25519 (`checksums.txt.sig`).
  - `chatcli update` verifies the signature against the public key embedded in the binary (`update/release_keys.go`) before checking a download's checksum, and refuses a release without a valid signature.
  - The release pipeline refuses to publish unsigned.
- **Container images** (`ghcr.io/diillson/chatcli`, `ghcr.io/diillson/chatcli-operator`):
  - They are signed with Cosign (keyless, GitHub OIDC), shipped with an SBOM and scanned with Trivy before publishing.
  - To verify an image:

    ```bash
    cosign verify ghcr.io/diillson/chatcli:<version> \
      --certificate-oidc-issuer https://token.actions.githubusercontent.com \
      --certificate-identity-regexp '^https://github.com/diillson/chatcli/'
    ```

## Security controls

ChatCLI applies defense in depth across its components. The [security overview](https://chatcli.edilsonfreitas.com/security/overview) documents each control.

**Agent and tools**
- A security policy (allow, ask, deny) gates every tool call.
- Workspace trust: a project's `.env`, hooks and policy load only after `chatcli trust`.
- A command allowlist of more than 200 commands.
- Blocking of reads of sensitive paths.
- Redaction of secrets in command output.
- Unattended surfaces turn `ask` into a refusal rather than an approval.

**Server**
- Bearer tokens or JWT (HS256/RS256) with roles, compared in constant time.
- TLS 1.3 minimum.
- Per-client rate limiting, request validation and audit logging.
- SSRF protection on outbound fetches.

**Gateway**
- A sender allowlist that fails closed: a channel with an empty allowlist refuses to start.
- HMAC-SHA256 verification of signed webhooks.

**Operator**
- Fail-closed authorization, a resource allowlist, least-privilege RBAC and log scrubbing.

**Credentials and data**
- OAuth with PKCE.
- Encryption at rest on by default (AES-256-GCM) for sessions, transcripts, prompt history, memory, contexts, attachments and credentials, with the key in the OS keychain on macOS, Linux and Windows. `chatcli server` seals with `CHATCLI_ENCRYPTION_KEY`, and in Kubernetes the Helm chart and the operator keep that key in a Secret by default (`CHATCLI_ENCRYPTION_KEY_SECRET`). See [Encryption at Rest](https://chatcli.edilsonfreitas.com/security/encryption-at-rest).
- Outbound TLS 1.2 or later.

**Plugins**
- Ed25519 signature verification, publisher trust and quarantine.

## Automated scanning

- **Every pull request:**
  - `govulncheck`, `gosec`, CodeQL and Trivy filesystem scans;
  - dependency review;
  - secret scanning with gitleaks.
- **Weekly:** `govulncheck`, `gosec`, CodeQL and Trivy run again on `main`.
- **Every release:** image scanning with Trivy, an SBOM and Cosign signatures.
- **Dependencies:** Dependabot keeps them current.

## Published advisories

All advisories are listed at [github.com/diillson/chatcli/security/advisories](https://github.com/diillson/chatcli/security/advisories).

## Acknowledgments

We thank the researchers who reported vulnerabilities responsibly:

- **Theodoros Lampropoulos** ([@GimmyDatBeeR](https://github.com/GimmyDatBeeR)): MCP OAuth resource indicator binding, [GHSA-95vg-3r9c-2v5q](https://github.com/diillson/chatcli/security/advisories/GHSA-95vg-3r9c-2v5q).
