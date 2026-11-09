# dzen verification map

The maintained source for proving dzen's user-facing behavior on the live test channel. Read this index, then follow the matching recipe.

## Baseline preconditions

- A run is active: `scripts/verify launch` printed `ready`.
- `scripts/verify doctor` reports `contract=ok` and `principal=expected`.
- `TAG` is the output of `scripts/verify tag`; `WORK` is the output of `scripts/verify workdir`, where relative paths resolve.
- Every run command goes through `scripts/verify cli --`.

## Driving conventions

- Start each recipe from the baseline. Create what it needs under a title starting with `TAG`.
- Write files inside `WORK` from the shell, then pass their names to the CLI.
- Assert on `--json` fields and exit codes.
- Restore shared state by cleaning up the run. Only objects carrying `TAG` are touched.

## Proof and skip reporting

- A proof is the transcript entry for the action plus a second read of the state through another command.
- Record the feature ID with the transcript path.
- Report an unreachable path with the attempted command and the unmet precondition. A skipped path is never reported as verified through another.

## Feature entry contract

Each file starts with an H1 and one paragraph, then four H2 sections in order: `Sub-features`, `How to get to it (user POV)`, `Driving it with scripts/verify`, `Gotchas`.

## Features

- [Accounts](./auth.md): importing the Chrome session, principal checks, profiles.
- [Drafts as files](./drafts.md): push, pull, diff, status, no-op, conflict, dry-run.
- [Publishing](./publishing.md): publish, edits of published articles, scheduling, delete.
- [Comments](./comments.md): reading, writing and moderation under the run's own article.
- [Archive](./crawl.md): public and own sync, offline queries, control manifest.
