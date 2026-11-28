CREATE TABLE channel_chat_timer (
    channel_id text NOT NULL,
    id text NOT NULL,
    config jsonb NOT NULL,
    PRIMARY KEY (channel_id, id)
);

CREATE TABLE twitch_chat_activation (
    user_id integer PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    enabled boolean NOT NULL DEFAULT true
);
