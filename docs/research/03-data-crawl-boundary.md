# 03. Граница данных и crawl для authoring CLI Дзена

Исследовательский отчёт, трек «Data and crawl boundary». Дата: 2026-11-09.
База: ветка `t3/dzen-article-cli-design`, коммит `9fb836a`.

Метки: **[C]** Confirmed (есть `file:line`, URL или воспроизведение), **[I]** Inferred
(вывод из подтверждённых фактов), **[U]** Unknown (нужна проверка, обычно приватным API).

## Главные выводы

Текущий `dzencrawl` годится как фундамент, но расширять его поверх нынешней модели
данных нельзя: при неудачном обновлении он **стирает уже сохранённые комментарии**,
один элемент ленты типа `gif` или `short_video` **прерывает весь sync**, а
`sql` **выполняет запись**, хотя называется read-only. Для инструмента, которым
управляет AI-агент и который хранит приватные черновики, эти три дефекта надо закрыть
раньше любых новых функций.

Рекомендуемая граница: `sync` наполняет append-friendly архив, а всё, что меняет или
готовит изменение в Дзене, читает живое состояние. Рабочая копия черновика живёт в
файлах, вне SQLite и вне каталога `export-md`, и связана с архивом только ссылкой на
неизменяемую базовую ревизию. `sync` рабочих копий не касается никогда.

По форме продукта рекомендую один модуль и две точки входа: новый `dzen` для авторинга
и `dzencrawl`, который физически не линкует код записи в Дзен и остаётся идентичностью
для `crawlctl`. crawlkit обновить до v0.17.4 отдельным подготовительным шагом:
экспортируемый API используемых пакетов не изменился, сборка и тесты на копии проходят,
но минимальная версия Go поднимается до 1.27.

Приятная находка: прямой `GET` публичной статьи с `?output=1` отдаёт полное draft.js
состояние (`contentState`). Для разбора статьи Firecrawl не обязателен, а уже
накопленные архивы содержат это состояние в `articles.raw_json`, то есть ревизии можно
восстановить миграцией без сети.

## Метод и ограничения

Прочитаны весь репозиторий, модуль crawlkit v0.13.4 из module cache, changelog v0.17.4
через `gh`, референсные проекты. Браузер и авторизованный доступ не использовались,
поэтому всё о приватном API здесь помечено [U].

Дефекты проверялись через `go test -overlay` с временными тестами вне репозитория, так
что файлы проекта не менялись. Базовые проверки чистые: `go build ./...` и `go vet ./...`
без ошибок, `go test ./...` проходит [C].

Публичные запросы: всего 14 за всю работу, с паузой 1700 мс между запросами в
скриптах: `robots.txt` (1), sitemap каналов (3), лента (5, из них 3 ответа 404),
статья с `output=1` (1), страница видео (1), комментарии (2, `batchSize=2`
для корня). Четырнадцатый запрос случаен: временный тест по ошибке обратился к реальному
хосту, потому что базовый URL в клиенте зашит (`internal/dzen/client.go:123`). Это был один
`GET` на несуществующий путь `api/comments/v2/probe-direct`, ответ 404. Канал выбран случайно из
sitemap и в отчёте не назван; текст комментариев и данные авторов не выводились и не
сохранялись в репозитории. Выборка мала (один канал, одна статья, одно видео), поэтому
контрактные выводы о публичном API верны для наблюдаемых ответов и должны стать
фикстурами, а не считаться гарантией.

## 1. Аудит текущего репозитория

### 1.1 Что переиспользуется

| Компонент | Оценка для авторинга | Источник |
| --- | --- | --- |
| Публичный клиент ленты и комментариев | Переиспользуется как реализация read-only источника; задержка уже есть, но заголовки, cursor-логика и обработка ошибок требуют усиления | `internal/dzen/client.go:26-160` [C] |
| Парсер SSR `var _params=(` | Переиспользуется как извлечение публичного контракта; `UseNumber` уже применён | `internal/dzen/parser.go:63-89` [C] |
| Открытие SQLite через crawlkit | Переиспользуется: WAL, `foreign_keys`, busy timeout, один writer, файл `0600` | `crawlkit@v0.13.4/store/store.go:217-219,258,270,280-291`, проба `foreign_keys=1`, `maxOpen=1` [C] |
| Embeddings и vector search | Переиспользуются почти без изменений | `cmd/dzencrawl/main.go:272-350` [C] |
| Control-манифест и статус | Форма правильная, содержание нужно исправить | `cmd/dzencrawl/main.go:392-415` [C] |
| Config-пути и TOML | Переиспользуются; нужна модель аккаунтов | `internal/config/config.go:45,64-84` [C] |
| Firecrawl-клиент | Оставить только как опциональный запасной путь для чужих каналов | `internal/firecrawl/client.go:43-91` [C] |
| Markdown-экспорт | Только как read-only выгрузка; как источник для записи не годится | `internal/markdown/export.go:47-72` [C] |

### 1.2 Дефекты, влияющие на расширение

