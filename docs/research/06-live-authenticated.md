# 06. Живая авторизованная поверхность Дзена

Дата: 2026-11-09. Продолжение 02, 04 и 05: живые запросы из изолированной agent-browser сессии с куками dzen.ru / yandex.ru, снятыми через CDP `Storage.getCookies` из настоящего Chrome пользователя. Прямое CDP к Chrome с ~55 вкладками зависало (два попытки, таймаут 30 с); дальше использовалась изолированная сессия со своим профилем. Идентификаторы, учётная запись, названия канала и текст тестовых материалов удалены. Метки: **Observed live** — ответ или запрос из авторизованной сессии; **Derived from bundle** — строка из studio/editor/comments bundle; **Unknown** — не установлено.

## Итог и граница прохода

Principal подтверждён до мутаций: `GET /editor-api/v2/user` вернул ожидаемый аккаунт (`authProvider:"yandex"`, совпадение `user.publisherId` с `publisher.id`). Созданы тестовые публикации с префиксом заголовка `dzen-cli-test` (черновики A/B/C и промежуточные). В конце все тестовые удалены; read-back `GET get-publication` → HTTP 404; в списках `draft` / `pending` / `published` остались только preexisting welcome-черновики. **Observed live**

Охват: создание/редактирование/настройки статей, изображения, lifecycle (schedule/publish/delete/remove-unpublished), session replay, частичные комментарии. Вне scope: статистика, pinning, коллекции, premium, ads/promo, monetization, чужой контент. Полный comments write-lifecycle не завершён: `create-comment` стабильно отдавал `HttpStatusCodeException`, пока у тестовой статьи не было `commonUrl` (публичные URL 404; в студии статья уже в `published`).

## 1. Поток записи студии

### `add-publication`

`POST /editor-api/v2/add-publication` с JSON-телом `{title, publisherId, publicationType}` (для статьи `publicationType` соответствует article). Ответ — полный `Publication`: `id`, `publisherId`, `content.type:"article"`, `content.articleContent.contentState:""`, `content.preview {title,snippet,galleryPreviewImages}`, `version:0`, `privateData.status:"draft"`. **Observed live**

### Save: тело, поля, `fp` / `X-FP-Token`

Студийный save (перехват сети редактора) и API-эквивалент:

- URL: `POST /editor-api/v2/update-publication-content?publisherId=<id>&clientRid=<rid>`
- Заголовки студии: `Content-Type: application/json`, `Accept: application/json`, `X-Csrf-Token`, `X-FP-Token`
- Тело (ключи): `id`, `preview {title,snippet,galleryPreviewImages}`, `snippetFrozen`, `hasNativeAds`, `commentsFlagState`, `delayedPublicationFlagState`, `visibleComments`, `visibilityType`, `premiumTariffs`, `articleContent {contentState}`, `tagsInput {tags,detectedTagsShown}`, `fp`

`contentState` на записи — **строка** JSON плоского Draft.js `{blocks, entityMap}`, не обёртка `draftJsState`. На чтении v2/`publisher/.../publication/...` после save также плоский объект/строка с `blocks` (обёртка `draftJsState` с публичного SSR сюда не приходит). Запись с обёрткой `draftJsState` принимается (`result:"OK"`), но при чтении содержимое оказывается flattened/flat — писать нужно плоский `{blocks,entityMap}` как строку. **Observed live**

`fp` и `X-FP-Token`: студия шлёт оба. Живые пробы: save без `fp` и без `X-FP-Token` → 200 `OK`; только body `fp` → OK; только header → OK; dummy `fp` → OK. Для CLI `fp` / `X-FP-Token` **не обязательны**. CSRF обязателен: без `X-Csrf-Token` → 400 `invalid-csrf-token-error` / `InvalidCsrfTokenException`. CSRF берётся из `GET /media-api/csrf-token` → `{result:string}`. **Observed live**

No-op save (то же тело без смысловых правок): 200 `{result:"OK"}`, `version` и `content.modTime` всё равно растут. **Observed live**

### Блоки и стили

Приняты и сохранились при read-back: `unstyled`, `header-two`, `header-three`, `marker`, `atomic:image`; inline `BOLD`, `ITALIC`, `UNDERLINE`; caption у `atomic:image` с `BOLD` в `inlineStyleRanges`. **Observed live**

