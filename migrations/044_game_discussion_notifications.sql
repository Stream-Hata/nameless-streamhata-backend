CREATE TABLE IF NOT EXISTS game_discussion_notifications (
	id bigserial PRIMARY KEY,
	user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	message_id bigint REFERENCES game_discussions(id) ON DELETE CASCADE,
	game_slug text NOT NULL,
	kind text NOT NULL,
	read_at timestamptz,
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS game_discussion_notifications_user_idx
	ON game_discussion_notifications (user_id, read_at, created_at DESC);
