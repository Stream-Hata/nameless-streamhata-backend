CREATE TABLE IF NOT EXISTS status_incidents (
	id bigserial PRIMARY KEY,
	code varchar(40) NOT NULL UNIQUE,
	codename varchar(80) NOT NULL DEFAULT '',
	title text NOT NULL,
	description text NOT NULL DEFAULT '',
	severity varchar(20) NOT NULL DEFAULT 'minor',
	status varchar(20) NOT NULL DEFAULT 'investigating',
	started_at timestamptz NOT NULL DEFAULT now(),
	resolved_at timestamptz,
	updated_at timestamptz NOT NULL DEFAULT now(),
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS status_incidents_started_idx ON status_incidents (started_at DESC);
CREATE INDEX IF NOT EXISTS status_incidents_active_idx ON status_incidents (resolved_at) WHERE resolved_at IS NULL;
