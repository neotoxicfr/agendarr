# Contributing

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
