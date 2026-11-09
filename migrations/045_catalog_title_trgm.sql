CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX IF NOT EXISTS catalog_game_title_trgm_idx
	ON catalog_game USING gin (lower(title) gin_trgm_ops);
