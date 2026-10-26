CREATE TABLE IF NOT EXISTS game_suggestions (
	id bigserial PRIMARY KEY,
	user_login text NOT NULL,
	user_display_name text NOT NULL DEFAULT '',
	title text NOT NULL,
	platform text NOT NULL DEFAULT '',
	year integer,
	links text NOT NULL DEFAULT '',
	comment text NOT NULL DEFAULT '',
	status text NOT NULL DEFAULT 'pending',
	moderator_login text NOT NULL DEFAULT '',
	moderator_comment text NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS game_suggestions_status_created_idx
	ON game_suggestions (status, created_at DESC);

CREATE INDEX IF NOT EXISTS game_suggestions_user_idx
	ON game_suggestions (lower(user_login), created_at DESC);
