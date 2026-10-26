#!/usr/bin/env bash
set -euo pipefail

# Восстанавливает дамп в PostgreSQL из docker-compose.
# Использование: ./restore-db.sh /path/to/rgg-YYYY-MM-DD.dump
# Переменные: DB_NAME (rgg), DB_USER (rgg)

DUMP="${1:?Укажи путь к .dump}"
DB_NAME="${DB_NAME:-rgg}"
DB_USER="${DB_USER:-rgg}"

if [ ! -f "$DUMP" ]; then
  echo "Дамп не найден: $DUMP" >&2
  exit 1
fi

echo "Восстанавливаю $DUMP в базу $DB_NAME (пользователь $DB_USER)…"

docker compose exec -T db pg_restore \
  -U "$DB_USER" \
  -d "$DB_NAME" \
  --clean \
  --if-exists \
  --no-owner \
  --no-privileges < "$DUMP"

echo "Готово."
