# Contributing

## Prerequisites

- **Go 1.27+** (the version in `go.mod` is authoritative; CI installs it from there)
- **golangci-lint v2.14+**, built with a Go version ≥ the one in `go.mod`
- **A C toolchain** (gcc/clang), only for `go test -race`
- **lefthook** *(optional)*: installs the git hooks in `lefthook.yml`
- **actionlint** *(optional)*: validates workflow changes

```bash
make dev    # serves on :8080, data in ./data
```

## Project conventions

- **No build step for the front-end**: `html/template` and vanilla JS/CSS,
  embedded in the binary. No npm.
- **Desktop and mobile matter equally**: every UI change is checked at
  1440×900 and at an iPhone viewport. Neither layout is a scaled version of
  the other.
- **Credentials never reach the browser**: Plex tokens and API keys are sealed
  at rest (`internal/secret`) and upstream images go through the poster proxy.
- **Errors**: wrap with context, `fmt.Errorf("context: %w", err)`.
- **Config**: `AGENDARR_*` environment variables, validated in `config.Load()`;
  a bad value fails at startup. A new variable goes in `config.go`,
  `Dockerfile` (if it has a default), `README.md` and
  `deploy/compose.yml.example`.
- **Tests**: table-driven; upstream APIs (plex.tv, Plex Media Server, Radarr,
  Sonarr, Seerr) are faked with `httptest` from real, anonymised responses.

## Workflow

The default branch is `main`; day-to-day work targets **`dev`**.

```bash
git switch dev
git switch -c my-change
```

Commits follow [Conventional Commits](https://www.conventionalcommits.org), in
English, with a body that explains the scenario when the change is not obvious:

```
feat: calendar sync
fix(api): reject events that end before they start
chore(deps): bump actions/checkout
chore(ci): add job timeouts
```

Pull requests target `dev` and are **squash-merged** (the only method enabled).
`main` is then fast-forwarded to `dev` at release time.

## Before pushing

```bash
make build
make test-race        # or: make test, if you have no C toolchain
make lint
make vuln
```

If you changed anything under `.github/workflows/`, run `actionlint`. Every
`uses:` **must** be pinned to a full commit SHA — the repository enforces it,
and an unpinned reference will simply refuse to run.

## Releases

Semantic versioning, judged by what is in the diff:

| Bump | When |
| ---- | ---- |
| patch | dependency bumps, fixes, CI-only changes |
| minor | new features |
| major | breaking changes to configuration or behaviour |

Git tags, release titles and release notes carry the `v` (`v1.2.3`); Docker
image tags do not (`ghcr.io/neotoxicfr/agendarr:1.2.3`), because
`docker/metadata-action` strips it — a `:v1.2.3` pull command points at a tag
that does not exist.

Release notes are written by hand: `## <emoji> <Category>` sections and a
`### Docker` block with the pull commands. No auto-generated content.
