CREATE TABLE IF NOT EXISTS game_discussion_likes (
	message_id bigint NOT NULL REFERENCES game_discussions(id) ON DELETE CASCADE,
	user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (message_id, user_id)
);

CREATE INDEX IF NOT EXISTS game_discussion_likes_user_idx
	ON game_discussion_likes (user_id, created_at DESC);
