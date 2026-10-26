ALTER TABLE users
	ADD COLUMN IF NOT EXISTS steam_id varchar(32) NOT NULL DEFAULT '',
	ADD COLUMN IF NOT EXISTS steam_linked_at timestamptz;