Отклонены HTTP 500 `internal-error` (часто `java.util.NoSuchElementException`); предыдущее содержимое сохраняется: `header-one` (H1), `header-four` (H4), `STRIKETHROUGH`, `depth>0`, неизвестный `type` блока. **Observed live**

Форма ошибки валидации на reuse чужого image id: HTTP 400 `{errors:[{type:"validation-error", message:"...InputValidationException"}]}`. Ошибки формата блоков чаще приходят как 500 `internal-error`, не как структурированная schema-ошибка. **Observed live**

### `remove-unpublished-changes`

`POST /editor-api/v2/remove-unpublished-changes?publicationId=<id>&publisherId=<id>&clientRid=<rid>` (пустое тело). Ответ `{result:"OK"}`. После правки опубликованной статьи `draftStatus` сбрасывается с `has-unpublished-changes` на `no-changes`, текст возвращается к опубликованному. Тело с JSON `id`/`publicationId` без query — 400. **Observed live**

## 2. Конкурентность

Поле `version` есть на чтении и растёт после каждого успешного save. Передача `version` в теле save **принимается**, но **не защищает**: stale `version:0` при актуальной версии N → всё равно 200 `OK`, версия становится N+1. Два последовательных save подряд — оба OK, last-write-wins. Серверного optimistic locking нет. **Observed live**

Открытый редактор + API-save: после API-save у черновика остаётся/появляется `has-unpublished-changes` относительно UI-состояния; точный cadence autosave студии не измерен (интервал таймера **Unknown**). CLI обязан сам сверять base/local/remote по `version`/`modTime`/хешу `contentState`. **Observed live** (эффект), **Unknown** (каденс).

## 3. Списки и состояния

`GET /editor-api/v3/publications?publisherId=<id>&state=<state>&pageSize=<n>&view=list`.

Рабочие `state`: `draft`, `pending`, `published` → HTTP 200. В ответе всегда ключи `drafts`, `publications` (и служебные counters/suites/pins). Для `draft` элементы лежат в `drafts`; отложенные попадают в `pending` (и в `drafts`, и в `publications` списка pending); сразу опубликованные — в `published`. **Observed live**

Прочие значения (`delayed`, `processing`, `banned`, `scheduled`, `awaiting`, `moderation`, `rejected`, `deleted`, `has-unpublished-changes`, `ready`, `all`, …) → HTTP 400 `{type,message}` с `internal-error`. **Observed live**

Пагинационный cursor в ответах не найден (`cursorFields` пуст). При `pageSize=1` возвращается один элемент без `next`/`cursor`/`hasMore`. Как листать дальше больших каналов — **Unknown**.

`privateData.status` / `draftStatus` на объекте: `draft` | `published`; `has-unpublished-changes` | `no-changes`. Отложенная: `status:"published"` + `delayedPublicationFlagState:"on"`.

## 4. Изображения

`POST /editor-api/v2/add-image` — multipart, поле файла `image` (**Derived from bundle** `FormData.append("image", …)`; live upload успешен). Ответ: `{id, namespace:"zen_doc", groupId:number, imageName:string, meta:{mainColor,origFormat}, sizes:{...}}`. **Observed live**

`POST /editor-api/v2/add-image-from-url` — тот же shape ответа. **Observed live**

Обложка: `preview.image.id` = id загруженного изображения; совпадает с upload id после save. Блок: `type:"atomic:image"`, `data.image.id`, подпись в `text` + `inlineStyleRanges`. **Observed live**

Reuse id изображения с публикации A на B (atomic + cover): HTTP 400 `validation-error` / `InputValidationException`; id не сохраняется. **Observed live**

## 5. Настройки публикации

Через то же тело `update-publication-content` / `...-and-publish`:

| Поле | Наблюдение |
|---|---|
| `preview.snippet` + `snippetFrozen:true` | Ручной snippet сохраняется; `snippetFrozen` читается `true`. **Observed live** |
| `snippetFrozen:false` | Snippet снова из текста. **Observed live** |
| `tagsInput.tags` на article | После save `tags` остаются `[]` — на статьях не персистятся (или игнорируются). **Observed live** |
| `commentsFlagState` | `"on"` / `"off"` сохраняются. **Observed live** |
| `visibleComments` | `"subscribe-visible"`, `"invisible"`, `"visible"` сохраняются. **Observed live** |
| `visibilityType` | Наблюдалось `"all"`; другие значения не перебирались. **Observed live** (частично) |
| `hasNativeAds` | `false` сохраняется; `true` не ставили (ads out of scope). **Observed live** |
| `customCommentsTitle` | Строка сохраняется и сбрасывается в `""`. **Observed live** |

