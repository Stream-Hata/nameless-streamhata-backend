ALTER TABLE streamer_messages
	ADD COLUMN IF NOT EXISTS edited_at timestamptz;
