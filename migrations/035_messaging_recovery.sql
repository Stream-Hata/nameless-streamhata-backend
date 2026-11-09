CREATE TABLE IF NOT EXISTS streamer_identity_recovery (
	user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
	blob jsonb NOT NULL,
	updated_at timestamptz NOT NULL DEFAULT now()
);