## 6. Lifecycle

### Schedule / reschedule / cancel

Планирование: тот же publish-маршрут или save с `delayedPublicationFlagState:"on"` и `requestedPublishTime` в ISO-8601 UTC (`…Z`). На чтении: `delayedPublicationFlagState:"on"`, `requestedPublishTimeIso`, `publishTime` (epoch ms), `delayingParams {minDelayHours:1, maxDelayHours:8761}`, `privateData.status:"published"`, список — `state=pending`. Reschedule тем же полем на новое время — OK. **Observed live**

Отмена через save с `delayedPublicationFlagState:"off"` (и `requestedPublishTime:null`) **не применяет сразу**: остаётся `delayed:"on"`, появляется `has-unpublished-changes`. Фактический сброс плана — через `update-publication-content-and-publish` с `delayedPublicationFlagState:"off"` (или discard unpublished, если отмена ещё не опубликована как изменение). **Observed live**

### Publish now

`POST /editor-api/v2/update-publication-content-and-publish?...` с полным телом документа → `{result:"OK"}`. После: `status:"published"`, `draftStatus:"no-changes"`, `delayedPublicationFlagState:"off"`, статья в списке `published`. `commonUrl` может отсутствовать минутами; публичные URL по title/`/a/...` отдают 404, пока обработка не закончится. Отдельного API-статуса «В обработке» нет — в списках сразу `published`. Длительность появления `commonUrl` / публичной страницы — **Unknown** (в пробах за несколько минут URL не появился). **Observed live**

### Edit after publish

`update-publication-content` на опубликованной → `draftStatus:"has-unpublished-changes"`, `hasChanges:true`, `status` остаётся `published`. Публикация правок — снова `...-and-publish`; отказ от правок — `remove-unpublished-changes`. **Observed live**

### Delete

`POST /editor-api/v2/delete-publication?id=<publicationId>&publisherId=<id>` с **пустым телом** → 200 `{objectId:<id>}`. Последующий `get-publication` → 404 / `internal-error`. Из списков исчезает. JSON-тело вместо query не сработало (400). **Observed live**

## 7. Комментарии и бан

### documentId

`GET /api/comments/v2/root-comments?documentId=native:<publication.id hex>&publisherId=...` → 200, `status:"ok"`, ключи `items`, `meta`, `usersById`, `csrfToken`, … **Observed live**

`documentId=native:<itemId>` (десятичный) → 400: нужен hex ObjectId. Другие префиксы (`publication:`, `pub:`, `article:`, …) → 400. **Observed live**

### Create / lifecycle

При `status:"published"`, `commentsFlagState:"on"`, но **без** `commonUrl`: `POST /api/comments/v2/create-comment` → HTTP 400 `{errors:[{type:"internal-error", message:"ru.yandex.zen.common.HttpStatusCodeException"}]}` на всех вариантах тела (`publisherByChannel` true/false, root csrf, zen-headers). Captcha в ответе не было. Reply/edit/delete/restore/react/pin/unpin/dedupe **не установлены** этим проходом. **Observed live** (блокер create), остальное **Unknown**.

### Лента канала

`GET /editor-api/v2/publisher/<publisherId>/comments` → 200 `{comments, users, publishers, profiles, publications}`.  
`/api/comments/v2/publisher-comments?...` → 404; `/editor-api/v3/publishers/.../comments` → 404. **Observed live**

### Ban

`POST /api/comments/v2/ban-user/<uid>` (self) → 400 `internal-error` / `HttpStatusCodeException` (harmless reject). Успешный ban/unban чужого пользователя не проверялся. В бандлах: `/api/comments/v2/ban-user/:uid`, `unban-user`, studio также ` /api/comments/ban-user/:uidSafe` и `cancel-ban-user`. **Observed live** (self reject), **Derived from bundle** (контракт маршрутов).

## 8. Session replay вне браузера

