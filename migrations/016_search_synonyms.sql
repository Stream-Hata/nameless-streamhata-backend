CREATE OR REPLACE FUNCTION normalize_search_term(input text) RETURNS text AS $$
	SELECT btrim(regexp_replace(lower(replace(coalesce(input, ''), 'ё', 'е')), '[^a-zа-я0-9]+', ' ', 'g'));
$$ LANGUAGE sql IMMUTABLE;

CREATE TABLE IF NOT EXISTS search_synonyms (
	id bigserial PRIMARY KEY,
	game_slug text REFERENCES catalog_game(slug) ON DELETE CASCADE,
	term text NOT NULL,
	replacement text,
	normalized_term text NOT NULL,
	normalized_replacement text,
	locale varchar(8) NOT NULL DEFAULT 'ru',
	weight int NOT NULL DEFAULT 100,
	enabled boolean NOT NULL DEFAULT true,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	UNIQUE NULLS NOT DISTINCT (game_slug, normalized_term)
);

CREATE INDEX IF NOT EXISTS search_synonyms_term_trgm
	ON search_synonyms USING gin (normalized_term gin_trgm_ops);
CREATE INDEX IF NOT EXISTS search_synonyms_game_idx ON search_synonyms (game_slug);

INSERT INTO search_synonyms (game_slug, term, replacement, normalized_term, normalized_replacement)
SELECT NULL, term, replacement, normalize_search_term(term), normalize_search_term(replacement)
FROM search_alias
ON CONFLICT DO NOTHING;