| # | Важность | Дефект и воспроизведение | Файл | Влияние |
| --- | --- | --- | --- | --- |
| D1 | Критично | Частичный сбой комментариев при `--refresh` заменяет полный набор: `main` присваивает `comments = initial`, отбрасывая даже частичный результат `AllComments`, а `UpsertArticle` сначала удаляет все комментарии. Проба: 2 комментария, upsert с пустым набором, остаётся 0 [C] | `cmd/dzencrawl/main.go:215-219`, `internal/store/store.go:91-96`, `internal/dzen/client.go:78` | Потеря данных; модель delete-and-reinsert несовместима с tombstones и историей |
| D2 | Критично | `ReadOnlySQL` проверяет только префикс на соединении с правом записи. Пробы: `with x as (select 1) delete from comments ...` и `select 1; delete from comments` реально удалили строки [C]. У crawlkit есть `OpenReadOnly` с `mode=ro` и `query_only(1)`, он не используется | `internal/store/store.go:223-228`; `crawlkit@v0.13.4/store/store.go:75-96,221-223` | Агент может уничтожить архив командой, заявленной как read-only |
| D3 | Критично | Лента содержит `card`, `gif`, `short_video` [C], цикл sync фильтра не имеет, а страница видео с `output=1` не содержит `publishersResponse` [C, прямой запрос]. `ParseArticle` возвращает ошибку, и `sync` завершается целиком. Для пути через Firecrawl поведение [I] | `cmd/dzencrawl/main.go:195-214`, `internal/dzen/parser.go:79-88` | На смешанном канале полный sync не завершится |
| D4 | Высокая | Ложная полнота: при `CommentsCount == 0` возвращается `complete=true` без доказательства, а счётчик равен нулю и тогда, когда SSR его не отдал. Проба: 0 комментариев, `complete=true` при неизвестном счётчике [C] | `internal/dzen/client.go:41-43`, `internal/dzen/parser.go:38` | `comments_complete` нельзя считать доказательством полноты |
| D5 | Высокая | Нет состояния прогона: `status` всегда пишет `State="current"`, `sync_state` хранит один ключ `last_sync`, ошибка одной статьи прерывает прогон без отметки degraded | `cmd/dzencrawl/main.go:208-213,406`, `internal/store/store.go:113` [C] | Агент не отличит полный архив от оборванного |
| D6 | Высокая | Нет схемных миграций: только `create table if not exists`, `schema_migrations` отсутствует (проба: 0 таблиц, `user_version=0`) [C] | `internal/store/store.go:53-64` | Новые колонки на существующей БД не появятся |
| D7 | Высокая | Коллизия имён: локальный `sync_state(key,value,synced_at)` занимает имя таблицы `crawlkit/state.Schema`; `create table if not exists` молча пропустится, а создание индекса упадёт. Проба: `state.EnsureSchema` возвращает `no such column: updated_at`, тогда как `EnsureCursorSchema` и `NewCursor().Set` работают [C] | `internal/store/store.go:63`, `crawlkit@v0.13.4/state/state.go:10-20` | `state.New` нельзя включить без миграции; см. раздел 5 |
| D8 | Средняя | Манифест объявляет `sync` без канала, а вызов без аргумента всегда падает (проба [C]). `crawlctl` берёт argv из манифеста как задание по умолчанию | `cmd/dzencrawl/main.go:155-157,400`; `crawlkit@v0.13.4/scheduler/discover.go:144-148` | Плановый запуск через `crawlctl` не работает |
| D9 | Средняя | `ChannelName` отклоняет `https://dzen.ru/id/<hex>`. В просмотренной части sitemap такой формы 1495 из 2855 URL [C, выборка из первых 250 КБ одного из 9 файлов]; `dzen.ru/foo` без схемы становится именем `dzen.ru/foo` (проба [C]) | `internal/dzen/client.go:225-241` | Заметная доля каналов не адресуется; authoring-идентичность не должна зависеть от nickname |
| D10 | Средняя | FTS получает сырой ввод. Пробы: `foo-bar`, `foo, bar`, `c++` и незакрытая кавычка дают SQL-ошибку [C]. `comment_fts` наполняется, но нигде не запрашивается [C] | `internal/store/store.go:155` | Есть готовые `FTS5Terms`, `FTS5TokenQuery` (`crawlkit@v0.13.4/store/fts.go:17,40`) |
| D11 | Средняя | `sorting=top` для пагинации с курсором `fetchAfterCommentId`: при изменении лайков порядок плавает [I]. API предлагает `newest` и `oldest` [C] | `internal/dzen/client.go:45` | Возможны пропуски; нестабильный порядок нельзя чекпойнтить |
| D12 | Средняя | Идентичность по URL: `HasArticle` ищет `url`, хотя лента уже отдаёт `publication_object_id` | `internal/store/store.go:76-80`, `internal/dzen/types.go:43`, `cmd/dzencrawl/main.go:199` [C] | Нужно ключевать по ID до платного запроса |
| D13 | Средняя | Нет retry, backoff и распознавания CAPTCHA; тело ответа попадает в текст ошибки (до 300 байт) | `internal/dzen/client.go:153-154` [C] | В авторизованном клиенте это утечка и ложные сбои; см. `diacrawl/docs/architecture.md:104-105` |
| D14 | Средняя | Экспорт пишет файлы `0644`, каталоги `0755`, не удаляет файл при смене заголовка, потому что имя содержит slug | `internal/markdown/export.go:23,32,35-40` [C] | Приватные черновики нельзя выводить этим путём |
| D15 | Низкая | `sync CHANNEL --max-articles 1` падает, хотя help и README показывают флаги после канала; `--help` завершается ошибкой (пробы [C]) | `cmd/dzencrawl/main.go:105,149-157`, `README.md:91-93` | Агенты получат ложные ошибки; нужен настоящий CLI-фреймворк |
| D16 | Низкая | Смена модели не пересчитывает embedding: кэш сравнивает только хэш текста. `q.Vectors[0]` не проверяет длину | `cmd/dzencrawl/main.go:295-302,325-342` [C] | Смешанные векторы, возможная паника |
| D17 | Латентная | Ответы комментариев декодируются во float64, поэтому ID больше 2^53 округлились бы (проба [C]). Наблюдаемые ID 10-значные, сейчас не проявляется [C] | `internal/dzen/client.go:156`, сравнить с `parser.go:75` | Для lossless-архива хранить байты ответа, числа разбирать через `UseNumber` |