Куки dzen.ru + yandex.ru, по одному значению на имя (~4.6 KiB compact Cookie header): `GET /editor-api/v2/user` и `GET /media-api/csrf-token` — 200; мутация save на тестовом draft — 200 `OK`. Полный dump всех cookie-записей → HTTP 431. Подмножества только `Session_id` (+ `i`, `yandex_login`, `yandexuid`, `zencookie`, sso) → 401. Минимальный достаточный набор имён целиком не сужен; рабочий набор — compact unique-by-name с доменов dzen/yandex. **Observed live**

Заголовки мутации: `X-Csrf-Token` обязателен; `Origin` / `Referer` / `X-FP-Token` / body `fp` для save **не обязательны** (проверено без Origin и без fp). User-Agent обычный браузерный использовался; необходимость конкретного UA не опровергнута. **Observed live**

Lifetime / IP / fingerprint binding: за сессию replay с той же машины работал; отдельной пробы с другого IP не было — **Unknown**. Cookie DB профиля Chrome (`Default/Cookies`): колонка `encrypted_value`; у всех просмотренных записей префикс plaintext blob — `v10` (864/864 в выборке). Keychain не трогался, расшифровки не было. **Observed live**

## Финальный контракт для implementer

### `push`

1. `GET /editor-api/v2/user` — проверить principal.  
2. `GET /media-api/csrf-token` → `X-Csrf-Token`.  
3. `GET .../get-publication` (или `GET /editor-api/v2/publisher/<pid>/publication/<id>`) — сохранить `version`, `content.modTime`, raw `contentState`. При расхождении с локальным base — остановиться (сервер last-write-wins).  
4. `POST /editor-api/v2/update-publication-content?publisherId&clientRid` с телом: `id`, `preview`, `snippetFrozen`, `hasNativeAds`, `commentsFlagState`, `delayedPublicationFlagState`, `visibleComments`, `visibilityType`, `premiumTariffs`, `articleContent.contentState` = **JSON.stringify({blocks, entityMap})**, `tagsInput`. `fp`/`X-FP-Token` опциональны.  
5. Не слать H1/H4/STRIKETHROUGH/`depth>0`/unknown block types.  
6. Auth: compact cookie set dzen+yandex + CSRF.

### `publish`

Тот же документ через `POST /editor-api/v2/update-publication-content-and-publish?...`. Считать accepted при `{result:"OK"}`; дождаться появления в `state=published` и (когда появится) `commonUrl`/публичного URL. Отдельного статуса «В обработке» в API нет.

### Schedule

`delayedPublicationFlagState:"on"` + `requestedPublishTime` ISO UTC на publish (или save+publish изменений). Список: `state=pending`. Reschedule — новое время тем же способом. Cancel — выставить `off` и **опубликовать** это изменение (одного save недостаточно).

### `delete`

`POST /editor-api/v2/delete-publication?id=<publicationId>&publisherId=<id>` с пустым телом. Подтвердить GET 404 и отсутствие в списках.

### Images

Сначала draft/`publicationId`. `POST add-image` multipart field `image`, либо `add-image-from-url`. Вставить returned `id` в `data.image.id` блока `atomic:image` и/или `preview.image.id`. Не переиспользовать id между публикациями.

### Comments

Читать root через `documentId=native:<publication.id>`. Channel feed: `/editor-api/v2/publisher/<id>/comments`. Write (create/reply/edit/delete/restore/react/pin) **не реализовывать**, пока create не пройдёт на статье с готовым `commonUrl`. Ban/unban — по бандлу; live подтверждён только harmless self-reject.

## Что остаётся Unknown

- Точный cadence autosave открытого редактора и гонка UI-autosave vs API-save побайтно.  
- Пагинация списков при `pageSize` меньше полного числа (cursor/offset).  
- Полный enum `visibilityType` кроме `"all"`.  
- Сколько длится отсутствие `commonUrl` / публичной страницы после publish («В обработке» в UI).  
- Comments write-lifecycle целиком (create при наличии `commonUrl`, reply, edit, delete, restore, react, pin/unpin, dedupe, HTTP vs `status`).  
- Успешный `ban-user` / `unban-user` / список banned на не-self цели.  
- Минимальное подмножество cookie-имён уже compact-набора; lifetime и привязка к IP/устройству.  
- Обязательность конкретного `User-Agent` / `Referer` для всех мутаций.  
- Поведение `hasNativeAds:true` и тегов на не-article типах (вне scope).

Сырые sanitized captures: `/tmp/dzencrawl-research/fixtures/live/`. Тестовые объекты удалены; preexisting не трогались.
