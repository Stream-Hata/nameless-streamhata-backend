CREATE TABLE IF NOT EXISTS security_incidents (
	id bigserial PRIMARY KEY,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	kind text NOT NULL,
	verdict text NOT NULL,
	severity text NOT NULL DEFAULT 'critical',
	status text NOT NULL DEFAULT 'pending_review',
	chat_type text NOT NULL DEFAULT '',
	chat_id text NOT NULL DEFAULT '',
	message_id bigint,
	sender_id bigint REFERENCES users(id) ON DELETE SET NULL,
	sender_login text NOT NULL DEFAULT '',
	recipient_id bigint REFERENCES users(id) ON DELETE SET NULL,
	file_name text NOT NULL DEFAULT '',
	file_hash text NOT NULL DEFAULT '',
	reason text NOT NULL DEFAULT '',
	report jsonb NOT NULL DEFAULT '{}'::jsonb,
	auto_ban boolean NOT NULL DEFAULT true,
	review_deadline timestamptz NOT NULL,
	auto_ban_at timestamptz,
	acknowledged_at timestamptz,
	acknowledged_by text NOT NULL DEFAULT '',
	reviewed_at timestamptz,
	reviewed_by text NOT NULL DEFAULT '',
	action text NOT NULL DEFAULT '',
	ban_applied boolean NOT NULL DEFAULT false
);

CREATE INDEX IF NOT EXISTS security_incidents_status_idx
	ON security_incidents (status, review_deadline);

CREATE INDEX IF NOT EXISTS security_incidents_sender_idx
	ON security_incidents (sender_id, created_at DESC);

CREATE TABLE IF NOT EXISTS security_alerts (
	id bigserial PRIMARY KEY,
	incident_id bigint NOT NULL REFERENCES security_incidents(id) ON DELETE CASCADE,
	recipient_login text NOT NULL,
	channel text NOT NULL DEFAULT 'in_app',
	status text NOT NULL DEFAULT 'pending',
	created_at timestamptz NOT NULL DEFAULT now(),
	delivered_at timestamptz,
	read_at timestamptz
);

CREATE INDEX IF NOT EXISTS security_alerts_recipient_idx
	ON security_alerts (recipient_login, status, created_at DESC);

CREATE TABLE IF NOT EXISTS security_incident_events (
	id bigserial PRIMARY KEY,
	incident_id bigint NOT NULL REFERENCES security_incidents(id) ON DELETE CASCADE,
	actor text NOT NULL DEFAULT '',
	action text NOT NULL,
	note text NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS security_incident_events_incident_idx
	ON security_incident_events (incident_id, created_at);
