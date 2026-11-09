CREATE TABLE IF NOT EXISTS streamer_conversation_state (
	owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	peer_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	pinned boolean NOT NULL DEFAULT false,
	archived boolean NOT NULL DEFAULT false,
	archived_at timestamptz,
	archive_reason text NOT NULL DEFAULT '',
	hidden_before timestamptz,
	updated_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (owner_id, peer_id)
);
