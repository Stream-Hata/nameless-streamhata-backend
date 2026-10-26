CREATE TABLE IF NOT EXISTS stream_event (
	id int generated always AS identity primary key,
	title text NOT NULL,
	type text NOT NULL DEFAULT 'custom',
	status text NOT NULL DEFAULT 'planned',
	starts_at timestamptz,
	created_at timestamptz NOT NULL DEFAULT now()
);
