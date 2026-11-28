CREATE TABLE widget_access_tokens (
    channel_id bigint PRIMARY KEY,
    token_hash bytea NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now()
);