Проверено и дефектом **не** является: `foreign_keys` включён на уровне DSN, а пул
ограничен одним соединением, поэтому локальные `pragma` в `store.go:54` просто избыточны.

### 1.3 Структурные ограничения

Ручной разбор флагов на стандартном `flag` не масштабируется на дерево команд:
`--json` есть только у `status`, у `search` и `sql` нет машинного вывода, код выхода
всегда 1 (`cmd/dzencrawl/main.go:30-34`) [C].

Хранилище частично многоканальное (`articles.channel_id`), но CLI, config и состояние
одноканальные: `sync` принимает один канал, `channels.id` равен изменяемому nickname,
понятия аккаунта нет (`internal/store/store.go:55,113`) [C]. Стабильный `publisher_id`
лежит только в `articles`.

Тело статьи в архиве зависит от Firecrawl: `markdown` приходит из внешнего сервиса с
`onlyMainContent`, а запасной `draftMarkdown` теряет блоки `atomic:image`,
`atomic:embed`, упорядоченные списки, inline-стили и ссылки
(`internal/firecrawl/client.go:54-60`, `internal/dzen/parser.go:130-160`) [C].

Покрытие тестами: `cmd` 0%, `config` 0%, `markdown` 0%, `dzen` 44.4%, `firecrawl` 69.4%,
`store` 52.1% [C]. Нет фикстур реальных ответов, тестов пагинации, частичного сбоя,
повторного `--refresh` и миграций.

### 1.4 Что показал публичный контракт

Идентичность публикации удобна: slug в `/a/<slug>` оказался base64url от 12 байт
hex-идентификатора `publication_object_id` (33 из 33 в выборке) [C]. Ссылки в ленте шли
без query-параметров [C]. Лента постраничная, с `pageCount`, 19 элементов на странице;
запрос за пределы диапазона даёт 404 [C], поэтому проверка пустой страницы в
`cmd/dzencrawl/main.go:185` на практике не срабатывает [I]. `creation_time` приходит
относительной строкой вроде «6 месяцев назад» [C] и как водяной знак непригоден.

SSR публикации содержит `version` (наблюдалось 19), `visibilityType`, `isBanned`,
`commentsFlagState`, `modificationTime`, `content.modTime` и `articleContent.contentState`
длиной около 10 КБ [C]. `version` выглядит кандидатом для оптимистичной блокировки, но
её семантика [U]. Вся страница статьи весит около 3.2 МБ [C], хранить её целиком в
SQLite не стоит.

Ответ комментариев даёт `rootCommentsCount`, `childCommentsCount`,
`totalCommentsCount`, `commentsVisibility`, а на комментарий: `state`, `isPinned`,
`isInappropriate`, `reactions` [C]. У ответа есть `rootCommentId` и `replyToCommentId`,
причём второе поле текущая схема не сохраняет отдельной колонкой [C]. Поля времени
редактирования не наблюдалось [C]. Курсор `moreItemsQueryString` содержит
`fetchAfterCommentId` и `sorting` [C].

## 2. Долговечные коллекции

Общие правила: ключ строится на ID провайдера, строки не удаляются, исчезновение
фиксируется только после завершённого обхода коллекции. Всё приватное ниже [U] до
подтверждения; наблюдаемое есть только для публичной поверхности.

**Опубликованные статьи.**
- Идентичность: `publication_id` (hex). Slug выводим из него [C]. Канал идентифицировать по `publisher_id`, nickname хранить как изменяемый атрибут [I].
- Курсор: публичный `page=1..pageCount` [C]. Смещение плавает при появлении новых публикаций, поэтому чекпойнт номера страницы ненадёжен [I]; в `sort_type_menu` есть `regular`, `reversed`, `top` [C], параметр запроса для них [U].
- Изменчивость: заголовок, тело, `version`, `modificationTime`, счётчики.
- Перекрытие: полный проход ленты за прогон; детальный запрос только при изменении `modificationTime`, `version` или счётчиков. Счётчики просмотров, лайков и комментариев уже есть в элементе ленты [C], это дешёвый детектор изменений.
- Хранение: контент публичный, но raw содержит поля-токены, значения которых логировать нельзя [C: имена `comments_token`, `commentsToken`].
- Доказательство полноты: дошли до `pageCount` без ошибок, а все элементы известного типа обработаны. Соответствует ли `pageCount` всей истории канала [U].
- До подтверждения [U]: есть ли авторизованный список с курсором и фильтром по статусу.

**Черновики.**
- Идентичность: вероятно тот же `publication_id` с момента создания [I], проверить, не меняется ли ID при публикации [U].
- Курсор, порядок и сортировка [U].
- Изменчивость: самая высокая, автосохранение редактора может создавать ревизии конкурентно с CLI [I].
- Перекрытие: всегда полный проход списка; нельзя полагаться на инкремент, пока нет доказанного `updated_at` с убывающей сортировкой [I].
- Хранение: **приватные данные**, вне snapshot и публичных экспортов.
- Полнота: счётчик студии, если он есть, плюс конечный курсор [U].

**Запланированные публикации.**
- Идентичность: как у черновика [I]; отдельная коллекция ли это или статус [U].
- Изменчивость: переход в опубликованные происходит на стороне Дзена, поэтому запись может исчезнуть из одного списка и появиться в другом [I].
- Правило: исчезновение из `scheduled` не является удалением, пока не проверен `published`. Время `scheduled_at` и таймзона [U].

