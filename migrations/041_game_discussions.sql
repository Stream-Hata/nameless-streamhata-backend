CREATE TABLE IF NOT EXISTS game_discussions (
	id bigserial PRIMARY KEY,
	game_slug text NOT NULL,
	user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	message text NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS game_discussions_slug_idx
	ON game_discussions (game_slug, created_at ASC);

CREATE INDEX IF NOT EXISTS game_discussions_user_idx
	ON game_discussions (user_id);
