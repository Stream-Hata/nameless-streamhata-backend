UPDATE users SET twitch_token = NULL;

WITH ranked AS (
	SELECT id, row_number() OVER (PARTITION BY lower(username) ORDER BY id DESC) AS rn
	FROM users
), doomed AS (SELECT id FROM ranked WHERE rn > 1)
DELETE FROM user_state WHERE user_id IN (SELECT id FROM doomed);

WITH ranked AS (
	SELECT id, row_number() OVER (PARTITION BY lower(username) ORDER BY id DESC) AS rn
	FROM users
), doomed AS (SELECT id FROM ranked WHERE rn > 1)
DELETE FROM tokens WHERE user_id IN (SELECT id FROM doomed);

WITH ranked AS (
	SELECT id, row_number() OVER (PARTITION BY lower(username) ORDER BY id DESC) AS rn
	FROM users
), doomed AS (SELECT id FROM ranked WHERE rn > 1)
DELETE FROM users WHERE id IN (SELECT id FROM doomed);

WITH ranked AS (
	SELECT id, row_number() OVER (PARTITION BY user_id ORDER BY id DESC) AS rn
	FROM tokens
	WHERE user_id IS NOT NULL
)
DELETE FROM tokens WHERE id IN (SELECT id FROM ranked WHERE rn > 1);

UPDATE users
SET twitch_token = (
	SELECT id FROM tokens WHERE tokens.user_id = users.id ORDER BY id DESC LIMIT 1
);

CREATE UNIQUE INDEX IF NOT EXISTS users_username_unique ON users (lower(username));
CREATE UNIQUE INDEX IF NOT EXISTS users_twitch_id_unique ON users (twitch_user_id);
CREATE UNIQUE INDEX IF NOT EXISTS tokens_user_unique ON tokens (user_id);
