<!--
Thanks for the PR. Keep the title short (≤72 chars). Use a Conventional
Commits prefix in the title if you can: feat / fix / refactor / docs / test /
chore.
-->

## Summary
<!-- One or two sentences. What does this change and why? -->

## Linked issues
<!-- Closes #123, Refs #456 -->

## Changes
- ...

## Tests
- [ ] `make test` passes locally (`go test -race -count=1 ./...`)
- [ ] `make lint` passes (`golangci-lint run`)
- [ ] If the SPA changed: `cd web && npm run build` is clean
- [ ] Added or updated tests where behaviour changed

## Notes for reviewers
<!-- Anything non-obvious you'd like a reviewer to focus on. -->

## Checklist
- [ ] My change does not regress the offline-first guarantee
- [ ] Public APIs and CLI flags are documented in `README.md` if changed
- [ ] User-visible changes are listed in `CHANGELOG.md` under `[Unreleased]`
- [ ] I have read the [contribution guide](../CONTRIBUTING.md) and agree to the [GPL-3.0-or-later](../LICENSE) licensing of my work
