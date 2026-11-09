CREATE TABLE IF NOT EXISTS streamer_groups (
	id bigserial PRIMARY KEY,
	owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	title text NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS streamer_group_members (
	group_id bigint NOT NULL REFERENCES streamer_groups(id) ON DELETE CASCADE,
	user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	role text NOT NULL DEFAULT 'member',
	joined_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (group_id, user_id)
);

CREATE TABLE IF NOT EXISTS streamer_group_keys (
	group_id bigint NOT NULL REFERENCES streamer_groups(id) ON DELETE CASCADE,
	user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	key_id text NOT NULL,
	envelope jsonb NOT NULL,
	updated_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (group_id, user_id)
);

CREATE TABLE IF NOT EXISTS streamer_group_messages (
	id bigserial PRIMARY KEY,
	group_id bigint NOT NULL REFERENCES streamer_groups(id) ON DELETE CASCADE,
	sender_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	envelope jsonb NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS streamer_group_messages_idx
	ON streamer_group_messages (group_id, created_at DESC);
