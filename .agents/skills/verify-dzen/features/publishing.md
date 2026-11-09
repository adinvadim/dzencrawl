# Publishing

A user publishes a draft now or later, edits a published article, and removes it.

## Sub-features

- `publish-gate` refuses to publish without confirmation.
- `publish-now` publishes and reports `accepted_pending` until Dzen issues a URL.
- `publish-edit` requires confirmation to save a published article.
- `publish-discard` drops unpublished edits.
- `publish-schedule` schedules and reschedules.
- `publish-delete` deletes and proves it with a read.

## How to get to it (user POV)

- `dzen publish`, `dzen push`, `dzen article discard-changes`, `dzen article delete`.

## Driving it with scripts/verify

Preconditions:

- The baseline. Push `# TAG publish` with one paragraph; `ID` is its `publication_id`.

- **Gate.** Run `cli -- publish a.md --no-input`. Exit 4, `needs_confirmation`, and `status` still shows `draft`.
- **Publish.** Run `cli -- publish a.md --yes --json`. Exit 0, `data.status` is `published`; `outcome` is `accepted_pending` or `confirmed`.
- **Edit gate.** Change the paragraph. Run `cli -- push a.md --no-input`. Exit 4.
- **Edit.** Run `cli -- push a.md --yes --json`, then `cli -- status ID --json`. `data.draft_status` is `has-unpublished-changes`.
- **Discard.** Run `cli -- article discard-changes ID --yes`, then `status`. `data.draft_status` is `no-changes`.
- **Schedule.** Push a second tagged article. Run `cli -- publish b.md --at <2 days ahead, RFC 3339> --yes`, then `cli -- status ID2 --json`. `data.delayed` is `on` and `data.scheduled_for` matches.
- **Reschedule.** Repeat with a later `--at`. `data.scheduled_for` changes and `delayed` stays `on`.
- **Window.** Run `cli -- publish b.md --at <10 minutes ahead> --yes`. Exit 2, `validation_schedule_window`.
- **Delete.** Run `cli -- article delete ID2 --yes`, then `cli -- status ID2`. Exit 5, `not_found`.

## Gotchas

- `publish` without `--at` on a scheduled article publishes it immediately; Dzen has no way back to drafts.
- Dzen can answer every publish with a VK ID captcha, even the first of a session after earlier runs, and `publish` exits 4 with `captcha_unsupported` unless `DZEN_CAPTCHA_PROXY` is set. Report the path as blocked by that precondition.
- A public URL can take long to appear, so `accepted_pending` is the expected outcome for a fresh channel.
