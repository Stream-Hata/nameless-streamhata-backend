ALTER TABLE points_wallet ADD COLUMN channel_id text NOT NULL DEFAULT '';
ALTER TABLE points_wallet DROP CONSTRAINT points_wallet_pkey;
ALTER TABLE points_wallet ADD PRIMARY KEY (channel_id, user_id);

ALTER TABLE points_transaction ADD COLUMN channel_id text NOT NULL DEFAULT '';
ALTER TABLE points_transaction DROP CONSTRAINT points_transaction_pkey;
ALTER TABLE points_transaction ADD PRIMARY KEY (channel_id, id);

ALTER TABLE reward ADD COLUMN channel_id text NOT NULL DEFAULT '';
ALTER TABLE reward DROP CONSTRAINT reward_pkey;
ALTER TABLE reward ADD PRIMARY KEY (channel_id, id);

ALTER TABLE quote ADD COLUMN channel_id text NOT NULL DEFAULT '';
ALTER TABLE quote DROP CONSTRAINT quote_pkey;
ALTER TABLE quote ADD PRIMARY KEY (channel_id, id);

ALTER TABLE redemption_mapping ADD COLUMN channel_id text NOT NULL DEFAULT '';
ALTER TABLE redemption_mapping DROP CONSTRAINT redemption_mapping_pkey;
ALTER TABLE redemption_mapping ADD PRIMARY KEY (channel_id, id);

ALTER TABLE track ADD COLUMN channel_id text NOT NULL DEFAULT '';
ALTER TABLE track DROP CONSTRAINT track_pkey;
ALTER TABLE track ADD PRIMARY KEY (channel_id, video_id);