**Удалённые и скрытые.**
- Идентичность: тот же `publication_id`; состояние хранить в наблюдениях.
- Публичная поверхность показывает только `isBanned`, `visibilityType` [C]. Есть ли корзина, срок хранения и восстановление [U].
- Правило: `missing_since` ставится лишь после завершённого обхода; `deleted_observed_at` только при явном сигнале или подтверждённой прямой проверке (404 в ленте после прохода сам по себе не доказывает удаление) [I].
- Хранение: последняя сохранённая ревизия остаётся в архиве; нужна команда явной очистки [I].

**Ревизии.**
- Идентичность: `(publication_id, content_sha256)`; `version` провайдера как атрибут.
- Курсора нет: ревизии появляются из наших наблюдений. Предоставляет ли Дзен историю версий [U].
- Полнота честно ограничена: «наблюдённые при sync и pull», пропуски между синхронизациями неизбежны.
- Существующий `articles.raw_json` уже содержит `contentState`, так как парсер сохраняет публикацию целиком (`internal/dzen/parser.go:46-47`) [C], значит первая ревизия заполняется миграцией.

**Комментарии и ответы.**
- Идентичность: `comment_id` (сейчас 10-значное число, хранить текстом) и `document_id`; `rootCommentId` и `replyToCommentId` для дерева [C].
- Курсор: `fetchAfterCommentId` внутри `moreItemsQueryString`, сортировки `top`, `newest`, `oldest` [C]. Курсор непрозрачен по контракту: передавать, не конструировать [I].
- Рекомендация: backfill по `oldest`, инкремент по `newest` до N подряд известных записей старше водяного знака [I]; монотонность ID относительно времени [U]. Ответы обходить заново при изменении `childrenCount` родителя.
- Поздние и меняющиеся: лайки, `state`, закрепление, скрытие, удаление; времени редактирования нет [C], поэтому изменения ловятся только хэшем текста.
- Перекрытие: стартовое предложение: повторно обходить публикации, у которых изменился счётчик в ленте, и всё за последние 30 дней; остальное раз в неделю [I].
- Хранение: **персональные данные третьих лиц**; см. авторов.
- Полнота: сравнение корневых и дочерних записей с `rootCommentsCount` и `childCommentsCount` раздельно [C поля]. Счётчик без полученного значения означает «неизвестно», а не ноль. Входит ли скрытое в счётчики [U]. Видит ли владелец канала модерируемое и удалённое [U].

**Авторы комментариев.**
- Идентичность: `authorSafeUid`/`uidSafe`; `publicId`, `displayName`, `avatarLink` и `publisher` изменяемы [C поля].
- `authorUid` в архив как ключ не выносить, если хватает safe-идентификатора [I].
- Курсора нет, собираются из `usersById` страниц.
- Хранение: обязательна удаляемость по автору и исключение из любого публичного share [I]. Юридическая оценка вне этого отчёта.

**Снимки статистики.**
- Идентичность: `(publication_id, scope, bucket)`, где `scope` это `public` или `studio`.
- Текущая схема перезаписывает счётчики (`internal/store/store.go:87`) [C], история теряется.
- Публичные снимки append-only и привязаны ко времени наблюдения. Данные студии (доход, дочитывания, источники трафика), их гранулярность, глубина окна и задержка финализации [U].
- Перекрытие: если гранулярность дневная, перечитывать последние дни, так как цифры могут дозревать [I].
- Полнота: только в рамках запрошенного окна.

**Бинарные ассеты.**
- Идентичность: SHA-256 содержимого; `provider_image_id` и URL как внешние ссылки. Блок `atomic:image` содержит `data.image.id` [C]; в HTML наблюдались изображения с хоста `avatars.dzeninfra.ru` [C].
- Очередь догрузки вычисляется как «ссылка есть, байт нет» (`diacrawl/docs/architecture.md:67-71`).
- Изменчивость: контент неизменяем, ссылки могут устаревать [I].
- Обязательные поля: размер, media type, результат попытки, включая постоянную недоступность, чтобы не повторять бесконечно (`diacrawl/docs/private-web-api.md:106-108`).
- Как узнать URL оригинала по `image.id`, ограничения загрузки [U].

## 3. Разделение live и archive

Правило простое: **архив отвечает на вопросы, живой API принимает решения.** Любая
операция, результат которой уходит в Дзен, читает базу у Дзена, а локальные запросы
никогда не аутентифицируются и не ходят в сеть (ровно этот контракт зафиксирован в
`exchangecrawl/SPEC.md:54-56`).

| Режим | Операции |
| --- | --- |
| Всегда live | Получение черновика для правки; создание, изменение, публикация, планирование, снятие и удаление; загрузка изображений; ответ, удаление, закрепление и скрытие комментария; бан автора; `auth status`; проба возможностей; проверка актуальности перед записью |
| Только локально | `search`, `semantic-search`, `sql`, `status`, `coverage`, `export-md`, тренды статистики, выборка необработанных комментариев |
| Гибридно, с явным выбором | `draft list` по умолчанию live с записью увиденного в архив; `article list` и `comment list` по умолчанию локально, с `--live`; ответ всегда содержит `as_of` и признак полноты |

Live-чтения пишут результат в архив как наблюдение по принципу write-through, но сбой
этой записи не должен ломать пользовательскую операцию [I]. После успешной мутации
обязательно живое чтение для подтверждения, затем запись наблюдения. Перед мутацией
комментария его наличие и состояние проверяются вживую: локальная копия могла устареть.

