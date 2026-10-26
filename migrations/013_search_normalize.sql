CREATE OR REPLACE FUNCTION translit_ru(input text) RETURNS text AS $$
	SELECT lower(regexp_replace(
		translate(lower(coalesce(input, '')), 'абвгдеёжзийклмнопрстуфхцчшщъыьэюя', 'abvgdeezziyklmnoprstufhccss y eua'),
		'[^a-z0-9]+', ' ', 'g'
	));
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
		COALESCE((SELECT string_agg(value, ' ') FROM jsonb_array_elements_text(COALESCE(NEW.payload->'genresRu', '[]'::jsonb))), ''),
		'[^a-zа-я0-9]+', ' ', 'g'
	)) || ' ' || translit_ru(NEW.title) || ' ' ||
	translit_ru(COALESCE(NEW.payload->>'titleRu', '')) || ' ' ||
	translit_ru(COALESCE((SELECT string_agg(value, ' ') FROM jsonb_array_elements_text(COALESCE(NEW.payload->'aliases', '[]'::jsonb))), ''));
	RETURN NEW;
END;
$$ LANGUAGE plpgsql;

UPDATE catalog_game SET payload = payload;
