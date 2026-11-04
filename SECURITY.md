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

- Access always requires an account of the configured media server: a Plex
  account that can reach the Plex server, or an enabled account of the Jellyfin
  server. The Plex owner, or the Jellyfin administrator who completes the first
  run, becomes the administrator.
- With Jellyfin, a password typed on the sign-in page is only passed on to the
  server; each Jellyfin session is signed out at once, and Agendarr reads the
  lists it needs with one API key.
- Plex tokens, the Jellyfin API key and Radarr/Sonarr/Seerr API keys are encrypted at rest with
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
  on every push to `dev`, every pull request and every release tag, and fails on a fixable
  `CRITICAL`/`HIGH`. No image is published unless it passes.
- Dependabot alerts and security updates are enabled.
