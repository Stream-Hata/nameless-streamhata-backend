CREATE TABLE IF NOT EXISTS streamer_message_reactions (
	message_id bigint NOT NULL REFERENCES streamer_messages(id) ON DELETE CASCADE,
	user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	emoji text NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (message_id, user_id, emoji)
);

CREATE INDEX IF NOT EXISTS streamer_message_reactions_message_idx
	ON streamer_message_reactions (message_id);
