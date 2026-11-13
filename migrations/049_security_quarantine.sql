CREATE TABLE IF NOT EXISTS security_quarantine (
	id bigserial PRIMARY KEY,
	incident_id bigint NOT NULL REFERENCES security_incidents(id) ON DELETE CASCADE,
	sha256 text NOT NULL,
	path text NOT NULL,
	size bigint NOT NULL DEFAULT 0,
	created_at timestamptz NOT NULL DEFAULT now(),
	expires_at timestamptz NOT NULL,
	deleted_at timestamptz
);

CREATE INDEX IF NOT EXISTS security_quarantine_expiry_idx
	ON security_quarantine (expires_at)
	WHERE deleted_at IS NULL;
