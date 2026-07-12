# dzencrawl V1 Spec

## Цель

Создать local-first read-only архив публичного канала `dzen.ru`, совместимый с
общими control/status, SQLite, embedding и vector primitives `crawlkit`.

## Источники

- Firecrawl `/v2/scrape`: канонический Markdown и полный rendered HTML статьи.
- Публичный SSR state Дзена: публикация, автор, timestamps и метрики.
- Read-only launcher pagination: обнаружение всех карточек канала.
- Read-only comments v2: корневые комментарии, ответы и cursor pagination.

Ни один источник не требует учётной записи Дзена. Любой ответ считается
недоверенным и проходит JSON decoding, size limit и schema checks.

## SQLite

- `channels`
- `articles`
- `comments`
- `embeddings`
- `sync_state`
- `article_fts`
- `comment_fts`

`articles.comments_complete` показывает, совпало ли число сохранённых
уникальных комментариев с публичным `totalCommentsCount`.

## Markdown

Путь:

```text
articles/<channel>/<unicode-title>-<short-id>.md
```

Front matter включает URL, даты, просмотры, дочитывания, время чтения, лайки,
число доступных/сохранённых комментариев и признак полноты. После тела статьи
идёт секция комментариев.

## Инкрементальность

Обычный `sync` пропускает уже известные URL. `sync --refresh` повторно получает
статью, метрики и комментарии. Embeddings пересчитываются только при изменении
SHA-256 заголовка или Markdown.

## Не входит в V1

- закрытые публикации и комментарии;
- авторизация и действия от лица пользователя;
- обход CAPTCHA, paywall или технических ограничений;
- запись/лайки/комментирование в Дзен;
- зеркалирование бинарных изображений и видео;
- гарантия сохранения удалённого или скрытого Дзеном контента.
