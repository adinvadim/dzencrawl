# Archive

A user keeps channels in a local archive and queries it offline.

## Sub-features

- `crawl-public` syncs a public channel by nickname.
- `crawl-mine` syncs the author's own publications.
- `crawl-offline` answers queries with no network.
- `crawl-readonly` refuses writes through `sql`.
- `crawl-manifest` prints `crawlkit.control.v1`.

## How to get to it (user POV)

- `dzen crawl sync|status|search|export-md|sql`, `dzen metadata`.

## Driving it with scripts/verify

Preconditions:

- The baseline.

- **Public.** Run `cli -- crawl sync https://dzen.ru/skillbox --max-articles 1`. Exit 0, or exit 7 with `could not be archived` when a feed item is a dead page; `crawl status` shows `articles>=1`.
- **Own.** Run `cli -- crawl sync --mine --json`. Exit 0, `data.records` set.
- **Search.** Run `cli -- crawl search TAG --json`. Exit 0.
- **Read-only.** Run `cli -- crawl sql "delete from articles"`. Exit 2 and `crawl status` keeps its counts.
- **Manifest.** Run `cli -- metadata`. `schema_version` is `crawlkit.control.v1` and `id` is `dzen`.

## Gotchas

- Feed items can point at deleted pages; those degrade the run with exit 7 without stopping it.
- `/id/<hex>` channel URLs are refused: the public feed cannot address them.
