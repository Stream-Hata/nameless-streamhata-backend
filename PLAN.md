# План работ (бэкенд)

Полный кросс-проектный план: `../rgg-inv/PLAN.md`.
Здесь — только рабочая очередь бэкенда в порядке приоритета.

## Фаза 0 — паритет с TS-бэкендом (`../rgg-inv-backend`)

Портировать в Go (референс — `../rgg-inv-backend`):

- [x] `chat` — ring-buffer чата, дедупликация, подписчики (`twitch/chat/chat.service.ts`)
- [x] `twitchirc` — IRC WebSocket: PASS/NICK/CAP/JOIN, tags, PING/PONG, реконнект (`twitch/twitch-irc.ts`) + тесты парсера
- [x] `eventsub` — WS-клиент, `session_welcome/reconnect`, `channel.chat.message` (`channel.chat.notification` — вместе с viewer, далее) + тесты
- [x] `llm` — Groq HTTP (net/http): `Classify` + `GenerateVerdict` + fallback, env `GROQ_API_KEY`, `GROQ_MODEL` + тесты
- [x] `chatjury` — сервис (settings/validation, голоса, таймер, вердикт с generationId, `Finish()` для кнопки «Стоп») + тесты
- [x] `chatjury` — планировщик (кандидаты, `!yea/!nei`, порог min_confidence) + тесты
- [x] `chatruntime` — runtime (refresh token по таймеру, helix/users, перезапуск IRC+EventSub, статус/тумблер интеграции)
- [x] `viewer` — runtime (chatters polling 60с с пагинацией, watch streak из chat.notification, очередь стриков с задержкой, фильтр/сортировка) + тесты
- [x] `syncstate` — `GET/POST /rgg-sync` с частичным merge (mode/timer/hotbar) + тесты
- [x] HTTP-роуты под `/rgg-sync`: chat-jury, donations, music, integrations, viewers(+latest-streak, :userId), /rgg-sync state, twitch(+алиасы validate)
- [x] `main.go` — авто-старт chatruntime при наличии токена, graceful shutdown, реестр интеграций (chat/donations/music)
- [x] `gorilla/websocket` + CORS-middleware
- [x] тесты: chatjury, twitchirc, eventsub, llm, donations, music, viewer, syncstate (8 пакетов)

## Сделано (итерация «донаты + музыка + интеграции»)

- [x] `donations` — шина, статистика, топ донатеров, пермиссивная нормализация
- [x] `donations` — DonationAlerts: OAuth (authorize/exchange/refresh), Centrifugo WS, реконнект
- [x] `donations` — вебхуки: Ko-fi (verification token), DonationTo, Donate.stream, DonatePay, generic
- [x] `chat` — шина чата (ring 500, дедуп, подписчики)
- [x] `music` — YouTube-парсер (ссылки из чата, метаданные без API-ключей), upsert, CSV-экспорт
- [x] `music` — персистентность (миграция `004_track.sql`, `data_base.NewConnection`)
- [x] `integrations` — реестр + `GET/POST /rgg-sync/integrations` (вкл/выкл без рестарта)
- [x] Роуты: `/rgg-sync/donations[...]`, `/rgg-sync/music[...]`; CORS; `127.0.0.1:3000`
- [x] Фикс generic-репозитория (`UPDATE`), тесты (`go test ./...` зелёный)

## Фаза 1 — сделано (команды, таймеры, модерация, алерты, клипы, цели)

- [x] `commands` — CRUD, переменные ($(user)/$(login)/$(channel)/$(count)/$(date)/$(time)), алиасы, кулдауны, уровни доступа, персистентность (миграция `005_commands.sql`), тест-рендер, роуты `/rgg-sync/commands`
- [x] `timers` — периодические сообщения (интервал, round-robin/random, active-only 5 мин), роуты `/rgg-sync/timers`
- [x] `moderation` — фильтры (слова/ссылки/капс/эмодзи/повторы), действия delete/timeout через Helix, сухой прогон, лог 500, роуты `/rgg-sync/moderation`
- [x] `alerts` — EventSub follow/sub/gift/resub/cheer/raid/hype_train/goal → лента + настройки типов + тест, роуты `/rgg-sync/alerts`
- [x] `clips` — `POST /rgg-sync/clips` (scope `clips:edit`), кнопка «Клип» в дашборде
- [x] `chatruntime` — Say (IRC), DeleteMessage/TimeoutUser/CreateClip (Helix), роутинг нотификаций (viewer/alerts), скопы расширены
- [x] тесты: commands, timers, moderation, alerts (+ chatjury/viewer/eventsub/music/donations/syncstate/twitchirc/llm) — 12 пакетов

