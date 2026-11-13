# Contributing

General contribution expectations for this project, including where to track bugs and features, are in the [ptp-operator CONTRIBUTING.md](https://github.com/k8snetworkplumbingwg/ptp-operator/blob/main/CONTRIBUTING.md). This file adds the commit expectations for this repository.

## Commit expectations

A PR may contain several commits. Each commit should be easy to review, revert and bisect on its own:

- Keep commits small, self-consistent and atomic: one logical change per commit, together with the tests and generated or vendored files that change needs.
- Refactoring needed for a feature or fix may be submitted in the same PR, but it must be in its own commit, separate from the feature or fix.
- Every commit must pass CI on its own, including lint and formatting checks, unit tests and the other required checks. It is not enough for only the final commit of the PR to pass.
