CREATE TABLE IF NOT EXISTS feedback_notify_links (
	author varchar(80) PRIMARY KEY,
	telegram_chat_id bigint,
	telegram_username varchar(80) NOT NULL DEFAULT '',
	link_token varchar(128) NOT NULL DEFAULT '',
	token_expires_at timestamptz,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS feedback_notify_links_token_idx
	ON feedback_notify_links (link_token) WHERE link_token <> '';
