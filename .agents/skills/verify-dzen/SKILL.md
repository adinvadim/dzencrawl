---
name: verify-dzen
description: "Verify the dzen CLI against real Dzen; prove a CLI change on the live test channel."
---

# Verify dzen

Prove `dzen` behavior on the real service: the surface is a short-lived CLI talking to production Dzen, so each run builds a binary, uses a disposable data directory, and drives the dedicated test channel. Run everything through `scripts/verify`; it pins the account, masks private data, and records evidence.

## Setup, once per computer

`~/.dzen-verify/local.env` holds the account that may be tested. It stays outside the repository, which is public.

```bash
DZEN_VERIFY_LOGIN=<login of the empty test channel>
```

Chrome must be signed in to that account with remote debugging enabled.

## Launch

```bash
.agents/skills/verify-dzen/scripts/verify launch
```

Builds the checkout, creates a run under `~/.dzen-verify/runs/<run>`, imports the session and stops unless the principal is `DZEN_VERIFY_LOGIN`. Ready when it prints `ready <run>`. One run at a time per computer: a second `launch` refuses while one is active.

## Doctor

```bash
.agents/skills/verify-dzen/scripts/verify doctor
```

Read-only. Worth driving only when it prints `contract=ok` and the expected git revision. Run it first whenever anything looks off.

## Drive

Pick the feature from [the map](features/README.md) and follow its recipe. Run each command as `scripts/verify cli -- <dzen args>`. Prefix every test title with the value `scripts/verify tag` prints.

```bash
.agents/skills/verify-dzen/scripts/verify cli -- draft list --json
```

Every publication the run creates carries the tag. Only the empty test channel, and only objects carrying the tag, are in scope: third-party articles and real readers are out of scope for comments, reactions, reports and bans.

## Evidence

`scripts/verify cli` appends each command, stdout, stderr and exit code to `~/.dzen-verify/evidence/<run>/transcript.log`, with ids and the login masked.

A proof shows the real command path, the action, and a second read of the resulting state. A mutation also shows the published or saved value through a different command (`push` then `pull`, `publish` then `status`). For `--dry-run`, the proof is that a `doctor`-less run with no session succeeds and the file is unchanged.

Report every feature ID with the transcript path. A path you could not reach is reported with its unmet precondition, never as verified through another path.

## Cleanup

```bash
.agents/skills/verify-dzen/scripts/verify cleanup
```

Deletes every publication carrying this run's tag, confirms by read-back, and removes the run directory with its session. Evidence stays in `~/.dzen-verify/evidence/<run>`. Run it after failed attempts too.

## Maintain

Add a feature file when a command appears, and keep recipes truthful: a precondition that cannot be met is a failure with the reason in that file's `Gotchas`.
