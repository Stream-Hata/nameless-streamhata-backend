CREATE TABLE IF NOT EXISTS command (
	id varchar(64) PRIMARY KEY,
	name varchar(50) NOT NULL,
	aliases jsonb NOT NULL DEFAULT '[]'::jsonb,
	response varchar(500) NOT NULL,
	cooldown_seconds int NOT NULL DEFAULT 0,
	user_level varchar(20) NOT NULL DEFAULT 'everyone',
	enabled boolean NOT NULL DEFAULT true,
	counter int NOT NULL DEFAULT 0,
	created_at timestamptz NOT NULL DEFAULT now()
);
