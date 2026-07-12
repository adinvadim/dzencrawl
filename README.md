# dzencrawl

Локальный архиватор публичных каналов Дзена на базе
[`openclaw/crawlkit`](https://github.com/openclaw/crawlkit), сделанный в стиле
[`notcrawl`](https://github.com/openclaw/notcrawl) и `gitcrawl`.

`dzencrawl` сохраняет:

- полный Markdown статьи, полученный через Firecrawl;
- дату публикации и изменения;
- просмотры, дочитывания, лайки и время чтения;
- корневые комментарии и ответы, автора, дату и реакции;
- исходные нормализованные JSON-данные публикации;
- FTS5-индекс и embeddings в SQLite;
- детерминированные `.md`-файлы с front matter и комментариями.

SQLite — канонический архив. Markdown — переносимый человеко- и
agent-friendly экспорт.

## Быстрый старт

Требуется Go 1.26.5+ и ключ Firecrawl.

```bash
go install ./cmd/dzencrawl

dzencrawl init
export FIRECRAWL_API_KEY="your-firecrawl-api-key"

dzencrawl doctor
dzencrawl sync https://dzen.ru/example_channel
dzencrawl export-md
dzencrawl search "базовый гардероб"
```

Для безопасной первой проверки:

```bash
dzencrawl sync --max-articles 2 https://dzen.ru/example_channel
```

По умолчанию данные находятся в:

- config: `~/.dzencrawl/config.toml`
- SQLite: `~/.dzencrawl/dzencrawl.db`
- Markdown: `~/.dzencrawl/articles/`

## Embeddings

Используется пакет `crawlkit/embed`; поддерживаются OpenAI, Ollama,
llama.cpp и OpenAI-compatible endpoints. По умолчанию embeddings выключены,
чтобы первая синхронизация не отправляла текст третьей стороне. Для локального
Ollama измените config:

```toml
[embeddings]
enabled = true
provider = "ollama"
model = "nomic-embed-text"
base_url = "http://127.0.0.1:11434"
request_timeout = "2m"
max_input_chars = 12000
```

Затем:

```bash
dzencrawl embed
dzencrawl semantic-search "как сочетать джоггеры"
```

При `enabled = true` команда `sync` автоматически обновляет embeddings после
загрузки. Векторы хранятся как float32 BLOB, а SHA-256 текста предотвращает
лишний пересчёт.

OpenAI-конфигурация:

```toml
[embeddings]
enabled = true
provider = "openai"
model = "text-embedding-3-small"
api_key_env = "OPENAI_API_KEY"
```

## Команды

```text
init                         создать config
 doctor                      проверить конфигурацию
sync CHANNEL                 синхронизировать канал
  --max-articles N           ограничить число новых статей
  --refresh                  обновить уже сохранённые статьи и метрики
export-md                    экспортировать Markdown
search QUERY                 полнотекстовый поиск
embed [--force]              построить embeddings
semantic-search QUERY        семантический поиск
status [--json]              состояние архива/crawlkit payload
sql 'SELECT ...'             read-only SQL
metadata                     crawlkit control manifest
```

## Как работает получение данных

1. Публичная лента канала перечисляется через read-only launcher endpoint
   Дзена с пагинацией. Он не документирован и может измениться.
2. Лента запрашивается с явно подписанным `dzencrawl/... Googlebot-compatible`
   User-Agent: Дзен отдаёт заполненный server-rendered feed только crawler-
   представлению. Это настраивается в config и не маскируется под обычный браузер.
3. Каждая статья передаётся Firecrawl как публичный URL с `output=1`. Этот
   параметр выбирает серверный публичный HTML и не обходит авторизацию.
4. Firecrawl возвращает `markdown` и `rawHtml`. Markdown сохраняется как текст
   статьи; SSR JSON из `rawHtml` даёт точные метрики и идентификаторы.
5. Публичный read-only comments endpoint догружает корневые комментарии,
   ответы и все курсоры. Если Дзен скрывает/удаляет часть комментариев,
   `comments_complete: false` честно фиксирует неполное покрытие.

`request_delay = "1500ms"` намеренно ограничивает частоту прямых read-only
запросов. Firecrawl вызывается последовательно. Архиватор не логинится, не
обходит CAPTCHA/paywall и ничего не пишет в Дзен.

## Ограничения и ответственное использование

- API ленты и комментариев не публично документированы, поэтому парсер покрыт
  контрактными тестами, но может потребовать обновления после изменений Дзена.
- Соблюдайте условия Дзена, права авторов, robots policy и применимое
  законодательство. Не публикуйте чужой архив без разрешения.
- Не уменьшайте задержку и не запускайте несколько синхронизаций одного канала
  параллельно.
- Для очень большого канала сначала используйте `--max-articles`, оцените
  расход Firecrawl credits, затем продолжайте. Уже сохранённые URL пропускаются.
- Метрики являются снимком на момент `sync`; используйте `--refresh` для
  обновления.

## Разработка

```bash
go test ./...
go vet ./...
go build ./cmd/dzencrawl
```

См. также [`SPEC.md`](SPEC.md).
