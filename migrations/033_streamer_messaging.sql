CREATE TABLE IF NOT EXISTS streamer_identities (
	user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
	login text NOT NULL,
	display_name text NOT NULL DEFAULT '',
	key_id text NOT NULL,
	encryption_key jsonb NOT NULL,
	signing_key jsonb NOT NULL,
	is_test boolean NOT NULL DEFAULT false,
	updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS streamer_identities_login_idx ON streamer_identities (lower(login));

CREATE TABLE IF NOT EXISTS streamer_messages (
	id bigserial PRIMARY KEY,
	sender_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	recipient_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	envelope jsonb NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	read_at timestamptz
);

CREATE INDEX IF NOT EXISTS streamer_messages_pair_idx
	ON streamer_messages (sender_id, recipient_id, created_at DESC);
CREATE INDEX IF NOT EXISTS streamer_messages_unread_idx
	ON streamer_messages (recipient_id, read_at, created_at DESC);

CREATE TABLE IF NOT EXISTS streamer_relationships (
	owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	peer_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	strength integer NOT NULL DEFAULT 0,
	mutual boolean NOT NULL DEFAULT false,
	blocked boolean NOT NULL DEFAULT false,
	last_interaction_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (owner_id, peer_id)
);
