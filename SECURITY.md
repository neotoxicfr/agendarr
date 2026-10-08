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

## Security model

- Access always requires a Plex account that can reach the configured Plex
  server; the server owner becomes the administrator on first run.
- Plex tokens and Radarr/Sonarr/Seerr API keys are encrypted at rest with
  AES-256-GCM. The key is `secret.key` in the data directory: treat the whole
  data volume as a secret.
- API keys never reach the browser; posters are fetched through a server-side
  proxy.
- iCal feed URLs embed a per-user secret token. Anyone holding the URL can read
  that calendar, so tokens can be revoked and regenerated.

## Container hardening

The image is `distroless/static` (CA bundle and time zones, no shell, no
package manager) running as a non-root user. The example compose adds
`read_only: true`, `cap_drop: [ALL]` and `no-new-privileges:true`.

## Supply chain

- Every GitHub Action is pinned to a full commit SHA, and the repository
  *enforces* SHA pinning, so a hijacked tag cannot alter a workflow.
- CI runs `govulncheck` and a Trivy filesystem scan (vulnerabilities, secrets, misconfigurations)
  on every push to `dev` and every pull request, and fails on `CRITICAL`/`HIGH`.
- Dependabot alerts and security updates are enabled.
