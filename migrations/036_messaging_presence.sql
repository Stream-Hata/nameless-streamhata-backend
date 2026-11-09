CREATE TABLE IF NOT EXISTS streamer_presence (
	user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
	last_seen timestamptz NOT NULL DEFAULT now(),
	typing_peer text NOT NULL DEFAULT '',
	typing_until timestamptz
);

CREATE INDEX IF NOT EXISTS streamer_presence_seen_idx ON streamer_presence (last_seen);
