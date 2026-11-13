CREATE TABLE IF NOT EXISTS security_hash_rules (
	id bigserial PRIMARY KEY,
	sha256 text NOT NULL UNIQUE,
	verdict text NOT NULL CHECK (verdict IN ('malicious', 'trusted')),
	reason text NOT NULL DEFAULT '',
	source text NOT NULL DEFAULT 'admin',
	created_by text NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS security_hash_rules_verdict_idx
	ON security_hash_rules (verdict, created_at DESC);
