# Article format

CommonMark with HTML-comment directives. A construct outside this list fails locally with its line number, because Dzen rejects it.

## Front matter

Optional. Unknown fields are errors.

| Field | Value |
|---|---|
| `title` | Up to 140 characters, no links. Required unless the body starts with `# Title` |
| `cover` | An image that is also in the body: the same local path, or the image URL `pull` wrote. Any other cover is refused with `validation_cover_not_in_body`, because Dzen would silently drop it |
| `snippet` | Up to 1000 characters. Omit it to let Dzen generate one |
| `comments` | `"on"` or `"off"`; quote it, YAML reads a bare `on` as true. `"off"` also hides existing comments |
| `comments_visibility` | `visible`, `invisible`, `subscribe-visible`. Dzen ties `invisible` to `comments: "off"`; changing either one sets both |
| `comments_title` | Custom comments heading |
| `native_ad` | `true` for partner material |
| `dzen:` | Written by the CLI. Keep it untouched |

## Body

| Write | For |
|---|---|
| paragraph | one blank line between blocks; a line break inside is a space |
| `\` at line end | a soft line break inside a block |
| `## H2`, `### H3` | headings; any other level fails |
| `> text` | quote; each paragraph is one quote block |
| `- a`, `1. a` | flat lists; nesting fails |
| `**bold**`, `*italic*`, `[text](https://url)` | inline, `http` or `https` only |
| `![](./a.png "caption")` | an image, alone in its paragraph; the title is the visible caption |

Images: a local file is uploaded on `push` (at least 300 px wide, at most 30 MB, 100 per article). A remote `https://` image needs `--import-remote`. Each publication has its own images: a Dzen image URL from another article fails.

Limits: body 100 000 characters, caption 2000, comment 2500.

## Directives

Each sits on its own line.

```markdown
<!-- dzen:empty -->

<!-- dzen:gallery -->
![](./one.jpg "caption")
![](./two.jpg)
<!-- /dzen:gallery -->

<!-- dzen:embed url="https://www.youtube.com/watch?v=ID" -->

<!-- dzen:raw key="abc12" type="atomic:poll" -->
```

`dzen:empty` is a blank paragraph. A gallery holds up to 30 images. `dzen:embed` accepts YouTube and Instagram. `dzen:raw` appears only after `pull`: it stands for a block kept in the snapshot, which you may move or delete, and only `pull` can create one.

Characters such as `*`, `_`, `[` and `#` are literal when escaped with `\`. `push` rewrites the file in canonical form, so expect its escaping and image URLs to change.
