CREATE OR REPLACE FUNCTION roman_normalize(input text) RETURNS text AS $$
	SELECT regexp_replace(
		regexp_replace(
			regexp_replace(
				regexp_replace(
					regexp_replace(
						regexp_replace(
							regexp_replace(
								regexp_replace(
									regexp_replace(lower(coalesce(input, '')), '\mviii\M', '8', 'g'),
									'\mvii\M', '7', 'g'),
								'\mvi\M', '6', 'g'),
							'\miv\M', '4', 'g'),
						'\mix\M', '9', 'g'),
					'\miii\M', '3', 'g'),
				'\mii\M', '2', 'g'),
			'\mv\M', '5', 'g'),
		'\mx\M', '10', 'g');
$$ LANGUAGE sql IMMUTABLE;

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
	NEW.search_text := lower(regexp_replace(
		NEW.title || ' ' ||
		COALESCE(NEW.payload->>'titleRu', '') || ' ' ||
		COALESCE(NEW.payload->>'originalTitle', '') || ' ' ||
		COALESCE((SELECT string_agg(value, ' ') FROM jsonb_array_elements_text(COALESCE(NEW.payload->'aliases', '[]'::jsonb))), '') || ' ' ||
		COALESCE((SELECT string_agg(value, ' ') FROM jsonb_array_elements_text(COALESCE(NEW.payload->'genres', '[]'::jsonb))), '') || ' ' ||
		COALESCE((SELECT string_agg(value, ' ') FROM jsonb_array_elements_text(COALESCE(NEW.payload->'genresRu', '[]'::jsonb))), '') || ' ' ||
		COALESCE((SELECT string_agg(value, ' ') FROM jsonb_array_elements_text(COALESCE(NEW.payload->'modes', '[]'::jsonb))), '') || ' ' ||
		COALESCE((SELECT string_agg(value, ' ') FROM jsonb_array_elements_text(COALESCE(NEW.payload->'themes', '[]'::jsonb))), '') || ' ' ||
		COALESCE((SELECT string_agg(value, ' ') FROM jsonb_array_elements_text(COALESCE(NEW.payload->'perspectives', '[]'::jsonb))), '') || ' ' ||
		COALESCE((SELECT string_agg(p->>'name', ' ') FROM jsonb_array_elements(COALESCE(NEW.payload->'platforms', '[]'::jsonb)) p), '') || ' ' ||
		COALESCE((SELECT string_agg(value, ' ') FROM jsonb_array_elements_text(COALESCE(NEW.payload->'developers', '[]'::jsonb))), '') || ' ' ||
		COALESCE((SELECT string_agg(value, ' ') FROM jsonb_array_elements_text(COALESCE(NEW.payload->'publishers', '[]'::jsonb))), ''),
		'[^a-zа-я0-9]+', ' ', 'g'
	)) || ' ' || translit_ru(NEW.title) || ' ' ||
	translit_ru(COALESCE(NEW.payload->>'titleRu', '')) || ' ' ||
	translit_ru(COALESCE((SELECT string_agg(value, ' ') FROM jsonb_array_elements_text(COALESCE(NEW.payload->'aliases', '[]'::jsonb))), '')) || ' ' ||
	roman_normalize(NEW.title) || ' ' ||
	roman_normalize(COALESCE(NEW.payload->>'titleRu', ''));
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

UPDATE catalog_game SET payload = payload;
