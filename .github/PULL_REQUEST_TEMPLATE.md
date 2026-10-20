## Summary
<!-- Provide a clear, concise explanation of the change and the problem it solves. -->

## Changes
<!-- List the specific files, packages, or architectural components modified. -->

## Motivation and Context
<!-- Why is this change required? What issue does it fix? Link relevant issues: Fixes #123 -->

## Checklist
Before submitting this pull request, please verify that you have completed the following:

- [ ] `make verify` passes cleanly (`make fmt`, `make tidy`, `make lint`, `make test`, `make build`).
- [ ] Tests added or updated to cover the new logic, bug fix, or edge cases.
- [ ] Documentation updated:
  - If a configuration field in `internal/config` was added or modified, `docs/configuration.md` has been updated in this PR.
  - If architectural decisions or request lifecycle changes were made, `docs/architecture.md` has been updated.
- [ ] PR title follows the conventional commit style (for example, `feat(cache): add multi-tier cache` or `fix(proxy): handle connection reset`).
- [ ] No em dash characters (`\u2014`) are used in user-facing text, error messages, or documentation (use plain hyphens).
- [ ] Pull request is focused on one logical change (no unrelated refactorings or formatting changes mixed in).
- [ ] Commits use the scoped conventional format and contain no Co-Authored-By trailers.
- [ ] Any AI assistance used is disclosed in the description above.
