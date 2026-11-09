# dzen

`dzen` управляет статьями и комментариями канала [Дзена](https://dzen.ru) из терминала. Статья здесь это обычный Markdown-файл: агент или человек пишет его, а `dzen push` превращает в черновик.

```bash
go install ./cmd/dzen

dzen auth import              # забрать вход из Chrome, без окон и вопросов
dzen push article.md          # создать или обновить черновик
dzen publish article.md --yes --wait 5m
```

Требуется Go 1.27+. Официального API у Дзена нет, поэтому `dzen` работает с приватным API студии. Это наблюдаемые контракты, а не обещания провайдера: если ответ изменился, команда завершится с `contract_drift`, а не будет гадать.

Полный контракт команд, форматов и кодов выхода: [`docs/CLI_SPEC.md`](docs/CLI_SPEC.md). Исследование: [`docs/research/`](docs/research/to-cli-synthesis.md).

## Статья как файл

```markdown
---
title: "Как выбрать походный рюкзак"
cover: ./img/cover.jpg
comments: "on"
---

Хороший рюкзак незаметен в пути.

## Объём

Для выходных хватит *30–40 литров*, подробности [в гиде](https://example.com/guide).

![](./img/pack.jpg "Рюкзак на 45 литров")
```

Заголовок можно задать и первой строкой `# Заголовок`. Поддерживаются абзацы, заголовки `##` и `###`, цитаты, плоские списки, жирный, курсив, ссылки, картинки и галереи. Всё остальное падает локально с номером строки, потому что Дзен это отвергает.

```bash
dzen pull https://dzen.ru/a/SLUG --dir drafts   # взять статью на правку
dzen diff drafts/article.md                     # что изменилось в файле
dzen diff drafts/article.md --remote            # что изменилось на сервере
dzen status drafts/article.md
dzen push drafts/article.md --dry-run           # план без сети и без сессии
```

`push` отправляет заново только изменённые блоки: всё, что не менялось, возвращается байт в байт, включая блоки, которых нет в Markdown (они выглядят как `<!-- dzen:raw ... -->`). После отправки файл переписывается в канонический вид и получает служебный блок `dzen:`.

Сервер не защищает от перезаписи: `dzen` сам сверяет версию и отказывается отправлять с кодом 6, если статья изменилась. Открытая вкладка редактора всё равно может затереть результат, поэтому редактировать одну статью из браузера и CLI одновременно не стоит.

## Публикация

```bash
dzen publish article.md --yes                               # сейчас
dzen publish article.md --at 2026-11-20T09:00:00+03:00 --yes  # отложенно
dzen article discard-changes ID --yes                       # отменить правки
dzen article delete ID --yes                                # необратимо
```

Снять статью с публикации в Дзене нельзя, только удалить. Запланированную статью тоже нельзя вернуть в черновики: можно перенести дату, опубликовать немедленно или удалить.

## Комментарии

```bash
dzen comment list ID --json
dzen comment list --channel
dzen comment reply CID --article ID --text "Спасибо!"
dzen comment ban UID --article ID --yes
```

Капча решается автоматически через 2Captcha. Ключ берётся из `TWOCAPTCHA_API_KEY`, `captcha.credential_process` или `~/.config/2captcha/api-key`. При частых публикациях Дзен показывает капчу VK ID, и для неё 2Captcha требует прокси: задайте `DZEN_CAPTCHA_PROXY=socks5://user:password@host:port`. Куки и текст статьи в 2Captcha не уходят.

## Аккаунты

Один аккаунт это один канал и один профиль. `dzen auth import --all` заводит профили для всех найденных аккаунтов, `dzen auth use NAME` и `--profile NAME` выбирают нужный. Сессии лежат в `~/.dzen/sessions` с правами `0600`. Истёкшая импортированная сессия сама обновляется из Chrome.

## Архив

`dzen crawl` хранит каналы локально в SQLite. Сеть нужна только `sync`, остальные команды работают офлайн.

```bash
dzen crawl sync https://dzen.ru/example_channel --max-articles 10
dzen crawl sync --mine            # свои черновики, запланированные и опубликованные
dzen crawl search "базовый гардероб"
dzen crawl export-md
dzen crawl sql 'select title from articles'
```

Для векторного поиска включите embeddings в `~/.dzen/config.toml`, раздел `[embeddings]`, и выполните `dzen crawl embed`. Данные старого `~/.dzencrawl` переносятся при первом запуске.

Соблюдайте условия Дзена и права авторов. Правила Дзена запрещают контент, созданный «почти полностью автоматически, без участия автора»: границы для агента заданы в [`skills/dzen`](skills/dzen/SKILL.md).

## Разработка

```bash
go test ./...
go vet ./...
go build ./cmd/dzen
```

Проверка на реальном канале: [`.agents/skills/verify-dzen`](.agents/skills/verify-dzen/SKILL.md).
