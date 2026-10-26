CREATE EXTENSION IF NOT EXISTS pg_trgm;

ALTER TABLE catalog_game ADD COLUMN IF NOT EXISTS year int;
ALTER TABLE catalog_game ADD COLUMN IF NOT EXISTS tier text;
ALTER TABLE catalog_game ADD COLUMN IF NOT EXISTS age text;
ALTER TABLE catalog_game ADD COLUMN IF NOT EXISTS status text;
ALTER TABLE catalog_game ADD COLUMN IF NOT EXISTS cover text;
ALTER TABLE catalog_game ADD COLUMN IF NOT EXISTS platforms text[];
ALTER TABLE catalog_game ADD COLUMN IF NOT EXISTS search_text text;

CREATE OR REPLACE FUNCTION catalog_game_sync() RETURNS trigger AS $$
BEGIN
	NEW.year := NULLIF(NEW.payload->>'year', '')::int;
	NEW.tier := COALESCE(NULLIF(NEW.payload->>'tier', ''), 'none');
	NEW.age := COALESCE(NULLIF(NEW.payload->>'age', ''), 'modern');
	NEW.status := COALESCE(NULLIF(NEW.payload->>'status', ''), 'seed');
	NEW.cover := NULLIF(NEW.payload->>'cover', '');
	NEW.platforms := COALESCE(
		(SELECT array_agg(p->>'code') FROM jsonb_array_elements(COALESCE(NEW.payload->'platforms', '[]'::jsonb)) p),
		ARRAY[]::text[]
	);
	NEW.search_text := lower(
		NEW.title || ' ' ||
		COALESCE(NEW.payload->>'titleRu', '') || ' ' ||
		COALESCE(NEW.payload->>'originalTitle', '') || ' ' ||
		COALESCE((SELECT string_agg(value, ' ') FROM jsonb_array_elements_text(COALESCE(NEW.payload->'aliases', '[]'::jsonb))), '') || ' ' ||
		COALESCE((SELECT string_agg(value, ' ') FROM jsonb_array_elements_text(COALESCE(NEW.payload->'genres', '[]'::jsonb))), '') || ' ' ||
		COALESCE((SELECT string_agg(value, ' ') FROM jsonb_array_elements_text(COALESCE(NEW.payload->'genresRu', '[]'::jsonb))), '')
	);
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS catalog_game_sync_trigger ON catalog_game;
CREATE TRIGGER catalog_game_sync_trigger
BEFORE INSERT OR UPDATE OF payload, title ON catalog_game
FOR EACH ROW EXECUTE FUNCTION catalog_game_sync();

INSERT INTO catalog_game (slug, title, source, payload)
SELECT g->>'slug', g->>'title', 'rgg-cat', g
FROM app_setting, jsonb_array_elements(value->'games') g
WHERE key = 'games_catalog_cache'
ON CONFLICT (slug) DO NOTHING;

UPDATE catalog_game SET payload = payload;

CREATE INDEX IF NOT EXISTS catalog_game_year_idx ON catalog_game (year);
CREATE INDEX IF NOT EXISTS catalog_game_tier_idx ON catalog_game (tier);
CREATE INDEX IF NOT EXISTS catalog_game_age_idx ON catalog_game (age);
CREATE INDEX IF NOT EXISTS catalog_game_status_idx ON catalog_game (status);
CREATE INDEX IF NOT EXISTS catalog_game_platforms_idx ON catalog_game USING gin (platforms);
CREATE INDEX IF NOT EXISTS catalog_game_search_idx ON catalog_game USING gin (search_text gin_trgm_ops);
