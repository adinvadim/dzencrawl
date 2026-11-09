# Comments

A user reads, writes and moderates comments under an article.

## Sub-features

- `comment-read` lists comments of an article and the channel feed.
- `comment-post` writes a comment, verified by listing it.
- `comment-reply` replies in a thread.
- `comment-edit` edits within the hour.
- `comment-delete` deletes and restores.
- `comment-pin` pins and unpins.
- `comment-react` sets and cancels a reaction.
- `comment-ban` is proven only by a harmless refusal.

## How to get to it (user POV)

- `dzen comment list|post|reply|edit|delete|restore|pin|unpin|react|ban|unban|banned`.

## Driving it with scripts/verify

Preconditions:

- The baseline. A published article carrying `TAG` whose `status --json` has a non-empty `data.url`.

- **Read.** Run `cli -- comment list ID --json` and `cli -- comment list --channel --json`. Exit 0 for both.
- **Post.** Run `cli -- comment post ID --text "TAG" --json`. Exit 0, `data.comment_id` set. `comment list` shows it.
- **Reply.** Run `cli -- comment reply CID --article ID --text "ответ" --json`. It appears with `parent_id` set to `CID`.
- **Edit.** Run `cli -- comment edit CID --article ID --text "новый" --json`. `comment list` shows the new text.
- **Pin.** Run `cli -- comment pin CID --article ID`, then `unpin`. Exit 0 for both.
- **React.** Run `cli -- comment react CID --article ID --reaction like`, then with `cancel_like`.
- **Delete.** Run `cli -- comment delete CID --article ID --yes`, then `restore`.
- **Ban list.** Run `cli -- comment banned --json`. Exit 0.

## Gotchas

- Dzen refuses comments on an article with no `commonUrl`, with `remote_validation`. A fresh article can wait a long time for one: report the feature as blocked by that precondition.
- Comment only under the run's own article. Bans need a real second reader, which is out of scope, so `ban` stays bundle-derived and unverified.
