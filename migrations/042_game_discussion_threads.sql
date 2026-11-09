ALTER TABLE game_discussions
	ADD COLUMN IF NOT EXISTS parent_id bigint REFERENCES game_discussions(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS game_discussions_parent_idx
	ON game_discussions (parent_id, created_at ASC);
