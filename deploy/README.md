# Деплой Nameless StreamHata

## 1. Бэкенд + PostgreSQL (Docker)

```bash
cd rgg-inv-go-backend
cp .env .env.production   # заполнить секреты
docker compose up -d --build
```

- База PostgreSQL поднимается автоматически, миграции применяются при старте бэкенда.
- Бэкенд слушает `127.0.0.1:3000` на хосте (наружу не открыт).
- Тома: `pgdata` — данные Postgres. Бэкап: `docker compose exec db pg_dump -U rgg rgg > backup.sql`.

## 2. Фронтенд

```bash
pnpm install
pnpm build
```

Скопировать `dist/nameless-streamhata/browser` на сервер, например в `/var/www/nameless/browser`.

## 3. Nginx

```bash
cp deploy/nginx.conf /etc/nginx/sites-available/nameless
ln -s /etc/nginx/sites-available/nameless /etc/nginx/sites-enabled/nameless
nginx -t && systemctl reload nginx
```

Nginx проксирует `/rgg-sync/` на `127.0.0.1:3000`, остальное отдаёт как SPA.

## 4. Переменные

- `LISTEN_ADDR=0.0.0.0:3000` — обязателен в контейнере.
- `DB_HOST/DB_PORT/DB_NAME/DB_USER/DB_PASSWORD` — переопределяются compose.
- `RAWG_API_KEY`, `RAWG_MONTHLY_LIMIT=18000`, `NEXUS_API_KEY` — источники каталога.
- `TWITCH_CLIENT_KEY`, `TWITCH_SECRET_KEY` — IGDB и Twitch.
- `OWNER_LOGIN`, `OWNER_LOGINS` — доступ к админке.

## 5. Обновление

```bash
git pull
docker compose up -d --build
pnpm build   # и обновить статику
```

## 6. Перенос базы с локальной машины

```bash
# локально: дамп
pg_dump -h localhost -U postgres -d rgg -Fc -f dumps/rgg-$(date +%F).dump

# на сервер
scp dumps/rgg-YYYY-MM-DD.dump user@server:/opt/nameless/

# на сервере: поднять БД и восстановить
cd /opt/nameless/rgg-inv-go-backend
docker compose up -d db
./deploy/restore-db.sh /opt/nameless/rgg-YYYY-MM-DD.dump
docker compose up -d backend
```

После восстановления проверить:

```bash
curl -s http://127.0.0.1:3000/rgg-sync/catalog/import/status
```
