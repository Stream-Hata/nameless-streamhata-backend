CREATE TABLE IF NOT EXISTS points_wallet (
	user_id varchar(64) PRIMARY KEY,
	login varchar(100) NOT NULL DEFAULT '',
	display_name varchar(100) NOT NULL DEFAULT '',
	balance int NOT NULL DEFAULT 0,
	total_earned int NOT NULL DEFAULT 0,
	total_spent int NOT NULL DEFAULT 0,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS points_transaction (
	id varchar(64) PRIMARY KEY,
	user_id varchar(64) NOT NULL,
	login varchar(100) NOT NULL DEFAULT '',
	delta int NOT NULL,
	reason varchar(200) NOT NULL DEFAULT '',
	balance_after int NOT NULL DEFAULT 0,
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS points_transaction_user_idx ON points_transaction (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS reward (
	id varchar(64) PRIMARY KEY,
	name varchar(100) NOT NULL,
	cost int NOT NULL,
	response varchar(500) NOT NULL DEFAULT '',
	enabled boolean NOT NULL DEFAULT true
);

CREATE TABLE IF NOT EXISTS quote (
	id varchar(64) PRIMARY KEY,
	text varchar(500) NOT NULL,
	author varchar(100) NOT NULL DEFAULT '',
	added_by varchar(100) NOT NULL DEFAULT '',
	created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS redemption_mapping (
	id varchar(64) PRIMARY KEY,
	reward_title varchar(200) NOT NULL,
	action varchar(30) NOT NULL,
	payload varchar(500) NOT NULL DEFAULT '',
	enabled boolean NOT NULL DEFAULT true
);
