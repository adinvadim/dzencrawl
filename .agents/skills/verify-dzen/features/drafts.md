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
- `draft-variants` round-trips every article shape: text only, every text block, images in PNG, JPEG, GIF and WebP with and without captions, galleries, YouTube and Instagram embeds, and each front matter field.
- `draft-cover` accepts a cover from the body and refuses any other.
- `draft-comment-switch` keeps `comments` and `comments_visibility` consistent.

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

- **Variants.** For each article shape, run `cli -- push NAME.md --json`, then `cli -- pull ID --dir fresh/NAME --json`, then `cli -- push NAME.md --json` and `cli -- diff NAME.md --remote`. The push has no `warnings`, the pulled body matches, `data.changed` is `false`, and the diff is empty.
- **Cover.** Push `cover: "img/a.png"` with that image in the body: no warnings, and a pulled file has a `cover` line. Change `cover` to an image outside the body: exit 2, `validation_cover_not_in_body`, also with `--dry-run`.
- **Comment switch.** In a pulled file set `comments: "off"`, leaving `comments_visibility: visible`, and push: no warnings, and the file then holds `comments_visibility: invisible`.

## Gotchas

- `push` rewrites the file to canonical form; read `publication_id` after the first push.
- Dzen raises `version` on every real save, so a matching version is the proof of a no-op.
- Dzen answers `OK` to a save whose cover is outside the body and drops the cover. A read of the saved state is the only proof a cover stuck.
- Dzen fetches `--import-remote` URLs itself and fails on some hosts with HTTP 500 (Wikimedia, picsum); that is `remote_validation`, exit 5. A 1x1 image fails the same way.
