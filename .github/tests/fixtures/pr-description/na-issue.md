## Summary
<!-- What this PR changes and why, in a few sentences a reviewer can follow without opening the diff. -->
Retries the AWS topology request once when the API throttles it.

## Linked issue
<!-- Write the issue number after Fixes or Related, or tick the trivial-fix box. CONTRIBUTING.md "Open an issue first" says when an issue is needed. -->
Fixes: N/A
Related: N/A
- [ ] Trivial fix (typo, broken link, small doc correction): no issue needed, per CONTRIBUTING.md "Open an issue first"

## Type of change
<!-- Tick every box that applies. -->
- [x] Bug fix
- [ ] New feature
- [ ] Breaking change
- [ ] Documentation
- [ ] Refactoring (no behavior change)
- [ ] Build or CI

## Area
<!-- Tick every area this PR touches. -->
- [x] Providers (pkg/providers)

## Testing
<!-- The commands you ran and their results: `make qualify`, plus `make chart-test` when charts/topograph/ changed. Add any manual or end-to-end check and what it showed. -->
`make qualify` passes.

## Documentation impact
<!-- The docs this PR updates, per the "Documentation Impact Evaluation" table in AGENTS.md, or why none apply. Record user-facing changes in CHANGELOG.md under [Unreleased]. -->
CHANGELOG.md under Fixed.

## Risk
<!-- Breaking changes, migration steps for operators, and what could go wrong after merge. -->
None.

## Checklist
- [x] I am familiar with the [Contributing Guidelines](./CONTRIBUTING.md).
- [x] New or changed behavior is covered by a test.
- [x] No credential, API key, or token appears in the diff.
- [x] All commits are signed off per DCO (`git commit -s`).
