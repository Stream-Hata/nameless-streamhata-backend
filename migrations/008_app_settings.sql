CREATE TABLE IF NOT EXISTS app_setting (
	key varchar(100) PRIMARY KEY,
	value jsonb NOT NULL DEFAULT '{}'::jsonb
);
