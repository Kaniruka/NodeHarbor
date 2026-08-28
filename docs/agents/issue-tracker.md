# Issue tracker: GitHub

Issues and specs for this repository live in GitHub Issues. Use the `gh` CLI and infer `Kaniruka/NodeHarbor` from the Git remote.

## Conventions

- Create, read, list, comment on, label, and close issues with `gh issue`.
- Fetch comments and labels when reading an issue.
- When a skill says "publish to the issue tracker", create a GitHub issue.
- When a skill says "fetch the relevant ticket", use `gh issue view <number> --comments`.

## Pull requests as a triage surface

PRs as a request surface: no.

A bare GitHub number may identify an issue or PR. Resolve it with `gh pr view <number>` and fall back to `gh issue view <number>`.

## Wayfinding operations

- A map is one issue labelled `wayfinder:map`.
- Child tickets use GitHub sub-issues when available.
- Child labels use `wayfinder:<type>`.
- Use native GitHub issue dependencies for blocking relationships.
- Claim work by assigning the issue to the current user.
- Resolve work by recording the result, closing the child, and updating the map's decisions.
