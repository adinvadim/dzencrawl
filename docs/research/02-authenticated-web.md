# 02. Авторизованная веб-поверхность Дзена (студия автора)

Дата исследования: 2026-11-09. Трек: read-only разведка для будущего CLI авторинга.

## Главное ограничение: сессии автора не было

Chrome-сессия, в которой работал исследователь, **не авторизована в Дзене**: в шапке кнопка «Войти», в SSR `authInfo.isAuthorized=false`, cookie `yandex_login` пустая. По правилам трека вход не выполнялся и 1Password не использовался. Поэтому в отчёте **нет ни одного наблюдения авторизованного ответа**.

Что из этого следует:

- Бандл самой студии получить не удалось: роуты `/profile/editor/...` анониму отдают HTML 404 без единого скрипта. Читались только публичные бандлы (`dzen-desktop`, `dzen-layout`, `content`, `comments2`, `dzen-root`).
- Контракты `editor-api` (черновики, публикация, загрузка картинок) известны только из стороннего открытого кода. Для них подтверждено лишь существование маршрутов и HTTP-метод.
- Контракты комментариев, наоборот, взяты из реального публичного бандла `comments2`.

**Самый ценный следующий шаг:** одна авторизованная сессия (пользователь входит сам) и повтор GET-проверок из раздела «Что нужно проверить с сессией».

## Метки

| Метка | Значение |
|---|---|
| **Observed live** | Наблюдалось в анонимной сессии: GET из контекста страницы или `curl`. Ни одного POST/PUT/PATCH/DELETE не отправлено |
| **Derived from bundle** | Прочитано в публичном JS. На живом сервисе не выполнялось, то есть unverified |
| **Unknown** | Не установлено. Пометка «стороннее свидетельство» означает, что сведение есть только в чужом открытом коде и нами не проверено |

Все эндпоинты приватные: это наблюдаемые контракты, а не обещания провайдера.

Сторонние источники (все публичные, обновлялись в 2026 году):

