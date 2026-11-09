# Drafts as files

A user writes a Markdown file, turns it into a Dzen draft, edits it from either side, and sees no false changes.

## Sub-features

- `draft-dry-run` plans a push with no session or network.
- `draft-create` creates a draft and rewrites the file with its identity.
- `draft-noop` sends nothing when nothing changed.
- `draft-edit` changes one block, verified by a fresh pull.
- `draft-diff` shows the local edit.
- `draft-conflict` refuses a stale push with exit 6.
- `draft-reject` rejects unsupported Markdown locally, with a line number.

## How to get to it (user POV)

- `dzen push`, `dzen pull`, `dzen diff`, `dzen status`.

## Driving it with scripts/verify

Preconditions:

- The baseline. `WORK/a.md` contains `# TAG draft`, a blank line, and `Первый абзац с **жирным**.`

- **Dry run.** Run `cli -- push a.md --dry-run --json`. Exit 0, `outcome` is `dry_run_ok`, and `a.md` is unchanged.
- **Create.** Run `cli -- push a.md --json`. Exit 0, `data.status` is `draft`; `a.md` now holds `publication_id`.
- **Read back.** Run `cli -- status a.md --json`. `data.local` is `clean` and `data.title` starts with TAG.
- **No-op.** Run `cli -- push a.md --json`. Exit 0, `data.changed` is `false`, and `data.version` matches the previous push.
- **Edit.** Replace `Первый` with `Второй` in `a.md`. Run `cli -- diff a.md`, whose output has `+Второй абзац`. Run `cli -- push a.md --json`; `data.changes` has one `edit`.
- **Second view.** Run `cli -- pull ID --dir fresh --json`, using `publication_id` read from `a.md`. The new file contains `Второй абзац с **жирным**.`
- **Conflict.** Edit the file in `fresh` and push it. Then change `a.md` and run `cli -- push a.md --json`. Exit 6, `revision_conflict`.
- **Unsupported.** Write `b.md` containing `# TAG b`, a blank line and a fenced code block. Run `cli -- push b.md --json`. Exit 2, `markdown_unsupported`, `error.diagnostics[0].line` set.

## Gotchas

- `push` rewrites the file to canonical form; read `publication_id` after the first push.
- Dzen raises `version` on every real save, so a matching version is the proof of a no-op.
