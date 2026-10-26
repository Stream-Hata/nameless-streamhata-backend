CREATE TABLE IF NOT EXISTS catalog_game (
	slug text PRIMARY KEY,
	title text NOT NULL,
	source varchar(50) NOT NULL DEFAULT '',
	payload jsonb NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS catalog_game_title_idx ON catalog_game (lower(title));