**Рабочая копия черновика.** Формат файла принадлежит другому треку; здесь важно только
её положение относительно архива. Рабочая копия это каталог с Markdown, изображениями и
небольшим `.dzen/base.json`, расположенный там, где работает агент, или по умолчанию в
`~/.dzencrawl/workspaces/<publisher_id>/<publication_id>/` с правами `0700`/`0600`.
В `~/.dzencrawl/articles/` её держать нельзя: этот каталог полностью генерируется
экспортом, а `render` дописывает секцию комментариев к телу статьи
(`internal/markdown/export.go:55`) [C], и такой файл нельзя отправлять обратно.

Связь строится как трёхсторонняя сверка:

```text
base    неизменяемая ревизия в архиве: publication_revisions.content_sha256 + provider version
local   файлы рабочей копии, правит агент
remote  живое состояние, читается непосредственно перед записью
```

`pull` записывает `base` и файлы. `push` читает `remote` и сравнивает с `base`; при
расхождении останавливается с конфликтом, а не перезаписывает. `sync` может обновить
архив, но никогда не трогает `local` и `base.json`. Архив хранит лишь таблицу-указатель
`working_copies` (путь, publication, базовая ревизия), поэтому удаление рабочей копии
ничего не ломает, а архив остаётся журналом наблюдений, а не редактором.

## 4. crawlkit v0.13.4

### Инвентарь

| Пакет | Публичный API и назначение | Нужно ли здесь |
| --- | --- | --- |
| `store` | `Open(Options{Path,Schema,SchemaVersion,MaxOpenConns,MaxIdleConns})`, `OpenReadOnly`, `DB`, `Path`, `WithTx`, `EnsureSchemaVersion`, `SchemaVersion`, `Query`, `QuoteIdent`, `EscapeLike`, `FTS5Phrase`, `FTS5Terms`, `FTS5TokenQuery`, `OptimizeFTS5` | Да |
| `state` | `Store` (`Set`, `Get`, `IsStale`), `ScopedStore`, `CursorStore`, варианты с mapping и clock, `EnsureSchema`, `EnsureCursorSchema`, `EnsureScopedSchema`, `ManifestKey` | Да, `CursorStore` |
| `control` | `crawlkit.control.v1`: `Manifest`, `Status`, `Command{Argv,JSON,Mutates,...}`, `Privacy`, `Freshness`, `Database`, `Count`, `SQLiteDatabase`, `RemoteDatabase`, contact export | Да |
| `config` | `App` (пути, `ResolveConfigPath`, платформенные каталоги), `LoadTOML`, `WriteTOML`, `ExpandHome`, `RuntimeConfig`, `TokenDiagnosticForEnv` | Да |
| `output` | `Format`, `Resolve`, `Write`, `UsageError`, `IsUsage` | Частично, конверт ошибок свой |
| `embed` | `NewProvider`, `CheckProvider`, `Provider`, `IsRateLimitError`, OpenAI-compatible/Ollama/llama.cpp | Да |
| `vector` | `Search`, `EncodeFloat32`, `DecodeFloat32`, `TopK`, `ReciprocalRankFusion`, `CosineSimilarity` | Да |
| `snapshot` | Экспорт и импорт JSONL/Gzip, `RowFilter`, `SyncSidecarTree`, планы инкрементального импорта | Позже, с фильтрами приватного |
| `backup` | Шифрованные age-шарды, манифесты, restore | Предпочтительно для приватного архива |
| `mirror` | Git-операции для snapshot-репозиториев | Только для зашифрованного или публичного |
| `scheduler` | `Discover` (через `metadata --json`), jobs, locks, история, launchd/systemd/cron; `cmd/crawlctl` | Да, косвенно |
| `progress` | `Tracker`, `Percent`, `Completion` | Да, в stderr |
| `tui` | Общий терминальный браузер архива | Опционально |
| `remote` | Клиент Worker-архивов, токены, SQLite-бандлы | Нет |
| `cache` | Безопасные снимки локальных файлов и SQLite | Нет |
| `releasecheck` | Проверка обновлений | Опционально |

### Что чьё

Источник границы: `crawlkit@v0.13.4/docs/boundary.md:7-10,26-64,68-87` [C]. crawlkit владеет
механикой: подключением SQLite, транзакциями, путями config, записью курсоров,
control-контрактом, embedding и вектором, snapshot, планировщиком. Провайдерский адаптер
владеет всем остальным: HTTP, cookies, сессиями, политикой лимитов, нормализацией,
схемой, CLI-контрактом, FTS-телами и фильтрами приватности (`docs/boundary.md:147-148`).

`CursorStore.Set` принимает `execQuerier`, которому удовлетворяет `*sql.Tx`
(`crawlkit@v0.13.4/state/state.go:35-38`) [C], поэтому страница и курсор коммитятся атомарно.

### Пробелы

В crawlkit нет шагового раннера миграций: `EnsureSchemaVersion` только записывает номер
(`store/store.go:134-160`) [C], поэтому `exchangecrawl` держит свой
(`internal/archive/migration.go:15-54`) [C]. Нет HTTP-клиента, rate limiter, retry,
cookie или session-примитивов, content-addressed ассетов, учёта прогонов (только JSONL
планировщика), модели degraded: `Status.State` это свободная строка
(`control/control.go:63`) [C].

`control.Command.Mutates` не различает локальную мутацию и запись в провайдера
(`control/control.go:44-51`) [C]; `sync` помечается `Mutates`, а публикация статьи
выглядела бы так же. Пока этого нет, команды записи в Дзен лучше не описывать
в манифесте, который видит планировщик, либо разделить бинарники (раздел 5).

