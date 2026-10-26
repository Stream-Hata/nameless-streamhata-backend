CREATE TABLE IF NOT EXISTS game_enrichment (
	slug varchar(255) PRIMARY KEY,
	source varchar(50) NOT NULL,
	source_url text NOT NULL DEFAULT '',
	payload jsonb NOT NULL DEFAULT '{}'::jsonb,
	updated_at timestamptz NOT NULL DEFAULT now()
);
