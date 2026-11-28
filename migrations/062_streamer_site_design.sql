ALTER TABLE streamer_site
	ADD COLUMN IF NOT EXISTS design jsonb NOT NULL DEFAULT '{"preset":"midnight","accentColor":"#9147ff","backgroundColor":"#120d1c","backgroundImage":"","themeSwitcher":false}'::jsonb;