В v0.13.4 схема применяется до проверки версии (`store/store.go:60-71`) [C], исправлено в
v0.15.0.

### Стоит ли обновляться

Да. Последний тег v0.17.4 от 2026-11-06 [C: `gh api repos/openclaw/crawlkit/tags`,
releases]. Между версиями: исправление повреждения при откате журнала в SQLite 3.53.3
(v0.14.7), проверка версии схемы до применения и безопасные пересборки snapshot и backup
(v0.15.0), завершение с I/O-ошибкой вместо падения при сбое WAL (раздел 0.16.8 в changelog, отдельного тега в списке нет), отмена ожидания
блокировки записи по контексту (v0.17.3), сборка с исправлением `crypto/x509` (v0.17.2) [C:
`CHANGELOG.md` тега v0.17.4]. Новый пакет `worker` с очередями, lease и fencing появился в
v0.16.0 и может пригодиться позже для догрузки ассетов [I], сразу он не нужен.

Цена обновления: v0.14.8 и новее требуют Go 1.27.0, а репозиторий и локальный toolchain на
1.26.5 [C]. На копии репозитория `go get crawlkit@v0.17.4` с `GOTOOLCHAIN=auto`, сборка,
`vet` и тесты прошли [C]; сигнатуры экспортируемых функций и типов в `store`, `state`,
`control`, `config`, `output` и поля `control.Command` совпали с v0.13.4 [C].
Рекомендация: отдельный коммит с обновлением Go и crawlkit до начала работы над схемой.

## 5. Форма продукта, схема, миграции, auth

### Один бинарник или другой инструмент

| Вариант | Плюсы | Минусы |
| --- | --- | --- |
| Оставить только `dzencrawl` и добавить запись | Нет переходного периода, один путь | Имя и документы обещают read-only (`SPEC.md:5,52-54`, `README.md:119-120`); фоновое задание с живой сессией работает в процессе, умеющем публиковать |
| Переименовать всё в `dzen` | Лучшая форма для агента | Ломает manifest id, пути и связь с `crawlctl`; та же проблема с фоновой записью |
| **Один модуль, две точки входа** | `cmd/dzencrawl` не импортирует код записи, что проверяется статически; плановый sync с сессией не может опубликовать статью; агент получает единое дерево `dzen` | Два тонких `main.go`, общий data root надо описать |

Рекомендую третий вариант. Это проверенная форма «две программы, один глубокий модуль»
из `exchangecrawl/SPEC.md:5-13`. `dzencrawl` сохраняет `sync`, `status`, `search`,
`export-md`, `metadata` и остаётся приложением с `id: dzencrawl` для `crawlctl`. `dzen`
предоставляет `auth`, `draft`, `article`, `comment` плюс локальные запросы и использует
тот же архив и `~/.dzencrawl`. Инвариант закрепляется тестом на граф импортов.
Точные имена команд принадлежат CLI-треку; важна именно линковочная граница.

### Предлагаемая схема

Набросок, а не окончательный DDL. Поля статусов намеренно свободнее там, где приватный
контракт [U].

```text
archive_metadata(key, value)                    -- application_id, format
accounts(account_id, created_at)                -- локальный alias, без логинов
channels(publisher_id pk, nickname, title, url, account_id?, first_seen_at, last_seen_at)
channel_aliases(publisher_id, nickname, first_seen_at, last_seen_at)

raw_payloads(sha256 pk, media_type, bytes, body blob)
raw_observations(id pk, run_id, source, collection, entity_type, entity_id,
                 payload_sha256, json_pointer, http_status, contract_version, observed_at)

publications(publication_id pk, publisher_id, item_type, slug, title, lifecycle,
             provider_state, visibility, provider_version, published_at, scheduled_at,
             modified_at, first_seen_at, last_seen_at, missing_since, deleted_observed_at,
             current_revision_sha)
publication_observations(publication_id, observed_at, lifecycle, state_fingerprint,
                         observation_id, unique(publication_id, observed_at, state_fingerprint))
publication_revisions(publication_id, content_sha256, content_format, content blob,
                      provider_version, title, markdown, renderer_version, source,
                      observed_at, primary key(publication_id, content_sha256))

comments(comment_id pk, publication_id, document_id, root_comment_id, reply_to_comment_id,
         author_safe_uid, text, created_at, state, is_pinned, is_inappropriate,
         likes, dislikes, first_seen_at, last_seen_at, missing_since, deleted_observed_at)
comment_observations(comment_id, observed_at, text_sha256, state, observation_id)
comment_authors(author_safe_uid pk, public_id, display_name, first_seen_at, last_seen_at)

stats_snapshots(publication_id, scope, bucket, observed_at, views, views_till_end, likes,
                comments_count, observation_id, primary key(publication_id, scope, bucket))

assets(sha256 pk, bytes, media_type, width, height, rel_path, first_seen_at)
asset_refs(owner_type, owner_id, role, provider_image_id, source_url, sha256?, last_attempt_at,
           attempt_state, primary key(owner_type, owner_id, role, provider_image_id))

sync_runs(id pk, source, collection, scope_id, status, result, pages, records,
          error_sanitized, started_at, finished_at)
working_copies(path pk, publication_id, base_revision_sha, updated_at)
mutation_journal(op_id pk, account_id, kind, target_id, base_revision_sha, request_sha256,
                 status, provider_ref, created_at, updated_at)
```

