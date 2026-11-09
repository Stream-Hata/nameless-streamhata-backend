CREATE TABLE IF NOT EXISTS status_incident_updates (
	id bigserial PRIMARY KEY,
	incident_id bigint NOT NULL REFERENCES status_incidents(id) ON DELETE CASCADE,
	status varchar(20) NOT NULL DEFAULT '',
	message text NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS status_incident_updates_incident_idx
	ON status_incident_updates (incident_id, created_at DESC);
