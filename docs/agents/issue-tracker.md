# Issue tracker: GitHub

Issues and specs for this repo live as GitHub issues. Use the `gh` CLI for all operations. Infer the repository from the current Git remote.

## Conventions

- Create: `gh issue create --title "..." --body "..."`
- Read: `gh issue view <number> --comments`
- List: `gh issue list` with appropriate state and label filters
- Comment: `gh issue comment <number> --body "..."`
- Apply or remove labels: `gh issue edit <number> --add-label "..."` or `--remove-label "..."`
- Close: `gh issue close <number> --comment "..."`

## Pull requests as a triage surface

**PRs as a request surface: no.**

GitHub shares one number space across issues and pull requests. Resolve an ambiguous number with `gh pr view <number>` and fall back to `gh issue view <number>`.

## Skill operations

When a skill says to publish to the issue tracker, create a GitHub issue.

When a skill says to fetch the relevant ticket, run `gh issue view <number> --comments`.

For wayfinding, use a `wayfinder:map` issue and `wayfinder:<type>` child issues. Prefer native GitHub sub-issues and issue dependencies; fall back to task lists and explicit `Blocked by:` references when those features are unavailable.
