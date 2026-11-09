ALTER TABLE streamer_identities
	ADD COLUMN IF NOT EXISTS test_owner_id bigint REFERENCES users(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS streamer_identities_test_owner_idx ON streamer_identities (test_owner_id);