Тело `raw_payloads` хранит исходные байты ответа, а не `json.Marshal` разобранной карты:
нынешний путь для комментариев проходит через float64 (`internal/dzen/client.go:156`,
`internal/dzen/parser.go:116`) [C]. Для страниц статей сохранять только извлечённый JSON
публикации, а не 3.2 МБ HTML [C размер]. Raw считается чувствительным целиком: сервер
может включать персональные поля, а в авторизованных ответах они есть почти наверняка
(`currentUserReaction`, `hasCurrentUserComplain` присутствуют в публичной форме) [C
поля]; угадывать секреты внутри raw не следует (`diacrawl/docs/architecture.md:107-109`).

**Ассеты** хранятся под `assets/sha256/<prefix>/<digest>` вне SQLite, атомарной публикацией
через временный файл и `link`, с правами `0700`/`0600`, как в
`diacrawl/internal/assets/store.go:71-129`. Строка в БД появляется только после того, как
байты записаны надёжно, поэтому отсутствующие строки образуют идемпотентную очередь.

**Чекпойнты по коллекциям.** Использовать `state.NewCursor` и таблицу `sync_cursor_state`:
`source` это `public` или `account:<id>`, `entity_type` это коллекция, `entity_id` это
`publisher_id` или `publication_id`. Значением служит JSON
`{cursor, complete, seen, high_water_at, contract_version}`, поскольку пустой курсор не
должен означать «нет чекпойнта» (`diacrawl/docs/architecture.md:32-37`,
`diacrawl/internal/archive/sync.go:202-235`). Коллизия D7 при этом не возникает.

**Degraded и возобновление.** Страница и её курсор коммитятся одной транзакцией
`store.WithTx`. Новый прогон той же области атомарно помечает предыдущий как `superseded`
(`diacrawl/internal/archive/sync.go:44-52`). Сбой одной статьи или ветки комментариев
записывается и не прерывает проход; водяной знак двигается только после полного успеха
(`exchangecrawl/SPEC.md:128-138`). Итог `degraded` даёт ненулевой код выхода и
`Status.State="degraded"` с редактированными предупреждениями
(`exchangecrawl/internal/archive/sync.go:60-65,108-110`). Цикл обязан отвергать повторы
курсора (`diacrawl/internal/crawl/runner.go:118-126`), а 429 и 5xx повторять с backoff
только для чтений (`diacrawl/internal/crawl/runner.go:182-188`). Лимит 1500 мс глобальный на
аккаунт и хост, с файловой блокировкой, так как нынешний запрет параллельных синхронизаций
есть только в README (`README.md:128-129`) [C].

### Подход к миграциям

Существующая БД без `schema_migrations` считается версией 1, если есть таблица `articles`,
и нулевой, если БД пуста. Раннер шагов по образцу
`exchangecrawl/internal/archive/migration.go:15-54` выполняется одной транзакцией, отказывает
при версии новее поддерживаемой и пишет номер только в конце. Перед первой миграцией
делается `VACUUM INTO` рядом с БД [I, не тестировалось].

Шаг на v2: создать новые таблицы; перенести `articles` в `publications`, переведя
`raw_json` в `raw_payloads` и первую ревизию; перенести комментарии без удаления; собрать
`channels` по `publisher_id`; создать `sync_cursor_state`; создать view `articles` для
совместимости с `sql`. Старую таблицу `sync_state` не удалять в этом же шаге, а переименовать
в `legacy_sync_state`. Каскадного удаления комментариев и `delete` при upsert в новой
модели нет. Обязательны тесты миграции на фикстуре старой БД, как
`exchangecrawl/internal/archive/migration_test.go`.

### Разделение auth, crawler и renderer

Цель: cookies не доходят ни до архива, ни до рендера, а crawler не умеет писать. Набросок
интерфейсов:

```go
// internal/session: единственный владелец cookies и CSRF.
type Broker interface {
    WithSession(ctx context.Context, account Account, fn func(Transport) error) error
    Status(ctx context.Context, account Account) (State, error)
}

// Transport: same-origin, общий лимитер, ошибки без тел и заголовков.
type Transport interface {
    Do(ctx context.Context, req *Request) (*Response, error)
}

// internal/dzen: типизированные адаптеры, Reader есть в публичной и приватной версиях.
type Reader interface {
    ListPublications(ctx context.Context, q ListQuery) (PublicationPage, error)
    GetPublication(ctx context.Context, id PublicationID) (RawPublication, error)
    ListComments(ctx context.Context, q CommentQuery) (CommentPage, error)
}
type Writer interface { // импортируется только из cmd/dzen
    Apply(ctx context.Context, m Mutation) (MutationOutcome, error)
}

// internal/render: чистые функции, без сети и файлов.
type Renderer interface {
    ToMarkdown(ContentState) (Document, []Loss, error)
    FromMarkdown(Document, AssetMap) (ContentState, []Diagnostic, error)
}
```

`internal/crawl` получает `Reader` и `Archive`, ни о `Broker`, ни о `Writer` не зная.
Рендер не загружает изображения: оркестратор загружает их до вызова и передаёт `AssetMap`.
Crawler-UA с пометкой `Googlebot-compatible` (`internal/config/config.go:57`) относится
только к публичному источнику и в авторизованный транспорт переноситься не должен [I].

## 6. Паттерны из референсных проектов

**Session broker.** `reg-ru-cli/internal/provider/portal/session/broker.go:21-110`:
адаптеры зависят от узкого `WithSession`
(`reg-ru-cli/internal/provider/support/portal.go:19-21`), а профиль защищён эксклюзивной
блокировкой и кодом `portal_profile_busy`. `diacrawl/docs/architecture.md:100-101` добавляет
важную деталь: блокировка держится на всё время загрузки, HTTP и атомарного сохранения,
иначе устаревший процесс затрёт обновлённые cookies. Взять целиком.

