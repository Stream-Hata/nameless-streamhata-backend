ALTER TABLE tokens
    ALTER COLUMN access_token TYPE text,
    ALTER COLUMN refresh_token TYPE text;

ALTER TABLE tokens
    ADD COLUMN IF NOT EXISTS refresh_token_hash varchar(64) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS tokens_refresh_token_hash_idx
    ON tokens (refresh_token_hash);
