## What and why

<!-- What changes, and what problem it solves. Link an issue if there is one. -->

## Checks

- [ ] `make build` / `make lint` pass
- [ ] `make test-race` passes (or `make test` with a note if no C toolchain)
- [ ] `make vuln` clean
- [ ] **UI touched?** checked at 1440×900 **and** on a phone viewport
- [ ] `actionlint` clean *(only if `.github/workflows/` changed — every `uses:` pinned to a full SHA)*
- [ ] No secret, token or personal data in the diff

## Impact

- [ ] **New Go dependency?** justified below
- [ ] **New config option?** added to `config.go`, `README.md` **and** `deploy/compose.yml.example`

## Notes

<!-- Anything a reviewer should know: trade-offs, follow-ups, things left out. -->