## Фаза 2 — сделано (баллы, игры, цитаты, channel points, опросы)

- [x] `points` — кошельки/транзакции/магазин наград, начисления (сообщение с кулдауном, минута просмотра из чатерсов, бонусы за стрик/подписки/битсы), встроенные команды `!points/!top/!shop/!redeem`, персистентность (миграция `006_engagement.sql`), роуты `/rgg-sync/points`
- [x] `games` — слоты (веса, выплаты ×2/×10/×15/×25), колесо (×0…×5), викторина (вопрос→ответ, награда первому), кулдауны, ставки min/max, роуты `/rgg-sync/games`
- [x] `quotes` — `!quote`, `!quote <n>`, `!quote add`, `!quote del` (стример), панельные роуты + персистентность
- [x] `redemptions` — привязка channel points наград к действиям (say/points/timeout), авто-fulfill через Helix, лента выкупов, роуты `/rgg-sync/redemptions`
- [x] `polls` — создание/завершение нативных опросов через Helix, зеркало опросов и предиктов из EventSub в панель, история, роуты `/rgg-sync/polls`
- [x] `chatruntime` — FulfillRedemption, CreatePoll/EndPoll, универсальный роутер нотификаций
- [x] хуки: viewer → points (tick/streak), alerts → points (подписки/битсы/гифты)
- [x] EventSub: redemptions add/update, polls begin/progress/end, predictions begin/progress/lock/end
- [x] Скопы: `channel:read/manage:redemptions`, `channel:read/manage:polls`, `channel:read:predictions`
- [x] тесты: points, games, quotes (+все прежние) — 15 пакетов

## Фаза 3 — сделано (OBS, эфир, уведомления, аналитика, деки)

- [x] `obs` — клиент OBS WebSocket 5: auth-хендшейк (sha256/base64), сцены, mute источников, стрим/запись/реплей, события смены сцены и статусов, авто-коннект из env, роуты `/rgg-sync/obs*` + тест алгоритма авторизации
- [x] `stream` — управление эфиром: title/категория (поиск категории и PATCH channels), рекламные паузы, Shield Mode (чтение/запись), роуты `/rgg-sync/stream*` (скопы `channel:manage:broadcast`, `moderator:read/manage:shield_mode`)
- [x] `notify` — Discord webhook и Telegram-бот: события стрима/алерты/донаты, тест-сообщение, роуты `/rgg-sync/notify*`
- [x] `analytics` — сессии эфира: старт/стоп по `stream.online/offline`, замеры зрителей раз в минуту (пик/среднее), счётчики сообщений и чатерсов, история, CSV-экспорт, роуты `/rgg-sync/analytics*` + тесты
- [x] `actions` — HTTP API для деков: реестр именованных действий (clip, obs.scene, obs.mute, obs.replay, stream.title, stream.shield) под `X-API-Key` (env `DECK_API_KEY`), роуты `/rgg-sync/actions*`
- [x] мульти-подписчики алертов (баллы + уведомления + аналитика), подписки `stream.online/offline`
- [x] тесты: obs, analytics (+прежние) — 17 пакетов

## Далее (Фаза 3 остатки / Фаза 4)

Фазы 1–4 (команды, модерация, алерты, цели, клипы, баллы, игры, OBS, мультиплатформа, интеграции, аналитика, AI) — см. `../rgg-inv/PLAN.md`, чеклисты «BE:».

## Конвенции

- Пакет на фичу (`chat`, `chatjury`, `llm`, `viewer`, …), без глобального состояния.
- Внешние вызовы только с `context.Context`; ретраи с backoff.
- Persist — PostgreSQL (миграции в `migrations/`, нумерация продолжается с 004).
- Env-конфиг через `.env`; секреты не коммитить.
