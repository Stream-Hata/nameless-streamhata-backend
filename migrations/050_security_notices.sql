CREATE TABLE IF NOT EXISTS security_notices (
	id bigserial PRIMARY KEY,
	incident_id bigint NOT NULL UNIQUE REFERENCES security_incidents(id) ON DELETE CASCADE,
	sender_id bigint REFERENCES users(id) ON DELETE SET NULL,
	recipient_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at timestamptz NOT NULL DEFAULT now(),
	read_at timestamptz
);

CREATE INDEX IF NOT EXISTS security_notices_pair_idx
	ON security_notices (sender_id, recipient_id, created_at DESC);