**Staged login.** `reg-ru-cli/docs/cli-contract.md:111-115`,
`reg-ru-cli/CONTEXT.md:56-61`: новая попытка живёт в отдельном профиле и заменяет
действующую сессию только после успеха и совпадения принципала; отмена, таймаут и несовпадение
оставляют прежнюю сессию. Состояния `not-established`, `active`, `session-lost` намеренно
не угадывают причину (`cli-contract.md:130-132`).

**credential_process.** `reg-ru-cli/docs/profile-secret-contract.md:6-55`,
`beget-cli/docs/AUTHENTICATION.md:44-69`: argv без shell, ленивый запуск не более раза за
вызов, строгий JSON со `schemaVersion`, лимит 64 КиБ и 30 секунд, stderr отбрасывается,
ошибки только кодами. Для Дзена подходит как способ получить логин и пароль, но сама
сессия, вероятно, браузерная [I], поэтому основной путь это staged login, а
`credential_process` дополнение. Проектный config может только выбирать alias
(`reg-ru-cli/docs/profile-secret-contract.md:97-105`); это защита от вредоносного checkout,
особенно актуальная для агентов.

**Drift-пробы.** В `reg-ru-cli/docs/cli-contract.md:90-99` пробы ограничены по времени, сбой
одной не скрывает другие, а тела ответов отбрасываются. Код выхода 8 отведён под дрейф
приватного контракта, и операция не выполняется, если проба провалена
(`reg-ru-cli/internal/cli/errors.go:345`). Разделение `configured` и `verified`
(`reg-ru-cli/CONTEXT.md:16-19`) ровно то, что нужно для недокументированного API Дзена. Для
sync достаточно шейп-проверки ответа с записью `contract_version`.

**outcome_unknown.** `reg-ru-cli/CONTEXT.md:28-33`: четыре исхода `committed`, `rejected`,
`not-sent`, `outcome_unknown`; неоднозначная мутация не повторяется вслепую. Код:
`reg-ru-cli/internal/cli/errors.go:351`, транспорт
`reg-ru-cli/internal/provider/cloudvps/client.go:705,727,749`, ошибка
`reg-ru-cli/internal/provider/cloudvps/types.go:321`. `beget-cli/docs/CLI_SPEC.md:38-39`
подтверждает контракт «ровно одна отправка, код 7». Для Дзена `mutation_journal` и обязательное
живое чтение превращают `outcome_unknown` в разрешимое состояние. Ключа идемпотентности у
Дзена может не быть [U], поэтому повторная отправка без сверки запрещена.

**`metadata --json` и `crawlkit.control.v1`.** `exchangecrawl/internal/cli/manifest.go:21-43`
показывает полный манифест. `diacrawl/internal/cli/control.go:31-35` решает дефект D8: `sync`
появляется в манифесте только когда известна цель. Для приватного режима выставить
`ContainsPrivateMessages: true` и локальные области, как в
`diacrawl/internal/cli/control.go:22-26`.

**Прочее.** Конверт `schemaVersion/ok/command/data|error`, коды выхода, stdout для данных и
stderr для диагностики (`reg-ru-cli/docs/cli-contract.md:181-254`); append-only наблюдения с
fingerprint (`exchangecrawl/internal/archive/schema.go:38-51`) и независимые потоки с
собственным перекрытием (`exchangecrawl/internal/source/source.go:58-67`); отчёт о покрытии с
явным списком не собираемого и точным смыслом полноты (`diacrawl/docs/coverage.md:29-58`);
редактирование диагностики (`diacrawl/internal/archive/lifecycle.go:11,48`); термин
«Living Archive», сохраняющий наблюдённые изменения и удаления, и граница «back office» в
`vkcrawl/CONTEXT.md:27-37`, полезная при решении, какие данные студии вообще собирать.

## 7. Что осталось неизвестным

Эти факты блокируют окончательные решения о схеме и должны прийти из приватного трека:

1. Списки черновиков, запланированных, скрытых и удалённых: endpoint, курсор, порядок, фильтры, счётчики.
2. Меняется ли `publication_id` при публикации; семантика `version` и требуется ли он при сохранении.
3. Формат записи: тот ли draft.js `contentState`, что отдаёт чтение, и какие блоки допустимы.
4. Есть ли у Дзена история ревизий и корзина со сроком восстановления.
5. Что видит владелец канала в комментариях: скрытые, удалённые, модерируемые; значения `state`; входят ли они в счётчики.
6. Статистика студии: поля, гранулярность, окно, финализация.
7. Получение оригинала по `image.id`, правила загрузки, лимиты.
8. Модель сессии: cookies, CSRF, время жизни; есть ли у мутаций идемпотентность и лимиты.
9. Отдаёт ли публичная лента всю историю канала, или `pageCount` усечён; параметр выбора `sort_type`; адресация канала по ID в launcher.

## 8. Рекомендуемая последовательность

1. Обновить Go и crawlkit отдельным коммитом.
2. Закрыть D1, D2, D3, D4: это потеря данных и ложные сигналы, без связи с новыми функциями.
3. Ввести раннер миграций и схему v2 с ревизиями из существующего `raw_json`.
4. Ввести `sync_runs`, чекпойнты и degraded; исправить манифест и статус.
5. Записать фикстуры публичных ответов и контрактные тесты.
6. После подтверждения приватного API добавить `Broker`, приватный `Reader`, затем рабочие копии и `Writer` с журналом.