- [fromgate/ProzenExtension](https://github.com/fromgate/ProzenExtension), файл `reference/zen-api.toml`, обновлён 2026-11-02. Самый ценный: машиночитаемая схема полей.
- [vetalione/content_pipeline](https://github.com/vetalione/content_pipeline), `dzen-api.ts`: полный поток записи.
- [Horosheff/hyperion-reels](https://github.com/Horosheff/hyperion-reels): браузерная автоматизация, поведение капчи.

## 1. Домены и эндпоинты

### Домены и пути

| Что | Значение | Метка |
|---|---|---|
| Студия | `dzen.ru/profile/editor/id/<publisherId>[/publications,/campaigns/]` | Derived from bundle |
| Редактор | `/profile/editor/id/<publisherId>/<publicationId>/edit` | Unknown, стороннее свидетельство (значение Referer) |
| Создание (**не открывать**, может создать черновик) | `/profile/editor/create`, `/profile/editor/new#/editor`, `#/video-editor`, `#/brief-editor` | Derived from bundle |
| Роут по слагу | `/profile/editor/<channel>/publications` | Unknown, стороннее свидетельство |
| API | `dzen.ru/editor-api/v2`, `dzen.ru/editor-api/v3`, `dzen.ru/media-api`, `dzen.ru/api/comments`, `dzen.ru/api/comments/v2`, `dzen.ru/api/auth/web/*` | Observed live |
| Статика | `static.dzeninfra.ru/s3/zen-apps/<app>/<semver>/<chunk>.modern.bundle.js` | Observed live |
| Вход | `sso.passport.yandex.ru/push`, `sso.dzen.ru`, `login.vk.com` (`act=autologin`), `/api/auth/web/autologin-vk-log-only` | Observed live |

`/profile/editor` без id отдаёт 404 (Observed live).

### Карта маршрутов по анонимным GET

Метод надёжный и дешёвый: API различает отсутствие маршрута и отсутствие авторизации. **Observed live**, id нулевые.

| Статус | Значение |
|---|---|
| `401` JSON | маршрут есть, нужна авторизация |
| `405` JSON | маршрут есть, метод не GET |
| `400` JSON | маршрут есть, запрос отклонён до авторизации |
| `404` `text/html` | маршрута нет, ответил HTML-фронт |

| Маршрут | Ответ анониму | Вывод |
|---|---|---|
| `GET /editor-api/v3/publications?state=…&publisherId=…` | 401 | **Основной список**, в том числе с `state=draft` принят маршрутом |
| `GET /editor-api/v3/publishers/{id}` | 401 | Карточка канала |
| `GET /editor-api/v3/publishers` | 405 | Коллекция без GET, **список каналов здесь не получить** |
| `GET /editor-api/v2/get-publication?publisherId&publicationId` | 401 | **Кандидат на чтение одной публикации / тела черновика** |
| `GET /media-api/get-publication?publicationId` | 401 | Второй кандидат на чтение одной публикации |
| `GET /editor-api/v2/user` | 401 | **Кандидат на текущего пользователя** |
| `GET /editor-api/v2/publisher/{id}` | 401 | Кандидат на данные канала |
| `GET /editor-api/v2/publisher/{id}/stats2` | 401 | Статистика |
| `GET /editor-api/v2/publisher/{id}/income2` | 401 | Доход |
| `GET /editor-api/v2/publisher/{id}/publications-count` | 400 `invalid-csrf-token-error` | Счётчики, **проверяет CSRF на GET** |
| `GET /editor-api/v2/v2/get-strikes` | 401 | Ограничения канала (двойной `v2` настоящий) |
| `GET /api/comments/banned-users` | 401 | Заблокированные комментаторы |
| `GET /api/comments/v2/root-comments` | 400 при неверном `documentId` | Публичный, формат `type:id` |
| `GET /media-api/publication-view-stat` | 200 | Публичная статистика публикации |
| `GET /api/spec-ads/onboarding/status` | 200 | `{hasAdsVkAccount}` |
| `POST`-only: `/editor-api/v2/add-publication`, `update-publication-content`, `update-publication-content-and-publish`, `add-image`, `add-image-from-url`, `delete-publication` | 405 | **Маршруты записи существуют** |
| `POST`-only: `/api/comments/v2/create-comment`, `delete-comment/{id}`, `ban-user/{id}` | 405 | Подтверждено |
| `PUT`-only: `/api/comments/v2/pin-comment` | 405 | Подтверждено |
| **Нет** (404 HTML): `/editor-api/v2/get-publications-by-filter`, `/editor-api/v2/publisher/{id}/publications`, `/editor-api/v3/publications/{id}`, `/editor-api/v2/publication/{id}`, `/editor-api/v2/publications/{id}`, `/editor-api/v3/me`, `/editor-api/v3/user`, `/editor-api/v2/get-user`, `/editor-api/v2/get-publishers`, `/editor-api/v2/publishers`, `/media-api/get-publications-by-state`, `/media-api/publisher`, `/editor-api/v2/publish-publication`, `unpublish-publication`, `restore-publication` | 404 | Старые или угаданные пути; `get-publications-by-filter` и `get-publications-by-state` встречаются в чужом коде, но сейчас мертвы |

### Инвентарь по задачам

| Задача | Запрос | Параметры и форма ответа | Уверенность / метка |
|---|---|---|---|
| Текущий пользователь | `GET /editor-api/v2/user` | Форма неизвестна | Низкая. Маршрут Observed live, контракт Unknown |
| То же, через SSR | `GET https://dzen.ru/` | `authInfo{isAuthorized, yandexuid, userStatusOnPublishersPlatform, clid, product, partner, clidName}` | Высокая для ключей, Observed live |
| Список каналов | не найден | Ни один кандидат не подтвердился | Unknown |
| Карточка канала | `GET /editor-api/v3/publishers/{id}` | `publisher{regTime, metrikaCounterId?, …}` | Средняя. Маршрут Observed live, поля Unknown (стороннее свидетельство) |
| Список публикаций | `GET /editor-api/v3/publications` | Query: `state`, `publisherId`, `pageSize`, `types?`, `view?`, `query?`, `publicationIdAfter?` | Средняя. Маршрут Observed live, параметры стороннее свидетельство |
| Значения `state` | `published` подтверждён сторонним кодом; `draft` только принят маршрутом | Значения для отложенных и архива неизвестны | Unknown |
| Пагинация | курсор `publicationIdAfter` (id последнего элемента) | `pageSize` по умолчанию у стороннего клиента 10 | Unknown, стороннее свидетельство |
| Счётчики | `GET …/publications-count?publisherId=` | `items.<type>.published` | Unknown, стороннее свидетельство |
| Чтение черновика | `GET /editor-api/v2/get-publication` | Форма неизвестна. В списке есть `publications[].content.articleContent`, вероятно с Draft.js | Низкая. Маршрут Observed live |
| Статистика | `GET …/stats2` | Query: повторяемый `fields`, `groupBy=flight,ageGender`, `publicationIds`, `from`, `to`, `publicationTypes`, `total`, `isSubscriber`. Ответ: `actuality`, `total.stats`, `publications[]{publication, stats}` | Unknown, стороннее свидетельство |
| Доход | `GET …/income2` | `total.intervals[]{timestamp, viewTimeSec, income}` | Unknown, стороннее свидетельство |
| Список комментариев | `GET /api/comments/v2/root-comments` | См. ниже | Высокая, Derived from bundle + Observed live |
| Модерация | `/api/comments/v2/*`, `/api/comments/banned-users` | См. ниже | Высокая для формы, Derived from bundle |

Форма элемента списка (Unknown, стороннее свидетельство, но актуальная на 2026-11-02): `publications[]{id, publisherId, status, isBanned, isPublished, visibilityType, origin, commonUrl, titleForUrl, addTime, publishTime, content{type, modTime, preview{title, snippet, imageId, image}, images[]{id, urlTemplate}, articleContent, briefContent}}`, плюс соседние массивы `publicationCounters[]` и `socialCounters[]`, связанные по `publicationId`.

### Запись (unverified)

**Важно:** эти контракты взяты **не из бандла студии** (он недоступен), а из стороннего кода, поэтому метка Unknown. Подтверждено только то, что маршруты отвечают 405 на GET. Вызывать их в рамках исследования нельзя.

| Шаг | Запрос |
|---|---|
| CSRF | `GET /media-api/csrf-token` |
| Создать черновик | `POST /editor-api/v2/add-publication?publisherId&clientRid&clid`, тело `{title, publisherId, publicationType:"article", fp}` |
| Картинка | `POST /editor-api/v2/add-image-from-url?publicationId&url&publisherId` (или `add-image`, multipart) |
| Сохранить | `POST /editor-api/v2/update-publication-content?publisherId&clientRid` |
| Опубликовать | `POST /editor-api/v2/update-publication-content-and-publish?publisherId&clientRid` |

Тело сохранения и публикации: `{id, preview{title, snippet, image?{id}}, snippetFrozen, hasNativeAds, commentsFlagState, delayedPublicationFlagState, visibleComments, visibilityType, premiumTariffs, customCommentsTitle, articleContent{contentState}, tagsInput{tags, detectedTagsShown}, fp}`. Поле `contentState` — **строка с JSON Draft.js** `{blocks, entityMap}`; типы блоков у стороннего клиента: `unstyled`, `header-two`, `blockquote`, `atomic:image`. Для Markdown-конвертера это главная новость: целевой формат похож на Draft.js ContentState. `clientRid` — случайные 7 байт в hex. Referer у стороннего клиента указывает на страницу редактора.

### Комментарии и модерация (Derived from bundle, `comments2` 1.49.0, unverified)

База: `/api/comments/v2`. Заголовки: `X-Csrf-Token`, `X-Zen-Comments-Clid`, `-Partner`, `-Integration`, `-Product`, `-Place`, `-Clid-V2`, `-Page-Type-V2`. Везде query `rid`.

| Действие | Запрос | Тело |
|---|---|---|
| Корневые | `GET root-comments` | Query: `documentId` (`type:id`, в репозитории `native:<id>`), `publicationPublisherId`, `sorting`, `batchSize`, `withConfig`, `withCurrentUser`, `sessionTs`, `clientTs`, `deviceId`, `subscriptionStateFor` |
| Популярные | `POST popular-root-comments` | — |
| Ветка | `GET child-comments`, `POST replies`, `GET child-replies`, `GET highlight-comment/{id}` | — |
| Создать | `POST create-comment` | `publisherByChannel, documentId, publicationPublisherId, text, imageIds, lang, rootCommentId?, replyToCommentId?, sessionTs, deviceId, clientTs` |
| Править | `POST edit-comment/{id}` | Как создание, без полей ответа |
| Удалить / вернуть | `POST delete-comment/{id}`, `restore-comment/{id}` | `documentId, publicationPublisherId, sessionTs, deviceId, clientTs` |
| Закрепить | `PUT pin-comment`, `unpin-comment` | `commentId, documentId, sessionTs, deviceId, clientTs` |
| Бан | `POST ban-user/{uid}`, `unban-user/{uid}` | `documentId, publicationPublisherId, shouldDeleteAllComments, clientTs` |
| Реакция | `POST react-to-comment/{id}` | — |

Ответ `root-comments`: `status` (`ok`, `error`, `captcha-required`), `serverTs`, `csrfToken?`, `captchaData?`, `usersById`, `currentUser?`, `publicationChannelOwnerUid`, `config?`, `meta{childCommentsCount, rootCommentsCount, totalCommentsCount, commentsVisibility, appliedSorting}`, `items`, `subthreadByCommentId`, `moreItemsQueryString?` (курсор).

Лимиты в `config`: `childBatchSize`, `rootBatchSize`, `maxCommentLength`, `cutAfterLines`, `cutAfterChars`, `attachmentMimeTypes`. Статус видимости комментария: `visible`, `hidden`, `deleted`.

Выделенного списка «все комментарии моего канала» в публичных бандлах нет. Его поверхность (если она есть) в недоступном бандле студии: Unknown.

## 2. Сессия и CSRF

**Cookies (только имена).** Observed live, анонимная сессия, домен `.dzen.ru`: httpOnly+secure `Session_id`, `sessar`, `dzen_sess_id`, `zen_sso_checked`, `zen_vk_sso_checked`; также `yandexuid`, `yandex_login` (пустая), `ys`, `_yasc`, `zencookie`, `mda2_beacon`, `vid`, `vid_logged`, `is_auth_through_phone`, `Zen-User-Data`. Сессионные cookies есть и у анонима, со сроком около 24 часов, поэтому **наличие `Session_id` не означает авторизацию**. `zen_sso_checked` и `zen_vk_sso_checked` живут 14 дней.

**Минимальный набор для авторизации: Unknown.** Сторонний клиент отбирает `Session_id`, `sessionid2`, `yandex_login`, `yandexuid`, `i`, `yp`, `ys`, `L`, `_yasc`, `zen_sso`, `dzen_sso`, `zen_session_id`, `dzen_sess_id`, `_ym*`, `is_gdpr*`.

**CSRF:**

| Факт | Метка |
|---|---|
| Источник: `GET /media-api/csrf-token` → `{"result": "<string>"}`, формат `hex{40}:epochMs` | Observed live |
| Выдаётся без cookies и без браузера, каждый вызов даёт новое значение | Observed live |
| Заголовок: `X-Csrf-Token` | Derived from bundle + Observed live |
| Плохой токен → 400 `invalid-csrf-token-error`; валидный анонимный на `publications-count` → уже 404 (проверка CSRF идёт раньше проверки сущности) | Observed live |
| Клиент добавляет токен ко всем запросам (`withCsrf` по умолчанию `true`), кэширует его на загрузку страницы, таймаут 3 с, ошибка `FiledGetCsrf` | Derived from bundle |
| Может приходить в `initialToken` и в ответах комментариев (`csrfToken`) | Derived from bundle |
| На GET проверяют не все маршруты: `publications-count` да, `stats2` и `publications` у анонима отвечают 401 | Observed live |
| Срок жизни, обновление и привязка к сессии | Unknown |

Практический вывод: в CLI брать свежий токен на пакет операций и при `invalid-csrf-token-error` запрашивать его один раз заново.

**Прочие заголовки** (Derived from bundle): `Accept: application/json`, `Content-Type: application/json` (кроме `FormData`), опциональные `Authorization: Bearer`, `X-Zen-Authorization`, `X-Vk-Authorization` (берутся из токенов приложения, в вебе, видимо, не заполнены), а также `X-FP-Token` и `X-Zen-Device-Id`, если заданы. **Источник `X-FP-Token` не найден**, а сторонний клиент сохраняет его из браузера и шлёт вместе с полем `fp` в теле. Это потенциальный блокер для чистого HTTP: Unknown.

**Origin, Referer, User-Agent.** В анонимном режиме статусы одинаковы для браузерного UA, UA `curl` и пустого UA, Referer и Origin не отправлялись (Observed live). Для записи сторонний клиент выставляет `Origin: https://dzen.ru` и Referer редактора; обязательность Unknown.

**Воспроизводимость через HTTP.** Установлено (Observed live): API-маршруты отвечают `curl` без cookies так же, как браузеру (401 JSON, CSRF 200); блокировки по TLS-отпечатку на малом объёме не видно. Документные и несуществующие маршруты без cookies дают 302 на `sso.passport.yandex.ru/push` или `login.vk.com`; с cookie-метками SSO эти редиректы не требуются. **Не установлено:** воспроизводится ли авторизованный GET с сохранённым cookie jar вне браузера, привязана ли сессия к IP или отпечатку. Без сессии это проверить нельзя.

**Протухание сессии.** На API-маршрутах ожидается 401 с JSON (Observed live у анонима). Формы ошибок две: `{type, message}` у `editor-api/v3` и `{errors:[{type, message}]}` у v2; это нормализует `JavaException` (Derived from bundle). У `editor-api` в `message` видны Java-классы вида `ru.yandex.zen.common.HttpStatusCodeException`. На студийных HTML-роутах аноним с SSO-меткой получил **404, а не редирект на вход** (Observed live), тогда как сторонний клиент ждёт редирект на passport. Значит, проверять авторизацию по URL ненадёжно, нужен JSON-вызов.

**Проверка принципала.** Рекомендуемая последовательность:

1. `GET https://dzen.ru/`, разобрать `authInfo.isAuthorized` (Observed live).
2. `GET /editor-api/v3/publishers/{id}`: 200 означает доступ к каналу (Unknown, проверить с сессией).
3. `GET root-comments?…&withCurrentUser=true` по своей статье: `currentUser.publicationChannelRole` со значениями `owner`, `moderator`, `none` и `currentUser.publisher{id, name, channelPath}` (Derived from bundle). Это лучший найденный сигнал роли именно в канале.

## 3. Мультиканальность

Глобального «активного канала» на сервере не обнаружено. Канал адресуется явно: `publisherId` в query почти всех запросов, `{id}` в пути `editor-api`, `/profile/editor/id/<publisherId>` в URL студии, `publicationPublisherId` и `publisherByChannel` в комментариях (Derived from bundle; Unknown по студии). Расширение Prozen извлекает `publisherId` из URL запросов самой студии, что косвенно подтверждает модель.

Как получить список каналов пользователя: **Unknown**, надёжного маршрута нет. До проверки с сессией CLI должен принимать `publisherId` явно и подтверждать его принципалом.

## 4. Сигналы дрейфа контракта

| Сигнал | Где | Метка |
|---|---|---|
| Версии `editor-api/v2`, `v3` рядом, двойной `editor-api/v2/v2` | пути | Observed live |
| Semver приложений: `dzen-desktop/1.980.0`, `dzen-layout/1.161.1`, `comments2/1.49.0`, `content/1.357.1`, `profile-settings/1.45.0` | URL бандлов | Observed live |
| `zen-version:1.980.0` в `clientExperiments` статистики | бандл | Derived from bundle |
| Заголовок `Zen-Features` | `dzen-desktop` | Derived from bundle |
| Заголовок `Zen-Client-Experiments: zen-version:<semver>` | сторонний код (Prozen) | Unknown |
| У Prozen `zenVersion=2.189.0`, то есть, вероятно, другой номер приложения студии | сторонний код | Unknown |
| Схемы zod-подобного рантайма в бандле с именами эндпоинтов (`deleteCommentEndpoint`, …) | `comments2` | Derived from bundle |

**Ограниченный дрейф-зонд:**

1. Анонимная карта маршрутов (раздел 1): сверять статусы и `content-type` для десятка путей. Без аккаунта, дёшево, ловит переезды и удаление.
2. Авторизованно: `GET /editor-api/v3/publications?state=published&pageSize=1` и сверка путей `publications[].id`, `.publisherId`, `.content.type`, `.content.modTime`, `.content.preview.title`, `publicationCounters[].publicationId`, `socialCounters[].publicationId`.
3. Для записи: только сверка формы `contentState` и `blocks[].type` на чтении черновика.

Формат `zen-api.toml` (разделы `used`/`ignored`, неизвестные поля помечаются как `new`, значения enum копятся в `monitor`) стоит взять за образец.

## 5. CAPTCHA и анти-бот

**В студии и комментариях Дзена SmartCaptcha не найдена.** В 9 публичных бандлах ноль вхождений `smart-token`, `smartCaptcha`, `captcha-api.yandex` (Derived from bundle). Реальные виды капчи:

| Вид | Как устроено | Метка |
|---|---|---|
| Внутренняя image/audio капча комментариев | `captchaData{key, imageUrl, voiceUrl, generatePath, captchaUrl}`, UI «Введите код с картинки». Повтор запроса с заголовками `captcha-key`, `captcha-rep` (urlencoded ответ). Обновление через `/api/comments/v2/captcha/generate` (401 анониму) | Derived from bundle; маршрут Observed live |
| Капча VK ID (iframe) | Если есть `captchaData.captchaUrl`: попап `type_1`, полученный токен уходит в заголовке `captcha-vk-token` | Derived from bundle |
| VK ID SDK | `captcha_sid`, `captcha_img` при входе | Derived from bundle |
| Студия при записи | HTTP 400 с `captcha-required-error` на `update-publication-content-and-publish`; сторонний код ждёт ручного «Я не робот». Вид капчи неизвестен | Unknown, стороннее свидетельство |
| Страница Yandex SmartCaptcha на периметре | Простой `curl` к `yandex.cloud/ru/docs/...` получил «Are you not a robot? … Yandex SmartCaptcha». На `dzen.ru` за примерно 70 анонимных запросов такого не было | Observed live |

**Триггер.** В комментариях капча приходит как `status: "captcha-required"`; фронт бросает `Captcha required`, показывает модалку или iframe и повторяет запрос (Derived from bundle). Точные пороги Unknown. Сторонний код отмечает корреляцию с частотой и с устаревшей сессией.

**2Captcha API v2** ([документация](https://2captcha.com/api-docs/yandex-smart-captcha)): `POST https://api.2captcha.com/createTask` с `{clientKey, task}`, затем `POST https://api.2captcha.com/getTaskResult` с `{clientKey, taskId}`, опрос не чаще раза в 5 секунд. Ответ содержит `errorId`, `status` (`processing`, `ready`), `solution`, `cost`.

| Тип | Поля | Результат |
|---|---|---|
| `YandexSmartCaptchaTaskProxyless` | `websiteURL`, `websiteKey`, опционально `userAgent`, `cookies` | `solution.token` для `smart-token` |
| `YandexSmartCaptchaTask` | то же + `proxyType`, `proxyAddress`, `proxyPort`, `proxyLogin?`, `proxyPassword?` | то же |
| `SmartCaptchaTask` | `image` (Base64, 320×180), `imgInstructions` (480×180), `comment?` | `solution.coordinates[]` |
| `PazlCaptchaTask` | `image`, `task` | шаги пазла |

По [документации Yandex](https://yandex.cloud/en/docs/smartcaptcha/concepts/validation) токен SmartCaptcha живёт 5 минут и проверяется один раз; ключ клиента начинается с `ysc1_`. Важно: **эти типы подходят только к SmartCaptcha, а найденная в комментариях капча другая.** Соответствие для внутренней image-капчи (текстовый тип 2Captcha) и для капчи VK ID не проверялось.

**Рекомендация.** Считать капчу состоянием «остановиться и передать человеку», а не задачей на автоматическое решение. В README проекта прямо сказано, что он «не логинится, не обходит CAPTCHA/paywall». Автоматическое решение капчи на реальном авторском аккаунте меняет эту позицию и несёт риск для канала. Решать это должен владелец продукта, отдельным явным решением.

**Вход.** Яндекс ID ([вход без пароля](https://yandex.ru/support/id/ru/no-password), [приложение Яндекс ID](https://yandex.ru/support/id/ru/authorization/twofa)): пароль с кодом из SMS или push, QR-код, выбор картинки в приложении (5 попыток), одноразовый пароль из приложения (30 секунд), биометрия WebAuthn. Многие из этих шагов требуют телефона. Дзен дополнительно поддерживает вход через VK ID (`login-by-phone-vk-id`, One Tap, `/api/auth/web/get-vk-sat`; Derived from bundle). Вывод для CLI: не автоматизировать форму входа, а импортировать сессию из браузера пользователя либо запускать интерактивный вход в видимом браузере.

## 6. Лимиты частоты

| Факт | Метка |
|---|---|
| 429 и `Retry-After` не встречены примерно за 70 последовательных анонимных запросов | Observed live |
| Заголовков лимитов нет (в ответе CSRF только `content-type`, `date`, `server`, `vary`, `x-envoy-upstream-service-time` и служебные) | Observed live |
| В 9 бандлах нет обработки 429, `Retry-After`, `rateLimit` | Derived from bundle |
| Базовый fetch-клиент: таймаут 20 с, `retry=0` и `retryDelay=0` по умолчанию, задержка постоянная, экспоненты нет | Derived from bundle |
| Лимиты авторизованной записи | Unknown |
| Сторонний клиент делает паузы 500 мс между загрузками картинок | Unknown, стороннее свидетельство |

Анонимная выборка мала и ничего не говорит о порогах записи. Сигналом торможения на практике может быть капча, а не 429. Рекомендация: один поток, пауза от 1.5 с как у текущего `request_delay`, запись реже, экспоненциальный backoff на 429 и 5xx, остановка при признаках капчи.

## Что нужно проверить с сессией

Только GET, пользователь входит сам:

1. `GET /editor-api/v2/user`: форма ответа, идентификатор пользователя.
2. Как студия получает список каналов: посмотреть XHR при открытии `/profile/editor/id/<publisherId>/publications`, при этом не открывая редактор.
3. Значения `state` для черновиков, отложенных и архива; скачать бандл студии и извлечь контракты записи.
4. `GET /editor-api/v2/get-publication` на существующем черновике: форма `articleContent`.
5. Воспроизведение авторизованного GET через `curl` с экспортированным cookie jar: минимальный набор cookies, нужен ли `X-FP-Token`.
6. Откуда берётся `X-FP-Token` и поле `fp`.
