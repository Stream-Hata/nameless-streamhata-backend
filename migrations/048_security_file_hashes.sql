CREATE TABLE IF NOT EXISTS security_file_hashes (
	sha256 text PRIMARY KEY,
	verdict text NOT NULL,
	reason text NOT NULL DEFAULT '',
	source text NOT NULL DEFAULT '',
	checked_at timestamptz NOT NULL DEFAULT now(),
	expires_at timestamptz
);

CREATE INDEX IF NOT EXISTS security_file_hashes_verdict_idx
	ON security_file_hashes (verdict, checked_at DESC);
