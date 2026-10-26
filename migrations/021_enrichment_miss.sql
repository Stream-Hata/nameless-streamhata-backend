CREATE TABLE IF NOT EXISTS game_enrichment_miss (
	slug text PRIMARY KEY,
	attempts int NOT NULL DEFAULT 1,
	last_error text NOT NULL DEFAULT '',
	updated_at timestamptz NOT NULL DEFAULT now()
);
