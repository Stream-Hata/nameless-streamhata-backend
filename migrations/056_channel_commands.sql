ALTER TABLE command ADD COLUMN channel_id text NOT NULL DEFAULT '';
ALTER TABLE command DROP CONSTRAINT command_pkey;
ALTER TABLE command ADD PRIMARY KEY (channel_id, id);
