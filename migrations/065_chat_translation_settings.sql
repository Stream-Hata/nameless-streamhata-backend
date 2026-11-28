CREATE TABLE IF NOT EXISTS chat_translation_settings (
	channel_id text PRIMARY KEY,
	auto_translation_enabled boolean NOT NULL DEFAULT true,
	updated_at timestamptz NOT NULL DEFAULT now()
);
