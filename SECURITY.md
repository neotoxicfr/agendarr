# Security Policy

## Supported versions

Only the latest release is supported. Fixes land on `dev` and ship in the next
tag; there are no backports.

| Version | Supported |
| ------- | --------- |
| latest release | ✅ |
| `dev` | ⚠️ pre-release, no guarantees |
| anything older | ❌ |

## Reporting a vulnerability

Open a [private security advisory](https://github.com/neotoxicfr/agendarr/security/advisories/new)
rather than a public issue. Please include the version, the configuration
involved, and a way to reproduce.

## Supply chain

- Every GitHub Action is pinned to a full commit SHA, and the repository
  *enforces* SHA pinning, so a hijacked tag cannot alter a workflow.
- CI runs a Trivy filesystem scan (vulnerabilities, secrets, misconfigurations)
  on every push to `dev` and every pull request, and fails on `CRITICAL`/`HIGH`.
- Dependabot alerts and security updates are enabled.
