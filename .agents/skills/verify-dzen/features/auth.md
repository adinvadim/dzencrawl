# Accounts

A user takes an already signed-in Dzen session from Chrome and manages one profile per account.

## Sub-features

- `auth-import` imports the session without prompts or a browser window.
- `auth-principal` verifies the account with Dzen.
- `auth-list` lists profiles and marks the current one.
- `auth-noaccount` reports a missing session with exit 3.

## How to get to it (user POV)

- `dzen auth import`, `dzen auth status --live`, `dzen auth list`.

## Driving it with scripts/verify

Preconditions:

- The baseline. `launch` has already imported the session.

- **Import.** Run `cli -- auth import --no-input --json`. Exit 0 and `data.imported` has one profile.
- **Principal.** Run `cli -- auth status --live --json`. Exit 0 and `data.verified` is `true`.
- **List.** Run `cli -- auth list --json`. One item with `current` true.
- **Missing account.** Run `cli -- --profile nobody auth status`. Exit 3, `not_authenticated`.

## Gotchas

- With Chrome remote debugging off, import reads the cookie database and macOS may ask for Keychain access once. That needs a person at the machine; keep debugging on for verification.
- Import stops at the first valid account unless `--all` is passed.
